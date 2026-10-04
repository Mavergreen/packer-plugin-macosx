package firmware

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// The compiler range: a claim about which compilers this project has a
// reason to believe in, not a pin (docs/decisions/0004 records what each
// row rests on). The constants are held equal to their golden by
// TestTheDeclaredRangeIsGcc13Through16.
const (
	CCFamily       = "gcc"
	CCFloor        = 13
	CCCeiling      = 16
	CCVerified     = "13.3.0"
	CCVerifiedList = "13.3.0, 14.2.0 and 16.2.1"
)

// RangeText is the declared range as one phrase.
func RangeText() string {
	return fmt.Sprintf("%s %d through %d, verified at %s %s", CCFamily, CCFloor, CCCeiling, CCFamily, CCVerifiedList)
}

var versionField = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// versionToken is the banner's first field that is a bare dotted number.
func versionToken(banner string) string {
	for _, f := range strings.Fields(banner) {
		if versionField.MatchString(f) {
			return f
		}
	}
	return ""
}

// ParseCompiler reads a `cc --version` first line. clang is checked
// first: on macOS `gcc` IS clang, and parsing its banner as GCC would
// judge a clang version against a GCC range.
func ParseCompiler(banner string) (family, version string) {
	if strings.Contains(banner, "clang") || strings.Contains(banner, "LLVM") {
		return "clang", versionToken(banner)
	}
	first, _, _ := strings.Cut(banner, " ")
	switch {
	case first == "gcc", first == "cc", first == "c99", first == "g++",
		strings.HasSuffix(first, "-gcc"), strings.HasSuffix(first, "-g++"):
		if v := versionToken(banner); v != "" {
			return "gcc", v
		}
	}
	return "unknown", ""
}

// RangeVerdict judges a compiler against the range. Pure, so every branch
// is testable on a host with one compiler.
func RangeVerdict(family, version string) (verdict, detail string) {
	switch family {
	case CCFamily:
	case "clang":
		v := version
		if v == "" {
			v = "(no version)"
		}
		return "UNKNOWN", fmt.Sprintf("clang %s: this project builds with TOOLCHAINS=GCC and has never built with clang, so the declared range (%s) does not cover it", v, RangeText())
	default:
		return "UNKNOWN", fmt.Sprintf("not a recognised %s: the range (%s) has nothing to say about it", CCFamily, RangeText())
	}
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || strings.Trim(majorText, "0123456789") != "" {
		return "UNKNOWN", `cannot read a major version out of "` + version + `"`
	}
	switch {
	case major < CCFloor:
		return "BELOW", fmt.Sprintf("%s %s is below the floor of this project's supported range, %s", family, version, RangeText())
	case major > CCCeiling:
		return "ABOVE", fmt.Sprintf("%s %s is above the ceiling of this project's supported range, %s", family, version, RangeText())
	}
	return "INSIDE", fmt.Sprintf("%s %s is inside this project's supported range, %s", family, version, RangeText())
}

// Toolchain is the host compiler EDK II will run: DEF(GCC_X64_PREFIX)gcc,
// where GCC_X64_PREFIX is ENV(GCC_BIN) -- usually empty, so plain gcc.
// Override is mavericks-firmware's compiler option: "NAME VERSION" to
// believe instead of asking the compiler. It moves nothing else;
// CompilerLine still reports the real compiler, so the build log shows
// the two disagreeing.
type Toolchain struct {
	Runner   proc.Runner
	GCCBin   string
	Override string
}

// GCC is the compiler's command name.
func (tc Toolchain) GCC() string { return tc.GCCBin + "gcc" }

// output is the first line cmd prints, or "" if it cannot be run.
func (tc Toolchain) output(ctx context.Context, args ...string) string {
	if _, err := tc.Runner.LookPath(tc.GCC()); err != nil {
		return ""
	}
	var out bytes.Buffer
	if err := tc.Runner.Run(ctx, proc.Cmd{Name: tc.GCC(), Args: args, Stdout: &out}); err != nil {
		return ""
	}
	line, _, _ := strings.Cut(out.String(), "\n")
	return strings.TrimSpace(line)
}

// Status is this host's compiler against the range. An UNKNOWN names
// what it could not read: a banner it cannot parse, or no compiler.
func (tc Toolchain) Status(ctx context.Context) (verdict, detail string) {
	banner := tc.Override
	if banner == "" {
		banner = tc.output(ctx, "--version")
	}
	verdict, detail = RangeVerdict(ParseCompiler(banner))
	if verdict == "UNKNOWN" {
		if banner != "" {
			detail += `; it said "` + banner + `"`
		} else {
			detail += "; " + tc.GCC() + " is not on PATH or did not answer --version"
		}
	}
	return verdict, detail
}

// CompilerLine is the compiler EDK II will actually run, as one line:
// never influenced by Override.
func (tc Toolchain) CompilerLine(ctx context.Context) string {
	ver := tc.output(ctx, "--version")
	if ver == "" {
		if _, err := tc.Runner.LookPath(tc.GCC()); err != nil {
			return tc.GCC() + " not found"
		}
		ver = "unknown"
	}
	target := tc.output(ctx, "-dumpmachine")
	if target == "" {
		target = "unknown-target"
	}
	return fmt.Sprintf("%s (%s) -std=%s", ver, target, CStd)
}

// Check is the gate the builds call before anything expensive. Only a
// compiler below the floor stops it; above the ceiling and "cannot tell"
// warn and carry on.
func (tc Toolchain) Check(ctx context.Context, logf func(string, ...any)) error {
	v, d := tc.Status(ctx)
	if tc.Override != "" {
		logf("warning: the compiler option is set: treating this host's compiler as %q", tc.Override)
	}
	switch v {
	case "INSIDE":
		logf("compiler: %s", d)
	case "ABOVE":
		logf("warning: compiler: %s", d)
		for _, l := range []string{
			"this is untested territory, not known-bad: building anyway.",
			"A new compiler's new warnings do not stop the firmware build (upstream's",
			"-Werror is not inherited -- decisions/0004). They are still printed, and up",
			"here they are worth reading. Up here the failure mode is usually not an error:",
			"OvmfPkg compiled clean under C23 and produced different firmware bytes",
			"(OVMF_CODE.fd 3373692a..., where docs/decisions/0004 records 195c4dcf...).",
			"So a green build is not proof you got the artifacts decisions/0004 describes:",
			"compare its checksums against what you built. Either answer is worth",
			"reporting -- that is how the ceiling moves.",
		} {
			logf("warning: %s", l)
		}
	case "UNKNOWN":
		logf("warning: compiler: %s", d)
		logf("warning: proceeding with the range unchecked. If you know what this compiler is, say so: compiler = \"<name> <version>\".")
	case "BELOW":
		logf("warning: compiler: %s", d)
		for _, l := range []string{
			"This project has NOT tested it. That is not the same as knowing it fails:",
			"nobody has ever tried. The firmware builds do not inherit upstream's",
			"-Werror, so a diagnostic this compiler spells differently is not fatal",
			"-- but its code generation is still a different artifact than the checksums",
			"in docs/decisions/0004 describe.",
			fmt.Sprintf("To build anyway, say what to believe: compiler = \"%s %s\".", CCFamily, CCVerified),
			"The build log still records the real compiler, so the two lines will",
			"disagree where anyone can see them.",
		} {
			logf("warning: %s", l)
		}
		return fmt.Errorf("unsupported compiler -- %s", d)
	}
	return nil
}
