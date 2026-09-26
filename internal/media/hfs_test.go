package media

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/diskimg"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// golden reads and gunzips a golden image from testdata/golden/<name>.gz
// (testdata/golden/README.md says what each is).
func golden(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join(repo(t), "internal", "media", "testdata", "golden", name+".gz"))
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

const baseSystem = "OS X Base System"

// repo is the repository root, found from this file's location.
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

// fakeMkfs is a Runner whose mkfs.hfsplus calls write on the file it is
// given, sized as CreateHFS truncated it.
func fakeMkfs(t *testing.T, write func(f *os.File, size int64) error) *proc.Fake {
	t.Helper()
	return &proc.Fake{Handle: func(c proc.Cmd) error {
		if c.Name != "mkfs.hfsplus" {
			return fmt.Errorf("unexpected command %s", c)
		}
		f, err := os.OpenFile(c.Args[len(c.Args)-1], os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		return write(f, fi.Size())
	}}
}

func writeHPlus(f *os.File, _ int64) error {
	_, err := f.WriteAt([]byte("H+"), 1024)
	return err
}

func TestCreateHFSGPTLaysOutOneAppleHFSPartition(t *testing.T) {
	const mib = 8
	img := filepath.Join(t.TempDir(), "media.img")
	r := fakeMkfs(t, writeHPlus)
	if err := CreateHFSGPT(context.Background(), r, img, mib, baseSystem); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(img)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(mib+2) << 20; fi.Size() != want {
		t.Fatalf("image is %d bytes, want %d", fi.Size(), want)
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, parts, err := diskimg.ReadGPT(f, uint64(fi.Size())/diskimg.SectorSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("%d partitions, want 1: %+v", len(parts), parts)
	}
	p := parts[0]
	if p.Type != diskimg.TypeAppleHFS || p.FirstLBA != 2048 || p.LastLBA != 2048+mib*2048-1 || p.Name != baseSystem {
		t.Fatalf("partition %+v", p)
	}
	sig := make([]byte, 2)
	if _, err := f.ReadAt(sig, 2048*diskimg.SectorSize+1024); err != nil {
		t.Fatal(err)
	}
	if string(sig) != "H+" {
		t.Fatalf("the partition's volume header begins %q, want H+", sig)
	}
	if len(r.Calls) != 1 {
		t.Fatalf("calls %v", r.Calls)
	}
	args := r.Calls[0].Args
	if len(args) != 3 || args[0] != "-v" || args[1] != baseSystem || !strings.HasSuffix(args[2], ".hfs-tmp") {
		t.Fatalf("mkfs.hfsplus args %q", args)
	}
	if _, err := os.Lstat(args[2]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp volume %s left behind: %v", args[2], err)
	}
}

func TestCreateHFSGPTRefusesAnExistingImage(t *testing.T) {
	img := filepath.Join(t.TempDir(), "media.img")
	if err := os.WriteFile(img, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := fakeMkfs(t, writeHPlus)
	err := CreateHFSGPT(context.Background(), r, img, 8, baseSystem)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(img); string(b) != "mine" {
		t.Fatalf("image changed to %q", b)
	}
	if len(r.Calls) != 0 {
		t.Fatalf("mkfs ran: %v", r.Calls)
	}
}

func TestCreateHFSGPTLeavesNothingWhenMkfsFails(t *testing.T) {
	img := filepath.Join(t.TempDir(), "media.img")
	r := &proc.Fake{Handle: func(c proc.Cmd) error { return &proc.ExitError{Cmd: c.String(), Code: 1} }}
	err := CreateHFSGPT(context.Background(), r, img, 8, baseSystem)
	if err == nil || !strings.Contains(err.Error(), "mkfs") {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{img, img + ".hfs-tmp"} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a failed mkfs: %v", p, err)
		}
	}
}

func allocated(t *testing.T, path string) (alloc, size int64) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no block counts on this platform")
	}
	return int64(st.Blocks) * 512, fi.Size()
}

