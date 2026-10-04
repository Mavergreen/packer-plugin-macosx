// Package store is content-addressed outputs a data source can reuse: a
// directory under Root, named by the digest of the listing that made it,
// built once and read many times. Two data sources (or two Packer builds)
// asking for the same kind with the same listing at once are serialized
// by internal/lock, so the second either waits behind a live builder's
// refusal or finds the first one's work already there.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx/internal/lock"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// completeMarker is the file whose presence alone says a store directory
// is finished: written last, after the listing, in the temp directory
// that is then renamed into place.
const completeMarker = "complete"

// Store is where a kind's built directories live, one per digest, under
// Root. PID is the builder's pid for internal/lock (0 is this process's
// own, the ordinary case). Log, when set, hears what Get did; nil is
// silent.
type Store struct {
	Root string
	PID  int
	Log  func(string, ...any)
}

// Get returns root/<kind>/<digest>/, where digest is pins.Digest(listing):
// straight away when its "complete" marker is already there, or after
// taking the lock on that key, running make into a fresh temporary
// directory beside it, writing the listing to "<dir>/inputs" and the
// "complete" marker, and renaming the directory into place. reused is
// true only for the first case.
//
// make never writes into that path: it writes into a
// ".store-tmp-<digest>-*" directory beside it, which only a complete
// build is renamed from. So what a make killed mid-way leaves (go-plugin's
// forced kill, say, which runs no deferred cleanup) is one of those temp
// directories -- for media, a whole installer image. Under the key's lock,
// before make runs, every such sibling for this digest is removed: the
// lock proves no live make owns one. A directory at the digest's own path
// without "complete" is not something Get leaves either; one damaged by
// hand (its marker deleted) is removed and rebuilt too. A failed make
// leaves nothing behind: no directory at the digest's path, no temp
// directory, and the lock released.
//
// kind must be a single, plain path element (letters, digits, dash,
// underscore): it names a directory under Root, and nothing a caller
// passes may reach outside it.
//
// A concurrent Get for the same kind and listing is refused by the lock
// while the first is running (the error names the holder); it is not
// waited out.
func (s Store) Get(ctx context.Context, kind string, listing []string, make func(ctx context.Context, dir string) error) (dir string, reused bool, err error) {
	if !safeKind.MatchString(kind) {
		return "", false, fmt.Errorf("store kind %q is not a single plain path element (letters, digits, dash, underscore)", kind)
	}
	digest := pins.Digest(listing)
	kindDir := filepath.Join(s.Root, kind)
	target := filepath.Join(kindDir, digest)

	if complete(target) {
		s.logf("%s: %s already built (%s)", kind, digest, target)
		return target, true, nil
	}

	if err := os.MkdirAll(kindDir, 0o755); err != nil {
		return "", false, err
	}

	l, err := lock.Acquire(target+".lock", s.PID)
	if err != nil {
		return "", false, err
	}
	defer func() {
		if rerr := l.Release(); err == nil {
			err = rerr
		}
	}()
	switch {
	case l.TookOver > 0:
		s.logf("%s: %s: taking over a stale lock left by pid %d", kind, digest, l.TookOver)
	case l.TookOver < 0:
		s.logf("%s: %s: taking over a stale lock left by an unknown pid", kind, digest)
	}

	// Another Get may have finished this key between our first look and
	// taking the lock.
	if complete(target) {
		s.logf("%s: %s already built (%s)", kind, digest, target)
		return target, true, nil
	}

	// A killed make's temp directories for this key: named by this key's
	// own digest, directly under kindDir, and -- with the lock held --
	// owned by no live make, so they are this store's to remove.
	if err := s.sweep(kindDir, digest); err != nil {
		return "", false, err
	}
	// A directory here without "complete" can only be a hand-damaged
	// entry (Get renames nothing into place before its marker is
	// written); it is rebuilt, not trusted.
	if err := os.RemoveAll(target); err != nil {
		return "", false, fmt.Errorf("cannot remove the incomplete %s: %w", target, err)
	}

	tmp, err := os.MkdirTemp(kindDir, tmpPrefix(digest))
	if err != nil {
		return "", false, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()

	if err := make(ctx, tmp); err != nil {
		return "", false, err
	}
	if err := writeListing(tmp, listing); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(filepath.Join(tmp, completeMarker), nil, 0o644); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", false, err
	}
	ok = true
	s.logf("%s: %s: built %s", kind, digest, target)
	return target, false, nil
}

// safeKind is what a kind may be: one plain path element.
var safeKind = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// tmpPrefix is the name every temp directory a make for digest builds in
// starts with.
func tmpPrefix(digest string) string { return ".store-tmp-" + digest + "-" }

// sweep removes every temp directory a make for digest left in kindDir.
// The caller holds digest's lock.
func (s Store) sweep(kindDir, digest string) error {
	ents, err := os.ReadDir(kindDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), tmpPrefix(digest)) {
			continue
		}
		p := filepath.Join(kindDir, e.Name())
		s.logf("removing %s, left by a make that never finished", p)
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("cannot remove the unfinished %s: %w", p, err)
		}
	}
	return nil
}

// complete reports whether dir holds the "complete" marker: a finished
// build, renamed into place only after the marker was written.
func complete(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, completeMarker))
	return err == nil
}

// writeListing writes rows to dir/inputs, one row per line,
// newline-terminated -- the listing this build was made from, so a
// reader of the store can see what it consumed without recomputing it.
func writeListing(dir string, rows []string) error {
	var body string
	for _, r := range rows {
		body += r + "\n"
	}
	return os.WriteFile(filepath.Join(dir, "inputs"), []byte(body), 0o644)
}

func (s Store) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}
