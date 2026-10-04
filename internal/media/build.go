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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/disc"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/lock"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

const (
	// ReferencePartitionBytes is the Mac-made reference's HFS+ partition,
	// measured (7z l InstallMavericks.iso: Physical Size of its "disk
	// image.hfs"), not guessed. HFS+ must fill its partition exactly and
	// mkfs works in whole MiB, so the partition rounds it up.
	ReferencePartitionBytes = 6550020096
	// MarginMiB is added because a Linux-built copy of the same files
	// needs a larger catalog: about 153 MB of metadata here against 105
	// on the Mac, and the reference had 30 MB free. It was raised from
	// 128 to 512 on a theory about corruption that turned out false, and
	// stays because it costs nothing in a sparse image: do not cite it as
	// a fix for anything.
	MarginMiB = 512
	// VolumeName is the media's volume, as Apple names it.
	VolumeName = "OS X Base System"
)

// BasePartMiB is the partition's size with nothing extra carried: the
// reference rounded up to whole MiB, plus the margin. No extra space
// must reproduce it exactly, because later builds are measured against
// media of exactly this geometry.
func BasePartMiB() int { return (ReferencePartitionBytes+1<<20-1)>>20 + MarginMiB }

// UpdatesExtraMiB is the room the update packages need on the media: the
// margin is not spare room -- it leaves about 484 MiB free, and
// updates "all" is 685 MiB -- so extra cargo brings its own. It is their
// size rounded up to whole MiB, plus 64, or 0 for none.
func UpdatesExtraMiB(pkgs []string) (int, error) {
	if len(pkgs) == 0 {
		return 0, nil
	}
	var total int64
	for _, p := range pkgs {
		fi, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		total += fi.Size()
	}
	return int((total+1<<20-1)>>20) + 64, nil
}

// MicroVM runs a payload as uid 0 with target and disks attached, and
// returns its console. privops.Backend is one; tests fake it.
type MicroVM interface {
	Run(ctx context.Context, target string, payload []byte, disks []privops.Disk) ([]byte, error)
	Missing() []string
}

var _ MicroVM = privops.Backend{}

// Options is one media build's choices.
type Options struct {
	Injectables
	ExtraSpaceMiB int  // enlarges the partition beyond BasePartMiB
	Force         bool // replaces existing media
	KeepWork      bool // keeps the multi-gigabyte raw conversions
}

// Builder builds installer media from InstallESD.dmg. Every program runs
// through Runner, and every read or write of an HFS+ volume's contents
// happens inside VM: NOTHING HERE MOUNTS ANYTHING, because a host mount
// needs a desktop seat (udisks2's polkit refuses loop-setup over SSH), so
// a headless host -- every CI runner -- could not build media at all.
type Builder struct {
	Paths  config.Paths
	Runner proc.Runner
	VM     MicroVM
	PID    int // the lock's holder; 0 is this process (lock.Acquire's default)
	Log    func(string, ...any)

	baseMiB int // tests only: the partition before ExtraSpaceMiB, when not BasePartMiB
	// afterMediaRename, tests only, runs once the media is in place.
	afterMediaRename func()
}

func (b *Builder) logf(f string, a ...any) {
	if b.Log != nil {
		b.Log(f, a...)
	}
}

func (b *Builder) partMiB(o Options) int {
	base := b.baseMiB
	if base == 0 {
		base = BasePartMiB()
	}
	return base + o.ExtraSpaceMiB
}

// The media build's scratch, in Paths.MediaWork(). Ours alone: they are
// removed before a build and, unless KeepWork, after it.
const (
	workESD     = "esd.img"
	workBSDmg   = "basesystem.dmg" // the raw disk BaseSystem.dmg comes out on
	workBSImg   = "basesystem.img"
	workTar     = "inject.tar" // the raw disk the injectables go in on
	workConsole = "console.txt"
)

var workFiles = []string{workESD, workBSDmg, workBSImg, workTar, workConsole}

