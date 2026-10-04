// Package media is the mavericks-media data source: the installer media
// -- Apple's InstallESD.dmg converted, the HFS+ volume assembled in the
// privops microVM, the unattended-install hooks and the first-boot
// payload injected, Apple's package checksums verified -- built once
// into internal/store and reused on every later Execute whose listing
// (the payload's and the media's inputs, the authorized key, the OpenSSH
// and updates choices, the pinned ESD) hasn't changed.
package media

//go:generate packer-sdc mapstructure-to-hcl2 -type Config,DatasourceOutput -output datasource.hcl2spec.go

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/hcl2helper"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"
	"golang.org/x/crypto/ssh"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/hostcheck"
	"github.com/Mavergreen/packer-plugin-macosx/internal/inputs"
	"github.com/Mavergreen/packer-plugin-macosx/internal/lock"
	"github.com/Mavergreen/packer-plugin-macosx/internal/media"
	"github.com/Mavergreen/packer-plugin-macosx/internal/payload"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
	"github.com/Mavergreen/packer-plugin-macosx/internal/store"
)

// Config is mavericks-media's HCL configuration.
type Config struct {
	// InstallESD is the path of Apple's InstallESD.dmg -- the template
	// passes data.macosx-mavericks-installesd.esd.path. Required. It is checked
	// against the repository's pinned sha256 before a build uses it.
	InstallESD string `mapstructure:"installesd"`
	// User is the guest account the first-boot payload creates, with
	// passwordless sudo. Default "vagrant".
	User string `mapstructure:"user"`
	// AuthorizedKey is the path of an SSH public key file the payload
	// authorizes for User. "" means Vagrant's own insecure key, whose
	// private half this data source then outputs as ssh_private_key_file.
	AuthorizedKey string `mapstructure:"authorized_key"`
	// OpenSSH carries the family's OpenSSH packages, which the guest
	// installs at first boot in place of 10.9's OpenSSH 6.2. Default true.
	OpenSSH configHelper.Trilean `mapstructure:"openssh"`
	// Updates is which of Apple's post-10.9.5 updates the media carries:
	// none, security or all. Default security.
	Updates string `mapstructure:"updates"`
	// ExtraSpaceMiB enlarges the media's partition beyond what the
	// updates already bring with them. Default 0.
	ExtraSpaceMiB int `mapstructure:"extra_space_mib"`
	// PrivopsTimeout bounds one privops microVM pass. Default 15m.
	PrivopsTimeout time.Duration `mapstructure:"privops_timeout"`
	// CacheDir names where the built store -- and, under it, the shared
	// download cache and the media build's scratch home -- live. ""
	// means packer.CachePath("mavericks").
	CacheDir string `mapstructure:"cache_dir"`
}

// DatasourceOutput is Execute's result.
type DatasourceOutput struct {
	// Path is the installer media in the store.
	Path string `mapstructure:"path"`
	// SHA256 is the media's own sha256, as built. A read-write mount
	// changes the file, so the template attaches it with snapshot=on.
	SHA256 string `mapstructure:"sha256"`
	// ContentDigest is media.Digest's sha256: what is ON the media, which
	// two builds from the same inputs agree on though their files never
	// will.
	ContentDigest string `mapstructure:"content_digest"`
	// SSHPrivateKeyFile is, with the default authorized key, Vagrant's
	// insecure private key in the store (mode 0600), for the build's own
	// SSH login; with a user-supplied authorized_key it is "", and the
	// template takes the private key from a variable.
	SSHPrivateKeyFile string `mapstructure:"ssh_private_key_file"`
}

// recipe is a row in every listing, standing for the Go that shapes this
// data source's output and that no other row names: internal/payload
// (Conf and its header, VagrantAccount's account, Postinstall's
// assembly, the flat-package writer), internal/media (the build, the
// injection of hooks and packages, the content digest), internal/diskimg,
// and make below. The embedded files that code reads are listed by their
// own rows already (payload:, autoinstall:, privops:); the Go is not, and
// a plugin upgrade that changes it must not reuse media an older plugin
// built. Bump its number whenever that code changes what an entry holds.
// The goldens part is the digest of that code's goldens (recipe_test.go's
// recipeGoldens), and TestRecipePinsTheGoldens fails when they change and
// this does not.
const recipe = "3 goldens:2850b5fe303d62c0"

// Datasource is mavericks-media.
type Datasource struct {
	config Config
}

var _ packersdk.Datasource = new(Datasource)

