package diskimg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const mib192 = 192 * 1024 * 1024 / SectorSize // 393216 sectors

func espOn(sectors uint64) []Partition {
	return []Partition{{Type: TypeEFISystem, GUID: DerivedGUID("test esp"), Name: "EFI",
		FirstLBA: AlignLBA, LastLBA: LastUsableLBA(sectors)}}
}

// image is an in-memory disk of n sectors. Like a file, it says so when
// a write or a read runs past its end.
type image []byte

func (m image) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("image: negative offset")
	}
	if off >= int64(len(m)) {
		return 0, io.ErrShortWrite
	}
	if n := copy(m[off:], p); n < len(p) {
		return n, io.ErrShortWrite
	}
	return len(p), nil
}

func (m image) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("image: negative offset")
	}
	if off >= int64(len(m)) {
		return 0, io.EOF
	}
	if n := copy(p, m[off:]); n < len(p) {
		return n, io.EOF
	}
	return len(p), nil
}

// The in-memory disk keeps io.WriterAt's and io.ReaderAt's promises, so
// that a test cannot pass by writing or reading past its end unnoticed.
func TestImageHonoursWriterAtAndReaderAt(t *testing.T) {
	m := make(image, 8)
	if n, err := m.WriteAt([]byte("abcd"), 6); n != 2 || err != io.ErrShortWrite {
		t.Errorf("a write across the end: %d, %v", n, err)
	}
	if n, err := m.WriteAt([]byte("x"), 100); n != 0 || err != io.ErrShortWrite {
		t.Errorf("a write past the end: %d, %v", n, err)
	}
	buf := make([]byte, 4)
	if n, err := m.ReadAt(buf, 6); n != 2 || err != io.EOF || string(buf[:2]) != "ab" {
		t.Errorf("a read across the end: %d, %v, %q", n, err, buf[:n])
	}
	if n, err := m.ReadAt(buf, 100); n != 0 || err != io.EOF {
		t.Errorf("a read past the end: %d, %v", n, err)
	}
	if _, err := m.ReadAt(buf, -1); err == nil {
		t.Error("a negative offset must fail")
	}
	if _, err := m.WriteAt(buf, -1); err == nil {
		t.Error("a negative offset must fail")
	}
	if n, err := m.ReadAt(buf, 0); n != 4 || err != nil {
		t.Errorf("a read inside: %d, %v", n, err)
	}
}

func TestGUIDTextRoundTrips(t *testing.T) {
	g := MustGUID("C12A7328-F81F-11D2-BA4B-00A0C93EC93B")
	if g.String() != "C12A7328-F81F-11D2-BA4B-00A0C93EC93B" {
		t.Fatal(g.String())
	}
	// On disk, the first three fields are little-endian.
	if !bytes.Equal(g[:8], []byte{0x28, 0x73, 0x2A, 0xC1, 0x1F, 0xF8, 0xD2, 0x11}) {
		t.Fatalf("% X", g[:8])
	}
	if _, err := ParseGUID("C12A7328F81F11D2BA4B00A0C93EC93B"); err == nil {
		t.Fatal("a GUID without dashes must be refused")
	}
}

func TestDerivedGUIDsAreStableAndVersion4(t *testing.T) {
	a, b := DerivedGUID("opencore esp"), DerivedGUID("opencore esp")
	if a != b || a == DerivedGUID("opencore disk") {
		t.Fatal("same seed, same GUID; different seed, different GUID")
	}
	s := a.String()
	if s[14] != '4' || !strings.ContainsRune("89AB", rune(s[19])) {
		t.Fatalf("not version 4 / RFC 4122 variant: %s", s)
	}
}

func TestLastUsableLBAIsSgdisksEnd(t *testing.T) {
	if got := LastUsableLBA(mib192); got != 393182 {
		t.Fatalf("got %d, sgdisk --new=1:2048:0 ends at 393182", got)
	}
}

