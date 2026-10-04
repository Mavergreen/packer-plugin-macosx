package media

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
)

// A Digest is what is ON an image, as one checksum: the sha256 of a
// sorted list of "<sha256>  <path>", one line per file, which the media
// data source outputs as content_digest. The image's own checksum cannot say this -- mkfs
// stamps the clock, a mount rewrites the header, and layout follows
// write order -- and mtimes, owners and modes are left out because none
// is what an installer reads.
type Digest struct {
	SHA256       string
	Files, Bytes int64
}

// String is the digest's summary line. Unreadable is always
// 0 -- the microVM is uid 0 -- and the field stays so the line keeps its
// shape for anything parsing it.
func (d Digest) String() string {
	return fmt.Sprintf("%s  %d files  %d bytes  0 unreadable", d.SHA256, d.Files, d.Bytes)
}

// ContentDigest digests img's contents in the privops microVM, which
// mounts it read-only off a read-only disk, so taking a digest cannot
// alter the image; the host mounts nothing. listing, if not nil, gets
// the per-file lines, so two builds can be diffed and not only compared.
//
// The listing comes back on a raw disk, not the console: it is about
// 4 MB for real media, and the console carries it one character at a
// time. A count that does not match -- files found, files hashed -- is
// refused, not digested: a digest of what happened to be readable would
// be a digest of nothing in particular.
func (b *Builder) ContentDigest(ctx context.Context, img string, listing io.Writer) (Digest, error) {
	if !config.RegularFile(img) {
		return Digest{}, fmt.Errorf("no such image: %s", img)
	}
	if _, err := b.Runner.LookPath("mkfs.hfsplus"); err != nil {
		return Digest{}, fmt.Errorf("the content digest needs mkfs.hfsplus (not on PATH)")
	}
	// Named, all of them, before anything else happens: a host learns
	// what it lacks in a second rather than after a boot.
	if m := b.VM.Missing(); len(m) > 0 {
		for _, l := range m {
			b.logf("  missing: %s", l)
		}
		return Digest{}, fmt.Errorf("the privops microVM is not available on this host, and it is how this reads an HFS+ volume at all -- nothing here mounts anything or installs anything. Missing: %s", strings.Join(m, "; "))
	}
	tmp, err := os.MkdirTemp("", "mavericks-digest-")
	if err != nil {
		return Digest{}, err
	}
	defer os.RemoveAll(tmp)

	// The backend mounts its first disk read-write, and this payload
	// writes nothing to it: a throwaway 32 MiB volume, sparse, of which
	// mkfs writes about 2 MB.
	scratch := filepath.Join(tmp, "scratch.img")
	if err := CreateHFS(ctx, b.Runner, scratch, 32, "MQG DIGEST"); err != nil {
		return Digest{}, err
	}
	// Sparse, so its size is a ceiling and not a cost; the guest refuses
	// rather than truncates a listing that would overflow it.
	list := filepath.Join(tmp, "listing.txt")
	if err := truncateNew(list, 256<<20); err != nil {
		return Digest{}, err
	}
	payload, err := fs.ReadFile(macosx.Files, "assets/privops/content-digest.sh")
	if err != nil {
		return Digest{}, err
	}
	console, err := b.VM.Run(ctx, scratch, payload,
		[]privops.Disk{{Role: "ro", Path: img}, {Role: "raw", Path: list}})
	if err != nil {
		return Digest{}, err
	}

	// Each marker is read by privops.Marker's rule, as the build reads
	// its own: printed once, or not an answer.
	raw := [4]string{marker(console, "MQG-DIGEST-FILES"), marker(console, "MQG-DIGEST-HASHED"),
		marker(console, "MQG-DIGEST-BYTES"), marker(console, "MQG-DIGEST-SIZE")}
	var n [4]int64
	for i, s := range raw {
		var ok bool
		if n[i], ok = count(s); !ok {
			return Digest{}, fmt.Errorf("the microVM did not report a usable count (files=%q hashed=%q bytes=%q size=%q)",
				raw[0], raw[1], raw[2], raw[3])
		}
	}
	files, hashed, total, size := n[0], n[1], n[2], n[3]
	if files != hashed {
		return Digest{}, fmt.Errorf("the microVM found %d files and could only checksum %d of them: the volume is unreadable in places, and a digest of what happened to be readable would be a digest of nothing in particular", files, hashed)
	}
	if err := os.Truncate(list, size); err != nil {
		return Digest{}, err
	}
	// The host's own read, against what the guest read back off the
	// device: a short or torn write through the raw disk would otherwise
	// be a digest that is simply wrong, with nothing to say so.
	want := marker(console, "MQG-DIGEST-SHA256")
	got, err := fetch.SHA256File(list)
	if err != nil {
		return Digest{}, err
	}
	if got != want {
		return Digest{}, fmt.Errorf("the listing did not survive the trip out of the microVM: the guest read %s and this host reads %s", want, got)
	}
	if listing != nil {
		f, err := os.Open(list)
		if err != nil {
			return Digest{}, err
		}
		_, err = io.Copy(listing, f)
		f.Close()
		if err != nil {
			return Digest{}, err
		}
	}
	return Digest{SHA256: got, Files: hashed, Bytes: total}, nil
}
