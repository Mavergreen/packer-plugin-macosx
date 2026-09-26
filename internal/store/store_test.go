package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingMake is a make func that writes marker into dir and counts how
// often it ran.
func countingMake(marker string) (func(context.Context, string) error, *int32) {
	var calls int32
	return func(_ context.Context, dir string) error {
		atomic.AddInt32(&calls, 1)
		return os.WriteFile(filepath.Join(dir, marker), []byte("built\n"), 0o644)
	}, &calls
}

func TestGetReusesWithTheSameListing(t *testing.T) {
	s := Store{Root: t.TempDir()}
	listing := []string{"a\t1", "b\t2"}
	make, calls := countingMake("marker")

	dir1, reused1, err := s.Get(context.Background(), "widget", listing, make)
	if err != nil {
		t.Fatal(err)
	}
	if reused1 {
		t.Fatal("first Get must run make, not reuse")
	}
	if _, err := os.Stat(filepath.Join(dir1, "marker")); err != nil {
		t.Fatalf("make did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir1, "complete")); err != nil {
		t.Fatalf("no complete marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir1, "inputs")); err != nil {
		t.Fatalf("no inputs file: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("make ran %d times, want 1", got)
	}

	dir2, reused2, err := s.Get(context.Background(), "widget", listing, make)
	if err != nil {
		t.Fatal(err)
	}
	if !reused2 {
		t.Fatal("second Get with the same listing must reuse")
	}
	if dir2 != dir1 {
		t.Fatalf("got a different directory: %q vs %q", dir2, dir1)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("make ran %d times on the second Get, want still 1", got)
	}
}

func TestGetWithADifferentListingMakesANewDirectory(t *testing.T) {
	s := Store{Root: t.TempDir()}
	make, calls := countingMake("marker")

	dir1, _, err := s.Get(context.Background(), "widget", []string{"a\t1"}, make)
	if err != nil {
		t.Fatal(err)
	}
	dir2, reused, err := s.Get(context.Background(), "widget", []string{"a\t2"}, make)
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Fatal("a different listing must not reuse")
	}
	if dir1 == dir2 {
		t.Fatalf("same directory for different listings: %q", dir1)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("make ran %d times, want 2", got)
	}
}

func TestAFailedMakeLeavesNoDirectoryAndNoLock(t *testing.T) {
	s := Store{Root: t.TempDir()}
	wantErr := errors.New("boom")

	dir, reused, err := s.Get(context.Background(), "widget", []string{"a\t1"}, func(context.Context, string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got err %v, want %v", err, wantErr)
	}
	if reused {
		t.Fatal("a failed make must not report reused")
	}
	if dir != "" {
		t.Fatalf("got dir %q, want none", dir)
	}

	kindDir := filepath.Join(s.Root, "widget")
	ents, err := os.ReadDir(kindDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("leftovers after a failed make: %v", names)
	}
}

func TestAConcurrentGetOnTheSameKeyIsRefusedByTheLock(t *testing.T) {
	s := Store{Root: t.TempDir()}
	started := make(chan struct{})
	proceed := make(chan struct{})

	holderMake := func(context.Context, string) error {
		close(started)
		<-proceed
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, _, err := s.Get(context.Background(), "widget", []string{"a\t1"}, holderMake); err != nil {
			t.Errorf("the holder's own Get failed: %v", err)
		}
	}()

	<-started
	_, _, err := s.Get(context.Background(), "widget", []string{"a\t1"}, func(context.Context, string) error {
		t.Fatal("a refused Get must not run make")
		return nil
	})
	close(proceed)
	wg.Wait()

	if err == nil {
		t.Fatal("a concurrent Get on the same key must be refused")
	}
	for _, want := range []string{"already building", "pid " + strconv.Itoa(os.Getpid())} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

// TestAKilledMakesTempDirectoryIsSwept: make builds in a
// ".store-tmp-<digest>-*" directory, so that -- not an incomplete entry --
// is what a make killed without running its deferred cleanup leaves. The
// next Get for the same key removes it under the lock; another key's temp
// directory (a live make, for all this Get knows) is left alone.
func TestAKilledMakesTempDirectoryIsSwept(t *testing.T) {
	s := Store{Root: t.TempDir()}
	listing := []string{"a\t1"}
	digest := digestFor(t, listing)
	kindDir := filepath.Join(s.Root, "widget")

	mine := filepath.Join(kindDir, ".store-tmp-"+digest+"-123456")
	theirs := filepath.Join(kindDir, ".store-tmp-"+digestFor(t, []string{"b\t2"})+"-654321")
	for _, d := range []string{mine, theirs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "installer-media.img"), []byte("half\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	make, calls := countingMake("marker")
	dir, reused, err := s.Get(context.Background(), "widget", listing, make)
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Fatal("a key with only a killed make's temp directory must be built, not reused")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("make ran %d times, want 1", got)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("this key's leftover temp directory was not swept: %v", err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("another key's temp directory was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "complete")); err != nil {
		t.Fatalf("no complete marker: %v", err)
	}
}

// TestAHandDamagedEntryIsRebuilt: Get never renames an entry into place
// before its "complete" marker is written, so an entry without one has
// been damaged by hand (its marker deleted). It is rebuilt, not trusted.
func TestAHandDamagedEntryIsRebuilt(t *testing.T) {
	s := Store{Root: t.TempDir()}
	listing := []string{"a\t1"}
	make, calls := countingMake("marker")
	target, _, err := s.Get(context.Background(), "widget", listing, make)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(target, "complete")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale"), []byte("hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, reused, err := s.Get(context.Background(), "widget", listing, make)
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Fatal("an entry without its complete marker must not be reported as reused")
	}
	if dir != target {
		t.Fatalf("got %q, want %q", dir, target)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("make ran %d times, want 2", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); !os.IsNotExist(err) {
		t.Fatalf("the damaged entry was not replaced: %v", err)
	}
}

func TestAKindThatIsNotOnePlainPathElementIsRefused(t *testing.T) {
	s := Store{Root: t.TempDir()}
	for _, kind := range []string{"", ".", "..", "../escape", "a/b", "/abs", ".hidden", "a b"} {
		_, _, err := s.Get(context.Background(), kind, []string{"a\t1"}, func(context.Context, string) error {
			t.Fatalf("kind %q: make ran", kind)
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "store kind") {
			t.Errorf("kind %q: err = %v, want a refusal naming the kind", kind, err)
		}
	}
	if ents, _ := os.ReadDir(s.Root); len(ents) != 0 {
		t.Fatalf("a refused kind left %d entries under Root", len(ents))
	}
}

// digestFor is the digest Get will compute for listing, so a test can
// find the target directory a leftover must be planted at.
func digestFor(t *testing.T, listing []string) string {
	t.Helper()
	s := Store{Root: t.TempDir()}
	dir, _, err := s.Get(context.Background(), "probe", listing, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(dir)
}

func TestInputsFileHoldsTheListingOneRowPerLine(t *testing.T) {
	s := Store{Root: t.TempDir()}
	listing := []string{"a\t1", "b\t2"}
	dir, _, err := s.Get(context.Background(), "widget", listing, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\t1\nb\t2\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
