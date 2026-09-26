package payload

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const epoch = "1970-01-01T00:00:00Z"

type member struct {
	Name string
	Mode uint32
	Data []byte
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// buildXar writes a xar archive whose members are stored raw, from a
// fixed TOC template.
func buildXar(members []member) ([]byte, error) {
	ms := append([]member(nil), members...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
	var heap bytes.Buffer
	heap.Write(make([]byte, sha1.Size)) // the TOC's own checksum goes here
	var entries strings.Builder
	for i, m := range ms {
		id := i + 1
		offset := heap.Len()
		heap.Write(m.Data)
		d := sha1.Sum(m.Data)
		sum := hex.EncodeToString(d[:])
		fmt.Fprintf(&entries, "  <file id=\"%d\">\n"+
			"   <data>\n"+
			"    <length>%d</length>\n"+
			"    <offset>%d</offset>\n"+
			"    <size>%d</size>\n"+
			"    <encoding style=\"application/octet-stream\"/>\n"+
			"    <extracted-checksum style=\"sha1\">%s</extracted-checksum>\n"+
			"    <archived-checksum style=\"sha1\">%s</archived-checksum>\n"+
			"   </data>\n"+
			"   <ctime>%s</ctime>\n"+
			"   <mtime>%s</mtime>\n"+
			"   <atime>%s</atime>\n"+
			"   <group>wheel</group>\n"+
			"   <gid>0</gid>\n"+
			"   <user>root</user>\n"+
			"   <uid>0</uid>\n"+
			"   <mode>%04o</mode>\n"+
			"   <deviceno>0</deviceno>\n"+
			"   <inode>%d</inode>\n"+
			"   <type>file</type>\n"+
			"   <name>%s</name>\n"+
			"  </file>\n",
			id, len(m.Data), offset, len(m.Data), sum, sum, epoch, epoch, epoch, m.Mode, id, xmlEscape(m.Name))
	}
	toc := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
		"<xar>\n"+
		" <toc>\n"+
		"  <creation-time>%s</creation-time>\n"+
		"  <checksum style=\"sha1\">\n"+
		"   <offset>0</offset>\n"+
		"   <size>%d</size>\n"+
		"  </checksum>\n"+
		"%s"+
		" </toc>\n"+
		"</xar>\n", strings.TrimSuffix(epoch, "Z"), sha1.Size, entries.String())
	var ztoc bytes.Buffer
	zw, err := zlib.NewWriterLevel(&ztoc, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	zw.Write([]byte(toc))
	if err := zw.Close(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("xar!")
	binary.Write(&out, binary.BigEndian, uint16(28))
	binary.Write(&out, binary.BigEndian, uint16(1))
	binary.Write(&out, binary.BigEndian, uint64(ztoc.Len()))
	binary.Write(&out, binary.BigEndian, uint64(len(toc)))
	binary.Write(&out, binary.BigEndian, uint32(1)) // sha1
	out.Write(ztoc.Bytes())
	h := heap.Bytes()
	tsum := sha1.Sum(ztoc.Bytes())
	copy(h[:sha1.Size], tsum[:])
	out.Write(h)
	return out.Bytes(), nil
}

var (
	fileRE = regexp.MustCompile(`(?s)<file id="\d+">(.*?)</file>`)
	nameRE = regexp.MustCompile(`(?s)<name>(.*?)</name>`)
	offRE  = regexp.MustCompile(`<offset>(\d+)</offset>`)
	sizeRE = regexp.MustCompile(`<size>(\d+)</size>`)
)

// readXar reads back what buildXar writes: enough of the format for tests
// and for anything else that inspects a package. It verifies the TOC checksum.
func readXar(blob []byte) (string, map[string][]byte, error) {
	if len(blob) < 28 || string(blob[:4]) != "xar!" {
		return "", nil, fmt.Errorf("not a xar archive")
	}
	hsize := int(binary.BigEndian.Uint16(blob[4:]))
	clen := int(binary.BigEndian.Uint64(blob[8:]))
	zr, err := zlib.NewReader(bytes.NewReader(blob[hsize : hsize+clen]))
	if err != nil {
		return "", nil, err
	}
	tb, err := io.ReadAll(zr)
	if err != nil {
		return "", nil, err
	}
	heap := blob[hsize+clen:]
	if want := sha1.Sum(blob[hsize : hsize+clen]); !bytes.Equal(heap[:sha1.Size], want[:]) {
		return "", nil, fmt.Errorf("TOC checksum mismatch")
	}
	toc := string(tb)
	members := map[string][]byte{}
	for _, m := range fileRE.FindAllStringSubmatch(toc, -1) {
		n, o, s := nameRE.FindStringSubmatch(m[1]), offRE.FindStringSubmatch(m[1]), sizeRE.FindStringSubmatch(m[1])
		if n == nil || o == nil || s == nil {
			continue
		}
		off, _ := strconv.Atoi(o[1])
		size, _ := strconv.Atoi(s[1])
		// <offset>/<size> of the TOC checksum element also match; entries
		// come from <file> bodies only, whose first <offset> is the data's.
		members[n[1]] = heap[off : off+size]
	}
	return toc, members, nil
}

// packageInfo is the package's PackageInfo document.
func packageInfo(identifier, version string) []byte {
	return []byte("<?xml version=\"1.0\" encoding=\"utf-8\" standalone=\"no\"?>\n" +
		"<pkg-info format-version=\"2\" identifier=\"" + identifier + "\" version=\"" + version + "\" install-location=\"/\" auth=\"root\">\n" +
		"    <payload installKBytes=\"0\" numberOfFiles=\"0\"/>\n" +
		"    <scripts>\n" +
		"        <postinstall file=\"./postinstall\"/>\n" +
		"    </scripts>\n" +
		"</pkg-info>\n")
}

// flatPackage is a payload-free component package: PackageInfo, and
// Scripts holding exactly one file, ./postinstall (mode 0755).
func flatPackage(postinstall []byte, identifier, version string) ([]byte, error) {
	scripts, err := gzipDeterministic(makeODC([]cpioEntry{
		{Name: ".", Mode: 0o755, Dir: true},
		{Name: "./postinstall", Mode: 0o755, Data: postinstall},
	}))
	if err != nil {
		return nil, err
	}
	return buildXar([]member{
		{Name: "PackageInfo", Mode: 0o644, Data: packageInfo(identifier, version)},
		{Name: "Scripts", Mode: 0o644, Data: scripts},
	})
}

// hasXarMagic reports whether path starts with "xar!", the magic every
// flat package (and so every package Build is handed) carries. fetch
// checks the same for what it downloads, with its own HasXarMagic: payload
// does not import fetch.
func hasXarMagic(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(f, b); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return string(b) == "xar!", nil
}
