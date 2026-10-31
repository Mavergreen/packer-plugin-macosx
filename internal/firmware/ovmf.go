package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// ovmfBuild is EDK II's own way to build: edksetup.sh (not written for
// set -u) puts `build` on PATH with WORKSPACE and CONF_PATH set, then
// build runs. bash -c is its subshell.
var ovmfBuild = fmt.Sprintf("set +u; . ./edksetup.sh >/dev/null || exit 1; exec build -a %s -b %s -t %s -p %s",
	Arch, EDKTarget, EDKToolchain, OVMFDsc)

// ovmfPatches are the patches the OVMF build applies to the EDK II tree,
// in order. file is where the patch's marker shows it took; doing is what
// the log says the patch is for; still is what file still does if it did
// not take.
var ovmfPatches = []struct{ patch, file, marker, doing, still string }{
	{"0002-ovmf-pin-the-c-dialect.patch", OVMFDsc, "std=gnu17", "state its C dialect", "does not state a C dialect"},
	{"0003-firmware-drop-werror.patch", OVMFDsc, "Wno-error", "stop treating upstream's warnings as errors", "promotes upstream's warnings to errors"},
	// A warm restart hung in PEI on the last OS's bytes read as a vCPU
	// count (docs/decisions/0018); upstream's fix, which audk lacks.
	{"0004-ovmf-reserve-the-igvm-parameter-area.patch", igvmC, "PlatformIgvmParamReserve",
		"reserve the IGVM parameter area, so a warm restart does not read the last OS's bytes as a vCPU count",
		"leaves the IGVM parameter area to the OS"},
	// Ours: QEMU here never loads an IGVM file, so OVMF never takes the
	// area, even where the OS cannot leave it alone (10.6's kernel).
	// It holds only while every reader of PcdOvmfIgvmParamBase in Igvm.c
	// tests mIgvmParamsInvalid first: checkIGVMReaders checks that in the
	// tree it patched, before every build (docs/decisions/0018).
	{"0005-ovmf-never-read-igvm-parameters.patch", igvmC, "packer-plugin-macosx: assets/firmware/patches/0005",
		"stop taking IGVM parameters from guest RAM, which QEMU never fills here",
		"takes IGVM parameters from whatever is in guest RAM"},
	// Ours: QEMU's pflash takes two traps and a write of its backing file
	// per programmed byte, so a variable reclaim, nearly all 0xFF over
	// erased blocks, held a boot 41-57 s (docs/decisions/0032).
	{"0006-ovmf-program-only-the-flash-bytes-that-change.patch", qemuFlashC, "packer-plugin-macosx: assets/firmware/patches/0006",
		"program only the flash bytes that change, so a variable reclaim takes seconds, not a minute",
		"programs every byte of a flash write, the ones that already hold their value too"},
}

// qemuFlashC is OVMF's driver for QEMU's flash device, whose
// QemuFlashWrite 0006 patches.
const qemuFlashC = "OvmfPkg/QemuFlashFvbServicesRuntimeDxe/QemuFlash.c"

// patchStampFile, in the EDK II tree, records which OVMF patches the tree
// may hold: one "<patch>\t<sha256>" line each, in order, each written
// before that patch is applied. assembleUDK writes it empty. A tree whose stamp is not a prefix of this binary's
// set -- a patch revised or dropped since, or a tree from before the
// stamp -- is reassembled rather than trusted, because a patch whose
// marker is already there is skipped, and the cache key would otherwise
// name a patch set the tree does not hold.
const patchStampFile = ".guest-macosx-ovmf-patches"

func (b *Builder) patchStampPath() string { return filepath.Join(b.udk(), patchStampFile) }

// ovmfPatchStamp is the stamp of a tree patched with every OVMF patch this
// binary carries, one line per patch.
func ovmfPatchStamp() ([]string, error) {
	var out []string
	for _, p := range ovmfPatches {
		data, err := fs.ReadFile(macosx.Files, "assets/firmware/patches/"+p.patch)
		if err != nil {
			return nil, err
		}
		s := sha256.Sum256(data)
		out = append(out, p.patch+"\t"+hex.EncodeToString(s[:])+"\n")
	}
	return out, nil
}

// patchStampFits says whether the tree's stamp is the stamp of some first
// k of this binary's patches, k from 0 (a fresh tree) to all of them. A
// stamp that cannot be read does not fit.
func (b *Builder) patchStampFits(stamp []string) bool {
	return b.patchStampPrefix(stamp) >= 0
}

// patchStampPrefix returns the number of recorded patches, or -1 if the
// stamp is unreadable or not an exact prefix of this binary's patch set.
func (b *Builder) patchStampPrefix(stamp []string) int {
	data, err := os.ReadFile(b.patchStampPath())
	if err != nil {
		return -1
	}
	for k := 0; k <= len(stamp); k++ {
		if string(data) == strings.Join(stamp[:k], "") {
			return k
		}
	}
	return -1
}