func checkSpace(o Options) error {
	if o.ExtraSpaceMiB < 0 {
		return fmt.Errorf("the extra space wants a whole number of MiB, not %d", o.ExtraSpaceMiB)
	}
	return nil
}

// Validate is Build's cheap refusals, which need neither the ESD nor the
// microVM: the extra space, each package (there, and a flat package), two
// packages on one name, and media already in place without Force. Build
// asks it first, and so does the media data source before fetching the
// ESD: a missing package should cost a second, not a 5.2 GB download and
// twenty minutes. It reads and writes nothing else. The media's
// existence is asked again under the lock, where the answer cannot
// change.
func (b *Builder) Validate(o Options) error {
	if err := checkSpace(o); err != nil {
		return err
	}
	if o.FirstbootPkg != "" {
		if !config.RegularFile(o.FirstbootPkg) {
			return fmt.Errorf("no first-boot package at %s -- build one first", o.FirstbootPkg)
		}
		if err := xarMagic(o.FirstbootPkg); err != nil {
			return err
		}
	}
	for _, e := range o.ExtraPkgs {
		if !config.RegularFile(e) {
			return fmt.Errorf("no such extra package: %s", e)
		}
		if err := xarMagic(e); err != nil {
			return err
		}
	}
	if _, err := o.packages(); err != nil {
		return err
	}
	return b.checkExisting(o)
}

// checkExisting refuses media already in place, unless o.Force.
func (b *Builder) checkExisting(o Options) error {
	out := b.Paths.InstallerMedia()
	if _, err := os.Lstat(out); err == nil && !o.Force {
		return fmt.Errorf("%s exists, and the build was not told to replace it (Options.Force)", out)
	}
	return nil
}

// acquire is lock.Acquire; a test replaces it.
var acquire = lock.Acquire

// sidecarTempPrefix is the name, within build/, of a sidecar this
// package stages before renaming it into place: any file with it is one
// a killed build left.
func sidecarTempPrefix(out string) string { return "." + filepath.Base(out) + ".sha256.tmp-" }

// staleSidecarTemps is every file beside out with sidecarTempPrefix. The
// directory is read, not globbed: a home holding a glob character --
// "[" is malformed, "a[1]" matches "a1" -- is taken literally.
func staleSidecarTemps(out string) ([]string, error) {
	dir := filepath.Dir(out)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var p []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), sidecarTempPrefix(out)) {
			p = append(p, filepath.Join(dir, e.Name()))
		}
	}
	return p, nil
}

// Preflight is whether this host can build media at all: the privops
// microVM's requirements (VM.Missing) and dmg2img and mkfs.hfsplus on
// PATH, every missing one named at once. Build asks it first, and so
// does the media data source before it fetches the ESD: a host that
// cannot boot the microVM should find out in a second and by name, not
// after a 5.2 GB download or twenty minutes of dmg2img. It reads nothing
// but PATH and the backend's own checks.
func (b *Builder) Preflight() error {
	var parts []string
	if m := b.VM.Missing(); len(m) > 0 {
		for _, l := range m {
			b.logf("  missing: %s", l)
		}
		parts = append(parts, fmt.Sprintf("the privops microVM is not available on this host, and it is how the media is built at all -- nothing here installs anything. Missing: %s", strings.Join(m, "; ")))
	}
	var tools []string
	for _, tool := range []string{"dmg2img", "mkfs.hfsplus"} {
		if _, err := b.Runner.LookPath(tool); err != nil {
			tools = append(tools, tool+" (not on PATH)")
		}
	}
	if len(tools) > 0 {
		parts = append(parts, "the media build needs "+strings.Join(tools, " and "))
	}
	if len(parts) > 0 {
		return errors.New(strings.Join(parts, "; and "))
	}
	return nil
}

