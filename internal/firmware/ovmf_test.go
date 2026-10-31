package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
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

// marker0005 is 0005's marker: upstream will never carry it, and 0004
// does not.
const marker0005 = "packer-plugin-macosx: assets/firmware/patches/0005"

// marker0006 is 0006's marker, in QemuFlash.c.
const marker0006 = "packer-plugin-macosx: assets/firmware/patches/0006"

func TestOVMFAppliesItsPatchesAndChecksThem(t *testing.T) {
	f := ovmfFixture(t)
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	names := []string{"0002-ovmf-pin-the-c-dialect.patch", "0003-firmware-drop-werror.patch",
		"0004-ovmf-reserve-the-igvm-parameter-area.patch", "0005-ovmf-never-read-igvm-parameters.patch",
		"0006-ovmf-program-only-the-flash-bytes-that-change.patch"}
	var want [][]byte
	for _, p := range names {
		b, err := fs.ReadFile(macosx.Files, "assets/firmware/patches/"+p)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, b)
	}
	if !reflect.DeepEqual(f.stdins[1:], want) {
		t.Errorf("%d patches after 0001; want 0002, 0003, 0004, 0005 then 0006", len(f.stdins)-1)
	}
	if n := len(f.applies(f.udk(), "-p1", "-")); n != 5 {
		t.Errorf("%d `git -C %s apply -p1 -` calls, want 5", n, f.udk())
	}
	dsc, _ := os.ReadFile(filepath.Join(f.udk(), "OvmfPkg", "OvmfPkgX64.dsc"))
	if !strings.Contains(string(dsc), "std=gnu17") || !strings.Contains(string(dsc), "Wno-error") {
		t.Errorf("the dsc lacks a flag:\n%s", dsc)
	}
	c, _ := os.ReadFile(filepath.Join(f.udk(), filepath.FromSlash(igvmC)))
	if !strings.Contains(string(c), "PlatformIgvmParamReserve") || !strings.Contains(string(c), marker0005) {
		t.Errorf("%s lacks 0004 or 0005:\n%s", igvmC, c)
	}
	if q, _ := os.ReadFile(filepath.Join(f.udk(), filepath.FromSlash(qemuFlashC))); !strings.Contains(string(q), marker0006) {
		t.Errorf("%s lacks 0006:\n%s", qemuFlashC, q)
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

// preDsc gives the fixture's dsc the flags 0002 and 0003 add, so only the
// Igvm.c patches are tried.
func preDsc(f *fixture) {
	dsc := filepath.Join(f.udk(), "OvmfPkg", "OvmfPkgX64.dsc")
	appendTo(dsc, "  GCC:*_*_*_CC_FLAGS = -std=gnu17\n")
	appendTo(dsc, "  GCC:*_*_*_CC_FLAGS = -Wno-error\n")
}

// TestOVMFSaysWhenTheIGVMAreaIsStillTheOSs: an Igvm.c 0004 did not
// change is named, and nothing is built from it.
func TestOVMFSaysWhenTheIGVMAreaIsStillTheOSs(t *testing.T) {
	f := ovmfFixture(t)
	preDsc(f)
	f.patchesTake = false
	_, err := f.ovmf()
	if err == nil || err.Error() != igvmC+" still leaves the IGVM parameter area to the OS" {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("built with an unpatched Igvm.c")
	}
}

// TestOVMFSaysWhenIGVMParametersAreStillRead: a tree with 0004 (or
// upstream's fix) whose Igvm.c 0005 did not change is named, and nothing is
// built from it.
func TestOVMFSaysWhenIGVMParametersAreStillRead(t *testing.T) {
	f := ovmfFixture(t)
	preDsc(f)
	appendTo(filepath.Join(f.udk(), filepath.FromSlash(igvmC)), "VOID\nEFIAPI\nPlatformIgvmParamReserve (\n  VOID\n  )\n{\n}\n")
	f.patchesTake = false
	_, err := f.ovmf()
	if err == nil || err.Error() != igvmC+" still takes IGVM parameters from whatever is in guest RAM" {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("built with an Igvm.c that still reads IGVM parameters")
	}
}

// TestOVMFSaysWhenFlashWritesStillProgramEveryByte: a tree with 0004 and
// 0005 whose QemuFlash.c 0006 did not change is named, and nothing is built
// from it.
func TestOVMFSaysWhenFlashWritesStillProgramEveryByte(t *testing.T) {
	f := ovmfFixture(t)
	preDsc(f)
	appendTo(filepath.Join(f.udk(), filepath.FromSlash(igvmC)), "VOID\nEFIAPI\nPlatformIgvmParamReserve (\n  VOID\n  )\n{\n}\n// "+marker0005+"\nSTATIC BOOLEAN  mIgvmParamsInvalid = TRUE;\n")
	f.patchesTake = false
	_, err := f.ovmf()
	if err == nil || err.Error() != qemuFlashC+" still programs every byte of a flash write, the ones that already hold their value too" {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("built with a QemuFlash.c that programs every byte")
	}
}

// TestOVMFLeavesAnIGVMAreaUpstreamAlreadyReserves: an audk that has
// merged upstream's fix is not given 0004 again, and still gets 0005,
// which upstream will never have.
func TestOVMFLeavesAnIGVMAreaUpstreamAlreadyReserves(t *testing.T) {
	f := ovmfFixture(t)
	appendTo(filepath.Join(f.udk(), filepath.FromSlash(igvmC)), "VOID\nEFIAPI\nPlatformIgvmParamReserve (\n  VOID\n  )\n{\n}\n")
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	got0005 := false
	for _, p := range f.stdins {
		if strings.Contains(string(p), "+PlatformIgvmParamReserve (") {
			t.Errorf("0004 was applied to a tree that already has it")
		}
		if strings.Contains(string(p), "+// "+marker0005) {
			got0005 = true
		}
	}
	if !got0005 {
		t.Errorf("0005 was not applied to a tree that has 0004")
	}
}

// diffOf is a patch from its first `diff --git`: what git applies, without
// the header that says why.
func diffOf(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(macosx.Files, "assets/firmware/patches/"+name)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(b), "diff --git ")
	if i < 0 {
		t.Fatalf("%s has no diff", name)
	}
	return string(b[i:])
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// The firmware hung on warm restarts (docs/decisions/0018): OVMF reads
// guest RAM at 0x812000 as IGVM parameters on every boot, and nothing kept
// the OS from writing there, so after a restart it took the last OS's bytes
// for a vCPU count and waited for CPUs that do not exist. 0004 is
// upstream's fix: the area becomes runtime services data, which no OS
// reuses. Its diff is upstream's, byte for byte: the sha256 of
// tianocore/edk2's own diff of a8210e06 (merged as e4f99a85545f).
func TestThePatchReservesTheIGVMParameterAreaAsUpstreamDoes(t *testing.T) {
	const upstream = "a3c6fe54147adac3e09ae681d01f494c82008f949e496de930aafc33f62027f6"
	if got := sha256Hex(diffOf(t, "0004-ovmf-reserve-the-igvm-parameter-area.patch")); got != upstream {
		t.Errorf("0004's diff is %s, not upstream's %s", got, upstream)
	}
	b, _ := fs.ReadFile(macosx.Files, "assets/firmware/patches/0004-ovmf-reserve-the-igvm-parameter-area.patch")
	for _, want := range []string{
		"e4f99a85545f41ef7ae23cd939484d69a84b6176", // tianocore/edk2's merge
		"a8210e06a2a19814e750e17919f44b18c6fb17c6", // the commit itself
		"https://github.com/tianocore/edk2/pull/11866",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("0004's header does not name %q", want)
		}
	}
}

