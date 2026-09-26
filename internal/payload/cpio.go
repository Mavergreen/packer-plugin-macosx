// Package payload builds the first-boot flat package: the xar container,
// its odc cpio Scripts archive, and the postinstall script that carries
// everything.
package payload

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strconv"
)

// File-type bits: NOT optional (see TestCPIOEntriesCarryFileTypeBits).
const (
	sIFDIR = 0o040000
	sIFREG = 0o100000
)

type cpioEntry struct {
	Name string
	Mode uint32 // permission bits; the type bits come from Dir
	Data []byte
	Dir  bool
}

func odcEntry(e cpioEntry, ino int) []byte {
	data := e.Data
	typ := uint32(sIFREG)
	if e.Dir {
		data, typ = nil, sIFDIR
	}
	name := append([]byte(e.Name), 0)
	h := fmt.Sprintf("070707%06o%06o%06o%06o%06o%06o%06o%011o%06o%011o",
		0, ino&0o777777, typ|(e.Mode&0o7777), 0, 0, 1, 0, 0, len(name), len(data))
	return append(append([]byte(h), name...), data...)
}

// makeODC is a POSIX.1 "odc" cpio (magic 070707): fixed mtimes, uid and
// gid 0, inodes numbered from 1, then the TRAILER!!! entry.
func makeODC(entries []cpioEntry) []byte {
	var b bytes.Buffer
	ino := 1
	for _, e := range entries {
		b.Write(odcEntry(e, ino))
		ino++
	}
	b.Write(odcEntry(cpioEntry{Name: "TRAILER!!!", Mode: 0o644}, ino))
	return b.Bytes()
}

func readODC(b []byte) ([]cpioEntry, error) {
	var out []cpioEntry
	for pos := 0; pos+76 <= len(b); {
		if string(b[pos:pos+6]) != "070707" {
			return nil, fmt.Errorf("not an odc cpio header at offset %d", pos)
		}
		f := string(b[pos+6 : pos+76])
		mode, _ := strconv.ParseUint(f[12:18], 8, 32)
		namesize, _ := strconv.ParseUint(f[53:59], 8, 32)
		filesize, _ := strconv.ParseUint(f[59:70], 8, 64)
		pos += 76
		if pos+int(namesize)+int(filesize) > len(b) {
			return nil, fmt.Errorf("truncated cpio entry at offset %d", pos-76)
		}
		name := string(b[pos : pos+int(namesize)-1])
		pos += int(namesize)
		data := b[pos : pos+int(filesize)]
		pos += int(filesize)
		if name == "TRAILER!!!" {
			break
		}
		out = append(out, cpioEntry{Name: name, Mode: uint32(mode) & 0o7777, Data: data, Dir: mode&0o170000 == sIFDIR})
	}
	return out, nil
}

// gzipDeterministic: level 9 and a zero mtime, so identical input gives
// identical output.
func gzipDeterministic(data []byte) ([]byte, error) {
	var b bytes.Buffer
	w, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
