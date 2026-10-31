package payload

// UserEventAgent's telemetry plugin, set aside at install time
// (assets/guest/postinstall, "the telemetry plugin"; docs/decisions/0020).
//
// UserEventAgent (System) crashed in 11 of 335 boots in the runs that saw it. Its telemetry
// plugin walks every USB device through IOUSBLib, a CFPlugIn, while the main
// thread is still loading plugins. Each device's last Release schedules
// IOUSBLib for unloading while IOUSBLib's own code is still on the telemetry
// thread's stack, and the main thread runs CoreFoundation's scheduled
// unloads before it loads the next plugin: dlclose unmaps IOUSBLib under the
// telemetry thread (MEASURED 2026-10-14). The postinstall moves the plugin
// out of /System/Library/UserEventPlugins on the target volume, so no boot,
// the first included, ever loads it.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// runPostinstallOn builds the package, and runs its postinstall offline
// against target, as the OS X Installer would ($3 is the target volume).
func runPostinstallOn(t *testing.T, target string, key []byte) []byte {
	t.Helper()
	return runPostinstallWith(t, target, key, func(*Config) {})
}

// runPostinstallWith is runPostinstallOn for DefaultConfig changed by edit.
func runPostinstallWith(t *testing.T, target string, key []byte, edit func(*Config)) []byte {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyPath, key, 0o644); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.SSHKey = keyPath
	edit(&c)
	out := filepath.Join(dir, "guest-macosx-firstboot.pkg")
	if _, err := Build(context.Background(), proc.Exec{}, c, out, t.Logf); err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	postPath := filepath.Join(dir, "postinstall")
	if err := os.WriteFile(postPath, extractPostinstall(t, pkg), 0o755); err != nil {
		t.Fatal(err)
	}
	combined, err := exec.Command("sh", postPath, "/pkg", "/dest", target, "/").CombinedOutput()
	if err != nil {
		t.Fatalf("postinstall: %v: %s", err, combined)
	}
	return combined
}

func writeTelemetryTestFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestThePostinstallSetsTheTelemetryPluginAsideOnTheTarget(t *testing.T) {
	target := t.TempDir()
	plugins := filepath.Join(target, "System", "Library", "UserEventPlugins")
	aside := filepath.Join(target, "System", "Library", "UserEventPlugins-disabled")
	tele := filepath.Join("com.apple.telemetry.plugin", "Contents", "MacOS", "com.apple.telemetry")
	other := filepath.Join("com.apple.time.plugin", "Contents", "MacOS", "com.apple.time")
	writeTelemetryTestFile(t, filepath.Join(plugins, tele), "telemetry")
	writeTelemetryTestFile(t, filepath.Join(plugins, other), "time")

	out := runPostinstallOn(t, target, rsaPubKey(t))

	if _, err := os.Stat(filepath.Join(plugins, "com.apple.telemetry.plugin")); !os.IsNotExist(err) {
		t.Errorf("com.apple.telemetry.plugin is still in UserEventPlugins (stat: %v); UserEventAgent would load it\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(aside, tele)); err != nil || string(b) != "telemetry" {
		t.Errorf("com.apple.telemetry.plugin was not moved, whole, to UserEventPlugins-disabled: %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(plugins, other)); err != nil || string(b) != "time" {
		t.Errorf("another plugin was touched: %q, %v", b, err)
	}
}

// Snow Leopard has no telemetry plugin (and the same postinstall runs for
// it): nothing to move is no failure, and no empty directory is left.
func TestThePostinstallLeavesATargetWithoutTheTelemetryPluginAlone(t *testing.T) {
	target := t.TempDir()
	runPostinstallOn(t, target, rsaPubKey(t))
	if _, err := os.Stat(filepath.Join(target, "System", "Library", "UserEventPlugins-disabled")); !os.IsNotExist(err) {
		t.Errorf("UserEventPlugins-disabled exists on a target that had no telemetry plugin (stat: %v)", err)
	}
}

// verify.sh requires "telemetry plugin set aside:" in the install's log on a
// 10.9 build, so an install that finds the plugin already absent says so, in
// a line that starts the same way and does not claim it moved anything. On
// Snow Leopard, which verify.sh does not ask, the log stays silent.
func TestThePostinstallSaysSoWhenThePluginIsAlreadyAbsentOn109ButNotOn106(t *testing.T) {
	for _, tc := range []struct {
		name, plist string
		want        bool
	}{
		{"10.9.5", "<key>ProductVersion</key>\n\t<string>10.9.5</string>", true},
		{"no SystemVersion.plist at all", "", true},
		{"10.6.8", "<key>ProductVersion</key>\n\t<string>10.6.8</string>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			if tc.plist != "" {
				writeTelemetryTestFile(t, filepath.Join(target, "System", "Library", "CoreServices", "SystemVersion.plist"), tc.plist)
			}
			out := runPostinstallOn(t, target, rsaPubKey(t))
			log, err := os.ReadFile(filepath.Join(target, "private", "var", "db", ".guest-macosx-firstboot", "postinstall.log"))
			if err != nil {
				t.Fatal(err)
			}
			got := regexp.MustCompile(`(?m)^telemetry plugin set aside: not present at install$`).Match(log)
			if got != tc.want {
				t.Errorf("log has the not-present line = %v, want %v\n%s", got, tc.want, out)
			}
			if strings.Contains(string(log), "COULD NOT") {
				t.Errorf("log claims a failure:\n%s", log)
			}
		})
	}
}

