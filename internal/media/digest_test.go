package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

const digestListing = "" +
	"1111111111111111111111111111111111111111111111111111111111111111  ./System/Library/CoreServices/boot.efi\n" +
	"2222222222222222222222222222222222222222222222222222222222222222  ./System/Installation/BaseSystem.dmg\n" +
	"3333333333333333333333333333333333333333333333333333333333333333  ./mach_kernel\n"

func listingSHA() string {
	s := sha256.Sum256([]byte(digestListing))
	return hex.EncodeToString(s[:])
}

type digestRig struct {
	b         *Builder
	r         *proc.Fake
	vm        *fakeVM
	img       string
	mkfsSize  int64
	listSize  int64 // the listing disk's size when the microVM got it
	consoleOf func(sha string) string
}

// newDigestRig is a Builder whose fake microVM writes the listing to its
// raw disk and reports it as the content-digest payload does; consoleOf
// is what it prints, given the listing's true sha.
func newDigestRig(t *testing.T) *digestRig {
	t.Helper()
	g := &digestRig{consoleOf: func(sha string) string {
		return fmt.Sprintf("MQG-DIGEST-FILES 3\r\nMQG-DIGEST-HASHED 3\r\nMQG-DIGEST-BYTES 1234\r\nMQG-DIGEST-SIZE %d\r\nMQG-DIGEST-SHA256 %s\r\ndigested 3 files, 1234 bytes\r\n",
			len(digestListing), sha)
	}}
	g.vm = &fakeVM{t: t, digest: func(disks []privops.Disk) string {
		fi, err := os.Stat(disks[1].Path)
		if err != nil {
			t.Fatal(err)
		}
		g.listSize = fi.Size()
		f, err := os.OpenFile(disks[1].Path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteAt([]byte(digestListing), 0); err != nil {
			t.Fatal(err)
		}
		return g.consoleOf(listingSHA())
	}}
	g.img = writeFile(t, filepath.Join(t.TempDir(), "installer-media.img"), "an installer image")
	g.r = fakeTools(func(size int64) error { g.mkfsSize = size; return nil })
	g.b = &Builder{Paths: config.Paths{Home: t.TempDir()}, Runner: g.r, VM: g.vm}
	return g
}

func TestDigest(t *testing.T) {
	g := newDigestRig(t)
	var listing bytes.Buffer
	d, err := g.b.ContentDigest(context.Background(), g.img, &listing)
	if err != nil {
		t.Fatal(err)
	}
	if want := listingSHA() + "  3 files  1234 bytes  0 unreadable"; d.String() != want {
		t.Fatalf("digest %q, want %q", d, want)
	}
	if listing.String() != digestListing {
		t.Fatalf("listing\n%s\nwant\n%s", listing.String(), digestListing)
	}
	if len(g.r.Calls) != 1 || g.r.Calls[0].Name != "mkfs.hfsplus" || len(g.r.Calls[0].Args) != 3 ||
		g.r.Calls[0].Args[0] != "-v" || g.r.Calls[0].Args[1] != "MQG DIGEST" {
		t.Fatalf("ran %v, want one mkfs.hfsplus -v 'MQG DIGEST'", g.r.Calls)
	}
	if g.mkfsSize != 32<<20 {
		t.Fatalf("the scratch volume is %d bytes, want 32 MiB", g.mkfsSize)
	}
	if len(g.vm.calls) != 1 {
		t.Fatalf("passes %q", g.vm.names())
	}
	c := g.vm.calls[0]
	if c.name != "content-digest" || c.target != g.r.Calls[0].Args[2] {
		t.Fatalf("ran %s on %s, want content-digest on the scratch volume %s", c.name, c.target, g.r.Calls[0].Args[2])
	}
	if len(c.disks) != 2 || c.disks[0] != (privops.Disk{Role: "ro", Path: g.img}) || c.disks[1].Role != "raw" {
		t.Fatalf("disks %v, want [ro %s, raw <listing>]", c.disks, g.img)
	}
	if g.listSize != 256<<20 {
		t.Fatalf("the listing disk is %d bytes, want 256 MiB", g.listSize)
	}
	absent(t, filepath.Dir(c.target), filepath.Dir(c.disks[1].Path))

	if d2, err := g.b.ContentDigest(context.Background(), g.img, nil); err != nil || d2 != d {
		t.Fatalf("without a listing: %v, %v", d2, err)
	}
}

func TestDigestRefusesWhatIsNotWholeAndRight(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"a file it could not read", "MQG-DIGEST-HASHED 3", "MQG-DIGEST-HASHED 2",
			"the microVM found 3 files and could only checksum 2 of them: the volume is unreadable in places"},
		{"a count that is not one", "MQG-DIGEST-BYTES 1234", "MQG-DIGEST-BYTES 12x4",
			"the microVM did not report a usable count"},
		{"a count that is missing", "MQG-DIGEST-FILES 3\r\n", "",
			"the microVM did not report a usable count"},
		{"a listing that changed on the way", "MQG-DIGEST-SHA256 ", "MQG-DIGEST-SHA256 0",
			"the listing did not survive the trip out of the microVM"},
		// Build's rule too (privops.Marker): a count printed twice is two
		// answers, and neither is taken -- not even when they agree.
		{"a count printed twice", "MQG-DIGEST-FILES 3\r\n", "MQG-DIGEST-FILES 3\r\nMQG-DIGEST-FILES 3\r\n",
			"the microVM did not report a usable count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newDigestRig(t)
			orig := g.consoleOf
			g.consoleOf = func(sha string) string { return strings.Replace(orig(sha), tc.from, tc.to, 1) }
			var listing bytes.Buffer
			_, err := g.b.ContentDigest(context.Background(), g.img, &listing)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if listing.Len() != 0 {
				t.Fatal("a refused listing was handed on")
			}
			absent(t, filepath.Dir(g.vm.calls[0].target))
		})
	}
}

func TestDigestNeedsAnImage(t *testing.T) {
	g := newDigestRig(t)
	gone := filepath.Join(t.TempDir(), "gone.img")
	if _, err := g.b.ContentDigest(context.Background(), gone, nil); err == nil || !strings.Contains(err.Error(), "no such image: "+gone) {
		t.Fatalf("err = %v", err)
	}
	g.vm.missing = []string{"busybox (not on PATH)"}
	if _, err := g.b.ContentDigest(context.Background(), g.img, nil); err == nil || !strings.Contains(err.Error(), "busybox (not on PATH)") {
		t.Fatalf("err = %v", err)
	}
	if len(g.r.Calls) != 0 || len(g.vm.calls) != 0 {
		t.Fatalf("ran %v and %q", g.r.Calls, g.vm.names())
	}
}
