package payload

// kextd held off while an update that installs a kernel goes in: 10.6's
// (docs/decisions/0026) and 10.9's (docs/decisions/0036). 10.6.0's kextd, 300 s after it starts and 5 s
// after each change to /System/Library/Extensions after that, rescans
// when its caches are out of date and sends every kext's personalities to
// the running kernel: during 10.6.8's combo, 10.6.8's kexts to 10.6.0's
// kernel, which loaded AudioAUUC 1.57 and, once, panicked in it (F24). The
// function's own cases are tests/firstboot_updates.bats; these say where
// firstboot.sh calls it.

import (
	"regexp"
	"strings"
	"testing"
)

var holdKextdCallRe = regexp.MustCompile(`hold_kextd_for "\$CONF_DIR/updates/\$_u"`)

// snow_leopard_updates holds kextd off before each update's installer,
// never after it.
func TestSnowLeopardUpdatesHoldKextdOffBeforeEachInstall(t *testing.T) {
	upd := funcBody(firstbootSh(t), "snow_leopard_updates")
	if upd == "" {
		t.Fatal("firstboot.sh has no snow_leopard_updates")
	}
	call, install := -1, -1
	for i, line := range strings.Split(upd, "\n") {
		if holdKextdCallRe.MatchString(line) && call < 0 {
			call = i
		}
		if strings.Contains(line, "installer -verbose -pkg") && install < 0 {
			install = i
		}
	}
	if call < 0 {
		t.Fatal("snow_leopard_updates never calls hold_kextd_for")
	}
	if install < 0 || call > install {
		t.Fatalf("hold_kextd_for at line %d of snow_leopard_updates; want before its installer (line %d)", call+1, install+1)
	}
}

// And in 10.9's updates (install_updates, docs/decisions/0036): before each
// update's installer, never after it; and nowhere else.
func TestMavericksUpdatesHoldKextdOffBeforeEachInstall(t *testing.T) {
	b := firstbootSh(t)
	upd := funcBody(b, "install_updates")
	if upd == "" {
		t.Fatal("firstboot.sh has no install_updates")
	}
	call, install := -1, -1
	for i, line := range strings.Split(upd, "\n") {
		if holdKextdCallRe.MatchString(line) && call < 0 {
			call = i
		}
		if strings.Contains(line, "installer -verbose") && install < 0 {
			install = i
		}
	}
	if call < 0 {
		t.Fatal("install_updates never calls hold_kextd_for")
	}
	if install < 0 || call > install {
		t.Fatalf("hold_kextd_for at line %d of install_updates; want before its installer (line %d)", call+1, install+1)
	}
	calls := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "hold_kextd_for ") && !strings.HasPrefix(strings.TrimSpace(line), "#") &&
			!strings.HasPrefix(strings.TrimSpace(line), "hold_kextd_for()") {
			calls++
		}
	}
	if calls != 2 {
		t.Fatalf("firstboot.sh calls hold_kextd_for %d times; want 2, in snow_leopard_updates and install_updates", calls)
	}
}