// OVMF builds the guest's UEFI firmware from the EDK II tree OpenCore
// assembled (one pinned tree, not a second copy that could drift), and
// ships OVMF_CODE.fd, OVMF_VARS.fd and OVMF.fd to build/firmware with a
// SHA256SUMS. Debian's stock OVMF does not work with OpenCore on these
// hosts (MEASURED; docs/configuration-register.md); the one built from
// acidanthera's audk does.
func (b *Builder) OVMF(ctx context.Context) ([]string, error) {
	// A home too long for EDK II otherwise fails minutes into the build.
	if err := CheckBuildPath(b.Paths, "ovmf"); err != nil {
		return nil, err
	}
	if err := b.requireEnv(); err != nil {
		return nil, err
	}
	// The compiler first, and for this build above all: OvmfPkg compiled
	// clean under C23 and emitted different firmware.
	if err := b.Toolchain.Check(ctx, b.logf); err != nil {
		return nil, err
	}
	have, err := os.ReadFile(filepath.Join(b.udk(), ".guest-macosx-prepared"))
	switch {
	case err != nil:
		return nil, fmt.Errorf("no assembled EDK II tree at %s -- assemble the OpenCore tree first", b.udk())
	case strings.TrimRight(string(have), "\n") != AudkCommit: // less its trailing newlines
		return nil, fmt.Errorf("the EDK II tree at %s holds audk %s, not the pinned %s -- reassemble the OpenCore tree",
			b.udk(), strings.TrimSpace(string(have)), AudkCommit)
	}
	if fi, err := os.Stat(filepath.Join(b.udk(), "BaseTools", "Source", "C", "bin", "GenFv")); err != nil || fi.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("BaseTools are not built in %s -- assemble the OpenCore tree first", b.udk())
	}
	if err := b.requireTools(ovmfTools(b.Toolchain.GCC())...); err != nil {
		return nil, err
	}
	// A tree another patch set touched is never patched on top of: its
	// markers would make this set's patches look applied when they are
	// not. OpenCore's assembly replaces such a tree before this runs.
	stamp, err := ovmfPatchStamp()
	if err != nil {
		return nil, err
	}
	recorded := b.patchStampPrefix(stamp)
	if recorded < 0 {
		return nil, fmt.Errorf("the EDK II tree at %s was patched with another set of OVMF patches than this plugin's (%s) -- reassemble the OpenCore tree",
			b.udk(), patchStampFile)
	}
	for i, p := range ovmfPatches {
		// Stamped before it is applied, so the stamp always names every
		// patch the tree may hold: a build that dies between the two can
		// leave a patch stamped and not applied (the next build applies
		// it, or reassembles if it was revised), never applied and not
		// stamped. Never shorten an existing valid stamp on a revisit:
		// later patches may already be in the tree if this build stops.
		if i >= recorded {
			if err := writeFileAtomic(b.patchStampPath(), []byte(strings.Join(stamp[:i+1], "")), 0o644); err != nil {
				return nil, err
			}
		}
		if err := b.patchOnce(ctx, p.file, p.patch, p.marker, p.doing, p.still); err != nil {
			return nil, err
		}
	}
	if err := checkIGVMReaders(b.udk()); err != nil {
		return nil, err
	}
	env, ccache, err := b.buildEnv(ctx)
	if err != nil {
		return nil, err
	}

	logPath := filepath.Join(b.udk(), "ovmf-build.log")
	b.logf("building %s from audk %s in %s", OVMFDsc, AudkCommit, b.udk())
	b.logf("arch %s, toolchain %s, target %s -- its output goes to %s", Arch, EDKToolchain, EDKTarget, logPath)
	b.logf("compiler: %s", b.Toolchain.CompilerLine(ctx))
	start := time.Now()
	if err := b.runLogged(ctx, proc.Cmd{Name: "bash", Args: []string{"-c", ovmfBuild}, Dir: b.udk(), Env: edkEnv(env, b.udk())}, logPath); err != nil {
		return nil, fmt.Errorf("the OVMF build failed -- see %s, and report the error rather than working around it: %w", logPath, err)
	}
	b.logf("build finished in %s", time.Since(start).Round(time.Second))
	if ccache {
		b.ccacheStats(ctx)
	}

	fv := filepath.Join(b.udk(), "Build", "OvmfX64", EDKTarget+"_"+EDKToolchain, "FV")
	var missing []string
	for _, n := range OVMFFiles {
		if _, err := os.Stat(filepath.Join(fv, n)); err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing %d of %d firmware images: %s (looked in %s)", len(missing), len(OVMFFiles), strings.Join(missing, " "), fv)
	}
	var out []string
	for _, n := range OVMFFiles {
		dst := filepath.Join(b.Paths.Firmware(), n)
		if err := copyAtomic(filepath.Join(fv, n), dst); err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	if err := WriteSums(b.Paths.Firmware(), OVMFFiles); err != nil {
		return nil, err
	}
	// The sizes are load-bearing for the pflash pair: QEMU sizes each
	// flash device from its file.
	b.logf("built %d firmware images into %s", len(out), b.Paths.Firmware())
	for _, p := range out {
		if fi, err := os.Stat(p); err == nil {
			b.logf("  %s  %d bytes", filepath.Base(p), fi.Size())
		}
	}
	return out, nil
}

// patchOnce applies one of our OVMF patches unless file (relative to the
// tree) already has its marker, then checks that it does. The tree is
// re-unpacked whenever the audk pin moves, which is why this runs on every
// build; an audk that has merged a fix upstream already has its marker.
func (b *Builder) patchOnce(ctx context.Context, file, patch, marker, doing, still string) error {
	path := filepath.Join(b.udk(), filepath.FromSlash(file))
	has := func() (bool, error) {
		data, err := os.ReadFile(path)
		return strings.Contains(string(data), marker), err
	}
	ok, err := has()
	if err != nil {
		return err
	}
	if !ok {
		b.logf("patching %s to %s", file, doing)
		if err := b.applyPatch(ctx, b.udk(), patch); err != nil {
			return fmt.Errorf("cannot patch %s -- did audk %s change? %w", file, AudkCommit, err)
		}
		if ok, err = has(); err != nil {
			return err
		}
	}
	if !ok {
		return fmt.Errorf("%s still %s", file, still)
	}
	return nil
}
