package media

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx"
)

const firstbootEntry = "/System/Installation/Packages/guest-macosx-firstboot.pkg"

var updateGoldens = flag.Bool("update", false, "rewrite testdata/golden/autoinstall-snowleopard.diff from the code's output (see testdata/golden/README.md)")

func embedded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(macosx.Files, name)
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
		"private/etc/rc.cdrom.local":                              {0o755, string(embedded(t, "assets/guest/autoinstall/autoinstall.sh"))},
		"System/Installation/Packages/Extras/minstallconfig.xml":  {0o644, string(embedded(t, "assets/guest/autoinstall/minstallconfig.xml"))},
		"System/Installation/Packages/OSInstall.collection":       {0o644, string(collection)},
		"System/Installation/Packages/guest-macosx-firstboot.pkg": {0o644, "xar!firstboot"},
		"System/Installation/Packages/openssh.pkg":                {0o644, "xar!a"},
		"System/Installation/Packages/other.pkg":                  {0o644, "xar!b"},
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
		{Injectables{FirstbootPkg: p}, true, "System/Installation/Packages/guest-macosx-firstboot.pkg"},
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

// hooks is a release's three unattended-install hooks as WriteTar puts
// them on its media, by their source under assets/guest/autoinstall/.
func hooks(t *testing.T, release string) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	if err := (Injectables{Autoinstall: true, Release: release}).WriteTar(&buf, nil); err != nil {
		t.Fatal(err)
	}
	tarred := readTar(t, buf.Bytes())
	got := map[string]string{}
	for _, f := range AutoinstallFilesFor(release) {
		e, ok := tarred[f.Dest]
		if !ok {
			t.Fatalf("release %q: %s (%s) is not in the tar", release, f.Dest, f.Source)
		}
		got[f.Source] = e.data
	}
	return got
}

// The releases WriteTar knows, and the volume each installs onto: the
// name the installed disk carries for good, as Finder and diskutil show
// it.
var releaseVolumes = []struct{ release, volume string }{
	{"", "Mavericks"},
	{"mavericks", "Mavericks"},
	{"snowleopard", "Snow Leopard"},
}

func TestEachReleaseInstallsOntoAVolumeNamedForIt(t *testing.T) {
	for _, rv := range releaseVolumes {
		if got := TargetVolume(rv.release); got != rv.volume {
			t.Errorf("TargetVolume(%q) = %q, want %q", rv.release, got, rv.volume)
		}
	}
}

// "the volume autoinstall.sh creates is the volume the installer targets":
// two files name the target volume independently -- minstallconfig.xml's
// Target and TargetName, and autoinstall.sh's VOLNAME default. If they
// drift, the install runs against a volume that does not exist and
// reboots in a loop. Asked of the assets themselves and of every
// release's hooks as its media carries them, each naming its release's
// volume.
func TestAutoinstallShTargetsTheVolumeMinstallconfigNames(t *testing.T) {
	agree := func(what, conf, sh, volume string) {
		t.Helper()
		target := plistDictString([]byte(conf), "Target")
		name := plistDictString([]byte(conf), "TargetName")
		if name != volume {
			t.Errorf("%s: minstallconfig.xml's TargetName = %q, want %q", what, name, volume)
		}
		if want := "/Volumes/" + name; target != want {
			t.Errorf("%s: Target = %q, want %q", what, target, want)
		}
		want := "VOLNAME=${GUEST_MACOSX_TARGET_VOLUME:-" + name + "}"
		if n := strings.Count(sh, want); n != 1 {
			t.Errorf("%s: autoinstall.sh has %d occurrences of %q, want 1", what, n, want)
		}
	}
	agree("the assets", string(embedded(t, "assets/guest/autoinstall/minstallconfig.xml")),
		string(embedded(t, "assets/guest/autoinstall/autoinstall.sh")), "Mavericks")
	for _, rv := range releaseVolumes {
		h := hooks(t, rv.release)
		agree("release "+strconv.Quote(rv.release), h["minstallconfig.xml"], h["autoinstall.sh"], rv.volume)
	}
}

// 10.9's hooks are the assets byte for byte: naming 10.6's volume changes
// nothing on 10.9's media.
func TestMavericksHooksAreTheAssetsByteForByte(t *testing.T) {
	for _, rel := range []string{"", "mavericks"} {
		for src, data := range hooks(t, rel) {
			if want := string(embedded(t, "assets/guest/autoinstall/"+src)); data != want {
				t.Errorf("release %q: %s differs from its asset", rel, src)
			}
		}
	}
}

