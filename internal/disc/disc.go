// Package disc reads an installer disc image the user supplies -- the
// plugin never downloads one (docs/decisions/0007) -- and finds the HFS+
// volume inside it, whatever tool made the image: a retail DVD ripped to
// an ISO or a Disk Utility "DVD/CD master" (an Apple Partition Map), a
// UDIF .dmg, or the volume alone.
package disc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// Format is how an image holds its volume.
type Format int

const (
	APM     Format = iota + 1 // an Apple Partition Map: a ripped disc
	UDIF                      // a .dmg, which dmg2img turns into a raw image
	BareHFS                   // the HFS+ volume alone
)

func (f Format) String() string {
	switch f {
	case APM:
		return "Apple Partition Map"
	case UDIF:
		return "UDIF disk image"
	case BareHFS:
		return "bare HFS+ volume"
	}
	return fmt.Sprintf("Format(%d)", int(f))
}

// Detect says which format r is, from its content and never a name: an
// APM's driver descriptor starts "ER"; a UDIF image ends in a 512-byte
// trailer starting "koly"; an HFS+ volume's header, at byte 1024, starts
// "H+".
func Detect(r io.ReaderAt, size int64) (Format, error) {
	head := make([]byte, 4)
	if _, err := r.ReadAt(head, 0); err != nil {
		return 0, fmt.Errorf("reading the image's first bytes: %w", err)
	}
	if bytes.HasPrefix(head, []byte("ER")) {
		return APM, nil
	}
	if size >= 512 {
		trailer := make([]byte, 4)
		if _, err := r.ReadAt(trailer, size-512); err == nil && string(trailer) == "koly" {
			return UDIF, nil
		}
	}
	sig := make([]byte, 2)
	if _, err := r.ReadAt(sig, 1024); err == nil && string(sig) == "H+" {
		return BareHFS, nil
	}
	return 0, fmt.Errorf("not a disc image this plugin reads: it starts %x, which is no Apple Partition Map, UDIF image or HFS+ volume", head)
}

// HFSPartition is the byte range of the first Apple_HFS partition in an
// APM image. The map's block size is the driver descriptor's (2048 on a
// retail DVD), and its entries follow at block 1.
func HFSPartition(r io.ReaderAt) (off, length int64, err error) {
	ddm := make([]byte, 4)
	if _, err := r.ReadAt(ddm, 0); err != nil {
		return 0, 0, err
	}
	bs := int64(binary.BigEndian.Uint16(ddm[2:4]))
	if bs == 0 {
		return 0, 0, errors.New("the partition map's driver descriptor gives a block size of 0")
	}
	entry := make([]byte, 80)
	count := int64(1)
	for i := int64(1); i <= count; i++ {
		if _, err := r.ReadAt(entry, i*bs); err != nil {
			return 0, 0, fmt.Errorf("reading partition map entry %d: %w", i, err)
		}
		if string(entry[:2]) != "PM" {
			return 0, 0, fmt.Errorf("partition map entry %d has no PM signature", i)
		}
		count = int64(binary.BigEndian.Uint32(entry[4:8]))
		if string(bytes.TrimRight(entry[48:80], "\x00")) == "Apple_HFS" {
			start := int64(binary.BigEndian.Uint32(entry[8:12]))
			blocks := int64(binary.BigEndian.Uint32(entry[12:16]))
			return start * bs, blocks * bs, nil
		}
	}
	return 0, 0, errors.New("the partition map has no Apple_HFS partition")
}

// Extract writes image's HFS+ volume to out: converted with dmg2img
// first if it is a UDIF image, cut out of the partition map if it has
// one, or copied as it is. out appears only complete: every write goes to
// a temp file beside it, renamed into place, and a failure leaves
// nothing behind.
func Extract(ctx context.Context, run proc.Runner, image, out string) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()

	src := image
	f, size, err := openSized(src)
	if err != nil {
		return err
	}
	format, err := Detect(f, size)
	f.Close()
	if err != nil {
		return err
	}
	if format == UDIF {
		raw := tmp.Name() + ".raw"
		defer os.Remove(raw)
		var stderr bytes.Buffer
		if err := run.Run(ctx, proc.Cmd{Name: "dmg2img", Args: []string{"-s", "-i", image, "-o", raw}, Stderr: &stderr}); err != nil {
			return fmt.Errorf("dmg2img failed on %s: %w %s", image, err, stderr.String())
		}
		src = raw
		if f, size, err = openSized(src); err != nil {
			return err
		}
		format, err = Detect(f, size)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s, converted: %w", image, err)
		}
	}

	f, size, err = openSized(src)
	if err != nil {
		return err
	}
	defer f.Close()
	off, n := int64(0), size
	if format == APM {
		if off, n, err = HFSPartition(f); err != nil {
			return fmt.Errorf("%s: %w", image, err)
		}
	}
	if _, err := io.Copy(tmp, io.NewSectionReader(f, off, n)); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}

func openSized(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}
