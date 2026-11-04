package firmware

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

func TestAboveBaselineNamesWhatAnInstructionNeeds(t *testing.T) {
	for _, c := range []struct{ raw, insn, want string }{
		{"c5 f8 29 bd 30 ff ff ff", "vmovaps %xmm7,-0xd0(%rbp)", "VEX-encoded (AVX or later)"},
		{"c4 e2 7d 00 c1", "vpshufb %ymm1,%ymm0,%ymm0", "VEX-encoded (AVX or later)"},
		{"66 c5 f8 29 00", "vmovaps %xmm0,(%rax)", "VEX-encoded (AVX or later)"},
		{"62 f1 7c 48 28 c1", "vmovaps %zmm1,%zmm0", "EVEX-encoded (AVX-512)"},
		{"8f e9 78 c0 c1 03", "vprotb $0x3,%xmm1,%xmm0", "XOP-encoded"},
		{"f3 48 0f b8 c7", "popcnt %rdi,%rax", "POPCNT"},
		{"f2 48 0f 38 f1 c1", "crc32q %rcx,%rax", "SSE4.2"},
		{"66 0f 38 00 c1", "pshufb %xmm1,%xmm0", "SSSE3"},
		{"66 0f 3a 0e c1 0f", "pblendw $0xf,%xmm1,%xmm0", "SSE4.1"},
		{"66 0f 38 20 c1", "pmovsxbw %xmm1,%xmm0", "SSE4.1"},
		{"f2 0f 12 c1", "movddup %xmm1,%xmm0", "SSE3"},
		{"f3 0f bd c0", "lzcnt %eax,%eax", "LZCNT"},
		{"0f 38 f1 07", "movbe %eax,(%rdi)", "MOVBE"},
		{"f0 48 0f c7 0f", "lock cmpxchg16b (%rdi)", "CMPXCHG16B"},
		// What x86-64 itself has, and what runs as something it has.
		{"44 0f 29 4d 80", "movaps %xmm9,-0x80(%rbp)", ""},
		{"66 0f c5 c1 03", "pextrw $0x3,%xmm1,%eax", ""},
		{"66 0f ea c1", "pminsw %xmm1,%xmm0", ""},
		{"66 0f f4 c1", "pmuludq %xmm1,%xmm0", ""},
		{"f3 0f bc c0", "tzcnt %eax,%eax", ""}, // gcc's rep bsf: BSF where BMI is absent
		{"8f 45 f8", "pop -0x8(%rbp)", ""},
		{"f3 48 ab", "rep stos %rax,%es:(%rdi)", ""},
		{"55", "push %rbp", ""},
	} {
		if got := aboveBaseline(hexBytes(t, c.raw), c.insn); got != c.want {
			t.Errorf("aboveBaseline(%s, %q) = %q, want %q", c.raw, c.insn, got, c.want)
		}
	}
}

func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, ok := parseHexBytes(s)
	if !ok {
		t.Fatalf("not hex bytes: %q", s)
	}
	return b
}

func TestFromAssemblyIsTheAssemblersSourcesAndNoSourceAtAll(t *testing.T) {
	for src, want := range map[string]bool{
		"":                        true,
		"/b/X64/Sha512Avx.iii":    true,
		"/b/X64/CpuAsm.nasm":      true,
		"/b/Ia32/Thunk.S":         true,
		"/b/X64/Switch.s":         true,
		"/b/X64/Old.asm":          true,
		"/b/Library/BaseLib.c":    false,
		"/b/Include/Inline.h":     false,
		"/b/Library/Cxx.cpp":      false,
		"/b/Library/Weird.nasm.c": false,
	} {
		if got := fromAssembly(src); got != want {
			t.Errorf("fromAssembly(%q) = %v, want %v", src, got, want)
		}
	}
}

