package firmware

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// entry is one member of a test archive. A name ending in "/" is a
// directory; link names a symlink's (or, with hard, a hard link's) target.
type entry struct {
	name, body, link string
	mode             int64
	hard             bool
	typ              byte // overrides the type when non-zero (a device, say)
}

// makeTarGz writes a GitHub-archive-shaped tarball: a pax global header
// first, then the entries.
func makeTarGz(t *testing.T, dir string, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": "0672a009e9ca85753d240324d761341adf0291b3"}}))
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, ModTime: time.Unix(1700000000, 0)}
		if e.mode != 0 {
			h.Mode = e.mode
		}
		switch {
		case e.typ != 0:
			h.Typeflag = e.typ
		case strings.HasSuffix(e.name, "/"):
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case e.link != "" && e.hard:
			h.Typeflag, h.Linkname = tar.TypeLink, e.link
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		must(tw.WriteHeader(h))
		if h.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			must(err)
		}
	}
	must(tw.Close())
	must(zw.Close())
	p := filepath.Join(dir, "fixture.tar.gz")
	must(os.WriteFile(p, buf.Bytes(), 0o644))
	return p
}

// makeZip writes a zip with the given entries; a name ending in "/" is a
// directory entry; mode 0 means 0644 (0755 for a directory).
func makeZip(t *testing.T, dir, name string, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(e.name, "/") {
			mode = os.ModeDir | 0o755
		}
		if e.mode != 0 {
			mode = os.FileMode(e.mode)
		}
		if e.link != "" {
			mode = os.ModeSymlink | 0o777
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		body := e.body
		if e.link != "" {
			body = e.link
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUntarStripsTheTopDirectory(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir,
		entry{name: "top/"},
		entry{name: "top/a.txt", body: "a"},
		entry{name: "top/sub/"},
		entry{name: "top/sub/run.sh", body: "#!/bin/sh\n", mode: 0o755},
	)
	dest := filepath.Join(dir, "out")
	if err := untarGz(context.Background(), a, dest, 1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "a" {
		t.Fatalf("a.txt = %q", b)
	}
	fi, err := os.Stat(filepath.Join(dest, "sub", "run.sh"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh lost its execute bit: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "top")); err == nil {
		t.Fatal("the top directory was not stripped")
	}
}

func TestUntarRefusesClimbingOut(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/../../evil", body: "x"})
	dest := filepath.Join(dir, "out")
	err := untarGz(context.Background(), a, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("dest must not exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "evil")); err == nil {
		t.Fatal("evil must not exist in dir's parent")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".out.unpack-*"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp dirs: %v", matches)
	}
}

func TestUntarRefusesAbsoluteNames(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "/etc/evil", body: "x"})
	dest := filepath.Join(dir, "out")
	if err := untarGz(context.Background(), a, dest, 0); err == nil {
		t.Fatal("must fail on an absolute name")
	}
}

func TestUntarNeverWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	a := makeTarGz(t, dir,
		entry{name: "top/link", link: outside},
		entry{name: "top/link/x", body: "x"},
	)
	dest := filepath.Join(dir, "out")
	err := untarGz(context.Background(), a, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside must stay empty: %v", entries)
	}
}

func TestUntarKeepsAnAbsoluteSymlinkAsASymlink(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/X11IncludeHack", link: "/opt/X11/include"})
	dest := filepath.Join(dir, "out")
	if err := untarGz(context.Background(), a, dest, 1); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(dest, "X11IncludeHack"))
	if err != nil || got != "/opt/X11/include" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestUntarMakesHardLinksToWhatItExtracted(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir,
		entry{name: "top/f", body: "hello"},
		entry{name: "top/g", link: "top/f", hard: true},
	)
	dest := filepath.Join(dir, "out")
	if err := untarGz(context.Background(), a, dest, 1); err != nil {
		t.Fatal(err)
	}
	fi1, err1 := os.Stat(filepath.Join(dest, "f"))
	fi2, err2 := os.Stat(filepath.Join(dest, "g"))
	if err1 != nil || err2 != nil || !os.SameFile(fi1, fi2) {
		t.Fatalf("not the same file: %v %v", err1, err2)
	}

	dir2 := t.TempDir()
	a2 := makeTarGz(t, dir2,
		entry{name: "top/g", link: "top/missing", hard: true},
	)
	dest2 := filepath.Join(dir2, "out")
	if err := untarGz(context.Background(), a2, dest2, 1); err == nil {
		t.Fatal("a hard link to a missing file must fail")
	}
}

