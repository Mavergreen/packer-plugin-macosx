package media

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zclconf/go-cty/cty"
	"golang.org/x/crypto/ssh"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/lock"
	"github.com/Mavergreen/packer-plugin-macosx/internal/media"
	"github.com/Mavergreen/packer-plugin-macosx/internal/payload"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

func decodeOutput(t *testing.T, v cty.Value) DatasourceOutput {
	t.Helper()
	m := v.AsValueMap()
	get := func(k string) string {
		f, ok := m[k]
		if !ok {
			t.Fatalf("output %#v is missing %q", m, k)
		}
		return f.AsString()
	}
	return DatasourceOutput{
		Path:              get("path"),
		SHA256:            get("sha256"),
		ContentDigest:     get("content_digest"),
		SSHPrivateKeyFile: get("ssh_private_key_file"),
	}
}

func TestConfigureDefaults(t *testing.T) {
	var d Datasource
	if err := d.Configure(map[string]interface{}{"installesd": "/somewhere/InstallESD.dmg"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	c := d.config
	if c.User != "vagrant" || c.AuthorizedKey != "" || !c.OpenSSH.True() || c.Updates != "security" ||
		c.ExtraSpaceMiB != 0 || c.PrivopsTimeout != privops.DefaultTimeout || c.CacheDir != "" {
		t.Fatalf("defaults = %+v", c)
	}

	var set Datasource
	if err := set.Configure(map[string]interface{}{
		"installesd": "/esd", "user": "alice", "authorized_key": "/k.pub", "openssh": false,
		"updates": "all", "extra_space_mib": 100, "privops_timeout": "30m", "cache_dir": "/c",
	}); err != nil {
		t.Fatalf("Configure(everything set): %v", err)
	}
	c = set.config
	if c.User != "alice" || c.AuthorizedKey != "/k.pub" || !c.OpenSSH.False() || c.Updates != "all" ||
		c.ExtraSpaceMiB != 100 || c.PrivopsTimeout != 30*time.Minute || c.CacheDir != "/c" {
		t.Fatalf("config = %+v", c)
	}
}

func TestConfigureRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]interface{}
		want string
	}{
		{"a bad updates value", map[string]interface{}{"installesd": "/esd", "updates": "some"}, `updates "some": choose one of none, security, all`},
		{"no installesd", map[string]interface{}{}, "installesd is required"},
		{"a bad user", map[string]interface{}{"installesd": "/esd", "user": "bob smith"}, "not a usable account name"},
		// sudo's #includedir skips a sudoers.d file named with a dot, and
		// the first boot names the fragment after the user.
		{"a user with a dot", map[string]interface{}{"installesd": "/esd", "user": "first.last"}, "no dot"},
		{"negative extra space", map[string]interface{}{"installesd": "/esd", "extra_space_mib": -1}, "extra_space_mib"},
		{"a negative timeout", map[string]interface{}{"installesd": "/esd", "privops_timeout": "-1m"}, "privops_timeout"},
		{"an unknown key", map[string]interface{}{"installesd": "/esd", "bogus": "y"}, "bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d Datasource
			err := d.Configure(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Configure(%v) = %v, want it to say %q", tc.raw, err, tc.want)
			}
		})
	}
}

// The update sources a fake registry pins, in "all"'s install order
// (fetch's updateSources).
var updateNames = []string{
	"apple-secupd-2016-004",
	"apple-safari-9.1.3",
	"apple-itunes-12.6.2-corefp",
	"apple-itunes-12.6.2-mobiledevice",
	"apple-itunes-12.6.2-itunesaccess",
	"apple-itunes-12.6.2-itunesx",
	"apple-itunes-12.6.2-coreadi",
}

