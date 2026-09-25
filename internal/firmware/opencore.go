package firmware

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

const buildOCPatch = "0001-build_oc-source-pinned-efibuild.patch"

// OpenCore builds OpenCore from the pinned sources in in and ships its
// five artifacts to build/artifacts with a SHA256SUMS. It reaches no
// network. Steps a warm tree has
// done (unpacking, patching, assembling the EDK II tree) are skipped;
// build_oc.tool itself is incremental.
func (b *Builder) OpenCore(ctx context.Context, in Inputs) ([]string, error) {
	// A home too long for EDK II otherwise fails minutes into the build.
	if err := CheckBuildPath(b.Paths, "opencore"); err != nil {
		return nil, err
	}
	if err := b.requireEnv(); err != nil {
		return nil, err
	}
	// The compiler first: a host below the floor hears that before
	// anything expensive, not after a fetch and three minutes of gcc.
	if err := b.Toolchain.Check(ctx, b.logf); err != nil {
		return nil, err
	}
	if err := CheckPins(b.Registry); err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, n := range OpenCoreSources() {
		p, err := b.input(in, n)
		if err != nil {
			return nil, err
		}
		files[n] = p
	}
	if err := b.requireTools(openCoreTools(b.Toolchain.GCC())...); err != nil {
		return nil, err
	}
	if err := b.requireHeaders(ctx); err != nil {
		return nil, err
	}
	if err := b.unpackOpenCorePkg(ctx, files["opencorepkg-src"]); err != nil {
		return nil, err
	}
	if err := b.patchBuildOCTool(ctx); err != nil {
		return nil, err
	}
	if err := b.assembleUDK(ctx, files); err != nil {
		return nil, err
	}
	env, ccache, err := b.buildEnv(ctx)
	if err != nil {
		return nil, err
	}

	b.logf("building OpenCore %s in %s", OCVersion, b.src())
	b.logf("arch %s, toolchain %s, target %s -- this takes a while; its output goes to %s", Arch, EDKToolchain, EDKTarget, b.buildLog())
	b.logf("compiler: %s", b.Toolchain.CompilerLine(ctx))
	start := time.Now()
	cmd := proc.Cmd{Name: "./build_oc.tool", Dir: b.src(), Env: append(edkEnv(env, b.src()),
		"ARCHS="+Arch, "TOOLCHAINS="+EDKToolchain, "TARGETS="+EDKTarget, "OFFLINE_MODE=1",
		"EFIBUILD_SH="+files["ocbuild-efibuild"],
		"BUILD_ARGUMENTS=-D OCPKG_BUILD_OPTIONS="+BuildOptions())}
	if err := b.runLogged(ctx, cmd, b.buildLog()); err != nil {
		return nil, fmt.Errorf("build_oc.tool failed -- see %s and %s, and report the error rather than working around it: %w",
			filepath.Join(b.udk(), "build.log"), b.buildLog(), err)
	}
	b.logf("build_oc.tool finished in %s", time.Since(start).Round(time.Second))
	if ccache {
		b.ccacheStats(ctx)
	}

	built := filepath.Join(b.udk(), "Build", "OpenCorePkg", EDKTarget+"_"+EDKToolchain, Arch)
	if fi, err := os.Stat(built); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("build reported success but %s does not exist", built)
	}
	if err := checkFlags(built); err != nil {
		return nil, err
	}
	return b.shipArtifacts(built)
}

// unpackOpenCorePkg unpacks the OpenCorePkg release once.
func (b *Builder) unpackOpenCorePkg(ctx context.Context, tarball string) error {
	if _, err := os.Stat(b.src()); err != nil {
		b.logf("unpacking OpenCorePkg %s to %s", OCVersion, b.src())
		if err := untarGz(ctx, tarball, b.src(), 1); err != nil {
			return err
		}
	} else {
		b.logf("OpenCorePkg %s already unpacked at %s", OCVersion, b.src())
	}
	if _, err := os.Stat(filepath.Join(b.src(), "build_oc.tool")); err != nil {
		return fmt.Errorf("%s does not look like OpenCorePkg: no build_oc.tool", b.src())
	}
	return nil
}

