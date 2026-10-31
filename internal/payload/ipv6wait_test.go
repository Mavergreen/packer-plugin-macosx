package payload

// 10.6's wait for IPv6 before launchctl binds sockets (docs/decisions/0029).
//
// 10.6's launchctl binds the sockets of the jobs Bonjour advertises, ssh's
// among them, about a second into the boot; the kernel refuses every IPv6
// bind until it has its first IPv6 address, which it adds from a timer of
// its own; and launchctl, at the first bind that fails, drops all of the
// job's sockets, IPv4 too: no sshd listener for the whole boot (F18,
// MEASURED 2026-10-19). The function's own cases, and the rc.server's, are
// tests/firstboot_ipv6_wait.bats; these say where firstboot.sh calls it.

import (
	"regexp"
	"strings"
	"testing"
)

var ipv6WaitCallRe = regexp.MustCompile(`^\s*set_ipv6_wait\s*$`)

// Called once, on the main line, for 10.6 alone, with updates or without:
// the first boot runs the main line either way, and no update writes
// /etc/rc.server. 10.9 never lost a listener this way, and nobody measured
// its bootstrap, so it gets none.
func TestFirstbootShWritesTheIPv6WaitOn10_6Alone(t *testing.T) {
	b := firstbootSh(t)
	lines := strings.Split(string(b), "\n")
	calls := 0
	for i, line := range lines {
		if !ipv6WaitCallRe.MatchString(line) {
			continue
		}
		calls++
		want := `if [ "$GUEST_MACOSX_FB_RELEASE" = snowleopard ]; then`
		if got := strings.TrimSpace(lines[i-1]); got != want {
			t.Errorf("set_ipv6_wait at line %d is not 10.6's alone; the line before it is %q", i+1, got)
		}
	}
	if calls != 1 {
		t.Fatalf("firstboot.sh calls set_ipv6_wait %d times; want 1", calls)
	}
}

// Before 10.6's updates begin: with updates, the main line hands off to
// snow_leopard_updates and never comes back, so a call after that would
// never run.
func TestFirstbootShWritesTheIPv6WaitBeforeTheUpdates(t *testing.T) {
	b := firstbootSh(t)
	call := lineOf(b, ipv6WaitCallRe)
	updates := lineOfLast(b, regexp.MustCompile(`^\s*snow_leopard_updates\s*$`))
	if call < 0 || updates < 0 {
		t.Fatalf("set_ipv6_wait at line %d, the main line's snow_leopard_updates at line %d; want both", call+1, updates+1)
	}
	if call > updates {
		t.Fatalf("set_ipv6_wait at line %d comes after the main line's snow_leopard_updates (line %d)", call+1, updates+1)
	}
}
