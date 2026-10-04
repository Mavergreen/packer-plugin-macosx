package firmware

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
)

// DebugPathLimit is the longest debug-symbol path audk's ImageTool will
// write into an image (BaseTools/ImageTool/Image.c: SymbolsPathLen >
// MAX_UINT8 is "ERROR: Debug symbol path exceeds maximum allowed range
// of 255 bytes!"). The path is absolute: build_rule passes
// $(DEBUG_DIR)/<Module>.dll, and DEBUG_DIR is under WORKSPACE, the UDK
// tree. So how deep the build may go is decided by where the firmware
// workspace (Builder.Paths.Home) is.
const DebugPathLimit = 255

// The deepest debug-symbol path each build writes, in bytes below the
// UDK tree, MEASURED 2026-09-25 (find Build/<platform> -name '*.dll' in
// a complete build of this pin: OpenCorePkg 1.0.7 on audk 0672a009):
//
//	opencore  Build/OpenCorePkg/RELEASE_GCC/X64/OpenCorePkg/Platform/FirmwareSettingsEntry/FirmwareSettingsEntry/DEBUG/FirmwareSettingsEntry.dll
//	ovmf      Build/OvmfX64/RELEASE_GCC/X64/MdeModulePkg/Universal/ReportStatusCodeRouter/RuntimeDxe/ReportStatusCodeRouterRuntimeDxe/DEBUG/ReportStatusCodeRouterRuntimeDxe.dll
//
// A pin bump must re-measure these.
const (
	OpenCoreDeepestDebugPath = 130
	OVMFDeepestDebugPath     = 162
)

// deepestDebugPath is target's deepest debug-symbol path below UDK, or
// false for a target EDK II does not build.
func deepestDebugPath(target string) (int, bool) {
	switch target {
	case "opencore":
		return OpenCoreDeepestDebugPath, true
	case "ovmf":
		return OVMFDeepestDebugPath, true
	}
	return 0, false
}

// udkBelowHome is how many bytes the UDK tree's path adds to the workspace:
// "/build/OpenCorePkg-<ver>/UDK".
func udkBelowHome() int {
	p := config.Paths{Home: "/h"}
	return len((&Builder{Paths: p}).udk()) - len(p.Home)
}

// MaxHomeLength is the longest workspace path, in bytes, with which target
// ("opencore" or "ovmf") builds: its deepest debug-symbol path must fit
// in DebugPathLimit. 0 for a target EDK II does not build.
func MaxHomeLength(target string) int {
	deepest, ok := deepestDebugPath(target)
	if !ok {
		return 0
	}
	return DebugPathLimit - deepest - 1 - udkBelowHome()
}

// CheckBuildPath refuses, before anything is fetched or unpacked, a
// workspace path too long for target's build: EDK II fails on it only after
// minutes of compiling. The path counted is the longer of the UDK
// tree's logical path (what edksetup.sh's WORKSPACE=$PWD sees, since the
// builds run with PWD set to it) and its physical one (every symlink
// resolved), so a symlink in either direction cannot hide a long path.
// A target EDK II does not build (efi) is never refused.
func CheckBuildPath(p config.Paths, target string) error {
	deepest, ok := deepestDebugPath(target)
	if !ok {
		return nil
	}
	udk := (&Builder{Paths: p}).udk()
	seen := udk
	phys, err := physicalPath(udk)
	if err != nil {
		return err
	}
	if len(phys) > len(seen) {
		seen = phys
	}
	if len(seen)+1+deepest <= DebugPathLimit {
		return nil
	}
	home := seen[:len(seen)-udkBelowHome()]
	return fmt.Errorf("the firmware workspace's path is too long for the %s build: EDK II refuses a debug-symbol path over %d bytes, and the deepest it writes is %d bytes below %s. "+
		"The workspace here is %s, %d bytes; for %s it can be at most %d. Use a shorter workspace path",
		target, DebugPathLimit, deepest, seen, home, len(home), target, MaxHomeLength(target))
}

// physicalPath is path with every symlink resolved: EvalSymlinks of the
// longest part of it that exists, and the rest as it is.
func physicalPath(path string) (string, error) {
	path = filepath.Clean(path)
	rest := []string{}
	for dir := path; ; {
		if _, err := os.Stat(dir); err == nil {
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path, nil
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
		dir = parent
	}
}

// debugPathError is runLogged's backstop for a path that grew past what
// DebugPathLimit's measurements allow for: EDK II's own words in the log
// become an error that says what they mean.
func debugPathError(log []byte) string {
	if !strings.Contains(string(log), "Debug symbol path exceeds") {
		return ""
	}
	return fmt.Sprintf("EDK II refused a debug-symbol path over %d bytes: the build path is too long -- use a shorter workspace path", DebugPathLimit)
}
