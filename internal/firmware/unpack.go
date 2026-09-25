package firmware

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// untarGz extracts a gzip'd tar into dest, which must not exist, dropping
// the first strip components of every name -- `tar -xzf archive
// --strip-components=strip`. It is built in a temp directory beside dest and
// renamed into place, so dest is either absent or complete.
//
// An archive's names are its author's choice, so: a name that is absolute
// or climbs out with ".." is refused; nothing is ever written through a
// symlink (every directory on the way to an entry is checked with Lstat,
// and files are created O_EXCL); a hard link must name a regular file
// this archive already put inside dest; devices and FIFOs are refused.
// Symlinks are created as they are, as tar creates them -- audk's tarball
// carries one absolute symlink, which the build never follows. File modes
// keep their permission bits (execute matters: build_oc.tool) and file
// times are the archive's, as tar sets them.
func untarGz(ctx context.Context, archive, dest string, strip int) (err error) {
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("cannot unpack %s: %s already exists", archive, dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dest)+".unpack-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()

	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", archive, err)
	}
	tr := tar.NewReader(zr)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue // GitHub's pax_global_header: the commit id, not a file
		}
		rel, ok, err := stripName(h.Name, strip)
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		if !ok {
			continue
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := mkdirsNoFollow(tmp, filepath.Dir(target)); err != nil {
			return fmt.Errorf("%s: %s: %w", archive, h.Name, err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = mkdirsNoFollow(tmp, target)
		case tar.TypeReg:
			err = writeNew(target, tr, os.FileMode(h.Mode).Perm(), h.ModTime)
		case tar.TypeSymlink:
			err = os.Symlink(h.Linkname, target)
		case tar.TypeLink:
			err = hardLink(tmp, h.Linkname, strip, target)
		default:
			err = fmt.Errorf("unsupported entry type %q", h.Typeflag)
		}
		if err != nil {
			return fmt.Errorf("%s: %s: %w", archive, h.Name, err)
		}
	}
	// MkdirTemp made tmp 0700; the unpacked root is 0755, as tar leaves it.
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// stripName cleans an archive member's name and drops its first strip
// components. ok is false when nothing is left: the entry is one of the
// directories being stripped.
func stripName(name string, strip int) (string, bool, error) {
	if strings.HasPrefix(name, "/") {
		return "", false, fmt.Errorf("%s: an absolute name", name)
	}
	var parts []string
	for _, p := range strings.Split(name, "/") {
		switch p {
		case "", ".":
		case "..":
			return "", false, fmt.Errorf("%s: contains a climbing .. component", name)
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) <= strip {
		return "", false, nil
	}
	return path.Join(parts[strip:]...), true, nil
}

// mkdirsNoFollow makes dir, and any missing parent between root and it,
// refusing to pass through anything that is not a real directory -- in
// particular a symlink an earlier archive entry created.
func mkdirsNoFollow(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", dir, root)
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory (a symlink?); refusing to write through it", cur)
		}
	}
	return nil
}

// writeNew creates path, which must not exist, from r.
func writeNew(path string, r io.Reader, perm os.FileMode, mtime time.Time) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if mtime.IsZero() {
		return nil
	}
	return os.Chtimes(path, mtime, mtime)
}

// hardLink makes target a hard link to the archive member linkname, which
// must be a regular file already extracted under root, reached without
// passing through a symlink.
func hardLink(root, linkname string, strip int, target string) error {
	rel, ok, err := stripName(linkname, strip)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("hard link to %s, which is stripped away", linkname)
	}
	src := filepath.Join(root, filepath.FromSlash(rel))
	if err := mkdirsNoFollow(root, filepath.Dir(src)); err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("hard link to %s: %w", linkname, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("hard link to %s, which is not a regular file", linkname)
	}
	return os.Link(src, target)
}

// extractKext copies the bundle name.kext out of a release zip into dest,
// which must not exist. The releases disagree on where the bundle sits --
// Lilu's is at the top, VirtualSMC's under Kexts/ -- so it is searched for
// the way `find -name X.kext -type d -prune | sort | head -n 1` finds
// it: every directory named name.kext that is not inside
// another one, and the lexically first of those. A zip may list only
// files, so a directory is recognised by having something below it.
func extractKext(ctx context.Context, archive, name, dest string) (err error) {
	want := name + ".kext"
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("cannot unpack %s: %w", archive, err)
	}
	defer zr.Close()

	roots := map[string]bool{}
	for _, f := range zr.File {
		parts := strings.Split(strings.TrimSuffix(f.Name, "/"), "/")
		for i, p := range parts {
			if p != want {
				continue
			}
			if i < len(parts)-1 || strings.HasSuffix(f.Name, "/") {
				roots[strings.Join(parts[:i+1], "/")] = true
			}
			break // -prune: nothing below the first match is a candidate
		}
	}
	if len(roots) == 0 {
		return fmt.Errorf("%s contains no %s", archive, want)
	}
	var cands []string
	for r := range roots {
		cands = append(cands, r)
	}
	sort.Strings(cands)
	root := cands[0]

	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("cannot unpack %s: %s already exists", archive, dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".unpack-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := strings.TrimSuffix(f.Name, "/")
		if n != root && !strings.HasPrefix(n, root+"/") {
			continue
		}
		rel, ok, err := stripName(strings.TrimPrefix(strings.TrimPrefix(n, root), "/"), 0)
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		if !ok {
			continue // the bundle directory itself
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := mkdirsNoFollow(tmp, filepath.Dir(target)); err != nil {
			return fmt.Errorf("%s: %s: %w", archive, f.Name, err)
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = mkdirsNoFollow(tmp, target)
		case mode.IsRegular():
			err = extractZipFile(f, target)
		default:
			err = fmt.Errorf("refusing a %v entry", mode.Type())
		}
		if err != nil {
			return fmt.Errorf("%s: %s: %w", archive, f.Name, err)
		}
	}
	// MkdirTemp made tmp 0700; the bundle is 0755, as unzip leaves it.
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func extractZipFile(f *zip.File, target string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	perm := os.FileMode(0o644)
	if f.Mode().Perm()&0o111 != 0 {
		perm = 0o755
	}
	return writeNew(target, r, perm, f.Modified)
}
