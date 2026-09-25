package diskimg

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Geometry is a FAT32 filesystem's layout, in sectors unless named
// otherwise.
type Geometry struct {
	Sectors           uint32 // the whole filesystem: its partition
	SectorsPerCluster uint32
	ReservedSectors   uint32
	FATs              uint32
	FATSectors        uint32 // each FAT's
	Clusters          uint32
	HiddenSectors     uint32 // before the filesystem on its disk: the partition's LBA
}

const (
	fatReserved      = 32
	fatCopies        = 2
	fat32MinClusters = 65525
	// fat32MaxClusters is the most a 28-bit FAT entry can number: cluster
	// 0x0FFFFFF6 on are reserved, and 0x0FFFFFF7 marks a bad cluster.
	fat32MaxClusters = 0x0FFFFFF5
	// fatMaxSectors is the largest FAT that numbering needs: 1 GiB.
	fatMaxSectors    = ((fat32MaxClusters+2)*4 + SectorSize - 1) / SectorSize
	rootCluster      = 2
	fsInfoSector     = 1
	backupBootSector = 6
	fatEOC           = 0x0FFFFFFF
	dirEntrySize     = 32
	attrDir          = 0x10
	attrArchive      = 0x20
	attrVolumeID     = 0x08
	attrLFN          = 0x0F
	// fatDate is 1980-01-01, the FAT epoch, and every timestamp this
	// package writes: so the same files give the same bytes.
	fatDate = 1<<5 | 1
)

// FAT32Geometry lays out a FAT32 filesystem of sectors sectors the way
// mformat -F does -- MEASURED against mtools 4.0.43 from 40 MiB to 2 GiB,
// and held there by tools_test.go: 32 reserved sectors and two FATs;
// the largest cluster of 8, 4, 2 or 1 sectors that still leaves the
// 65525 clusters FAT32 needs; the smallest FAT that maps every cluster.
func FAT32Geometry(sectors, hidden uint32) (Geometry, error) {
	for _, spc := range []uint32{8, 4, 2, 1} {
		for fatSectors := uint32(1); fatReserved+fatCopies*fatSectors < sectors; fatSectors++ {
			clusters := (sectors - fatReserved - fatCopies*fatSectors) / spc
			if uint64(clusters+2)*4 > uint64(fatSectors)*SectorSize {
				continue // this FAT is too small to map them; try a larger one
			}
			if clusters < fat32MinClusters {
				break // too few clusters at this size: try a smaller cluster
			}
			if clusters > fat32MaxClusters {
				return Geometry{}, fmt.Errorf("%d sectors is too large: %d clusters of %d sectors, and FAT32 numbers at most %d clusters", sectors, clusters, spc, fat32MaxClusters)
			}
			return Geometry{Sectors: sectors, SectorsPerCluster: spc, ReservedSectors: fatReserved,
				FATs: fatCopies, FATSectors: fatSectors, Clusters: clusters, HiddenSectors: hidden}, nil
		}
	}
	return Geometry{}, fmt.Errorf("%d sectors is too small for FAT32, which needs at least %d clusters", sectors, fat32MinClusters)
}

// valid says whether WriteTo can lay a filesystem out by g: a FAT that
// maps every cluster, the boot sectors inside the reserved area, and
// nothing past g.Sectors. FAT32Geometry's are.
func (g Geometry) valid() error {
	end := uint64(g.ReservedSectors) + uint64(g.FATs)*uint64(g.FATSectors) + uint64(g.Clusters)*uint64(g.SectorsPerCluster)
	switch {
	case g.SectorsPerCluster == 0 || g.FATs == 0:
		return fmt.Errorf("geometry %+v: no clusters or no FATs", g)
	case g.SectorsPerCluster > 128 || g.SectorsPerCluster&(g.SectorsPerCluster-1) != 0:
		return fmt.Errorf("geometry %+v: %d sectors per cluster; the boot sector holds a power of 2 from 1 to 128", g, g.SectorsPerCluster)
	case g.FATs > 0xFF || g.ReservedSectors > 0xFFFF:
		return fmt.Errorf("geometry %+v: the boot sector holds at most 255 FATs and 65535 reserved sectors", g)
	case g.Clusters > fat32MaxClusters:
		return fmt.Errorf("geometry %+v: %d clusters, and FAT32 numbers at most %d clusters", g, g.Clusters, fat32MaxClusters)
	case g.FATSectors > fatMaxSectors:
		return fmt.Errorf("geometry %+v: a FAT of %d sectors, more than the %d that number every cluster FAT32 can", g, g.FATSectors, fatMaxSectors)
	case g.ReservedSectors <= backupBootSector:
		return fmt.Errorf("geometry %+v: %d reserved sectors cannot hold the backup boot sector at %d", g, g.ReservedSectors, backupBootSector)
	case (uint64(g.Clusters)+2)*4 > uint64(g.FATSectors)*SectorSize:
		return fmt.Errorf("geometry %+v: a FAT of %d sectors cannot map %d clusters", g, g.FATSectors, g.Clusters)
	case end > uint64(g.Sectors):
		return fmt.Errorf("geometry %+v: the layout ends at sector %d, past the filesystem's %d", g, end, g.Sectors)
	}
	return nil
}