// Build makes the installer media from esd and returns its path,
// Paths.InstallerMedia(), with a .sha256 sidecar beside it:
//
//  1. dmg2img the ESD.
//  2. Create a GPT image with one AF00 partition holding an "OS X Base
//     System" volume, sized from the reference.
//  3. microVM pass 1: copy the ESD's BaseSystem.dmg onto a raw disk,
//     since dmg2img runs here and cannot read an HFS+ volume; dmg2img it.
//  4. Pass 2: copy BaseSystem onto the media, replace its dangling
//     Packages symlink with the ESD's real Packages, add BaseSystem.dmg
//     and its chunklist, and untar the injectables.
//  5. Pass 3: restore root ownership.
//  6. Pass 4, in a microVM of its own: read the Packages back and check
//     them against Apple's pinned checksums.
//
// Four boots rather than one, at about four seconds each: the price of a
// host that needs no desktop seat. The image is built as
// InstallerMedia()+".building" and renamed into place only once it is
// verified, so a killed build never leaves media that looks finished.
func (b *Builder) Build(ctx context.Context, esd string, o Options) (_ string, err error) {
	return b.buildWith(o, func() error {
		if !config.RegularFile(esd) {
			return fmt.Errorf("no InstallESD.dmg at %s -- fetch it first", esd)
		}
		return nil
	}, func(work, building string) (string, error) {
		return b.build(ctx, esd, o, work, building)
	})
}

// BuildFromVolume builds installer media from a retail disc's verified
// HFS+ volume (internal/disc): a fresh volume as large as the disc's
// used space, MarginMiB and o.ExtraSpaceMiB, the disc copied onto it
// whole, the release's injectables unpacked over it, root ownership
// restored, and the copy read back in a microVM of its own against the
// known disc's pinned packages. The volume is only ever attached
// read-only.
func (b *Builder) BuildFromVolume(ctx context.Context, volume, release string, o Options) (string, error) {
	o.Release = release
	return b.buildWith(o, func() error {
		if !config.RegularFile(volume) {
			return fmt.Errorf("no installer volume at %s", volume)
		}
		return nil
	}, func(work, building string) (string, error) {
		return b.buildFromVolume(ctx, volume, o, work, building)
	})
}

