package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/config"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/lock"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/privops"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// payloadNames are the media build's microVM payloads, which the fake
// recognises by their bytes.
var payloadNames = []string{"extract-basesystem", "assemble", "fix-ownership", "verify-packages", "content-digest"}

type vmCall struct {
	name, target string
	disks        []privops.Disk
	tar          map[string]entry // pass 2's inject.tar, read during the pass
}

// fakeVM answers each payload as the real guest would, through the
// console and the raw disks.
type fakeVM struct {
	t       *testing.T
	missing []string
	calls   []vmCall

	baseSystem []byte // what extract writes to its raw disk
	reportSHA  string // extract's reported sha, when not the true one
	badESD     string // a package whose ESD sum assemble gets wrong
	dropMedia  string // a package verify does not find
	failPass   string // a payload whose run fails outright

	digest func(disks []privops.Disk) string // content-digest's console
}

func (v *fakeVM) Missing() []string { return v.missing }

func (v *fakeVM) Run(_ context.Context, target string, payload []byte, disks []privops.Disk) ([]byte, error) {
	t := v.t
	name := ""
	for _, n := range payloadNames {
		if bytes.Equal(payload, embedded(t, "assets/privops/"+n+".sh")) {
			name = n
		}
	}
	call := vmCall{name: name, target: target, disks: append([]privops.Disk(nil), disks...)}
	if name == v.failPass {
		v.calls = append(v.calls, call)
		return []byte("[    1.0] Kernel panic\r\n"), errors.New("qemu-system-x86_64: exit status 1")
	}
	var con strings.Builder
	// A serial console's carriage returns and a kernel line, which the
	// markers must be read through.
	con.WriteString("[    0.123456] booting\r\n")
	switch name {
	case "extract-basesystem":
		// The raw disk is as large as the ESD image, as the real one must
		// be to hold whatever BaseSystem.dmg the ESD carries.
		esdSize, rawSize := statSize(t, disks[0].Path), statSize(t, disks[1].Path)
		if rawSize != esdSize {
			t.Errorf("the raw disk is %d bytes and esd.img %d", rawSize, esdSize)
			return nil, errors.New("the raw disk is the wrong size")
		}
		f, err := os.OpenFile(disks[1].Path, os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteAt(v.baseSystem, 0)
		f.Close()
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(v.baseSystem)
		sha := hex.EncodeToString(sum[:])
		if v.reportSHA != "" {
			sha = v.reportSHA
		}
		fmt.Fprintf(&con, "BaseSystem.dmg on the ESD: %d bytes\r\n", len(v.baseSystem))
		fmt.Fprintf(&con, "MQG-BASESYSTEM-BYTES %d\r\nMQG-BASESYSTEM-SHA256 %s\r\n", len(v.baseSystem), sha)
	case "assemble":
		for _, d := range disks {
			if d.Role == "raw" {
				b, err := os.ReadFile(d.Path)
				if err != nil {
					return nil, err
				}
				call.tar = readTar(t, b)
			}
		}
		v.sums(&con, "MQG-SUM-ESD", v.badESD, "")
		writeAt(t, target, assembledAt, "assembled by pass 2")
	case "verify-packages":
		v.sums(&con, "MQG-SUM-MEDIA", "", v.dropMedia)
		// A read-write mount rewrites the HFS+ header, whatever the pass.
		writeAt(t, target, mountedAt, "mounted by pass 4")
	case "content-digest":
		con.WriteString(v.digest(disks))
	case "fix-ownership":
		writeAt(t, target, ownedAt, "owned by pass 3")
	default:
		t.Fatalf("an unknown payload was run on %s", target)
	}
	con.WriteString("MQG-PRIVOPS-OK rc=0\r\n")
	v.calls = append(v.calls, call)
	return []byte(con.String()), nil
}

// sums prints a marker line for every pinned package, as sha256sum
// prints them, with bad's checksum wrong and drop left out.
func (v *fakeVM) sums(w *strings.Builder, marker, bad, drop string) {
	pinned := parseSums(embedded(v.t, "assets/pins/apple-packages.sha256"), true)
	var names []string
	for n := range pinned {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sha := pinned[n]
		switch n {
		case drop:
			continue
		case bad:
			sha = strings.Repeat("0", 64)
		}
		fmt.Fprintf(w, "%s %s  %s\r\n", marker, sha, n)
	}
}

// Where the fake passes 2 and 3 write on the target: inside the
// partition, clear of the volume header at 1 MiB + 1024.
const (
	assembledAt = 2 << 20
	ownedAt     = 2<<20 + 4096
	mountedAt   = 2<<20 + 8192
)

func writeAt(t *testing.T, path string, off int64, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte(data), off); err != nil {
		t.Fatal(err)
	}
}

func statSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func (v *fakeVM) names() []string {
	var n []string
	for _, c := range v.calls {
		n = append(n, c.name)
	}
	return n
}

// fakeTools is a Runner with dmg2img and mkfs.hfsplus on its PATH:
// dmg2img writes a few bytes to its -o -- 64 KiB for esd.img, larger
// than the BaseSystem fixture, so that a basesystem.dmg left at the ESD
// image's size is caught -- and mkfs.hfsplus writes a volume
// signature, after telling mkfs (if set) the size it was given.
func fakeTools(mkfs func(size int64) error) *proc.Fake {
	return &proc.Fake{
		Paths: map[string]string{"dmg2img": "/usr/bin/dmg2img", "mkfs.hfsplus": "/usr/sbin/mkfs.hfsplus"},
		Handle: func(c proc.Cmd) error {
			switch c.Name {
			case "dmg2img":
				if len(c.Args) != 5 || c.Args[0] != "-s" || c.Args[1] != "-i" || c.Args[3] != "-o" {
					return fmt.Errorf("unexpected %s", c)
				}
				data := []byte("raw of " + c.Args[2])
				if filepath.Base(c.Args[4]) == "esd.img" {
					data = bytes.Repeat([]byte("raw ESD "), 8192)
				}
				return os.WriteFile(c.Args[4], data, 0o644)
			case "mkfs.hfsplus":
				f, err := os.OpenFile(c.Args[len(c.Args)-1], os.O_RDWR, 0)
				if err != nil {
					return err
				}
				defer f.Close()
				fi, err := f.Stat()
				if err != nil {
					return err
				}
				if mkfs != nil {
					if err := mkfs(fi.Size()); err != nil {
						return err
					}
				}
				_, err = f.WriteAt([]byte("H+"), 1024)
				return err
			}
			return fmt.Errorf("unexpected command %s", c)
		},
	}
}

type rig struct {
	b    *Builder
	r    *proc.Fake
	vm   *fakeVM
	esd  string
	out  string
	logs []string
}

func (g *rig) logged(s string) bool {
	for _, l := range g.logs {
		if l == s {
			return true
		}
	}
	return false
}

// newRig is a Builder on an empty home, with a small ESD, fake tools, a
// fake microVM and a 4 MiB partition: the real 6759 MiB costs seconds of
// reading holes per build, and only TestThePartitionIsSizedFromTheReference
// needs it.
func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{r: fakeTools(nil)}
	g.vm = &fakeVM{t: t, baseSystem: bytes.Repeat([]byte("BaseSystem fixture "), 1000)}
	paths := config.Paths{Home: t.TempDir()}
	g.esd = writeFile(t, filepath.Join(t.TempDir(), "InstallESD.dmg"), "a small fake ESD")
	g.out = paths.InstallerMedia()
	g.b = &Builder{Paths: paths, Runner: g.r, VM: g.vm, PID: os.Getpid(), baseMiB: 4,
		Log: func(f string, a ...any) { g.logs = append(g.logs, fmt.Sprintf(f, a...)) }}
	return g
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func absent(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there (%v)", p, err)
		}
	}
}

