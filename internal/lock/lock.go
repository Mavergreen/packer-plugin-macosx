// Package lock is this project's build lock: a directory holding its holder's
// pid, created by renaming a temporary directory -- pid file and all --
// into place, so that it is never visible without its pid. A lock whose
// holder is no longer running is stale, and is taken over rather than
// left to block every later build.
//
// Rename is by path, and cannot be made conditional on what is at the
// path, so acquisition -- judging the lock, taking a stale one over,
// sweeping this package's own leftovers -- is serialised by a short
// flock(2) on the parent directory. That flock is held for a few system
// calls, never for the build, and leaves no file behind. Where a
// filesystem refuses it, Acquire goes on without it and says so
// (Lock.Serialized): a fresh lock still has one holder, but two takers
// of one stale lock can then both win.
//
// It excludes only other processes of this project: nothing may rely on
// this lock excluding another builder, or on its files being read by one.
package lock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// A Lock is held until Release.
type Lock struct {
	Dir string
	PID int
	// TookOver is the stale holder's pid when Acquire took the lock over,
	// -1 when the stale lock named no pid, and 0 when the lock was free.
	TookOver int
	// Serialized is whether acquisition held the flock on the parent. When
	// it is false the filesystem refused flock: the lock was still never
	// visible without its pid, but a stale lock two builders judged at
	// once may have been taken over by both, and no leftovers were swept.
	Serialized bool
}

// flock is flock(2); tests make it fail.
var flock = syscall.Flock

// Acquire takes the lock at dir for pid; a pid below 1 is this process's,
// since a lock naming pid 0 names nobody and is stale to everyone. A lock
// held by a live process is refused, and left exactly as it was.
//
// The lock is never visible without its pid. It is assembled as a
// temporary directory beside dir, pid and all, and renamed onto dir,
// which is atomic: a mkdir-then-write lock is briefly an empty directory,
// which a second builder would judge stale and take over. A stale lock
// is renamed aside to a unique name first, which only one taker can do,
// and removed from there.
//
// That rename is by path, and cannot be made conditional on what is at
// the path: a taker that judged the old lock stale can, a moment later,
// move aside the fresh lock another taker has just put there, and both
// then hold it. So acquisition holds a flock on the parent directory for
// the few system calls it lasts (serialize), which makes judging and
// taking over one step. Under it, Acquire first sweeps what this package
// leaves beside the lock when a builder is killed mid-way (sweep).
func Acquire(dir string, pid int) (*Lock, error) {
	if pid < 1 {
		pid = os.Getpid()
	}
	parent, base := filepath.Dir(dir), filepath.Base(dir)
	l := &Lock{Dir: dir, PID: pid}
	unlock, ok := serialize(parent)
	defer unlock()
	l.Serialized = ok
	if ok {
		// Only under the flock: a live acquirer's temporary directory has
		// no pid for a moment, and the flock is what hides that moment.
		sweep(parent, base)
	}
	tmp, err := os.MkdirTemp(parent, tmpPrefix(base)+"*")
	if err != nil {
		return nil, fmt.Errorf("cannot create the lock %s: %w", dir, err)
	}
	held := false
	defer func() {
		if !held {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the lock %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("cannot write the lock's pid: %w", err)
	}
	for attempt := 0; attempt < 100; attempt++ {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			// Rename replaces an empty directory, so dir is checked
			// first: an empty lock is a stale one, and is reported.
			if err := os.Rename(tmp, dir); err == nil {
				held = true
				return l, nil
			} else if !exists(err) {
				return nil, fmt.Errorf("cannot create the lock %s: %w", dir, err)
			}
			continue // another builder got there first: look again
		} else if err != nil {
			return nil, fmt.Errorf("cannot read the lock %s: %w", dir, err)
		}
		holder := readPID(dir)
		if holder > 0 && alive(holder) {
			return nil, fmt.Errorf("pid %d is already building here (lock %s): two builders sharing one image file corrupt it, and each verifies its own page cache and sees nothing wrong -- wait for it, or stop it and remove %s", holder, dir, dir)
		}
		aside := dir + staleInfix + randomSuffix()
		if err := os.Rename(dir, aside); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // another taker moved it first
			}
			return nil, fmt.Errorf("cannot remove the stale lock %s: %w", dir, err)
		}
		l.TookOver = holder
		if holder == 0 {
			l.TookOver = -1
		}
		if err := os.RemoveAll(aside); err != nil {
			return nil, fmt.Errorf("cannot remove the stale lock %s (moved to %s): %w", dir, aside, err)
		}
	}
	return nil, fmt.Errorf("cannot take the lock %s: it kept changing under this builder", dir)
}

// The names this package gives the directories it makes beside a lock
// named base: the temporary one a lock is assembled in, a stale lock
// moved aside, and a released one moved aside. Each holds a pid file.
const (
	staleInfix    = ".stale-"
	releasedInfix = ".released-"
)

func tmpPrefix(base string) string { return "." + base + ".tmp-" }

// sweep removes, from parent, this package's own directories for the
// lock base -- by exact prefix, read from the directory and never
// globbed, so a parent holding a glob character is taken literally --
// whose pid file is missing or names a process that is not running: what
// a builder killed between two of its renames leaves. A live pid's is in
// use and stays, and so does anything else. It is best effort: what it
// cannot remove it leaves for the next sweep, and acquisition goes on.
func sweep(parent, base string) {
	ents, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	prefixes := []string{tmpPrefix(base), base + staleInfix, base + releasedInfix}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		for _, prefix := range prefixes {
			if !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			p := filepath.Join(parent, e.Name())
			if holder := readPID(p); holder == 0 || !alive(holder) {
				_ = os.RemoveAll(p)
			}
			break
		}
	}
}

// serialize holds an exclusive flock on dir until the returned function
// is called, and says whether it could take it. Where the filesystem
// refuses flock on a directory (some network filesystems do), it holds
// nothing, and acquisition keeps the rename-only protocol, which is still
// never seen without its pid.
func serialize(dir string) (func(), bool) {
	f, err := os.Open(dir)
	if err != nil {
		return func() {}, false
	}
	for {
		err = flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return func() {}, false
	}
	return func() {
		_ = flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true
}

// exists is whether a rename failed because its target is already there:
// EEXIST or ENOTEMPTY, which systems use interchangeably for a directory.
func exists(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTEMPTY)
}

func randomSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Release removes the lock, but only while it is still this holder's: a
// later builder that took it over owns it now. The lock is renamed aside
// before it is removed, so that no other builder ever sees it half gone,
// and under the parent's flock, so that no sweep sees it half removed.
func (l *Lock) Release() error {
	unlock, _ := serialize(filepath.Dir(l.Dir))
	defer unlock()
	if _, err := os.Lstat(l.Dir); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the lock %s is gone: something removed it while pid %d held it", l.Dir, l.PID)
	}
	if got := readPID(l.Dir); got != l.PID {
		return fmt.Errorf("the lock %s is no longer this process's: it names pid %d, not %d; leaving it", l.Dir, got, l.PID)
	}
	aside := l.Dir + releasedInfix + randomSuffix()
	if err := os.Rename(l.Dir, aside); err != nil {
		return fmt.Errorf("cannot release the lock %s: %w", l.Dir, err)
	}
	return os.RemoveAll(aside)
}

// readPID is the pid the lock names, or 0 when it names none. A pid below
// 1 counts as none: kill(0) and kill(-n) address process groups, and
// would find "a live holder" in any lock whose file said so.
func readPID(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// alive is kill(pid, 0): EPERM means it exists and is someone else's.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
