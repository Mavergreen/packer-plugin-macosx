package media

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
)

const firstbootEntry = "/System/Installation/Packages/mqg-firstboot.pkg"

func embedded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(vmguest.Files, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAddToCollection(t *testing.T) {
	orig := embedded(t, "assets/guest/autoinstall/OSInstall.collection")
	got, n, err := AddToCollection(orig, firstbootEntry)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(orig, []byte("</array>"))
	want := string(orig[:i]) + "\t<string>" + firstbootEntry + "</string>\n" + string(orig[i:])
	if string(got) != want {
		t.Fatalf("collection\n%s\nwant\n%s", got, want)
	}
	if n != 3 {
		t.Fatalf("count %d, want 3", n)
	}

	again, n2, err := AddToCollection(got, firstbootEntry)
	if err != nil || !bytes.Equal(again, got) || n2 != 3 {
		t.Fatalf("adding it again: %d, %v, changed=%v", n2, err, !bytes.Equal(again, got))
	}

	if _, _, err := AddToCollection([]byte("<plist version=\"1.0\"><array/></plist>\n"), firstbootEntry); err == nil ||
		!strings.Contains(err.Error(), "</array>") {
		t.Fatalf("no </array>: err = %v", err)
	}

	unclosed := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\">\n<array>\n\t<string>/a.pkg</string>\n</plist>\n</array>\n"
	if _, _, err := AddToCollection([]byte(unclosed), firstbootEntry); err == nil ||
		!strings.Contains(err.Error(), "does not parse") {
		t.Fatalf("an <array> never closed before </plist>: err = %v", err)
	}
}

type entry struct {
	mode int64
	data string
}

// readTar is a tar's entries by name, checking what every entry must be:
// a regular file, owned by 0:0, with the fixed mtime.
func readTar(t *testing.T, b []byte) map[string]entry {
	t.Helper()
	got := map[string]entry{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			t.Errorf("%s: type %q, want a regular file", h.Name, h.Typeflag)
		}
		if h.Uid != 0 || h.Gid != 0 {
			t.Errorf("%s: owner %d:%d, want 0:0", h.Name, h.Uid, h.Gid)
		}
		if !h.ModTime.Equal(time.Unix(0, 0)) {
			t.Errorf("%s: mtime %v, want the epoch", h.Name, h.ModTime)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := got[h.Name]; dup {
			t.Errorf("%s is in the tar twice", h.Name)
		}
		got[h.Name] = entry{h.Mode, string(data)}
	}
}

func writeFile(t *testing.T, path, data string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheInjectablesTar(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, filepath.Join(dir, "firstboot-built.pkg"), "xar!firstboot")
	a := writeFile(t, filepath.Join(dir, "a", "openssh.pkg"), "xar!a")
	b := writeFile(t, filepath.Join(dir, "b", "other.pkg"), "xar!b")
	var buf bytes.Buffer
	if err := (Injectables{Autoinstall: true, FirstbootPkg: p, ExtraPkgs: []string{a, b}}).WriteTar(&buf, nil); err != nil {
		t.Fatal(err)
	}
	collection, _, err := AddToCollection(embedded(t, "assets/guest/autoinstall/OSInstall.collection"), firstbootEntry)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]entry{
		"private/etc/rc.cdrom.local":                             {0o755, string(embedded(t, "assets/guest/autoinstall/autoinstall.sh"))},
		"System/Installation/Packages/Extras/minstallconfig.xml": {0o644, string(embedded(t, "assets/guest/autoinstall/minstallconfig.xml"))},
		"System/Installation/Packages/OSInstall.collection":      {0o644, string(collection)},
		"System/Installation/Packages/mqg-firstboot.pkg":         {0o644, "xar!firstboot"},
		"System/Installation/Packages/openssh.pkg":               {0o644, "xar!a"},
		"System/Installation/Packages/other.pkg":                 {0o644, "xar!b"},
	}
	got := readTar(t, buf.Bytes())
	if len(got) != len(want) {
		var names []string
		for n := range got {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Fatalf("the tar holds %q", names)
	}
	for name, w := range want {
		g, ok := got[name]
		switch {
		case !ok:
			t.Errorf("%s is not in the tar", name)
		case g.mode != w.mode:
			t.Errorf("%s: mode %o, want %o", name, g.mode, w.mode)
		case g.data != w.data:
			t.Errorf("%s: contents\n%s\nwant\n%s", name, g.data, w.data)
		}
	}

	buf.Reset()
	if err := (Injectables{Autoinstall: true}).WriteTar(&buf, nil); err != nil {
		t.Fatal(err)
	}
	got = readTar(t, buf.Bytes())
	if c := got["System/Installation/Packages/OSInstall.collection"].data; c != string(embedded(t, "assets/guest/autoinstall/OSInstall.collection")) {
		t.Fatalf("without a first-boot package the collection changed:\n%s", c)
	}
	if len(got) != 3 {
		t.Fatalf("without packages the tar holds %d files, want 3", len(got))
	}

	buf.Reset()
	if err := (Injectables{}).WriteTar(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(readTar(t, buf.Bytes())); n != 0 {
		t.Fatalf("without the hooks the tar holds %d files", n)
	}
	if buf.Len() != 1024 || !bytes.Equal(buf.Bytes(), make([]byte, 1024)) {
		t.Fatalf("without the hooks the tar is %d bytes, not a bare end-of-archive", buf.Len())
	}
}

// The guest untars the injectables with busybox's tar, not Go's reader.
func TestBusyboxTarReadsTheInjectables(t *testing.T) {
	need(t, "busybox")
	dir := t.TempDir()
	long := writeFile(t, filepath.Join(dir, strings.Repeat("x", 90)+".pkg"), "xar!long")
	longer := writeFile(t, filepath.Join(dir, strings.Repeat("y", 200)+".pkg"), "xar!longer")
	var buf bytes.Buffer
	if err := (Injectables{Autoinstall: true, ExtraPkgs: []string{long, longer}}).WriteTar(&buf, nil); err != nil {
		t.Fatal(err)
	}
	tarFile := writeFile(t, filepath.Join(dir, "inject.tar"), buf.String())
	out, err := exec.Command("busybox", "tar", "tf", tarFile).CombinedOutput()
	if err != nil {
		t.Fatalf("busybox tar tf: %v\n%s", err, out)
	}
	var names []string
	for n := range readTar(t, buf.Bytes()) {
		names = append(names, n)
	}
	sort.Strings(names)
	listed := strings.Fields(string(out))
	sort.Strings(listed)
	if strings.Join(listed, "\n") != strings.Join(names, "\n") {
		t.Fatalf("busybox tar lists\n%s\nwant\n%s", strings.Join(listed, "\n"), strings.Join(names, "\n"))
	}
}

func TestTwoExtrasWithOneBasenameAreRefused(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, filepath.Join(dir, "a", "same.pkg"), "xar!a")
	b := writeFile(t, filepath.Join(dir, "b", "same.pkg"), "xar!b")
	err := (Injectables{Autoinstall: true, ExtraPkgs: []string{a, b}}).WriteTar(io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) {
		t.Fatalf("err = %v, want one naming %s and %s", err, a, b)
	}
}

// A package is asked for by being named: a first-boot or extra package
// implies the unattended-install hooks, and a tar that dropped the package
// would build media without it and say nothing.
func TestAPackageImpliesAutoinstall(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, filepath.Join(dir, "firstboot.pkg"), "xar!firstboot")
	e := writeFile(t, filepath.Join(dir, "extra.pkg"), "xar!extra")
	for _, tc := range []struct {
		in      Injectables
		enabled bool
		pkg     string
	}{
		{Injectables{}, false, ""},
		{Injectables{Autoinstall: true}, true, ""},
		{Injectables{FirstbootPkg: p}, true, "System/Installation/Packages/mqg-firstboot.pkg"},
		{Injectables{ExtraPkgs: []string{e}}, true, "System/Installation/Packages/extra.pkg"},
	} {
		if got := tc.in.Enabled(); got != tc.enabled {
			t.Errorf("%+v: Enabled() = %v, want %v", tc.in, got, tc.enabled)
		}
		var buf bytes.Buffer
		if err := tc.in.WriteTar(&buf, nil); err != nil {
			t.Fatal(err)
		}
		got := readTar(t, buf.Bytes())
		if tc.enabled != (len(got) > 0) {
			t.Errorf("%+v: the tar holds %d files", tc.in, len(got))
		}
		if _, ok := got["private/etc/rc.cdrom.local"]; tc.enabled && !ok {
			t.Errorf("%+v: the hooks are not in the tar", tc.in)
		}
		if tc.pkg != "" {
			if _, ok := got[tc.pkg]; !ok {
				t.Errorf("%+v: %s is not in the tar", tc.in, tc.pkg)
			}
		}
	}
}

