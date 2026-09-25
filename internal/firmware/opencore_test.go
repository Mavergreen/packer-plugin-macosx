package firmware

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

func (f *fixture) openCore() ([]string, error) {
	return f.b.OpenCore(context.Background(), f.in)
}

func (f *fixture) mustOpenCore() []string {
	f.t.Helper()
	out, err := f.openCore()
	if err != nil {
		f.t.Fatalf("OpenCore: %v\nlog:\n%s", err, f.log.String())
	}
	return out
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// applies is every `git -C dir apply ...` call whose arguments after
// "apply" are exactly rest's prefix.
func (f *fixture) applies(dir string, rest ...string) []proc.Cmd {
	var out []proc.Cmd
	for _, c := range f.calls("git") {
		if len(c.Args) >= 3+len(rest) && c.Args[0] == "-C" && c.Args[1] == dir && c.Args[2] == "apply" &&
			reflect.DeepEqual(c.Args[3:3+len(rest)], rest) {
			out = append(out, c)
		}
	}
	return out
}

func TestOpenCoreBuildsAndShips(t *testing.T) {
	f := newFixture(t)
	got := f.mustOpenCore()
	art := filepath.Join(f.home, "build", "artifacts")
	var want []string
	for _, n := range ShipNames() {
		want = append(want, filepath.Join(art, n))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenCore returned\n%v\nwant\n%v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(art, "BOOTx64.efi")); string(b) != "built Bootstrap.efi" {
		t.Errorf("BOOTx64.efi holds %q", b)
	}
	var sums strings.Builder
	for _, n := range ShipNames() {
		sums.WriteString(sha(t, filepath.Join(art, n)) + "  " + n + "\n")
	}
	have, err := os.ReadFile(filepath.Join(art, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != sums.String() {
		t.Errorf("SHA256SUMS is\n%s\nwant\n%s", have, sums.String())
	}
	if _, err := exec.LookPath("sha256sum"); err == nil {
		cmd := exec.Command("sha256sum", ShipNames()...)
		cmd.Dir = art
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(have) {
			t.Errorf("SHA256SUMS is\n%s\nsha256sum prints\n%s", have, out)
		}
	}
}

func TestOpenCoreBuildEnvironment(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	cs := f.calls("./build_oc.tool")
	if len(cs) != 1 {
		t.Fatalf("%d build_oc.tool calls, want 1", len(cs))
	}
	c := cs[0]
	if want := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7"); c.Dir != want {
		t.Errorf("Dir = %s, want %s", c.Dir, want)
	}
	for _, kv := range []string{"PATH=/usr/bin:/bin", "ARCHS=X64", "TOOLCHAINS=GCC", "TARGETS=RELEASE",
		"OFFLINE_MODE=1", "EFIBUILD_SH=" + f.in["ocbuild-efibuild"],
		"BUILD_ARGUMENTS=-D OCPKG_BUILD_OPTIONS=-std=gnu17\t-Wno-error"} {
		if !hasEnv(c.Env, kv) {
			t.Errorf("Env lacks %q: %q", kv, c.Env)
		}
	}
	log, err := os.ReadFile(filepath.Join(f.home, "build", "opencore-build.log"))
	if err != nil || !strings.Contains(string(log), "compiling OpenCore") {
		t.Errorf("build log = %q, %v", log, err)
	}
}

// envValues is every value env gives key, in order.
func envValues(env []string, key string) []string {
	var vs []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			vs = append(vs, v)
		}
	}
	return vs
}

