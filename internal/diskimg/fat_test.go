package diskimg

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"testing"
)

// The geometry table MEASURED against mformat (mtools 4.0.43) on
// 2026-09-25: filesystem sectors -> sectors per cluster, FAT sectors.
// tools_test.go checks it against mformat's own images.
var mformatGeometry = []struct{ sectors, spc, fatSectors uint32 }{
	{79839, 1, 614}, {96223, 1, 740}, {128991, 1, 993}, {202719, 2, 786},
	{391135, 4, 761}, {612319, 8, 597}, {1046495, 8, 1020},
	{2095071, 8, 2042}, {4192223, 8, 4086},
}

func TestGeometryIsMformats(t *testing.T) {
	for _, m := range mformatGeometry {
		g, err := FAT32Geometry(m.sectors, AlignLBA)
		if err != nil {
			t.Fatal(err)
		}
		if g.SectorsPerCluster != m.spc || g.FATSectors != m.fatSectors || g.ReservedSectors != 32 || g.FATs != 2 {
			t.Errorf("%d sectors: got %+v, mformat chose %d/cluster and %d FAT sectors", m.sectors, g, m.spc, m.fatSectors)
		}
	}
}

func TestFAT32GeometryRefusesATinyFilesystem(t *testing.T) {
	if _, err := FAT32Geometry(20*2048, AlignLBA); err == nil || !strings.Contains(err.Error(), "65525") {
		t.Fatalf("err = %v", err)
	}
}

// build writes f into a fresh in-memory filesystem of mib MiB (the whole
// buffer is the filesystem, as a partition is) and opens it for reading.
func build(t *testing.T, f *FAT, mib uint32) (image, *FATReader) {
	t.Helper()
	sectors := mib * 2048
	g, err := FAT32Geometry(sectors, 0)
	if err != nil {
		t.Fatal(err)
	}
	img := make(image, int(sectors)*SectorSize)
	if err := f.WriteTo(img, 0, g); err != nil {
		t.Fatal(err)
	}
	r, err := OpenFAT(img, 0)
	if err != nil {
		t.Fatal(err)
	}
	return img, r
}