// The store entry's files.
const (
	mediaName     = "installer-media.img"
	payloadName   = media.FirstbootPkgName
	digestName    = "content-digest"
	privateKey    = "vagrant_insecure_key"
	publicKey     = "vagrant_insecure_key.pub"
	userKey       = "authorized_key.pub"
	updatesSubdir = "updates"
)

// userName is what Configure accepts for user: a macOS short name's
// characters, starting with a letter or underscore -- but no dot. The
// first boot writes the account's sudoers fragment as
// /etc/sudoers.d/<user>, and sudo's #includedir skips any file name with
// a dot in it, so "first.last" would get a fragment sudo never reads: no
// passwordless sudo, and a build that hangs at its shutdown_command.
var userName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return d.config.FlatMapstructure().HCL2Spec()
}

// Configure decodes and checks the configuration, and fills in the
// defaults. It reads nothing from the host: packer validate runs it
// where the ESD and the key file may not exist yet.
func (d *Datasource) Configure(raws ...interface{}) error {
	if err := configHelper.Decode(&d.config, nil, raws...); err != nil {
		return err
	}
	c := &d.config
	if c.User == "" {
		c.User = "vagrant"
	}
	if c.OpenSSH == configHelper.TriUnset {
		c.OpenSSH = configHelper.TriTrue
	}
	if c.Updates == "" {
		c.Updates = config.DefaultUpdates
	}
	if c.PrivopsTimeout == 0 {
		c.PrivopsTimeout = privops.DefaultTimeout
	}

	var errs []error
	if c.InstallESD == "" {
		errs = append(errs, errors.New("installesd is required: the path of InstallESD.dmg (data.macosx-mavericks-installesd.<name>.path)"))
	}
	if !userName.MatchString(c.User) {
		errs = append(errs, fmt.Errorf("user %q is not a usable account name (letters, digits, underscore, dash; not starting with a digit or dash; no dot, since sudo ignores a sudoers.d file named with one)", c.User))
	}
	if !slices.Contains(config.UpdateChoices, c.Updates) {
		errs = append(errs, fmt.Errorf("updates %q: choose one of %s", c.Updates, strings.Join(config.UpdateChoices, ", ")))
	}
	if c.ExtraSpaceMiB < 0 {
		errs = append(errs, fmt.Errorf("extra_space_mib wants a whole number of MiB, not %d", c.ExtraSpaceMiB))
	}
	if c.PrivopsTimeout < 0 {
		errs = append(errs, fmt.Errorf("privops_timeout wants a positive duration, such as 30m, not %v", c.PrivopsTimeout))
	}
	return errors.Join(errs...)
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(DatasourceOutput).FlatMapstructure().HCL2Spec()
}

// mediaBuilder is what Execute asks of *media.Builder: the seam a test
// replaces, so that no test boots a microVM or converts real media.
type mediaBuilder interface {
	Validate(o media.Options) error
	Preflight() error
	Build(ctx context.Context, esd string, o media.Options) (string, error)
	ContentDigest(ctx context.Context, img string, listing io.Writer) (media.Digest, error)
}

// Test seams. Production checks this host, reads the registry this
// binary was built with, fetches OpenSSH from its real releases, builds
// the payload with internal/payload and the media on this host's
// privops microVM.
var (
	hostCheck       = hostcheck.Check
	loadRegistry    = pins.Embedded
	openSSHReleases = fetch.DefaultOpenSSHReleases
	buildPayload    = payload.Build
	newMediaBuilder = func(p config.Paths, timeout time.Duration) (mediaBuilder, error) {
		be, err := privops.NewBackend(proc.Exec{}, config.DefaultQEMU, logf)
		if err != nil {
			return nil, err
		}
		be.Timeout = timeout
		return &media.Builder{Paths: p, Runner: proc.Exec{}, VM: be, Log: logf}, nil
	}
)

// logf reaches Packer's log (PACKER_LOG=1): a data source gets no UI, and
// nothing here may print to stdout, which the plugin's RPC owns.
func logf(format string, a ...any) { log.Printf("mavericks-media: "+format, a...) }