func (g Geometry) clusterBytes() uint32 { return g.SectorsPerCluster * SectorSize }

// clusterOffset is cluster c's byte offset within the filesystem.
func (g Geometry) clusterOffset(c uint32) int64 {
	return (int64(g.ReservedSectors) + int64(g.FATs)*int64(g.FATSectors) + (int64(c)-2)*int64(g.SectorsPerCluster)) * SectorSize
}

// A FAT is a FAT32 filesystem put together in memory and written in one
// pass by WriteTo. Entries keep the order they were added in, and so does
// the layout, so the same calls give the same bytes.
type FAT struct {
	label  [11]byte
	serial uint32
	root   *fnode
}

type fnode struct {
	name     string
	dir      bool
	data     []byte
	children []*fnode
	// set by WriteTo
	short  [11]byte
	lfn    bool
	first  uint32
	nclust uint32
	parent *fnode
}

// NewFAT starts an empty filesystem. label is at most 11 characters that
// are valid in a short name; it is stored upper-case.
func NewFAT(label string, serial uint32) (*FAT, error) {
	up := strings.ToUpper(label)
	if up == "" || len(up) > 11 {
		return nil, fmt.Errorf("volume label %q: 1 to 11 characters", label)
	}
	for _, c := range up {
		if !validShort(c) && c != ' ' {
			return nil, fmt.Errorf("volume label %q: %q is not allowed", label, c)
		}
	}
	var l [11]byte
	copy(l[:], up+strings.Repeat(" ", 11-len(up)))
	return &FAT{label: l, serial: serial, root: &fnode{dir: true}}, nil
}

// Mkdir adds a directory. Its parent must exist; it must not.
func (f *FAT) Mkdir(path string) error { return f.add(path, &fnode{dir: true}) }

// WriteFile adds a file holding data. Its parent must exist; it must not.
func (f *FAT) WriteFile(path string, data []byte) error {
	if err := checkFileSize(int64(len(data))); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return f.add(path, &fnode{data: append([]byte(nil), data...)})
}

func (f *FAT) add(path string, n *fnode) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	dir := f.root
	for i, p := range parts {
		if err := checkLongName(p); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		var found *fnode
		for _, c := range dir.children {
			if strings.EqualFold(c.name, p) {
				found = c
			}
		}
		if i == len(parts)-1 {
			if found != nil {
				return fmt.Errorf("%s already exists", path)
			}
			n.name = p
			dir.children = append(dir.children, n)
			return nil
		}
		if found == nil || !found.dir {
			return fmt.Errorf("%s: %s is not a directory", path, "/"+strings.Join(parts[:i+1], "/"))
		}
		dir = found
	}
	return nil
}

// entryCount is how many 32-byte entries dir's directory holds.
func (n *fnode) entryCount() uint32 {
	c := uint32(2) // "." and ".."; the root's volume label, and one entry spare
	for _, ch := range n.children {
		c++
		if ch.lfn {
			c += lfnCount(ch.name)
		}
	}
	return c
}