// --- the autoinstall assets themselves ---------------------------------
//
// assets/guest/autoinstall/autoinstall.sh runs inside the installer
// environment, and minstallconfig.xml and OSInstall.collection are the
// OS X Installer's own data files. The tests below check the two plists'
// content as regexp checks of the embedded bytes -- there is no full
// plist decoder in this package, and these files are simple flat
// structures, so one is not needed. autoinstall.sh's own disk-selection
// behaviour, which needs a stub diskutil(1) and a real shell to
// exercise, is tested in tests/guest_assets.bats.

// plistDictString is a flat <dict>'s <key>name</key> value, as the string
// literal immediately following it, or "" if name is not a key in b (every
// file this repository ships writes a key and its value adjacently).
func plistDictString(b []byte, name string) string {
	m := regexp.MustCompile(`(?s)<key>` + regexp.QuoteMeta(name) + `</key>\s*<string>([^<]*)</string>`).FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// "minstallconfig.xml is a valid plist that asks for an automated install"
func TestMinstallconfigAsksForAnAutomatedInstall(t *testing.T) {
	b := embedded(t, "assets/guest/autoinstall/minstallconfig.xml")
	if got := plistDictString(b, "InstallType"); got != "automated" {
		t.Errorf("InstallType = %q, want automated", got)
	}
	if got := plistDictString(b, "Package"); got != "/System/Installation/Packages/OSInstall.collection" {
		t.Errorf("Package = %q", got)
	}
}

// "OSInstall.collection is a valid plist listing OSInstall.mpkg"
func TestOSInstallCollectionListsOSInstallMpkg(t *testing.T) {
	b := embedded(t, "assets/guest/autoinstall/OSInstall.collection")
	if !bytes.Contains(b, []byte("<string>/System/Installation/Packages/OSInstall.mpkg</string>")) {
		t.Error("OSInstall.collection never lists /System/Installation/Packages/OSInstall.mpkg")
	}
}

// "the volume autoinstall.sh creates is the volume the installer targets":
// two files name the target volume independently -- minstallconfig.xml's
// Target and TargetName, and autoinstall.sh's VOLNAME default. If they
// drift, the install runs against a volume that does not exist and
// reboots in a loop.
func TestAutoinstallShTargetsTheVolumeMinstallconfigNames(t *testing.T) {
	conf := embedded(t, "assets/guest/autoinstall/minstallconfig.xml")
	target := plistDictString(conf, "Target")
	name := plistDictString(conf, "TargetName")
	if name == "" {
		t.Fatal("minstallconfig.xml has no TargetName")
	}
	if want := "/Volumes/" + name; target != want {
		t.Errorf("Target = %q, want %q", target, want)
	}

	sh := embedded(t, "assets/guest/autoinstall/autoinstall.sh")
	want := []byte("VOLNAME=${MQG_TARGET_VOLUME:-" + name + "}")
	if n := bytes.Count(sh, want); n != 1 {
		t.Errorf("autoinstall.sh has %d occurrences of %q, want 1", n, want)
	}
}
