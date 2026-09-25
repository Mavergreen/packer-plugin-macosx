package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// kextsDir is where the kext bundles are unpacked: build/kexts.
func (b *Builder) kextsDir() string { return filepath.Join(b.Paths.Build(), "kexts") }

// kextMarker is where Kexts records which pinned release a bundle it
// unpacked came from: build/kexts/.<name>.kext.sha256 (see Kexts).
func (b *Builder) kextMarker(name string) string {
	return filepath.Join(b.kextsDir(), "."+name+".kext.sha256")
}

// Kexts unpacks each pinned kext release's bundle into build/kexts: the
// layout the EFI image copies from. These are the only things in the
// image not built here (acidanthera ships them as binaries); they are
// pinned by checksum, and each bundle must hold the two files
// config.plist names for it, checked by name so a failure says which.
//
// A bundle is unpacked once, and a marker beside it records the release
// it came from and the tree it unpacked to ("<zip sha256> <tree
// sha256>"), so that a pin bump replaces it rather than shipping the old
// one. A bundle is this code's only while the marker exists AND the
// bundle still digests to the marker's tree: a marker can outlive the
// bundle it was written for. Such a bundle is
//   - at the pin: kept;
//   - at another pin: replaced by the pinned release, which is unpacked
//     beside it and checked first, so a broken release replaces nothing.
//
// Any other bundle -- no marker (something else unpacked it), an older
// one-field marker, or a tree that is not the marked one -- is compared,
// path by path and byte by byte, with the pinned release unpacked beside
// it. The same: the marker is written and the bundle kept. Different: an
// error, and the bundle is left alone -- nothing proves it is ours to
// delete.
func (b *Builder) Kexts(ctx context.Context, in Inputs) ([]string, error) {
	var out []string
	for _, k := range Kexts {
		bundle := filepath.Join(b.kextsDir(), k.Name+".kext")
		if err := b.kext(ctx, in, k, bundle); err != nil {
			return nil, err
		}
		if err := checkKext(bundle, k.Name); err != nil {
			return nil, err
		}
		b.logf("%s.kext ready at %s", k.Name, bundle)
		out = append(out, bundle)
	}
	return out, nil
}

// writeKextMarker records that bundle is the release whose zip has
// sha256 zip, unpacked.
func (b *Builder) writeKextMarker(name, zip, bundle string) error {
	tree, err := treeDigest(bundle)
	if err != nil {
		return err
	}
	return writeFileAtomic(b.kextMarker(name), []byte(zip+" "+tree+"\n"), 0o644)
}

// kextOwner is the release (zip sha256) the marker says bundle is, when
// bundle is the tree the marker was written for; "" when there is no
// two-field marker or bundle is some other tree.
func (b *Builder) kextOwner(name, bundle string) (string, error) {
	data, err := os.ReadFile(b.kextMarker(name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return "", nil
	}
	tree, err := treeDigest(bundle)
	if err != nil {
		return "", err
	}
	if tree != fields[1] {
		return "", nil
	}
	return fields[0], nil
}

// kext brings bundle up to the pinned release of k.
func (b *Builder) kext(ctx context.Context, in Inputs, k Kext, bundle string) error {
	pin, err := b.Registry.Lookup(k.Source)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(bundle); errors.Is(err, fs.ErrNotExist) {
		archive, err := b.input(in, k.Source)
		if err != nil {
			return err
		}
		if err := extractKext(ctx, archive, k.Name, bundle); err != nil {
			return err
		}
		return b.writeKextMarker(k.Name, pin.SHA256, bundle)
	} else if err != nil {
		return err
	}
	owner, err := b.kextOwner(k.Name, bundle)
	if err != nil {
		return err
	}
	if owner == pin.SHA256 {
		b.logf("%s.kext already unpacked at %s", k.Name, bundle)
		return nil
	}

	archive, err := b.input(in, k.Source)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(b.kextsDir(), "."+k.Name+".kext.unpack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	fresh := filepath.Join(tmp, k.Name+".kext")
	if err := extractKext(ctx, archive, k.Name, fresh); err != nil {
		return err
	}

	if owner != "" {
		// Ours, from another pin: the new release must be a usable
		// bundle before the old one goes.
		if piece := missingKextPiece(fresh, k.Name); piece != "" {
			return fmt.Errorf("the pinned %s (%s) is not a kext bundle: no %s; %s is unchanged", k.Source, pin.SHA256, piece, bundle)
		}
		b.logf("%s.kext at %s is from another pin (%s); replacing it with %s", k.Name, bundle, owner, pin.SHA256)
		if err := os.RemoveAll(bundle); err != nil {
			return err
		}
		if err := os.Rename(fresh, bundle); err != nil {
			return err
		}
		return b.writeKextMarker(k.Name, pin.SHA256, bundle)
	}

	have, err := treeDigest(bundle)
	if err != nil {
		return err
	}
	want, err := treeDigest(fresh)
	if err != nil {
		return err
	}
	if have != want {
		return fmt.Errorf("%s does not match the pinned %s (%s); remove it and re-run", bundle, k.Source, pin.SHA256)
	}
	b.logf("%s.kext at %s matches the pinned %s; keeping it", k.Name, bundle, k.Source)
	return b.writeKextMarker(k.Name, pin.SHA256, bundle)
}

// checkKext checks bundle holds the two paths config.plist names for a
// kext, PlistPath and ExecutablePath, as regular files.
func checkKext(bundle, name string) error {
	if piece := missingKextPiece(bundle, name); piece != "" {
		return fmt.Errorf("%s is not a kext bundle: no %s", bundle, piece)
	}
	return nil
}

// missingKextPiece is the first of the two paths checkKext wants that
// bundle lacks, slash-separated, or "".
func missingKextPiece(bundle, name string) string {
	for _, want := range []string{"Contents/Info.plist", "Contents/MacOS/" + name} {
		if fi, err := os.Stat(filepath.Join(bundle, filepath.FromSlash(want))); err != nil || !fi.Mode().IsRegular() {
			return want
		}
	}
	return ""
}

// treeDigest is a sha256 over a directory tree: every path under dir,
// in sorted order, with its kind and, for a regular file, its bytes (for
// a symlink, its target). Two trees digest alike exactly when they hold
// the same paths, each of the same kind with the same contents.
func treeDigest(dir string) (string, error) {
	type ent struct {
		rel  string
		kind fs.FileMode
		p    string
	}
	var es []ent
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		es = append(es, ent{filepath.ToSlash(rel), d.Type(), p})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(es, func(i, j int) bool { return es[i].rel < es[j].rel })
	h := sha256.New()
	for _, e := range es {
		var data []byte
		switch {
		case e.kind.IsRegular():
			if data, err = os.ReadFile(e.p); err != nil {
				return "", err
			}
		case e.kind&fs.ModeSymlink != 0:
			target, err := os.Readlink(e.p)
			if err != nil {
				return "", err
			}
			data = []byte(target)
		}
		// Lengths first, so no path or content can run into the next.
		fmt.Fprintf(h, "%d:%s %d %d:", len(e.rel), e.rel, uint32(e.kind), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