// patchBuildOCTool replaces build_oc.tool's curl-and-eval of efibuild.sh
// with a read of the pinned copy (assets/firmware/patches/0001), and checks it took,
// so a patch that silently did nothing cannot pass for success.
func (b *Builder) patchBuildOCTool(ctx context.Context) error {
	tool := filepath.Join(b.src(), "build_oc.tool")
	fetches := func() (bool, error) {
		data, err := os.ReadFile(tool)
		return strings.Contains(string(data), "raw.githubusercontent.com"), err
	}
	yes, err := fetches()
	if err != nil {
		return err
	}
	if yes {
		b.logf("patching build_oc.tool to read the pinned efibuild.sh")
		if err := b.applyPatch(ctx, b.src(), buildOCPatch); err != nil {
			return fmt.Errorf("cannot patch build_oc.tool -- did OpenCorePkg %s change? %w", OCVersion, err)
		}
		if yes, err = fetches(); err != nil {
			return err
		}
	}
	if yes {
		return fmt.Errorf("build_oc.tool still fetches shell from the network")
	}
	return nil
}

// assembleUDK builds the EDK II tree efibuild.sh would otherwise clone
// at master: audk at its pinned commit, its submodules from their own
// tarballs, OpenCorePkg's own patches. The .mqg-prepared marker says
// which commit the tree holds; a tree at another (or no) commit is
// removed and rebuilt. UDK.ready must
// exist before build_oc.tool runs, or efibuild.sh deletes the tree.
func (b *Builder) assembleUDK(ctx context.Context, files map[string]string) error {
	marker := filepath.Join(b.udk(), ".mqg-prepared")
	// Compared less its trailing newlines.
	if have, err := os.ReadFile(marker); err == nil && strings.TrimRight(string(have), "\n") == AudkCommit {
		b.logf("EDK II tree already assembled at audk %s", AudkCommit)
		return nil
	}
	b.logf("assembling the EDK II tree: audk %s", AudkCommit)
	if err := os.RemoveAll(b.udk()); err != nil {
		return err
	}
	if err := untarGz(ctx, files["audk-src"], b.udk(), 1); err != nil {
		return err
	}
	// audk has a submodule of its own called OpenCorePkg, which the
	// archive leaves as an empty directory; efibuild.sh wants a symlink
	// there, and its symlink() does nothing if a directory is in the way.
	if err := os.RemoveAll(filepath.Join(b.udk(), "OpenCorePkg")); err != nil {
		return err
	}
	for _, s := range Submodules {
		b.logf("  + %s @ %s", s.Path, s.Commit)
		dest := filepath.Join(b.udk(), filepath.FromSlash(s.Path))
		// The audk archive made the directories on the way to dest, and
		// it may make any of them a symlink: neither removing the
		// placeholder nor untarGz (whose MkdirAll follows symlinks) may
		// pass through one, so each is checked to be a real directory.
		if err := mkdirsNoFollow(b.udk(), filepath.Dir(dest)); err != nil {
			return fmt.Errorf("cannot place submodule %s: %w", s.Path, err)
		}
		if err := os.RemoveAll(dest); err != nil { // the archive's empty placeholder
			return err
		}
		if err := untarGz(ctx, files[s.Source], dest, 1); err != nil {
			return err
		}
	}
	// Read, not globbed, so that a workspace path holding a glob character
	// -- "[" is malformed, "a[1]" matches "a1" -- is taken literally.
	var patches []string
	ents, err := os.ReadDir(filepath.Join(b.src(), "Patches"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range ents {
		patches = append(patches, filepath.Join(b.src(), "Patches", e.Name()))
	}
	sort.Strings(patches)
	for _, p := range patches {
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		b.logf("  + patch %s", filepath.Base(p))
		if err := b.Runner.Run(ctx, proc.Cmd{Name: "git", Args: []string{"-C", b.udk(), "apply", "--ignore-whitespace", p},
			Stderr: logWriter{b}}); err != nil {
			return fmt.Errorf("cannot apply %s to the EDK II tree: %w", p, err)
		}
	}
	for _, r := range []string{"patches.ready", "submodules.ready", "UDK.ready"} {
		if err := writeFileAtomic(filepath.Join(b.udk(), r), nil, 0o644); err != nil {
			return err
		}
	}
	return writeFileAtomic(marker, []byte(AudkCommit+"\n"), 0o644)
}

// checkFlags asserts both halves of BuildOptions reached the compiler, in
// the first generated GNUmakefile: the check a tab quietly becoming a
// space, or upstream's hook quietly going away on a pin bump, cannot get
// past. The makefiles are rewritten on every build, warm or cold. The one
// checked is the first of `find | LC_ALL=C sort`,
// which sorts whole paths byte by byte -- not a directory walk's order,
// in which x/ comes before x-y/ although "x-y/" sorts before "x/".
func checkFlags(built string) error {
	var mks []string
	err := filepath.WalkDir(built, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "GNUmakefile" {
			mks = append(mks, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(mks) == 0 {
		return fmt.Errorf("no GNUmakefile under %s -- cannot check the build flags", built)
	}
	sort.Strings(mks)
	mk := mks[0]
	data, err := os.ReadFile(mk)
	if err != nil {
		return err
	}
	for _, flag := range []string{"-std=" + CStd, NoWerror} {
		if !strings.Contains(string(data), flag) {
			return fmt.Errorf("%s never reached the compiler (checked %s) -- did OpenCorePkg %s drop $(OCPKG_BUILD_OPTIONS)?", flag, mk, OCVersion)
		}
	}
	return nil
}

// shipArtifacts copies what we ship out of the build tree, under the
// names we ship them as, and writes their SHA256SUMS.
func (b *Builder) shipArtifacts(built string) ([]string, error) {
	var missing []string
	for _, a := range Artifacts {
		if _, err := os.Stat(filepath.Join(built, a.Built)); err != nil {
			missing = append(missing, a.Built)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing %d of %d artifacts: %s (looked in %s)", len(missing), len(Artifacts), strings.Join(missing, " "), built)
	}
	var out []string
	for _, a := range Artifacts {
		dst := filepath.Join(b.artifactsDir(), a.Ship)
		if err := copyAtomic(filepath.Join(built, a.Built), dst); err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	if err := WriteSums(b.artifactsDir(), ShipNames()); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(b.ocvalidate()); err == nil && fi.Mode()&0o111 != 0 {
		b.logf("ocvalidate: %s", b.ocvalidate())
	} else {
		b.logf("warning: ocvalidate not built at %s -- a derived SMBIOS config will ship unvalidated", b.ocvalidate())
	}
	b.logf("built %d artifacts into %s", len(out), b.artifactsDir())
	return out, nil
}

// headerPackages names, for each of Headers, the package that provides
// it.
var headerPackages = map[string]string{
	"uuid/uuid.h": "the uuid development package: uuid-dev on Debian, util-linux-libs on Arch",
}

// HeaderPackage is what to install for header, one of Headers: the words
// both the build and a host readiness check use when it is missing. ""
// for a header this package does not name.
func HeaderPackage(header string) string { return headerPackages[header] }

// HeaderCompiles asks gcc whether it can find and compile against
// header: "gcc -fsyntax-only -x c -", fed a one-line source that only
// #includes it, run through r with env (nil means the child gets r's own
// default -- see proc.Cmd.Env). Only the compiler itself knows its own
// search path, so this is the one place that asks it, shared by
// Builder.requireHeaders (which has already confirmed gcc is on PATH,
// via requireTools) and a host readiness check (which has not, and
// checks that itself -- "cannot tell" is not "missing").
func HeaderCompiles(ctx context.Context, r proc.Runner, gcc string, env []string, header string) bool {
	src := "#include <" + header + ">\nint main(void){return 0;}\n"
	err := r.Run(ctx, proc.Cmd{Name: gcc, Args: []string{"-fsyntax-only", "-x", "c", "-"},
		Stdin: strings.NewReader(src), Stdout: io.Discard, Stderr: io.Discard, Env: env})
	return err == nil
}

// requireHeaders asks the compiler whether each of Headers compiles, and
// names every one that does not
// with its package, before anything is unpacked: a missing header
// otherwise surfaces minutes into BaseTools as a fatal error.
func (b *Builder) requireHeaders(ctx context.Context) error {
	var missing []string
	for _, h := range Headers {
		if HeaderCompiles(ctx, b.Runner, b.Toolchain.GCC(), b.Env, h) {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m := h
		if p := HeaderPackage(h); p != "" {
			m += " (" + p + ")"
		}
		missing = append(missing, m)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s cannot include %s -- install it first", b.Toolchain.GCC(), strings.Join(missing, ", "))
	}
	return nil
}