// world is one test's fake host: an httptest server standing in for the
// OpenSSH releases and Apple's update downloads (flat packages, by their
// xar magic), a registry pinning the updates and a fake ESD to those
// bytes' real sha256s (fetch's own verification is never mocked), and
// fakes for the host check, the payload build and the media builder.
type world struct {
	t        *testing.T
	cacheDir string
	esd      string
	requests int32
	sshReqs  int32 // of those, for the OpenSSH release
	sumsReqs int32 // of those, for its SHA256SUMS

	mu      sync.Mutex
	content map[string][]byte // what the server serves, by path

	payloadCalls int32
	payloadCfg   payload.Config
	payloadOut   string
	payloadKey   []byte // the authorized key bytes the payload read

	mb *fakeMedia
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, cacheDir: t.TempDir()}

	content := map[string][]byte{}
	w.content = content
	var rows []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w.requests, 1)
		if strings.HasPrefix(r.URL.Path, "/openssh/") {
			atomic.AddInt32(&w.sshReqs, 1)
			if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
				atomic.AddInt32(&w.sumsReqs, 1)
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		b, ok := content[r.URL.Path]
		if !ok {
			// /<tag>/SHA256SUMS and the packages it names, for any tag.
			parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/openssh/"), "/", 2)
			if len(parts) == 2 {
				b, ok = content["/openssh/TAG/"+parts[1]]
			}
		}
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		rw.Write(b)
	}))
	t.Cleanup(srv.Close)

	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	var sums strings.Builder
	for _, n := range []string{"openssh-10.5p1.pkg", "openssh-10.5p1-System-Replace.pkg"} {
		b := []byte("xar!" + n)
		content["/openssh/TAG/"+n] = b
		fmt.Fprintf(&sums, "%s  %s\n", sum(b), n)
	}
	content["/openssh/TAG/SHA256SUMS"] = []byte(sums.String())
	for i, n := range updateNames {
		b := []byte("xar!" + n)
		path := fmt.Sprintf("/apple/Update%d-%s.pkg", i+1, n)
		content[path] = b
		rows = append(rows, n+"\t"+srv.URL+path+"\t"+sum(b))
	}

	esd := []byte("not really InstallESD.dmg")
	w.esd = filepath.Join(t.TempDir(), "InstallESD.dmg")
	if err := os.WriteFile(w.esd, esd, 0o644); err != nil {
		t.Fatal(err)
	}
	rows = append(rows, fetch.ESDSource+"\thttp://example.invalid/InstallESD.dmg\t"+sum(esd))
	reg, err := pins.Parse(strings.NewReader(strings.Join(rows, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}

	w.mb = &fakeMedia{t: t}
	saved := []any{hostCheck, loadRegistry, openSSHReleases, buildPayload, newMediaBuilder}
	t.Cleanup(func() {
		hostCheck = saved[0].(func() error)
		loadRegistry = saved[1].(func() (*pins.Registry, error))
		openSSHReleases = saved[2].(string)
		buildPayload = saved[3].(func(context.Context, proc.Runner, payload.Config, string, func(string, ...any)) (string, error))
		newMediaBuilder = saved[4].(func(config.Paths, time.Duration) (mediaBuilder, error))
	})
	hostCheck = func() error { return nil }
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	openSSHReleases = srv.URL + "/openssh"
	buildPayload = func(_ context.Context, _ proc.Runner, c payload.Config, out string, _ func(string, ...any)) (string, error) {
		atomic.AddInt32(&w.payloadCalls, 1)
		w.payloadCfg, w.payloadOut = c, out
		// What payload.Build would do with the key: read it, then.
		key, err := os.ReadFile(c.SSHKey)
		if err != nil {
			return "", err
		}
		w.payloadKey = key
		return "", os.WriteFile(out, []byte("xar!payload"), 0o644)
	}
	newMediaBuilder = func(p config.Paths, timeout time.Duration) (mediaBuilder, error) {
		w.mb.paths, w.mb.timeout = p, timeout
		return w.mb, nil
	}
	return w
}

func (w *world) configure(raw map[string]interface{}) *Datasource {
	w.t.Helper()
	full := map[string]interface{}{"installesd": w.esd, "cache_dir": w.cacheDir}
	for k, v := range raw {
		full[k] = v
	}
	var d Datasource
	if err := d.Configure(full); err != nil {
		w.t.Fatal(err)
	}
	return &d
}

// fakeMedia is a media builder that builds nothing: Build writes a few
// bytes and a sidecar where the real one would, in its scratch home,
// after checking every package it is given is there.
type fakeMedia struct {
	t       *testing.T
	paths   config.Paths
	timeout time.Duration

	preflightErr error
	validated    []media.Options
	builds       int32
	built        media.Options
	esd          string
	digestOf     string
}

func (f *fakeMedia) Validate(o media.Options) error {
	f.validated = append(f.validated, o)
	return nil
}

func (f *fakeMedia) Preflight() error { return f.preflightErr }

func (f *fakeMedia) Build(_ context.Context, esd string, o media.Options) (string, error) {
	atomic.AddInt32(&f.builds, 1)
	f.built, f.esd = o, esd
	for _, p := range append([]string{o.FirstbootPkg}, o.ExtraPkgs...) {
		if ok, err := fetch.HasXarMagic(p); err != nil || !ok {
			f.t.Errorf("Build given %s, not a flat package (%v)", p, err)
		}
	}
	out := f.paths.InstallerMedia()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, []byte("media bytes"), 0o644); err != nil {
		return "", err
	}
	s := sha256.Sum256([]byte("media bytes"))
	side := fmt.Sprintf("# sha256 of installer-media.img as built\n%s  installer-media.img\n", hex.EncodeToString(s[:]))
	return out, os.WriteFile(out+".sha256", []byte(side), 0o644)
}