// WriteTo writes the filesystem at byte offset off of w, laid out by g
// (from FAT32Geometry). It writes the boot sectors, both FATs, every
// directory and every file, and nothing outside g.Sectors: w is expected
// to be zero where nothing is written (a fresh, truncated file).
func (f *FAT) WriteTo(w io.WriterAt, off int64, g Geometry) error {
	if err := g.valid(); err != nil {
		return err
	}
	if err := f.root.assignNames(); err != nil {
		return err
	}
	cb := g.clusterBytes()
	next := uint32(rootCluster)
	alloc := func(n *fnode, bytes uint32) {
		n.nclust = clustersFor(bytes, cb)
		if n.dir && n.nclust == 0 {
			n.nclust = 1
		}
		if n.nclust > 0 {
			n.first = next
			next += n.nclust
		}
	}
	var place func(d *fnode)
	place = func(d *fnode) {
		for _, c := range d.children {
			c.parent = d
			if c.dir {
				alloc(c, c.entryCount()*dirEntrySize)
				place(c)
			} else {
				alloc(c, uint32(len(c.data)))
			}
		}
	}
	alloc(f.root, f.root.entryCount()*dirEntrySize)
	place(f.root)
	used := next - rootCluster
	if used > g.Clusters {
		return fmt.Errorf("the files need %d clusters; a filesystem of %d sectors has %d", used, g.Sectors, g.Clusters)
	}

	fat := make([]byte, int(g.FATSectors)*SectorSize)  // at most 1 GiB: valid bounds FATSectors
	binary.LittleEndian.PutUint32(fat[0:], 0x0FFFFFF8) // the media byte, 0xF8
	binary.LittleEndian.PutUint32(fat[4:], 0xFFFFFFFF) // clean, no errors: all ones, as mformat writes it
	var chain func(n *fnode)
	chain = func(n *fnode) {
		for i := uint32(0); i < n.nclust; i++ {
			v := uint32(fatEOC)
			if i+1 < n.nclust {
				v = n.first + i + 1
			}
			binary.LittleEndian.PutUint32(fat[(n.first+i)*4:], v)
		}
		for _, c := range n.children {
			chain(c)
		}
	}
	chain(f.root)

	reserved := make([]byte, int(g.ReservedSectors)*SectorSize)
	boot := bootSector(g, f.label, f.serial)
	// FSInfo's "next free" hint holds the last cluster allocated, as
	// mformat writes it and the Linux driver reads it (it searches from the
	// one after). mformat keeps no backup FSInfo beside the backup boot
	// sector, and neither does this.
	info := fsInfo(g.Clusters-used, next-1)
	copy(reserved[0:], boot)
	copy(reserved[fsInfoSector*SectorSize:], info)
	copy(reserved[backupBootSector*SectorSize:], boot)

	writes := []write{{0, reserved}}
	for i := uint32(0); i < g.FATs; i++ {
		writes = append(writes, write{(int64(g.ReservedSectors) + int64(i)*int64(g.FATSectors)) * SectorSize, fat})
	}
	var dirs func(d *fnode)
	dirs = func(d *fnode) {
		writes = append(writes, write{g.clusterOffset(d.first), d.dirBytes(f.label, cb)})
		for _, c := range d.children {
			if c.dir {
				dirs(c)
			} else if c.nclust > 0 {
				buf := make([]byte, c.nclust*cb)
				copy(buf, c.data)
				writes = append(writes, write{g.clusterOffset(c.first), buf})
			}
		}
	}
	dirs(f.root)
	for _, wr := range writes {
		if _, err := w.WriteAt(wr.data, off+wr.at); err != nil {
			return err
		}
	}
	return nil
}

// A write is bytes at an offset within the filesystem.
type write struct {
	at   int64
	data []byte
}

// assignNames gives every entry below n its short name, and says which
// need long-name entries too.
func (n *fnode) assignNames() error {
	taken := map[[11]byte]bool{}
	for _, c := range n.children {
		s, lfn, err := shortName(c.name, taken)
		if err != nil {
			return err
		}
		taken[s] = true
		c.short, c.lfn = s, lfn
		if c.dir {
			if err := c.assignNames(); err != nil {
				return err
			}
		}
	}
	return nil
}

// dirBytes is directory d's clusters: the volume label (root) or "." and
// ".." (anything else), then each child's long-name entries and short
// entry.
func (d *fnode) dirBytes(label [11]byte, cb uint32) []byte {
	buf := make([]byte, 0, d.nclust*cb)
	if d.parent == nil {
		buf = append(buf, dirent(label, attrVolumeID, 0, 0)...)
	} else {
		parent := d.parent.first
		if d.parent.parent == nil {
			parent = 0 // ".." of a top-level directory names the root as 0
		}
		buf = append(buf, dirent(pack83(".", ""), attrDir, d.first, 0)...)
		buf = append(buf, dirent(pack83("..", ""), attrDir, parent, 0)...)
	}
	for _, c := range d.children {
		if c.lfn {
			for _, e := range lfnEntries(c.name, c.short) {
				buf = append(buf, e...)
			}
		}
		attr, size := byte(attrArchive), uint32(len(c.data))
		if c.dir {
			attr, size = attrDir, 0
		}
		buf = append(buf, dirent(c.short, attr, c.first, size)...)
	}
	out := make([]byte, d.nclust*cb)
	copy(out, buf)
	return out
}

