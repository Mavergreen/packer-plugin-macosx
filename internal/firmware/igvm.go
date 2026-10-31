package firmware

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The IGVM parameter area is the page at PcdOvmfIgvmParamBase that OVMF
// would read IGVM parameters from (docs/decisions/0018). Patch 0005 makes
// mIgvmParamsInvalid start TRUE. That keeps OVMF from reading the page only
// while three things hold: every reader of PcdOvmfIgvmParamBase tests the
// flag before it reads the PCD, the flag starts TRUE, and nothing sets it
// FALSE. An audk bump can break any of them while 0005 still applies
// cleanly, so checkIGVMReaders checks them on the patched tree before
// every OVMF build.
//
// It is a tripwire for drift, not a proof, and it fails closed by
// structure. It reads EDK II's own style of C and nothing else:
//   - a function is "Name (" alone on a line, one parameter per line, ")"
//     alone, then "{" alone; a guard is `if (mIgvmParamsInvalid) {` /
//     `return ...;` / `}` at the top of its body;
//   - every top-level brace block must be such a function or a
//     `typedef struct|union|enum ... {`, or the check fails;
//   - every use of the PCD and of the flag must sit in the body of a
//     function it recognised (the flag's one declaration aside), or the
//     check fails.
//   - the guard must be the body's first statement, after nothing but
//     local declarations, and a body that names the PCD may not jump
//     (goto, or a label);
//   - a backslash-newline anywhere, or a #define/#undef over TRUE, FALSE,
//     if, return, the flag or the PCD, is refused.
// A header in another style is not guessed at. Anything the check does
// not recognise is an error for a person to re-read, not a pass. It is
// still a tripwire against drift in EDK II-style code, not a proof about
// arbitrary C: what it cannot see includes code that reaches the page
// without naming the PCD, such as a literal 0x812000.

// igvmC is the file that reads the area, which 0004 and 0005 patch and
// their markers are checked in.
const igvmC = "OvmfPkg/Library/PlatformInitLib/Igvm.c"

const igvmParamPcd = "PcdOvmfIgvmParamBase"

// igvmReservers read PcdOvmfIgvmParamBase only to pass the address on,
// never to read the page. Each is exempt only while its body is exactly
// what was read: the sha256 of its comment- and string-stripped text,
// whitespace collapsed (normalizedFunc). PlatformIgvmParamReserve is
// 0004's, which hands the address to BuildMemoryAllocationHob;
// TestTheReservationIsExemptOnlyAsWritten builds its digest from 0004.
var igvmReservers = map[string]string{
	"PlatformIgvmParamReserve": "bcd7ffe8505117d246303d56ccfc542984d73469cd991ce3a2d22ca4c4465b03",
}