// buildWith is what every media build shares around its build proper:
// the checks, the one-builder lock, the scratch swept before and after,
// the sidecar, and the rename into place. check runs before the lock.
func (b *Builder) buildWith(o Options, check func() error, build func(work, building string) (string, error)) (_ string, err error) {
	started := time.Now()
	out := b.Paths.InstallerMedia()
	// Packages are checked now, not when they are copied: a missing one
	// should cost a second, not twenty minutes.
	if err := b.Validate(o); err != nil {
		return "", err
	}
	if err := b.Preflight(); err != nil {
		return "", err
	}
	if err := check(); err != nil {
		return "", err
	}

	// ONE BUILDER PER IMAGE FILE. Two builders writing one image each
	// read back their own page cache and see nothing wrong, while the
	// file on disk is a mix of both: the one mechanism of media
	// corruption ever caught in the act.
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	l, err := acquire(out+".lock", b.PID)
	if err != nil {
		return "", err
	}
	if !l.Serialized {
		b.logf("warning: the filesystem refused flock on %s: the build lock is still exclusive, but a stale one may have been taken over by two builders at once", filepath.Dir(out))
	}
	switch {
	case l.TookOver > 0:
		b.logf("taking over a stale lock left by pid %d", l.TookOver)
	case l.TookOver < 0:
		b.logf("taking over a stale lock left by an unknown pid")
	}
	// A lock that cannot be released after a build that succeeded fails
	// the build, whose path is still returned: the media is in place.
	defer func() {
		if rerr := l.Release(); err == nil && rerr != nil {
			err = rerr
		}
	}()

	// Validate asked this before the lock; under it the answer holds.
	if err := b.checkExisting(o); err != nil {
		return "", err
	}
	if _, serr := os.Lstat(out); serr == nil {
		// Kept until the new media is verified, and then replaced by one
		// rename: a forced build that fails leaves what was there.
		b.logf("replacing %s once the new media is verified", out)
	}

	work := b.Paths.MediaWork()
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	// Regenerated every run, never reused: they cost seconds, next to a
	// stale or half-written one silently becoming media that then costs
	// an hour of booting. The .building file is only ever a killed build.
	building := out + ".building"
	stale, err := staleSidecarTemps(out)
	if err != nil {
		return "", err
	}
	for _, p := range append(append(inDir(work, workFiles...), building, building+".hfs-tmp"), stale...) {
		if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return "", rerr
		}
	}
	defer func() {
		if err != nil {
			os.Remove(building)
		}
	}()

	sum, err := build(work, building)
	if err != nil {
		return "", err
	}

	// The sidecar carries its expiry: mounting HFS+ read-write rewrites
	// the volume header, so the first mount after this -- a microVM, a
	// guest booting the media -- changes the file. That is the media, not
	// corruption. (sha256sum -c ignores the # lines.)
	side, err := os.CreateTemp(filepath.Dir(out), sidecarTempPrefix(out)+"*")
	if err != nil {
		return "", err
	}
	defer os.Remove(side.Name())
	_, err = fmt.Fprintf(side, "# sha256 of %s as built at %s\n"+
		"# Mounting the image invalidates this: HFS+ records the mount\n"+
		"# in its volume header, and a read-write mount rewrites it.\n"+
		"%s  %s\n", filepath.Base(out), time.Now().UTC().Format("2006-01-02T15:04:05Z"), sum, filepath.Base(out))
	if err == nil {
		err = side.Sync()
	}
	if cerr := side.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(side.Name(), 0o644)
	}
	if err != nil {
		return "", err
	}
	// The old sidecar goes first, so that no sidecar ever describes an
	// image it was not written for; the rename then replaces any old
	// media in one step.
	oldSidecar := false
	if err := os.Remove(out + ".sha256"); err == nil {
		oldSidecar = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(building, out); err != nil {
		if oldSidecar {
			return "", fmt.Errorf("the old media at %s is intact, but its sidecar was removed: cannot rename the new media over it: %w", out, err)
		}
		return "", fmt.Errorf("cannot rename the new media into place: %w", err)
	}
	if b.afterMediaRename != nil {
		b.afterMediaRename()
	}
	if err := os.Rename(side.Name(), out+".sha256"); err != nil {
		return out, fmt.Errorf("%s is in place, but its sidecar is missing: %w -- its sha256 is %s", out, err, sum)
	}

	// The media is built and in place: nothing after this can fail the
	// build, only warn.
	if !o.KeepWork {
		b.logf("removing the raw conversions (Options.KeepWork keeps them)")
		for _, p := range inDir(work, workFiles...) {
			if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				b.logf("warning: cannot remove %s: %v", p, rerr)
			}
		}
		os.Remove(work) // only if empty: what else is there is not ours
	}
	b.logf("built %s in %s", out, time.Since(started).Round(time.Second))
	if fi, serr := os.Stat(out); serr != nil {
		b.logf("warning: cannot read %s back: %v", out, serr)
	} else {
		b.logf("size %d bytes, sha256 %s", fi.Size(), sum)
	}
	return out, nil
}