func TestTheVolumeIsCopiedSparsely(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	f, err := os.Create(probe)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(64 << 20)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if a, s := allocated(t, probe); a >= s/4 {
		t.Skip("this filesystem does not keep holes")
	}
	os.Remove(probe)

	img := filepath.Join(dir, "media.img")
	chunk := bytes.Repeat([]byte{0xa5}, 4096)
	r := fakeMkfs(t, func(f *os.File, size int64) error {
		if _, err := f.WriteAt(chunk, 0); err != nil {
			return err
		}
		_, err := f.WriteAt(chunk, size-4096)
		return err
	})
	if err := CreateHFSGPT(context.Background(), r, img, 64, baseSystem); err != nil {
		t.Fatal(err)
	}
	a, s := allocated(t, img)
	if a >= s/4 {
		t.Fatalf("%d of %d bytes allocated: the volume was not copied sparsely", a, s)
	}
	f, err = os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, off := range []int64{1 << 20, 65<<20 - 4096} {
		got := make([]byte, 4096)
		if _, err := f.ReadAt(got, off); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, chunk) {
			t.Fatalf("the volume's bytes at image offset %d were not copied", off)
		}
	}
}

// sgdiskInfo is `sgdisk -i 1`'s answer, keyed by field name.
func sgdiskInfo(t *testing.T, img string) map[string]string {
	t.Helper()
	out, err := exec.Command("sgdisk", "-i", "1", img).CombinedOutput()
	if err != nil {
		t.Fatalf("sgdisk -i 1 %s: %v\n%s", img, err, out)
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok {
			m[k] = v
		}
	}
	return m
}

// hfsGeometry is the signature, block size and total blocks of the
// volume header at start+1024.
func hfsGeometry(t *testing.T, img string, start int64) (string, uint32, uint32) {
	t.Helper()
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := make([]byte, 48)
	if _, err := f.ReadAt(h, start+1024); err != nil {
		t.Fatal(err)
	}
	return string(h[:2]), binary.BigEndian.Uint32(h[40:44]), binary.BigEndian.Uint32(h[44:48])
}

