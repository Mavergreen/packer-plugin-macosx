package firmware

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// goldenLines is golden(t, name) split into lines: each of these files
// holds one case per line, in the order the test lists its cases.
func goldenLines(t *testing.T, name string) []string {
	return strings.Split(strings.TrimSuffix(golden(t, name), "\n"), "\n")
}

func TestRangeVerdictMatchesItsGolden(t *testing.T) {
	cases := [][2]string{
		{"gcc", "12.4.0"}, {"gcc", "13.0.0"}, {"gcc", "13.3.0"}, {"gcc", "15.1.1"},
		{"gcc", "16.2.1"}, {"gcc", "17.0.0"}, {"gcc", "x.y"}, {"clang", "17.0.0"},
		{"clang", ""}, {"unknown", ""},
	}
	want := goldenLines(t, "compiler-range-verdict.txt")
	if len(want) != len(cases) {
		t.Fatalf("%d golden lines, want %d", len(want), len(cases))
	}
	for i, c := range cases {
		v, d := RangeVerdict(c[0], c[1])
		if got := v + "\t" + d; got != want[i] {
			t.Errorf("%v:\n got:    %q\n golden: %q", c, got, want[i])
		}
	}
}

func TestParseCompilerMatchesItsGolden(t *testing.T) {
	banners := []string{
		"gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0",
		"gcc (GCC) 15.1.1 20250425",
		"cc (GCC) 14.2.0",
		"x86_64-linux-gnu-gcc (Debian 14.2.0-19) 14.2.0",
		"Apple clang version 17.0.0 (clang-1700.0.13)",
		"clang version 18.1.3",
		"gcc 13.3.0",
		"tcc version 0.9.27",
		"",
	}
	want := goldenLines(t, "compiler-parse.txt")
	if len(want) != len(banners) {
		t.Fatalf("%d golden lines, want %d", len(want), len(banners))
	}
	for i, b := range banners {
		fam, ver := ParseCompiler(b)
		if got := fam + "\t" + ver + "\t" + b; got != want[i] {
			t.Errorf("%q:\n got:    %q\n golden: %q", b, got, want[i])
		}
	}
}

func TestTheDeclaredRangeIsGcc13Through16(t *testing.T) {
	if RangeText() != "gcc 13 through 16, verified at gcc 13.3.0, 14.2.0 and 16.2.1" {
		t.Fatal(RangeText())
	}
	if got := golden(t, "compiler-range-text.txt"); got != RangeText()+"\n" {
		t.Fatalf("the golden says %q", got)
	}
}

// gccFake answers `gcc --version` and `gcc -dumpmachine` as banner and
// target, and knows gcc is on PATH unless banner is "".
func gccFake(banner, target string) *proc.Fake {
	f := &proc.Fake{Paths: map[string]string{}}
	if banner != "" {
		f.Paths["gcc"] = "/usr/bin/gcc"
	}
	f.Handle = func(c proc.Cmd) error {
		if c.Name == "gcc" && len(c.Args) == 1 && c.Stdout != nil {
			switch c.Args[0] {
			case "--version":
				c.Stdout.Write([]byte(banner + "\nCopyright (C) 2023\n"))
			case "-dumpmachine":
				c.Stdout.Write([]byte(target + "\n"))
			}
		}
		return nil
	}
	return f
}

func TestStatusNamesWhatItCouldNotRead(t *testing.T) {
	ctx := context.Background()
	v, d := Toolchain{Runner: gccFake("tcc version 0.9.27", "x")}.Status(ctx)
	if v != "UNKNOWN" || !strings.HasSuffix(d, `; it said "tcc version 0.9.27"`) {
		t.Fatalf("%s %s", v, d)
	}
	v, d = Toolchain{Runner: gccFake("", "")}.Status(ctx)
	if v != "UNKNOWN" || !strings.HasSuffix(d, "; gcc is not on PATH or did not answer --version") {
		t.Fatalf("%s %s", v, d)
	}
}

func TestTheOverrideReplacesDetection(t *testing.T) {
	tc := Toolchain{Runner: gccFake("gcc (GCC) 12.1.0", "x"), Override: "gcc 15.1.0"}
	if v, d := tc.Status(context.Background()); v != "INSIDE" || !strings.HasPrefix(d, "gcc 15.1.0 is inside") {
		t.Fatalf("verdict %s: %s", v, d)
	}
	// The override moves nothing else: the compiler line is the real one.
	if cl := tc.CompilerLine(context.Background()); !strings.HasPrefix(cl, "gcc (GCC) 12.1.0 (x) -std=gnu17") {
		t.Fatal(cl)
	}
}

