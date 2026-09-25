package diskimg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"unicode/utf16"
)

// A DirEntry is one file or directory as a FAT directory lists it. Name
// is its long name when it has one, and Short otherwise; Short is the 8.3
// name as "BASE.EXT", lower-cased where the entry's case flags say so.
type DirEntry struct {
	Name, Short string
	Dir         bool
	Size        uint32
	Cluster     uint32 // the first; 0 for an empty file
}

// A FATReader reads a FAT32 filesystem: whatever wrote it, this package
// or mtools. It reads the first FAT once, at OpenFAT, and everything
// else as it is asked for.
type FATReader struct {
	G      Geometry
	Label  string // from the boot sector, right-trimmed
	OEM    string
	Serial uint32

	r    io.ReaderAt
	off  int64
	root uint32
	fat  []uint32
}

// fatEnd is where a FAT32 entry means the end of a chain.
const fatEnd = 0x0FFFFFF8

// OpenFAT reads the FAT32 filesystem whose boot sector is at byte off of
// r.
func OpenFAT(r io.ReaderAt, off int64) (*FATReader, error) {
	b := make([]byte, SectorSize)
	if _, err := r.ReadAt(b, off); err != nil {
		return nil, fmt.Errorf("reading the boot sector: %w", err)
	}
	le16 := func(at int) uint32 { return uint32(binary.LittleEndian.Uint16(b[at:])) }
	le32 := func(at int) uint32 { return binary.LittleEndian.Uint32(b[at:]) }
	switch {
	case b[510] != 0x55 || b[511] != 0xAA:
		return nil, errors.New("not a FAT filesystem: no 55 AA boot signature")
	case le16(11) != SectorSize:
		return nil, fmt.Errorf("%d bytes per sector; only %d is supported", le16(11), SectorSize)
	case le16(22) != 0 || le32(36) == 0:
		return nil, errors.New("not FAT32: the FAT size is in the FAT12/16 field")
	case b[13] == 0 || b[16] == 0:
		return nil, fmt.Errorf("implausible boot sector: %d sectors per cluster, %d FATs", b[13], b[16])
	}
	g := Geometry{
		Sectors:           le32(32),
		SectorsPerCluster: uint32(b[13]),
		ReservedSectors:   le16(14),
		FATs:              uint32(b[16]),
		FATSectors:        le32(36),
		HiddenSectors:     le32(28),
	}
	meta := uint64(g.ReservedSectors) + uint64(g.FATs)*uint64(g.FATSectors)
	if meta >= uint64(g.Sectors) {
		return nil, fmt.Errorf("implausible boot sector: %d reserved and FAT sectors in a filesystem of %d", meta, g.Sectors)
	}
	g.Clusters = (g.Sectors - uint32(meta)) / g.SectorsPerCluster
	if g.Clusters > fat32MaxClusters {
		return nil, fmt.Errorf("implausible boot sector: %d clusters, and FAT32 numbers at most %d clusters", g.Clusters, fat32MaxClusters)
	}
	fatBytes := (int64(g.Clusters) + 2) * 4 // the part of the FAT that maps a cluster
	if fatBytes > int64(g.FATSectors)*SectorSize {
		return nil, fmt.Errorf("implausible boot sector: a FAT of %d sectors cannot map %d clusters", g.FATSectors, g.Clusters)
	}
	fr := &FATReader{
		G:      g,
		Label:  strings.TrimRight(string(b[71:82]), " "),
		OEM:    string(b[3:11]),
		Serial: le32(67),
		r:      r,
		off:    off,
		root:   le32(44),
	}
	if fr.root < rootCluster || fr.root > g.Clusters+1 {
		return nil, fmt.Errorf("root cluster %d is outside clusters 2-%d", fr.root, g.Clusters+1)
	}
	// Before allocating the FAT, make sure r holds all of it: a crafted
	// boot sector must not buy a gigabyte of memory with 4 KiB of input.
	fatAt := off + int64(g.ReservedSectors)*SectorSize
	if _, err := r.ReadAt(make([]byte, 1), fatAt+fatBytes-1); err != nil {
		return nil, fmt.Errorf("the FAT's %d bytes are not all there: %w", fatBytes, err)
	}
	raw := make([]byte, fatBytes)
	if _, err := r.ReadAt(raw, fatAt); err != nil {
		return nil, fmt.Errorf("reading the FAT: %w", err)
	}
	fr.fat = make([]uint32, g.Clusters+2)
	for i := range fr.fat {
		fr.fat[i] = binary.LittleEndian.Uint32(raw[4*i:]) & 0x0FFFFFFF // the top four bits are reserved
	}
	return fr, nil
}

