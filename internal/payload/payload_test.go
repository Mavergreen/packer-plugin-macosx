package payload

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
	"golang.org/x/crypto/ssh"
)

// --- test helpers ----------------------------------------------------

func rsaPubKey(t *testing.T) []byte {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(pub)
}

func ed25519PubKey(t *testing.T) []byte {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(sshPub)
}

// fakePkg writes a fake flat package -- "xar!" plus filler, never Apple's
// bytes -- at dir/name, and returns its path.
func fakePkg(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("xar!not a real package, just filler for the tests"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// extractPostinstall reads the one file a flatPackage's Scripts member
// carries.
func extractPostinstall(t *testing.T, pkg []byte) []byte {
	t.Helper()
	_, members, err := readXar(pkg)
	if err != nil {
		t.Fatal(err)
	}
	scripts, ok := members["Scripts"]
	if !ok {
		t.Fatal("package has no Scripts member")
	}
	raw := gunzip(t, scripts)
	entries, err := readODC(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "./postinstall" {
			return e.Data
		}
	}
	t.Fatal("Scripts has no ./postinstall entry")
	return nil
}

// golden reads testdata/golden/<name> (testdata/golden/README.md says
// what each file is).
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), "internal", "payload", "testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// updateGoldens is how the postinstall goldens change: when a guest
// asset under assets/guest/ or the conf's text changes on purpose,
// `go test ./internal/payload -run PostinstallGolden -update` rewrites
// them from what the code now produces, and the diff is reviewed and
// committed with the change (testdata/golden/README.md).
var updateGoldens = flag.Bool("update", false, "rewrite testdata/golden/postinstall-*.golden from the code's output (see testdata/golden/README.md)")

// postinstallGolden is golden for the three postinstall goldens, or,
// under -update, writes got over the golden and returns it.
func postinstallGolden(t *testing.T, name string, got []byte) []byte {
	t.Helper()
	if *updateGoldens {
		p := filepath.Join(repoRoot(), "internal", "payload", "testdata", "golden", name)
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s: review the diff before committing it", p)
	}
	return golden(t, name)
}

// fixedRSAKey and fixedEd25519Key are the keys the postinstall goldens
// were made with: a golden comparison needs the same bytes every run, so
// the three postinstall golden tests use these fixed keys
// (testdata/golden/README.md). rsaPubKey and ed25519PubKey (below)
// generate a fresh key for every other test.
func fixedRSAKey(t *testing.T) []byte     { return golden(t, "fixed-rsa.pub") }
func fixedEd25519Key(t *testing.T) []byte { return golden(t, "fixed-ed25519.pub") }

// requireBash5 skips unless the bash on PATH is version 5 or later.
// bashQuote is bash 5's printf %q; macOS's /bin/bash is 3.2 (the go-macos CI job's), whose %q quotes
// differently, so comparing against it would fail for no reason
// connected to this code.
func requireBash5(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	out, err := exec.Command("bash", "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	if err != nil {
		t.Skipf("cannot ask bash its version: %v", err)
	}
	if major, err := strconv.Atoi(strings.TrimSpace(string(out))); err != nil || major < 5 {
		t.Skipf("bash major version %q: printf %%q before bash 5 is not what bashQuote reproduces", strings.TrimSpace(string(out)))
	}
}

func TestBashQuoteMatchesBash(t *testing.T) {
	requireBash5(t)
	corpus := []string{
		"mavsuser", "Mavericks User", "/bin/bash", "a.pkg b.pkg ", "it's",
		"$HOME", "~root", "a=~b", "#x", "x#", "100%", "a,b", "semi;colon",
		`""`, `back\slash`,
	}
	for _, s := range corpus {
		got, err := bashQuote(s)
		if err != nil {
			t.Errorf("bashQuote(%q): %v", s, err)
			continue
		}
		out, err := exec.Command("bash", "-c", `printf %q "$1"`, "_", s).Output()
		if err != nil {
			t.Fatalf("bash printf %%q: %v", err)
		}
		if got != string(out) {
			t.Errorf("bashQuote(%q) = %q, bash printf %%q gives %q", s, got, out)
		}
	}
	for _, c := range []byte{0x00, 0x01, 0x09, 0x0a, 0x7f, 0x80, 0xff} {
		if _, err := bashQuote(string([]byte{c})); err == nil {
			t.Errorf("bashQuote(0x%02x): want an error, got none", c)
		}
	}
}

// TestConfDefaultsMatchThePostinstallGoldenByteForByte: the postinstall
// assembled from the default Config and fixedRSAKey.
func TestConfDefaultsMatchThePostinstallGoldenByteForByte(t *testing.T) {
	key := fixedRSAKey(t)

	c := DefaultConfig()
	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Postinstall(conf, key)
	if err != nil {
		t.Fatal(err)
	}

	want := postinstallGolden(t, "postinstall-defaults.golden", got)
	if !bytes.Equal(got, want) {
		t.Fatalf("postinstall differs from its golden:\n--- got ---\n%s\n--- golden ---\n%s", got, want)
	}
}

// TestConfWithOpenSSHAndUpdatesMatchesThePostinstallGolden: the
// postinstall assembled from fixedEd25519Key and these fixed-content
// packages.
func TestConfWithOpenSSHAndUpdatesMatchesThePostinstallGolden(t *testing.T) {
	dir := t.TempDir()
	key := fixedEd25519Key(t)

	base := fakePkg(t, dir, "openssh-6.9p1-mavericks.2-base.pkg")
	replace := fakePkg(t, dir, "openssh-6.9p1-mavericks.2-replace.pkg")
	upd := fakePkg(t, dir, "mqg-update-01-SecUpd2016-004Mavericks.pkg")

	c := DefaultConfig()
	c.OpenSSHPkgs = []string{base, replace}
	c.OpenSSHTag = "10.5p1-mavericks.2"
	// The staged path: its base IS the media's name.
	c.UpdatePkgs = []MediaFile{{Path: upd, Name: filepath.Base(upd)}}
	c.Updates = "security"

	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Postinstall(conf, key)
	if err != nil {
		t.Fatal(err)
	}

	want := postinstallGolden(t, "postinstall-openssh-updates.golden", got)
	if !bytes.Equal(got, want) {
		t.Fatalf("postinstall differs from its golden:\n--- got ---\n%s\n--- golden ---\n%s", got, want)
	}
}

// TestConfWithPasswordMatchesThePostinstallGolden: the postinstall
// assembled from fixedRSAKey with the password hunter2.
func TestConfWithPasswordMatchesThePostinstallGolden(t *testing.T) {
	key := fixedRSAKey(t)

	c := DefaultConfig()
	c.Password = "hunter2"
	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Postinstall(conf, key)
	if err != nil {
		t.Fatal(err)
	}

	want := postinstallGolden(t, "postinstall-password.golden", got)
	if !bytes.Equal(got, want) {
		t.Fatalf("postinstall with a password differs from its golden:\n--- got ---\n%s\n--- golden ---\n%s", got, want)
	}
}

// TestConfPlacesThePasswordLineLast checks the conf's line order
// directly: header; account fields; MQG_FB_ADMIN_GID; OpenSSH block;
// updates block; password last. TestConfWithPasswordMatchesThePostinstallGolden
// above already holds this byte for byte; this test pins the same fact
// at the Conf level.
func TestConfPlacesThePasswordLineLast(t *testing.T) {
	c := DefaultConfig()
	c.Password = "hunter2"
	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(conf), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "MQG_FB_PASSWORD=") {
		t.Fatalf("MQG_FB_PASSWORD is not the last line:\n%s", conf)
	}
}