// The protective MBR's ending CHS is the disk's last LBA on the 255-head,
// 63-sector geometry sgdisk assumes, or FF FF FF past cylinder 1023, as the
// UEFI specification asks. MEASURED 2026-09-25 against sgdisk 1.0.10
// (--clear on a truncated file of each size): bytes 451-453 of sector 0.
func TestProtectiveMBRCHSIsSgdisks(t *testing.T) {
	for _, c := range []struct {
		sectors uint64
		chs     [3]byte
	}{
		{2048, [3]byte{0x20, 0x20, 0x00}},
		{4096, [3]byte{0x41, 0x01, 0x00}},
		{81920, [3]byte{0x19, 0x14, 0x05}},
		{mib192, [3]byte{0x79, 0x21, 0x18}},
		{7800 * 2048, [3]byte{0x5B, 0xF9, 0xE2}},
		{8300 * 2048, [3]byte{0xFF, 0xFF, 0xFF}},
	} {
		if got := chs(c.sectors - 1); got != c.chs {
			t.Errorf("%d sectors: % X, sgdisk writes % X", c.sectors, got, c.chs)
		}
	}
}

func TestGPTRoundTrip(t *testing.T) {
	img := make(image, mib192*SectorSize)
	disk := DerivedGUID("test disk")
	if err := WriteGPT(img, mib192, disk, espOn(mib192)); err != nil {
		t.Fatal(err)
	}
	gotDisk, parts, err := ReadGPT(img, mib192)
	if err != nil {
		t.Fatal(err)
	}
	if gotDisk != disk || len(parts) != 1 || parts[0] != espOn(mib192)[0] {
		t.Fatalf("%v %+v", gotDisk, parts)
	}
	if img[510] != 0x55 || img[511] != 0xAA || img[450] != 0xEE {
		t.Fatal("no protective MBR")
	}
}

func TestReadGPTRefusesACorruptTable(t *testing.T) {
	img := make(image, mib192*SectorSize)
	if err := WriteGPT(img, mib192, DerivedGUID("d"), espOn(mib192)); err != nil {
		t.Fatal(err)
	}
	img[2*SectorSize+100] ^= 0xFF // inside the primary partition entries
	if _, _, err := ReadGPT(img, mib192); err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("err = %v", err)
	}
}