func TestCompilerLine(t *testing.T) {
	ctx := context.Background()
	tc := Toolchain{Runner: gccFake("gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0", "x86_64-linux-gnu")}
	if got := tc.CompilerLine(ctx); got != "gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0 (x86_64-linux-gnu) -std=gnu17" {
		t.Fatal(got)
	}
	if got := (Toolchain{Runner: gccFake("", "")}).CompilerLine(ctx); got != "gcc not found" {
		t.Fatal(got)
	}
	if got := (Toolchain{Runner: gccFake("", ""), GCCBin: "x86_64-elf-"}).GCC(); got != "x86_64-elf-gcc" {
		t.Fatal(got)
	}
}

func TestCheckRefusesBelowTheFloorAndWarnsAbove(t *testing.T) {
	ctx := context.Background()
	var log bytes.Buffer
	logf := func(f string, a ...any) { log.WriteString(fmt.Sprintf(f, a...) + "\n") }

	err := Toolchain{Runner: gccFake("gcc (GCC) 12.2.0", "x")}.Check(ctx, logf)
	if err == nil || !strings.Contains(err.Error(), "below the floor") {
		t.Fatalf("below: %v", err)
	}
	if !strings.Contains(log.String(), "NOT tested") || !strings.Contains(log.String(), "compiler = ") {
		t.Fatalf("below log: %s", log.String())
	}
	// The BELOW warning explains that -Werror is not inherited, so a
	// diagnostic this compiler spells differently is not fatal.
	if !strings.Contains(log.String(), "do not inherit") || !strings.Contains(log.String(), "is not fatal") {
		t.Fatalf("below log missing the -Werror sentence: %s", log.String())
	}

	log.Reset()
	if err := (Toolchain{Runner: gccFake("gcc (GCC) 17.1.0", "x")}).Check(ctx, logf); err != nil {
		t.Fatalf("above must proceed: %v", err)
	}
	if !strings.Contains(log.String(), "above the ceiling") || !strings.Contains(log.String(), "not proof") {
		t.Fatalf("above log: %s", log.String())
	}
	// The ABOVE warning names the two concrete checksums and says to
	// compare against what was built: the evidence stays in the warning.
	if !strings.Contains(log.String(), "3373692a") || !strings.Contains(log.String(), "195c4dcf") ||
		!strings.Contains(log.String(), "compare its checksums against what you built") {
		t.Fatalf("above log missing the concrete evidence: %s", log.String())
	}

	log.Reset()
	if err := (Toolchain{Runner: gccFake("tcc version 0.9.27", "x")}).Check(ctx, logf); err != nil {
		t.Fatalf("unknown must proceed: %v", err)
	}
	if !strings.Contains(log.String(), "compiler = ") {
		t.Fatalf("unknown log: %s", log.String())
	}
}

func TestCcacheIsOffByDefault(t *testing.T) {
	if CcacheDefault {
		t.Fatal("ccache must stay off until someone measures it (docs/decisions/0004)")
	}
}

// The verdicts are the golden file's, word for word.
func TestCcacheVerdictMatchesItsGolden(t *testing.T) {
	cases := []struct {
		wanted bool
		path   string
	}{{true, "/usr/bin/ccache"}, {true, ""}, {false, "/usr/bin/ccache"}, {false, ""}}
	want := goldenLines(t, "ccache-verdict.txt")
	if len(want) != len(cases) {
		t.Fatalf("%d golden lines, want %d", len(want), len(cases))
	}
	for i, c := range cases {
		v, d := CcacheVerdict(c.wanted, c.path)
		if got := v + "\t" + d; got != want[i] {
			t.Errorf("%v:\n got:    %q\n golden: %q", c, got, want[i])
		}
	}
}

func TestTheShimWrapsTheRealCompilerByAbsolutePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := filepath.Join(t.TempDir(), "ccache-bin")
	look := func(n string) (string, error) {
		if n == "gcc" {
			return "/usr/bin/gcc", nil
		}
		return "", exec.ErrNotFound
	}
	if err := writeCcacheShims(dir, "/usr/bin/ccache", look); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gcc"))
	if err != nil || string(b) != "#!/bin/sh\nexec /usr/bin/ccache /usr/bin/gcc \"$@\"\n" {
		t.Fatalf("%q %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "gcc")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(dir, "g++")); err == nil {
		t.Fatal("no g++ on PATH, so no g++ shim")
	}
}
