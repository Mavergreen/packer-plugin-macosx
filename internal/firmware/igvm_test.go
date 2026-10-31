package firmware

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 0005 keeps OVMF from reading the IGVM parameter area only while every
// reader of PcdOvmfIgvmParamBase tests mIgvmParamsInvalid before it reads
// the PCD, the flag starts TRUE and nothing sets it FALSE
// (docs/decisions/0018). An audk bump can add a reader, or a path that
// trusts the area, and 0005 would still apply cleanly: its hunk's context
// is the struct above the flag. checkIGVMReaders runs on the patched tree
// before every OVMF build, so such a tree fails the build instead.

// igvmModelTemplate is a model of the patched Igvm.c's readers: the two
// that read the area, 0004's reservation (RESERVE, filled in from 0004
// itself by igvmModel), which only passes the address on, and the
// data-HOB walker, which reads another PCD.
const igvmModelTemplate = `#include <Base.h>

#define VP_COUNT_OFFSET  0xC0

typedef struct IGVM_MEMORY_MAP_ENTRY {
  UINT64    PageStart;
  UINT64    PageCount;
} IGVM_MEMORY_MAP_ENTRY;

STATIC BOOLEAN  mIgvmParamsInvalid = TRUE;

STATIC
IGVM_MEMORY_MAP_ENTRY *
EFIAPI
PlatformIgvmMemoryMapFind (
  VOID
  )
{
  UINT64                 Address;

  if (mIgvmParamsInvalid) {
    return NULL;
  }

  Address = FixedPcdGet64 (PcdOvmfIgvmParamBase);
  return (VOID *)(UINTN)Address;
}

RESERVE

VOID
EFIAPI
PlatformIgvmDataHobs (
  VOID
  )
{
  UINT64  Start;

  Start = FixedPcdGet64 (PcdOvmfIgvmHobBase);
}

UINT32
EFIAPI
PlatformIgvmVpCount (
  VOID
  )
{
  UINT64  Address;

  if (mIgvmParamsInvalid) {
    return 0;
  }

  Address = FixedPcdGet64 (PcdOvmfIgvmParamBase);
  return *(UINT32 *)(UINTN)(Address + 0xC0);
}
`

// reserve0004 is PlatformIgvmParamReserve exactly as 0004 adds it.
func reserve0004(t *testing.T) string {
	t.Helper()
	d := diffOf(t, "0004-ovmf-reserve-the-igvm-parameter-area.patch")
	// Igvm.c's hunk, not the header's prototype.
	d = d[strings.Index(d, "+++ b/"+igvmC):]
	i := strings.Index(d, "+VOID\r\n+EFIAPI\r\n+PlatformIgvmParamReserve (")
	j := strings.Index(d[i:], "+}\r\n")
	if i < 0 || j < 0 {
		t.Fatal("0004 has no PlatformIgvmParamReserve")
	}
	var b strings.Builder
	for _, l := range strings.SplitAfter(d[i:i+j+len("+}\r\n")], "\n") {
		b.WriteString(strings.ReplaceAll(strings.TrimPrefix(l, "+"), "\r", ""))
	}
	return b.String()
}

// igvmModel is the model with 0004's PlatformIgvmParamReserve in it.
func igvmModel(t *testing.T) string {
	return strings.Replace(igvmModelTemplate, "RESERVE\n", reserve0004(t), 1)
}

