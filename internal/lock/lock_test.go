package lock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func pidIn(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// deadPID is the pid of a process that has exited and been reaped (in
// shell, `sleep 0 &; wait`).
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Start(); err != nil {
		t.Skipf("cannot start true: %v", err)
	}
	pid := c.Process.Pid
	_ = c.Wait()
	return pid
}

func TestAcquireCreatesTheLockAndReleaseRemovesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out.img.lock")
	l, err := Acquire(dir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if l.TookOver != 0 {
		t.Errorf("TookOver = %d on a free lock", l.TookOver)
	}
	if got := pidIn(t, dir); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("pid file %q", got)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("lock still there after Release: %v", err)
	}
}

// Two builders over one image file corrupt it, and each verifies its own
// page cache and sees nothing wrong.
func TestASecondBuilderIsRefused(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()
	holder := sleeper.Process.Pid

	dir := filepath.Join(t.TempDir(), "out.img.lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(holder)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir, os.Getpid())
	if err == nil {
		t.Fatalf("acquired a live holder's lock: %+v", l)
	}
	for _, want := range []string{"pid " + strconv.Itoa(holder), "is already building", dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	// And it must not have touched the holder's lock on its way out.
	if got := pidIn(t, dir); got != strconv.Itoa(holder)+"\n" {
		t.Fatalf("holder's pid file rewritten: %q", got)
	}
}

func TestAStaleLockIsTakenOver(t *testing.T) {
	dead := deadPID(t)
	dir := filepath.Join(t.TempDir(), "out.img.lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(dead)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if l.TookOver != dead {
		t.Errorf("TookOver = %d, want %d", l.TookOver, dead)
	}
	if got := pidIn(t, dir); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("pid file %q after takeover", got)
	}
}

// A lock directory with no pid in it is stale too: a builder killed
// between making the directory and writing its pid.
func TestALockWithNoPidIsTakenOver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out.img.lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if l.TookOver != -1 {
		t.Errorf("TookOver = %d, want -1 (unknown holder)", l.TookOver)
	}
	if got := pidIn(t, dir); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("pid file %q after takeover", got)
	}
}

// A lock taken over by a later builder -- this one was judged stale, or
// someone removed it by hand -- is that builder's now, not ours to remove.
func TestReleaseOnlyRemovesItsOwnLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out.img.lock")
	l, err := Acquire(dir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	other := os.Getpid() + 1
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(other)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = l.Release()
	if err == nil || !strings.Contains(err.Error(), "no longer") {
		t.Fatalf("Release of someone else's lock: err = %v", err)
	}
	if got := pidIn(t, dir); got != strconv.Itoa(other)+"\n" {
		t.Fatalf("the later taker's lock was disturbed: %q", got)
	}
}

// Many builders racing for one lock, fresh and stale, never both win: a
// lock is never visible without its pid (the window a mkdir-then-write
// lock has), and a stale lock is taken over by exactly one of them. Every
// racer here has this process's pid, which is live, so every loser must be
// refused. Nothing is left beside the lock but the lock itself.
func TestRacingBuildersNeverBothWin(t *testing.T) {
	dead := deadPID(t)
	const racers, rounds = 8, 300
	for _, stale := range []bool{false, true} {
		for round := 0; round < rounds; round++ {
			parent := t.TempDir()
			dir := filepath.Join(parent, "out.img.lock")
			if stale {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(dead)+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var (
				mu      sync.Mutex
				winners []*Lock
				errs    []error
				start   = make(chan struct{})
				wg      sync.WaitGroup
			)
			for i := 0; i < racers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					l, err := Acquire(dir, os.Getpid())
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						winners = append(winners, l)
					} else {
						errs = append(errs, err)
					}
				}()
			}
			close(start)
			wg.Wait()
			if len(winners) != 1 {
				t.Fatalf("stale=%v round %d: %d holders (refusals: %v)", stale, round, len(winners), errs)
			}
			for _, err := range errs {
				if !strings.Contains(err.Error(), "is already building") {
					t.Fatalf("stale=%v round %d: a loser failed otherwise: %v", stale, round, err)
				}
			}
			want := 0
			if stale {
				want = dead
			}
			if winners[0].TookOver != want {
				t.Fatalf("stale=%v round %d: TookOver %d, want %d", stale, round, winners[0].TookOver, want)
			}
			if err := winners[0].Release(); err != nil {
				t.Fatal(err)
			}
			if left, _ := os.ReadDir(parent); len(left) != 0 {
				var names []string
				for _, e := range left {
					names = append(names, e.Name())
				}
				t.Fatalf("stale=%v round %d: left behind %v", stale, round, names)
			}
		}
	}
}