// Security Update 2016-004 carries the plugin (its Bom lists
// System/Library/UserEventPlugins/com.apple.telemetry.plugin), and
// firstboot.sh installs it on 10.9 with updates asked for, which puts the
// plugin back. So firstboot.sh sets it aside again, after the updates and
// before its marker; UserEventAgent, started without it, does not load it
// on this boot.
var (
	telemetryAsideCallRe = regexp.MustCompile(`^\s*set_telemetry_plugin_aside /\s*$`)
	telemetryAsideFuncRe = regexp.MustCompile(`(?s)\nset_telemetry_plugin_aside\(\) \{\n.*?\n\}\n`)
	updatesCallRe        = regexp.MustCompile(`^\s*install_updates\s*$`)
)

func TestFirstbootShSetsTheTelemetryPluginAsideAgainAfterTheUpdates(t *testing.T) {
	b := firstbootSh(t)
	lines := strings.Split(string(b), "\n")
	call, upd, done := lineOf(b, telemetryAsideCallRe), lineOf(b, updatesCallRe), lineOfLast(b, doneMarkerRe)
	if call < 0 {
		t.Fatal("firstboot.sh never calls set_telemetry_plugin_aside")
	}
	if upd < 0 || call < upd || call > done {
		t.Fatalf("set_telemetry_plugin_aside at line %d; want after 10.9's install_updates (line %d) and before the marker (line %d)", call+1, upd+1, done+1)
	}
	if strings.TrimSpace(lines[call-1]) != `if [ -z "$GUEST_MACOSX_FB_RELEASE" ]; then` {
		t.Errorf("set_telemetry_plugin_aside is not 10.9's alone; the line before it is %q", strings.TrimSpace(lines[call-1]))
	}
}

// The function itself, run under this host's sh against a fake volume:
// the install set the 10.9.5 plugin aside, the update put its own back, and
// the update's copy is the one kept aside (whole, not nested in the first).
func TestFirstbootShsTelemetryFunctionMovesThePluginAside(t *testing.T) {
	m := telemetryAsideFuncRe.Find(firstbootSh(t))
	if m == nil {
		t.Fatal("firstboot.sh has no set_telemetry_plugin_aside() { ... } function")
	}
	root := t.TempDir()
	plugins := filepath.Join(root, "System", "Library", "UserEventPlugins")
	aside := filepath.Join(root, "System", "Library", "UserEventPlugins-disabled")
	tele := filepath.Join("com.apple.telemetry.plugin", "Contents", "Info.plist")
	writeTelemetryTestFile(t, filepath.Join(aside, tele), "10.9.5")
	writeTelemetryTestFile(t, filepath.Join(plugins, tele), "2016-004")
	writeTelemetryTestFile(t, filepath.Join(plugins, "com.apple.time.plugin", "Contents", "Info.plist"), "time")
	script := "say() { echo \"$*\"; }\n" + string(m) + "set_telemetry_plugin_aside \"$1\"\n"
	for i := 0; i < 2; i++ { // the second run finds nothing to do
		out, err := exec.Command("sh", "-c", script, "sh", root).CombinedOutput()
		if err != nil {
			t.Fatalf("set_telemetry_plugin_aside: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(plugins, "com.apple.telemetry.plugin")); !os.IsNotExist(err) {
			t.Errorf("the plugin is still in UserEventPlugins (stat: %v)\n%s", err, out)
		}
		if b, err := os.ReadFile(filepath.Join(aside, tele)); err != nil || string(b) != "2016-004" {
			t.Errorf("the update's plugin is not the one aside: %q, %v\n%s", b, err, out)
		}
		if _, err := os.Stat(filepath.Join(aside, "com.apple.telemetry.plugin", "com.apple.telemetry.plugin")); !os.IsNotExist(err) {
			t.Errorf("the plugin was moved into the old one, not over it (stat: %v)", err)
		}
	}
	if _, err := os.Stat(filepath.Join(plugins, "com.apple.time.plugin")); err != nil {
		t.Errorf("another plugin was touched: %v", err)
	}
}