// TestConfCarriesSudo: a build that asks for Sudo gets MQG_FB_SUDO=1;
// DefaultConfig's own build (Sudo unset) gets MQG_FB_SUDO=0 -- unlike
// the OpenSSH and updates blocks, Sudo is always written, the same way
// MQG_FB_AUTOLOGIN always is, so a default conf still says explicitly
// that sudo was not asked for.
func TestConfCarriesSudo(t *testing.T) {
	c := DefaultConfig()
	c.Sudo = true
	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(conf, []byte("MQG_FB_SUDO=1")) {
		t.Fatalf("conf does not carry MQG_FB_SUDO=1:\n%s", conf)
	}
}

func TestDefaultConfigConfCarriesNoSudo(t *testing.T) {
	conf, err := Conf(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(conf, []byte("MQG_FB_SUDO=0")) {
		t.Fatalf("conf does not carry MQG_FB_SUDO=0:\n%s", conf)
	}
}

func TestANoneConfSaysNothingAboutUpdates(t *testing.T) {
	conf, err := Conf(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(conf, []byte("MQG_FB_UPDATES")) {
		t.Fatalf("a conf built with updates none mentions updates:\n%s", conf)
	}
}

// TestConfErrorsNameTheOffendingField: a value Conf cannot quote must say which Config field it came from, not just
// bashQuote's own bare complaint about the byte it choked on.
func TestConfErrorsNameTheOffendingField(t *testing.T) {
	c := DefaultConfig()
	c.RealName = "bad\x01name"
	_, err := Conf(c)
	if err == nil || !strings.Contains(err.Error(), "RealName") {
		t.Fatalf("want an error naming RealName, got %v", err)
	}
}

func TestKeyRules(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("ed25519 without OpenSSH is refused", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("e1.pub", ed25519PubKey(t))
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "6.5") {
			t.Fatalf("want an error mentioning 6.5, got %v", err)
		}
	})

	t.Run("ed25519 with OpenSSH is accepted", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("e2.pub", ed25519PubKey(t))
		c.OpenSSHPkgs = []string{fakePkg(t, dir, "b1.pkg"), fakePkg(t, dir, "b2.pkg")}
		c.OpenSSHTag = "10.5p1-mavericks.2"
		if _, err := validateConfig(c, t.Logf); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("a private key is refused", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("priv", []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n"))
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("want an error mentioning PRIVATE, got %v", err)
		}
	})

	t.Run("not a key is refused", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("notkey", []byte("not a key\n"))
		if _, err := validateConfig(c, t.Logf); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a missing file is refused", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = filepath.Join(dir, "does-not-exist.pub")
		if _, err := validateConfig(c, t.Logf); err == nil {
			t.Fatal("want an error")
		}
	})

	// A comment above the real key must not hide it: a key file with
	// "# my laptop key" on line 1 and "ssh-ed25519 ..." on line 2 is an
	// Ed25519 key, refused without OpenSSH with the Ed25519/6.5
	// message -- not a key of type "#".
	t.Run("a commented Ed25519 key is refused without OpenSSH", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("commented-ed25519.pub", append([]byte("# my laptop key\n"), ed25519PubKey(t)...))
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "6.5") {
			t.Fatalf("want an error mentioning 6.5, got %v", err)
		}
	})

	t.Run("a commented Ed25519 key is accepted with OpenSSH", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("commented-ed25519-ok.pub", append([]byte("# my laptop key\n"), ed25519PubKey(t)...))
		c.OpenSSHPkgs = []string{fakePkg(t, dir, "b3.pkg"), fakePkg(t, dir, "b4.pkg")}
		c.OpenSSHTag = "10.5p1-mavericks.2"
		if _, err := validateConfig(c, t.Logf); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("a blank first line does not hide an Ed25519 key on the second", func(t *testing.T) {
		c := DefaultConfig()
		c.SSHKey = write("blank-first-ed25519.pub", append([]byte("\n"), ed25519PubKey(t)...))
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "6.5") {
			t.Fatalf("want an error mentioning 6.5, got %v", err)
		}
	})

	t.Run("a comment above an RSA key is accepted with no bogus warning", func(t *testing.T) {
		var logs []string
		capture := func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }
		c := DefaultConfig()
		c.SSHKey = write("commented-rsa.pub", append([]byte("# my laptop key\n"), rsaPubKey(t)...))
		if _, err := validateConfig(c, capture); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, l := range logs {
			if strings.Contains(l, "a # key") {
				t.Errorf("bogus warning about the comment line, not the real key: %q", l)
			}
		}
	})
}

// TestBuildLogsTheRealKeyType: the "authorized key:" line Build logs
// must name the key's real type, not "#" -- the refusal above's rule, on
// the logging side.
func TestBuildLogsTheRealKeyType(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "commented.pub")
	if err := os.WriteFile(keyPath, append([]byte("# my laptop key\n"), rsaPubKey(t)...), 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath

	var logs []string
	capture := func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }
	out := filepath.Join(dir, "mqg-firstboot.pkg")
	if _, err := Build(context.Background(), proc.Exec{}, c, out, capture); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, l := range logs {
		if !strings.HasPrefix(l, "authorized key: ") {
			continue
		}
		found = true
		if strings.Contains(l, "authorized key: #") {
			t.Errorf("logged the comment marker as the key type: %q", l)
		}
		if !strings.Contains(l, "ssh-rsa") {
			t.Errorf("did not log the real key type: %q", l)
		}
	}
	if !found {
		t.Fatal(`no "authorized key:" log line`)
	}
}