func (d *Datasource) Execute() (cty.Value, error) {
	ctx := context.Background()
	null := cty.NullVal(cty.EmptyObject)
	c := d.config

	// 1. The host, before anything: an AMD CPU, no VT-x or no writable
	// /dev/kvm is refused by name, and so is any OS but Linux.
	if err := hostCheck(); err != nil {
		return null, err
	}

	cacheDir := c.CacheDir
	if cacheDir == "" {
		var err error
		if cacheDir, err = packersdk.CachePath("mavericks"); err != nil {
			return null, err
		}
	}
	// 2. The listing. The build tools are not asked about here: an
	// entry already in the store needs none of them (make asks, before
	// it fetches or builds anything).
	reg, err := loadRegistry()
	if err != nil {
		return null, err
	}
	key, err := d.authorizedKey()
	if err != nil {
		return null, err
	}
	// The OpenSSH release's SHA256SUMS, which names the packages, is
	// cached per tag: on a warm cache this reads a file; on a cold one it
	// is fetched once, and make's packages are the ones it names.
	g := &fetch.Getter{Paths: config.Paths{Home: cacheDir}, Log: logf}
	var osh *fetch.OpenSSHRelease
	if c.OpenSSH.True() {
		tag, err := fetch.OpenSSHTag()
		if err != nil {
			return null, err
		}
		rel, err := g.OpenSSHRelease(ctx, openSSHReleases, tag)
		if err != nil {
			return null, err
		}
		osh = &rel
	}
	listing, err := d.listing(reg, key, osh)
	if err != nil {
		return null, err
	}

	// 3. The store.
	st := store.Store{Root: cacheDir, Log: logf}
	dir, _, err := st.Get(ctx, "media", listing, func(ctx context.Context, dir string) error {
		return d.make(ctx, g, reg, key, osh, dir)
	})
	if err != nil {
		return null, err
	}

	// 4. The outputs, read back from the entry: the same on a reuse as
	// on the build that made it.
	img := filepath.Join(dir, mediaName)
	sum, err := sidecarSum(img + ".sha256")
	if err != nil {
		return null, err
	}
	digest, err := os.ReadFile(filepath.Join(dir, digestName))
	if err != nil {
		return null, err
	}
	fields := strings.Fields(string(digest))
	if len(fields) == 0 {
		return null, fmt.Errorf("%s is empty", filepath.Join(dir, digestName))
	}
	output := DatasourceOutput{Path: img, SHA256: sum, ContentDigest: fields[0]}
	if c.AuthorizedKey == "" {
		output.SSHPrivateKeyFile = filepath.Join(dir, privateKey)
	}
	return hcl2helper.HCL2ValueFromConfig(output, d.OutputSpec()), nil
}

// authorizedKey is the public key the payload authorizes, as bytes:
// Vagrant's insecure key by default, or the authorized_key file's. It is
// read once: the listing hashes these bytes, and make writes these same
// bytes into the entry for the payload to read, so a file that changes
// in between cannot put one key in the guest under another's name.
func (d *Datasource) authorizedKey() ([]byte, error) {
	if d.config.AuthorizedKey == "" {
		return payload.VagrantPublicKey(), nil
	}
	b, err := os.ReadFile(d.config.AuthorizedKey)
	if err != nil {
		return nil, fmt.Errorf("authorized_key: %w", err)
	}
	return b, nil
}

// fingerprints is every key's SHA256 fingerprint in an authorized_keys
// text, in order, space-separated: a key added to or dropped from the
// file is a changed input, not only the first one.
func fingerprints(key []byte) (string, error) {
	var fps []string
	rest := key
	for len(bytes.TrimSpace(rest)) > 0 {
		k, _, _, r, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			return "", err
		}
		fps = append(fps, ssh.FingerprintSHA256(k))
		rest = r
	}
	if len(fps) == 0 {
		return "", errors.New("no public key in it")
	}
	return strings.Join(fps, " "), nil
}