// A lock that is gone -- removed by hand, say -- is reported as gone,
// not as held by "pid 0".
func TestReleaseOfAVanishedLockSaysItIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out.img.lock")
	l, err := Acquire(dir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	err = l.Release()
	if err == nil || !strings.Contains(err.Error(), "is gone") || strings.Contains(err.Error(), "pid 0") {
		t.Fatalf("err = %v", err)
	}
}

// plant makes a directory beside the lock, as a killed builder would have
// left it, holding pid (none when pid is 0).
func plant(t *testing.T, parent, name string, pid int) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if pid != 0 {
		if err := os.WriteFile(filepath.Join(p, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// A builder killed mid-acquisition or mid-release leaves this package's
// own directories beside the lock. The next Acquire sweeps those whose
// pid is dead or missing, and nothing else: a live pid's is still in use,
// and a name this package does not make is someone else's.
func TestAcquireSweepsItsOwnLeftovers(t *testing.T) {
	dead := deadPID(t)
	live := os.Getppid() // alive: it is running this test
	parent := t.TempDir()
	dir := filepath.Join(parent, "out.img.lock")
	var gone, kept []string
	for _, prefix := range []string{".out.img.lock.tmp-", "out.img.lock.stale-", "out.img.lock.released-"} {
		gone = append(gone, plant(t, parent, prefix+"dead", dead), plant(t, parent, prefix+"nopid", 0))
		kept = append(kept, plant(t, parent, prefix+"live", live))
	}
	kept = append(kept,
		plant(t, parent, "other.lock.stale-dead", dead),
		plant(t, parent, "out.img.lock.old", dead),
		plant(t, parent, ".out.img.lock.tmpdead", dead),
		plant(t, parent, "xout.img.lock.stale-dead", dead))
	notADir := filepath.Join(parent, "out.img.lock.stale-file")
	if err := os.WriteFile(notADir, []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept = append(kept, notADir)

	l, err := Acquire(dir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if !l.Serialized {
		t.Skip("this filesystem refuses flock on a directory: nothing is swept without it")
	}
	for _, p := range gone {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s was not swept (%v)", filepath.Base(p), err)
		}
	}
	for _, p := range kept {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was swept: %v", filepath.Base(p), err)
		}
	}
	if got := pidIn(t, filepath.Join(parent, ".out.img.lock.tmp-live")); got != strconv.Itoa(live)+"\n" {
		t.Errorf("a live leftover was changed: %q", got)
	}
}

// Where flock cannot be taken, Acquire says so (Serialized is false),
// sweeps nothing -- a live acquirer's temporary directory has no pid for
// a moment, and only the flock makes that moment unobservable -- and a
// fresh lock still has exactly one holder: the rename-only protocol
// never shows a lock without its pid.
func TestWithoutFlockAFreshLockStillHasOneHolder(t *testing.T) {
	orig := flock
	flock = func(int, int) error { return syscall.ENOLCK }
	t.Cleanup(func() { flock = orig })

	dead := deadPID(t)
	const racers, rounds = 8, 200
	for round := 0; round < rounds; round++ {
		parent := t.TempDir()
		dir := filepath.Join(parent, "out.img.lock")
		leftover := plant(t, parent, "out.img.lock.stale-dead", dead)
		var (
			mu      sync.Mutex
			winners []*Lock
			errs    []error
			start   = make(chan struct{})
			wg      sync.WaitGroup
		)
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				l, err := Acquire(dir, os.Getpid())
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					winners = append(winners, l)
				} else {
					errs = append(errs, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if len(winners) != 1 {
			t.Fatalf("round %d: %d holders (refusals: %v)", round, len(winners), errs)
		}
		if winners[0].Serialized {
			t.Fatalf("round %d: Serialized with flock failing", round)
		}
		for _, err := range errs {
			if !strings.Contains(err.Error(), "is already building") {
				t.Fatalf("round %d: a loser failed otherwise: %v", round, err)
			}
		}
		if _, err := os.Lstat(leftover); err != nil {
			t.Fatalf("round %d: swept without the flock: %v", round, err)
		}
		if err := winners[0].Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAcquireIsSerializedWhereFlockWorks(t *testing.T) {
	l, err := Acquire(filepath.Join(t.TempDir(), "out.img.lock"), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if !l.Serialized {
		t.Skip("this filesystem refuses flock on a directory")
	}
}

// A lock naming pid 0 names nobody, and every other builder would take
// it over: a pid below 1 is this process's.
func TestAnUnsetPIDIsThisProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out.img.lock")
	l, err := Acquire(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if want := strconv.Itoa(os.Getpid()); pidIn(t, dir) != want+"\n" || l.PID != os.Getpid() {
		t.Fatalf("pid file %q, Lock.PID %d; want %s", pidIn(t, dir), l.PID, want)
	}
}
