package diskimg

// Agreement with the tools diskimg does without: sgdisk and mtools -- live
// where a test runs them, and otherwise against reference images they
// made (testdata/golden/README.md) -- with fsck.fat as the judge of a
// well-formed filesystem. Each test skips when a tool it runs is absent.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// golden reads and gunzips a reference image from
// testdata/golden/<name>.gz.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join(repo(t), "internal", "diskimg", "testdata", "golden", name+".gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// repo is the repository root, found from this file's location, as
// internal/firmware's tests find it.
func repo(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// need skips t unless every tool is on PATH.
func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
}

// mtools runs an mtools command, which refuses a geometry it thinks odd
// unless told not to check.
func mtools(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// fsck runs fsck.fat -n -v on a filesystem, which it takes as a whole
// file: fs is the filesystem's bytes, from its first sector to its last.
func fsck(t *testing.T, fs io.Reader) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fs.img")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, fs); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("fsck.fat", "-n", "-v", path).CombinedOutput()
	if err != nil || bytes.Contains(out, []byte("differ")) || bytes.Contains(out, []byte("Bad")) {
		t.Fatalf("fsck.fat -n -v: %v\n%s", err, out)
	}
}

// diskImage makes a GPT disk of sectors sectors in a file, with one EFI
// system partition from AlignLBA to the last usable LBA, and f written
// into it. It returns the file's path and the partition's geometry.
func diskImage(t *testing.T, f *FAT, sectors uint64) (string, Geometry) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disk.img")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(int64(sectors) * SectorSize); err != nil {
		t.Fatal(err)
	}
	if err := WriteGPT(file, sectors, DerivedGUID("tools disk"), espOn(sectors)); err != nil {
		t.Fatal(err)
	}
	g, err := FAT32Geometry(uint32(LastUsableLBA(sectors)-AlignLBA+1), AlignLBA)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteTo(file, AlignLBA*SectorSize, g); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path, g
}

