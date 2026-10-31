package privops

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// lacking is which of Modules release rel has neither as a module file
// under ModulesDir/rel nor in that tree's modules.builtin, and known is
// whether that can be said at all: without a readable modules.builtin a
// module that is not a file may be built in, and nothing is known.
func (b Backend) lacking(rel string) (lack []string, known bool) {
	builtin, ok := b.builtinModules(rel)
	if !ok {
		return nil, false
	}
	for _, m := range b.Modules {
		if !builtin[m] && b.findModuleIn(rel, m) == "" && !contains(lack, m) {
			lack = append(lack, m)
		}
	}
	return lack, true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// builtinModules is the set of module names ModulesDir/rel/modules.builtin
// lists (kernel/fs/nls/nls_base.ko is nls_base), and false when there is
// no such file to read.
func (b Backend) builtinModules(rel string) (map[string]bool, bool) {
	f, err := os.Open(filepath.Join(b.ModulesDir, rel, "modules.builtin"))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	set := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name := strings.TrimSuffix(filepath.Base(strings.TrimSpace(sc.Text())), ".ko"); name != "" && name != "." {
			set[name] = true
		}
	}
	return set, sc.Err() == nil
}

// chooseRelease is the release the microVM boots and stages modules for.
// It is the running one, unless that one's modules.builtin and module
// tree show it lacks one of Modules -- Ubuntu's Azure kernels before 7.0
// keep hfsplus in linux-modules-extra, which a host need not have, and a
// newer release's packages are not the running one's. Then it is the
// newest other release under ModulesDir that has a readable kernel image
// keyed to it and every one of Modules, by the same test; with none, the
// running one still, which Missing then names as lacking. The microVM is
// a kernel booted under QEMU, not the host's own: any release whose
// modules it can load will do. The log says which, and why.
func (b Backend) chooseRelease(running string) string {
	lack, known := b.lacking(running)
	switch {
	case !known:
		b.logf("booting the running kernel, %s: with no %s, which modules it lacks is not known",
			running, filepath.Join(b.ModulesDir, running, "modules.builtin"))
		return running
	case len(lack) == 0:
		b.logf("booting the running kernel, %s, whose modules have every one the microVM loads", running)
		return running
	}
	best := ""
	ents, _ := os.ReadDir(b.ModulesDir)
	for _, e := range ents {
		rel := e.Name()
		if rel == running || (best != "" && !newerRelease(rel, best)) {
			continue
		}
		// The image first: it is one open, and a tree is thousands of
		// files, while Ubuntu leaves every image but the running one's
		// readable by root alone.
		if !b.hasKeyedKernel(rel) {
			continue
		}
		if l, k := b.lacking(rel); !k || len(l) > 0 {
			continue
		}
		best = rel
	}
	why := strings.Join(lack, ", ") + " (no module, and not in its modules.builtin)"
	if best == "" {
		b.logf("the running kernel, %s, lacks %s, and no other installed release with a readable kernel image has them all", running, why)
		return running
	}
	b.logf("the running kernel, %s, lacks %s: booting %s instead, the newest installed release whose modules have every one the microVM loads",
		running, why, best)
	return best
}

// hasKeyedKernel is whether a kernel image named for rel is readable here.
func (b Backend) hasKeyedKernel(rel string) bool {
	for _, k := range b.keyedCandidates(rel) {
		if readableFile(k) {
			return true
		}
	}
	return false
}

// newerRelease is whether release a is newer than b, comparing runs of
// digits as numbers and the rest as text, as sort -V does: 6.10 is newer
// than 6.8.
func newerRelease(a, b string) bool {
	for a != "" && b != "" {
		ra, rb := releaseRun(a), releaseRun(b)
		a, b = a[len(ra):], b[len(rb):]
		if ra == rb {
			continue
		}
		na, ea := strconv.ParseUint(ra, 10, 64)
		nb, eb := strconv.ParseUint(rb, 10, 64)
		switch {
		case ea != nil || eb != nil:
			return ra > rb
		case na != nb:
			return na > nb
		}
	}
	return a != ""
}

// releaseRun is s's leading run of digits, or of anything else.
func releaseRun(s string) string {
	digit := unicode.IsDigit(rune(s[0]))
	i := 1
	for i < len(s) && unicode.IsDigit(rune(s[i])) == digit {
		i++
	}
	return s[:i]
}