const fakeDigest = "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1"

func (f *fakeMedia) ContentDigest(_ context.Context, img string, _ io.Writer) (media.Digest, error) {
	f.digestOf = img
	if _, err := os.Stat(img); err != nil {
		return media.Digest{}, err
	}
	return media.Digest{SHA256: fakeDigest, Files: 3, Bytes: 11}, nil
}

func TestExecuteBuildsOnceAndReuses(t *testing.T) {
	w := newWorld(t)
	d := w.configure(map[string]interface{}{"extra_space_mib": 7, "privops_timeout": "20m"})

	v, err := d.Execute()
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	out := decodeOutput(t, v)
	dir := filepath.Dir(out.Path)

	if filepath.Base(out.Path) != "installer-media.img" || filepath.Dir(dir) != filepath.Join(w.cacheDir, "media") {
		t.Fatalf("path = %s, want <cache_dir>/media/<digest>/installer-media.img", out.Path)
	}
	if b, err := os.ReadFile(out.Path); err != nil || string(b) != "media bytes" {
		t.Fatalf("the media in the store = %q, %v", b, err)
	}
	s := sha256.Sum256([]byte("media bytes"))
	if out.SHA256 != hex.EncodeToString(s[:]) {
		t.Fatalf("sha256 = %s, want the sidecar's", out.SHA256)
	}
	if out.ContentDigest != fakeDigest {
		t.Fatalf("content_digest = %s", out.ContentDigest)
	}
	// Taken inside make, of the entry's own media, before the store
	// renames the entry into place.
	if filepath.Base(w.mb.digestOf) != "installer-media.img" ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(w.mb.digestOf)), ".store-tmp-"+filepath.Base(dir)) {
		t.Fatalf("the content digest was taken of %s, want the entry's installer-media.img", w.mb.digestOf)
	}

	// Vagrant's insecure key: the private half, 0600, in the entry.
	if out.SSHPrivateKeyFile != filepath.Join(dir, "vagrant_insecure_key") {
		t.Fatalf("ssh_private_key_file = %s", out.SSHPrivateKeyFile)
	}
	fi, err := os.Stat(out.SSHPrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(out.SSHPrivateKeyFile); string(b) != string(payload.VagrantPrivateKey()) {
		t.Fatal("the private key file is not Vagrant's insecure key")
	}

	// The payload: Vagrant's account, Vagrant's public key written into
	// the entry, OpenSSH, the security update by its staged name.
	pc := w.payloadCfg
	if pc.User != "vagrant" || pc.RealName != "Vagrant" || !pc.Sudo || pc.Updates != "security" {
		t.Fatalf("payload config = %+v", pc)
	}
	if filepath.Base(pc.SSHKey) != "vagrant_insecure_key.pub" {
		t.Fatalf("payload SSHKey = %s, want the entry's vagrant_insecure_key.pub", pc.SSHKey)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "vagrant_insecure_key.pub")); string(b) != string(payload.VagrantPublicKey()) {
		t.Fatal("the entry's public key is not Vagrant's")
	}
	tag, _ := fetch.OpenSSHTag()
	if pc.OpenSSHTag != tag || len(pc.OpenSSHPkgs) != 2 {
		t.Fatalf("payload OpenSSH = %s %v", pc.OpenSSHTag, pc.OpenSSHPkgs)
	}
	if filepath.Base(w.payloadOut) != "mqg-firstboot.pkg" {
		t.Fatalf("payload built at %s", w.payloadOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "mqg-firstboot.pkg")); err != nil {
		t.Fatalf("the payload is not in the entry: %v", err)
	}

	// The downloads are in the ONE shared cache, not the entry.
	for _, p := range append(append([]string{}, pc.OpenSSHPkgs...), pc.UpdatePkgs[0].Path) {
		if !strings.HasPrefix(p, filepath.Join(w.cacheDir, "cache")+string(filepath.Separator)) {
			t.Errorf("%s was not fetched into <cache_dir>/cache", p)
		}
	}

	// The media: forced, with the hooks, the payload, the OpenSSH
	// packages then the staged update, room for the update plus the
	// configured extra, built from the given ESD in the scratch home.
	o := w.mb.built
	if !o.Force || !o.Autoinstall || filepath.Base(o.FirstbootPkg) != "mqg-firstboot.pkg" {
		t.Fatalf("media options = %+v", o)
	}
	wantExtra := []string{pc.OpenSSHPkgs[0], pc.OpenSSHPkgs[1], "mqg-update-01-"}
	if len(o.ExtraPkgs) != 3 || o.ExtraPkgs[0] != wantExtra[0] || o.ExtraPkgs[1] != wantExtra[1] ||
		!strings.HasPrefix(filepath.Base(o.ExtraPkgs[2]), wantExtra[2]) {
		t.Fatalf("ExtraPkgs = %v", o.ExtraPkgs)
	}
	mib, err := media.UpdatesExtraMiB([]string{pc.UpdatePkgs[0].Path})
	if err != nil {
		t.Fatal(err)
	}
	if o.ExtraSpaceMiB != mib+7 {
		t.Fatalf("ExtraSpaceMiB = %d, want %d + 7", o.ExtraSpaceMiB, mib)
	}
	if w.mb.esd != w.esd {
		t.Fatalf("built from %s, want %s", w.mb.esd, w.esd)
	}
	if w.mb.paths.Home != filepath.Join(w.cacheDir, "media-build") || w.mb.timeout != 20*time.Minute {
		t.Fatalf("builder home %s, timeout %v", w.mb.paths.Home, w.mb.timeout)
	}
	if _, err := os.Stat(w.mb.paths.InstallerMedia()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the media was left in the scratch home: %v", err)
	}
	if len(w.mb.validated) == 0 || w.mb.validated[0].ExtraSpaceMiB != 7 {
		t.Fatalf("Validate was not asked about the extra space first: %+v", w.mb.validated)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "inputs")); err != nil || !strings.Contains(string(b), "sshkey\tSHA256:") {
		t.Fatalf("the entry's listing = %q, %v", b, err)
	}

	requests := atomic.LoadInt32(&w.requests)
	v2, err := d.Execute()
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if out2 := decodeOutput(t, v2); out2 != out {
		t.Fatalf("second Execute = %+v, want %+v", out2, out)
	}
	if n := atomic.LoadInt32(&w.mb.builds); n != 1 {
		t.Fatalf("Build ran %d times, want 1: the store must be reused", n)
	}
	if n := atomic.LoadInt32(&w.payloadCalls); n != 1 {
		t.Fatalf("the payload was built %d times, want 1", n)
	}
	if n := atomic.LoadInt32(&w.requests); n != requests {
		t.Fatalf("%d requests after the reuse, want still %d", n, requests)
	}
}