// dirent is one 32-byte short directory entry.
func dirent(name [11]byte, attr byte, cluster, size uint32) []byte {
	e := make([]byte, dirEntrySize)
	copy(e[0:11], name[:])
	e[11] = attr
	binary.LittleEndian.PutUint16(e[16:], fatDate) // created
	binary.LittleEndian.PutUint16(e[18:], fatDate) // accessed
	binary.LittleEndian.PutUint16(e[20:], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[24:], fatDate) // written
	binary.LittleEndian.PutUint16(e[26:], uint16(cluster))
	binary.LittleEndian.PutUint32(e[28:], size)
	return e
}

func bootSector(g Geometry, label [11]byte, serial uint32) []byte {
	b := make([]byte, SectorSize)
	copy(b[0:3], []byte{0xEB, 0x58, 0x90})
	copy(b[3:11], "MSWIN4.1")
	binary.LittleEndian.PutUint16(b[11:], SectorSize)
	b[13] = byte(g.SectorsPerCluster)
	binary.LittleEndian.PutUint16(b[14:], uint16(g.ReservedSectors))
	b[16] = byte(g.FATs)
	b[21] = 0xF8                                                     // media: fixed disk
	binary.LittleEndian.PutUint16(b[24:], 63)                        // sectors per track, as mformat
	binary.LittleEndian.PutUint16(b[26:], lbaAssistHeads(g.Sectors)) // heads, as mformat
	binary.LittleEndian.PutUint32(b[28:], g.HiddenSectors)
	binary.LittleEndian.PutUint32(b[32:], g.Sectors)
	binary.LittleEndian.PutUint32(b[36:], g.FATSectors)
	binary.LittleEndian.PutUint32(b[44:], rootCluster)
	binary.LittleEndian.PutUint16(b[48:], fsInfoSector)
	binary.LittleEndian.PutUint16(b[50:], backupBootSector)
	b[64] = 0x80 // drive number
	b[66] = 0x29 // the next three fields are present
	binary.LittleEndian.PutUint32(b[67:], serial)
	copy(b[71:82], label[:])
	copy(b[82:90], "FAT32   ")
	b[510], b[511] = 0x55, 0xAA
	return b
}

// lbaAssistHeads is the heads a BIOS's LBA-assisted translation gives a
// disk of sectors sectors, and what mformat writes: the fewest of 16, 32,
// 64 and 128 that keep it under 1024 cylinders of 63 sectors, or 255.
// MEASURED against mtools 4.0.43. mformat translates the whole device it
// is handed -- the partition this package lays out, and the backup GPT
// after it -- so for a filesystem that ends within 33 sectors below a boundary
// the two differ; this one field is all that changes, and nothing reads it.
func lbaAssistHeads(sectors uint32) uint16 {
	for _, h := range []uint32{16, 32, 64, 128} {
		if sectors < 1024*63*h {
			return uint16(h)
		}
	}
	return 255
}

func fsInfo(free, last uint32) []byte {
	b := make([]byte, SectorSize)
	binary.LittleEndian.PutUint32(b[0:], 0x41615252)
	binary.LittleEndian.PutUint32(b[484:], 0x61417272)
	binary.LittleEndian.PutUint32(b[488:], free)
	binary.LittleEndian.PutUint32(b[492:], last)
	binary.LittleEndian.PutUint32(b[508:], 0xAA550000)
	return b
}

// checkFileSize refuses a file larger than a directory entry's 32-bit
// size can say.
func checkFileSize(n int64) error {
	if n > 0xFFFFFFFF {
		return fmt.Errorf("%d bytes; a FAT file holds at most 4294967295", n)
	}
	return nil
}

// clustersFor is how many clusters of cb bytes hold n bytes, rounded up
// without wrapping for the largest n.
func clustersFor(n, cb uint32) uint32 {
	return uint32((uint64(n) + uint64(cb) - 1) / uint64(cb))
}