// build is the build proper, into building, and returns its sha256.
func (b *Builder) build(ctx context.Context, esd string, o Options, work, building string) (string, error) {
	esdImg, bsDmg, bsImg, tarPath := filepath.Join(work, workESD), filepath.Join(work, workBSDmg),
		filepath.Join(work, workBSImg), filepath.Join(work, workTar)

	b.logf("converting InstallESD.dmg to raw (about 5 GB)")
	if err := b.dmg2img(ctx, esd, esdImg); err != nil {
		return "", fmt.Errorf("dmg2img failed on %s: %w", esd, err)
	}
	esdSize, err := fileSize(esdImg)
	if err != nil {
		return "", err
	}
	b.logf("ESD raw image: %d bytes", esdSize)

	// Created before anything is copied: pass 1 needs a target, since the
	// backend always mounts its first disk, and an empty volume will do.
	part := b.partMiB(o)
	b.logf("creating %s: GPT, one AF00 partition, %d MiB, %q", building, part, VolumeName)
	if err := CreateHFSGPT(ctx, b.Runner, building, part, VolumeName); err != nil {
		return "", err
	}

	// Pass 1. BaseSystem.dmg is inside the ESD volume, UDIF-compressed:
	// only dmg2img decodes it, and dmg2img runs here, which cannot read
	// the ESD. So the guest writes it to a raw disk -- a plain file here,
	// sparse and as large as the ESD image, which costs nothing unwritten.
	b.logf("bringing BaseSystem.dmg out of the ESD (microVM pass 1 of 4)")
	if err := truncateNew(bsDmg, esdSize); err != nil {
		return "", err
	}
	console, err := b.pass(ctx, 1, "extract-basesystem", building, work,
		privops.Disk{Role: "ro", Path: esdImg}, privops.Disk{Role: "raw", Path: bsDmg})
	if err != nil {
		return "", err
	}
	n, ok := count(marker(console, "MQG-BASESYSTEM-BYTES"))
	if !ok {
		return "", errors.New("the microVM did not report a BaseSystem.dmg size")
	}
	if err := os.Truncate(bsDmg, n); err != nil {
		return "", err
	}
	// The host's own read, against the digest the guest sent: a short or
	// torn write through the raw disk would otherwise surface as a
	// dmg2img failure that says nothing about where the bytes went.
	want := marker(console, "MQG-BASESYSTEM-SHA256")
	got, err := fetch.SHA256File(bsDmg)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", fmt.Errorf("BaseSystem.dmg did not survive the trip out of the microVM: the guest read %s and this host reads %s", want, got)
	}
	b.logf("BaseSystem.dmg: %d bytes, sha256 %s", n, got)

	b.logf("converting BaseSystem.dmg to raw")
	if err := b.dmg2img(ctx, bsDmg, bsImg); err != nil {
		return "", fmt.Errorf("dmg2img failed on BaseSystem.dmg: %w", err)
	}
	if n, err := fileSize(bsImg); err == nil {
		b.logf("BaseSystem raw image: %d bytes", n)
	}

	// Pass 2. The injectables go in as a tar on a raw disk -- the channel
	// BaseSystem.dmg came out on, run the other way -- and are untarred
	// BEFORE the ownership pass: anything injected after the chown would
	// be the one uid-1000 file on root-owned media, which launchd skips as
	// "Dubious ownership".
	disks := []privops.Disk{{Role: "ro", Path: bsImg}, {Role: "ro", Path: esdImg}}
	if o.Enabled() {
		if err := b.stageInjectables(o.Injectables, tarPath); err != nil {
			return "", err
		}
		disks = append(disks, privops.Disk{Role: "raw", Path: tarPath})
	}
	b.logf("assembling the media inside the microVM (pass 2 of 4)")
	if console, err = b.pass(ctx, 2, "assemble", building, work, disks...); err != nil {
		return "", err
	}
	// Checked against a constant, not against the source: a bad byte out
	// of dmg2img would be copied faithfully and verified as correct.
	if err := b.checkSums(console, "MQG-SUM-ESD", "the ESD's Packages, as converted and read",
		"the ESD does not contain what Apple shipped. The suspects are dmg2img and the Linux hfsplus read of its output, in that order -- not the media, whose copy of them has not been checked yet, and not assets/pins/apple-packages.sha256, whose values were read from two images that share no code"); err != nil {
		return "", err
	}

	if err := syncFile(building); err != nil {
		return "", err
	}
	b.logf("restoring root ownership (microVM pass 3 of 4)")
	if _, err := b.pass(ctx, 3, "fix-ownership", building, work); err != nil {
		return "", err
	}

	// Pass 4, in a microVM of its own, booted after the writing one
	// exited: a fresh kernel with no page cache, pulling every byte off
	// this host's file. A check through the cache that did the writing
	// once passed media that was corrupt.
	b.logf("reading the finished media back in a microVM of its own (pass 4 of 4)")
	if console, err = b.pass(ctx, 4, "verify-packages", building, work); err != nil {
		return "", err
	}
	if err := b.checkSums(console, "MQG-SUM-MEDIA", "the finished media, read by a fresh guest",
		"the media does not contain what Apple shipped. This is the fault that a finished copy, and a read-back through the same cache, both fail to report. Build it again"); err != nil {
		return "", err
	}

	// After the ownership pass, not before: the microVM mounts the image,
	// and mounting HFS+ rewrites its header.
	b.logf("checksumming %s", building)
	return fetch.SHA256File(building)
}