// Both builds run edksetup.sh, which makes bash's $PWD the WORKSPACE:
// the child's PWD must be the logical path it starts in, as a shell's cd
// gives it, and nothing of an EDK II workspace set up elsewhere may
// reach it -- edksetup.sh returns at once when WORKSPACE is set, and
// sources $EDK_TOOLS_PATH/BuildEnv when that is.
func TestTheBuildsRunInTheirOwnWorkspace(t *testing.T) {
	stale := []string{"PWD=/somewhere/else", "WORKSPACE=/ws", "PACKAGES_PATH=/pp", "EDK_TOOLS_PATH=/etp", "CONF_PATH=/cp"}
	f := newFixture(t)
	f.b.Env = append(f.b.Env, stale...)
	f.mustOpenCore()
	if _, err := f.ovmf(); err != nil {
		t.Fatalf("OVMF: %v\n%s", err, f.log.String())
	}
	for _, c := range []proc.Cmd{f.calls("./build_oc.tool")[0], f.calls("bash")[0]} {
		if got := envValues(c.Env, "PWD"); !reflect.DeepEqual(got, []string{c.Dir}) {
			t.Errorf("%s: PWD = %q, want just %s", c.Name, got, c.Dir)
		}
		for _, k := range []string{"WORKSPACE", "PACKAGES_PATH", "EDK_TOOLS_PATH", "CONF_PATH"} {
			if got := envValues(c.Env, k); len(got) != 0 {
				t.Errorf("%s: %s reaches the build: %q", c.Name, k, got)
			}
		}
		if !hasEnv(c.Env, "PATH=/usr/bin:/bin") {
			t.Errorf("%s: the rest of the environment is lost: %q", c.Name, c.Env)
		}
	}
	if !hasEnv(f.b.Env, "WORKSPACE=/ws") {
		t.Errorf("the Builder's own Env was changed: %q", f.b.Env)
	}
}

func TestOpenCorePatchesBuildOCToolAndChecksIt(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	src := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7")
	want, err := fs.ReadFile(vmguest.Files, "assets/firmware/patches/0001-build_oc-source-pinned-efibuild.patch")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.stdins) == 0 || string(f.stdins[0]) != string(want) {
		t.Fatalf("the first patch applied is not 0001: %d patches", len(f.stdins))
	}
	if n := len(f.applies(src, "-p1", "-")); n != 1 {
		t.Errorf("%d `git -C %s apply -p1 -` calls, want 1", n, src)
	}
	tool, _ := os.ReadFile(filepath.Join(src, "build_oc.tool"))
	if strings.Contains(string(tool), "raw.githubusercontent.com") {
		t.Errorf("build_oc.tool still fetches:\n%s", tool)
	}
	f.mustOpenCore()
	if n := len(f.applies(src, "-p1", "-")); n != 1 {
		t.Errorf("a second build patched build_oc.tool again: %d calls", n)
	}

	g := newFixture(t)
	g.patchesTake = false
	_, err = g.openCore()
	if err == nil || !strings.Contains(err.Error(), "build_oc.tool still fetches shell from the network") {
		t.Fatalf("err = %v", err)
	}
	if n := len(g.calls("./build_oc.tool")); n != 0 {
		t.Errorf("build_oc.tool ran %d times on an unpatched tree", n)
	}
}

func TestOpenCoreAssemblesTheUDKTree(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	src := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7")
	udk := filepath.Join(src, "UDK")
	if b, _ := os.ReadFile(filepath.Join(udk, ".mqg-prepared")); string(b) != AudkCommit+"\n" {
		t.Errorf(".mqg-prepared = %q", b)
	}
	for _, r := range []string{"patches.ready", "submodules.ready", "UDK.ready"} {
		if _, err := os.Stat(filepath.Join(udk, r)); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(udk, "OpenCorePkg")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("UDK/OpenCorePkg is still there: %v", err)
	}
	for _, s := range Submodules {
		b, err := os.ReadFile(filepath.Join(udk, filepath.FromSlash(s.Path), "README"))
		if err != nil || string(b) != s.Source {
			t.Errorf("%s/README = %q, %v; want %q", s.Path, b, err, s.Source)
		}
	}
	as := f.applies(udk, "--ignore-whitespace")
	var got []string
	for _, c := range as {
		got = append(got, c.Args[len(c.Args)-1])
	}
	want := []string{filepath.Join(src, "Patches", "0001-first.patch"), filepath.Join(src, "Patches", "0002-second.patch")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("upstream patches applied: %v, want %v", got, want)
	}
}