// listing is what the media is made of: the payload's repository rows
// (the three files it embeds, the OpenSSH release), the media's (the
// autoinstall hooks, the privops scripts, the pinned ESD), recipe, and
// what this configuration adds -- the account; the authorized key's
// sha256 (the payload embeds the file verbatim, so an options field or a
// comment is as much an input as the key) and, for a reader of the
// entry's inputs file, each key's fingerprint; whether the key is
// Vagrant's, whether OpenSSH is carried and, when it is, the two
// packages by the names and sha256s the release's SHA256SUMS gives them
// (the tag is pinned; SUMS is not, so what it says is an input too); the
// updates stamp and the extra space.
//
// The payload is not named by its own sha256: it is built inside make,
// after the listing is taken, and it is byte-for-byte a function of what
// is listed (payload's TestTheSameInputsGiveAByteIdenticalPackage). Nor
// is the ESD hashed here: its pinned sha256 is the media rows'
// source:apple-installesd row, and make refuses an ESD that does not
// match it before building.
func (d *Datasource) listing(reg *pins.Registry, key []byte, osh *fetch.OpenSSHRelease) ([]string, error) {
	c := d.config
	pay, err := inputs.RepoRows(reg, "payload", "")
	if err != nil {
		return nil, err
	}
	med, err := inputs.RepoRows(reg, "media", "")
	if err != nil {
		return nil, err
	}
	fp, err := fingerprints(key)
	if err != nil {
		return nil, fmt.Errorf("authorized_key %s: %w", c.AuthorizedKey, err)
	}
	keySum := sha256.Sum256(key)
	extras := []inputs.Row{
		{Key: "recipe", Value: recipe},
		{Key: "user", Value: c.User},
		{Key: "sshkey", Value: fp},
		{Key: "sshkey-sha256", Value: hex.EncodeToString(keySum[:])},
		{Key: "sshkey-vagrant-insecure", Value: onOff(c.AuthorizedKey == "")},
		{Key: "openssh-enabled", Value: onOff(c.OpenSSH.True())},
		{Key: "extra-space-mib", Value: strconv.Itoa(c.ExtraSpaceMiB)},
	}
	if osh != nil {
		for _, a := range []fetch.OpenSSHAsset{osh.Base, osh.Replace} {
			extras = append(extras, inputs.Row{Key: "openssh:" + a.Name, Value: a.SHA256})
		}
	}
	stamp, err := inputs.UpdatesStamp(reg, c.Updates)
	if err != nil {
		return nil, err
	}
	extras = append(extras, stamp...)
	// payload and media both list the OpenSSH release: once is enough.
	return inputs.Listing(dedupeRows(append(pay, med...)), extras), nil
}