// TestTheStagedNamesAreFetchStagedName: every update is staged in the
// entry's updates/ as fetch.StagedName names it, in install order,
// pointing at the shared cache; the payload names each by that name and
// the media carries exactly those links, after the OpenSSH packages.
func TestTheStagedNamesAreFetchStagedName(t *testing.T) {
	w := newWorld(t)
	d := w.configure(map[string]interface{}{"updates": "all"})
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(decodeOutput(t, v).Path)

	ups := w.payloadCfg.UpdatePkgs
	if len(ups) != len(updateNames) {
		t.Fatalf("%d update packages, want %d", len(ups), len(updateNames))
	}
	var wantLinks []string
	for i, u := range ups {
		want := fetch.StagedName(i+1, u.Path)
		if u.Name != want {
			t.Errorf("update %d is named %s in the payload, want %s", i+1, u.Name, want)
		}
		if !strings.Contains(u.Path, updateNames[i]) {
			t.Errorf("update %d is %s, want %s (install order)", i+1, u.Path, updateNames[i])
		}
		link := filepath.Join(dir, "updates", want)
		target, err := os.Readlink(link)
		if err != nil || target != u.Path {
			t.Errorf("%s -> %s (%v), want -> %s", link, target, err, u.Path)
		}
		// Build was given the link in the temporary entry the store then
		// renamed into place: compare by name within updates/.
		wantLinks = append(wantLinks, filepath.Join("updates", want))
	}
	extra := w.mb.built.ExtraPkgs
	if len(extra) != 2+len(wantLinks) {
		t.Fatalf("ExtraPkgs = %v", extra)
	}
	var got []string
	for _, e := range extra[2:] {
		got = append(got, filepath.Join(filepath.Base(filepath.Dir(e)), filepath.Base(e)))
	}
	if !reflect.DeepEqual(got, wantLinks) {
		t.Fatalf("the media's update packages = %v, want %v", got, wantLinks)
	}
}