// What the compiler generated above x86-64 is found, at its own source
// line; assembly is not, whether its source is named or it has none
// (the line before a symbol is never carried past it).
func TestScanBaselineFindsTheCompilersInstructionsOnly(t *testing.T) {
	got := scanBaseline(bytes.NewReader(testdataFile(t, "baseline-objdump.txt")))
	want := []baselineHit{
		{Addr: "2ada", Insn: "vmovaps %xmm7,-0xd0(%rbp)", Why: "VEX-encoded (AVX or later)",
			Source: "/b/OpenCorePkg/Library/OcApplicationEntryPoint/ApplicationEntryPoint.c:39"},
		{Addr: "2ae2", Insn: "popcnt %rdi,%rax", Why: "POPCNT",
			Source: "/b/OpenCorePkg/Library/OcApplicationEntryPoint/ApplicationEntryPoint.c:39"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanBaseline =\n%+v\nwant\n%+v", got, want)
	}
}

// dirtyObjdump is objdump's listing of a module the compiler built for
// a newer CPU than x86-64.
func dirtyObjdump(t *testing.T) string { return string(testdataFile(t, "baseline-objdump.txt")) }

func TestOpenCoreChecksEveryShippedModuleAgainstTheBaseline(t *testing.T) {
	f := newFixture(t)
	if _, err := f.openCore(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range f.calls("objdump") {
		if !reflect.DeepEqual(c.Args[:3], []string{"-d", "-l", "-w"}) {
			t.Errorf("objdump %q, want -d -l -w <module>", c.Args)
		}
		got = append(got, filepath.Base(c.Args[len(c.Args)-1]))
	}
	want := []string{"OpenCore.dll", "Bootstrap.dll", "OpenRuntime.dll", "OpenPartitionDxe.dll", "OpenHfsPlus.dll"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("objdump read %q, want each shipped module: %q", got, want)
	}
	if !strings.Contains(f.log.String(), "every instruction the compiler generated in 5 modules is x86-64's") {
		t.Errorf("the log does not say the check passed:\n%s", f.log.String())
	}
}

func TestOpenCoreRefusesAModuleBuiltForANewerCPU(t *testing.T) {
	f := newFixture(t)
	f.objdump = map[string]string{"Bootstrap.dll": dirtyObjdump(t)}
	_, err := f.openCore()
	if err == nil {
		t.Fatal("OpenCore shipped a module built for a newer CPU")
	}
	for _, want := range []string{"2 instructions above x86-64", "Bootstrap.dll", "0x2ada vmovaps %xmm7,-0xd0(%rbp)",
		"VEX-encoded (AVX or later)", "ApplicationEntryPoint.c:39", March} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
	efis, _ := filepath.Glob(filepath.Join(f.home, "build", "artifacts", "*.efi"))
	if len(efis) != 0 {
		t.Errorf("artifacts shipped anyway: %v", efis)
	}
}

func TestOpenCoreRefusesAShippedModuleWithNoDebugImage(t *testing.T) {
	f := newFixture(t)
	f.noDebug = []string{"OpenHfsPlus"}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "no debug image OpenHfsPlus.dll") {
		t.Fatalf("err = %v", err)
	}
}

func TestOVMFChecksEveryModuleAgainstTheBaseline(t *testing.T) {
	f := ovmfFixture(t)
	n := len(f.calls("objdump"))
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range f.calls("objdump")[n:] {
		got = append(got, filepath.Base(c.Args[len(c.Args)-1]))
	}
	if want := []string{"DxeCore.dll", "SecMain.dll"}; !reflect.DeepEqual(got, want) {
		t.Errorf("objdump read %q, want every OVMF module: %q", got, want)
	}
}

func TestOVMFRefusesAModuleBuiltForANewerCPU(t *testing.T) {
	f := ovmfFixture(t)
	f.objdump = map[string]string{"SecMain.dll": dirtyObjdump(t)}
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "SecMain.dll") || !strings.Contains(err.Error(), "above x86-64") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.b.Paths.Firmware(), "OVMF_CODE.fd")); err == nil {
		t.Error("OVMF_CODE.fd shipped anyway")
	}
}

// A listing objdump could not produce is a failure, not a pass.
func TestTheBaselineCheckFailsWhenObjdumpDoes(t *testing.T) {
	f := newFixture(t)
	f.objdumpErr = &proc.ExitError{Cmd: "objdump", Code: 1}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "cannot disassemble") {
		t.Fatalf("err = %v", err)
	}
}

// cleanObjdump is objdump's listing of a module with nothing above x86-64.
const cleanObjdump = "0000000000001000 <F>:\nF():\n/b/F.c:1\n    1000:\t55                   \tpush   %rbp\n"

func (f *fixture) playObjdump(c proc.Cmd) error {
	if f.objdumpErr != nil {
		return f.objdumpErr
	}
	out := cleanObjdump
	if s, ok := f.objdump[filepath.Base(c.Args[len(c.Args)-1])]; ok {
		out = s
	}
	_, err := io.WriteString(c.Stdout, out)
	return err
}