// TestCreateHFSGPTMatchesItsGolden: testdata/golden/hfs-create-gpt.img.gz
// is a 40 MiB disk laid out the way CreateHFSGPT must lay it out, with an
// HFS+ volume named "OS X Base System". sgdisk judges both images live.
func TestCreateHFSGPTMatchesItsGolden(t *testing.T) {
	need(t, "sgdisk")
	dir := t.TempDir()
	ref, goImg := filepath.Join(dir, "golden.img"), filepath.Join(dir, "go.img")
	if err := os.WriteFile(ref, golden(t, "hfs-create-gpt.img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateHFSGPT(context.Background(), proc.Exec{}, goImg, 40, baseSystem); err != nil {
		t.Fatal(err)
	}
	ri, gi := sgdiskInfo(t, ref), sgdiskInfo(t, goImg)
	for _, k := range []string{"First sector", "Last sector", "Partition GUID code", "Partition name"} {
		if ri[k] == "" || ri[k] != gi[k] {
			t.Errorf("%s: golden %q, Go %q", k, ri[k], gi[k])
		}
	}
	if !strings.Contains(gi["Partition GUID code"], "Apple HFS/HFS+") {
		t.Errorf("partition type %q", gi["Partition GUID code"])
	}
	a, _ := os.Stat(ref)
	b, _ := os.Stat(goImg)
	if a.Size() != b.Size() {
		t.Errorf("golden image %d bytes, Go %d", a.Size(), b.Size())
	}
	rsig, rbs, rtb := hfsGeometry(t, ref, 1<<20)
	gsig, gbs, gtb := hfsGeometry(t, goImg, 1<<20)
	if rsig != "H+" || gsig != "H+" || rbs != gbs || rtb != gtb {
		t.Errorf("volume headers: golden %q %d×%d, Go %q %d×%d", rsig, rbs, rtb, gsig, gbs, gtb)
	}
}

// syntheticVolume writes an HFS+ volume of blocks×4096 bytes at start
// in a file of size bytes, with the given attributes in its header and
// its alternate header.
func syntheticVolume(t *testing.T, path string, size, start int64, blocks uint32, attrs, altAttrs uint32) {
	t.Helper()
	b := make([]byte, size)
	head := func(at int64, a uint32) {
		copy(b[at:], "H+")
		binary.BigEndian.PutUint16(b[at+2:], 4)
		binary.BigEndian.PutUint32(b[at+4:], a)
		binary.BigEndian.PutUint32(b[at+40:], 4096)
		binary.BigEndian.PutUint32(b[at+44:], blocks)
	}
	head(start+1024, attrs)
	head(start+int64(blocks)*4096-1024, altAttrs)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func attrsAt(t *testing.T, path string, at int64) uint32 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return binary.BigEndian.Uint32(b[at+4:])
}

func TestMarkCleanSetsTheBitInBothHeaders(t *testing.T) {
	img := filepath.Join(t.TempDir(), "vol.img")
	syntheticVolume(t, img, 1<<20, 0, 256, 0x800, 0x800)
	changed, err := MarkClean(img, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{1024, 1<<20 - 1024} {
		if got := attrsAt(t, img, at); got != 0x100 {
			t.Errorf("attributes at %d are 0x%08x, want 0x00000100", at, got)
		}
	}
	want := []string{
		"1024: attributes 0x00000800 -> 0x00000100",
		"1047552: attributes 0x00000800 -> 0x00000100",
	}
	if strings.Join(changed, "\n") != strings.Join(want, "\n") {
		t.Fatalf("changes %q, want %q", changed, want)
	}
	again, err := MarkClean(img, 0)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"1024: already clean (0x00000100)", "1047552: already clean (0x00000100)"}
	if strings.Join(again, "\n") != strings.Join(want, "\n") {
		t.Fatalf("second run %q, want %q", again, want)
	}
}

// TestMarkCleanMatchesItsGolden: each case's "want" is the synthetic
// fixture this test builds, marked clean, byte for byte.
func TestMarkCleanMatchesItsGolden(t *testing.T) {
	for _, fx := range []struct {
		name            string
		golden          string
		size, start     int64
		attrs, altAttrs uint32
	}{
		{"bare", "mark-clean-bare.img", 1 << 20, 0, 0x800, 0x800},
		{"at 1 MiB", "mark-clean-at-1mib.img", 3 << 20, 1 << 20, 0x80000800, 0x00000900},
		{"one header clean", "mark-clean-one-header-clean.img", 1 << 20, 0, 0x100, 0x2800},
	} {
		t.Run(fx.name, func(t *testing.T) {
			dir := t.TempDir()
			goImg := filepath.Join(dir, "go.img")
			syntheticVolume(t, goImg, fx.size, fx.start, 256, fx.attrs, fx.altAttrs)
			want := golden(t, fx.golden)
			if _, err := MarkClean(goImg, fx.start); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(goImg)
			if !bytes.Equal(want, b) {
				t.Fatal("the marked volume differs from its golden")
			}
			if attrsAt(t, goImg, fx.start+1024)&0x900 != 0x100 {
				t.Fatal("the fixture was not marked clean")
			}
		})
	}
}

func TestMarkCleanRefusesWhatIsNotHFSPlus(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "vol.img")
	syntheticVolume(t, img, 1<<20, 0, 256, 0x800, 0x800)
	b, _ := os.ReadFile(img)
	copy(b[1024:], "\x00\x00")
	if err := os.WriteFile(img, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := MarkClean(img, 0)
	if err == nil || !strings.Contains(err.Error(), "no HFS+ volume header at offset 1024") {
		t.Fatalf("err = %v", err)
	}

	big := filepath.Join(dir, "big.img")
	syntheticVolume(t, big, 2<<20, 0, 256, 0x800, 0x800)
	if err := os.Truncate(big, 1<<20-1); err != nil {
		t.Fatal(err)
	}
	_, err = MarkClean(big, 0)
	want := fmt.Sprintf("volume at 0 claims %d bytes, which does not fit in %s", 256*4096, big)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A block size and a block count are each 32 bits, and their product is
// not: a corrupt header's length is reported as the header claims it,
// not as it wraps in a signed 64-bit multiply.
func TestMarkCleanRefusesAGeometryThatOverflows(t *testing.T) {
	img := filepath.Join(t.TempDir(), "vol.img")
	syntheticVolume(t, img, 1<<20, 0, 256, 0x800, 0x800)
	b, _ := os.ReadFile(img)
	binary.BigEndian.PutUint32(b[1024+40:], 0xFFFFFFFF)
	binary.BigEndian.PutUint32(b[1024+44:], 0xFFFFFFFF)
	if err := os.WriteFile(img, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := MarkClean(img, 0)
	want := fmt.Sprintf("volume at 0 claims %d bytes, which does not fit in %s", uint64(0xFFFFFFFF)*0xFFFFFFFF, img)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := attrsAt(t, img, 1024); got != 0x800 {
		t.Fatalf("attributes changed to 0x%08x on a refused volume", got)
	}
}