// chain is the clusters of the chain that starts at first: none when
// first is 0. Every entry up to the end-of-chain mark must name another
// cluster of the filesystem: a free (0) or reserved (1) entry, or one
// past the last cluster, cuts the chain, and is an error, never the end.
func (fr *FATReader) chain(first uint32) ([]uint32, error) {
	if first == 0 {
		return nil, nil
	}
	last := fr.G.Clusters + 1
	if first < rootCluster || first > last {
		return nil, fmt.Errorf("a chain starts at cluster %d, outside clusters 2-%d", first, last)
	}
	var cs []uint32
	for c := first; ; {
		if uint32(len(cs)) >= fr.G.Clusters {
			return nil, fmt.Errorf("the chain from cluster %d is a loop", first)
		}
		cs = append(cs, c)
		n := fr.fat[c]
		switch {
		case n >= fatEnd:
			return cs, nil
		case n == 0:
			return nil, fmt.Errorf("the chain from cluster %d is cut: cluster %d's FAT entry is 0, a free cluster, not the end-of-chain mark", first, c)
		case n == 1:
			return nil, fmt.Errorf("the chain from cluster %d is cut: cluster %d's FAT entry is 1, a reserved value", first, c)
		case n > last:
			return nil, fmt.Errorf("the chain from cluster %d is cut: cluster %d's FAT entry is %#x, outside clusters 2-%d", first, c, n, last)
		}
		c = n
	}
}

// readChain is the bytes of the chain from first, a run of contiguous
// clusters read at once.
func (fr *FATReader) readChain(first uint32) ([]byte, error) {
	cs, err := fr.chain(first)
	if err != nil {
		return nil, err
	}
	cb := int(fr.G.clusterBytes())
	buf := make([]byte, len(cs)*cb)
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && cs[j] == cs[j-1]+1 {
			j++
		}
		if _, err := fr.r.ReadAt(buf[i*cb:j*cb], fr.off+fr.G.clusterOffset(cs[i])); err != nil {
			return nil, fmt.Errorf("reading clusters %d-%d: %w", cs[i], cs[j-1], err)
		}
		i = j
	}
	return buf, nil
}

// readDir lists the directory whose chain starts at cluster.
func (fr *FATReader) readDir(cluster uint32) ([]DirEntry, error) {
	data, err := fr.readChain(cluster)
	if err != nil {
		return nil, err
	}
	return parseDir(data), nil
}

// parseDir reads a directory's entries, 32 bytes at a time, putting each
// run of long-name entries together with the short entry after it.
func parseDir(data []byte) []DirEntry {
	var (
		long    []uint16 // the pending long name, 13 units a part
		sum     byte     // its checksum
		want    int      // the ordinal of the part expected next; 0 when complete
		pending bool
	)
	drop := func() { long, pending = nil, false }
	var out []DirEntry
	for at := 0; at+dirEntrySize <= len(data); at += dirEntrySize {
		e := data[at : at+dirEntrySize]
		switch {
		case e[0] == 0x00:
			return out
		case e[0] == 0xE5:
			drop()
			continue
		case e[11]&0x3F == attrLFN:
			ord := int(e[0] & 0x1F)
			if e[0]&0x40 != 0 {
				long, sum, want, pending = make([]uint16, ord*13), e[13], ord, true
			}
			if !pending || ord == 0 || ord != want || e[13] != sum {
				drop()
				continue
			}
			i := (ord - 1) * 13
			for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
				for p := r[0]; p < r[1]; p += 2 {
					long[i] = binary.LittleEndian.Uint16(e[p:])
					i++
				}
			}
			want--
			continue
		case e[11]&attrVolumeID != 0, e[0] == '.':
			drop()
			continue
		}

		var s [11]byte
		copy(s[:], e[0:11])
		base := strings.TrimRight(string(e[0:8]), " ")
		ext := strings.TrimRight(string(e[8:11]), " ")
		if e[0] == 0x05 {
			base = "\xE5" + base[1:]
		}
		if e[12]&0x08 != 0 {
			base = strings.ToLower(base)
		}
		if e[12]&0x10 != 0 {
			ext = strings.ToLower(ext)
		}
		d := DirEntry{
			Short:   base,
			Dir:     e[11]&attrDir != 0,
			Size:    binary.LittleEndian.Uint32(e[28:]),
			Cluster: uint32(binary.LittleEndian.Uint16(e[20:]))<<16 | uint32(binary.LittleEndian.Uint16(e[26:])),
		}
		if ext != "" {
			d.Short += "." + ext
		}
		d.Name = d.Short
		if pending && want == 0 && sum == lfnChecksum(s) {
			n := len(long)
			for i, u := range long {
				if u == 0 {
					n = i
					break
				}
			}
			d.Name = string(utf16.Decode(long[:n]))
		}
		drop()
		out = append(out, d)
	}
	return out
}