// igvmTree is a minimal EDK II tree whose Igvm.c is body (written with
// CRLF, as the real one is), plus any other files given.
func igvmTree(t *testing.T, body string, others map[string]string) string {
	t.Helper()
	udk := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(udk, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(s, "\n", "\r\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(igvmC, body)
	for rel, s := range others {
		write(rel, s)
	}
	return udk
}

func TestTheIGVMReadersOfThePatchedTreeAllTestTheFlag(t *testing.T) {
	if err := checkIGVMReaders(igvmTree(t, igvmModel(t), nil)); err != nil {
		t.Fatal(err)
	}
}

func TestANewIGVMReaderWithoutTheFlagFailsTheCheck(t *testing.T) {
	body := igvmModel(t) + `
UINT32
EFIAPI
PlatformIgvmNewField (
  VOID
  )
{
  return *(UINT32 *)(UINTN)(FixedPcdGet64 (PcdOvmfIgvmParamBase) + 0xD0);
}
`
	err := checkIGVMReaders(igvmTree(t, body, nil))
	if err == nil || !strings.Contains(err.Error(), "PlatformIgvmNewField") || !strings.Contains(err.Error(), "docs/decisions/0018") {
		t.Fatalf("err = %v", err)
	}
}

func TestAReaderThatTestsTheFlagTooLateFailsTheCheck(t *testing.T) {
	body := strings.Replace(igvmModel(t), `  if (mIgvmParamsInvalid) {
    return 0;
  }

  Address = FixedPcdGet64 (PcdOvmfIgvmParamBase);`, `  Address = FixedPcdGet64 (PcdOvmfIgvmParamBase);

  if (mIgvmParamsInvalid) {
    return 0;
  }`, 1)
	if body == igvmModel(t) {
		t.Fatal("the model did not change")
	}
	err := checkIGVMReaders(igvmTree(t, body, nil))
	if err == nil || !strings.Contains(err.Error(), "PlatformIgvmVpCount") {
		t.Fatalf("err = %v", err)
	}
}

func TestAReaderInAnotherFileFailsTheCheck(t *testing.T) {
	other := map[string]string{"OvmfPkg/Library/PlatformInitLib/Platform.c": "x = FixedPcdGet64 (PcdOvmfIgvmParamBase);\n"}
	err := checkIGVMReaders(igvmTree(t, igvmModel(t), other))
	if err == nil || !strings.Contains(err.Error(), "OvmfPkg/Library/PlatformInitLib/Platform.c") {
		t.Fatalf("err = %v", err)
	}
}

// What the build writes under Build/ is not source, and is not read.
func TestTheCheckSkipsTheBuildDirectory(t *testing.T) {
	other := map[string]string{"Build/OvmfX64/AutoGen.c": "x = PcdOvmfIgvmParamBase;\n"}
	if err := checkIGVMReaders(igvmTree(t, igvmModel(t), other)); err != nil {
		t.Fatal(err)
	}
}

func TestAFlagThatDoesNotStartTrueFailsTheCheck(t *testing.T) {
	for name, body := range map[string]string{
		"0005 not in":      strings.Replace(igvmModel(t), "mIgvmParamsInvalid = TRUE;", "mIgvmParamsInvalid;", 1),
		"something clears": igvmModel(t) + "\nVOID\nEFIAPI\nReset (\n  VOID\n  )\n{\n  mIgvmParamsInvalid = FALSE;\n}\n",
	} {
		if err := checkIGVMReaders(igvmTree(t, body, nil)); err == nil || !strings.Contains(err.Error(), "mIgvmParamsInvalid") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The OVMF build runs the check on the tree it patched, and builds
// nothing from a tree that fails it.
func TestOVMFRefusesATreeWithAnUncheckedIGVMReader(t *testing.T) {
	f := ovmfFixture(t)
	if err := os.WriteFile(filepath.Join(f.udk(), filepath.FromSlash(igvmC)),
		[]byte("PlatformIgvmParamReserve (\nSTATIC BOOLEAN  mIgvmParamsInvalid = TRUE;\n"+
			"// packer-plugin-macosx: assets/firmware/patches/0005\n"+
			"UINT32\nEFIAPI\nSneaky (\n  VOID\n  )\n{\n  return FixedPcdGet64 (PcdOvmfIgvmParamBase);\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "Sneaky") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls("bash")) != 0 {
		t.Errorf("built anyway")
	}
}

// reader is a function that reads the area after whatever lines guard
// stands for, for the cases below.
func reader(guard string) string {
	return "\nUINT32\nEFIAPI\nPlatformIgvmNewField (\n  VOID\n  )\n{\n  UINT64  Address;\n\n" + guard +
		"  Address = FixedPcdGet64 (PcdOvmfIgvmParamBase);\n  return *(UINT32 *)(UINTN)(Address + 0xD0);\n}\n"
}

// The check fails closed: only a real guard counts, at the top of the
// function, returning, before the read. Text that merely mentions the
// flag -- in a comment, a string, a test that guards nothing, a nested
// block, or code the preprocessor may drop -- does not.
func TestOnlyARealGuardCountsAsTestingTheFlag(t *testing.T) {
	for name, guard := range map[string]string{
		"a comment above":                   "  // if (mIgvmParamsInvalid) {\n  //   return 0;\n  // }\n",
		"a block comment above":             "  /* if (mIgvmParamsInvalid) {\n    return 0;\n  } */\n",
		"a string":                          "  DEBUG ((DEBUG_INFO, \"if (mIgvmParamsInvalid) {\"));\n",
		"a test guarding nothing":           "  if (mIgvmParamsInvalid) {\n    Address = 1;\n  }\n",
		"a guard in a nested block":         "  if (Address == 0) {\n    if (mIgvmParamsInvalid) {\n      return 0;\n    }\n  }\n",
		"a guard the preprocessor may drop": "#if 0\n  if (mIgvmParamsInvalid) {\n    return 0;\n  }\n#endif\n",
		"an expression, not the flag alone": "  if (mIgvmParamsInvalid && Address) {\n    return 0;\n  }\n",
	} {
		err := checkIGVMReaders(igvmTree(t, igvmModel(t)+reader(guard), nil))
		if err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	// The same reader with a real guard passes, so the cases above fail
	// for their guard alone.
	if err := checkIGVMReaders(igvmTree(t, igvmModel(t)+reader("  if (mIgvmParamsInvalid) {\n    return 0;\n  }\n\n"), nil)); err != nil {
		t.Errorf("a real guard: %v", err)
	}
}

// A guard on the reading line itself, after the read, guards nothing.
func TestAFlagTestAfterTheReadOnItsLineDoesNotCount(t *testing.T) {
	body := igvmModel(t) + "\nUINT32\nEFIAPI\nPlatformIgvmNewField (\n  VOID\n  )\n{\n" +
		"  return (UINT32)FixedPcdGet64 (PcdOvmfIgvmParamBase); // if (mIgvmParamsInvalid) {\n}\n"
	if err := checkIGVMReaders(igvmTree(t, body, nil)); err == nil || !strings.Contains(err.Error(), "PlatformIgvmNewField") {
		t.Fatalf("err = %v", err)
	}
}

// Every use of the flag must be one the check knows: the declaration,
// a guard, or setting it TRUE. Anything else -- however it is spelled --
// might clear it.
func TestAnyOtherUseOfTheFlagFailsTheCheck(t *testing.T) {
	for name, line := range map[string]string{
		"FALSE, spaced":        "  mIgvmParamsInvalid  = FALSE;\n",
		"FALSE, unspaced":      "  mIgvmParamsInvalid=FALSE;\n",
		"zero":                 "  mIgvmParamsInvalid = 0;\n",
		"a cast zero":          "  mIgvmParamsInvalid = (BOOLEAN)0;\n",
		"its address":          "  ZeroMem (&mIgvmParamsInvalid, sizeof mIgvmParamsInvalid);\n",
		"a computed value":     "  mIgvmParamsInvalid = Address == 0;\n",
		"a second declaration": "",
	} {
		body := igvmModel(t) + "\nVOID\nEFIAPI\nReset (\n  VOID\n  )\n{\n  UINT64  Address;\n\n  Address = 0;\n" + line + "}\n"
		if name == "a second declaration" {
			body = igvmModel(t) + "\nSTATIC BOOLEAN  mIgvmParamsInvalid = TRUE;\n"
		}
		if err := checkIGVMReaders(igvmTree(t, body, nil)); err == nil || !strings.Contains(err.Error(), "mIgvmParamsInvalid") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// PlatformIgvmParamReserve is exempt only as 0004 wrote it. One that also
// reads the page, or is changed in any other way, fails until someone
// re-reads it.
func TestTheReservationIsExemptOnlyAsWritten(t *testing.T) {
	model := igvmModel(t)
	changed := strings.Replace(model, "  if (Base && Size) {\n",
		"  if (*(UINT32 *)(UINTN)(Base + 0xC0) != 0) {\n    return;\n  }\n\n  if (Base && Size) {\n", 1)
	if changed == model {
		t.Fatal("the model did not change")
	}
	err := checkIGVMReaders(igvmTree(t, changed, nil))
	if err == nil || !strings.Contains(err.Error(), "PlatformIgvmParamReserve") {
		t.Fatalf("err = %v", err)
	}
}

// A function left open, or a brace too many, is not a tree the check can
// read, so it does not pass.
func TestUnbalancedBracesFailTheCheck(t *testing.T) {
	for name, body := range map[string]string{
		"left open":    igvmModel(t) + "\nVOID\nEFIAPI\nOpen (\n  VOID\n  )\n{\n",
		"one too many": igvmModel(t) + "\n}\n",
	} {
		if err := checkIGVMReaders(igvmTree(t, body, nil)); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// The exempt digest is 0004's PlatformIgvmParamReserve, which is
// upstream's byte for byte: re-pinning it means re-reading that function.
func TestTheReservationsDigestIsZeroZeroZeroFours(t *testing.T) {
	r := reserve0004(t)
	// From its name, as the check collects a function.
	r = r[strings.Index(r, "PlatformIgvmParamReserve ("):]
	if got, want := normalizedFunc(stripC(r)), igvmReservers["PlatformIgvmParamReserve"]; got != want {
		t.Errorf("0004's PlatformIgvmParamReserve digests to %s; igvmReservers pins %s", got, want)
	}
}

// The check fails closed by structure: every use of the PCD or the flag
// must sit in the body of a function it has recognised by EDK II's own
// header style, and every top-level brace block must be one it can
// classify. A header in any other style is not guessed at.
func TestAFunctionTheCheckCannotRecogniseFailsIt(t *testing.T) {
	read := "{\n  return *(UINT32 *)(UINTN)FixedPcdGet64 (PcdOvmfIgvmParamBase);\n}\n"
	for name, header := range map[string]string{
		"one line":                      "UINT32 Foo (VOID)\n",
		"type above, name and params":   "UINT32\nFoo (VOID)\n",
		"K&R-ish":                       "UINT32\nFoo (Bar)\n  UINT32 Bar;\n",
		"brace on the ) line":           "UINT32\nEFIAPI\nFoo (\n  VOID\n  ) ",
		"multi-line, brace on the name": "UINT32\nFoo (\n  IN UINT32  Bar,\n  IN UINT32  Baz\n  ) ",
	} {
		// After the model, whose last function is guarded: nothing may
		// carry over from it.
		err := checkIGVMReaders(igvmTree(t, igvmModel(t)+"\n"+header+read, nil))
		if err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

func TestAFlagUseOutsideARecognisedFunctionFailsTheCheck(t *testing.T) {
	err := checkIGVMReaders(igvmTree(t, igvmModel(t)+"\nVOID Foo (VOID)\n{\n  mIgvmParamsInvalid = TRUE;\n}\n", nil))
	if err == nil {
		t.Fatal("passed")
	}
}

func TestATopLevelBlockTheCheckCannotClassifyFailsIt(t *testing.T) {
	for name, block := range map[string]string{
		"an initialised table": "STATIC UINT32  mTable[] = {\n  1,\n  2\n};\n",
		"a bare block":         "{\n  x = 1;\n}\n",
	} {
		err := checkIGVMReaders(igvmTree(t, igvmModel(t)+"\n"+block, nil))
		if err == nil || !strings.Contains(err.Error(), "cannot classify") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestThePCDInsideATypeFailsTheCheck(t *testing.T) {
	body := igvmModel(t) + "\ntypedef struct X {\n  UINT64  A[FixedPcdGet64 (PcdOvmfIgvmParamBase)];\n} X;\n"
	if err := checkIGVMReaders(igvmTree(t, body, nil)); err == nil {
		t.Fatal("passed")
	}
}

// A guard whose opening brace is commented out leaves the braces
// unbalanced; the message says so, not only that the PCD is out of place.
func TestUnbalancedBracesAreNamedAsSuch(t *testing.T) {
	body := strings.Replace(igvmModel(t), "  if (mIgvmParamsInvalid) {\n    return 0;", "  // if (mIgvmParamsInvalid) {\n    return 0;", 1)
	err := checkIGVMReaders(igvmTree(t, body, nil))
	if err == nil || !strings.Contains(err.Error(), "braces do not balance") {
		t.Fatalf("err = %v", err)
	}
}

// Another file that names the PCD only in a comment does not read it.
func TestAMentionInAnotherFilesCommentIsNotARead(t *testing.T) {
	other := map[string]string{"OvmfPkg/Library/PlatformInitLib/Platform.c": "// see PcdOvmfIgvmParamBase in Igvm.c\n/* and PcdOvmfIgvmParamBase */\nx = \"PcdOvmfIgvmParamBase\";\n"}
	if err := checkIGVMReaders(igvmTree(t, igvmModel(t), other)); err != nil {
		t.Fatal(err)
	}
}

// The guard must be the reader's first statement, after its local
// declarations only: a guard behind a condition, an else, or a jump may
// not run.
func TestAGuardThatIsNotTheFirstStatementDoesNotCount(t *testing.T) {
	g := "  if (mIgvmParamsInvalid) {\n    return 0;\n  }\n\n"
	for name, guard := range map[string]string{
		"behind a braceless if": "  if (Address == 0)\n" + g,
		"behind an else":        "  if (Address == 0) {\n  }\n  else\n" + g,
		"after a statement":     "  Address = 1;\n" + g,
	} {
		if err := checkIGVMReaders(igvmTree(t, igvmModel(t)+reader(guard), nil)); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// A reader may not jump: a goto or a label can step past a guard.
func TestAReaderWithAJumpFailsTheCheck(t *testing.T) {
	g := "  if (mIgvmParamsInvalid) {\n    return 0;\n  }\n\n"
	for name, body := range map[string]string{
		"a goto past the guard": "  goto Skip;\n" + g + "Skip:\n",
		"a label":               g + "Again:\n",
		"a goto":                g + "  goto Out;\n",
	} {
		err := checkIGVMReaders(igvmTree(t, igvmModel(t)+reader(body), nil))
		if err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// A backslash at the end of a line splices it to the next, which the
// check's line-by-line reading does not model: any is a shape it refuses.
func TestALineSpliceFailsTheCheck(t *testing.T) {
	for name, extra := range map[string]string{
		"a comment carried onto the guard": "  // a note \\\n" + "  if (mIgvmParamsInvalid) {\n    return 0;\n  }\n",
		"the PCD split":                    "  Address = FixedPcdGet64 (PcdOvmfIgvm\\\nParamBase);\n",
	} {
		err := checkIGVMReaders(igvmTree(t, igvmModel(t)+reader(extra), nil))
		if err == nil || !strings.Contains(err.Error(), "backslash") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// A macro can change what the guard or the flag means without naming
// either where they are used.
func TestAMacroOverWhatTheCheckReadsFailsIt(t *testing.T) {
	for name, d := range map[string]string{
		"TRUE":     "#define TRUE FALSE\n",
		"FALSE":    "#  define FALSE 1\n",
		"undef":    "#undef TRUE\n",
		"the flag": "#define mIgvmParamsInvalid mOther\n",
		"the PCD":  "#define PcdOvmfIgvmParamBase PcdOther\n",
		"if":       "#define if(x) if (1)\n",
		"return":   "#define return\n",
	} {
		err := checkIGVMReaders(igvmTree(t, d+igvmModel(t), nil))
		if err == nil || !strings.Contains(err.Error(), "macro") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