func TestOpenCoreKeepsAWarmTree(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	udk := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK")
	sentinel := filepath.Join(udk, "sentinel")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(f.applies(udk, "--ignore-whitespace"))
	f.mustOpenCore()
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the warm tree was rebuilt: %v", err)
	}
	if after := len(f.applies(udk, "--ignore-whitespace")); after != before {
		t.Errorf("upstream patches applied again: %d, then %d", before, after)
	}
}

func TestOpenCoreRebuildsATreeAtAnotherCommit(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	udk := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK")
	if err := os.WriteFile(filepath.Join(udk, ".mqg-prepared"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(udk, "sentinel")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.mustOpenCore()
	if _, err := os.Stat(sentinel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the tree at another commit was kept: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(udk, ".mqg-prepared")); string(b) != AudkCommit+"\n" {
		t.Errorf(".mqg-prepared = %q", b)
	}
}

// A symlink in the audk tree on the way to a submodule's directory must
// not carry the submodule (or its placeholder's removal) outside the tree.
func TestOpenCoreNeverUnpacksASubmoduleThroughASymlink(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "openssl", "keep")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	openssl := Submodules[0].Path // CryptoPkg/Library/OpensslLib/openssl
	parent := "audk-x/" + filepath.ToSlash(filepath.Dir(openssl))
	var es []entry
	for _, e := range audkEntries() {
		if !strings.HasPrefix(e.name, parent+"/") {
			es = append(es, e)
		}
	}
	es = append(es, entry{name: parent, link: outside})
	f.repin("audk-src", rename(t, makeTarGz(t, t.TempDir(), es...), filepath.Join(t.TempDir(), AudkCommit+".tar.gz")))

	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v", err)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine" {
		t.Errorf("a file outside the tree was touched: %q, %v", b, err)
	}
	ents, _ := os.ReadDir(outside)
	if len(ents) != 1 {
		t.Errorf("outside holds %d entries, want 1", len(ents))
	}
	if n := len(f.calls("./build_oc.tool")); n != 0 {
		t.Errorf("build_oc.tool ran %d times", n)
	}
}

func TestOpenCoreRefusesFlagsThatDidNotArrive(t *testing.T) {
	f := newFixture(t)
	f.flags = "-std=gnu17"
	_, err := f.openCore()
	mk := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK", "Build", "OpenCorePkg", "RELEASE_GCC", "X64",
		"OpenCorePkg", "Library", "x", "GNUmakefile")
	if err == nil || !strings.Contains(err.Error(), "-Wno-error") || !strings.Contains(err.Error(), mk) {
		t.Fatalf("err = %v", err)
	}
	efis, _ := filepath.Glob(filepath.Join(f.home, "build", "artifacts", "*.efi"))
	if len(efis) != 0 {
		t.Errorf("artifacts shipped anyway: %v", efis)
	}
}

func TestOpenCoreNamesTheMissingArtifacts(t *testing.T) {
	f := newFixture(t)
	f.built = nil
	for _, a := range Artifacts {
		if a.Built != "Bootstrap.efi" {
			f.built = append(f.built, a.Built)
		}
	}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "Bootstrap.efi") || !strings.Contains(err.Error(), "missing 1 of 5") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCoreNamesTheMissingInput(t *testing.T) {
	f := newFixture(t)
	delete(f.in, "audk-src")
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "audk-src") || !strings.Contains(err.Error(), "fetch it first") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.fake.Calls {
		if c.Name != "gcc" {
			t.Errorf("ran %s before finding its inputs", c)
		}
	}
}

func TestOpenCoreRefusesAnInputThatNoLongerVerifies(t *testing.T) {
	f := newFixture(t)
	p := f.in["audk-src"]
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.Write([]byte{0})
	fh.Close()
	_, err = f.openCore()
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCoreRefusesACompilerBelowTheFloor(t *testing.T) {
	f := newFixture(t)
	f.banner = "gcc (GCC) 12.2.0"
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "below the floor") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.calls("./build_oc.tool")); n != 0 {
		t.Errorf("build_oc.tool ran %d times", n)
	}
	f.b.Toolchain.Override = "gcc 13.3.0"
	f.mustOpenCore()
	if !strings.Contains(f.log.String(), "the compiler option is set") {
		t.Errorf("the log does not say the override is in effect:\n%s", f.log.String())
	}
}