func TestUntarRefusesDevices(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/dev", typ: tar.TypeChar})
	dest := filepath.Join(dir, "out")
	err := untarGz(context.Background(), a, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "top/dev") {
		t.Fatalf("err = %v", err)
	}
}

func TestUntarRefusesAnExistingDest(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/a.txt", body: "a"})
	dest := filepath.Join(dir, "out")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := untarGz(context.Background(), a, dest, 1); err == nil {
		t.Fatal("must fail when dest already exists")
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dest must be untouched: %v %v", entries, err)
	}
}

func TestUntarStopsWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/a.txt", body: "a"})
	dest := filepath.Join(dir, "out")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := untarGz(ctx, a, dest, 1); err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("dest must not exist")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".out.unpack-*"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp dirs: %v", matches)
	}
}

func TestUntarWithoutStripKeepsTheTopDirectory(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/"}, entry{name: "top/a.txt", body: "a"})
	dest := filepath.Join(dir, "out")
	if err := untarGz(context.Background(), a, dest, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "top", "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractKextFindsTheBundleWhereverItIs(t *testing.T) {
	dir := t.TempDir()
	a := makeZip(t, dir, "VirtualSMC.zip",
		entry{name: "Kexts/VirtualSMC.kext/Contents/Info.plist", body: "<plist/>"},
		entry{name: "Kexts/VirtualSMC.kext/Contents/MacOS/VirtualSMC", body: "bin", mode: 0o755},
		entry{name: "dSYM/VirtualSMC.kext.dSYM/Contents/Info.plist", body: "<plist/>"},
		entry{name: "Tools/smcread", body: "bin"},
	)
	dest := filepath.Join(dir, "out")
	if err := extractKext(context.Background(), a, "VirtualSMC", dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "Contents", "Info.plist")); err != nil || string(b) != "<plist/>" {
		t.Fatalf("Info.plist: %q %v", b, err)
	}
	fi, err := os.Stat(filepath.Join(dest, "Contents", "MacOS", "VirtualSMC"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("MacOS/VirtualSMC: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "dSYM")); err == nil {
		t.Fatal("dSYM must not be extracted")
	}

	var got []string
	if err := filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dest {
			return nil
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		got = append(got, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Contents", "Contents/Info.plist", "Contents/MacOS", "Contents/MacOS/VirtualSMC"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dest contents = %v, want %v (nothing from dSYM or Tools)", got, want)
	}
}

func TestExtractKextTakesTheFirstBundleAndItsNestedContents(t *testing.T) {
	dir := t.TempDir()
	a := makeZip(t, dir, "Lilu.zip",
		entry{name: "b/Lilu.kext/Contents/Info.plist", body: "b"},
		entry{name: "a/Lilu.kext/Contents/Info.plist", body: "a"},
		entry{name: "a/Lilu.kext/Contents/PlugIns/Lilu.kext/Contents/Info.plist", body: "nested"},
	)
	dest := filepath.Join(dir, "out")
	if err := extractKext(context.Background(), a, "Lilu", dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "Contents", "Info.plist")); err != nil || string(b) != "a" {
		t.Fatalf("chose wrong bundle: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "Contents", "PlugIns", "Lilu.kext", "Contents", "Info.plist")); err != nil || string(b) != "nested" {
		t.Fatalf("nested content missing: %q %v", b, err)
	}
}

func TestExtractKextFindsATopLevelBundleWithoutDirectoryEntries(t *testing.T) {
	dir := t.TempDir()
	a := makeZip(t, dir, "Lilu.zip",
		entry{name: "Lilu.kext/Contents/Info.plist", body: "info"},
		entry{name: "Lilu.kext/Contents/MacOS/Lilu", body: "bin", mode: 0o755},
	)
	dest := filepath.Join(dir, "out")
	if err := extractKext(context.Background(), a, "Lilu", dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "Contents", "Info.plist")); err != nil || string(b) != "info" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestExtractKextSaysSoWhenThereIsNoBundle(t *testing.T) {
	dir := t.TempDir()
	a := makeZip(t, dir, "Lilu.zip", entry{name: "nothing/here.txt", body: "x"})
	dest := filepath.Join(dir, "out")
	err := extractKext(context.Background(), a, "Lilu", dest)
	if err == nil || err.Error() != a+" contains no Lilu.kext" {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractKextRefusesSymlinksAndClimbingNames(t *testing.T) {
	dir := t.TempDir()
	a := makeZip(t, dir, "Lilu.zip",
		entry{name: "Lilu.kext/Contents/Info.plist", body: "info"},
		entry{name: "Lilu.kext/Contents/x", link: "/etc/passwd"},
	)
	dest := filepath.Join(dir, "out")
	err := extractKext(context.Background(), a, "Lilu", dest)
	if err == nil || !strings.Contains(err.Error(), "Lilu.kext/Contents/x") {
		t.Fatalf("err = %v", err)
	}

	dir2 := t.TempDir()
	a2 := makeZip(t, dir2, "Lilu.zip",
		entry{name: "Lilu.kext/Contents/Info.plist", body: "info"},
		entry{name: "Lilu.kext/../../evil", body: "x"},
	)
	dest2 := filepath.Join(dir2, "out")
	if err := extractKext(context.Background(), a2, "Lilu", dest2); err == nil {
		t.Fatal("a climbing name must fail")
	}
}

// TestExtractKextPropagatesALstatErrorThatIsNotNotExist is untarGz's own
// rule (an Lstat(dest) failure that is not "does not exist" is returned as
// itself, not swallowed) mirrored onto extractKext: a dest whose parent is
// unreadable makes Lstat(dest) fail with EACCES, which is not
// fs.ErrNotExist. Before this fix, extractKext ignored that error and kept
// going, so the eventual failure came from a much later step (MkdirTemp)
// instead of naming the real cause.
func TestExtractKextPropagatesALstatErrorThatIsNotNotExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not work this way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply as root")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0o755)

	a := makeZip(t, dir, "Lilu.zip", entry{name: "Lilu.kext/Contents/Info.plist", body: "x"})
	dest := filepath.Join(blocked, "out")
	err := extractKext(context.Background(), a, "Lilu", dest)
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "lstat" {
		t.Fatalf("must propagate the Lstat error itself: %v", err)
	}
}

// The unpacked root is 0755, as tar and unzip leave it, not the 0700 of
// the temp directory it was built in.
func TestUnpackedRootsAreNotPrivate(t *testing.T) {
	dir := t.TempDir()
	a := makeTarGz(t, dir, entry{name: "top/"}, entry{name: "top/a", body: "a"})
	dest := filepath.Join(dir, "out")
	if err := untarGz(context.Background(), a, dest, 1); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("untarGz's root: %v, %v", fi.Mode(), err)
	}
	z := makeZip(t, dir, "k.zip", entry{name: "K.kext/Contents/Info.plist", body: "p"})
	bundle := filepath.Join(dir, "K.kext")
	if err := extractKext(context.Background(), z, "K", bundle); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(bundle); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("extractKext's root: %v, %v", fi.Mode(), err)
	}
}
