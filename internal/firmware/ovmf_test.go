package firmware

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

func (f *fixture) ovmf() ([]string, error) { return f.b.OVMF(context.Background()) }

func (f *fixture) udk() string { return filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK") }

// ovmfFixture is a fixture on which OpenCore has built: an assembled
// tree and a GenFv.
func ovmfFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.mustOpenCore()
	return f
}

func TestOVMFBuildsX64ReleaseWithGCC(t *testing.T) {
	f := ovmfFixture(t)
	if _, err := f.ovmf(); err != nil {
		t.Fatalf("OVMF: %v\n%s", err, f.log.String())
	}
	cs := f.calls("bash")
	if len(cs) != 1 {
		t.Fatalf("%d bash calls, want 1", len(cs))
	}
	c := cs[0]
	want := []string{"-c", "set +u; . ./edksetup.sh >/dev/null || exit 1; exec build -a X64 -b RELEASE -t GCC -p OvmfPkg/OvmfPkgX64.dsc"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Errorf("Args = %q\nwant %q", c.Args, want)
	}
	if c.Dir != f.udk() {
		t.Errorf("Dir = %s, want %s", c.Dir, f.udk())
	}
	if !hasEnv(c.Env, "PATH=/usr/bin:/bin") {
		t.Errorf("Env lacks the base PATH: %q", c.Env)
	}
	log, err := os.ReadFile(filepath.Join(f.udk(), "ovmf-build.log"))
	if err != nil || !strings.Contains(string(log), "building OVMF") {
		t.Errorf("ovmf-build.log = %q, %v", log, err)
	}
}

func TestOVMFShipsTheThreeImages(t *testing.T) {
	f := ovmfFixture(t)
	got, err := f.ovmf()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.home, "build", "firmware")
	var want []string
	var sums strings.Builder
	for _, n := range []string{"OVMF_CODE.fd", "OVMF_VARS.fd", "OVMF.fd"} {
		p := filepath.Join(dir, n)
		want = append(want, p)
		if b, _ := os.ReadFile(p); string(b) != "fd "+n {
			t.Errorf("%s holds %q", n, b)
		}
		sums.WriteString(sha(t, p) + "  " + n + "\n")
		if line := n + "  " + strconv.Itoa(len("fd "+n)) + " bytes"; !strings.Contains(f.log.String(), line) {
			t.Errorf("the log does not say %q:\n%s", line, f.log.String())
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OVMF returned %v, want %v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS")); string(b) != sums.String() {
		t.Errorf("SHA256SUMS is\n%s\nwant\n%s", b, sums.String())
	}
}

func TestOVMFAppliesBothPatchesAndChecksThem(t *testing.T) {
	f := ovmfFixture(t)
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	var want [][]byte
	for _, p := range []string{"0002-ovmf-pin-the-c-dialect.patch", "0003-firmware-drop-werror.patch"} {
		b, err := fs.ReadFile(vmguest.Files, "assets/firmware/patches/"+p)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, b)
	}
	if !reflect.DeepEqual(f.stdins[1:], want) {
		t.Errorf("%d patches after 0001; want 0002 then 0003", len(f.stdins)-1)
	}
	if n := len(f.applies(f.udk(), "-p1", "-")); n != 2 {
		t.Errorf("%d `git -C %s apply -p1 -` calls, want 2", n, f.udk())
	}
	dsc, _ := os.ReadFile(filepath.Join(f.udk(), "OvmfPkg", "OvmfPkgX64.dsc"))
	if !strings.Contains(string(dsc), "std=gnu17") || !strings.Contains(string(dsc), "Wno-error") {
		t.Errorf("the dsc lacks a flag:\n%s", dsc)
	}
	n := len(f.stdins)
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	if len(f.stdins) != n {
		t.Errorf("a second OVMF patched again: %d patches, then %d", n, len(f.stdins))
	}

	g := ovmfFixture(t)
	g.patchesTake = false
	_, err := g.ovmf()
	if err == nil || err.Error() != "OvmfPkg/OvmfPkgX64.dsc still does not state a C dialect" {
		t.Fatalf("err = %v", err)
	}
	if len(g.calls("bash")) != 0 {
		t.Errorf("built with an unpatched dsc")
	}
}