func TestOpenCoreNamesEveryMissingTool(t *testing.T) {
	f := newFixture(t)
	for _, tool := range []string{"zip", "python3", "nasm", "iasl"} {
		delete(f.fake.Paths, tool)
	}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "install them first") {
		t.Fatalf("err = %v", err)
	}
	for _, tool := range []string{"zip", "python3", "nasm", "iasl"} {
		if !strings.Contains(err.Error(), tool) {
			t.Errorf("the error does not name %s: %v", tool, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, "build", "OpenCorePkg-1.0.7")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unpacked without the tools: %v", err)
	}
}

func TestOpenCoreBuildFailureNamesTheLogs(t *testing.T) {
	f := newFixture(t)
	f.buildErr = &proc.ExitError{Cmd: "./build_oc.tool", Code: 2}
	_, err := f.openCore()
	udkLog := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK", "build.log")
	ourLog := filepath.Join(f.home, "build", "opencore-build.log")
	if err == nil || !strings.Contains(err.Error(), udkLog) || !strings.Contains(err.Error(), ourLog) {
		t.Fatalf("err = %v", err)
	}
	var ee *proc.ExitError
	if !errors.As(err, &ee) || ee.Code != 2 {
		t.Errorf("the tool's exit status is lost: %v", err)
	}
	if !strings.Contains(f.log.String(), "line 2") {
		t.Errorf("the log's tail is not shown:\n%s", f.log.String())
	}
}

func TestOpenCoreWithCcache(t *testing.T) {
	f := newFixture(t)
	f.b.Ccache = true
	f.fake.Paths["ccache"] = "/usr/bin/ccache"
	f.mustOpenCore()
	shims := filepath.Join(f.home, "build", "ccache-bin")
	b, err := os.ReadFile(filepath.Join(shims, "gcc"))
	if err != nil || string(b) != "#!/bin/sh\nexec /usr/bin/ccache /usr/bin/gcc \"$@\"\n" {
		t.Errorf("gcc shim = %q, %v", b, err)
	}
	env := f.calls("./build_oc.tool")[0].Env
	base, shim := -1, -1
	for i, kv := range env {
		switch {
		case kv == "PATH=/usr/bin:/bin":
			base = i
		case strings.HasPrefix(kv, "PATH="+shims+string(os.PathListSeparator)):
			shim = i
		}
	}
	if base < 0 || shim < base {
		t.Errorf("the shim PATH (at %d) must come after the base PATH (at %d): %q", shim, base, env)
	}
	if !hasEnv(env, "CCACHE_DIR="+filepath.Join(f.home, "build", "ccache")) {
		t.Errorf("no CCACHE_DIR: %q", env)
	}
	if len(f.calls("ccache")) != 1 {
		t.Errorf("ccache -s was not shown after the build")
	}

	g := newFixture(t)
	g.b.Ccache = true
	g.mustOpenCore()
	if !strings.Contains(g.log.String(), "ccache is not installed") {
		t.Errorf("no warning:\n%s", g.log.String())
	}
	if len(g.calls("./build_oc.tool")) != 1 {
		t.Errorf("did not build")
	}
	if _, err := os.Stat(filepath.Join(g.home, "build", "ccache-bin")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a shim directory without ccache: %v", err)
	}
}

