package firmware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// The CPU baseline check (docs/decisions/0042). Both builds state March,
// and then each refuses to ship a module in which the compiler generated
// an instruction x86-64 itself does not have: a guest CPU model without
// it takes #UD, as Penryn did on Ubuntu 26.04's gcc, whose default is
// x86-64-v3. Assembly is not the compiler's: OpenSSL's and OpenCore's
// AVX routines, and OVMF's register save code, run only where CPUID says
// they may, and are there on every host. So an instruction counts only
// when its debug line names a C source.

// objdumpTool is the objdump beside gcc: GCC_BIN prefixes binutils too.
func objdumpTool(gcc string) string { return strings.TrimSuffix(gcc, "gcc") + "objdump" }

// legacyPrefix is every byte that may precede an x86-64 opcode without
// being one: operand and address size, lock, rep and the segments.
var legacyPrefix = map[byte]bool{0x26: true, 0x2e: true, 0x36: true, 0x3e: true, 0x64: true, 0x65: true,
	0x66: true, 0x67: true, 0xf0: true, 0xf2: true, 0xf3: true}

// newerMnemonics is each instruction above x86-64 that is not VEX, EVEX
// or XOP encoded, by the mnemonic objdump gives it (a prefix of it, to
// take AT&T's size suffixes), with what it needs. Not tzcnt: gcc emits
// it for x86-64 itself, as rep bsf, which a CPU without BMI runs as BSF.
var newerMnemonics = []struct{ prefix, needs string }{
	{"addsubp", "SSE3"}, {"haddp", "SSE3"}, {"hsubp", "SSE3"}, {"lddqu", "SSE3"},
	{"movddup", "SSE3"}, {"movshdup", "SSE3"}, {"movsldup", "SSE3"}, {"fisttp", "SSE3"},
	{"pabsb", "SSSE3"}, {"pabsw", "SSSE3"}, {"pabsd", "SSSE3"}, {"palignr", "SSSE3"},
	{"phaddw", "SSSE3"}, {"phaddd", "SSSE3"}, {"phaddsw", "SSSE3"}, {"phsubw", "SSSE3"},
	{"phsubd", "SSSE3"}, {"phsubsw", "SSSE3"}, {"pmaddubsw", "SSSE3"}, {"pmulhrsw", "SSSE3"},
	{"pshufb", "SSSE3"}, {"psignb", "SSSE3"}, {"psignw", "SSSE3"}, {"psignd", "SSSE3"},
	{"blendp", "SSE4.1"}, {"blendvp", "SSE4.1"}, {"dppd", "SSE4.1"}, {"dpps", "SSE4.1"},
	{"extractps", "SSE4.1"}, {"insertps", "SSE4.1"}, {"movntdqa", "SSE4.1"}, {"mpsadbw", "SSE4.1"},
	{"packusdw", "SSE4.1"}, {"pblendvb", "SSE4.1"}, {"pblendw", "SSE4.1"}, {"pcmpeqq", "SSE4.1"},
	{"pextrb", "SSE4.1"}, {"pextrd", "SSE4.1"}, {"pextrq", "SSE4.1"}, {"phminposuw", "SSE4.1"},
	{"pinsrb", "SSE4.1"}, {"pinsrd", "SSE4.1"}, {"pinsrq", "SSE4.1"}, {"pmaxsb", "SSE4.1"},
	{"pmaxsd", "SSE4.1"}, {"pmaxud", "SSE4.1"}, {"pmaxuw", "SSE4.1"}, {"pminsb", "SSE4.1"},
	{"pminsd", "SSE4.1"}, {"pminud", "SSE4.1"}, {"pminuw", "SSE4.1"}, {"pmovsx", "SSE4.1"},
	{"pmovzx", "SSE4.1"}, {"pmuldq", "SSE4.1"}, {"pmulld", "SSE4.1"}, {"ptest", "SSE4.1"},
	{"roundp", "SSE4.1"}, {"rounds", "SSE4.1"},
	{"crc32", "SSE4.2"}, {"pcmpestr", "SSE4.2"}, {"pcmpistr", "SSE4.2"}, {"pcmpgtq", "SSE4.2"},
	{"popcnt", "POPCNT"}, {"lzcnt", "LZCNT"}, {"movbe", "MOVBE"}, {"cmpxchg16b", "CMPXCHG16B"},
}

// mnemonicPrefix is every word objdump prints before an instruction's
// own mnemonic.
var mnemonicPrefix = map[string]bool{"rep": true, "repz": true, "repnz": true, "repe": true, "repne": true,
	"lock": true, "notrack": true, "bnd": true, "data16": true, "addr32": true,
	"cs": true, "ds": true, "es": true, "fs": true, "gs": true, "ss": true}

// aboveBaseline is what one instruction needs beyond x86-64, or "": raw
// is its bytes, insn the text objdump gives it.
func aboveBaseline(raw []byte, insn string) string {
	i := 0
	for i < len(raw) && legacyPrefix[raw[i]] {
		i++
	}
	if i < len(raw) && raw[i]&0xf0 == 0x40 { // REX
		i++
	}
	if i < len(raw) {
		switch op := raw[i]; {
		case op == 0xc4 || op == 0xc5: // LES and LDS in 32-bit code; always VEX in 64-bit
			return "VEX-encoded (AVX or later)"
		case op == 0x62: // BOUND in 32-bit code; always EVEX in 64-bit
			return "EVEX-encoded (AVX-512)"
		case op == 0x8f && i+1 < len(raw) && raw[i+1]&0x1f >= 8: // POP r/m has a ModRM reg of 0
			return "XOP-encoded"
		}
	}
	for _, w := range strings.Fields(insn) {
		if mnemonicPrefix[w] {
			continue
		}
		for _, m := range newerMnemonics {
			if strings.HasPrefix(w, m.prefix) {
				return m.needs
			}
		}
		break
	}
	return ""
}