// buildFromVolume is BuildFromVolume's build proper, into building.
func (b *Builder) buildFromVolume(ctx context.Context, volume string, o Options, work, building string) (string, error) {
	used, err := hfsUsedBytes(volume)
	if err != nil {
		return "", err
	}
	part := int((used+1<<20-1)>>20) + MarginMiB + o.ExtraSpaceMiB
	b.logf("the disc's volume uses %d bytes; creating %s: GPT, one AF00 partition, %d MiB, %q", used, building, part, DiscVolumeName)
	if err := CreateHFSGPT(ctx, b.Runner, building, part, DiscVolumeName); err != nil {
		return "", err
	}

	disks := []privops.Disk{{Role: "ro", Path: volume}}
	if o.Enabled() {
		tarPath := filepath.Join(work, workTar)
		if err := b.stageInjectables(o.Injectables, tarPath); err != nil {
			return "", err
		}
		disks = append(disks, privops.Disk{Role: "raw", Path: tarPath})
	}
	b.logf("copying the disc onto the media inside the microVM (pass 1 of 3)")
	if _, err := b.pass(ctx, 1, "disc/assemble-disc", building, work, disks...); err != nil {
		return "", err
	}
	if err := syncFile(building); err != nil {
		return "", err
	}
	b.logf("restoring root ownership (microVM pass 2 of 3)")
	if _, err := b.pass(ctx, 2, "fix-ownership", building, work); err != nil {
		return "", err
	}
	b.logf("reading the finished media back in a microVM of its own (pass 3 of 3)")
	console, err := b.pass(ctx, 3, "verify-packages", building, work)
	if err != nil {
		return "", err
	}
	if err := checkDiscSums(console); err != nil {
		return "", err
	}
	b.logf("checksumming %s", building)
	return fetch.SHA256File(building)
}

// DiscVolumeName is the 10.6 media's volume name, the retail disc's own.
const DiscVolumeName = "Mac OS X Install DVD"

// hfsUsedBytes is how much of a bare HFS+ volume is in use, read from its
// volume header: block size at 40, total blocks at 44, free at 48.
func hfsUsedBytes(volume string) (int64, error) {
	f, err := os.Open(volume)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := make([]byte, 52)
	if _, err := f.ReadAt(h, 1024); err != nil {
		return 0, fmt.Errorf("reading %s's volume header: %w", volume, err)
	}
	if string(h[:2]) != "H+" {
		return 0, fmt.Errorf("%s is not an HFS+ volume", volume)
	}
	bs := int64(binary.BigEndian.Uint32(h[40:44]))
	total := int64(binary.BigEndian.Uint32(h[44:48]))
	free := int64(binary.BigEndian.Uint32(h[48:52]))
	return (total - free) * bs, nil
}

