package payload

// 10.6.8's BootCache, set aside by firstboot.sh (docs/decisions/0025).
//
// BootCache.kext 31.1 shuts its cache down by itself inside its strategy
// hook, on the reading thread, once a read takes its hit rate under 50 %
// after 500 lookups: it closes /var/db/BootCache.data there, and HFS's
// inactive call wants a journal transaction while that thread holds a
// B-tree's lock (the catalog's or the attributes file's) shared. A writer
// in a transaction that wants that lock, or the reader itself, and the
// guest's file system stops (MEASURED 2026-10-18, F22). The hook goes in
// only on `BootCacheControl start`, which launchctl runs only from
// /usr/sbin. The function's own cases are tests/firstboot_bootcache.bats;
// these say where firstboot.sh calls it.

import (
	"regexp"
	"strings"
	"testing"
)

var bootcacheAsideCallRe = regexp.MustCompile(`^\s*set_bootcache_aside\s*$`)

// The updates put a BootCacheControl back (10.6.8's combo carries it), so
// with updates the set-aside is finish_snow_leopard's, on the boot of the
// last one, before that boot's restart.
func TestFinishSnowLeopardSetsBootCacheAsideBeforeItsRestart(t *testing.T) {
	fin := funcBody(firstbootSh(t), "finish_snow_leopard")
	if fin == "" {
		t.Fatal("firstboot.sh has no finish_snow_leopard")
	}
	call, restart := -1, -1
	for i, line := range strings.Split(fin, "\n") {
		if bootcacheAsideCallRe.MatchString(line) && call < 0 {
			call = i
		}
		if strings.HasPrefix(strings.TrimSpace(line), "shutdown -r now") && restart < 0 {
			restart = i
		}
	}
	if call < 0 {
		t.Fatal("finish_snow_leopard never calls set_bootcache_aside")
	}
	if restart < 0 || call > restart {
		t.Fatalf("set_bootcache_aside at line %d of finish_snow_leopard; want before its restart (line %d)", call+1, restart+1)
	}
}

// 10.6.8's combo puts BootCache 31.1 and its own BootCacheControl on the
// disk, and the next boot (the one that installs 2013-004, its installer
// a load of reads the stale playlist misses) would start it. So the
// restart between updates sets it aside first, as soon as 31.1 is there.
func TestSnowLeopardUpdatesSetBootCacheAsideBeforeTheRestartBetweenThem(t *testing.T) {
	upd := funcBody(firstbootSh(t), "snow_leopard_updates")
	if upd == "" {
		t.Fatal("firstboot.sh has no snow_leopard_updates")
	}
	lines := strings.Split(upd, "\n")
	restart := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "shutdown -r now") {
			restart = i
			break
		}
	}
	if restart < 0 {
		t.Fatal("snow_leopard_updates has no restart between updates")
	}
	call := -1
	for i := 0; i < restart; i++ {
		if bootcacheAsideCallRe.MatchString(lines[i]) {
			call = i
		}
	}
	if call < 0 {
		t.Fatalf("snow_leopard_updates never calls set_bootcache_aside before its restart (line %d)", restart+1)
	}
	for i := call + 1; i < restart; i++ {
		if strings.Contains(lines[i], "installer ") {
			t.Fatalf("set_bootcache_aside at line %d comes before an install at line %d, not after it", call+1, i+1)
		}
	}
}

// Without updates, 10.6's main line calls it, and only there: the deadlock
// was measured on 10.6.8 alone, and 10.9's BootCache is another.
func TestFirstbootShSetsBootCacheAsideOn10_6WithoutUpdatesAlone(t *testing.T) {
	b := firstbootSh(t)
	lines := strings.Split(string(b), "\n")
	inFunc := func(name string, i int) bool {
		start := lineOf(b, regexp.MustCompile(`^`+name+`\(\) \{`))
		end := start + strings.Count(funcBody(b, name), "\n")
		if start < 0 {
			t.Fatalf("firstboot.sh has no %s", name)
		}
		return i >= start && i <= end
	}
	calls := 0
	for i, line := range lines {
		if !bootcacheAsideCallRe.MatchString(line) || inFunc("finish_snow_leopard", i) || inFunc("snow_leopard_updates", i) {
			continue
		}
		calls++
		want := `if [ "$GUEST_MACOSX_FB_RELEASE" = snowleopard ] && [ "$GUEST_MACOSX_FB_RESTART" = 0 ]; then`
		if got := strings.TrimSpace(lines[i-1]); got != want {
			t.Errorf("set_bootcache_aside at line %d is not 10.6-without-updates' alone; the line before it is %q", i+1, got)
		}
	}
	if calls != 1 {
		t.Fatalf("firstboot.sh's main line calls set_bootcache_aside %d times outside finish_snow_leopard; want 1", calls)
	}
}

// The versions it sets aside are the ones measured, and nothing wider.
func TestBootCacheDeadlocksIsTheMeasuredVersionAlone(t *testing.T) {
	m := regexp.MustCompile(`(?m)^BOOTCACHE_DEADLOCKS="([^"]*)"$`).FindSubmatch(firstbootSh(t))
	if m == nil {
		t.Fatal(`firstboot.sh has no BOOTCACHE_DEADLOCKS="..." line`)
	}
	if string(m[1]) != "31.1" {
		t.Errorf("BOOTCACHE_DEADLOCKS is %q; only 31.1 (10.6.8's) was measured to deadlock", m[1])
	}
}