func mustFAT(t *testing.T) *FAT {
	t.Helper()
	f, err := NewFAT("EFI", 0x5641564D)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFATRoundTrip(t *testing.T) {
	f := mustFAT(t)
	big := bytes.Repeat([]byte("0123456789abcdef"), 1000) // several clusters
	for _, step := range []error{
		f.Mkdir("/EFI"), f.Mkdir("/EFI/OC"), f.Mkdir("/EFI/BOOT"),
		f.WriteFile("/EFI/BOOT/BOOTx64.efi", []byte("boot")),
		f.WriteFile("/EFI/OC/OpenCore.efi", big),
		f.WriteFile("/EFI/OC/empty", nil),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	_, r := build(t, f, 48)
	if r.Label != "EFI" || r.Serial != 0x5641564D || r.OEM != "MSWIN4.1" {
		t.Fatalf("label %q serial %x oem %q", r.Label, r.Serial, r.OEM)
	}
	if b, err := r.ReadFile("/EFI/OC/OpenCore.efi"); err != nil || !bytes.Equal(b, big) {
		t.Fatalf("OpenCore.efi: %v", err)
	}
	if b, err := r.ReadFile("/efi/boot/bootx64.EFI"); err != nil || string(b) != "boot" {
		t.Fatalf("names must match case-insensitively: %q %v", b, err)
	}
	if b, err := r.ReadFile("/EFI/OC/empty"); err != nil || len(b) != 0 {
		t.Fatalf("empty: %q %v", b, err)
	}
	es, err := r.ReadDir("/EFI")
	if err != nil || len(es) != 2 || es[0].Name != "OC" || es[1].Name != "BOOT" || !es[0].Dir {
		t.Fatalf("ReadDir keeps the order things were added in: %+v %v", es, err)
	}
}

func TestFATNestedDirectoriesAndLongDirectories(t *testing.T) {
	f := mustFAT(t)
	for _, d := range []string{"/a", "/a/b", "/a/b/c", "/a/b/c/Contents"} {
		if err := f.Mkdir(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.WriteFile("/a/b/c/Contents/Info.plist", []byte("plist")); err != nil {
		t.Fatal(err)
	}
	// 300 long names: many more entries than one 512-byte cluster holds.
	for i := 0; i < 300; i++ {
		if err := f.WriteFile(fmt.Sprintf("/a/A long file name number %03d.txt", i), []byte(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	_, r := build(t, f, 40) // 1 sector per cluster: the directory spans many
	if b, err := r.ReadFile("/a/b/c/Contents/Info.plist"); err != nil || string(b) != "plist" {
		t.Fatal(err)
	}
	for _, i := range []int{0, 150, 299} {
		b, err := r.ReadFile(fmt.Sprintf("/a/A long file name number %03d.txt", i))
		if err != nil || string(b) != fmt.Sprint(i) {
			t.Fatalf("%d: %q %v", i, b, err)
		}
	}
	var n int
	if err := r.Walk(func(string, DirEntry) error { n++; return nil }); err != nil || n != 305 {
		t.Fatalf("walked %d entries, err %v", n, err)
	}
}

func TestShortNames(t *testing.T) {
	cases := []struct {
		name, short string
		lfn         bool
	}{
		{"EFI", "EFI        ", false},
		{"ACPI", "ACPI       ", false},
		{"BOOTx64.efi", "BOOTX64 EFI", true},
		{"OpenCore.efi", "OPENCOREEFI", true},
		{"OpenRuntime.efi", "OPENRU~1EFI", true},
		{"config.plist", "CONFIG~1PLI", true},
		{"Info.plist", "INFO~1  PLI", true},
		{"Lilu.kext", "LILU~1  KEX", true},
		{"Resources", "RESOUR~1   ", true},
		{"a+b.txt", "A_B~1   TXT", true},
		{".hidden", "HIDDEN~1   ", true},
	}
	for _, c := range cases {
		s, lfn, err := shortName(c.name, map[[11]byte]bool{})
		if err != nil || string(s[:]) != c.short || lfn != c.lfn {
			t.Errorf("%q: got %q lfn=%v err=%v, want %q lfn=%v", c.name, s, lfn, err, c.short, c.lfn)
		}
	}
	taken := map[[11]byte]bool{}
	var got []string
	for _, n := range []string{"OpenRuntime.efi", "OpenRuntime2.efi", "OpenRuntimeX.efi"} {
		s, _, _ := shortName(n, taken)
		taken[s] = true
		got = append(got, string(s[:]))
	}
	if strings.Join(got, ",") != "OPENRU~1EFI,OPENRU~2EFI,OPENRU~3EFI" {
		t.Fatalf("collisions: %v", got)
	}
	for i := 1; i <= 9; i++ {
		taken[pack83(fmt.Sprintf("ABCDEF~%d", i), "")] = true
	}
	if s, _, _ := shortName("abcdefghij", taken); string(s[:]) != "ABCDE~10   " {
		t.Fatalf("a two-digit tail shortens the basis: %q", s)
	}
}

func TestLFNChecksumKnownAnswers(t *testing.T) {
	for short, want := range map[string]byte{"OPENRU~1EFI": 0x84, "CONFIG~1PLI": 0xEF, "BOOTX64 EFI": 0x1D} {
		var s [11]byte
		copy(s[:], short)
		if got := lfnChecksum(s); got != want {
			t.Errorf("%s: %#x, want %#x", short, got, want)
		}
	}
}

func TestFATRefusesWhatItCannotHold(t *testing.T) {
	f := mustFAT(t)
	if err := f.Mkdir("/EFI"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"a duplicate":                  f.Mkdir("/EFI"),
		"a duplicate but for its case": f.Mkdir("/efi"),
		"a missing parent":             f.WriteFile("/nope/x", nil),
		"a file as a parent":           func() error { f.WriteFile("/f", nil); return f.WriteFile("/f/x", nil) }(),
		"a colon":                      f.WriteFile("/a:b", nil),
		"a star":                       f.WriteFile("/a*", nil),
		"a trailing dot":               f.WriteFile("/trailing.", nil),
		"a control character":          f.WriteFile("/x\x01", nil),
		"dot-dot":                      f.Mkdir("/EFI/.."),
	} {
		if err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := NewFAT("A LABEL TOO LONG", 0); err == nil {
		t.Error("a label over 11 characters must be refused")
	}
}

func TestFATRefusesWhatDoesNotFit(t *testing.T) {
	f := mustFAT(t)
	if err := f.WriteFile("/big", make([]byte, 45<<20)); err != nil {
		t.Fatal(err)
	}
	g, _ := FAT32Geometry(40*2048, 0)
	err := f.WriteTo(make(image, 40<<20), 0, g)
	if err == nil || !strings.Contains(err.Error(), "clusters") {
		t.Fatalf("err = %v", err)
	}
}

func TestFATIsDeterministic(t *testing.T) {
	make1 := func() image {
		f := mustFAT(t)
		f.Mkdir("/EFI")
		f.WriteFile("/EFI/config.plist", []byte("<plist/>"))
		img, _ := build(t, f, 40)
		return img
	}
	if !bytes.Equal(make1(), make1()) {
		t.Fatal("the same calls must give the same bytes")
	}
}

// The filesystem must stay inside its partition, off the backup GPT at
// the end of the disk: mformat needs -T to promise that; here the
// promise is that WriteTo writes nothing past g.Sectors.
func TestFATStaysInsideItsPartition(t *testing.T) {
	const sectors = 40 * 2048
	g, err := FAT32Geometry(sectors, 0)
	if err != nil {
		t.Fatal(err)
	}
	if end := g.ReservedSectors + g.FATs*g.FATSectors + g.Clusters*g.SectorsPerCluster; end > sectors {
		t.Fatalf("the layout ends at sector %d of %d", end, sectors)
	}
	img := make(image, sectors*SectorSize+4096)
	for i := sectors * SectorSize; i < len(img); i++ {
		img[i] = 0xAB
	}
	f := mustFAT(t)
	f.WriteFile("/fill", make([]byte, 30<<20))
	if err := f.WriteTo(img, 0, g); err != nil {
		t.Fatal(err)
	}
	for i := sectors * SectorSize; i < len(img); i++ {
		if img[i] != 0xAB {
			t.Fatalf("byte %d past the filesystem was written", i)
		}
	}
}

func TestFAT32GeometryRefusesTooManyClusters(t *testing.T) {
	// 2 TiB at 8 sectors a cluster is more clusters than a 28-bit FAT
	// entry can number (0x0FFFFFF5).
	if _, err := FAT32Geometry(0xFFFFFFFF, 0); err == nil || !strings.Contains(err.Error(), "clusters") {
		t.Fatalf("err = %v", err)
	}
}

// What an empty filesystem holds besides its boot sector: MEASURED
// 2026-09-25 on a fresh mformat -F (mtools 4.0.43) of 40 MiB. FAT[1] is
// all ones; FSInfo's "next free" is the last
// cluster allocated (the root's, 2), as mtools and the Linux driver read
// it; and there is no backup FSInfo at sector 7. tools_test.go compares
// the whole of it with mformat's.
func TestAnEmptyFATIsMformats(t *testing.T) {
	img, r := build(t, mustFAT(t), 40)
	fat := img[32*SectorSize:]
	if !bytes.Equal(fat[:12], []byte{0xF8, 0xFF, 0xFF, 0x0F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F}) {
		t.Errorf("FAT[0..2] = % X", fat[:12])
	}
	info := img[SectorSize : 2*SectorSize]
	if free, next := le32(info[488:]), le32(info[492:]); free != r.G.Clusters-1 || next != 2 {
		t.Errorf("FSInfo free %d next %d; want %d and 2", free, next, r.G.Clusters-1)
	}
	for s := 2; s < 32; s++ {
		if s != 6 && bytes.Count(img[s*SectorSize:(s+1)*SectorSize], []byte{0}) != SectorSize {
			t.Errorf("reserved sector %d is not empty", s)
		}
	}
	if !bytes.Equal(img[:SectorSize], img[6*SectorSize:7*SectorSize]) {
		t.Error("sector 6 is not a copy of the boot sector")
	}
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// The reader's rules, on a root directory written by hand: deleted
// entries, orphaned or mismatched long names, mtools' lower-case flags,
// 0x05 for 0xE5, and nothing read past the end marker.
func TestReadDirFollowsTheRules(t *testing.T) {
	img, r := build(t, mustFAT(t), 40)
	short := func(base, ext string) [11]byte { return pack83(base, ext) }
	flagged := func(e []byte, flags byte) []byte { e[12] = flags; return e }
	deleted := func(e []byte) []byte { e[0] = 0xE5; return e }
	var d [][]byte
	d = append(d, dirent(short("EFI", ""), attrVolumeID, 0, 0))
	d = append(d, deleted(dirent(short("GONE", "TXT"), attrArchive, 0, 0)))
	d = append(d, lfnEntries("Real name.txt", short("REALNA~1", "TXT"))...)
	d = append(d, deleted(dirent(short("X", ""), attrArchive, 0, 0))) // orphans the long name
	d = append(d, dirent(short("REALNA~1", "TXT"), attrArchive, 0, 0))
	d = append(d, flagged(dirent(short("LOWER", "TXT"), attrArchive, 0, 0), 0x18))
	d = append(d, flagged(dirent(short("BASE", "TXT"), attrArchive, 0, 0), 0x08))
	d = append(d, dirent([11]byte{0x05, 'B', 'C', ' ', ' ', ' ', ' ', ' ', 'T', 'X', 'T'}, attrArchive, 0, 0))
	d = append(d, lfnEntries("Wrong checksum.txt", short("OTHER", "TXT"))...)
	d = append(d, dirent(short("WRONG", "TXT"), attrArchive, 0, 0))
	d = append(d, lfnEntries("Good long name.txt", short("GOODLO~1", "TXT"))...)
	d = append(d, dirent(short("GOODLO~1", "TXT"), attrArchive, 0, 0))
	d = append(d, dirent(short("SUB", ""), attrDir, 0, 0))
	d = append(d, make([]byte, dirEntrySize)) // the end
	d = append(d, dirent(short("AFTER", "TXT"), attrArchive, 0, 0))
	at := r.G.clusterOffset(rootCluster)
	for _, e := range d {
		at += int64(copy(img[at:], e))
	}
	es, err := r.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, fmt.Sprintf("%s|%s|%v", e.Name, e.Short, e.Dir))
	}
	want := []string{
		"REALNA~1.TXT|REALNA~1.TXT|false",
		"lower.txt|lower.txt|false",
		"base.TXT|base.TXT|false",
		"\xE5BC.TXT|\xE5BC.TXT|false",
		"WRONG.TXT|WRONG.TXT|false",
		"Good long name.txt|GOODLO~1.TXT|false",
		"SUB|SUB|true",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if b, err := r.ReadFile("/goodlo~1.txt"); err != nil || len(b) != 0 {
		t.Fatalf("a short name finds its entry too: %q %v", b, err)
	}
}

// The reader refuses a broken filesystem rather than looping or reading
// past it.
func TestFATReaderRefusesBrokenChains(t *testing.T) {
	fresh := func(t *testing.T) (image, *FATReader, uint32) {
		f := mustFAT(t)
		if err := f.Mkdir("/D"); err != nil {
			t.Fatal(err)
		}
		if err := f.WriteFile("/F", make([]byte, 3*SectorSize)); err != nil {
			t.Fatal(err)
		}
		img, r := build(t, f, 40)
		es, err := r.ReadDir("/")
		if err != nil || len(es) != 2 {
			t.Fatalf("%+v %v", es, err)
		}
		return img, r, es[1].Cluster
	}
	setFAT := func(img image, c, v uint32) {
		for i := 0; i < 4; i++ {
			img[32*SectorSize+int(c)*4+i] = byte(v >> (8 * i))
		}
	}
	for name, c := range map[string]struct {
		patch func(img image, r *FATReader, first uint32)
		want  string
	}{
		"a loop": {func(img image, _ *FATReader, c uint32) { setFAT(img, c+2, c) }, "loop"},
		// /F's last entry freed: the three clusters still hold its size,
		// so only the chain itself can tell that it is cut.
		"a free entry ending the chain": {func(img image, _ *FATReader, c uint32) { setFAT(img, c+2, 0) }, "free"},
		"a cluster past the end":        {func(img image, r *FATReader, c uint32) { setFAT(img, c, r.G.Clusters+2) }, "cluster"},
		"a size past the chain": {func(img image, r *FATReader, _ uint32) {
			img[r.G.clusterOffset(rootCluster)+2*dirEntrySize+28] = 0xFF // /F's size (slot 2): 0x6FF, more than 3 clusters
			img[r.G.clusterOffset(rootCluster)+2*dirEntrySize+29] = 0x06
		}, "size"},
	} {
		t.Run(name, func(t *testing.T) {
			img, r, first := fresh(t)
			c.patch(img, r, first)
			r, err := OpenFAT(img, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.ReadFile("/f"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}

	// /D's one cluster, its chain cut by a free entry: an error, not
	// a directory listed as far as the cut.
	{
		img, r, _ := fresh(t)
		es, _ := r.ReadDir("/")
		setFAT(img, es[0].Cluster, 0)
		r, err := OpenFAT(img, 0)
		if err != nil {
			t.Fatal(err)
		}
		if es, err := r.ReadDir("/D"); err == nil || es != nil || !strings.Contains(err.Error(), "free") {
			t.Errorf("a directory whose chain is cut: %+v, %v", es, err)
		}
		if err := r.Walk(func(string, DirEntry) error { return nil }); err == nil || !strings.Contains(err.Error(), "free") {
			t.Errorf("Walk over a cut directory: %v", err)
		}
	}

	img, r, _ := fresh(t)
	if _, err := r.ReadFile("/d"); err == nil {
		t.Error("ReadFile of a directory must fail")
	}
	if _, err := r.ReadFile("/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	// /D's entry (no long name, so slot 1) made to point back at the root: a directory loop.
	e := img[r.G.clusterOffset(rootCluster)+dirEntrySize:]
	e[26], e[27], e[20], e[21] = rootCluster, 0, 0, 0
	if err := r.Walk(func(string, DirEntry) error { return nil }); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Errorf("Walk over a directory loop: %v", err)
	}
	if _, err := OpenFAT(make(image, 40<<20), 0); err == nil {
		t.Error("OpenFAT of zeros must fail")
	}
}

// The boot sector's heads are mformat's LBA-assisted translation of the
// size it formats: 16 heads below 1024 cylinders of 16 heads of 63
// sectors, then 32, 64, 128 and 255. MEASURED 2026-09-25 by bisecting
// mformat -F (mtools 4.0.43) on devices of these sizes; the facts file's
// "heads 16" holds only up to 504 MiB.
func TestHeadsAreMformats(t *testing.T) {
	for _, c := range []struct {
		sectors uint32
		heads   uint16
	}{
		{79839, 16}, {1032191, 16}, {1032192, 32}, {2064383, 32}, {2064384, 64},
		{4128767, 64}, {4128768, 128}, {8257535, 128}, {8257536, 255}, {16000000, 255},
	} {
		b := bootSector(Geometry{Sectors: c.sectors, SectorsPerCluster: 8, ReservedSectors: 32, FATs: 2}, pack83("EFI", ""), 0)
		if got := uint16(b[26]) | uint16(b[27])<<8; got != c.heads {
			t.Errorf("%d sectors: %d heads, mformat writes %d", c.sectors, got, c.heads)
		}
	}
}

// WriteTo refuses a Geometry that FAT32Geometry would not have made,
// rather than panicking on it or writing past the filesystem.
func TestFATWriteToRefusesABadGeometry(t *testing.T) {
	good, err := FAT32Geometry(40*2048, 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]Geometry{
		"a zero geometry":             {},
		"a FAT too small":             func() Geometry { g := good; g.FATSectors = 1; return g }(),
		"more clusters than it holds": func() Geometry { g := good; g.Clusters += 100; return g }(),
		"no reserved sectors":         func() Geometry { g := good; g.ReservedSectors = 0; return g }(),
		// Within a uint32 end to end, but more clusters than a 28-bit
		// entry numbers.
		"more clusters than FAT32 numbers": {Sectors: 0xFFFFFFFF, SectorsPerCluster: 1, ReservedSectors: 32, FATs: 1,
			FATSectors: 0x200000, Clusters: fat32MaxClusters + 1},
		// FATSectors*512 wraps a uint32 to 0.
		"a FAT whose size wraps": {Sectors: 0xFFFFFFFF, SectorsPerCluster: 1, ReservedSectors: 32, FATs: 2,
			FATSectors: 0x800000, Clusters: 1000},
		// The boot sector holds these in 16 and 8 bits.
		"reserved sectors past 16 bits":        func() Geometry { g := good; g.ReservedSectors = 0x10020; g.Sectors += 0x10000; return g }(),
		"256 sectors per cluster":              func() Geometry { g := good; g.SectorsPerCluster = 256; g.Clusters /= 256; return g }(),
		"sectors per cluster not a power of 2": func() Geometry { g := good; g.SectorsPerCluster = 3; g.Clusters /= 3; return g }(),
		"256 FATs":                             func() Geometry { g := good; g.FATs = 256; g.FATSectors = 1; g.Clusters = 100; return g }(),
	} {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panicked: %v", p)
				}
			}()
			return mustFAT(t).WriteTo(make(image, 41<<20), 0, g)
		}()
		if err == nil || !strings.Contains(err.Error(), "geometry") {
			t.Errorf("%s: err = %v, want Geometry.valid's refusal", name, err)
		}
	}
}

// OpenFAT refuses a boot sector whose numbers would have it allocate
// gigabytes, or overflow into a panic, before it allocates anything
// large: a FAT that a 28-bit entry cannot number, or one the reader does
// not hold.
func TestOpenFATRefusesACraftedBootSector(t *testing.T) {
	craft := func(size int, spc byte, fats byte, fatSectors, clusters uint32) []byte {
		b := make([]byte, size)
		copy(b[3:11], "CRAFTED ")
		b[11], b[12] = 0x00, 0x02 // 512 bytes per sector
		b[13] = spc
		b[14], b[15] = 32, 0 // reserved
		b[16] = fats
		put := func(at int, v uint32) {
			for i := 0; i < 4; i++ {
				b[at+i] = byte(v >> (8 * i))
			}
		}
		put(32, 32+uint32(fats)*fatSectors+clusters*uint32(spc))
		put(36, fatSectors)
		put(44, rootCluster)
		b[510], b[511] = 0x55, 0xAA
		return b
	}
	for name, c := range map[string]struct {
		img  []byte
		want string
	}{
		// Clusters+2 is 0x40000000: times four, it wraps a uint32 to 0.
		"a FAT whose size wraps, on 20 KiB": {craft(20<<10, 1, 1, 0x800001, 0x3FFFFFFE), "clusters"},
		// Within FAT32's numbering, but a 1 GiB FAT on 4 KiB of input.
		"a FAT the input does not hold, on 4 KiB": {craft(4<<10, 1, 1, 0x200000, 0x0FFFFFF0), "FAT"},
	} {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("panicked: %v", p)
					}
				}()
				_, err = OpenFAT(bytes.NewReader(c.img), 0)
				return err
			}()
			runtime.ReadMemStats(&after)
			if err == nil || strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one naming %q", err, c.want)
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
				t.Errorf("allocated %d MiB on the way", grew>>20)
			}
		})
	}
}

// A file's size is 32 bits in its directory entry.
func TestWriteFileRefusesMoreThanADirectoryEntrySizes(t *testing.T) {
	if err := checkFileSize(1 << 32); err == nil || !strings.Contains(err.Error(), "4294967295") {
		t.Errorf("4 GiB: %v", err)
	}
	if err := checkFileSize(1<<32 - 1); err != nil {
		t.Errorf("4 GiB less a byte: %v", err)
	}
	// And the largest size it takes must not wrap when rounded up to
	// whole clusters.
	if got := clustersFor(0xFFFFFFFF, 4096); got != 0x100000 {
		t.Errorf("0xFFFFFFFF bytes in 4 KiB clusters: %#x clusters, want 0x100000", got)
	}
	if got := clustersFor(0, 512); got != 0 {
		t.Errorf("an empty file: %d clusters", got)
	}
}