func TestBuildMakesTheMedia(t *testing.T) {
	g := newRig(t)
	out, err := g.b.Build(context.Background(), g.esd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(g.b.Paths.Home, "build", "installer-media.img"); out != want {
		t.Fatalf("Build returned %s, want %s", out, want)
	}
	sidecar, err := os.ReadFile(out + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^# sha256 of installer-media\.img as built at \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ
# Mounting the image invalidates this: HFS\+ records the mount
# in its volume header, and a read-write mount rewrites it\.
` + sha256Of(t, out) + `  installer-media\.img
$`)
	if !re.Match(sidecar) {
		t.Fatalf("the sidecar is\n%s", sidecar)
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "installer-media.img installer-media.img.sha256" {
		t.Fatalf("build/ holds %q", names)
	}
	absent(t, out+".building", out+".lock", g.b.Paths.MediaWork())
	img, _ := os.ReadFile(out)
	for off, want := range map[int]string{assembledAt: "assembled by pass 2", ownedAt: "owned by pass 3", mountedAt: "mounted by pass 4"} {
		if got := string(img[off : off+len(want)]); got != want {
			t.Errorf("the media holds %q at %d, want %q", got, off, want)
		}
	}
	for _, l := range []string{"converting InstallESD.dmg to raw (about 5 GB)",
		"bringing BaseSystem.dmg out of the ESD (microVM pass 1 of 4)",
		"assembling the media inside the microVM (pass 2 of 4)",
		"restoring root ownership (microVM pass 3 of 4)",
		"reading the finished media back in a microVM of its own (pass 4 of 4)",
		fmt.Sprintf("size %d bytes, sha256 %s", 6<<20, sha256Of(t, out))} {
		if !g.logged(l) {
			t.Errorf("not logged: %q", l)
		}
	}
}

func TestPassOrderAndDisks(t *testing.T) {
	for _, withPkg := range []bool{false, true} {
		t.Run(fmt.Sprintf("firstboot=%v", withPkg), func(t *testing.T) {
			g := newRig(t)
			var o Options
			if withPkg {
				// No Autoinstall: the package implies it.
				o.FirstbootPkg = writeFile(t, filepath.Join(t.TempDir(), "fb.pkg"), "xar!the first-boot payload")
			}
			if _, err := g.b.Build(context.Background(), g.esd, o); err != nil {
				t.Fatal(err)
			}
			w := g.b.Paths.MediaWork()
			ro := func(n string) privops.Disk { return privops.Disk{Role: "ro", Path: filepath.Join(w, n)} }
			raw := func(n string) privops.Disk { return privops.Disk{Role: "raw", Path: filepath.Join(w, n)} }
			assemble := []privops.Disk{ro("basesystem.img"), ro("esd.img")}
			if withPkg {
				assemble = append(assemble, raw("inject.tar"))
			}
			want := []vmCall{
				{name: "extract-basesystem", disks: []privops.Disk{ro("esd.img"), raw("basesystem.dmg")}},
				{name: "assemble", disks: assemble},
				{name: "fix-ownership"},
				{name: "verify-packages"},
			}
			if len(g.vm.calls) != len(want) {
				t.Fatalf("passes %q", g.vm.names())
			}
			for i, c := range g.vm.calls {
				if c.name != want[i].name || c.target != g.out+".building" ||
					fmt.Sprint(c.disks) != fmt.Sprint(want[i].disks) {
					t.Errorf("pass %d: %s on %s with %v, want %s on %s.building with %v",
						i+1, c.name, c.target, c.disks, want[i].name, g.out, want[i].disks)
				}
			}
			tar := g.vm.calls[1].tar
			if !withPkg {
				if tar != nil {
					t.Fatal("a tar was given without anything to inject")
				}
				return
			}
			if got := tar["System/Installation/Packages/mqg-firstboot.pkg"]; got.data != "xar!the first-boot payload" {
				t.Fatalf("the first-boot package is not on the media: %+v", got)
			}
			if _, ok := tar["private/etc/rc.cdrom.local"]; !ok {
				t.Fatal("the hooks are not on the media")
			}
		})
	}
}

func TestDmg2imgRuns(t *testing.T) {
	g := newRig(t)
	if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
		t.Fatal(err)
	}
	w := g.b.Paths.MediaWork()
	var got []string
	for _, c := range g.r.Calls {
		if c.Name == "dmg2img" {
			got = append(got, strings.Join(c.Args, " "))
		}
	}
	want := []string{
		"-s -i " + g.esd + " -o " + filepath.Join(w, "esd.img"),
		"-s -i " + filepath.Join(w, "basesystem.dmg") + " -o " + filepath.Join(w, "basesystem.img"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dmg2img ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestBaseSystemMustSurviveTheTrip(t *testing.T) {
	g := newRig(t)
	g.vm.reportSHA = strings.Repeat("a", 64)
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil || !strings.Contains(err.Error(), "BaseSystem.dmg did not survive the trip out of the microVM") {
		t.Fatalf("err = %v", err)
	}
	absent(t, g.out, g.out+".building", g.out+".sha256", g.out+".lock")
	if n := g.vm.names(); len(n) != 1 {
		t.Fatalf("passes after a torn BaseSystem.dmg: %q", n)
	}
}

func TestTheBaseSystemSizeMustBeReported(t *testing.T) {
	g := newRig(t)
	g.b.VM = &consoleEdit{g.vm, func(c []byte) []byte {
		return regexp.MustCompile(`(?m)^MQG-BASESYSTEM-BYTES .*$`).ReplaceAll(c, []byte("MQG-BASESYSTEM-BYTES 12a\r"))
	}}
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil || !strings.Contains(err.Error(), "the microVM did not report a BaseSystem.dmg size") {
		t.Fatalf("err = %v", err)
	}
	absent(t, g.out+".building")
}

// consoleEdit is a MicroVM whose console is rewritten on the way out.
type consoleEdit struct {
	*fakeVM
	edit func([]byte) []byte
}

func (c *consoleEdit) Run(ctx context.Context, target string, payload []byte, disks []privops.Disk) ([]byte, error) {
	con, err := c.fakeVM.Run(ctx, target, payload, disks)
	return c.edit(con), err
}

func TestAConversionThatIsNotApplesIsNamed(t *testing.T) {
	g := newRig(t)
	g.vm.badESD = "Essentials.pkg"
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil || !strings.HasPrefix(err.Error(), "the ESD does not contain what Apple shipped") {
		t.Fatalf("err = %v", err)
	}
	found := false
	for _, l := range g.logs {
		found = found || strings.HasPrefix(l, "    Essentials.pkg: FAILED -- "+strings.Repeat("0", 64)+" is not what Apple shipped")
	}
	if !found {
		t.Errorf("the wrong package was not logged:\n%s", strings.Join(g.logs, "\n"))
	}
	if n := g.vm.names(); strings.Join(n, " ") != "extract-basesystem assemble" {
		t.Fatalf("passes %q", n)
	}
	absent(t, g.out, g.out+".building", g.out+".sha256")
}

func TestACopyThatIsNotApplesIsNamed(t *testing.T) {
	g := newRig(t)
	g.vm.dropMedia = "BSD.pkg"
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil || !strings.HasPrefix(err.Error(), "the media does not contain what Apple shipped") {
		t.Fatalf("err = %v", err)
	}
	if !g.logged("    BSD.pkg: MISSING -- the media does not have it") {
		t.Errorf("the missing package was not logged:\n%s", strings.Join(g.logs, "\n"))
	}
	absent(t, g.out, g.out+".building", g.out+".sha256", g.out+".lock")
}

func TestBuildRefusesWithoutTheBackend(t *testing.T) {
	g := newRig(t)
	g.vm.missing = []string{"qemu-system-x86_64 (not on PATH)", "busybox (not on PATH)"}
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil {
		t.Fatal("built without the backend")
	}
	for _, m := range g.vm.missing {
		if !strings.Contains(err.Error(), m) {
			t.Errorf("the error does not name %q: %v", m, err)
		}
	}
	if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
		t.Fatalf("ran %v and %q", g.r.Calls, g.vm.names())
	}
}

// TestPreflightNamesEverythingMissing: one check, in one place, that
// both Build and the media data source (before it fetches anything) ask. It names
// every missing thing at once, the backend's lines and the host tools,
// and Build refuses with exactly its error, having run nothing.
func TestPreflightNamesEverythingMissing(t *testing.T) {
	g := newRig(t)
	if err := g.b.Preflight(); err != nil {
		t.Fatalf("Preflight on a host with everything = %v", err)
	}

	g.vm.missing = []string{"qemu-system-x86_64 (not on PATH)", "busybox (not on PATH)"}
	g.r.Paths = map[string]string{}
	err := g.b.Preflight()
	if err == nil {
		t.Fatal("Preflight passed with nothing present")
	}
	for _, m := range append(g.vm.missing, "dmg2img (not on PATH)", "mkfs.hfsplus (not on PATH)") {
		if !strings.Contains(err.Error(), m) {
			t.Errorf("Preflight does not name %q: %v", m, err)
		}
	}
	_, berr := g.b.Build(context.Background(), g.esd, Options{})
	if berr == nil || berr.Error() != err.Error() {
		t.Fatalf("Build refused with %v, want Preflight's %v", berr, err)
	}
	if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
		t.Fatalf("ran %v and %q", g.r.Calls, g.vm.names())
	}
	absent(t, g.b.Paths.Build())

	// Each kind alone keeps the message Build has always given.
	g.vm.missing = nil
	g.r.Paths = map[string]string{"mkfs.hfsplus": "/usr/sbin/mkfs.hfsplus"}
	if err := g.b.Preflight(); err == nil || err.Error() != "the media build needs dmg2img (not on PATH)" {
		t.Fatalf("Preflight = %v", err)
	}
}

func TestBuildRefuses(t *testing.T) {
	dir := t.TempDir()
	notXar := writeFile(t, filepath.Join(dir, "not-xar.pkg"), "PK\x03\x04 a zip")
	good := writeFile(t, filepath.Join(dir, "good.pkg"), "xar!good")
	nowhere := filepath.Join(dir, "nowhere.pkg")
	for _, tc := range []struct {
		name  string
		esd   string // "" is the rig's
		o     Options
		tools map[string]string
		want  string
	}{
		{"no ESD", filepath.Join(dir, "InstallESD.dmg"), Options{}, nil,
			"no InstallESD.dmg at " + filepath.Join(dir, "InstallESD.dmg") + " -- fetch it first"},
		{"no first-boot package", "", Options{Injectables: Injectables{FirstbootPkg: nowhere}}, nil,
			"no first-boot package at " + nowhere},
		{"first-boot package not xar", "", Options{Injectables: Injectables{FirstbootPkg: notXar}}, nil,
			notXar + " is not a flat package (no xar magic)"},
		{"no extra package", "", Options{Injectables: Injectables{ExtraPkgs: []string{good, nowhere}}}, nil,
			"no such extra package: " + nowhere},
		{"extra package not xar", "", Options{Injectables: Injectables{ExtraPkgs: []string{notXar}}}, nil,
			notXar + " is not a flat package (no xar magic)"},
		{"two extras on one name", "", Options{Injectables: Injectables{ExtraPkgs: []string{good, good}}}, nil,
			"would both be System/Installation/Packages/good.pkg"},
		{"negative space", "", Options{ExtraSpaceMiB: -1}, nil,
			"the extra space wants a whole number of MiB, not -1"},
		{"no dmg2img", "", Options{}, map[string]string{"mkfs.hfsplus": "/usr/sbin/mkfs.hfsplus"},
			"dmg2img (not on PATH)"},
		{"no mkfs.hfsplus", "", Options{}, map[string]string{"dmg2img": "/usr/bin/dmg2img"},
			"mkfs.hfsplus (not on PATH)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t)
			if tc.tools != nil {
				g.r.Paths = tc.tools
			}
			esd := g.esd
			if tc.esd != "" {
				esd = tc.esd
			}
			_, err := g.b.Build(context.Background(), esd, tc.o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
				t.Fatalf("ran %v and %q", g.r.Calls, g.vm.names())
			}
			absent(t, g.b.Paths.Build())
		})
	}

	t.Run("existing media", func(t *testing.T) {
		g := newRig(t)
		writeFile(t, g.out, "the old media")
		_, err := g.b.Build(context.Background(), g.esd, Options{})
		if want := g.out + " exists, and the build was not told to replace it (Options.Force)"; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if b, _ := os.ReadFile(g.out); string(b) != "the old media" {
			t.Fatalf("the media was touched: %q", b)
		}
		if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
			t.Fatalf("ran %v and %q", g.r.Calls, g.vm.names())
		}
		absent(t, g.out+".lock")
	})
}

func TestForceReplacesTheMedia(t *testing.T) {
	g := newRig(t)
	writeFile(t, g.out, "the old media")
	writeFile(t, g.out+".sha256", "the old sidecar\n")
	// The old sidecar goes before the image is replaced: never a moment
	// when it sits beside media it was not written for.
	g.b.afterMediaRename = func() {
		if _, err := os.Lstat(g.out + ".sha256"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the old sidecar is still there once the new media is in place (%v)", err)
		}
	}
	if _, err := g.b.Build(context.Background(), g.esd, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(g.out); string(b) == "the old media" {
		t.Fatal("the old media is still there")
	}
	side, _ := os.ReadFile(g.out + ".sha256")
	if !strings.HasSuffix(string(side), sha256Of(t, g.out)+"  installer-media.img\n") {
		t.Fatalf("the sidecar is\n%s", side)
	}
}

// A .building file is only ever a build that was killed: never media.
func TestAStaleBuildingFileIsNotInTheWay(t *testing.T) {
	g := newRig(t)
	writeFile(t, g.out+".building", "a build that was killed")
	writeFile(t, filepath.Join(g.b.Paths.MediaWork(), "basesystem.img"), "a stale conversion")
	if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
		t.Fatal(err)
	}
	absent(t, g.out+".building")
}

func TestAnotherBuilderIsRefused(t *testing.T) {
	g := newRig(t)
	lockDir := g.out + ".lock"
	holder := os.Getppid() // alive: it is running this test
	writeFile(t, filepath.Join(lockDir, "pid"), strconv.Itoa(holder)+"\n")
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("pid %d ", holder)) {
		t.Fatalf("err = %v", err)
	}
	if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
		t.Fatalf("ran %v and %q", g.r.Calls, g.vm.names())
	}
	if b, _ := os.ReadFile(filepath.Join(lockDir, "pid")); string(b) != strconv.Itoa(holder)+"\n" {
		t.Fatalf("the other builder's lock was changed: %q", b)
	}
}

func TestAStaleLockIsTakenOver(t *testing.T) {
	g := newRig(t)
	writeFile(t, filepath.Join(g.out+".lock", "pid"), "")
	if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
		t.Fatal(err)
	}
	if !g.logged("taking over a stale lock left by an unknown pid") {
		t.Fatalf("the takeover was not logged:\n%s", strings.Join(g.logs, "\n"))
	}
	absent(t, g.out+".lock")
}

func TestKeepWorkKeepsTheConversions(t *testing.T) {
	g := newRig(t)
	if _, err := g.b.Build(context.Background(), g.esd, Options{KeepWork: true, Injectables: Injectables{Autoinstall: true}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"esd.img", "basesystem.dmg", "basesystem.img", "inject.tar", "console.txt"} {
		if _, err := os.Stat(filepath.Join(g.b.Paths.MediaWork(), n)); err != nil {
			t.Error(err)
		}
	}
}

func TestNothingMounts(t *testing.T) {
	g := newRig(t)
	o := Options{Injectables: Injectables{Autoinstall: true,
		FirstbootPkg: writeFile(t, filepath.Join(t.TempDir(), "fb.pkg"), "xar!fb")}}
	if _, err := g.b.Build(context.Background(), g.esd, o); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.r.Calls {
		switch c.Name {
		case "mount", "umount", "losetup", "udisksctl", "sgdisk":
			t.Errorf("ran %s", c)
		case "dmg2img", "mkfs.hfsplus":
		default:
			t.Errorf("ran something else: %s", c)
		}
	}
}

func TestThePartitionIsSizedFromTheReference(t *testing.T) {
	if got := BasePartMiB(); got != 6759 {
		t.Fatalf("BasePartMiB() = %d, want 6759", got)
	}
	g := newRig(t)
	g.b.baseMiB = 0
	var sized int64
	stop := errors.New("stopped once the size was seen")
	g.r = fakeTools(func(size int64) error { sized = size; return stop })
	g.r.Paths = map[string]string{"dmg2img": "x", "mkfs.hfsplus": "y"}
	g.b.Runner = g.r
	if _, err := g.b.Build(context.Background(), g.esd, Options{ExtraSpaceMiB: 300}); !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
	if want := int64(6759+300) << 20; sized != want {
		t.Fatalf("mkfs.hfsplus was given %d bytes, want %d", sized, want)
	}
	absent(t, g.out, g.out+".building", g.out+".building.hfs-tmp")

	dir := t.TempDir()
	big := filepath.Join(dir, "big.pkg")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, 3<<19); err != nil {
		t.Fatal(err)
	}
	small := writeFile(t, filepath.Join(dir, "small.pkg"), "0123456789")
	if got, err := UpdatesExtraMiB([]string{big, small}); err != nil || got != 66 {
		t.Fatalf("UpdatesExtraMiB = %d, %v, want 66", got, err)
	}
	if got, err := UpdatesExtraMiB(nil); err != nil || got != 0 {
		t.Fatalf("UpdatesExtraMiB(none) = %d, %v, want 0", got, err)
	}
	if _, err := UpdatesExtraMiB([]string{filepath.Join(dir, "gone.pkg")}); err == nil {
		t.Fatal("a package that is not there was sized")
	}
}

// A lock naming pid 0 names nobody, and any other builder would take it
// over: an unset PID is this process's.
func TestTheLockNamesThisProcessWhenPIDIsUnset(t *testing.T) {
	g := newRig(t)
	g.b.PID = 0
	var seen string
	g.r.Handle = func(inner func(proc.Cmd) error) func(proc.Cmd) error {
		return func(c proc.Cmd) error {
			if seen == "" {
				b, _ := os.ReadFile(filepath.Join(g.out+".lock", "pid"))
				seen = string(b)
			}
			return inner(c)
		}
	}(g.r.Handle)
	if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(os.Getpid()) + "\n"; seen != want {
		t.Fatalf("the lock named %q during the build, want %q", seen, want)
	}
}

// Force replaces the media only with media that has been verified: a
// forced build that fails leaves the old media and its sidecar exactly
// as they were.
func TestAFailedForcedBuildKeepsTheOldMedia(t *testing.T) {
	g := newRig(t)
	writeFile(t, g.out, "the old media")
	writeFile(t, g.out+".sha256", "the old sidecar\n")
	g.vm.dropMedia = "BSD.pkg"
	_, err := g.b.Build(context.Background(), g.esd, Options{Force: true})
	if err == nil || !strings.HasPrefix(err.Error(), "the media does not contain what Apple shipped") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(g.out); string(b) != "the old media" {
		t.Fatalf("the old media was touched: %q", b)
	}
	if b, _ := os.ReadFile(g.out + ".sha256"); string(b) != "the old sidecar\n" {
		t.Fatalf("the old sidecar was touched: %q", b)
	}
	absent(t, g.out+".building", g.out+".lock")
	if !g.logged("replacing " + g.out + " once the new media is verified") {
		t.Errorf("the replacement was not announced:\n%s", strings.Join(g.logs, "\n"))
	}
}

func TestAPassThatFailsIsNamed(t *testing.T) {
	g := newRig(t)
	g.vm.failPass = "fix-ownership"
	_, err := g.b.Build(context.Background(), g.esd, Options{})
	if err == nil || !strings.Contains(err.Error(), "pass 3 (fix-ownership): qemu-system-x86_64: exit status 1") {
		t.Fatalf("err = %v", err)
	}
	absent(t, g.out, g.out+".building", g.out+".sha256", g.out+".lock")
	if n := g.vm.names(); strings.Join(n, " ") != "extract-basesystem assemble fix-ownership" {
		t.Fatalf("passes %q", n)
	}
}

// A sidecar temp is only ever one this package staged and a killed build
// left: its own prefix, and nothing else in build/, is swept.
//
// The home is taken literally, whatever it holds: "[" alone is a
// malformed glob, and "a[1]" one that matches "a1" and not itself.
func TestStaleSidecarTempsAreSwept(t *testing.T) {
	for _, home := range []string{"", "plugin[", "a[1]"} {
		t.Run("home="+home, func(t *testing.T) {
			g := newRig(t)
			if home != "" {
				g.b.Paths = config.Paths{Home: filepath.Join(t.TempDir(), home)}
				g.out = g.b.Paths.InstallerMedia()
			}
			dir := filepath.Dir(g.out)
			stale := writeFile(t, filepath.Join(dir, ".installer-media.img.sha256.tmp-12345"), "stale")
			other := writeFile(t, filepath.Join(dir, "installer-media.img.sha256.tmp-notours"), "not ours")
			if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
				t.Fatal(err)
			}
			absent(t, stale)
			if _, err := os.Stat(other); err != nil {
				t.Fatalf("a file that is not ours was removed: %v", err)
			}
			if _, err := os.Stat(g.out + ".sha256"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A failed rename after the old sidecar is gone leaves the old media,
// and the error says so: its sidecar is what is missing.
func TestAFailedRenameSaysTheOldMediaIsIntact(t *testing.T) {
	g := newRig(t)
	// A rename cannot replace a directory that holds something.
	writeFile(t, filepath.Join(g.out, "in the way"), "x")
	writeFile(t, g.out+".sha256", "the old sidecar\n")
	_, err := g.b.Build(context.Background(), g.esd, Options{Force: true})
	want := "the old media at " + g.out + " is intact, but its sidecar was removed: cannot rename"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want one containing %q", err, want)
	}
	if _, serr := os.Stat(filepath.Join(g.out, "in the way")); serr != nil {
		t.Fatal(serr)
	}
	absent(t, g.out+".sha256", g.out+".building", g.out+".lock")
}

// Once the verified media is in place, the build has succeeded: tidying
// up after it can only warn. A sidecar that could not follow it is an
// error that says the media is there.
func TestAfterTheMediaIsInPlace(t *testing.T) {
	t.Run("cleanup", func(t *testing.T) {
		g := newRig(t)
		g.b.afterMediaRename = func() {
			esdImg := filepath.Join(g.b.Paths.MediaWork(), "esd.img")
			if err := os.Remove(esdImg); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(esdImg, "in the way"), "x")
		}
		out, err := g.b.Build(context.Background(), g.esd, Options{})
		if err != nil || out != g.out {
			t.Fatalf("Build = %q, %v", out, err)
		}
		warned := false
		for _, l := range g.logs {
			warned = warned || strings.HasPrefix(l, "warning: cannot remove "+filepath.Join(g.b.Paths.MediaWork(), "esd.img"))
		}
		if !warned {
			t.Errorf("no warning:\n%s", strings.Join(g.logs, "\n"))
		}
		if _, err := os.Stat(g.out + ".sha256"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sidecar", func(t *testing.T) {
		g := newRig(t)
		g.b.afterMediaRename = func() {
			temps, _ := filepath.Glob(filepath.Join(filepath.Dir(g.out), ".installer-media.img.sha256.tmp-*"))
			for _, p := range temps {
				os.Remove(p)
			}
		}
		out, err := g.b.Build(context.Background(), g.esd, Options{})
		if out != g.out || err == nil || !strings.Contains(err.Error(), g.out+" is in place, but its sidecar is missing") {
			t.Fatalf("Build = %q, %v", out, err)
		}
		if _, err := os.Stat(g.out); err != nil {
			t.Fatal(err)
		}
		absent(t, g.out+".lock")
	})
}

// Validate is Build's cheap refusals, which need no ESD and no microVM:
// the media data source asks it before it fetches 5.2 GB. Each refusal is Build's
// own error, word for word, and touches nothing.
func TestValidateRefusesWhatBuildWould(t *testing.T) {
	dir := t.TempDir()
	notXar := writeFile(t, filepath.Join(dir, "not-xar.pkg"), "PK\x03\x04 a zip")
	good := writeFile(t, filepath.Join(dir, "good.pkg"), "xar!good")
	nowhere := filepath.Join(dir, "nowhere.pkg")
	for _, o := range []Options{
		{Injectables: Injectables{FirstbootPkg: nowhere}},
		{Injectables: Injectables{FirstbootPkg: notXar}},
		{Injectables: Injectables{ExtraPkgs: []string{good, nowhere}}},
		{Injectables: Injectables{ExtraPkgs: []string{notXar}}},
		{Injectables: Injectables{ExtraPkgs: []string{good, good}}},
		{Injectables: Injectables{ExtraPkgs: []string{good, writeFile(t, filepath.Join(dir, "sub", "good.pkg"), "xar!also")}}},
		{ExtraSpaceMiB: -1},
	} {
		g := newRig(t)
		verr := g.b.Validate(o)
		if verr == nil {
			t.Errorf("%+v: Validate passed", o)
			continue
		}
		_, berr := g.b.Build(context.Background(), g.esd, o)
		if berr == nil || berr.Error() != verr.Error() {
			t.Errorf("%+v: Build refused with %v, Validate with %v", o, berr, verr)
		}
		if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
			t.Errorf("%+v: ran %v and %q", o, g.r.Calls, g.vm.names())
		}
		absent(t, g.b.Paths.Build())
	}

	g := newRig(t)
	if err := g.b.Validate(Options{Injectables: Injectables{FirstbootPkg: good}}); err != nil {
		t.Fatalf("Validate refused a good package: %v", err)
	}
	writeFile(t, g.out, "the old media")
	if err := g.b.Validate(Options{}); err == nil || err.Error() != g.out+" exists, and the build was not told to replace it (Options.Force)" {
		t.Fatalf("existing media: Validate = %v", err)
	}
	if err := g.b.Validate(Options{Force: true}); err != nil {
		t.Fatalf("existing media with Force: Validate = %v", err)
	}
	absent(t, g.out+".lock")
	if b, _ := os.ReadFile(g.out); string(b) != "the old media" {
		t.Fatalf("Validate touched the media: %q", b)
	}
}

// Where the lock's acquisition could not be serialised, the build goes
// on -- the lock itself still holds -- and says so.
func TestAnUnserializedLockIsWarnedOf(t *testing.T) {
	g := newRig(t)
	orig := acquire
	acquire = func(dir string, pid int) (*lock.Lock, error) {
		l, err := orig(dir, pid)
		if l != nil {
			l.Serialized = false
		}
		return l, err
	}
	t.Cleanup(func() { acquire = orig })
	if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
		t.Fatal(err)
	}
	want := "warning: the filesystem refused flock on " + filepath.Dir(g.out) +
		": the build lock is still exclusive, but a stale one may have been taken over by two builders at once"
	if !g.logged(want) {
		t.Fatalf("no warning %q:\n%s", want, strings.Join(g.logs, "\n"))
	}

	g = newRig(t)
	acquire = orig
	if _, err := g.b.Build(context.Background(), g.esd, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, l := range g.logs {
		if strings.Contains(l, "flock") {
			t.Fatalf("warned with the flock taken: %q", l)
		}
	}
}

// The privops backend's own requirements are Preflight's: a KVM device
// this user cannot open is named before anything is fetched or run.
func TestPreflightNamesTheKVMDevice(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "boot", "vmlinuz-6.1.0-test"), "kernel")
	g := newRig(t)
	g.r.Paths["qemu-system-x86_64"] = "/usr/bin/qemu-system-x86_64"
	g.r.Paths["busybox"] = filepath.Join(root, "busybox") // unreadable, so not judged dynamic
	be := privops.Backend{Runner: g.r, QEMU: "qemu-system-x86_64", BootDir: filepath.Join(root, "boot"),
		ModulesDir: filepath.Join(root, "modules"), KVer: "6.1.0-test", GOOS: "linux",
		KVMDevice: filepath.Join(root, "kvm")}
	g.b.VM = be
	err := g.b.Preflight()
	if want := filepath.Join(root, "kvm") + " does not exist"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Preflight = %v, want it to name %q", err, want)
	}
	if len(g.r.Calls) != 0 {
		t.Fatalf("ran %v", g.r.Calls)
	}
}
