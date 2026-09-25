package diskimg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"unicode/utf16"
)

const (
	gptEntries      = 128
	gptEntrySize    = 128
	gptEntrySectors = gptEntries * gptEntrySize / SectorSize // 32
	gptHeaderSize   = 92

	// FirstUsableLBA follows the protective MBR (0), the header (1) and
	// the partition entries (2-33).
	FirstUsableLBA = 2 + gptEntrySectors
	// AlignLBA is where a first partition starts: 1 MiB, as sgdisk and
	// every current tool align it.
	AlignLBA = 2048
)

// A Partition is one GPT entry. LastLBA is inclusive; Name is at most 36
// UTF-16 code units.
type Partition struct {
	Type, GUID        GUID
	Name              string
	FirstLBA, LastLBA uint64
}

// LastUsableLBA is the last sector a partition may use on a disk of
// sectors sectors: the backup entries and header follow it.
func LastUsableLBA(sectors uint64) uint64 { return sectors - 2 - gptEntrySectors }

// WriteGPT writes a protective MBR, the primary GPT header and entries,
// and the backup entries and header, to a disk of sectors sectors. It
// writes nothing else: w is expected to be zero elsewhere (a fresh,
// truncated file).
func WriteGPT(w io.WriterAt, sectors uint64, disk GUID, parts []Partition) error {
	if sectors < AlignLBA+FirstUsableLBA {
		return fmt.Errorf("a disk of %d sectors is too small for a GPT", sectors)
	}
	if len(parts) > gptEntries {
		return fmt.Errorf("%d partitions; a GPT holds %d", len(parts), gptEntries)
	}
	sorted := append([]Partition(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FirstLBA < sorted[j].FirstLBA })
	for i, p := range sorted {
		switch {
		case p.Type == GUID{}:
			return fmt.Errorf("partition %q has no type; a zero type GUID marks an unused entry", p.Name)
		case p.FirstLBA < FirstUsableLBA:
			return fmt.Errorf("partition %q starts at LBA %d, before the first usable LBA %d", p.Name, p.FirstLBA, FirstUsableLBA)
		case p.LastLBA > LastUsableLBA(sectors):
			return fmt.Errorf("partition %q ends at LBA %d, past the last usable LBA %d", p.Name, p.LastLBA, LastUsableLBA(sectors))
		case p.LastLBA < p.FirstLBA:
			return fmt.Errorf("partition %q ends (LBA %d) before it starts (LBA %d)", p.Name, p.LastLBA, p.FirstLBA)
		case len(utf16.Encode([]rune(p.Name))) > 36:
			return fmt.Errorf("partition name %q is longer than 36 UTF-16 units", p.Name)
		case i > 0 && p.FirstLBA <= sorted[i-1].LastLBA:
			return fmt.Errorf("partitions %q and %q overlap", sorted[i-1].Name, p.Name)
		}
	}

	entries := make([]byte, gptEntries*gptEntrySize)
	for i, p := range parts {
		e := entries[i*gptEntrySize:]
		copy(e[0:16], p.Type[:])
		copy(e[16:32], p.GUID[:])
		binary.LittleEndian.PutUint64(e[32:], p.FirstLBA)
		binary.LittleEndian.PutUint64(e[40:], p.LastLBA)
		// e[48:56] attributes: none
		for j, u := range utf16.Encode([]rune(p.Name)) {
			binary.LittleEndian.PutUint16(e[56+2*j:], u)
		}
	}
	entriesCRC := crc32.ChecksumIEEE(entries)
	last := sectors - 1
	backupEntries := last - gptEntrySectors

	header := func(my, alt, entriesLBA uint64) []byte {
		h := make([]byte, SectorSize)
		copy(h[0:8], "EFI PART")
		binary.LittleEndian.PutUint32(h[8:], 0x00010000)
		binary.LittleEndian.PutUint32(h[12:], gptHeaderSize)
		binary.LittleEndian.PutUint64(h[24:], my)
		binary.LittleEndian.PutUint64(h[32:], alt)
		binary.LittleEndian.PutUint64(h[40:], FirstUsableLBA)
		binary.LittleEndian.PutUint64(h[48:], LastUsableLBA(sectors))
		copy(h[56:72], disk[:])
		binary.LittleEndian.PutUint64(h[72:], entriesLBA)
		binary.LittleEndian.PutUint32(h[80:], gptEntries)
		binary.LittleEndian.PutUint32(h[84:], gptEntrySize)
		binary.LittleEndian.PutUint32(h[88:], entriesCRC)
		binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:gptHeaderSize]))
		return h
	}

	mbr := make([]byte, SectorSize)
	pe := mbr[446:]
	start, end := chs(1), chs(sectors-1)
	copy(pe[1:4], start[:])
	pe[4] = 0xEE // GPT protective
	copy(pe[5:8], end[:])
	binary.LittleEndian.PutUint32(pe[8:], 1)
	size := sectors - 1
	if size > 0xFFFFFFFF {
		size = 0xFFFFFFFF
	}
	binary.LittleEndian.PutUint32(pe[12:], uint32(size))
	mbr[510], mbr[511] = 0x55, 0xAA

	for _, wr := range []struct {
		lba  uint64
		data []byte
	}{
		{0, mbr},
		{1, header(1, last, 2)},
		{2, entries},
		{backupEntries, entries},
		{last, header(last, 1, backupEntries)},
	} {
		if _, err := w.WriteAt(wr.data, int64(wr.lba)*SectorSize); err != nil {
			return err
		}
	}
	return nil
}

