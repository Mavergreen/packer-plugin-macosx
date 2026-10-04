package firmware

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// buildOCToolFetch is the line in OpenCorePkg 1.0.7's build_oc.tool that
// assets/firmware/patches/0001 replaces: a curl of efibuild.sh at master.
const buildOCToolFetch = "src=$(curl -LfsS https://raw.githubusercontent.com/acidanthera/ocbuild/master/efibuild.sh) && eval \"$src\" || exit 1\n"

// A fixture is a firmware workspace whose every firmware input is a small
// generated file, pinned in a test registry, and a proc.Fake that plays
// gcc, git, build_oc.tool and EDK II's build. Shared by the OpenCore,
// OVMF, kext and EFI image tests.
type fixture struct {
	t    *testing.T
	home string
	b    *Builder
	in   Inputs
	fake *proc.Fake
	log  bytes.Buffer
	rows []string // the test registry, name<TAB>url<TAB>sha256, in pin order

	// What the fake tools do; a test changes these before building.
	banner      string   // gcc --version's first line
	built       []string // build_oc.tool's outputs; default: every Artifacts Built name
	flags       string   // the fake GNUmakefile's CC_FLAGS
	patchesTake bool     // whether a fake `git apply -p1 -` changes the file
	buildErr    error    // build_oc.tool's (and EDK II build's) result
	buildOut    string   // what either build prints, if not its default line or two
	fv          []string // OVMF's outputs; default: OVMFFiles
	stdins      [][]byte // every patch git apply read from stdin
	ocvalidate  error    // what an ocvalidate (built or on PATH) returns

	missingHeaders []string          // headers the fake compiler cannot find
	applyErr       error             // `git apply --ignore-whitespace`'s result
	makefiles      map[string]string // more GNUmakefiles build_oc.tool writes: path under the build dir -> CC_FLAGS
}

