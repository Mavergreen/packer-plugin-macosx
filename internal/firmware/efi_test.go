package firmware

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/diskimg"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// writeOVMF writes OVMF_CODE.fd and OVMF_VARS.fd to dir with the given
// contents, and a matching SHA256SUMS beside them (WriteSums, the same
// writer Builder.OVMF uses).
func writeOVMF(t *testing.T, dir, code, vars string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "OVMF_CODE.fd"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OVMF_VARS.fd"), []byte(vars), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSums(dir, []string{"OVMF_CODE.fd", "OVMF_VARS.fd"}); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyOVMFAcceptsMatchingSums: the common case -- a build's own
// files against its own SHA256SUMS.
func TestVerifyOVMFAcceptsMatchingSums(t *testing.T) {
	dir := t.TempDir()
	writeOVMF(t, dir, "code bytes", "vars bytes")
	if err := VerifyOVMF(dir); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyOVMFAcceptsSha256sumsOwnFormat: sha256sum's own output,
// "<hex><two spaces><name>", is byte for byte what WriteSums writes --
// MEASURED against coreutils sha256sum on this host -- so a SHA256SUMS
// written by hand with sha256sum needs no translation.
func TestVerifyOVMFAcceptsSha256sumsOwnFormat(t *testing.T) {
	dir := t.TempDir()
	code := filepath.Join(dir, "OVMF_CODE.fd")
	vars := filepath.Join(dir, "OVMF_VARS.fd")
	if err := os.WriteFile(code, []byte("code bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vars, []byte("vars bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sums := sha(t, code) + "  OVMF_CODE.fd\n" + sha(t, vars) + "  OVMF_VARS.fd\n"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOVMF(dir); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyOVMFRefusesAMismatch: a file that does not match its own
// SHA256SUMS is refused, naming the file and the fix.
func TestVerifyOVMFRefusesAMismatch(t *testing.T) {
	dir := t.TempDir()
	writeOVMF(t, dir, "code bytes", "vars bytes")
	if err := os.WriteFile(filepath.Join(dir, "OVMF_VARS.fd"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := VerifyOVMF(dir)
	if err == nil || !strings.Contains(err.Error(), "OVMF_VARS.fd") || !strings.Contains(err.Error(), "does not match") ||
		!strings.Contains(err.Error(), "rebuild the firmware") {
		t.Fatalf("err = %v", err)
	}
}

// TestVerifyOVMFRefusesAMissingSumsFile: no SHA256SUMS at all is refused
// the same way, naming the file and the fix, not a bare os.ReadFile error.
func TestVerifyOVMFRefusesAMissingSumsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "OVMF_CODE.fd"), []byte("code"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OVMF_VARS.fd"), []byte("vars"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := VerifyOVMF(dir)
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") || !strings.Contains(err.Error(), "rebuild the firmware") {
		t.Fatalf("err = %v", err)
	}
}

// TestVerifyOVMFRefusesASumsFileThatDoesNotListOne: present and
// matching for what it lists is not enough; OVMF_VARS.fd must be named.
func TestVerifyOVMFRefusesASumsFileThatDoesNotListOne(t *testing.T) {
	dir := t.TempDir()
	code := []byte("code bytes")
	if err := os.WriteFile(filepath.Join(dir, "OVMF_CODE.fd"), code, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OVMF_VARS.fd"), []byte("vars bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSums(dir, []string{"OVMF_CODE.fd"}); err != nil {
		t.Fatal(err)
	}
	err := VerifyOVMF(dir)
	if err == nil || !strings.Contains(err.Error(), "OVMF_VARS.fd") || !strings.Contains(err.Error(), "rebuild the firmware") {
		t.Fatalf("err = %v", err)
	}
}

// shipped makes build/ look as OpenCore and Kexts leave it: five fake
// artifacts with their SHA256SUMS, and the two kext bundles. Neither
// build tool is needed.
func shipped(f *fixture) {
	f.t.Helper()
	art := filepath.Join(f.home, "build", "artifacts")
	if err := os.MkdirAll(art, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for _, n := range ShipNames() {
		if err := os.WriteFile(filepath.Join(art, n), []byte("fake "+n), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := WriteSums(art, ShipNames()); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) efiImage(model string) (string, error) {
	return f.b.EFIImage(context.Background(), model, false)
}

// imageContents is an image's GPT partitions and its FAT, read back.
type imageContents struct {
	parts []diskimg.Partition
	fat   *diskimg.FATReader
	paths []string // every walked path, sorted
	files map[string][]byte
}

func readEFI(t *testing.T, img string) imageContents {
	t.Helper()
	fh, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fh.Close() })
	fi, err := fh.Stat()
	if err != nil {
		t.Fatal(err)
	}
	_, parts, err := diskimg.ReadGPT(fh, uint64(fi.Size())/diskimg.SectorSize)
	if err != nil {
		t.Fatal(err)
	}
	fat, err := diskimg.OpenFAT(fh, 2048*512)
	if err != nil {
		t.Fatal(err)
	}
	c := imageContents{parts: parts, fat: fat, files: map[string][]byte{}}
	if err := fat.Walk(func(p string, e diskimg.DirEntry) error {
		c.paths = append(c.paths, p)
		if !e.Dir {
			b, err := fat.ReadFile(p)
			if err != nil {
				return err
			}
			c.files[p] = b
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(c.paths)
	return c
}

// TestFitsDemandsHeadroom: each case's verdict is also in
// testdata/golden/efi-fits.txt, one line per case, 0 for "fits".
func TestFitsDemandsHeadroom(t *testing.T) {
	cases := []struct {
		mib     int
		payload int64
		want    bool
	}{
		{192, 0, true}, {192, 98041855, true}, {192, 98041856, true}, {192, 98041857, false},
		{192, 200000000, false}, {5, 1, false}, {4, 0, false},
	}
	fits := goldenLines(t, "efi-fits.txt")
	if len(fits) != len(cases) {
		t.Fatalf("%d golden lines, want %d", len(fits), len(cases))
	}
	for i, c := range cases {
		got := Fits(c.mib, c.payload)
		if got != c.want {
			t.Errorf("Fits(%d, %d) = %v, want %v", c.mib, c.payload, got, c.want)
		}
		if want := fits[i] == "0"; want != got {
			t.Errorf("Fits(%d, %d) = %v, the golden says %v", c.mib, c.payload, got, want)
		}
	}
}

func TestKextsUnpackEachBundleWithItsBinary(t *testing.T) {
	f := newFixture(t)
	got, err := f.b.Kexts(context.Background(), f.in)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.home, "build", "kexts")
	want := []string{filepath.Join(dir, "Lilu.kext"), filepath.Join(dir, "VirtualSMC.kext")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Kexts returned %v, want %v", got, want)
	}
	for _, k := range Kexts {
		bundle := filepath.Join(dir, k.Name+".kext")
		if _, err := os.Stat(filepath.Join(bundle, "Contents", "Info.plist")); err != nil {
			t.Error(err)
		}
		fi, err := os.Stat(filepath.Join(bundle, "Contents", "MacOS", k.Name))
		if err != nil || fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s's binary: %v, %v", k.Name, fi, err)
		}
	}
}

func TestKextsKeepAnExistingBundle(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(f.home, "build", "kexts", "Lilu.kext", "Contents", "Info.plist")
	before, err := os.Stat(plist)
	if err != nil {
		t.Fatal(err)
	}
	// A bundle that is Go's and at the pin is kept without its release:
	// nothing is unpacked again.
	delete(f.in, "lilu-release")
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(plist); err != nil || !os.SameFile(before, after) {
		t.Errorf("the bundle was unpacked again: %v", err)
	}
}

func TestKextsNameTheMissingBinary(t *testing.T) {
	f := newFixture(t)
	z := makeZip(t, t.TempDir(), "Lilu-RELEASE.zip", entry{name: "Lilu.kext/Contents/Info.plist", body: "plist Lilu"})
	f.repin("lilu-release", z)
	_, err := f.b.Kexts(context.Background(), f.in)
	want := filepath.Join(f.home, "build", "kexts", "Lilu.kext") + " is not a kext bundle: no Contents/MacOS/Lilu"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

func TestEFIImageLayout(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	img, err := f.efiImage("")
	if err != nil {
		t.Fatalf("EFIImage: %v\n%s", err, f.log.String())
	}
	if want := filepath.Join(f.home, "build", "opencore.img"); img != want {
		t.Errorf("EFIImage returned %s, want %s", img, want)
	}
	if b, _ := os.ReadFile(img + ".sha256"); string(b) != sha(t, img)+"\n" {
		t.Errorf("the sidecar holds %q", b)
	}
	c := readEFI(t, img)
	if len(c.parts) != 1 {
		t.Fatalf("%d partitions, want 1", len(c.parts))
	}
	p := c.parts[0]
	if p.Type != diskimg.TypeEFISystem || p.FirstLBA != 2048 || p.LastLBA != 393182 || p.Name != "EFI" {
		t.Errorf("partition %+v", p)
	}
	var want []string
	want = append(want, "/EFI", "/EFI/BOOT", "/EFI/OC", "/EFI/OC/Drivers", "/EFI/OC/Kexts", "/EFI/OC/ACPI",
		"/EFI/OC/Tools", "/EFI/OC/Resources", "/EFI/BOOT/BOOTx64.efi", "/EFI/OC/OpenCore.efi",
		"/EFI/OC/Drivers/OpenRuntime.efi", "/EFI/OC/Drivers/OpenPartitionDxe.efi", "/EFI/OC/Drivers/OpenHfsPlus.efi",
		"/EFI/OC/config.plist")
	for _, k := range []string{"Lilu", "VirtualSMC"} {
		b := "/EFI/OC/Kexts/" + k + ".kext"
		want = append(want, b, b+"/Contents", b+"/Contents/MacOS", b+"/Contents/Info.plist", b+"/Contents/MacOS/"+k)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(c.paths, want) {
		t.Errorf("the image holds\n%s\nwant\n%s", strings.Join(c.paths, "\n"), strings.Join(want, "\n"))
	}
	if string(c.files["/EFI/OC/config.plist"]) != string(embeddedConfig(t)) {
		t.Errorf("config.plist is not the repository's")
	}
	if string(c.files["/EFI/BOOT/BOOTx64.efi"]) != "fake BOOTx64.efi" {
		t.Errorf("BOOTx64.efi holds %q", c.files["/EFI/BOOT/BOOTx64.efi"])
	}
	for _, p := range c.paths {
		if strings.Contains(p, "HfsPlusLegacy") {
			t.Errorf("the image holds %s", p)
		}
	}
}

// goldenEFIImage is the sha256 of the image EFIImage makes from the
// synthetic fixture: shipped's fake artifacts, the fixture's kext
// bundles and the repository's config.plist at the default SMBIOS model
// (a known answer). It moves only with a deliberate change to the
// image's format or to assets/firmware/config.plist; say which in the
// commit.
const goldenEFIImage = "fa24df21bdde91f8f3f4149fc7d5a0525cb3b6e9881244111a9431b692c8124e"

func TestEFIImageIsDeterministic(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	img, err := f.efiImage("")
	if err != nil {
		t.Fatal(err)
	}
	first := sha(t, img)
	if first != goldenEFIImage {
		t.Errorf("the fixture's image has sha256 %s, pinned %s", first, goldenEFIImage)
	}
	for _, p := range []string{img, img + ".sha256"} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.efiImage(""); err != nil {
		t.Fatal(err)
	}
	if again := sha(t, img); again != first {
		t.Errorf("two builds differ: %s, then %s", first, again)
	}
}

func TestEFIImageRefusesArtifactsThatDoNotMatchSHA256SUMS(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	if err := os.WriteFile(filepath.Join(f.home, "build", "artifacts", "OpenCore.efi"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(f.home, "build", "opencore.img")
	if err := os.WriteFile(img, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.efiImage("")
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(img); string(b) != "old" {
		t.Errorf("the previous image was replaced")
	}
}

func TestEFIImageNamesTheMissingPiece(t *testing.T) {
	f := newFixture(t)
	_, err := f.efiImage("")
	art := filepath.Join(f.home, "build", "artifacts")
	if want := "no artifacts at " + art + " -- build the OpenCore artifacts first"; err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}

	g := newFixture(t)
	shipped(g)
	bundle := filepath.Join(g.home, "build", "kexts", "VirtualSMC.kext")
	if err := os.Remove(filepath.Join(bundle, "Contents", "Info.plist")); err != nil {
		t.Fatal(err)
	}
	_, err = g.efiImage("")
	if err == nil || !strings.Contains(err.Error(), bundle) || !strings.Contains(err.Error(), "Contents/Info.plist") ||
		!strings.Contains(err.Error(), "remove "+bundle+" and rebuild the EFI image") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(g.home, "build", "opencore.img")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an image was written anyway: %v", err)
	}
}

func TestEFIImageWithAnotherSMBIOS(t *testing.T) {
	want, err := SetProductName(embeddedConfig(t), "MacPro5,1")
	if err != nil {
		t.Fatal(err)
	}

	f := newFixture(t)
	shipped(f)
	if err := os.MkdirAll(filepath.Dir(f.b.ocvalidate()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.b.ocvalidate(), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	img, err := f.efiImage("MacPro5,1")
	if err != nil {
		t.Fatalf("EFIImage: %v\n%s", err, f.log.String())
	}
	derived := filepath.Join(f.home, "build", "config", "config-MacPro5,1.plist")
	if b, _ := os.ReadFile(derived); string(b) != string(want) {
		t.Errorf("%s is not SetProductName's", derived)
	}
	if c := readEFI(t, img); string(c.files["/EFI/OC/config.plist"]) != string(want) {
		t.Errorf("the image's config.plist is not the derived one")
	}
	cs := f.calls(f.b.ocvalidate())
	if len(cs) != 1 || !reflect.DeepEqual(cs[0].Args, []string{derived}) {
		t.Errorf("ocvalidate calls: %v", cs)
	}

	f.ocvalidate = &proc.ExitError{Cmd: f.b.ocvalidate(), Code: 1}
	if _, err := f.efiImage("MacPro5,1"); err == nil || !strings.Contains(err.Error(), "ocvalidate rejected the derived config") {
		t.Errorf("err = %v", err)
	}

	g := newFixture(t)
	shipped(g)
	if _, err := g.efiImage("MacPro5,1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.log.String(), "shipping the derived config unvalidated") {
		t.Errorf("no warning:\n%s", g.log.String())
	}
}

func TestEFIImageRefusesAMalformedSMBIOS(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	_, err := f.efiImage("a<b")
	if err == nil || !strings.Contains(err.Error(), "a<b") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "build", "opencore.img")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an image was written: %v", err)
	}
}

// TestEFIImageMatchesItsGolden holds the image EFIImage builds equal,
// where it should be, to the same image as sgdisk and mtools lay it out
// from the same artifacts and kexts (shipped(f), the default SMBIOS):
// testdata/golden/efi-image/ holds that image's structural facts -- the
// partition, the masked FAT geometry, the volume label, the sorted paths
// and every file's bytes -- and its sidecar. sgdisk, mtools and
// sha256sum still judge the Go image live elsewhere in this package.
//
// The differences that are deliberate, and so not compared:
//   - the GPT disk and partition GUIDs: sgdisk's are random, ours derived;
//   - the volume serial number: mformat's is random, ours derived;
//   - the OEM name: mformat writes MTOO4043, we write MSWIN4.1;
//   - hidden sectors: mformat writes 0, we write the partition's LBA;
//   - timestamps: mtools' are now, ours 1980-01-01;
//   - short-name tails: mtools and we may number an 8.3 alias differently,
//     so paths are compared by long name.
//
// mformat's boot-code stub is not compared either: nothing reads it.
func TestEFIImageMatchesItsGolden(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	goImg, err := f.efiImage("")
	if err != nil {
		t.Fatal(err)
	}
	want := readGoldenEFIImage(t)
	g := readEFI(t, goImg)
	if len(g.parts) != 1 {
		t.Fatalf("partitions: go %d", len(g.parts))
	}
	gp := g.parts[0]
	if want.FirstLBA != uint64(gp.FirstLBA) || want.LastLBA != uint64(gp.LastLBA) || want.Type != fmt.Sprintf("%v", gp.Type) || want.Name != gp.Name {
		t.Errorf("partition: golden %+v, go %+v", want, gp)
	}
	gg := g.fat.G
	gg.HiddenSectors = 0
	if want.Geometry != fmt.Sprintf("%+v", gg) {
		t.Errorf("FAT geometry: golden %s, go %+v", want.Geometry, gg)
	}
	if want.Label != g.fat.Label {
		t.Errorf("volume label: golden %q, go %q", want.Label, g.fat.Label)
	}
	if !reflect.DeepEqual(want.Paths, g.paths) {
		t.Errorf("paths: golden\n%s\ngo\n%s", strings.Join(want.Paths, "\n"), strings.Join(g.paths, "\n"))
	}
	for p, b := range want.Files {
		if string(g.files[p]) != string(b) {
			t.Errorf("%s: golden %d bytes, go %d bytes, and they differ", p, len(b), len(g.files[p]))
		}
	}
	t.Logf("golden and go agree: partition %d-%d %q, geometry %+v, %d paths, %d files",
		gp.FirstLBA, gp.LastLBA, gp.Name, gg, len(g.paths), len(g.files))
	// An sgdisk-and-mtools image is not reproducible byte for byte
	// (sgdisk's GPT/partition GUIDs and mformat's volume serial are
	// random), so the golden sidecar cannot be checked against a
	// recomputed hash; what it shows is the sidecar's shape, bare hex and
	// a newline.
	if !bareHexAndNewline.MatchString(want.SidecarSHA256) {
		t.Errorf("the golden sidecar is not bare hex and a newline: %q", want.SidecarSHA256)
	}
}

var bareHexAndNewline = regexp.MustCompile(`^[0-9a-f]{64}\n$`)

// goldenEFIImageMeta is testdata/golden/efi-image/meta.json's shape.
type goldenEFIImageMeta struct {
	FirstLBA, LastLBA uint64
	Type              string
	Name              string
	Geometry          string
	Label             string
	Paths             []string
}

// goldenEFIImageFacts is the golden EFI image's structural facts and file
// bytes (see TestEFIImageMatchesItsGolden's comment).
type goldenEFIImageFacts struct {
	goldenEFIImageMeta
	Files         map[string][]byte
	SidecarSHA256 string
}

// readGoldenEFIImageFiles reads files.tar.gz: every non-directory path's
// bytes, gzip'd and tarred (rather than tracked as loose files under a
// path shaped like a kext bundle) so bin/no-apple-bytes.sh's path check
// -- which flags any *.kext/* path, fixture or not -- has nothing to
// flag.
func readGoldenEFIImageFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "files.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files["/"+hdr.Name] = data
	}
	return files
}

func readGoldenEFIImage(t *testing.T) goldenEFIImageFacts {
	t.Helper()
	root := filepath.Join(repo(t), "internal", "firmware", "testdata", "golden", "efi-image")
	b, err := os.ReadFile(filepath.Join(root, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta goldenEFIImageMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile(filepath.Join(root, "sidecar.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	return goldenEFIImageFacts{goldenEFIImageMeta: meta, Files: readGoldenEFIImageFiles(t, root), SidecarSHA256: string(sidecar)}
}

func TestKextsMarkWhatTheyUnpacked(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	for _, k := range Kexts {
		marker := filepath.Join(f.home, "build", "kexts", "."+k.Name+".kext.sha256")
		tree, err := treeDigest(filepath.Join(f.home, "build", "kexts", k.Name+".kext"))
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(marker); string(b) != sha(t, f.in[k.Source])+" "+tree+"\n" {
			t.Errorf("%s holds %q", marker, b)
		}
	}
}

// wantMarker is the marker Kexts writes for source's bundle at the pin.
func wantMarker(t *testing.T, f *fixture, source, bundle string) string {
	t.Helper()
	tree, err := treeDigest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return sha(t, f.in[source]) + " " + tree + "\n"
}

func TestTreeDigestSeesPathsKindsAndBytes(t *testing.T) {
	mk := func(files map[string]string, dirs ...string) string {
		d := t.TempDir()
		for _, dir := range dirs {
			if err := os.MkdirAll(filepath.Join(d, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for p, body := range files {
			if err := os.MkdirAll(filepath.Join(d, filepath.Dir(p)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, p), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sum, err := treeDigest(d)
		if err != nil {
			t.Fatal(err)
		}
		if len(sum) != 64 {
			t.Fatalf("digest %q", sum)
		}
		return sum
	}
	base := mk(map[string]string{"a/b": "x", "c": "y"})
	if again := mk(map[string]string{"c": "y", "a/b": "x"}); again != base {
		t.Error("the same tree digests differently")
	}
	for name, other := range map[string]string{
		"a byte":         mk(map[string]string{"a/b": "z", "c": "y"}),
		"a path":         mk(map[string]string{"a/b": "x", "d": "y"}),
		"an extra dir":   mk(map[string]string{"a/b": "x", "c": "y"}, "e"),
		"a file for dir": mk(map[string]string{"a/b": "x", "c/f": "y"}),
		"moved bytes":    mk(map[string]string{"a/b": "xy", "c": ""}),
	} {
		if other == base {
			t.Errorf("%s changed and the digest did not", name)
		}
	}
}

// liluZip is a Lilu release whose Info.plist says body.
func liluZip(t *testing.T, body string) string {
	return makeZip(t, t.TempDir(), "Lilu-RELEASE.zip",
		entry{name: "Lilu.kext/Contents/Info.plist", body: body},
		entry{name: "Lilu.kext/Contents/MacOS/Lilu", body: "macho Lilu", mode: 0o755})
}

// noTemps fails t if anything but the bundles and their markers is in
// build/kexts.
func noTemps(t *testing.T, f *fixture) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(f.home, "build", "kexts"))
	for _, e := range ents {
		switch e.Name() {
		case "Lilu.kext", "VirtualSMC.kext", ".Lilu.kext.sha256", ".VirtualSMC.kext.sha256":
		default:
			t.Errorf("left behind in build/kexts: %s", e.Name())
		}
	}
}

func TestKextsReplaceTheirOwnBundleWhenThePinMoves(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(f.home, "build", "kexts", "Lilu.kext")
	f.repin("lilu-release", liluZip(t, "plist Lilu, the next release"))
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "plist Lilu, the next release" {
		t.Errorf("the bundle was not replaced: Info.plist holds %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(f.home, "build", "kexts", ".Lilu.kext.sha256")); string(b) != wantMarker(t, f, "lilu-release", bundle) {
		t.Errorf("the marker holds %q", b)
	}
	noTemps(t, f)
}

// A pinned release that is not a usable bundle does not replace one that
// is: it is checked before the old bundle goes.
func TestKextsKeepTheirOwnBundleWhenTheNewPinIsBroken(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	kexts := filepath.Join(f.home, "build", "kexts")
	marker := filepath.Join(kexts, ".Lilu.kext.sha256")
	oldMarker, _ := os.ReadFile(marker)
	plist := filepath.Join(kexts, "Lilu.kext", "Contents", "Info.plist")
	before, err := os.Stat(plist)
	if err != nil {
		t.Fatal(err)
	}
	f.repin("lilu-release", makeZip(t, t.TempDir(), "Lilu-RELEASE.zip", entry{name: "Lilu.kext/Contents/Info.plist", body: "no binary"}))
	_, err = f.b.Kexts(context.Background(), f.in)
	if err == nil || !strings.Contains(err.Error(), "Contents/MacOS/Lilu") || !strings.Contains(err.Error(), "lilu-release") {
		t.Fatalf("err = %v", err)
	}
	if after, err := os.Stat(plist); err != nil || !os.SameFile(before, after) {
		t.Errorf("the old bundle was replaced: %v", err)
	}
	if b, _ := os.ReadFile(marker); string(b) != string(oldMarker) {
		t.Errorf("the marker changed: %q, then %q", oldMarker, b)
	}
	noTemps(t, f)
}

// foreign replaces a Go-marked Lilu.kext with a bundle Go did not unpack,
// leaving the marker: a marker that outlived its bundle must not make the
// new bundle Go's.
func foreign(t *testing.T, f *fixture) string {
	t.Helper()
	bundle := filepath.Join(f.home, "build", "kexts", "Lilu.kext")
	if err := os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Contents/Info.plist", "Contents/MacOS/Lilu"} {
		if err := os.MkdirAll(filepath.Join(bundle, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, p), []byte("foreign "+p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bundle
}

func TestKextsCompareAForeignBundleBehindAMarkerAtTheSamePin(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	bundle := foreign(t, f)
	_, err := f.b.Kexts(context.Background(), f.in)
	want := bundle + " does not match the pinned lilu-release (" + sha(t, f.in["lilu-release"]) + "); remove it and re-run"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "foreign Contents/Info.plist" {
		t.Errorf("the foreign bundle was touched: %q", b)
	}
	noTemps(t, f)
}

func TestKextsNeverRemoveAForeignBundleBehindAMarkerWhenThePinMoves(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	bundle := foreign(t, f)
	f.repin("lilu-release", liluZip(t, "plist Lilu, the next release"))
	_, err := f.b.Kexts(context.Background(), f.in)
	want := bundle + " does not match the pinned lilu-release (" + sha(t, f.in["lilu-release"]) + "); remove it and re-run"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "foreign Contents/Info.plist" {
		t.Errorf("the foreign bundle was touched: %q", b)
	}
	noTemps(t, f)
}

// An older one-field marker, the zip's sha256 alone, says nothing about
// the bundle, so it counts as no marker.
func TestKextsTreatAOneFieldMarkerAsNone(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	kexts := filepath.Join(f.home, "build", "kexts")
	marker := filepath.Join(kexts, ".Lilu.kext.sha256")
	if err := os.WriteFile(marker, []byte(sha(t, f.in["lilu-release"])+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(kexts, "Lilu.kext")
	if b, _ := os.ReadFile(marker); string(b) != wantMarker(t, f, "lilu-release", bundle) {
		t.Errorf("a matching bundle's marker is %q", b)
	}

	if err := os.WriteFile(marker, []byte(sha(t, f.in["lilu-release"])+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	foreign(t, f)
	if _, err := f.b.Kexts(context.Background(), f.in); err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "foreign Contents/Info.plist" {
		t.Errorf("the foreign bundle was touched: %q", b)
	}
}

func TestKextsKeepAnUnmarkedBundleThatMatches(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(f.home, "build", "kexts", ".Lilu.kext.sha256")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(f.home, "build", "kexts", "Lilu.kext", "Contents", "Info.plist")
	before, err := os.Stat(plist)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.Kexts(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(plist)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("the matching bundle was replaced: %v", err)
	}
	if b, _ := os.ReadFile(marker); string(b) != wantMarker(t, f, "lilu-release", filepath.Join(f.home, "build", "kexts", "Lilu.kext")) {
		t.Errorf("the marker holds %q", b)
	}
	noTemps(t, f)
}

func TestKextsNeverRemoveAnUnmarkedBundleThatDiffers(t *testing.T) {
	f := newFixture(t)
	bundle := filepath.Join(f.home, "build", "kexts", "Lilu.kext")
	for _, p := range []string{"Contents/Info.plist", "Contents/MacOS/Lilu"} {
		if err := os.MkdirAll(filepath.Join(bundle, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, p), []byte("an older "+p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.b.Kexts(context.Background(), f.in)
	want := bundle + " does not match the pinned lilu-release (" + sha(t, f.in["lilu-release"]) + "); remove it and re-run"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "an older Contents/Info.plist" {
		t.Errorf("the bundle was touched: %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.home, "build", "kexts", ".Lilu.kext.sha256")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a marker for a bundle that does not match: %v", err)
	}
	noTemps(t, f)
}

func TestEFIImageLeavesNoTempWhenTheSidecarCannotBeWritten(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	side := filepath.Join(f.home, "build", "opencore.img.sha256")
	if err := os.MkdirAll(filepath.Join(side, "in-the-way"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.efiImage(""); err == nil {
		t.Fatal("wrote a sidecar over a directory")
	}
	temps, _ := filepath.Glob(filepath.Join(f.home, "build", ".opencore.img*"))
	if len(temps) != 0 {
		t.Errorf("temp files left behind: %v", temps)
	}
}

// An existing image is never clobbered in place: a build that fails
// leaves it as it was, and one that succeeds replaces it whole (written
// beside it and renamed over it).
func TestEFIImageLeavesThePreviousImageWhenItFails(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	img := filepath.Join(f.home, "build", "opencore.img")
	if err := os.WriteFile(img, []byte("the previous image"), 0o644); err != nil {
		t.Fatal(err)
	}
	efi := filepath.Join(f.home, "build", "artifacts", "OpenCore.efi")
	good, err := os.ReadFile(efi)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(efi, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.efiImage(""); err == nil {
		t.Fatal("built from an artifact SHA256SUMS does not vouch for")
	}
	if b, _ := os.ReadFile(img); string(b) != "the previous image" {
		t.Errorf("a failed build changed the previous image (%d bytes now)", len(b))
	}
	if err := os.WriteFile(efi, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.efiImage(""); err != nil {
		t.Fatal(err)
	}
	if got := sha(t, img); got != goldenEFIImage {
		t.Errorf("the rebuilt image is %s, not the whole new image %s", got, goldenEFIImage)
	}
}

func TestEFIImageRefusesSHA256SUMSMissingAnArtifact(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	art := filepath.Join(f.home, "build", "artifacts")
	var names []string
	for _, n := range ShipNames() {
		if n != "OpenRuntime.efi" {
			names = append(names, n)
		}
	}
	if err := WriteSums(art, names); err != nil {
		t.Fatal(err)
	}
	_, err := f.efiImage("")
	if err == nil || !strings.Contains(err.Error(), "OpenRuntime.efi") || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v", err)
	}
}

func TestEFIImageValidatesWithAnOcvalidateOnPATH(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	f.fake.Paths["ocvalidate"] = "/usr/local/bin/ocvalidate"
	if _, err := f.efiImage("MacPro5,1"); err != nil {
		t.Fatal(err)
	}
	derived := filepath.Join(f.home, "build", "config", "config-MacPro5,1.plist")
	cs := f.calls("/usr/local/bin/ocvalidate")
	if len(cs) != 1 || !reflect.DeepEqual(cs[0].Args, []string{derived}) {
		t.Errorf("ocvalidate calls: %v", cs)
	}
	if !strings.Contains(f.log.String(), "ocvalidate accepts the derived config") {
		t.Errorf("log:\n%s", f.log.String())
	}
}