func TestPackageRules(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "k.pub")
	if err := os.WriteFile(keyPath, rsaPubKey(t), 0o644); err != nil {
		t.Fatal(err)
	}
	base := func() Config {
		c := DefaultConfig()
		c.SSHKey = keyPath
		return c
	}

	t.Run("a whitespace basename is refused", func(t *testing.T) {
		c := base()
		c.UpdatePkgs = []MediaFile{{Path: fakePkg(t, dir, "has space.pkg"), Name: "fine.pkg"}}
		c.Updates = "security"
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "whitespace") {
			t.Fatalf("want an error mentioning whitespace, got %v", err)
		}
	})

	t.Run("a non-xar file is refused", func(t *testing.T) {
		p := filepath.Join(dir, "notpkg.pkg")
		if err := os.WriteFile(p, []byte("not a package"), 0o644); err != nil {
			t.Fatal(err)
		}
		c := base()
		c.UpdatePkgs = []MediaFile{{Path: p, Name: "notpkg.pkg"}}
		c.Updates = "security"
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "xar") {
			t.Fatalf("want an error mentioning xar magic, got %v", err)
		}
	})

	for _, name := range []string{"", ".", "..", "staged/u1.pkg", "has space.pkg"} {
		t.Run(fmt.Sprintf("a media name of %q is refused", name), func(t *testing.T) {
			c := base()
			c.UpdatePkgs = []MediaFile{{Path: fakePkg(t, dir, "u1.pkg"), Name: name}}
			c.Updates = "security"
			_, err := validateConfig(c, t.Logf)
			if err == nil || !strings.Contains(err.Error(), "UpdatePkgs") {
				t.Fatalf("want an UpdatePkgs error, got %v", err)
			}
		})
	}

	t.Run("OpenSSH packages without a tag are refused", func(t *testing.T) {
		c := base()
		c.OpenSSHPkgs = []string{fakePkg(t, dir, "b1.pkg"), fakePkg(t, dir, "b2.pkg")}
		_, err := validateConfig(c, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "OpenSSHTag") {
			t.Fatalf("want an error mentioning OpenSSHTag, got %v", err)
		}
	})

	t.Run("updates none with packages is refused", func(t *testing.T) {
		c := base()
		c.UpdatePkgs = []MediaFile{{Path: fakePkg(t, dir, "u1.pkg"), Name: "u1.pkg"}}
		c.Updates = "none"
		if _, err := validateConfig(c, t.Logf); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("security without packages is refused", func(t *testing.T) {
		c := base()
		c.Updates = "security"
		if _, err := validateConfig(c, t.Logf); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("sometimes is refused", func(t *testing.T) {
		c := base()
		c.Updates = "sometimes"
		if _, err := validateConfig(c, t.Logf); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestAMissingTrailingNewlineStillTerminatesTheHeredoc(t *testing.T) {
	conf, err := Conf(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC comment-with-no-trailing-newline")
	post, err := Postinstall(conf, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(post, append(append([]byte{}, key...), '\n', 'M', 'Q', 'G', '_', 'E', 'O', 'F', '_', 'A', 'U', 'T', 'H', 'O', 'R', 'I', 'Z', 'E', 'D', '_', 'K', 'E', 'Y', 'S', '\n')) {
		t.Fatalf("MQG_EOF_AUTHORIZED_KEYS is not on its own line after the key:\n%s", post)
	}
}

func TestBuildWritesThePackageAndSidecarDeterministically(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyPath, rsaPubKey(t), 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath

	r := proc.Exec{}
	ctx := context.Background()

	out1 := filepath.Join(dir, "mqg-firstboot.pkg")
	sha1, err := Build(ctx, r, c, out1, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	pkg1, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	if string(pkg1[:4]) != "xar!" {
		t.Fatalf("package does not start with xar!: %q", pkg1[:4])
	}

	out2 := filepath.Join(dir, "second", "mqg-firstboot.pkg")
	sha2, err := Build(ctx, r, c, out2, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if sha1 != sha2 {
		t.Fatalf("sha256 differs between two builds of the same inputs: %s vs %s", sha1, sha2)
	}

	sidecar, err := os.ReadFile(out1 + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%s  %s\n", sha1, filepath.Base(out1))
	if string(sidecar) != want {
		t.Fatalf("sidecar = %q, want %q", sidecar, want)
	}

	// The sidecar is written the same temp-then-rename way as the package
	// itself, and no temp of either is left behind.
	entries, err := os.ReadDir(filepath.Dir(out2))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"mqg-firstboot.pkg", "mqg-firstboot.pkg.sha256"}; !slices.Equal(names, want) {
		t.Fatalf("%s holds %v, want exactly %v", filepath.Dir(out2), names, want)
	}
	for _, f := range []string{out1, out1 + ".sha256"} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s: mode %v (%v), want 0644", f, fi.Mode().Perm(), err)
		}
	}
}

// TestBuildNeverWritesThroughAFixedTempName: a fixed out+".tmp" temp
// would be opened, truncated and written through if something already
// sat at that name -- here a hard link to a file that is not Build's to
// touch. Every temp is a fresh, uniquely named file.
func TestBuildNeverWritesThroughAFixedTempName(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyPath, rsaPubKey(t), 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath
	out := filepath.Join(dir, "mqg-firstboot.pkg")
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, fixed := range []string{out + ".tmp", out + ".sha256.tmp"} {
		if err := os.Link(victim, fixed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Build(context.Background(), proc.Exec{}, c, out, t.Logf); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "not yours" {
		t.Fatalf("Build wrote through a pre-existing temp name: victim = %q, %v", b, err)
	}
}

func TestTheBuiltPostinstallInstallsThePayloadOnATargetOffline(t *testing.T) {
	dir := t.TempDir()
	key := rsaPubKey(t)
	keyPath := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyPath, key, 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath

	out := filepath.Join(dir, "mqg-firstboot.pkg")
	if _, err := Build(context.Background(), proc.Exec{}, c, out, t.Logf); err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	post := extractPostinstall(t, pkg)

	postPath := filepath.Join(dir, "postinstall")
	if err := os.WriteFile(postPath, post, 0o755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	// $1 package path, $2 install destination, $3 THE TARGET VOLUME, $4
	// root of the target system -- the OS X Installer's own convention
	// (see assets/guest/postinstall's header comment).
	cmd := exec.Command("sh", postPath, "/pkg", "/dest", target, "/")
	if combined, err := cmd.CombinedOutput(); err != nil {
		// chown failing here (not root) is expected and the script
		// ignores it; a real failure is everything else.
		t.Fatalf("postinstall: %v: %s", err, combined)
	}

	confDir := filepath.Join(target, "private", "var", "db", ".mqg-firstboot")

	gotSh, err := os.ReadFile(filepath.Join(confDir, "firstboot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	wantSh, err := fs.ReadFile(vmguest.Files, "assets/guest/firstboot.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSh, wantSh) {
		t.Error("firstboot.sh on the target differs from the embedded copy")
	}

	gotKey, err := os.ReadFile(filepath.Join(confDir, "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Error("authorized_keys on the target differs from the key that was asked for")
	}

	conf, err := os.ReadFile(filepath.Join(confDir, "firstboot.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(conf, []byte("MQG_FB_USER=mavsuser")) {
		t.Error("firstboot.conf does not carry MQG_FB_USER=mavsuser")
	}
	if bytes.Contains(conf, []byte("MQG_FB_PASSWORD")) {
		t.Error("firstboot.conf mentions a password that was never asked for")
	}

	gotPlist, err := os.ReadFile(filepath.Join(target, "Library", "LaunchDaemons", "com.mqg.firstboot.plist"))
	if err != nil {
		t.Fatal(err)
	}
	wantPlist, err := fs.ReadFile(vmguest.Files, "assets/guest/com.mqg.firstboot.plist")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPlist, wantPlist) {
		t.Error("the LaunchDaemon plist on the target differs from the embedded copy")
	}

	if _, err := os.Stat(filepath.Join(target, "private", "var", "db", ".AppleSetupDone")); err != nil {
		t.Errorf(".AppleSetupDone was not created: %v", err)
	}

	checkMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	checkMode(filepath.Join(confDir, "firstboot.sh"), 0o755)
	checkMode(filepath.Join(confDir, "firstboot.conf"), 0o600)
	checkMode(filepath.Join(confDir, "authorized_keys"), 0o644)
	checkMode(filepath.Join(target, "Library", "LaunchDaemons", "com.mqg.firstboot.plist"), 0o644)
}

func TestBuildRefusesAnInvalidPostinstall(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyPath, rsaPubKey(t), 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath

	out := filepath.Join(dir, "mqg-firstboot.pkg")
	fake := &proc.Fake{Handle: func(cmd proc.Cmd) error {
		return &proc.ExitError{Cmd: cmd.String(), Code: 1}
	}}

	_, err := Build(context.Background(), fake, c, out, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "not valid shell") {
		t.Fatalf("want an error mentioning \"not valid shell\", got %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("Build wrote a package despite the invalid postinstall")
	}

	// Build must check the assembled postinstall with exactly one
	// `sh -n <file>` call, not run anything else.
	if len(fake.Calls) != 1 {
		t.Fatalf("want exactly one command run, got %d: %+v", len(fake.Calls), fake.Calls)
	}
	if fake.Calls[0].Name != "sh" {
		t.Fatalf("want sh run, got %q", fake.Calls[0].Name)
	}
	if len(fake.Calls[0].Args) == 0 || fake.Calls[0].Args[0] != "-n" {
		t.Fatalf("want sh's first argument to be -n, got %v", fake.Calls[0].Args)
	}
}

// TestBuildIncludesShStderrInTheError: sh -n's own complaint (e.g. "line 42: syntax error") is what actually says
// what is wrong with the assembled postinstall, and Build's error must
// carry it, not just report that sh exited non-zero.
func TestBuildIncludesShStderrInTheError(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyPath, rsaPubKey(t), 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath

	out := filepath.Join(dir, "mqg-firstboot.pkg")
	fake := &proc.Fake{Handle: func(cmd proc.Cmd) error {
		if cmd.Stderr != nil {
			cmd.Stderr.Write([]byte("sh: line 42: syntax error near unexpected token\n"))
		}
		return &proc.ExitError{Cmd: cmd.String(), Code: 2}
	}}

	_, err := Build(context.Background(), fake, c, out, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "syntax error near unexpected token") {
		t.Fatalf("want sh -n's stderr in the error, got %v", err)
	}
}

// TestTheConfNamesUpdatesAsTheMediaPresentsThem: fetch caches an update
// under Apple's own name (cache/<sha>/SecUpd2016-004Mavericks.pkg), but
// the media presents it as mqg-update-01-..., and the guest's carry_pkgs
// copies exactly the names firstboot.conf gives it off the media. A conf
// naming the cache file's base would leave the guest without its
// security update, silently ("NOT FOUND on the media" in a log nobody
// reads).
func TestTheConfNamesUpdatesAsTheMediaPresentsThem(t *testing.T) {
	dir := t.TempDir()
	cached := fakePkg(t, dir, "SecUpd2016-004Mavericks.pkg")
	c := DefaultConfig()
	c.UpdatePkgs = []MediaFile{{Path: cached, Name: "mqg-update-01-SecUpd2016-004Mavericks.pkg"}}
	c.Updates = "security"
	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	if want := "MQG_FB_UPDATE_PKGS=mqg-update-01-SecUpd2016-004Mavericks.pkg\\ \n"; !strings.Contains(string(conf), want) {
		t.Fatalf("conf = %s, want the line %q", conf, want)
	}
	if strings.Contains(string(conf), "=SecUpd2016") {
		t.Fatalf("conf = %s names the cache file, not the media's name", conf)
	}
}

// --- the guest-side assets themselves ---------------------------------
//
// assets/guest/firstboot.sh, postinstall and com.mqg.firstboot.plist run
// inside the guest. The tests below are static content checks of those
// three files -- the ones no execution-based test above already covers --
// as regexp checks of the embedded bytes. The package builder's rules
// are covered by TestKeyRules, TestPackageRules and the tests above, and
// by internal/media's build tests.

// firstbootSh is assets/guest/firstboot.sh, read fresh per test, like its
// siblings above and below read their own embedded files.
func firstbootSh(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(vmguest.Files, "assets/guest/firstboot.sh")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// "firstboot.sh skips Setup Assistant"
func TestFirstbootShSkipsSetupAssistant(t *testing.T) {
	if !bytes.Contains(firstbootSh(t), []byte("AppleSetupDone")) {
		t.Fatal("firstboot.sh never mentions AppleSetupDone")
	}
}

// "firstboot.sh creates the account the click-log recorded"
func TestFirstbootShCreatesTheRecordedAccount(t *testing.T) {
	b := firstbootSh(t)
	if !bytes.Contains(b, []byte("mavsuser")) {
		t.Error("firstboot.sh never mentions mavsuser")
	}
	if !bytes.Contains(b, []byte("dscl")) {
		t.Error("firstboot.sh never calls dscl")
	}
}

// "firstboot.sh never contains an embedded private key or password"
func TestFirstbootShNeverEmbedsASecret(t *testing.T) {
	b := firstbootSh(t)
	if privateKeyRe.Match(b) {
		t.Error("firstboot.sh contains what looks like an embedded private key")
	}
	if hardcodedPasswordRe.Match(b) {
		t.Error("firstboot.sh contains what looks like a hardcoded password")
	}
}

var (
	privateKeyRe         = regexp.MustCompile(`(?i)BEGIN (RSA|OPENSSH|DSA|EC) PRIVATE KEY`)
	hardcodedPasswordRe  = regexp.MustCompile(`(?im)^[^#]*password=[^$"]`)
	publicKeyBlobRe      = regexp.MustCompile(`(?i)(ssh-(rsa|dss|ed25519)|ecdsa-sha2-[a-z0-9-]+) +AAAA`)
	networkOrInstallerRe = regexp.MustCompile(`^\s*(softwareupdate|installer)\s`)
	timeoutInvocationRe  = regexp.MustCompile(`run_with_timeout [0-9]+ (softwareupdate|installer)`)
)

// "every network-touching or long-running step has a timeout": 10.9 has no
// timeout(1) and softwareupdate against Apple's 2026 servers, or a wedged
// installer run, can hang the first boot forever with nothing saying
// which step stopped. Comment lines about softwareupdate/installer are not
// invocations -- excluding only lines that also mention run_with_timeout
// would go red the moment firstboot.sh explains itself in prose, so this
// checks whole lines against both patterns instead.
func TestFirstbootShWrapsNetworkOrLongRunningStepsInATimeout(t *testing.T) {
	var unwrapped []string
	timeouts := 0
	for _, line := range strings.Split(string(firstbootSh(t)), "\n") {
		if networkOrInstallerRe.MatchString(line) && !strings.Contains(line, "run_with_timeout") {
			unwrapped = append(unwrapped, line)
		}
		if timeoutInvocationRe.MatchString(line) {
			timeouts++
		}
	}
	if len(unwrapped) > 0 {
		t.Errorf("firstboot.sh runs softwareupdate/installer with no timeout:\n%s", strings.Join(unwrapped, "\n"))
	}
	if timeouts < 3 {
		t.Errorf("firstboot.sh has %d run_with_timeout softwareupdate/installer invocations, want at least 3", timeouts)
	}
}

// "firstboot.sh removes its own LaunchDaemon so it runs exactly once": a
// grep for the path alone can match a comment instead of the code, so
// this checks the actual $DAEMON variable is both the LaunchDaemon's
// path and what gets removed.
func TestFirstbootShRemovesItsOwnLaunchDaemon(t *testing.T) {
	b := firstbootSh(t)
	if !regexp.MustCompile(`(?m)^DAEMON=.*com\.mqg\.firstboot`).Match(b) {
		t.Error("firstboot.sh does not set DAEMON to its own LaunchDaemon's path")
	}
	if !regexp.MustCompile(`rm\s+-f\s+"?\$DAEMON"?`).Match(b) {
		t.Error("firstboot.sh never removes $DAEMON")
	}
}

var (
	sudoersGuardRe  = regexp.MustCompile(`^\s*if \[ "\$MQG_FB_SUDO" = "1" \]; then\s*$`)
	sudoersVisudoRe = regexp.MustCompile(`visudo -cf "\$SUDOERS_CANDIDATE"`)
	sudoersWriteRe  = regexp.MustCompile(`^\s*install -m 0440 -o root -g wheel "\$SUDOERS_CANDIDATE" "\$SUDOERS_FILE"\s*$`)

	sudoersMainCopyRe    = regexp.MustCompile(`^\s*cp "\$SUDOERS_MAIN" "\$SUDOERS_MAIN_CANDIDATE"\s*$`)
	sudoersMainAppendRe  = regexp.MustCompile(`^\s*echo "\$SUDOERS_INCLUDE" >> "\$SUDOERS_MAIN_CANDIDATE"\s*$`)
	sudoersMainVisudoRe  = regexp.MustCompile(`visudo -cf "\$SUDOERS_MAIN_CANDIDATE"`)
	sudoersMainInstallRe = regexp.MustCompile(`^\s*install -m 0440 -o root -g wheel "\$SUDOERS_MAIN_CANDIDATE" "\$SUDOERS_MAIN"\s*$`)
)

// "firstboot writes /etc/sudoers.d/<user> only when the build asked for
// passwordless sudo, and only after visudo accepts the candidate": the
// same three-part shape (presence, then order) as the other invariants
// in this section. A build that never sets Sudo getting no such write is
// already covered at the Conf level by TestDefaultConfigConfCarriesNoSudo
// and structurally by MQG_FB_SUDO defaulting to 0 in this very file. The
// install line also pins mode and owner: TestFirstbootShInstallsSudoersFilesInOneStep
// below is the test that a chmod/chown pair never crept back in.
func TestFirstbootShWritesSudoersOnlyWhenRequested(t *testing.T) {
	b := firstbootSh(t)
	guard, visudo, write := lineOf(b, sudoersGuardRe), lineOf(b, sudoersVisudoRe), lineOf(b, sudoersWriteRe)
	if guard < 0 || visudo < 0 || write < 0 {
		t.Fatalf("guard at line %d, visudo check at line %d, sudoers write at line %d; want all three", guard+1, visudo+1, write+1)
	}
	if !(guard < visudo && visudo < write) {
		t.Fatalf("want the MQG_FB_SUDO guard (line %d) before visudo -cf (line %d) before the write (line %d)", guard+1, visudo+1, write+1)
	}
}

// "firstboot never installs a sudoers file (main or fragment) with a
// separate chmod/chown after the copy": both installs above use `install
// -m 0440 -o root -g wheel` in one step, never followed by chmod/chown
// of that same path, which would leave a window with the wrong
// permissions in place.
func TestFirstbootShInstallsSudoersFilesInOneStep(t *testing.T) {
	b := firstbootSh(t)
	for _, bad := range []string{
		`chmod 440 "$SUDOERS_FILE"`, `chown root:wheel "$SUDOERS_FILE"`,
		`chmod 440 "$SUDOERS_MAIN"`, `chown root:wheel "$SUDOERS_MAIN"`,
	} {
		if bytes.Contains(b, []byte(bad)) {
			t.Errorf("firstboot.sh still contains a separate %q; the mode/owner belong in the install line", bad)
		}
	}
}

// "firstboot checks the WHOLE candidate /etc/sudoers -- not just the
// line it added -- before ever installing it": copy the live file aside,
// append the include line to the copy, run visudo -cf over the copy in
// full, and only then install it back over /etc/sudoers, in that order.
func TestFirstbootShChecksTheWholeSudoersFileBeforeInstallingIt(t *testing.T) {
	b := firstbootSh(t)
	cp := lineOf(b, sudoersMainCopyRe)
	appendLine := lineOf(b, sudoersMainAppendRe)
	visudo := lineOf(b, sudoersMainVisudoRe)
	install := lineOf(b, sudoersMainInstallRe)
	if cp < 0 || appendLine < 0 || visudo < 0 || install < 0 {
		t.Fatalf("copy at line %d, append at line %d, visudo -cf at line %d, install at line %d; want all four", cp+1, appendLine+1, visudo+1, install+1)
	}
	if !(cp < appendLine && appendLine < visudo && visudo < install) {
		t.Fatalf("want copy (line %d) before append (line %d) before visudo -cf the WHOLE candidate (line %d) before install (line %d)", cp+1, appendLine+1, visudo+1, install+1)
	}
}

// "the fragment is only ever written to a directory /etc/sudoers is
// confirmed to include": a fragment sudo never reads is a fragment that
// does nothing, silently, so the write is gated on the same include-line
// check that decided whether to add it.
func TestFirstbootShWritesTheFragmentOnlyIfSudoersIncludesTheDirectory(t *testing.T) {
	b := firstbootSh(t)
	includeCheck := lineOf(b, regexp.MustCompile(`^\s*if \[ -f "\$SUDOERS_MAIN" \] && grep -qF "\$SUDOERS_INCLUDE" "\$SUDOERS_MAIN"`))
	write := lineOf(b, sudoersWriteRe)
	if includeCheck < 0 || write < 0 {
		t.Fatalf("include check at line %d, fragment write at line %d; want both", includeCheck+1, write+1)
	}
	if includeCheck >= write {
		t.Fatalf("want the include check (line %d) before the fragment write (line %d)", includeCheck+1, write+1)
	}
}

// "no file under assets/guest/ carries a public key either": the key is a
// build-time parameter, and one committed by accident would grant its
// holder every guest this plugin ever builds. Checked over everything
// embedded, not just assets/guest/, since that is the guarantee that
// matters and it costs nothing extra.
//
// assets/vagrant/ is the deliberate exception: it exists to carry
// Vagrant's own published, public-by-design keypair (its README.md), and
// VagrantPublicKey/VagrantPrivateKey are what the rest of this file's
// guarantee is protecting everything else from becoming.
func TestNoEmbeddedAssetCarriesAPublicKey(t *testing.T) {
	err := fs.WalkDir(vmguest.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(path, "assets/vagrant/") {
			return nil
		}
		b, err := fs.ReadFile(vmguest.Files, path)
		if err != nil {
			return err
		}
		if publicKeyBlobRe.Match(b) {
			t.Errorf("%s contains what looks like a public key", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// And the guard itself works: it must actually recognise a key, not
	// merely the words that describe one.
	if !publicKeyBlobRe.Match([]byte("ssh-rsa AAAAB3NzaC1yc2EAAAA notarealkey\n")) {
		t.Fatal("the public-key guard does not recognise a planted key")
	}
}

// "the postinstall script installs the daemon and the script on the
// target": TestTheBuiltPostinstallInstallsThePayloadOnATargetOffline
// already runs it end to end; this is the one static assertion that is
// not implied by that -- postinstall reads $3, the target volume, which a
// test driving it with a fixed argument list would not catch regressing.
func TestPostinstallReadsTheTargetVolumeArgument(t *testing.T) {
	b, err := fs.ReadFile(vmguest.Files, "assets/guest/postinstall")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("LaunchDaemons")) {
		t.Error("postinstall never mentions LaunchDaemons")
	}
	if !bytes.Contains(b, []byte("$3")) {
		t.Error("postinstall never reads $3, the target volume")
	}
}

// "the firstboot LaunchDaemon plist is valid and runs at load"
func TestTheFirstbootLaunchDaemonPlistRunsAtLoad(t *testing.T) {
	b, err := fs.ReadFile(vmguest.Files, "assets/guest/com.mqg.firstboot.plist")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("firstboot.sh")) {
		t.Error("com.mqg.firstboot.plist's ProgramArguments never names firstboot.sh")
	}
	if !regexp.MustCompile(`(?s)<key>RunAtLoad</key>\s*<true/>`).Match(b) {
		t.Error("com.mqg.firstboot.plist does not set RunAtLoad true")
	}
}

// --- the guest's OpenSSH and Apple's updates, as the guest runs them ------
//
// What assets/guest/firstboot.sh and assets/guest/postinstall say about
// the guest's OpenSSH and Apple's updates, as line-order and regexp
// checks of the embedded bytes. The fetchers and the package builder are
// tested by internal/fetch's openssh/updates tests, TestPackageRules and
// the golden postinstall tests above; the Renovate test is
// TestRenovateTracksTheOpenSSHPin at the repository root.

// postinstallSh is assets/guest/postinstall, as the binary carries it.
func postinstallSh(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(vmguest.Files, "assets/guest/postinstall")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// lineOf is the 0-based index of the first line of b matching re, or -1.
func lineOf(b []byte, re *regexp.Regexp) int {
	for i, line := range strings.Split(string(b), "\n") {
		if re.MatchString(line) {
			return i
		}
	}
	return -1
}

var (
	installBaseRe    = regexp.MustCompile(`installer -verbose -pkg "\$fb_base"`)
	installReplaceRe = regexp.MustCompile(`installer -verbose -pkg "\$fb_replace"`)
	keygenCallRe     = regexp.MustCompile(`^\s*write_sshd_keygen_wrapper\s*$`)
	restoreCallRe    = regexp.MustCompile(`^\s*restore_vanilla_openssh\s*$`)
	installUpdateRe  = regexp.MustCompile(`-pkg "\$CONF_DIR/updates/\$_u"`)
	// The verbs that reach Apple's servers: list, install, download, all,
	// recommended. `softwareupdate --schedule off` is the opposite act (it
	// stops the installed system phoning home on a timer), so this forbids
	// the verbs, not the word.
	softwareupdateVerbRe = regexp.MustCompile(`softwareupdate\s+(-[ildar]|--install|--list|--download|--all|--recommended)`)
	softwareupdateWordRe = regexp.MustCompile(`\bsoftwareupdate\b`)
	scheduleOffRe        = regexp.MustCompile(`^\s*run_with_timeout [0-9]+ softwareupdate --schedule off\s*$`)
)

// "firstboot installs the base package before the replacement": the
// replacement symlinks the system paths at files the base package lays
// down, so the other order leaves them dangling.
func TestFirstbootShInstallsTheBasePackageBeforeTheReplacement(t *testing.T) {
	b := firstbootSh(t)
	base, replace := lineOf(b, installBaseRe), lineOf(b, installReplaceRe)
	if base < 0 || replace < 0 {
		t.Fatalf("firstboot.sh installs base at line %d, replacement at line %d; want both", base+1, replace+1)
	}
	if base >= replace {
		t.Fatalf("firstboot.sh installs the replacement (line %d) before the base package (line %d)", replace+1, base+1)
	}
}

// "firstboot writes the sshd-keygen-wrapper the replacement package
// lacks": 10.9's ssh.plist execs /usr/libexec/sshd-keygen-wrapper, which
// the replacement symlinks into /usr/local but does not ship. Written
// BEFORE the replacement runs, so the symlink lands on a real file.
func TestFirstbootShWritesTheKeygenWrapperBeforeTheReplacement(t *testing.T) {
	b := firstbootSh(t)
	if n := bytes.Count(b, []byte("write_sshd_keygen_wrapper")); n < 2 {
		t.Fatalf("write_sshd_keygen_wrapper appears %d time(s); want it defined and called", n)
	}
	call, replace := lineOf(b, keygenCallRe), lineOf(b, installReplaceRe)
	if call < 0 || replace < 0 {
		t.Fatalf("wrapper call at line %d, replacement install at line %d; want both", call+1, replace+1)
	}
	if call >= replace {
		t.Fatalf("the wrapper is written (line %d) after the replacement installs (line %d)", call+1, replace+1)
	}
	if base := lineOf(b, installBaseRe); call <= base {
		t.Fatalf("the wrapper is written (line %d) before the base package installs (line %d)", call+1, base+1)
	}
}

// "firstboot generates a modern host key set, not just Apple's three":
// Apple's wrapper makes only rsa1/rsa/dsa in /etc, every one of which a
// 2026 client refuses.
func TestFirstbootShGeneratesAModernHostKeySet(t *testing.T) {
	b := firstbootSh(t)
	if !bytes.Contains(b, []byte("ed25519")) {
		t.Error("firstboot.sh never mentions ed25519")
	}
	if n := bytes.Count(b, []byte("ssh_host_${_t}_key")); n < 2 {
		t.Errorf("ssh_host_${_t}_key appears %d time(s); want the wrapper's loop and firstboot's own", n)
	}
}

// "a broken OpenSSH replacement rolls back instead of stranding the
// guest": the replacement's preinstall backs the vanilla binaries up, so
// the rollback is a copy -- and it must be called, not just defined.
func TestFirstbootShRollsBackABrokenReplacement(t *testing.T) {
	b := firstbootSh(t)
	if n := bytes.Count(b, []byte("restore_vanilla_openssh")); n < 2 {
		t.Errorf("restore_vanilla_openssh appears %d time(s); want it defined and called", n)
	}
	if lineOf(b, restoreCallRe) < 0 {
		t.Error("firstboot.sh never calls restore_vanilla_openssh")
	}
	if !bytes.Contains(b, []byte("/var/backups/vanilla-openssh")) {
		t.Error("firstboot.sh never names /var/backups/vanilla-openssh")
	}
}

// "firstboot.sh installs the updates BEFORE the family's OpenSSH":
// Security Update 2016-004 carries ./usr/bin/ssh and ./usr/sbin/sshd, and
// installed after the System-Replace package it would overwrite that
// package's symlinks.
func TestFirstbootShInstallsUpdatesBeforeTheFamilysOpenSSH(t *testing.T) {
	b := firstbootSh(t)
	upd, base := lineOf(b, installUpdateRe), lineOf(b, installBaseRe)
	if upd < 0 || base < 0 {
		t.Fatalf("update install at line %d, OpenSSH base install at line %d; want both", upd+1, base+1)
	}
	if upd >= base {
		t.Fatalf("firstboot.sh installs the updates (line %d) after the OpenSSH base package (line %d)", upd+1, base+1)
	}
}

// "the update packages land in their own directory, not among the
// OpenSSH ones": firstboot.sh classifies pkgs/*.pkg by name, and an
// update package there would be handed to installer as the base OpenSSH.
func TestPostinstallCarriesUpdatesToTheirOwnDirectory(t *testing.T) {
	b := postinstallSh(t)
	if !bytes.Contains(b, []byte(`carry_pkgs "$CONF_DIR/updates" $MQG_FB_UPDATE_PKGS`)) {
		t.Error(`postinstall does not carry $MQG_FB_UPDATE_PKGS into "$CONF_DIR/updates"`)
	}
	if n := bytes.Count(b, []byte(`carry_pkgs "$CONF_DIR/pkgs"`)); n != 1 {
		t.Errorf(`carry_pkgs "$CONF_DIR/pkgs" appears %d time(s); want exactly 1 (the OpenSSH pair only)`, n)
	}
}

// "firstboot.sh records a receipt and a build number, not an installer
// exit code": sw_vers still says 10.9.5 after 2016-004; pkgutil's receipt
// list and ProductBuildVersion are the witnesses that move.
func TestFirstbootShRecordsAReceiptAndABuildNumber(t *testing.T) {
	b := firstbootSh(t)
	if !bytes.Contains(b, []byte("pkgutil --pkgs")) {
		t.Error("firstboot.sh never records pkgutil --pkgs")
	}
	if n := bytes.Count(b, []byte("sw_vers -buildVersion")); n < 2 {
		t.Errorf("sw_vers -buildVersion appears %d time(s); want a before and an after", n)
	}
}

// "nothing anywhere asks softwareupdate to list, download or install":
// that would reach Apple's servers during the build, and a 2013 OS
// talking to 2026 servers may hang. Every script the binary carries, and
// every line -- a run_with_timeout in front does not make it allowed. The
// one invocation there is, `softwareupdate --schedule off`, is checked
// to be exactly that. The rest of the tree is
// TestNothingInTheTreeRunsSoftwareupdate at the repository root.
func TestNoGuestScriptAsksSoftwareupdateForAnything(t *testing.T) {
	scripts := 0
	err := fs.WalkDir(vmguest.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".sh") && filepath.Base(path) != "postinstall" {
			return nil
		}
		scripts++
		b, err := fs.ReadFile(vmguest.Files, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if softwareupdateVerbRe.MatchString(line) {
				t.Errorf("%s:%d asks softwareupdate for something: %s", path, i+1, line)
			}
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") || !softwareupdateWordRe.MatchString(code) {
				continue
			}
			if !scheduleOffRe.MatchString(line) {
				t.Errorf("%s:%d runs softwareupdate as something other than --schedule off: %s", path, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scripts < 3 {
		t.Fatalf("checked %d embedded scripts; the walk is not finding them", scripts)
	}
	// The guard recognises the thing it forbids, wrapped or not.
	for _, bad := range []string{"softwareupdate -i -a", "run_with_timeout 600 softwareupdate --install --all"} {
		if !softwareupdateVerbRe.MatchString(bad) {
			t.Fatalf("the softwareupdate guard misses %q", bad)
		}
	}
}

// "the payload records the update packages in install order": firstboot.sh
// installs them in the order MQG_FB_UPDATE_PKGS names them, and
// 2016-004 must go on first.
func TestTheConfRecordsUpdatePackagesInInstallOrder(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.UpdatePkgs = []MediaFile{
		{Path: fakePkg(t, dir, "a.pkg"), Name: "mqg-update-01-a.pkg"},
		{Path: fakePkg(t, dir, "b.pkg"), Name: "mqg-update-02-b.pkg"},
	}
	c.Updates = "all"
	conf, err := Conf(c)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^MQG_FB_UPDATE_PKGS=.*mqg-update-01-a\.pkg.*mqg-update-02-b\.pkg`).Match(conf) {
		t.Fatalf("conf does not name the update packages in install order:\n%s", conf)
	}
}

// --- Vagrant defaults ---------------------------------------------------
//
// VagrantDefaults, VagrantPublicKey and VagrantPrivateKey read Vagrant's
// own insecure keypair from assets/vagrant/ (README.md there says why
// they exist and why TestNoEmbeddedAssetCarriesAPublicKey above is
// scoped away from that directory).

// TestVagrantDefaultsLeavesSSHKeyEmpty: VagrantDefaults must be a
// pure function of no arguments, the same shape as DefaultConfig, so it
// cannot itself set SSHKey to a path (there is no path to give it
// without writing a file, which is exactly what it must not do). A
// caller that means to Build with VagrantDefaults writes
// VagrantPublicKey() to a path of its own and sets SSHKey there, the
// same as it would for any other authorized key.
func TestVagrantDefaultsLeavesSSHKeyEmpty(t *testing.T) {
	c := VagrantDefaults()
	if c.SSHKey != "" {
		t.Fatalf("VagrantDefaults().SSHKey = %q, want empty", c.SSHKey)
	}
}

// TestVagrantDefaultsCreatesNoFile: the other half -- calling
// VagrantDefaults, repeatedly, must leave os.TempDir() exactly as it
// found it: a temp file per call would have no owner to remove it.
//
// os.TempDir() is pointed at a directory of this test's own first: the
// shared one gains and loses entries whenever another package's tests
// run beside this one (`go test ./...`), which made the count flaky.
func TestVagrantDefaultsCreatesNoFile(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	before, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Skipf("cannot read os.TempDir(): %v", err)
	}
	for i := 0; i < 5; i++ {
		_ = VagrantDefaults()
	}
	after, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("os.TempDir() held %d entries before VagrantDefaults(), %d after 5 calls: it must create no file", len(before), len(after))
	}
}

func TestVagrantDefaultsSetsTheVagrantAccountAndSudo(t *testing.T) {
	c := VagrantDefaults()
	if c.User != "vagrant" {
		t.Errorf("User = %q, want vagrant", c.User)
	}
	if c.UID != 501 {
		t.Errorf("UID = %d, want 501", c.UID)
	}
	if c.RealName != "Vagrant" {
		t.Errorf("RealName = %q, want Vagrant", c.RealName)
	}
	if !c.Sudo {
		t.Error("Sudo is false, want true")
	}
}

// TestVagrantAccountNamesACustomUserAfterItself: a custom user's full
// name is the user name, not "Vagrant"; Vagrant's own user keeps
// "Vagrant", and VagrantAccount("vagrant") is VagrantDefaults exactly.
func TestVagrantAccountNamesACustomUserAfterItself(t *testing.T) {
	c := VagrantAccount("alice")
	if c.User != "alice" || c.RealName != "alice" {
		t.Errorf("VagrantAccount(alice): User %q, RealName %q, want alice, alice", c.User, c.RealName)
	}
	want := VagrantDefaults()
	want.User, want.RealName = "alice", "alice"
	if !reflect.DeepEqual(c, want) {
		t.Errorf("VagrantAccount(alice) = %+v, want VagrantDefaults but for the account: %+v", c, want)
	}
	if got := VagrantAccount("vagrant"); !reflect.DeepEqual(got, VagrantDefaults()) {
		t.Errorf("VagrantAccount(vagrant) = %+v, want VagrantDefaults() %+v", got, VagrantDefaults())
	}
}

// TestVagrantDefaultsValidatesWithVagrantPublicKey: VagrantDefaults' own
// fields -- Sudo true, no OpenSSH packages -- plus VagrantPublicKey
// written to a path of the test's own choosing, must pass validateConfig
// as a whole: a caller wiring the two together (as the media data
// source does) gets a config that just works.
func TestVagrantDefaultsValidatesWithVagrantPublicKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "vagrant.pub")
	if err := os.WriteFile(keyPath, VagrantPublicKey(), 0o644); err != nil {
		t.Fatal(err)
	}
	c := VagrantDefaults()
	c.SSHKey = keyPath
	if _, err := validateConfig(c, t.Logf); err != nil {
		t.Fatalf("VagrantDefaults() + VagrantPublicKey() does not validate: %v", err)
	}
}

// TestVagrantPublicKeyIsRSAOnly: validateKey refuses an Ed25519
// authorized-key line outright when the guest has no replacement OpenSSH
// (VagrantDefaults' own OpenSSHPkgs is empty), so this must never carry
// the Ed25519 half of Vagrant's published pair -- only carrying both
// would silently break TestVagrantDefaultsValidatesWithVagrantPublicKey's
// own guarantee the moment someone "helpfully" widened it.
func TestVagrantPublicKeyIsRSAOnly(t *testing.T) {
	key := VagrantPublicKey()
	if !bytes.HasPrefix(key, []byte("ssh-rsa ")) {
		head := key
		if len(head) > 32 {
			head = head[:32]
		}
		t.Fatalf("VagrantPublicKey() does not start with ssh-rsa: %q", head)
	}
	if bytes.Contains(key, []byte("ssh-ed25519")) {
		t.Fatal("VagrantPublicKey() also carries an Ed25519 line, which stock OpenSSH 6.2 cannot use")
	}
}

// TestVagrantPrivateKeyMatchesVagrantPublicKey: the private key
// VagrantPrivateKey returns must actually be the private half of
// VagrantPublicKey -- proven by parsing both and comparing the derived
// public key, not just by two files being copied from the same place.
func TestVagrantPrivateKeyMatchesVagrantPublicKey(t *testing.T) {
	priv := VagrantPrivateKey()
	if !bytes.Contains(priv, []byte("PRIVATE KEY")) {
		t.Fatalf("VagrantPrivateKey() does not look like a private key: %q", priv)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey: %v", err)
	}

	pub, _, _, _, err := ssh.ParseAuthorizedKey(VagrantPublicKey())
	if err != nil {
		t.Fatalf("ssh.ParseAuthorizedKey: %v", err)
	}

	if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		t.Fatal("VagrantPrivateKey() is not the private half of VagrantPublicKey()")
	}
}
