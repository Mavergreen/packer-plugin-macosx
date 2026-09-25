package firmware

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
)

// repo is the repository root, for the golden files and for reading
// files this package ships (e.g. assets/firmware/config.plist).
func repo(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// golden reads testdata/golden/<name>, whole (testdata/golden/README.md
// says what each file is).
func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo(t), "internal", "firmware", "testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

// TestPinsAndListsMatchTheirGoldens: the pins, and every list the builds
// name their inputs and outputs by, are the golden files' word for word.
func TestPinsAndListsMatchTheirGoldens(t *testing.T) {
	var pinLines []string
	for _, p := range OpenCorePins() {
		pinLines = append(pinLines, p.Source+"\t"+p.Commit)
	}
	checks := []struct {
		name string
		got  []string
		want string
	}{
		{"OpenCore pins", pinLines, golden(t, "opencore-pins.txt")},
		{"OpenCore artifacts", ShipNames(), golden(t, "opencore-artifacts.txt")},
		{"UDK commit", []string{AudkCommit}, golden(t, "opencore-udk-commit.txt")},
		{"build options", []string{strings.ReplaceAll(BuildOptions(), "\t", " ")}, golden(t, "opencore-build-options.txt")},
		{"OVMF artifacts", OVMFFiles, golden(t, "ovmf-artifacts.txt")},
		{"OVMF build", []string{OVMFDsc + "\t" + Arch + "\t" + EDKToolchain + "\t" + EDKTarget}, golden(t, "ovmf-build.txt")},
		{"kexts", kextNames(), golden(t, "kexts.txt")},
		{"OpenCorePkg version", []string{"OpenCorePkg " + OCVersion}, golden(t, "opencorepkg-version.txt")},
		{"EFI image contents", efiContents(), golden(t, "efi-image-contents.txt")},
		{"EDK II sources", pinSources(), golden(t, "edk2-sources.txt")},
	}
	for _, c := range checks {
		if strings.Join(c.got, "\n") != strings.Join(lines(c.want), "\n") {
			t.Errorf("%s:\n got:    %q\n golden: %q", c.name, c.got, lines(c.want))
		}
	}
}

func kextNames() (n []string) {
	for _, k := range Kexts {
		n = append(n, k.Name)
	}
	return n
}

func pinSources() (n []string) {
	for _, p := range OpenCorePins() {
		n = append(n, p.Source)
	}
	return n
}

// efiContents is what the EFI image holds, by top-level name: the
// artifacts in image order, the kext bundles, then the config.
func efiContents() []string {
	c := append([]string{"BOOTx64.efi", "OpenCore.efi"}, EFIDrivers...)
	for _, k := range Kexts {
		c = append(c, k.Name+".kext")
	}
	return append(c, "config.plist")
}

// TestEFIImageSizeMatchesItsGolden: the EFI image's size in MiB is a
// known answer, so changing it is a deliberate edit of
// testdata/golden/efi-image-mib.txt too.
func TestEFIImageSizeMatchesItsGolden(t *testing.T) {
	if got, want := fmt.Sprintf("%d\n", EFIImageMiB), golden(t, "efi-image-mib.txt"); got != want {
		t.Fatalf("EFIImageMiB is %q; the golden says %q", strings.TrimSpace(got), strings.TrimSpace(want))
	}
}

func TestSourceNamesAreThePins(t *testing.T) {
	want := append(pinSources(), "opencorepkg-src")
	if strings.Join(OpenCoreSources(), " ") != strings.Join(want, " ") {
		t.Fatalf("OpenCoreSources = %v", OpenCoreSources())
	}
	all := append(append([]string{}, OpenCoreSources()...), KextSources()...)
	if strings.Join(SourceNames(), " ") != strings.Join(all, " ") {
		t.Fatalf("SourceNames = %v", SourceNames())
	}
}

func TestEveryPinIsInTheEmbeddedRegistryWithItsCommit(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPins(reg); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPinsRefusesAURLWithoutItsCommit(t *testing.T) {
	reg := registryWith(t, "audk-src", "https://example.test/archive/0000.tar.gz", strings.Repeat("a", 64))
	err := CheckPins(reg)
	if err == nil || !strings.Contains(err.Error(), "audk-src") || !strings.Contains(err.Error(), AudkCommit) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckPinsRefusesUnpinned(t *testing.T) {
	reg := registryWith(t, "audk-src", "https://example.test/archive/"+AudkCommit+".tar.gz", "TOFU")
	if err := CheckPins(reg); err == nil || !strings.Contains(err.Error(), "audk-src") {
		t.Fatalf("err = %v", err)
	}
}

func TestEveryPinIsAnImmutableURL(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range SourceNames() {
		s, err := reg.Lookup(n)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"refs/heads", "/master/", "/main/"} {
			if strings.Contains(s.URL, bad) {
				t.Errorf("%s fetches from a mutable branch: %s", n, s.URL)
			}
		}
	}
}

// registryWith is the embedded registry with one row replaced, so a test
// can break exactly one pin.
func registryWith(t *testing.T, name, url, sha string) *pins.Registry {
	t.Helper()
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(name + "\t" + url + "\t" + sha + "\n") // first match wins
	for _, r := range reg.Rows() {
		b.WriteString(r.Name + "\t" + r.URL + "\t" + r.SHA256 + "\n")
	}
	out, err := pins.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// One list per build is what each build requires and what doctor asks
// about: Tools is their union, each once, in the order they first appear.
func TestToolsIsEachBuildsListTogether(t *testing.T) {
	want := []string{"bash", "make", "x-gcc", "git", "python3", "nasm", "iasl", "zip"}
	if got := Tools("x-gcc"); !reflect.DeepEqual(got, want) {
		t.Errorf("Tools = %q, want %q", got, want)
	}
	for _, list := range [][]string{openCoreTools("x-gcc"), ovmfTools("x-gcc")} {
		for _, tool := range list {
			if !slices.Contains(Tools("x-gcc"), tool) {
				t.Errorf("Tools lacks %s", tool)
			}
		}
	}
}

// Each build refuses to start without any one of its own list.
func TestEachBuildRequiresEveryToolOnItsList(t *testing.T) {
	for _, tool := range openCoreTools("gcc") {
		f := newFixture(t)
		delete(f.fake.Paths, tool)
		if _, err := f.openCore(); err == nil || !strings.Contains(err.Error(), "missing build tools: "+tool) {
			t.Errorf("OpenCore without %s: %v", tool, err)
		}
	}
	for _, tool := range ovmfTools("gcc") {
		f := ovmfFixture(t)
		delete(f.fake.Paths, tool)
		if _, err := f.ovmf(); err == nil || !strings.Contains(err.Error(), "missing build tools: "+tool) {
			t.Errorf("OVMF without %s: %v", tool, err)
		}
	}
}

func TestHeaderPackageNamesTheDistributionsPackages(t *testing.T) {
	if got := HeaderPackage("uuid/uuid.h"); got != "the uuid development package: uuid-dev on Debian, util-linux-libs on Arch" {
		t.Errorf("HeaderPackage(uuid/uuid.h) = %q", got)
	}
	for _, h := range Headers {
		if HeaderPackage(h) == "" {
			t.Errorf("no package named for %s", h)
		}
	}
}
