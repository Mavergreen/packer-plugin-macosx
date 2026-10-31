package firmware

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CcacheDefault is off: docs/decisions/0004 claims the firmware is
// reproducible, and nobody has yet shown that a ccache build produces the
// same eight checksums as a cold one. docs/decisions/0004 records what
// WAS measured (the PATH-shim seam changes nothing) and what would move
// this.
const CcacheDefault = false

// CcacheVerdict judges whether ccache will be used. Pure.
//
//	USED     asked for, and present.
//	MISSING  asked for, and not installed: warn and build anyway.
//	OFF      not asked for; the detail says whether it is even here.
func CcacheVerdict(wanted bool, path string) (verdict, detail string) {
	switch {
	case wanted && path != "":
		return "USED", path
	case wanted:
		return "MISSING", "ccache was asked for, but ccache is not installed; compiling everything"
	case path != "":
		return "OFF", fmt.Sprintf("ccache is installed at %s but not used: set ccache = true to use it. The default is off because nobody has yet shown that a ccache build produces the same eight checksums as a cold one -- see docs/decisions/0004", path)
	}
	return "OFF", "ccache is not installed; every file is compiled"
}

// ccacheShim is each wrapper writeCcacheShims writes, @CCACHE@ and
// @COMPILER@ to be replaced: a real file (ccache-shim.sh), so the lint
// reads it as the shell script it is.
//
//go:embed ccache-shim.sh
var ccacheShim string

// writeCcacheShims writes gcc and g++ wrappers into dir that run ccache
// on the real compiler, found by absolute path before dir is on PATH --
// a wrapper that said `exec ccache gcc` would find itself. A PATH shim
// rather than GCC_BIN: GCC_X64_PREFIX is glued onto ld, objcopy and ar
// too, and pointing it at two wrappers would hide the rest of binutils.
// A compiler that is not installed gets no wrapper.
func writeCcacheShims(dir, ccache string, lookPath func(string) (string, error)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create the ccache shim directory %s: %w", dir, err)
	}
	for _, tool := range []string{"gcc", "g++"} {
		real, err := lookPath(tool)
		if err != nil || strings.HasPrefix(real, dir+string(filepath.Separator)) {
			continue
		}
		body := strings.NewReplacer("@CCACHE@", ccache, "@COMPILER@", real).Replace(ccacheShim)
		p := filepath.Join(dir, tool)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			return err
		}
		if err := os.Chmod(p, 0o755); err != nil { // WriteFile's mode is masked by umask
			return err
		}
	}
	return nil
}