func TestAUserSuppliedKeyOutputsNoPrivateKey(t *testing.T) {
	w := newWorld(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(keyFile, ssh.MarshalAuthorizedKey(sp), 0o644); err != nil {
		t.Fatal(err)
	}

	d := w.configure(map[string]interface{}{"authorized_key": keyFile, "user": "alice", "updates": "none"})
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	out := decodeOutput(t, v)
	if out.SSHPrivateKeyFile != "" {
		t.Fatalf("ssh_private_key_file = %q, want \"\" with a user-supplied key", out.SSHPrivateKeyFile)
	}
	dir := filepath.Dir(out.Path)
	for _, n := range []string{"vagrant_insecure_key", "vagrant_insecure_key.pub"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is in the entry with a user-supplied key: %v", n, err)
		}
	}
	if w.payloadCfg.User != "alice" || len(w.payloadCfg.UpdatePkgs) != 0 {
		t.Fatalf("payload config = %+v", w.payloadCfg)
	}
	// A custom user's full name is its own, not Vagrant's.
	if w.payloadCfg.RealName != "alice" {
		t.Fatalf("payload RealName = %q, want alice", w.payloadCfg.RealName)
	}
	// The payload reads the bytes the listing hashed, written into the
	// entry -- never the user's file a second time.
	if filepath.Base(w.payloadCfg.SSHKey) != "authorized_key.pub" || strings.HasPrefix(w.payloadCfg.SSHKey, filepath.Dir(keyFile)) {
		t.Fatalf("payload SSHKey = %s, want the entry's authorized_key.pub", w.payloadCfg.SSHKey)
	}
	if kb, _ := os.ReadFile(filepath.Join(dir, "authorized_key.pub")); string(kb) != string(ssh.MarshalAuthorizedKey(sp)) {
		t.Fatalf("the entry's authorized_key.pub = %q", kb)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "inputs"))
	keySum := sha256.Sum256(ssh.MarshalAuthorizedKey(sp))
	for _, row := range []string{
		"sshkey\t" + ssh.FingerprintSHA256(sp) + "\n",
		"sshkey-sha256\t" + hex.EncodeToString(keySum[:]) + "\n",
		"sshkey-vagrant-insecure\t0\n",
	} {
		if !strings.Contains(string(b), row) {
			t.Fatalf("the listing does not carry %q:\n%s", row, b)
		}
	}

	// The default key is another listing, so another entry -- one with
	// the private key in it.
	w2 := w.configure(map[string]interface{}{"user": "alice", "updates": "none"})
	v2, err := w2.Execute()
	if err != nil {
		t.Fatal(err)
	}
	out2 := decodeOutput(t, v2)
	if filepath.Dir(out2.Path) == dir || out2.SSHPrivateKeyFile == "" {
		t.Fatalf("the default key reused the user-supplied key's entry: %+v", out2)
	}
}

func TestOpenSSHOffFetchesNoOpenSSH(t *testing.T) {
	w := newWorld(t)
	d := w.configure(map[string]interface{}{"openssh": false})
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	// No package rows: the listing names no OpenSSH asset.
	if rows := listingRows(t, v, "openssh:"); len(rows) != 0 {
		t.Fatalf("the listing names OpenSSH assets with openssh = false: %v", rows)
	}
	if len(w.payloadCfg.OpenSSHPkgs) != 0 || w.payloadCfg.OpenSSHTag != "" {
		t.Fatalf("payload config = %+v", w.payloadCfg)
	}
	extra := w.mb.built.ExtraPkgs
	if len(extra) != 1 || !strings.HasPrefix(filepath.Base(extra[0]), "mqg-update-01-") {
		t.Fatalf("ExtraPkgs = %v, want the one staged update", extra)
	}
	if n := atomic.LoadInt32(&w.sshReqs); n != 0 {
		t.Fatalf("%d requests for the OpenSSH release with openssh = false", n)
	}
}