func TestOpenCoreRefusesAMissingHeaderBeforeUnpacking(t *testing.T) {
	if !reflect.DeepEqual(Headers, []string{"uuid/uuid.h"}) {
		t.Fatalf("Headers = %q", Headers)
	}
	f := newFixture(t)
	f.missingHeaders = []string{"uuid/uuid.h"}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "uuid/uuid.h") ||
		!strings.Contains(err.Error(), "the uuid development package: uuid-dev on Debian, util-linux-libs on Arch") {
		t.Fatalf("err = %v", err)
	}
	var probe *proc.Cmd
	for _, c := range f.calls("gcc") {
		if len(c.Args) > 0 && c.Args[0] == "-fsyntax-only" {
			probe = &c
		}
	}
	if probe == nil || !reflect.DeepEqual(probe.Args, []string{"-fsyntax-only", "-x", "c", "-"}) {
		t.Fatalf("the header was not asked of the compiler: %v", probe)
	}
	if _, err := os.Stat(filepath.Join(f.home, "build", "OpenCorePkg-1.0.7")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unpacked without the header: %v", err)
	}

	g := newFixture(t)
	g.mustOpenCore()
	for _, c := range g.calls("gcc") {
		if len(c.Args) > 0 && c.Args[0] == "-fsyntax-only" {
			return
		}
	}
	t.Error("a build that passed never checked the header")
}

// TestHeaderCompilesAsksGCC: HeaderCompiles is the exported probe
// Builder.requireHeaders and a host readiness check share -- one place
// that knows how to ask a compiler whether it can find a header.
func TestHeaderCompilesAsksGCC(t *testing.T) {
	fake := &proc.Fake{Handle: func(c proc.Cmd) error {
		if c.Name != "gcc" || !reflect.DeepEqual(c.Args, []string{"-fsyntax-only", "-x", "c", "-"}) {
			t.Fatalf("unexpected command: %+v", c)
		}
		src, _ := io.ReadAll(c.Stdin)
		if !strings.Contains(string(src), "#include <good/h.h>") && !strings.Contains(string(src), "#include <bad/h.h>") {
			t.Fatalf("stdin = %q, want a one-line #include", src)
		}
		if strings.Contains(string(src), "bad/h.h") {
			return &proc.ExitError{Cmd: c.String(), Code: 1}
		}
		return nil
	}}
	if !HeaderCompiles(context.Background(), fake, "gcc", nil, "good/h.h") {
		t.Fatal("HeaderCompiles(good/h.h) = false, want true")
	}
	if HeaderCompiles(context.Background(), fake, "gcc", nil, "bad/h.h") {
		t.Fatal("HeaderCompiles(bad/h.h) = true, want false")
	}
}

func TestOpenCoreBelowTheFloorUnpacksNothing(t *testing.T) {
	f := newFixture(t)
	f.banner = "gcc (GCC) 12.2.0"
	if _, err := f.openCore(); err == nil {
		t.Fatal("built below the floor")
	}
	if _, err := os.Stat(filepath.Join(f.home, "build", "OpenCorePkg-1.0.7")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenCorePkg was unpacked: %v", err)
	}
}

// checkFlags reads the first GNUmakefile in `find | LC_ALL=C sort` order,
// where "x-y/GNUmakefile" sorts before "x/GNUmakefile" ('-' < '/') though
// a directory walk visits x first.
func TestOpenCoreChecksTheFirstMakefileInSortOrder(t *testing.T) {
	f := newFixture(t)
	f.makefiles = map[string]string{"OpenCorePkg/Library/x-y/GNUmakefile": "-std=gnu17"}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "-Wno-error") || !strings.Contains(err.Error(), filepath.Join("x-y", "GNUmakefile")) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCoreRefusesANilEnvironment(t *testing.T) {
	f := newFixture(t)
	f.b.Env = nil
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "Builder.Env is nil: pass the environment the build tools inherit") {
		t.Fatalf("err = %v", err)
	}
	if len(f.fake.Calls) != 0 {
		t.Errorf("ran %v before refusing", f.fake.Calls)
	}
	if _, err := os.Stat(filepath.Join(f.home, "build")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote under build/ before refusing: %v", err)
	}
}