// mformat's layout: sgdisk partitioned and mformat formatted a disk of
// each size (testdata/golden/efi-<MiB>.img.gz), and what mformat wrote
// must be what FAT32Geometry lays out for the same partition.
// HiddenSectors is not compared: mformat writes 0, and diskimg the
// partition's start LBA, as the FAT specification says.
//
// Beyond the numbers, the bytes: an empty diskimg filesystem must be
// mformat's, byte for byte through its root directory, but for diskimg's
// deliberate differences, which are masked below:
//   - the OEM name (MTOO4043; Go's MSWIN4.1);
//   - hidden sectors (0; Go's the partition's LBA);
//   - the boot code (mformat's stub, and the partition entry it puts in
//     the boot sector; Go writes none);
//   - timestamps (mformat's now; Go's 1980-01-01), here the volume
//     label's.
//
// The serial number differs deliberately too (mformat's random; Go's
// fixed): the Go filesystem is given mformat's serial, so every other
// byte of the extended BPB is compared. Short-name tails, the last
// deliberate difference, show only once there are files, and mformat
// makes none.
// Heads are compared: mformat's LBA-assisted translation of the device
// it is handed differs from Go's of the partition only when the
// filesystem ends within 33 sectors of a translation boundary, which no
// size here does (lbaAssistHeads).
func TestGeometryMatchesMformat(t *testing.T) {
	for _, mib := range []int{40, 48, 64, 100, 192, 300, 512} {
		t.Run(fmt.Sprintf("%dMiB", mib), func(t *testing.T) {
			t.Parallel()
			img := filepath.Join(t.TempDir(), "efi.img")
			if err := os.WriteFile(img, golden(t, fmt.Sprintf("efi-%d.img", mib)), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(img)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, parts, err := ReadGPT(f, uint64(mib)*2048)
			if err != nil || len(parts) != 1 {
				t.Fatalf("%+v %v", parts, err)
			}
			part := uint32(parts[0].LastLBA - parts[0].FirstLBA + 1)
			r, err := OpenFAT(f, AlignLBA*SectorSize)
			if err != nil {
				t.Fatal(err)
			}
			want, err := FAT32Geometry(part, AlignLBA)
			if err != nil {
				t.Fatal(err)
			}
			got := r.G
			got.HiddenSectors = want.HiddenSectors // 0 from mformat, the LBA from Go
			if got != want || got.Sectors != part {
				t.Fatalf("mformat laid out %+v; FAT32Geometry %+v; the partition has %d sectors", r.G, want, part)
			}

			// The bytes, through the root directory's first cluster.
			n := int64(want.ReservedSectors+want.FATs*want.FATSectors+want.SectorsPerCluster) * SectorSize
			theirs := make([]byte, n)
			if _, err := f.ReadAt(theirs, AlignLBA*SectorSize); err != nil {
				t.Fatal(err)
			}
			empty, err := NewFAT("EFI", r.Serial)
			if err != nil {
				t.Fatal(err)
			}
			ours := make(image, want.Sectors*SectorSize)
			if err := empty.WriteTo(ours, 0, want); err != nil {
				t.Fatal(err)
			}
			ours = ours[:n]
			root := want.clusterOffset(rootCluster)
			for _, img := range [][]byte{theirs, ours} {
				for _, boot := range []int{0, backupBootSector * SectorSize} {
					clear(img[boot+3 : boot+11])   // OEM name: MTOO4043, MSWIN4.1
					clear(img[boot+28 : boot+32])  // hidden sectors: 0, the partition's LBA
					clear(img[boot+90 : boot+510]) // boot code: mformat's stub, none
				}
				clear(img[root+13 : root+20]) // the label's creation and access times
				clear(img[root+22 : root+26]) // its write time
			}
			if !bytes.Equal(theirs, ours) {
				for i := range theirs {
					if theirs[i] != ours[i] {
						t.Fatalf("byte %d (sector %d) is %#02x; mformat's is %#02x", i, i/SectorSize, ours[i], theirs[i])
					}
				}
			}
		})
	}
}

// fsck.fat finds nothing wrong with what Go writes: nested directories,
// a directory of hundreds of long names spanning many clusters, short
// names that collide, an empty file and a file of many clusters.
func TestFsckAcceptsTheGoFilesystem(t *testing.T) {
	need(t, "fsck.fat")
	f := mustFAT(t)
	for _, d := range []string{"/a", "/a/b", "/a/b/c", "/a/b/c/Contents"} {
		if err := f.Mkdir(d); err != nil {
			t.Fatal(err)
		}
	}
	add := func(path string, data []byte) {
		if err := f.WriteFile(path, data); err != nil {
			t.Fatal(err)
		}
	}
	add("/a/b/c/Contents/Info.plist", []byte("plist"))
	for i := 0; i < 300; i++ {
		add(fmt.Sprintf("/a/A long file name number %03d.txt", i), []byte(fmt.Sprint(i)))
	}
	for _, n := range []string{"OpenRuntime.efi", "OpenRuntime2.efi", "OpenRuntimeX.efi"} {
		add("/"+n, []byte(n))
	}
	add("/empty", nil)
	add("/a/b/big.bin", bytes.Repeat([]byte{0x5A}, 100<<10))
	img, _ := build(t, f, 48)
	fsck(t, bytes.NewReader(img))
}

// mtools reads a whole Go-built disk -- GPT and FAT32 -- by its long
// names, and gets back the bytes Go put in; sgdisk and fsck.fat find
// nothing wrong with it.
func TestMtoolsReadsTheGoImage(t *testing.T) {
	need(t, "mdir", "mcopy")
	f := mustFAT(t)
	files := map[string][]byte{
		"/EFI/OC/Drivers/OpenRuntime.efi":             bytes.Repeat([]byte("runtime "), 5000),
		"/EFI/OC/config.plist":                        []byte("<plist version=\"1.0\"><dict/></plist>\n"),
		"/EFI/OC/Kexts/Lilu.kext/Contents/MacOS/Lilu": []byte("not really a kext"),
	}
	for _, d := range []string{"/EFI", "/EFI/OC", "/EFI/OC/Drivers", "/EFI/OC/Kexts",
		"/EFI/OC/Kexts/Lilu.kext", "/EFI/OC/Kexts/Lilu.kext/Contents", "/EFI/OC/Kexts/Lilu.kext/Contents/MacOS"} {
		if err := f.Mkdir(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"/EFI/OC/Drivers/OpenRuntime.efi", "/EFI/OC/config.plist", "/EFI/OC/Kexts/Lilu.kext/Contents/MacOS/Lilu"} {
		if err := f.WriteFile(p, files[p]); err != nil {
			t.Fatal(err)
		}
	}
	img, g := diskImage(t, f, mib192)
	at := fmt.Sprintf("%s@@%d", img, AlignLBA*SectorSize)

	listing := mtools(t, "mdir", "-b", "-/", "-i", at, "::")
	for p := range files {
		if !strings.Contains(listing, "::"+p+"\n") {
			t.Errorf("mdir -b -/ does not list ::%s:\n%s", p, listing)
		}
	}
	for p, want := range files {
		out := filepath.Join(t.TempDir(), "out")
		mtools(t, "mcopy", "-n", "-i", at, "::"+p, out)
		if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, want) {
			t.Errorf("mcopy ::%s gave %d bytes, err %v; want %d", p, len(got), err, len(want))
		}
	}

	if _, err := exec.LookPath("sgdisk"); err == nil {
		out, err := exec.Command("sgdisk", "-v", img).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "No problems found") {
			t.Errorf("sgdisk -v: %v\n%s", err, out)
		}
	}
	if _, err := exec.LookPath("fsck.fat"); err == nil {
		disk, err := os.Open(img)
		if err != nil {
			t.Fatal(err)
		}
		defer disk.Close()
		fsck(t, io.NewSectionReader(disk, AlignLBA*SectorSize, int64(g.Sectors)*SectorSize))
	}
}