func TestAFailingHostCheckStopsEverything(t *testing.T) {
	w := newWorld(t)
	hostCheck = func() error { return errors.New("kvm-device: /dev/kvm does not exist") }
	newMediaBuilder = func(config.Paths, time.Duration) (mediaBuilder, error) {
		t.Fatal("the media builder was made on a host that failed the check")
		return nil, nil
	}
	d := w.configure(nil)
	if _, err := d.Execute(); err == nil || !strings.Contains(err.Error(), "/dev/kvm does not exist") {
		t.Fatalf("Execute = %v", err)
	}
	if n := atomic.LoadInt32(&w.requests); n != 0 {
		t.Fatalf("%d requests after a failed host check", n)
	}
}

// TestAFailingPreflightFetchesNothing: a host that cannot build media
// downloads nothing. The OpenSSH release's SHA256SUMS is the one
// exception: the listing names the packages it lists, so on a cold cache
// it is read before the store is asked.
func TestAFailingPreflightFetchesNothing(t *testing.T) {
	w := newWorld(t)
	w.mb.preflightErr = errors.New("the media build needs dmg2img (not on PATH)")
	d := w.configure(nil)
	if _, err := d.Execute(); err == nil || !strings.Contains(err.Error(), "dmg2img") {
		t.Fatalf("Execute = %v", err)
	}
	n := atomic.LoadInt32(&w.requests) - atomic.LoadInt32(&w.sumsReqs)
	if n != 0 || w.payloadCalls != 0 {
		t.Fatalf("%d requests besides SHA256SUMS, %d payload builds after a failed preflight", n, w.payloadCalls)
	}
}

func TestAnESDThatIsNotThePinnedOneIsRefused(t *testing.T) {
	w := newWorld(t)
	if err := os.WriteFile(w.esd, []byte("some other file"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := w.configure(nil)
	_, err := d.Execute()
	if err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("Execute = %v", err)
	}
	if w.mb.builds != 0 {
		t.Fatal("Build ran on an ESD that is not the pinned one")
	}
	if ents, _ := os.ReadDir(filepath.Join(w.cacheDir, "media")); len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("a failed build left %v in the store", names)
	}
}

// TestAConcurrentBuildInTheScratchHomeIsRefused: the scratch home is
// shared by every listing, so while another build holds it a second is
// refused, naming the holder, and succeeds once it is released.
func TestAConcurrentBuildInTheScratchHomeIsRefused(t *testing.T) {
	w := newWorld(t)
	l, err := lock.Acquire(filepath.Join(w.cacheDir, "media-build.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	d := w.configure(nil)
	_, err = d.Execute()
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("Execute while the scratch home is held = %v", err)
	}
	if w.mb.builds != 0 {
		t.Fatal("Build ran while the scratch home was held elsewhere")
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Execute(); err != nil {
		t.Fatalf("Execute after the release: %v", err)
	}
}

// TestAKeysOptionsAndCommentAreInputs: the payload embeds the
// authorized_key file verbatim, so a change to a line's options or its
// comment -- same key, same fingerprint -- is a changed input: a new
// entry, whose payload was built from the new bytes.
func TestAKeysOptionsAndCommentAreInputs(t *testing.T) {
	w := newWorld(t)
	keyFile := filepath.Join(t.TempDir(), "id_rsa.pub")
	vagrant := strings.TrimSpace(string(payload.VagrantPublicKey()))
	fields := strings.Fields(vagrant)
	line := fields[0] + " " + fields[1]

	var dirs []string
	for _, text := range []string{
		line + " one comment\n",
		line + " another comment\n",
		`from="10.0.2.2" ` + line + " another comment\n",
	} {
		if err := os.WriteFile(keyFile, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		d := w.configure(map[string]interface{}{"authorized_key": keyFile, "updates": "none"})
		v, err := d.Execute()
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, filepath.Dir(decodeOutput(t, v).Path))
		if string(w.payloadKey) != text {
			t.Fatalf("the payload read %q, want %q", w.payloadKey, text)
		}
	}
	if dirs[0] == dirs[1] || dirs[1] == dirs[2] || dirs[0] == dirs[2] {
		t.Fatalf("a changed comment or options field reused an entry: %v", dirs)
	}
	if n := atomic.LoadInt32(&w.payloadCalls); n != 3 {
		t.Fatalf("the payload was built %d times, want 3", n)
	}
}

// TestAStoreHitNeedsNoBuildTools: reusing an entry needs neither the
// media builder nor its tools (dmg2img, mkfs.hfsplus, the microVM's
// kernel); only a store miss asks for them.
func TestAStoreHitNeedsNoBuildTools(t *testing.T) {
	w := newWorld(t)
	d := w.configure(nil)
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	w.mb.preflightErr = errors.New("the media build needs dmg2img (not on PATH)")
	newMediaBuilder = func(config.Paths, time.Duration) (mediaBuilder, error) {
		t.Fatal("the media builder was made for an entry already in the store")
		return nil, nil
	}
	v2, err := d.Execute()
	if err != nil {
		t.Fatalf("a store hit asked for build tools: %v", err)
	}
	if decodeOutput(t, v2) != decodeOutput(t, v) {
		t.Fatal("the store hit gave a different output")
	}
}

// listingRows is the entry's inputs rows that begin with prefix.
func listingRows(t *testing.T, v cty.Value, prefix string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(decodeOutput(t, v).Path), "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			rows = append(rows, l)
		}
	}
	return rows
}