func sha(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// audkEntries is the fixture's audk archive: a few files, the OpenCorePkg
// placeholder the real archive has, and an empty directory at every
// submodule path, as a GitHub archive leaves a gitlink.
func audkEntries() []entry {
	es := []entry{{name: "audk-x/"}, {name: "audk-x/MdePkg/MdePkg.dec", body: "dec"},
		{name: "audk-x/OpenCorePkg/"}, {name: "audk-x/OvmfPkg/OvmfPkgX64.dsc", body: "[BuildOptions]\n"}}
	for _, s := range Submodules {
		es = append(es, entry{name: "audk-x/" + s.Path + "/"})
	}
	return es
}

// shortHome is a new, empty directory for a workspace that
// CheckBuildPath allows on any host: under /tmp, not t.TempDir(), which
// on macOS sits under a $TMPDIR of about 50 bytes, and names the test
// too -- the OVMF build allows a home of 64 bytes at most. It is removed
// when the test ends.
func shortHome(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		base = ""
	}
	d, err := os.MkdirTemp(base, "vf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(d); err != nil {
			t.Errorf("removing %s: %v", d, err)
		}
	})
	return d
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, home: shortHome(t), in: Inputs{}, banner: "gcc (GCC) 13.3.0",
		flags: "-std=gnu17 -Wno-error", patchesTake: true}
	for _, a := range Artifacts {
		f.built = append(f.built, a.Built)
	}
	f.fv = append(f.fv, OVMFFiles...)
	inputs := t.TempDir()
	pin := func(name, commit, file string) {
		f.in[name] = file
		f.rows = append(f.rows, name+"\thttps://example.test/"+commit+"/"+filepath.Base(file)+"\t"+sha(t, file))
	}

	efibuild := filepath.Join(inputs, "efibuild.sh")
	if err := os.WriteFile(efibuild, []byte("# efibuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pin("ocbuild-efibuild", OCBuildCommit, efibuild)

	pin("audk-src", AudkCommit, rename(t, makeTarGz(t, t.TempDir(), audkEntries()...), filepath.Join(inputs, AudkCommit+".tar.gz")))
	for _, p := range OpenCorePins()[2:] {
		tgz := makeTarGz(t, t.TempDir(), entry{name: p.Source + "-x/"}, entry{name: p.Source + "-x/README", body: p.Source})
		pin(p.Source, p.Commit, rename(t, tgz, filepath.Join(inputs, p.Commit+".tar.gz")))
	}
	ocpkg := makeTarGz(t, t.TempDir(),
		entry{name: "OpenCorePkg-" + OCVersion + "/"},
		entry{name: "OpenCorePkg-" + OCVersion + "/build_oc.tool", body: "#!/bin/bash\n" + buildOCToolFetch, mode: 0o755},
		entry{name: "OpenCorePkg-" + OCVersion + "/Patches/0002-second.patch", body: "second"},
		entry{name: "OpenCorePkg-" + OCVersion + "/Patches/0001-first.patch", body: "first"},
	)
	pin("opencorepkg-src", "refs/tags/"+OCVersion, rename(t, ocpkg, filepath.Join(inputs, OCVersion+".tar.gz")))
	for _, k := range Kexts {
		z := makeZip(t, inputs, k.Name+"-RELEASE.zip",
			entry{name: k.Name + ".kext/Contents/Info.plist", body: "plist " + k.Name},
			entry{name: k.Name + ".kext/Contents/MacOS/" + k.Name, body: "macho " + k.Name, mode: 0o755})
		pin(k.Source, "releases", z)
	}

	f.fake = &proc.Fake{Paths: map[string]string{}, Handle: f.handle}
	for _, tool := range []string{"bash", "git", "zip", "make", "python3", "gcc", "nasm", "iasl"} {
		f.fake.Paths[tool] = "/usr/bin/" + tool
	}
	f.b = &Builder{
		Paths:     config.Paths{Home: f.home},
		Runner:    f.fake,
		Toolchain: Toolchain{Runner: f.fake},
		Env:       []string{"PATH=/usr/bin:/bin", "HOME=" + f.home},
		Log:       func(format string, a ...any) { f.log.WriteString(fmt.Sprintf(format, a...) + "\n") },
	}
	f.parse()
	return f
}

// parse (re)builds the Builder's registry from f.rows.
func (f *fixture) parse() {
	f.t.Helper()
	r, err := pins.Parse(strings.NewReader(strings.Join(f.rows, "\n") + "\n"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.b.Registry = r
}

// repin points source name at file, with a registry row carrying file's
// checksum, keeping the URL's commit: a test that rebuilds an input.
func (f *fixture) repin(name, file string) {
	f.t.Helper()
	for i, row := range f.rows {
		fields := strings.Split(row, "\t")
		if fields[0] == name {
			f.rows[i] = name + "\t" + fields[1] + "\t" + sha(f.t, file)
			f.in[name] = file
			f.parse()
			return
		}
	}
	f.t.Fatalf("no registry row for %s", name)
}

func rename(t *testing.T, from, to string) string {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	return to
}

// handle plays the external tools.
func (f *fixture) handle(c proc.Cmd) error {
	switch {
	case c.Name == f.b.Toolchain.GCC() && len(c.Args) == 1 && c.Args[0] == "--version":
		io.WriteString(c.Stdout, f.banner+"\n")
	case c.Name == f.b.Toolchain.GCC() && len(c.Args) == 1 && c.Args[0] == "-dumpmachine":
		io.WriteString(c.Stdout, "x86_64-linux-gnu\n")
	case c.Name == f.b.Toolchain.GCC() && len(c.Args) == 4 && c.Args[0] == "-fsyntax-only":
		src, _ := io.ReadAll(c.Stdin)
		for _, h := range f.missingHeaders {
			if strings.Contains(string(src), "#include <"+h+">") {
				return &proc.ExitError{Cmd: c.String(), Code: 1}
			}
		}
	case c.Name == "git" && len(c.Args) >= 4 && c.Args[2] == "apply" && c.Args[3] == "--ignore-whitespace":
		return f.applyErr
	case c.Name == "git" && len(c.Args) >= 4 && c.Args[2] == "apply" && c.Args[len(c.Args)-1] == "-":
		patch, _ := io.ReadAll(c.Stdin)
		f.stdins = append(f.stdins, patch)
		if f.patchesTake {
			f.applyFake(c.Args[1], string(patch))
		}
	case c.Name == "./build_oc.tool":
		if c.Stdout != nil {
			io.WriteString(c.Stdout, cmp.Or(f.buildOut, "compiling OpenCore\nline 2\n"))
		}
		if f.buildErr != nil {
			return f.buildErr
		}
		built := filepath.Join(c.Dir, "UDK", "Build", "OpenCorePkg", EDKTarget+"_"+EDKToolchain, Arch)
		os.MkdirAll(filepath.Join(built, "OpenCorePkg", "Library", "x"), 0o755)
		os.WriteFile(filepath.Join(built, "OpenCorePkg", "Library", "x", "GNUmakefile"), []byte("CC_FLAGS = -Os "+f.flags+"\n"), 0o644)
		for rel, flags := range f.makefiles {
			os.MkdirAll(filepath.Join(built, filepath.Dir(rel)), 0o755)
			os.WriteFile(filepath.Join(built, rel), []byte("CC_FLAGS = -Os "+flags+"\n"), 0o644)
		}
		for _, n := range f.built {
			os.WriteFile(filepath.Join(built, n), []byte("built "+n), 0o644)
		}
		os.MkdirAll(filepath.Join(c.Dir, "UDK", "BaseTools", "Source", "C", "bin"), 0o755)
		os.WriteFile(filepath.Join(c.Dir, "UDK", "BaseTools", "Source", "C", "bin", "GenFv"), []byte("#!/bin/sh\n"), 0o755)
	case filepath.Base(c.Name) == "ocvalidate":
		return f.ocvalidate
	case c.Name == "bash" && len(c.Args) == 2 && c.Args[0] == "-c" && strings.Contains(c.Args[1], "edksetup.sh"):
		if c.Stdout != nil {
			io.WriteString(c.Stdout, cmp.Or(f.buildOut, "building OVMF\n"))
		}
		if f.buildErr != nil {
			return f.buildErr
		}
		fv := filepath.Join(c.Dir, "Build", "OvmfX64", EDKTarget+"_"+EDKToolchain, "FV")
		os.MkdirAll(fv, 0o755)
		for _, n := range f.fv {
			os.WriteFile(filepath.Join(fv, n), []byte("fd "+n), 0o644)
		}
	}
	return nil
}

// applyFake makes a patch's effect happen: build_oc.tool stops fetching
// efibuild.sh, or the OVMF dsc gains the flag its patch adds.
func (f *fixture) applyFake(dir, patch string) {
	switch {
	case strings.Contains(patch, "build_oc.tool"):
		p := filepath.Join(dir, "build_oc.tool")
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(b), buildOCToolFetch, "src=$(cat \"${EFIBUILD_SH}\") && eval \"$src\" || exit 1\n", 1)), 0o755)
	case strings.Contains(patch, "std=gnu17"):
		appendTo(filepath.Join(dir, OVMFDsc), "  GCC:*_*_*_CC_FLAGS = -std=gnu17\n")
	case strings.Contains(patch, "Wno-error"):
		appendTo(filepath.Join(dir, OVMFDsc), "  GCC:*_*_*_CC_FLAGS = -Wno-error\n")
	}
}

func appendTo(p, s string) {
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, s...), 0o644)
}

// calls is every command the fake saw whose name is name.
func (f *fixture) calls(name string) []proc.Cmd {
	var out []proc.Cmd
	for _, c := range f.fake.Calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}