// Go reads what mtools writes: long names, mtools' own short names and
// lower-case flags, a nested bundle. img is the image mmd and mcopy wrote
// these same source files into (testdata/golden/mtools-image.img.gz).
func TestGoReadsAnMtoolsImage(t *testing.T) {
	dir := t.TempDir()
	src := map[string][]byte{
		"OpenPartitionDxe.efi":          bytes.Repeat([]byte("partition "), 3000),
		"lower.txt":                     []byte("mtools keeps this name's case in flags, not a long name\n"),
		"Lilu.kext/Contents/Info.plist": []byte("<plist/>\n"),
		"Lilu.kext/Contents/MacOS/Lilu": []byte("lilu"),
	}
	img := filepath.Join(dir, "img")
	if err := os.WriteFile(img, golden(t, "mtools-image.img"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := OpenFAT(f, AlignLBA*SectorSize)
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range src {
		got, err := r.ReadFile("/EFI/" + rel)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("/EFI/%s: %d bytes, err %v; want %d", rel, len(got), err, len(want))
		}
	}
	var walked []string
	if err := r.Walk(func(p string, _ DirEntry) error { walked = append(walked, p); return nil }); err != nil {
		t.Fatal(err)
	}
	sort.Strings(walked)
	want := []string{
		"/EFI",
		"/EFI/Lilu.kext",
		"/EFI/Lilu.kext/Contents",
		"/EFI/Lilu.kext/Contents/Info.plist",
		"/EFI/Lilu.kext/Contents/MacOS",
		"/EFI/Lilu.kext/Contents/MacOS/Lilu",
		"/EFI/OpenPartitionDxe.efi",
		"/EFI/lower.txt",
	}
	if strings.Join(walked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Walk visited\n%s\nwant\n%s", strings.Join(walked, "\n"), strings.Join(want, "\n"))
	}
}
