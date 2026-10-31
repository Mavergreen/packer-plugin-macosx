package payload

// systemstats' three jobs, set aside with the telemetry plugin
// (assets/guest/postinstall, "the systemstats jobs"; docs/decisions/0022).
//
// systemstatsd answers a request for statistics (systemstats --daily's
// spindump, at 00:15 every day) only when the telemetry plugin next sends
// it data: it posts com.apple.telemetry.immediate, holds an XPC transaction
// open, and ends it on the plugin's event. With the plugin set aside
// (docs/decisions/0020) no event ever comes, so systemstatsd stays dirty for
// the rest of the boot, ignores SIGTERM as a transaction-tracked job does,
// and holds every later shutdown its full 20 s (MEASURED 2026-10-15). So
// where the plugin is set aside, the postinstall sets its three consumers
// aside too; where it is not, it leaves them alone.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var systemstatsJobs = []string{"com.apple.systemstatsd", "com.apple.systemstats.daily", "com.apple.systemstats.analysis"}

// a 10.9 target: the telemetry plugin, the three systemstats plists, and
// another daemon's plist that must not be touched.
func systemstatsTarget(t *testing.T) (target, daemons, aside string) {
	t.Helper()
	target = t.TempDir()
	daemons = filepath.Join(target, "System", "Library", "LaunchDaemons")
	aside = filepath.Join(target, "System", "Library", "LaunchDaemons-disabled")
	writeTelemetryTestFile(t, filepath.Join(target, "System", "Library", "UserEventPlugins",
		"com.apple.telemetry.plugin", "Contents", "MacOS", "com.apple.telemetry"), "telemetry")
	for _, j := range systemstatsJobs {
		writeTelemetryTestFile(t, filepath.Join(daemons, j+".plist"), j)
	}
	writeTelemetryTestFile(t, filepath.Join(daemons, "com.apple.spindump.plist"), "spindump")
	return target, daemons, aside
}