// assemblySources are the extensions of what EDK II assembles rather
// than compiles: .iii is its preprocessed NASM.
var assemblySources = map[string]bool{".nasm": true, ".iii": true, ".asm": true, ".s": true, ".S": true}

// fromAssembly says whether an instruction whose debug line names
// source (or none: "") was written by hand.
func fromAssembly(source string) bool {
	return source == "" || assemblySources[filepath.Ext(source)]
}

// A baselineHit is one instruction the compiler generated above x86-64.
type baselineHit struct{ Addr, Insn, Why, Source string }

var (
	objdumpSymbol = regexp.MustCompile(`^[0-9a-f]+ <.*>:$`)
	objdumpLine   = regexp.MustCompile(`^(.+:[0-9]+)(?: \(discriminator [0-9]+\))?$`)
	objdumpInsn   = regexp.MustCompile(`^ +([0-9a-f]+):\t([0-9a-f ]+)\t(.*)$`)
)

func parseHexBytes(s string) ([]byte, bool) {
	b, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	return b, err == nil
}

// scanBaseline reads `objdump -d -l -w` of one module and returns every
// instruction above x86-64 whose source is C. A symbol starts with no
// source, so assembly without line information is never charged to the
// C function before it.
func scanBaseline(r io.Reader) []baselineHit {
	var hits []baselineHit
	source := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if objdumpSymbol.MatchString(line) {
			source = ""
			continue
		}
		if m := objdumpInsn.FindStringSubmatch(line); m != nil {
			raw, ok := parseHexBytes(m[2])
			if !ok {
				continue
			}
			insn := strings.Join(strings.Fields(m[3]), " ")
			if why := aboveBaseline(raw, insn); why != "" && !fromAssembly(fileOf(source)) {
				hits = append(hits, baselineHit{Addr: m[1], Insn: insn, Why: why, Source: source})
			}
			continue
		}
		if m := objdumpLine.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") {
			source = m[1]
		}
	}
	return hits
}

// fileOf is a debug line's file, less its line number.
func fileOf(source string) string {
	if i := strings.LastIndexByte(source, ':'); i > 0 {
		return source[:i]
	}
	return source
}

// shippedDebugImages is each shipped OpenCore module's DEBUG .dll under
// built, in Artifacts order: the ELF image its .efi was made from, which
// alone carries the debug lines.
func shippedDebugImages(built string) ([]string, error) {
	found := map[string]string{}
	err := filepath.WalkDir(built, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(filepath.Dir(p)) == "DEBUG" && filepath.Ext(p) == ".dll" {
			if _, seen := found[d.Name()]; !seen {
				found[d.Name()] = p
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range Artifacts {
		n := strings.TrimSuffix(a.Built, ".efi") + ".dll"
		p, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("no debug image %s under %s, so %s cannot be checked against the CPU baseline", n, built, a.Built)
		}
		out = append(out, p)
	}
	return out, nil
}

// everyDebugImage is every module's DEBUG .dll under built, sorted.
func everyDebugImage(built string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(built, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(filepath.Dir(p)) == "DEBUG" && filepath.Ext(p) == ".dll" {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no debug images under %s, so the build cannot be checked against the CPU baseline", built)
	}
	sort.Strings(out)
	return out, nil
}

// checkBaseline disassembles each module and refuses the build if the
// compiler generated anything above x86-64 in any of them.
func (b *Builder) checkBaseline(ctx context.Context, modules []string) error {
	objdump := objdumpTool(b.Toolchain.GCC())
	type moduleHits struct {
		module string
		hits   []baselineHit
	}
	var bad []moduleHits
	total := 0
	for _, m := range modules {
		var out bytes.Buffer
		if err := b.Runner.Run(ctx, proc.Cmd{Name: objdump, Args: []string{"-d", "-l", "-w", m}, Stdout: &out, Env: b.Env}); err != nil {
			return fmt.Errorf("cannot disassemble %s to check it against the CPU baseline: %w", m, err)
		}
		if hits := scanBaseline(&out); len(hits) > 0 {
			bad = append(bad, moduleHits{m, hits})
			total += len(hits)
		}
	}
	if total == 0 {
		b.logf("CPU baseline: every instruction the compiler generated in %d modules is x86-64's", len(modules))
		return nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d instructions above x86-64 that the compiler generated, in %d of %d modules, although the build stated %s: a guest CPU without them would fault, so nothing is shipped. Report it rather than working around it (docs/decisions/0042).",
		total, len(bad), len(modules), March)
	for _, mh := range bad {
		fmt.Fprintf(&sb, "\n  %s: %d", filepath.Base(mh.module), len(mh.hits))
		for i, h := range mh.hits {
			if i == 3 {
				fmt.Fprintf(&sb, "\n    ... and %d more", len(mh.hits)-i)
				break
			}
			fmt.Fprintf(&sb, "\n    0x%s %s -- %s, from %s", h.Addr, h.Insn, h.Why, h.Source)
		}
	}
	return fmt.Errorf("%s", sb.String())
}