// split is path's components: none for the root.
func split(path string) []string {
	p := strings.Trim(path, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// find is the entry named name in es: by long name, then by short name,
// either without regard to case.
func find(es []DirEntry, name string) (DirEntry, bool) {
	for _, e := range es {
		if strings.EqualFold(e.Name, name) {
			return e, true
		}
	}
	for _, e := range es {
		if strings.EqualFold(e.Short, name) {
			return e, true
		}
	}
	return DirEntry{}, false
}

// ReadDir lists the directory at path, in the order its entries sit on
// disk.
func (fr *FATReader) ReadDir(path string) ([]DirEntry, error) {
	es, err := fr.readDir(fr.root)
	if err != nil {
		return nil, fmt.Errorf("/: %w", err)
	}
	parts := split(path)
	for i, p := range parts {
		here := "/" + strings.Join(parts[:i+1], "/")
		e, ok := find(es, p)
		switch {
		case !ok:
			return nil, fmt.Errorf("%s: %w", here, fs.ErrNotExist)
		case !e.Dir:
			return nil, fmt.Errorf("%s is not a directory", here)
		}
		if es, err = fr.readDir(e.Cluster); err != nil {
			return nil, fmt.Errorf("%s: %w", here, err)
		}
	}
	return es, nil
}

// ReadFile is the contents of the file at path.
func (fr *FATReader) ReadFile(path string) ([]byte, error) {
	parts := split(path)
	if len(parts) == 0 {
		return nil, errors.New("/ is a directory")
	}
	es, err := fr.ReadDir(strings.Join(parts[:len(parts)-1], "/"))
	if err != nil {
		return nil, err
	}
	e, ok := find(es, parts[len(parts)-1])
	switch {
	case !ok:
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	case e.Dir:
		return nil, fmt.Errorf("%s is a directory", path)
	}
	data, err := fr.readChain(e.Cluster)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if uint64(e.Size) > uint64(len(data)) {
		return nil, fmt.Errorf("%s: its size, %d bytes, is more than its %d clusters hold", path, e.Size, len(data)/int(fr.G.clusterBytes()))
	}
	return data[:e.Size], nil
}

// Walk calls fn for every file and directory, a directory before what it
// holds, each directory's entries in the order they sit on disk. Paths are
// "/" and the long names joined by "/". An error from fn stops the walk
// and is returned.
func (fr *FATReader) Walk(fn func(path string, e DirEntry) error) error {
	seen := map[uint32]bool{fr.root: true}
	var walk func(dir string, cluster uint32) error
	walk = func(dir string, cluster uint32) error {
		es, err := fr.readDir(cluster)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		for _, e := range es {
			p := strings.TrimSuffix(dir, "/") + "/" + e.Name
			if err := fn(p, e); err != nil {
				return err
			}
			if !e.Dir {
				continue
			}
			if e.Cluster == 0 || seen[e.Cluster] {
				return fmt.Errorf("%s: directory at cluster %d makes a loop", p, e.Cluster)
			}
			seen[e.Cluster] = true
			if err := walk(p, e.Cluster); err != nil {
				return err
			}
		}
		return nil
	}
	return walk("/", fr.root)
}