func postinstallLog(t *testing.T, target string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(target, "private", "var", "db", ".guest-macosx-firstboot", "postinstall.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestThePostinstallSetsTheSystemstatsJobsAsideWithTheTelemetryPlugin(t *testing.T) {
	target, daemons, aside := systemstatsTarget(t)
	out := runPostinstallOn(t, target, rsaPubKey(t))
	for _, j := range systemstatsJobs {
		if _, err := os.Stat(filepath.Join(daemons, j+".plist")); !os.IsNotExist(err) {
			t.Errorf("%s.plist is still in LaunchDaemons (stat: %v); launchd would load it\n%s", j, err, out)
		}
		if b, err := os.ReadFile(filepath.Join(aside, j+".plist")); err != nil || string(b) != j {
			t.Errorf("%s.plist was not moved, whole, to LaunchDaemons-disabled: %q, %v", j, b, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(daemons, "com.apple.spindump.plist")); err != nil || string(b) != "spindump" {
		t.Errorf("another daemon's plist was touched: %q, %v", b, err)
	}
	want := "systemstats jobs set aside, with the telemetry plugin: " + strings.Join(systemstatsJobs, " ") + "\n"
	if log := postinstallLog(t, target); !strings.Contains(log, want) {
		t.Errorf("the install's log does not say %q:\n%s", want, log)
	}
}

// Inert where its precondition is absent: the telemetry plugin still in
// UserEventPlugins (here, because it could not be set aside) feeds
// systemstatsd, which then answers, so its jobs stay, and the log says why.
func TestThePostinstallLeavesTheSystemstatsJobsWhileThePluginIsInPlace(t *testing.T) {
	target, daemons, aside := systemstatsTarget(t)
	// UserEventPlugins-disabled is a file: the plugin cannot be moved there.
	writeTelemetryTestFile(t, filepath.Join(target, "System", "Library", "UserEventPlugins-disabled"), "in the way")
	out := runPostinstallOn(t, target, rsaPubKey(t))
	for _, j := range systemstatsJobs {
		if b, err := os.ReadFile(filepath.Join(daemons, j+".plist")); err != nil || string(b) != j {
			t.Errorf("%s.plist was touched although the telemetry plugin is in place: %q, %v\n%s", j, b, err, out)
		}
	}
	if _, err := os.Stat(aside); !os.IsNotExist(err) {
		t.Errorf("LaunchDaemons-disabled exists although nothing was set aside (stat: %v)", err)
	}
	log := postinstallLog(t, target)
	if !strings.Contains(log, "systemstats jobs left in place: the telemetry plugin is still in UserEventPlugins\n") {
		t.Errorf("the install's log does not say the jobs were left in place, and why:\n%s", log)
	}
	if strings.Contains(log, "systemstats jobs set aside") {
		t.Errorf("the install's log claims it set the jobs aside:\n%s", log)
	}
}

// Snow Leopard has no systemstats jobs (and the same postinstall runs for
// it): nothing to move, nothing said, no empty directory left.
func TestThePostinstallLeavesATargetWithoutSystemstatsAlone(t *testing.T) {
	target := t.TempDir()
	writeTelemetryTestFile(t, filepath.Join(target, "System", "Library", "CoreServices", "SystemVersion.plist"),
		"<key>ProductVersion</key>\n\t<string>10.6.8</string>")
	runPostinstallOn(t, target, rsaPubKey(t))
	if _, err := os.Stat(filepath.Join(target, "System", "Library", "LaunchDaemons-disabled")); !os.IsNotExist(err) {
		t.Errorf("LaunchDaemons-disabled exists on a target that had no systemstats jobs (stat: %v)", err)
	}
	if log := postinstallLog(t, target); strings.Contains(log, "systemstats") {
		t.Errorf("the install's log speaks of systemstats on a target without it:\n%s", log)
	}
}

// One job already aside (a re-run, an earlier medium): the rest are moved,
// and the one aside is not nested in itself or lost.
func TestThePostinstallSetsAsideOnlyTheSystemstatsJobsStillInPlace(t *testing.T) {
	target, daemons, aside := systemstatsTarget(t)
	if err := os.Rename(filepath.Join(daemons, systemstatsJobs[0]+".plist"), filepath.Join(target, "x")); err != nil {
		t.Fatal(err)
	}
	writeTelemetryTestFile(t, filepath.Join(aside, systemstatsJobs[0]+".plist"), "earlier")
	runPostinstallOn(t, target, rsaPubKey(t))
	if b, err := os.ReadFile(filepath.Join(aside, systemstatsJobs[0]+".plist")); err != nil || string(b) != "earlier" {
		t.Errorf("the job already aside was changed: %q, %v", b, err)
	}
	want := "systemstats jobs set aside, with the telemetry plugin: " + strings.Join(systemstatsJobs[1:], " ") + "\n"
	log := postinstallLog(t, target)
	if !strings.Contains(log, want) {
		t.Errorf("the install's log does not say %q:\n%s", want, log)
	}
	if strings.Contains(log, "COULD NOT") {
		t.Errorf("the install's log claims a failure although every job in place was moved:\n%s", log)
	}
}

// A 10.9 medium that has no telemetry plugin at all (the postinstall's "not
// present at install" case) is F11's precondition from the start: the jobs
// are set aside there too, not only where the postinstall moved the plugin.
func TestThePostinstallSetsTheSystemstatsJobsAsideWhereThePluginIsNotPresent(t *testing.T) {
	target, daemons, aside := systemstatsTarget(t)
	if err := os.RemoveAll(filepath.Join(target, "System", "Library", "UserEventPlugins")); err != nil {
		t.Fatal(err)
	}
	out := runPostinstallOn(t, target, rsaPubKey(t))
	for _, j := range systemstatsJobs {
		if _, err := os.Stat(filepath.Join(daemons, j+".plist")); !os.IsNotExist(err) {
			t.Errorf("%s.plist is still in LaunchDaemons (stat: %v)\n%s", j, err, out)
		}
		if _, err := os.Stat(filepath.Join(aside, j+".plist")); err != nil {
			t.Errorf("%s.plist was not set aside: %v", j, err)
		}
	}
}

// A move that fails says so in the log, which jobs, and that launchd will
// load them: verify.sh fails the build too, but the log tells which way the
// install went. LaunchDaemons-disabled is a file here, so nothing can move.
func TestThePostinstallSaysWhichSystemstatsJobsItCouldNotSetAside(t *testing.T) {
	target, daemons, aside := systemstatsTarget(t)
	writeTelemetryTestFile(t, aside, "in the way")
	out := runPostinstallOn(t, target, rsaPubKey(t))
	for _, j := range systemstatsJobs {
		if b, err := os.ReadFile(filepath.Join(daemons, j+".plist")); err != nil || string(b) != j {
			t.Errorf("%s.plist was lost or changed by a move that failed: %q, %v\n%s", j, b, err, out)
		}
	}
	want := "systemstats jobs: COULD NOT set aside: " + strings.Join(systemstatsJobs, " ") + "; launchd will load them\n"
	if log := postinstallLog(t, target); !strings.Contains(log, want) {
		t.Errorf("the install's log does not say %q:\n%s", want, log)
	}
}

// "logs which": a 10.9 target with none of the jobs (a re-run, a medium
// without them) says so, so an empty line in verify.sh's report cannot be
// mistaken for a postinstall that died before the block. 10.6 stays silent
// (TestThePostinstallLeavesATargetWithoutSystemstatsAlone).
func TestThePostinstallSaysWhenAMavericksTargetHasNoSystemstatsJobs(t *testing.T) {
	target := t.TempDir()
	writeTelemetryTestFile(t, filepath.Join(target, "System", "Library", "CoreServices", "SystemVersion.plist"),
		"<key>ProductVersion</key>\n\t<string>10.9.5</string>")
	runPostinstallOn(t, target, rsaPubKey(t))
	if log := postinstallLog(t, target); !strings.Contains(log, "systemstats jobs set aside: none in LaunchDaemons at install\n") {
		t.Errorf("the install's log does not say there were none:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(target, "System", "Library", "LaunchDaemons-disabled")); !os.IsNotExist(err) {
		t.Errorf("LaunchDaemons-disabled exists although nothing was set aside (stat: %v)", err)
	}
}
