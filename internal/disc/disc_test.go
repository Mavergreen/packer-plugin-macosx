package disc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// apmBlock is the block size of a retail disc's partition map: the 10.6
// retail DVD's driver descriptor says 2048 (MEASURED 2026-10-04).
const apmBlock = 2048

// apmImage is a small disc laid out like the 10.6 retail DVD: a driver
// descriptor, the map, a Boot Camp ATAPI region, then the Apple_HFS
// volume, whose bytes are vol.
func apmImage(t *testing.T, vol []byte) []byte {
	t.Helper()
	hfsBlocks := (len(vol) + apmBlock - 1) / apmBlock
	img := make([]byte, (6+hfsBlocks)*apmBlock)
	copy(img, "ER")
	binary.BigEndian.PutUint16(img[2:], apmBlock)
	entries := []struct {
		start, count uint32
		name, typ    string
	}{
		{1, 3, "Apple", "Apple_partition_map"},
		{4, 2, "Macintosh", "Apple_Driver_ATAPI"},
		{6, uint32(hfsBlocks), "Mac_OS_X", "Apple_HFS"},
	}
	for i, e := range entries {
		b := img[(1+i)*apmBlock:]
		copy(b, "PM")
		binary.BigEndian.PutUint32(b[4:], uint32(len(entries)))
		binary.BigEndian.PutUint32(b[8:], e.start)
		binary.BigEndian.PutUint32(b[12:], e.count)
		copy(b[16:48], e.name)
		copy(b[48:80], e.typ)
	}
	copy(img[6*apmBlock:], vol)
	return img
}

// hfsVolume is a bare volume: HFS+'s signature at byte 1024, then a
// recognizable tail.
func hfsVolume(size int) []byte {
	v := make([]byte, size)
	copy(v[1024:], "H+")
	copy(v[size-8:], "THE-END!")
	return v
}

func udifImage() []byte {
	b := make([]byte, 4096)
	copy(b[len(b)-512:], "koly")
	return b
}

func write(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func detect(t *testing.T, b []byte) (Format, error) {
	t.Helper()
	return Detect(bytes.NewReader(b), int64(len(b)))
}

func TestDetectAPM(t *testing.T) {
	if f, err := detect(t, apmImage(t, hfsVolume(8192))); err != nil || f != APM {
		t.Fatalf("Detect = %v, %v; want APM", f, err)
	}
}

func TestDetectBareHFS(t *testing.T) {
	if f, err := detect(t, hfsVolume(8192)); err != nil || f != BareHFS {
		t.Fatalf("Detect = %v, %v; want BareHFS", f, err)
	}
}

func TestDetectUDIF(t *testing.T) {
	if f, err := detect(t, udifImage()); err != nil || f != UDIF {
		t.Fatalf("Detect = %v, %v; want UDIF", f, err)
	}
}

func TestDetectRefusesUnknown(t *testing.T) {
	_, err := detect(t, make([]byte, 4096))
	if err == nil || !strings.Contains(err.Error(), "00000000") {
		t.Fatalf("err = %v; want one naming the first bytes, 00000000", err)
	}
}

func TestHFSPartitionFindsTheHFSEntry(t *testing.T) {
	vol := hfsVolume(3 * apmBlock)
	off, n, err := HFSPartition(bytes.NewReader(apmImage(t, vol)))
	if err != nil {
		t.Fatal(err)
	}
	if off != 6*apmBlock || n != int64(len(vol)) {
		t.Fatalf("HFSPartition = %d, %d; want %d, %d", off, n, 6*apmBlock, len(vol))
	}
}

func TestHFSPartitionRefusesNoHFS(t *testing.T) {
	img := apmImage(t, hfsVolume(apmBlock))
	copy(img[3*apmBlock+48:3*apmBlock+80], make([]byte, 32))
	copy(img[3*apmBlock+48:], "Apple_Free")
	_, _, err := HFSPartition(bytes.NewReader(img))
	if err == nil || !strings.Contains(err.Error(), "Apple_HFS") {
		t.Fatalf("err = %v; want one naming Apple_HFS", err)
	}
}

func TestExtractAPMCopiesTheVolume(t *testing.T) {
	vol := hfsVolume(3 * apmBlock)
	image := write(t, "Snow Leopard retail.iso", apmImage(t, vol))
	out := filepath.Join(t.TempDir(), "volume.hfs")
	if err := Extract(context.Background(), &proc.Fake{}, image, out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, vol) {
		t.Fatalf("extracted %d bytes that are not the volume's %d", len(got), len(vol))
	}
}

func TestExtractBareHFSCopiesTheFile(t *testing.T) {
	vol := hfsVolume(8192)
	out := filepath.Join(t.TempDir(), "volume.hfs")
	if err := Extract(context.Background(), &proc.Fake{}, write(t, "v.hfs", vol), out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, vol) {
		t.Fatal("the bare volume was not copied as it is")
	}
}

func TestExtractUDIFRunsDmg2img(t *testing.T) {
	vol := hfsVolume(3 * apmBlock)
	raw := apmImage(t, vol)
	image := write(t, "Snow Leopard.dmg", udifImage())
	f := &proc.Fake{Handle: func(c proc.Cmd) error {
		if c.Name != "dmg2img" {
			return errors.New("unexpected " + c.Name)
		}
		for i, a := range c.Args {
			if a == "-o" {
				return os.WriteFile(c.Args[i+1], raw, 0o644)
			}
		}
		return errors.New("no -o")
	}}
	out := filepath.Join(t.TempDir(), "volume.hfs")
	if err := Extract(context.Background(), f, image, out); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "dmg2img" || !strings.Contains(strings.Join(f.Calls[0].Args, " "), "-i "+image) {
		t.Fatalf("calls = %v; want one dmg2img -i %s", f.Calls, image)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, vol) {
		t.Fatal("the volume inside the converted image was not extracted")
	}
	entries, _ := os.ReadDir(filepath.Dir(out))
	if len(entries) != 1 {
		t.Fatalf("left %d files beside the volume; want the volume alone", len(entries))
	}
}

func TestExtractLeavesNothingOnFailure(t *testing.T) {
	image := write(t, "x.dmg", udifImage())
	dir := t.TempDir()
	out := filepath.Join(dir, "volume.hfs")
	f := &proc.Fake{Handle: func(proc.Cmd) error { return errors.New("dmg2img: corrupt") }}
	if err := Extract(context.Background(), f, image, out); err == nil {
		t.Fatal("Extract succeeded with a failing dmg2img")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}
}
