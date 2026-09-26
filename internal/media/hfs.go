// Package media builds bootable Mavericks installer media from Apple's
// InstallESD.dmg without a Mac, root, or a host mount: the host makes the
// disk image and the raw conversions, and every read or write of an HFS+
// volume's contents happens as uid 0 inside the privops microVM. It also
// digests what is on the media, and checks Apple's packages against
// what Apple shipped.
package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/diskimg"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// CreateHFS makes img a bare HFS+ volume of mib MiB: a sparse file,
// formatted by mkfs.hfsplus, which writes only metadata. It refuses an
// img that exists, and leaves nothing behind when it fails.
func CreateHFS(ctx context.Context, r proc.Runner, img string, mib int, volname string) error {
	f, err := os.OpenFile(img, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("image already exists: %s", img)
		}
		return err
	}
	err = f.Truncate(int64(mib) << 20)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		var stderr bytes.Buffer
		err = r.Run(ctx, proc.Cmd{Name: "mkfs.hfsplus", Args: []string{"-v", volname, img}, Stderr: &stderr})
		if err != nil {
			err = fmt.Errorf("mkfs.hfsplus failed on %s: %w%s", img, err, detail(stderr.String()))
		}
	}
	if err != nil {
		os.Remove(img)
	}
	return err
}

// detail is a program's stderr, as a suffix for an error message.
func detail(stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return ": " + s
	}
	return ""
}

// CreateHFSGPT makes img a GPT disk with one Apple HFS+ partition at
// 1 MiB holding an HFS+ volume of mib MiB that fills it exactly -- the
// kernel reads the alternate volume header from the end of the block
// device, so a volume one MiB short of its partition does not mount.
// mkfs.hfsplus cannot write at an offset, so it formats a separate
// sparse file, img.hfs-tmp, whose non-empty regions are then copied in.
// One MiB of GPT goes in front and one behind; unlike sgdisk's, the
// GUIDs are derived, not random.
func CreateHFSGPT(ctx context.Context, r proc.Runner, img string, mib int, volname string) (err error) {
	if _, err := os.Lstat(img); err == nil {
		return fmt.Errorf("image already exists: %s", img)
	}
	vol := img + ".hfs-tmp"
	os.Remove(vol) // our own temp name, left by an interrupted run
	if err := CreateHFS(ctx, r, vol, mib, volname); err != nil {
		return err
	}
	defer os.Remove(vol)

	out, err := os.OpenFile(img, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("image already exists: %s", img)
		}
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(img)
		}
	}()
	sectors := uint64(mib+2) * 2048
	if err := out.Truncate(int64(sectors) * diskimg.SectorSize); err != nil {
		return err
	}
	part := diskimg.Partition{
		Type: diskimg.TypeAppleHFS, GUID: diskimg.DerivedGUID("installer media hfs " + volname),
		Name: volname, FirstLBA: 2048, LastLBA: 2048 + uint64(mib)*2048 - 1,
	}
	if err := diskimg.WriteGPT(out, sectors, diskimg.DerivedGUID("installer media disk"), []diskimg.Partition{part}); err != nil {
		return fmt.Errorf("cannot lay out %s: %w", img, err)
	}
	if err := copySparse(out, 2048*diskimg.SectorSize, vol); err != nil {
		return fmt.Errorf("cannot copy the volume into %s: %w", img, err)
	}
	return out.Sync()
}

// copySparse writes src's non-zero 1 MiB blocks into dst at off; the
// rest stays a hole, as dd conv=sparse leaves it. mkfs.hfsplus writes
// about 21 MB of a 6.6 GB volume.
func copySparse(dst *os.File, off int64, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	buf := make([]byte, 1<<20)
	zero := make([]byte, 1<<20)
	for pos := int64(0); ; {
		n, rerr := io.ReadFull(in, buf)
		if n > 0 && !bytes.Equal(buf[:n], zero[:n]) {
			if _, err := dst.WriteAt(buf[:n], off+pos); err != nil {
				return err
			}
		}
		pos += int64(n)
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

const (
	hfsUnmounted    = 0x00000100 // kHFSVolumeUnmountedBit
	hfsInconsistent = 0x00000800 // kHFSVolumeInconsistentBit
)

// MarkClean marks the HFS+ volume at byte start of img cleanly
// unmounted, in place, from the host: Linux's hfsplus mounts read-only,
// silently, a volume whose header does not say so -- the state of any
// media a QEMU guest has booted -- and -o force does not help. It sets
// kHFSVolumeUnmountedBit and
// clears kHFSVolumeInconsistentBit in the volume header and the alternate
// header, and reports what it did to each. The volume's length comes from
// its header, not the file, because a partitioned image is longer than
// the volume inside it. THIS IS NOT A FILESYSTEM CHECK: use it on a
// volume nothing was writing to.
func MarkClean(img string, start int64) ([]string, error) {
	f, err := os.OpenFile(img, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var geo [8]byte
	if _, err := f.ReadAt(geo[:], start+1024+40); err != nil {
		return nil, fmt.Errorf("cannot read the volume header at %d: %w", start+1024, err)
	}
	// Two 32-bit fields, whose product needs 64 unsigned bits: a corrupt
	// header must be refused for what it claims, not for what that
	// wraps to in an int64.
	claimed := uint64(binary.BigEndian.Uint32(geo[0:4])) * uint64(binary.BigEndian.Uint32(geo[4:8]))
	if claimed == 0 || start < 0 || start > fi.Size() || claimed > uint64(fi.Size()-start) {
		return nil, fmt.Errorf("volume at %d claims %d bytes, which does not fit in %s", start, claimed, img)
	}
	length := int64(claimed)
	var report []string
	for _, where := range []int64{start + 1024, start + length - 1024} {
		var head [8]byte
		if _, err := f.ReadAt(head[:], where); err != nil {
			return report, err
		}
		if sig := string(head[:2]); sig != "H+" && sig != "HX" {
			return report, fmt.Errorf("no HFS+ volume header at offset %d (found %q)", where, head[:2])
		}
		attrs := binary.BigEndian.Uint32(head[4:8])
		fixed := (attrs | hfsUnmounted) &^ hfsInconsistent
		if fixed == attrs {
			report = append(report, fmt.Sprintf("%d: already clean (0x%08x)", where, attrs))
			continue
		}
		var w [4]byte
		binary.BigEndian.PutUint32(w[:], fixed)
		if _, err := f.WriteAt(w[:], where+4); err != nil {
			return report, err
		}
		report = append(report, fmt.Sprintf("%d: attributes 0x%08x -> 0x%08x", where, attrs, fixed))
	}
	return report, f.Sync()
}