// 0005 is ours: OVMF never takes the IGVM parameter area at all, because
// QEMU here never loads an IGVM file. 0004 keeps an OS out of the page;
// 10.6's kernel is copied over it anyway (docs/decisions/0018). Its diff is
// one line of Igvm.c, the flag both readers of the area check first, and
// is pinned by its sha256.
func TestThePatchNeverReadsIGVMParameters(t *testing.T) {
	const pinned = "267c995785b0d99f695c205a8c0c9abf3401154d77704f8f3dae4f42407d4b86"
	d := diffOf(t, "0005-ovmf-never-read-igvm-parameters.patch")
	if got := sha256Hex(d); got != pinned {
		t.Errorf("0005's diff is %s, not the measured %s", got, pinned)
	}
	for _, want := range []string{
		"-STATIC BOOLEAN  mIgvmParamsInvalid;\r\n",
		"+STATIC BOOLEAN  mIgvmParamsInvalid = TRUE;\r\n",
		"+// " + marker0005,
	} {
		if !strings.Contains(d, want) {
			t.Errorf("0005 lacks %q", want)
		}
	}
	if strings.Count(d, "+++ b/") != 1 || !strings.Contains(d, "+++ b/"+igvmC+"\n") {
		t.Errorf("0005 should change %s alone:\n%s", igvmC, d)
	}
}

// 0006 is ours: QemuFlashWrite programs only the bytes that change. QEMU's
// pflash costs two traps and a synchronous write of the varstore file per
// programmed byte, and a variable reclaim, which OVMF's own console
// variables force at the 61st warm restart of one QEMU process, programmed
// some 800,000 bytes, nearly all 0xFF onto erased blocks: a warm restart
// held 41-57 s before OpenCore (docs/decisions/0032). Its diff changes
// QemuFlashWrite and QemuFlashEraseBlock, and is pinned by its sha256.
func TestThePatchProgramsOnlyTheFlashBytesThatChange(t *testing.T) {
	const pinned = "e2bf179aed61fb6f2ffbcaaba5e533e2b4cd3a90c91521e02d69eb3c222ef9ae"
	d := diffOf(t, "0006-ovmf-program-only-the-flash-bytes-that-change.patch")
	if got := sha256Hex(d); got != pinned {
		t.Errorf("0006's diff is %s, not the measured %s", got, pinned)
	}
	for _, want := range []string{
		"+  // " + marker0006,
		// a byte that already holds its value is left alone
		"+    if (Ptr[Loop] == Buffer[Loop]) {\r\n",
		"+      QemuFlashPtrWrite (Ptr + Loop, WRITE_BYTE_CMD);\r\n",
		"+      QemuFlashPtrWrite (Ptr + Loop, Buffer[Loop]);\r\n",
		"+    QemuFlashPtrWrite (Ptr + End - 1, READ_ARRAY_CMD);\r\n",
		// an erase leaves read-array mode too: after one a read returns the status register
		"   QemuFlashPtrWrite (Ptr, BLOCK_ERASE_CONFIRM_CMD);\r\n+\r\n",
		"+  QemuFlashPtrWrite (Ptr, READ_ARRAY_CMD);\r\n   return EFI_SUCCESS;\r\n }\r\n",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("0006 lacks %q", want)
		}
	}
	if strings.Count(d, "+++ b/") != 1 || !strings.Contains(d, "+++ b/"+qemuFlashC+"\n") {
		t.Errorf("0006 should change %s alone:\n%s", qemuFlashC, d)
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
	if err := os.WriteFile(filepath.Join(f.udk(), ".guest-macosx-prepared"), []byte("abc\n"), 0o644); err != nil {
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
		if err := os.WriteFile(filepath.Join(f.udk(), ".guest-macosx-prepared"), []byte(marker), 0o644); err != nil {
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