func dedupeRows(rows []inputs.Row) []inputs.Row {
	seen := make(map[inputs.Row]bool, len(rows))
	out := make([]inputs.Row, 0, len(rows))
	for _, r := range rows {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// make is store.Get's make, building into dir: the media builder asked
// whether this host can build media at all; the ESD checked against its
// pin; OpenSSH (the packages osh names, as the listing does) and the
// updates fetched into the shared cache; the
// updates staged as dir/updates/ symlinks under the names the media
// presents them by; the authorized key written into the entry; the
// first-boot payload built into dir/mqg-firstboot.pkg; the media built
// in the scratch home and moved into dir/installer-media.img, with its
// sidecar; its content digest taken; and, with the default key,
// Vagrant's insecure private key written as dir/vagrant_insecure_key,
// mode 0600.
//
// The scratch home is shared by every store key, so, as for the
// firmware's workspace, make holds its own lock on it for all of make:
// store.Get's lock serializes only same-listing callers, and media.Build's
// own lock on the scratch media is released before the media moves out.
// A second build refuses, naming the holder. The three locks are three
// different paths -- <cache>/media/<digest>.lock (the store's),
// <cache>/media-build.lock (this) and
// <cache>/media-build/build/installer-media.img.lock (media.Build's) --
// so none waits on another.
func (d *Datasource) make(ctx context.Context, g *fetch.Getter, reg *pins.Registry, key []byte, osh *fetch.OpenSSHRelease, dir string) (err error) {
	c := d.config
	// The media builder's home is scratch under the store's root, never
	// the store entry: its work/media/ holds several GB of raw
	// conversions, and its build/ is where the media is made before it
	// moves into the entry.
	scratch := config.Paths{Home: filepath.Join(g.Paths.Home, "media-build")}
	l, err := lock.Acquire(scratch.Home+".lock", 0)
	if err != nil {
		return err
	}
	defer func() {
		if rerr := l.Release(); err == nil {
			err = rerr
		}
	}()

	// What the builder can say before anything is fetched or built: the
	// partition's extra space, and whether this host has dmg2img,
	// mkfs.hfsplus and what the privops microVM needs. Asked only here,
	// on a store miss: reusing an entry needs none of it.
	mb, err := newMediaBuilder(scratch, c.PrivopsTimeout)
	if err != nil {
		return err
	}
	if err := mb.Validate(media.Options{ExtraSpaceMiB: c.ExtraSpaceMiB, Force: true}); err != nil {
		return err
	}
	if err := mb.Preflight(); err != nil {
		return err
	}

	if err := checkESD(reg, c.InstallESD); err != nil {
		return err
	}

	// The key the payload authorizes is a file payload.Build reads: the
	// exact bytes the listing hashed, written into this entry -- Vagrant's
	// public key, or the user's, never re-read from their file.
	keyPath := filepath.Join(dir, publicKey)
	if c.AuthorizedKey != "" {
		keyPath = filepath.Join(dir, userKey)
	}
	if err := os.WriteFile(keyPath, key, 0o644); err != nil {
		return err
	}

	var pkgs fetch.OpenSSHPkgs
	if osh != nil {
		if pkgs, err = g.OpenSSHPackages(ctx, openSSHReleases, *osh); err != nil {
			return err
		}
	}
	ups, err := g.Updates(ctx, reg, c.Updates)
	if err != nil {
		return err
	}
	staged, err := stageUpdates(filepath.Join(dir, updatesSubdir), ups)
	if err != nil {
		return err
	}

	pc := payload.VagrantAccount(c.User)
	pc.SSHKey = keyPath
	if osh != nil {
		pc.OpenSSHPkgs = []string{pkgs.Base, pkgs.Replace}
		pc.OpenSSHTag = pkgs.Tag
	}
	pc.Updates = c.Updates
	var updatePaths []string
	for _, u := range ups {
		pc.UpdatePkgs = append(pc.UpdatePkgs, payload.MediaFile{Path: u.Path, Name: u.Staged})
		updatePaths = append(updatePaths, u.Path)
	}
	pkg := filepath.Join(dir, payloadName)
	if _, err := buildPayload(ctx, proc.Exec{}, pc, pkg, logf); err != nil {
		return err
	}

	// The OpenSSH packages first, then the updates in install order, as
	// the media presents them.
	var extra []string
	if osh != nil {
		extra = append(extra, pkgs.Base, pkgs.Replace)
	}
	extra = append(extra, staged...)
	mib, err := media.UpdatesExtraMiB(updatePaths)
	if err != nil {
		return err
	}
	o := media.Options{
		Injectables:   media.Injectables{Autoinstall: true, FirstbootPkg: pkg, ExtraPkgs: extra},
		ExtraSpaceMiB: mib + c.ExtraSpaceMiB,
		Force:         true,
	}
	// Build asks Validate and Preflight again, now with the packages.
	built, err := mb.Build(ctx, c.InstallESD, o)
	if err != nil {
		return err
	}
	img := filepath.Join(dir, mediaName)
	// The sidecar first: a media file in the entry always has its own.
	// Both renames stay within cacheDir.
	if err := os.Rename(built+".sha256", img+".sha256"); err != nil {
		return err
	}
	if err := os.Rename(built, img); err != nil {
		return err
	}

	dg, err := mb.ContentDigest(ctx, img, nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, digestName), []byte(dg.String()+"\n"), 0o644); err != nil {
		return err
	}

	if c.AuthorizedKey == "" {
		if err := writePrivateKey(filepath.Join(dir, privateKey)); err != nil {
			return err
		}
	}
	return nil
}

// checkESD refuses an ESD whose sha256 is not the pinned one: the
// listing names the ESD by its pin, so a build from any other file would
// be filed under a name that does not describe it.
func checkESD(reg *pins.Registry, esd string) error {
	src, err := reg.Lookup(fetch.ESDSource)
	if err != nil {
		return err
	}
	if !config.RegularFile(esd) {
		return fmt.Errorf("installesd: no InstallESD.dmg at %s", esd)
	}
	logf("checking %s against its pinned sha256", esd)
	sum, err := fetch.SHA256File(esd)
	if err != nil {
		return fmt.Errorf("installesd: %w", err)
	}
	if sum != src.SHA256 {
		return fmt.Errorf("installesd: %s has sha256 %s, not the pinned %s", esd, sum, src.SHA256)
	}
	return nil
}

// stageUpdates makes dir/<u.Staged> a symlink to each update in the
// shared cache, in install order, and returns the links: the media
// carries a package under its path's base, and fetch.StagedName's name is
// the one firstboot.conf tells the guest to install. dir is inside a
// fresh store entry, so there is nothing of an earlier selection to
// clear.
func stageUpdates(dir string, ups []fetch.Update) ([]string, error) {
	if len(ups) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var links []string
	for _, u := range ups {
		link := filepath.Join(dir, u.Staged)
		if err := os.Symlink(u.Path, link); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, nil
}

// writePrivateKey writes Vagrant's insecure private key at path, mode
// 0600 from the moment it exists: ssh refuses a private key others can
// read. It is public by design (assets/vagrant/README.md).
func writePrivateKey(path string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	// The umask can only take bits away; this says 0600 whatever it was.
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	_, err = f.Write(payload.VagrantPrivateKey())
	return err
}

// sidecarSum is the checksum in media.Build's .sha256 sidecar: the first
// field of its first line that is not a comment.
func sidecarSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.Fields(line)[0], nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return "", fmt.Errorf("%s names no checksum: %w", path, fs.ErrNotExist)
}