// checkDiscSums holds the finished media's packages to the known disc
// they came from: every pinned package present and as shipped. The media
// carries more than the disc did -- the first-boot payload, the updates
// -- and those are not the disc's to vouch for.
func checkDiscSums(console []byte) error {
	got := map[string]string{}
	for _, m := range privops.Markers(console, "MQG-SUM-MEDIA") {
		if sum, name, ok := strings.Cut(m, "  "); ok {
			got[name] = sum
		}
	}
	known, err := disc.KnownDiscs()
	if err != nil {
		return err
	}
	var last []string
	for _, k := range known {
		var problems []string
		for name, want := range k.Sums {
			switch g, ok := got[name]; {
			case !ok:
				problems = append(problems, name+": missing from the media")
			case g != want:
				problems = append(problems, fmt.Sprintf("%s: sha256 %s is not the pinned %s", name, g, want))
			}
		}
		if len(problems) == 0 {
			return nil
		}
		sort.Strings(problems)
		last = problems
	}
	return fmt.Errorf("the media does not hold the disc as it shipped -- build it again: %s", strings.Join(last, "; "))
}

// pass runs one embedded payload in the microVM, keeping its console in
// the work area for whoever has to find out what went wrong.
func (b *Builder) pass(ctx context.Context, n int, name, target, work string, disks ...privops.Disk) ([]byte, error) {
	payload, err := fs.ReadFile(macosx.Files, "assets/privops/"+name+".sh")
	if err != nil {
		return nil, err
	}
	console, err := b.VM.Run(ctx, target, payload, disks)
	if console != nil {
		if werr := os.WriteFile(filepath.Join(work, workConsole), console, 0o644); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		return console, fmt.Errorf("pass %d (%s): %w", n, name, err)
	}
	return console, nil
}

// checkSums holds a pass's checksum markers against Apple's pinned
// values, logging each package that is missing or wrong.
func (b *Builder) checkSums(console []byte, marker, what, fault string) error {
	b.logf("checking %s against Apple's pinned checksums", what)
	sums := strings.Join(privops.Markers(console, marker), "\n")
	problems, err := CheckAppleSums([]byte(sums))
	if err == nil {
		return nil
	}
	for _, p := range problems {
		b.logf("    %s", p)
	}
	return fmt.Errorf("%s (%w)", fault, err)
}

func (b *Builder) dmg2img(ctx context.Context, in, out string) error {
	var stderr bytes.Buffer
	if err := b.Runner.Run(ctx, proc.Cmd{Name: "dmg2img", Args: []string{"-s", "-i", in, "-o", out}, Stderr: &stderr}); err != nil {
		return fmt.Errorf("%w%s", err, detail(stderr.String()))
	}
	return nil
}

// stageInjectables writes the injectables' tar where pass 2 gets it.
func (b *Builder) stageInjectables(in Injectables, path string) error {
	b.logf("injecting the unattended-install hooks")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	err = in.WriteTar(f, b.Log)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cannot build %s: %w", path, err)
	}
	if n, err := fileSize(path); err == nil {
		b.logf("staged %d bytes of files to inject", n)
	}
	return nil
}

// marker is a marker's value by privops.Marker's rule -- printed on
// exactly one line -- and "" otherwise, which no caller accepts: not a
// count, and not a checksum. The build and the digest read markers alike.
func marker(console []byte, name string) string {
	v, _ := privops.Marker(console, name)
	return v
}

// count is a non-negative decimal: not empty, and all digits -- no sign,
// no space.
func count(s string) (int64, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// xarMagic refuses a file that does not begin "xar!": a flat package is
// a xar archive, and anything else fails the install much later.
func xarMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "xar!" {
		return fmt.Errorf("%s is not a flat package (no xar magic)", path)
	}
	return nil
}

func fileSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// truncateNew creates path as a sparse file of size bytes.
func truncateNew(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncFile is the sync between the passes, for the one file that
// matters: what pass 2 wrote is on the disk before pass 3 boots.
func syncFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// inDir is each of names, joined to dir.
func inDir(dir string, names ...string) []string {
	var p []string
	for _, n := range names {
		p = append(p, filepath.Join(dir, n))
	}
	return p
}