// ReadGPT refuses a table whose CRCs hold but whose numbers cannot: a
// primary header that says it lives elsewhere, and entries that end
// before they start or past the disk.
func TestReadGPTRefusesATableThatCannotBe(t *testing.T) {
	le := binary.LittleEndian
	// reseal recomputes the primary entries' CRC and the header's.
	reseal := func(img image) {
		h := img[SectorSize : 2*SectorSize]
		le.PutUint32(h[88:], crc32.ChecksumIEEE(img[2*SectorSize:34*SectorSize]))
		le.PutUint32(h[16:], 0)
		le.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
	}
	entry := 2 * SectorSize
	for name, c := range map[string]struct {
		patch func(img image)
		want  string
	}{
		"a header that says it is at LBA 2": {func(img image) { le.PutUint64(img[SectorSize+24:], 2) }, "LBA"},
		"an entry that ends before it starts": {func(img image) {
			le.PutUint64(img[entry+32:], 5000)
			le.PutUint64(img[entry+40:], 4000)
		}, "ends"},
		"an entry that ends past the disk": {func(img image) { le.PutUint64(img[entry+40:], mib192) }, "past"},
	} {
		t.Run(name, func(t *testing.T) {
			img := make(image, mib192*SectorSize)
			if err := WriteGPT(img, mib192, DerivedGUID("d"), espOn(mib192)); err != nil {
				t.Fatal(err)
			}
			c.patch(img)
			reseal(img)
			if _, _, err := ReadGPT(img, mib192); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
}

func TestWriteGPTRefusesPartitionsThatDoNotFit(t *testing.T) {
	img := make(image, mib192*SectorSize)
	for name, p := range map[string]Partition{
		"before the first usable LBA":    {Type: TypeEFISystem, FirstLBA: 33, LastLBA: 100},
		"past the last usable LBA":       {Type: TypeEFISystem, FirstLBA: AlignLBA, LastLBA: LastUsableLBA(mib192) + 1},
		"ending before it starts":        {Type: TypeEFISystem, FirstLBA: 5000, LastLBA: 4000},
		"with a name too long":           {Type: TypeEFISystem, FirstLBA: AlignLBA, LastLBA: 4000, Name: strings.Repeat("x", 37)},
		"with no type (an unused entry)": {FirstLBA: AlignLBA, LastLBA: 4000},
	} {
		if err := WriteGPT(img, mib192, DerivedGUID("d"), []Partition{p}); err == nil {
			t.Errorf("a partition %s must be refused", name)
		}
	}
	overlap := []Partition{
		{Type: TypeEFISystem, FirstLBA: AlignLBA, LastLBA: 10000},
		{Type: TypeAppleHFS, FirstLBA: 9000, LastLBA: 20000},
	}
	if err := WriteGPT(img, mib192, DerivedGUID("d"), overlap); err == nil {
		t.Error("overlapping partitions must be refused")
	}
}

// sgdisk is the reference: it must find nothing wrong with Go's table,
// and must read back the partition Go wrote, where sgdisk would have put
// it. And Go must read sgdisk's own table.
func TestGPTMatchesSgdisk(t *testing.T) {
	for _, tool := range []string{"sgdisk", "truncate"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	dir := t.TempDir()
	goImg := filepath.Join(dir, "go.img")
	f, err := os.Create(goImg)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(mib192 * SectorSize); err != nil {
		t.Fatal(err)
	}
	if err := WriteGPT(f, mib192, DerivedGUID("d"), espOn(mib192)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command("sgdisk", "-v", goImg).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "No problems found") {
		t.Fatalf("sgdisk -v: %v\n%s", err, out)
	}
	info, _ := exec.Command("sgdisk", "-i", "1", goImg).CombinedOutput()
	for _, want := range []string{"First sector: 2048", "Last sector: 393182", "EFI system partition", "Partition name: 'EFI'"} {
		if !strings.Contains(string(info), want) {
			t.Errorf("sgdisk -i 1 lacks %q:\n%s", want, info)
		}
	}

	shImg := filepath.Join(dir, "sh.img")
	defer func() {
		if t.Failed() {
			return
		}
		// Byte for byte, sgdisk's image is Go's but for the GUIDs, which
		// sgdisk makes at random, and the CRCs that cover them.
		a, err := os.ReadFile(goImg)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(shImg)
		if err != nil {
			t.Fatal(err)
		}
		last := (mib192 - 1) * SectorSize
		entries := []int{2 * SectorSize, (mib192 - 1 - 32) * SectorSize}
		for _, img := range [][]byte{a, b} {
			for _, h := range []int{SectorSize, last} {
				clear(img[h+16 : h+20]) // header CRC
				clear(img[h+56 : h+72]) // disk GUID
				clear(img[h+88 : h+92]) // entries CRC
			}
			for _, e := range entries {
				clear(img[e+16 : e+32]) // partition GUID
			}
		}
		if len(a) != len(b) {
			t.Fatalf("%d bytes, sgdisk's %d", len(a), len(b))
		}
		if bytes.Equal(a, b) {
			return
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("byte %d (sector %d) is %#02x; sgdisk's is %#02x", i, i/SectorSize, a[i], b[i])
			}
		}
	}()
	if out, err := exec.Command("truncate", "-s", "192M", shImg).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := exec.Command("sgdisk", "--clear", "--new=1:2048:0", "--typecode=1:EF00",
		"--change-name=1:EFI", shImg).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	sf, err := os.Open(shImg)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	_, parts, err := ReadGPT(sf, mib192)
	if err != nil {
		t.Fatal(err)
	}
	p := parts[0]
	if len(parts) != 1 || p.Type != TypeEFISystem || p.FirstLBA != 2048 || p.LastLBA != 393182 || p.Name != "EFI" {
		t.Fatalf("read sgdisk's table as %+v", parts)
	}
}