// TestTheListingNamesTheOpenSSHAssets: the tag is pinned, but the
// release's SHA256SUMS -- which names the two packages and their sha256s
// -- is not. The listing carries both packages by name and sha256, and a
// release whose SUMS says otherwise is a different entry.
func TestTheListingNamesTheOpenSSHAssets(t *testing.T) {
	w := newWorld(t)
	d := w.configure(nil)
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	want := []string{
		"openssh:openssh-10.5p1-System-Replace.pkg\t" + sum([]byte("xar!openssh-10.5p1-System-Replace.pkg")),
		"openssh:openssh-10.5p1.pkg\t" + sum([]byte("xar!openssh-10.5p1.pkg")),
	}
	if got := listingRows(t, v, "openssh:"); !slices.Equal(got, want) {
		t.Fatalf("the listing's OpenSSH rows = %q, want %q", got, want)
	}

	// The release, re-rolled under the same tag: new package bytes, and
	// a SUMS that says so. With the cached SUMS gone, the next Execute
	// reads the new one, lists the new sha256, and builds a new entry
	// from the new package.
	w.mu.Lock()
	rerolled := []byte("xar!openssh-10.5p1.pkg, re-rolled")
	w.content["/openssh/TAG/openssh-10.5p1.pkg"] = rerolled
	w.content["/openssh/TAG/SHA256SUMS"] = []byte(fmt.Sprintf("%s  openssh-10.5p1.pkg\n%s  openssh-10.5p1-System-Replace.pkg\n",
		sum(rerolled), sum([]byte("xar!openssh-10.5p1-System-Replace.pkg"))))
	w.mu.Unlock()
	tag, err := fetch.OpenSSHTag()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove((config.Paths{Home: w.cacheDir}).OpenSSHSums(tag)); err != nil {
		t.Fatal(err)
	}
	v2, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(decodeOutput(t, v2).Path) == filepath.Dir(decodeOutput(t, v).Path) {
		t.Fatal("a release whose SUMS names other packages reused the entry")
	}
	if got := listingRows(t, v2, "openssh:openssh-10.5p1.pkg\t"); len(got) != 1 || !strings.HasSuffix(got[0], sum(rerolled)) {
		t.Fatalf("the re-rolled listing's base row = %q, want its sha256 %s", got, sum(rerolled))
	}
	if b, err := os.ReadFile(w.payloadCfg.OpenSSHPkgs[0]); err != nil || string(b) != string(rerolled) {
		t.Fatalf("the new entry's payload has base package %q (%v), want the re-rolled one", b, err)
	}
}

func TestTheListingCarriesTheRecipe(t *testing.T) {
	w := newWorld(t)
	v, err := w.configure(nil).Execute()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(decodeOutput(t, v).Path), "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{"recipe\t" + recipe + "\n", "privops:assemble.sh\t", "privops:content-digest.sh\t"} {
		if !strings.Contains(string(b), row) {
			t.Errorf("the listing does not carry %q:\n%s", row, b)
		}
	}
}
