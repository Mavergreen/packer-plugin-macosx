package firmware

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// ovmfBuild is EDK II's own way to build: edksetup.sh (not written for
// set -u) puts `build` on PATH with WORKSPACE and CONF_PATH set, then
// build runs. bash -c is its subshell.
var ovmfBuild = fmt.Sprintf("set +u; . ./edksetup.sh >/dev/null || exit 1; exec build -a %s -b %s -t %s -p %s",
	Arch, EDKTarget, EDKToolchain, OVMFDsc)

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
	have, err := os.ReadFile(filepath.Join(b.udk(), ".mqg-prepared"))
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
	dsc := filepath.Join(b.udk(), filepath.FromSlash(OVMFDsc))
	// doing is what the log says the patch is for; still is what the dsc
	// still does if it did not take.
	for _, p := range []struct{ patch, marker, doing, still string }{
		{"0002-ovmf-pin-the-c-dialect.patch", "std=gnu17", "state its C dialect", "does not state a C dialect"},
		{"0003-firmware-drop-werror.patch", "Wno-error", "stop treating upstream's warnings as errors", "promotes upstream's warnings to errors"},
	} {
		if err := b.patchOnce(ctx, dsc, p.patch, p.marker, p.doing, p.still); err != nil {
			return nil, err
		}
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

// patchOnce applies one of our dsc patches unless the dsc already has its
// marker, then checks that it does. The tree is re-unpacked whenever the
// audk pin moves, which is why this runs on every build.
func (b *Builder) patchOnce(ctx context.Context, dsc, patch, marker, doing, still string) error {
	has := func() (bool, error) {
		data, err := os.ReadFile(dsc)
		return strings.Contains(string(data), marker), err
	}
	ok, err := has()
	if err != nil {
		return err
	}
	if !ok {
		b.logf("patching %s to %s", OVMFDsc, doing)
		if err := b.applyPatch(ctx, b.udk(), patch); err != nil {
			return fmt.Errorf("cannot patch %s -- did audk %s change? %w", OVMFDsc, AudkCommit, err)
		}
		if ok, err = has(); err != nil {
			return err
		}
	}
	if !ok {
		return fmt.Errorf("%s still %s", OVMFDsc, still)
	}
	return nil
}