// hookChanges is what a release's hooks change from the assets, line by
// line: each file in AutoinstallFilesFor's order, then each line that
// differs, as the asset has it (-) and as the media does (+). Naming the
// volume replaces lines, never adds or removes them, so a file whose line
// count moved is reported as that.
func hookChanges(t *testing.T, release string) string {
	t.Helper()
	h := hooks(t, release)
	var b strings.Builder
	for _, f := range AutoinstallFilesFor(release) {
		asset := strings.SplitAfter(string(embedded(t, "assets/guest/autoinstall/"+f.Source)), "\n")
		got := strings.SplitAfter(h[f.Source], "\n")
		fmt.Fprintf(&b, "%s, from %s:", f.Dest, f.Source)
		if len(asset) != len(got) {
			fmt.Fprintf(&b, " %d lines, not the asset's %d\n", len(got), len(asset))
			continue
		}
		changed := false
		for i := range asset {
			if asset[i] != got[i] {
				if !changed {
					b.WriteString("\n")
				}
				changed = true
				fmt.Fprintf(&b, "line %d\n-%s+%s", i+1, asset[i], got[i])
			}
		}
		if !changed {
			b.WriteString(" unchanged\n")
		}
	}
	return b.String()
}

// 10.6's hooks change exactly the lines naming the volume, and nothing
// else: testdata/golden/autoinstall-snowleopard.diff, rewritten by -update.
func TestSnowLeopardHooksMatchTheirGolden(t *testing.T) {
	got := hookChanges(t, "snowleopard")
	golden := filepath.Join("testdata", "golden", "autoinstall-snowleopard.diff")
	if *updateGoldens {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("10.6's hooks change\n%s\nwant (%s)\n%s", got, golden, want)
	}
}

// A volume name with a space is one argument only where it is quoted:
// every expansion of $VOLNAME in autoinstall.sh, outside comments, is
// inside double quotes (an odd number of them before it on its line).
func TestAutoinstallShQuotesEveryVolumeName(t *testing.T) {
	for i, line := range strings.Split(string(embedded(t, "assets/guest/autoinstall/autoinstall.sh")), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for rest, at := line, 0; ; {
			j := strings.Index(rest, "$VOLNAME")
			if j < 0 {
				break
			}
			if strings.Count(line[:at+j], `"`)%2 != 1 {
				t.Errorf("autoinstall.sh line %d expands $VOLNAME unquoted: %s", i+1, line)
			}
			at += j + len("$VOLNAME")
			rest = line[at:]
		}
	}
}

// 10.6's autoinstall.sh, as its media carries it, runs under a POSIX sh
// and bash (10.6's /bin/sh is bash 3.2) and names its volume in full: a
// dry run against a stub diskutil with one empty 20 GiB disk picks that
// disk and says what it will name the volume.
func TestSnowLeopardAutoinstallShNamesItsVolume(t *testing.T) {
	dir := t.TempDir()
	script := writeFile(t, filepath.Join(dir, "rc.cdrom.local"), hooks(t, "snowleopard")["autoinstall.sh"])
	diskutil, err := os.ReadFile(filepath.Join("testdata", "diskutil-one-disk.sh"))
	if err != nil {
		t.Fatal(err)
	}
	stub := writeFile(t, filepath.Join(dir, "diskutil"), string(diskutil))
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"sh", "bash"} {
		need(t, shell)
		cmd := exec.Command(shell, script)
		cmd.Env = append(os.Environ(), "GUEST_MACOSX_AUTOINSTALL_DRY_RUN=1", "GUEST_MACOSX_DISKUTIL="+stub,
			"GUEST_MACOSX_AUTOINSTALL_LOG="+filepath.Join(dir, shell+".log"), "GUEST_MACOSX_TARGET_WAIT=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s", shell, err, stderr.String())
		}
		if got := strings.TrimSpace(string(out)); got != "disk1" {
			t.Errorf("%s: picked %q, want disk1\n%s", shell, got, stderr.String())
		}
		if want := "target volume will be named Snow Leopard\n"; !strings.Contains(stderr.String(), want) {
			t.Errorf("%s: never says %q:\n%s", shell, want, stderr.String())
		}
	}
}

// A hook that no longer names the volume as nameVolume expects, or a name
// either file would need escaped, fails the build rather than making
// media whose installer and autoinstall.sh disagree.
func TestNameVolumeRefusesWhatItCannotNameSafely(t *testing.T) {
	sh := embedded(t, "assets/guest/autoinstall/autoinstall.sh")
	for _, bad := range []string{"", "Snow  Leopard", " Snow", "Snow\"Leopard", "A&B", "x}y", "a$b"} {
		if _, err := nameVolume("autoinstall.sh", sh, bad); err == nil {
			t.Errorf("volume %q: no error", bad)
		}
	}
	gone := bytes.Replace(sh, []byte("VOLNAME=${GUEST_MACOSX_TARGET_VOLUME:-Mavericks}"), []byte("VOLNAME=Mavericks"), 1)
	if _, err := nameVolume("autoinstall.sh", gone, "Snow Leopard"); err == nil || !strings.Contains(err.Error(), "0 times") {
		t.Errorf("an autoinstall.sh without its VOLNAME line: err = %v", err)
	}
	conf := embedded(t, "assets/guest/autoinstall/minstallconfig.xml")
	twice := append(append([]byte(nil), conf...), "<key>TargetName</key>\n\t<string>Mavericks</string>\n"...)
	if _, err := nameVolume("minstallconfig.xml", twice, "Snow Leopard"); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("a minstallconfig.xml naming TargetName twice: err = %v", err)
	}
}