// ReadGPT reads a disk's primary GPT, checking both CRCs, and returns its
// disk GUID and non-empty partitions in table order.
func ReadGPT(r io.ReaderAt, sectors uint64) (GUID, []Partition, error) {
	var disk GUID
	h := make([]byte, SectorSize)
	if _, err := r.ReadAt(h, SectorSize); err != nil {
		return disk, nil, err
	}
	if string(h[0:8]) != "EFI PART" {
		return disk, nil, errors.New("no GPT header at LBA 1")
	}
	hsize := binary.LittleEndian.Uint32(h[12:])
	if hsize < gptHeaderSize || hsize > SectorSize {
		return disk, nil, fmt.Errorf("GPT header size %d", hsize)
	}
	want := binary.LittleEndian.Uint32(h[16:])
	c := append([]byte(nil), h[:hsize]...)
	binary.LittleEndian.PutUint32(c[16:], 0)
	if crc32.ChecksumIEEE(c) != want {
		return disk, nil, errors.New("GPT header CRC mismatch")
	}
	if my := binary.LittleEndian.Uint64(h[24:]); my != 1 {
		return disk, nil, fmt.Errorf("the GPT header at LBA 1 says it is at LBA %d", my)
	}
	copy(disk[:], h[56:72])
	lba := binary.LittleEndian.Uint64(h[72:])
	n := binary.LittleEndian.Uint32(h[80:])
	esz := binary.LittleEndian.Uint32(h[84:])
	if esz < gptEntrySize || n == 0 || uint64(n)*uint64(esz) > 1<<20 || lba >= sectors {
		return disk, nil, fmt.Errorf("implausible GPT entry array: %d entries of %d bytes at LBA %d", n, esz, lba)
	}
	entries := make([]byte, int(n)*int(esz))
	if _, err := r.ReadAt(entries, int64(lba)*SectorSize); err != nil {
		return disk, nil, err
	}
	if crc32.ChecksumIEEE(entries) != binary.LittleEndian.Uint32(h[88:]) {
		return disk, nil, errors.New("GPT partition entries CRC mismatch")
	}
	var parts []Partition
	for i := 0; i < int(n); i++ {
		e := entries[i*int(esz):]
		var p Partition
		copy(p.Type[:], e[0:16])
		if p.Type == (GUID{}) {
			continue
		}
		copy(p.GUID[:], e[16:32])
		p.FirstLBA = binary.LittleEndian.Uint64(e[32:])
		p.LastLBA = binary.LittleEndian.Uint64(e[40:])
		switch {
		case p.LastLBA < p.FirstLBA:
			return disk, nil, fmt.Errorf("GPT entry %d ends (LBA %d) before it starts (LBA %d)", i+1, p.LastLBA, p.FirstLBA)
		case p.LastLBA > sectors-1:
			return disk, nil, fmt.Errorf("GPT entry %d ends at LBA %d, past the disk's last, %d", i+1, p.LastLBA, sectors-1)
		}
		var u []uint16
		for j := 0; j < 36; j++ {
			c := binary.LittleEndian.Uint16(e[56+2*j:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		p.Name = string(utf16.Decode(u))
		parts = append(parts, p)
	}
	return disk, parts, nil
}

// chs is lba's cylinder-head-sector address as an MBR entry stores it, on
// the 255-head, 63-sector geometry sgdisk assumes; FF FF FF when the
// cylinder is past 1023, which the UEFI specification says to write when
// the address cannot be represented.
func chs(lba uint64) [3]byte {
	const heads, secs = 255, 63
	c := lba / (heads * secs)
	if c > 1023 {
		return [3]byte{0xFF, 0xFF, 0xFF}
	}
	h, s := lba/secs%heads, lba%secs+1
	return [3]byte{byte(h), byte(s) | byte(c>>8)<<6, byte(c)}
}