func TestOpenCoreHeaderProbeGetsTheEnvironment(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	for _, c := range f.calls("gcc") {
		if len(c.Args) > 0 && c.Args[0] == "-fsyntax-only" {
			if !reflect.DeepEqual(c.Env, f.b.Env) {
				t.Errorf("the header probe's Env is %q, want %q", c.Env, f.b.Env)
			}
			return
		}
	}
	t.Error("no header probe")
}

func TestOpenCoreCcachePathHasNoEmptyElement(t *testing.T) {
	f := newFixture(t)
	f.b.Env = []string{"HOME=" + f.home}
	f.b.Ccache = true
	f.fake.Paths["ccache"] = "/usr/bin/ccache"
	f.mustOpenCore()
	env := f.calls("./build_oc.tool")[0].Env
	if want := "PATH=" + filepath.Join(f.home, "build", "ccache-bin"); !hasEnv(env, want) {
		t.Errorf("Env lacks %q: %q", want, env)
	}
}

func TestOpenCoreRefusesASymlinkedLog(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.home, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.home, "build", "opencore-build.log")); err != nil {
		t.Fatal(err)
	}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "mine" {
		t.Errorf("wrote through the symlink: %q", b)
	}
	if n := len(f.calls("./build_oc.tool")); n != 0 {
		t.Errorf("build_oc.tool ran %d times", n)
	}
}

// TestOpenCoreReadsTheMarkerLessTrailingNewlines: a marker naming the
// pinned commit, with any number of trailing newlines, keeps the tree.
func TestOpenCoreReadsTheMarkerLessTrailingNewlines(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	udk := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK")
	sentinel := filepath.Join(udk, "sentinel")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{AudkCommit, AudkCommit + "\n\n"} {
		if err := os.WriteFile(filepath.Join(udk, ".mqg-prepared"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
		f.mustOpenCore()
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("marker %q: the tree was rebuilt", marker)
		}
	}
}

func TestOpenCoreReplacesAnUnmarkedTree(t *testing.T) {
	f := newFixture(t)
	f.mustOpenCore()
	udk := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK")
	if err := os.Remove(filepath.Join(udk, ".mqg-prepared")); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(udk, "sentinel")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.mustOpenCore()
	if _, err := os.Stat(sentinel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a tree without a marker was kept: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(udk, ".mqg-prepared")); string(b) != AudkCommit+"\n" {
		t.Errorf(".mqg-prepared = %q", b)
	}
}

func TestOpenCoreAFailedUpstreamPatchLeavesNoMarker(t *testing.T) {
	f := newFixture(t)
	f.applyErr = &proc.ExitError{Cmd: "git apply", Code: 1}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "0001-first.patch") {
		t.Fatalf("err = %v", err)
	}
	udk := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7", "UDK")
	if _, err := os.Stat(filepath.Join(udk, ".mqg-prepared")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a marker after a failed patch: %v", err)
	}
	before := len(f.applies(udk, "--ignore-whitespace"))
	f.applyErr = nil
	f.mustOpenCore()
	if after := len(f.applies(udk, "--ignore-whitespace")); after != before+2 {
		t.Errorf("the tree was not reassembled: %d upstream patch calls, then %d", before, after)
	}
}

// The workspace path is taken literally when the upstream patches are listed:
// "[" alone is a malformed glob, and "a[1]" one that matches "a1".
func TestOpenCorePatchesAreFoundWhateverTheHome(t *testing.T) {
	for _, name := range []string{"v[", "a[1]"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.home = filepath.Join(f.home, name)
			if err := os.MkdirAll(f.home, 0o755); err != nil {
				t.Fatal(err)
			}
			f.b.Paths.Home = f.home
			f.mustOpenCore()
			src := filepath.Join(f.home, "build", "OpenCorePkg-1.0.7")
			var got []string
			for _, c := range f.applies(filepath.Join(src, "UDK"), "--ignore-whitespace") {
				got = append(got, c.Args[len(c.Args)-1])
			}
			want := []string{filepath.Join(src, "Patches", "0001-first.patch"), filepath.Join(src, "Patches", "0002-second.patch")}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("upstream patches applied: %v, want %v", got, want)
			}
		})
	}
}