var (
	cFuncName   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*) \($`)
	macroOver   = regexp.MustCompile(`^#\s*(define|undef)\b.*\b(TRUE|FALSE|if|return|mIgvmParamsInvalid|PcdOvmfIgvmParamBase)\b`)
	localDecl   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*\s+)+\**\s*[A-Za-z_][A-Za-z0-9_]*;$`)
	notDecl     = regexp.MustCompile(`^(return|goto|break|continue|else|do|case)\b`)
	jump        = regexp.MustCompile(`\bgoto\b|^[A-Za-z_][A-Za-z0-9_]*\s*:$`)
	typedefOpen = regexp.MustCompile(`^typedef (struct|union|enum)( [A-Za-z_][A-Za-z0-9_]*)? \{$`)
	flagDecl    = regexp.MustCompile(`^STATIC BOOLEAN\s+mIgvmParamsInvalid\s*=\s*TRUE\s*;$`)
	flagSet     = regexp.MustCompile(`^mIgvmParamsInvalid\s*=\s*TRUE\s*;$`)
	guardRet    = regexp.MustCompile(`^return\b[^;{}]*;$`)
)

const flagGuard = "if (mIgvmParamsInvalid) {"

// stripC is src's lines, CR dropped, with comments removed and the
// contents of string and character literals emptied, so that neither can
// look like code to the check.
func stripC(src string) []string {
	src = strings.ReplaceAll(src, "\r", "")
	var out strings.Builder
	const (
		code = iota
		line
		block
		str
		chr
	)
	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch state {
		case code:
			switch {
			case c == '/' && next == '/':
				state, i = line, i+1
			case c == '/' && next == '*':
				state, i = block, i+1
				out.WriteByte(' ')
			case c == '"':
				state = str
				out.WriteByte(c)
			case c == '\'':
				state = chr
				out.WriteByte(c)
			default:
				out.WriteByte(c)
			}
		case line:
			if c == '\n' {
				state = code
				out.WriteByte(c)
			}
		case block:
			if c == '*' && next == '/' {
				state, i = code, i+1
			} else if c == '\n' {
				out.WriteByte(c)
			}
		case str, chr:
			quote := byte('"')
			if state == chr {
				quote = '\''
			}
			switch {
			case c == '\\':
				i++
			case c == quote:
				state = code
				out.WriteByte(c)
			case c == '\n':
				out.WriteByte(c)
			}
		}
	}
	return strings.Split(out.String(), "\n")
}

// normalizedFunc is a function's stripped lines with all whitespace
// collapsed, hashed.
func normalizedFunc(lines []string) string {
	s := sha256.Sum256([]byte(strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")))
	return hex.EncodeToString(s[:])
}

// checkIGVMReaders says what, in the EDK II tree at udk, would let OVMF
// read the IGVM parameter area despite 0005, or nil.
func checkIGVMReaders(udk string) error {
	const why = " -- so patch 0005 may no longer keep OVMF from reading the IGVM parameter area; " +
		"re-read Igvm.c against docs/decisions/0018 before using this audk"
	data, err := os.ReadFile(filepath.Join(udk, filepath.FromSlash(igvmC)))
	if err != nil {
		return err
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf(igvmC+": "+format+why, a...)
	}
	const unbalanced = " (or its braces do not balance)"
	var (
		depth, decls     int
		kind             int      // the top-level block depth > 0 is in
		hdr              int      // at depth 0: 1 after "Name (", 2 after its ")"
		hdrName, name    string   // the header being read; the function being checked
		fn               []string // the current function's lines, from its name
		guarded, unguard bool     // a real guard seen; the PCD read before one
		guardStep        int      // 1: saw the guard's if; 2: saw its return
		first            bool     // no statement yet in this body, only declarations
		readsPcd, jumps  bool     // this body names the PCD; it has a goto or a label
	)
	const (
		none = iota
		function
		aggregate
	)
	// A backslash-newline splices two lines into one, which this
	// line-by-line reading does not model: refused, not guessed at.
	for n, l := range strings.Split(string(data), "\n") {
		if strings.HasSuffix(strings.TrimRight(l, "\r"), "\\") {
			return fail("line %d ends in a backslash, splicing it to the next: a shape the check does not read", n+1)
		}
	}
	for n, raw := range stripC(string(data)) {
		l := strings.TrimSpace(raw)
		at := n + 1
		opens, closes := strings.Count(l, "{"), strings.Count(l, "}")
		// A macro can change what the guard or the flag means without
		// naming either where they are used.
		if macroOver.MatchString(l) {
			return fail("line %d: a macro over TRUE, FALSE, if, return, mIgvmParamsInvalid or %s (%q)", at, igvmParamPcd, l)
		}
		if depth == 0 {
			// EDK II's header: "Name (" alone on a line, a parameter per
			// line, ")" alone, then "{" alone. Nothing else opens a
			// function body; a header in any other style is not guessed at.
			switch m := cFuncName.FindStringSubmatch(l); {
			case m != nil:
				hdr, hdrName, fn = 1, m[1], []string{l}
			case hdr == 1 && l == ")":
				hdr, fn = 2, append(fn, l)
			case hdr == 1 && !strings.ContainsAny(l, "{};"):
				fn = append(fn, l)
			case hdr == 2 && (l == "" || l == "{"):
			default:
				hdr = 0
			}
			if opens > 0 {
				switch {
				case l == "{" && hdr == 2:
					kind, name, fn = function, hdrName, append(fn, l)
					guarded, unguard, guardStep = false, false, 0
					first, readsPcd, jumps = true, false, false
				case opens == 1 && closes == 0 && typedefOpen.MatchString(l):
					kind = aggregate
				default:
					return fail("line %d opens a top-level block the check cannot classify (%q): only EDK II-style functions and typedefs are read", at, l)
				}
				hdr = 0
			}
			if strings.Contains(l, igvmParamPcd) {
				return fail("line %d names %s outside any function the check recognised%s", at, igvmParamPcd, unbalanced)
			}
			if strings.Contains(l, "mIgvmParamsInvalid") {
				if !flagDecl.MatchString(l) {
					return fail("line %d uses mIgvmParamsInvalid outside any function the check recognised (%q)%s", at, l, unbalanced)
				}
				decls++
			}
			if depth += opens - closes; depth < 0 {
				return fail("line %d closes a brace that was never opened: its braces do not balance", at)
			}
			continue
		}
		if kind == aggregate {
			if strings.Contains(l, igvmParamPcd) || strings.Contains(l, "mIgvmParamsInvalid") {
				return fail("line %d names %s or mIgvmParamsInvalid inside a type", at, igvmParamPcd)
			}
		} else {
			fn = append(fn, l)
			if strings.HasPrefix(l, "#") {
				return fail("line %d: a preprocessor directive inside %s", at, name)
			}
			// The PCD before the flag: a guard on the reading line itself
			// comes after the read.
			if strings.Contains(l, igvmParamPcd) {
				readsPcd = true
				if !guarded {
					unguard = true
				}
			}
			if jump.MatchString(l) {
				jumps = true
			}
			if strings.Contains(l, "mIgvmParamsInvalid") {
				switch {
				// The guard counts only as the body's first statement,
				// after nothing but local declarations: behind a
				// condition, an else or a jump it may not run.
				case depth == 1 && l == flagGuard && guardStep == 0 && first:
					guardStep, first = 1, false
				case flagSet.MatchString(l):
					first = false
				default:
					return fail("line %d in %s uses mIgvmParamsInvalid in a way the check does not know (%q): not a first-statement guard, and it may clear it", at, name, l)
				}
			} else if l != "" {
				if first && (!localDecl.MatchString(l) || notDecl.MatchString(l)) {
					first = false
				}
				switch {
				case guardStep == 1 && guardRet.MatchString(l):
					guardStep = 2
				case guardStep == 2 && l == "}":
					guarded, guardStep = true, 0
				default:
					guardStep = 0
				}
			}
		}
		if depth += opens - closes; depth < 0 {
			return fail("line %d closes a brace that was never opened: its braces do not balance", at)
		}
		if depth == 0 {
			if kind == function && readsPcd && jumps {
				return fail("%s names %s and jumps (a goto or a label), which can step past a guard", name, igvmParamPcd)
			}
			if kind == function && unguard {
				if want, ok := igvmReservers[name]; !ok {
					return fail("%s reads %s without first returning if mIgvmParamsInvalid is set", name, igvmParamPcd)
				} else if got := normalizedFunc(fn); got != want {
					return fail("%s is exempt as an address-only reservation only as it was read (%s), and it is now %s", name, want, got)
				}
			}
			kind, name, fn = none, "", nil
		}
	}
	if depth != 0 {
		return fail("a block is left open at the end of the file: its braces do not balance")
	}
	if decls != 1 {
		return fail("%d declarations of STATIC BOOLEAN mIgvmParamsInvalid = TRUE, not 1", decls)
	}
	// Any other source whose code names the PCD is a reader 0005 does not
	// cover. A comment that names it reads nothing.
	return filepath.WalkDir(udk, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(udk, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "Build" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == igvmC || !d.Type().IsRegular() || (!strings.HasSuffix(p, ".c") && !strings.HasSuffix(p, ".h")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(strings.Join(stripC(string(b)), "\n"), igvmParamPcd) {
			return fmt.Errorf("%s names %s, and 0005 covers only %s%s", rel, igvmParamPcd, igvmC, why)
		}
		return nil
	})
}