func TestOVMFRefusesAnAbsentTree(t *testing.T) {
	f := newFixture(t)
	_, err := f.ovmf()
	want := "no assembled EDK II tree at " + f.udk() + " -- assemble the OpenCore tree first"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("ran bash")
	}
}

func TestOVMFRefusesATreeAtAnotherCommit(t *testing.T) {
	f := ovmfFixture(t)
	if err := os.WriteFile(filepath.Join(f.udk(), ".mqg-prepared"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "abc") || !strings.Contains(err.Error(), AudkCommit) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("ran bash")
	}
}

func TestOVMFSaysSoWhenBaseToolsAreNotBuilt(t *testing.T) {
	f := ovmfFixture(t)
	if err := os.Remove(filepath.Join(f.udk(), "BaseTools", "Source", "C", "bin", "GenFv")); err != nil {
		t.Fatal(err)
	}
	_, err := f.ovmf()
	want := "BaseTools are not built in " + f.udk() + " -- assemble the OpenCore tree first"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

func TestOVMFNamesMissingImages(t *testing.T) {
	f := ovmfFixture(t)
	f.fv = []string{"OVMF_CODE.fd", "OVMF_VARS.fd"}
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "missing 1 of 3 firmware images: OVMF.fd") {
		t.Fatalf("err = %v", err)
	}
}

func TestOVMFBuildFailureNamesTheLog(t *testing.T) {
	f := ovmfFixture(t)
	f.buildErr = &proc.ExitError{Cmd: "bash", Code: 1}
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), filepath.Join(f.udk(), "ovmf-build.log")) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(f.log.String(), "building OVMF") {
		t.Errorf("the log's tail is not shown:\n%s", f.log.String())
	}
}

func TestOVMFRefusesACompilerBelowTheFloor(t *testing.T) {
	f := ovmfFixture(t)
	f.banner = "gcc (GCC) 12.2.0"
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "below the floor") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("ran bash")
	}
}

func TestOVMFNeedsNasmAndIasl(t *testing.T) {
	f := ovmfFixture(t)
	delete(f.fake.Paths, "nasm")
	delete(f.fake.Paths, "iasl")
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "nasm") || !strings.Contains(err.Error(), "iasl") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("ran bash")
	}
}

// TestOVMFReadsTheMarkerLessTrailingNewlines: a marker naming the pinned
// commit, with any number of trailing newlines, is the pinned tree.
func TestOVMFReadsTheMarkerLessTrailingNewlines(t *testing.T) {
	for _, marker := range []string{AudkCommit, AudkCommit + "\n\n"} {
		f := ovmfFixture(t)
		if err := os.WriteFile(filepath.Join(f.udk(), ".mqg-prepared"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ovmf(); err != nil {
			t.Errorf("marker %q: %v", marker, err)
		}
	}
}

func TestOVMFSaysWhenWarningsAreStillErrors(t *testing.T) {
	f := ovmfFixture(t)
	// The dialect patch is already in; only 0003's is tried, and it
	// does nothing.
	appendTo(filepath.Join(f.udk(), "OvmfPkg", "OvmfPkgX64.dsc"), "  GCC:*_*_*_CC_FLAGS = -std=gnu17\n")
	f.patchesTake = false
	_, err := f.ovmf()
	if err == nil || err.Error() != "OvmfPkg/OvmfPkgX64.dsc still promotes upstream's warnings to errors" {
		t.Fatalf("err = %v", err)
	}
}

func TestOVMFRefusesANilEnvironment(t *testing.T) {
	f := ovmfFixture(t)
	f.b.Env = nil
	calls := len(f.fake.Calls)
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "Builder.Env is nil: pass the environment the build tools inherit") {
		t.Fatalf("err = %v", err)
	}
	if len(f.fake.Calls) != calls {
		t.Errorf("ran %v before refusing", f.fake.Calls[calls:])
	}
}
