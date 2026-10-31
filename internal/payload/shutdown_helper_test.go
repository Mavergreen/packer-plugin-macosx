//go:build unix

package payload

// The shutdown helper's behaviour (assets/guest/firstboot.sh, "the shutdown
// helper"; docs/decisions/0019), run for real: its own sh and Python, as
// firstboot.sh writes them, against fakes.
//
// NOTHING HERE MAY REACH THE HOST. The helper shuts a machine down. So:
//   - every absolute path to a command in a copy of the helper is rewritten
//     to a fake in a fresh directory, and the copy is refused if any other
//     /bin, /sbin, /usr/bin or /usr/sbin path is left in it (the #!/bin/sh
//     line aside: the copy is run as `/bin/sh copy`, never by its shebang);
//   - PATH is that directory alone, so `id` and `killall` are fakes too;
//   - the fakes only record their arguments and print canned output. None of
//     them calls the command it stands in for;
//   - libSystem is a .so built here, whose proc_get_dirty reads a pid's
//     flags from a file, and whose mach_absolute_time is this host's
//     monotonic clock (or a frozen one).
// ps answers two questions: `pid=,command=` (the agents, from ps.txt) and
// `pid=,ppid=,pgid=,uid=,etime=,stat=,command=` (the jobs and their ages,
// from jobs.txt, each age counted from the helper's first look, so it grows
// as a real one does).
// Nothing the rig does is timed from when it was set up: a loaded host can
// take any time to get the helper to its first look. A dirty agent, a job's
// age, and a job that appears or goes are timed from the helper's own first
// look at them, and a duration the helper reports is checked against the
// fake calls on either side of it, not against a window of wall-clock time.
// python3 (the helper's /usr/bin/python) and a C compiler (for the .so) come
// from the host; without either, these skip.

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeDirtyC = `#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <time.h>
/* mach_absolute_time: CLOCK_MONOTONIC, in ticks of $FAKE_TIMEBASE
   ("<numer>/<denom>", 1/1 if unset), as mach_timebase_info says; frozen
   when $FAKE_MACH_FROZEN is set. mach_timebase_info returns
   $FAKE_TIMEBASE_RC (0 if unset), and is left out when built with
   -DFAKE_NO_TIMEBASE. */
struct fake_timebase { uint32_t numer, denom; };
static void fake_tb(uint32_t *n, uint32_t *d) {
	const char *s = getenv("FAKE_TIMEBASE");
	*n = 1;
	*d = 1;
	if (s != NULL && sscanf(s, "%u/%u", n, d) != 2) { *n = 1; *d = 1; }
}
#ifndef FAKE_NO_TIMEBASE
int mach_timebase_info(struct fake_timebase *t) {
	const char *rc = getenv("FAKE_TIMEBASE_RC");
	fake_tb(&t->numer, &t->denom);
	return rc != NULL ? atoi(rc) : 0;
}
#endif
/* Each read is also appended to $FAKE_CALLS.times as "clock <seconds>",
   the seconds being what the read means at the timebase it is given in:
   the helper's own clock, in order among its calls, so a test can see each
   decision a wait made by it. */
static void fake_clock_read(uint64_t t, uint32_t n, uint32_t d) {
	char p[1100];
	const char *c = getenv("FAKE_CALLS");
	FILE *f;
	if (c == NULL || d == 0) return;
	snprintf(p, sizeof p, "%s.times", c);
	f = fopen(p, "a");
	if (f == NULL) return;
	fprintf(f, "clock %.9f\n", (double)t * n / d / 1e9);
	fclose(f);
}
uint64_t mach_absolute_time(void) {
	struct timespec ts;
	uint32_t n, d;
	uint64_t t = 1000000000ULL;
	fake_tb(&n, &d);
	if (getenv("FAKE_MACH_FROZEN") == NULL) {
		clock_gettime(CLOCK_MONOTONIC, &ts);
		t = ((uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec) * d / n;
	}
	fake_clock_read(t, n, d);
	return t;
}
static double fake_now(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec + ts.tv_nsec / 1e9;
}
/* flags from $FAKE_DIRTY_DIR/<pid>: "<flags> [<seconds>]"; <seconds> after
   the first time this pid is asked about (kept in <pid>.first), the pid
   reads as tracked and clean (1). No file: 1. So a pid is dirty for the
   first <seconds> of the helper's own looks at it, however long the helper
   took to get there. */
int proc_get_dirty(int pid, uint32_t *flags) {
	char p[1024], q[1100];
	const char *d = getenv("FAKE_DIRTY_DIR");
	unsigned v = 1;
	double secs = 0, first;
	FILE *f;
	if (d == NULL) return -1;
	snprintf(p, sizeof p, "%s/%d", d, pid);
	f = fopen(p, "r");
	if (f != NULL) {
		if (fscanf(f, "%u %lf", &v, &secs) < 1) v = 1;
		fclose(f);
	}
	if (secs > 0) {
		snprintf(q, sizeof q, "%s.first", p);
		f = fopen(q, "r");
		if (f == NULL || fscanf(f, "%lf", &first) != 1) {
			if (f != NULL) fclose(f);
			first = fake_now();
			f = fopen(q, "w");
			if (f == NULL) return -1;
			fprintf(f, "%.6f\n", first);
		}
		fclose(f);
		if (fake_now() - first >= secs) v = 1;
	}
	*flags = v;
	return 0;
}
`

const (
	systemAgent  = "/usr/libexec/UserEventAgent (System)"
	lwAgent      = "/usr/libexec/UserEventAgent (LoginWindow)"
	appleIDAgent = "/System/Library/CoreServices/AppleIDAuthAgent"
	systemPlist  = "/System/Library/LaunchDaemons/com.apple.UserEventAgent-System.plist"
)

// helperRig is one run's fakes.
type helperRig struct {
	t                     *testing.T
	dir, fake, dirty, ps  string
	jobsFile              string
	calls, script, python string
	cc, src, lib          string
	env                   []string
	// noWait: the helper's first sleep ends it (see wantNoWait).
	noWait bool
}

// skipRig skips a test this host has no python3 or working cc to run the
// rig with: a laptop without them need not fail. In CI (GitHub Actions sets
// $CI) it fails instead, since a job whose rig tests all skip is green, and
// says nothing about the helper.
func skipRig(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("in CI, the rig must run: "+format, args...)
	}
	t.Skipf(format, args...)
}

func newHelperRig(t *testing.T, withProcGetDirty bool) *helperRig {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		skipRig(t, "no python3 to run the helper's Python with")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		skipRig(t, "no cc to build a fake libSystem with")
	}
	r := &helperRig{t: t, dir: t.TempDir(), python: py}
	r.fake = filepath.Join(r.dir, "fake")
	r.dirty = filepath.Join(r.dir, "dirty")
	r.ps = filepath.Join(r.dir, "ps.txt")
	r.jobsFile = filepath.Join(r.dir, "jobs.txt")
	r.calls = filepath.Join(r.dir, "calls.log")
	for _, d := range []string{r.fake, r.dirty} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// libSystem: with proc_get_dirty and mach_absolute_time, or (an OS
	// without them) with neither.
	src := filepath.Join(r.dir, "fake.c")
	body := fakeDirtyC
	if !withProcGetDirty {
		body = "int fake_libsystem_placeholder(void) { return 0; }\n"
	}
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(r.fake, "libSystem.so")
	r.cc, r.src, r.lib = cc, src, lib
	if out, err := exec.Command(cc, "-shared", "-fPIC", "-o", lib, src).CombinedOutput(); err != nil {
		// A cc that builds no shared library at all is this host's; a fake
		// that does not compile is the rig's own fault, and must not read
		// as every test here passing.
		probe := filepath.Join(r.dir, "probe.c")
		if werr := os.WriteFile(probe, []byte("int fake_probe(void) { return 0; }\n"), 0o644); werr != nil {
			t.Fatal(werr)
		}
		if exec.Command(cc, "-shared", "-fPIC", "-o", filepath.Join(r.dir, "probe.so"), probe).Run() != nil {
			skipRig(t, "cc builds no shared library here: %v\n%s", err, out)
		}
		t.Fatalf("cc could not build the fake libSystem: %v\n%s", err, out)
	}

	// The fakes. Each records its argv, one line per call, in calls.log, and
	// the time it was called with it in calls.log.times: the monotonic
	// clock, which the fake mach_absolute_time and proc_get_dirty read too.
	rec := `import os, sys, time
_argv = ' '.join([os.path.basename(sys.argv[0])] + sys.argv[1:])
with open(os.environ['FAKE_CALLS'], 'a') as f:
    f.write(_argv + '\n')
with open(os.environ['FAKE_CALLS'] + '.times', 'a') as f:
    f.write('%.6f %s\n' % (time.monotonic(), _argv))
`
	fakes := map[string]string{
		// ps: the table in ps.txt, as `ps -axwwo pid=,command=` prints it; or,
		// asked for ppid and etime too, the jobs in jobs.txt ("<pid> <ppid>
		// <age-seconds|now|respawn|bad> <command>"), each with its age as ps's
		// etime, [[dd-]hh:]mm:ss, and its state. An age is the job's at the
		// helper's first jobs query (kept in jobs.txt.first), and grows from
		// there, so how long the helper took to get to it does not matter.
		// "now" is a job that is always just starting; "respawn" is one that
		// is always just starting and is a new pid every time ps looks (a
		// count of looks, in jobs.txt.looks); "bad", "minus5" and
		// "minus30s" have an age ps prints as a clock step does ("-1:-30",
		// "-5:00", "00:-30"). A command in parentheses is a zombie, unless it
		// is marked "live:" (ps prints a live process whose arguments it
		// cannot read, one exiting say, as "(name)" too).
		// A pid of 0 is the helper's own (this ps's parent). FAKE_PS_FAIL=1
		// makes the jobs query exit 1, printing nothing, and
		// FAKE_PS_FAIL_AFTER=S makes it do so from S seconds after the first
		// one. FAKE_PS_DELAY_JOBS=S
		// and FAKE_PS_DELAY_AGENTS=S make the jobs query or the agents query
		// take S seconds more.
		"ps": rec + `_q = 'JOBS' if 'ppid=' in ' '.join(sys.argv[1:]) else 'AGENTS'
if os.environ.get('FAKE_PS_DELAY_' + _q):
    time.sleep(float(os.environ['FAKE_PS_DELAY_' + _q]))
if 'ppid=' not in ' '.join(sys.argv[1:]):
    sys.stdout.write(open(os.environ['FAKE_PS']).read())
    sys.exit(0)
_first = os.environ['FAKE_JOBS'] + '.first'
if not os.path.exists(_first):
    open(_first, 'w').write('%.6f\n' % time.monotonic())
since = time.monotonic() - float(open(_first).read())
_looks = os.environ['FAKE_JOBS'] + '.looks'
look = int(open(_looks).read()) + 1 if os.path.exists(_looks) else 1
open(_looks, 'w').write('%d\n' % look)
if os.environ.get('FAKE_PS_FAIL') == '1' or os.path.exists(os.environ['FAKE_JOBS'] + '.broken'):
    sys.exit(1)
if os.environ.get('FAKE_PS_FAIL_AFTER') and since >= float(os.environ['FAKE_PS_FAIL_AFTER']):
    sys.exit(1)
try:
    lines = open(os.environ['FAKE_JOBS']).read().splitlines()
except IOError:
    lines = []
for l in lines:
    pid, ppid, start, cmd = l.split(None, 3)
    uid, pgid, live = '0', None, False
    while True:
        if cmd.startswith('uid='):
            uid, _, cmd = cmd[4:].partition(':')
        elif cmd.startswith('pg:'):
            pgid, cmd = str(os.getpgrp()), cmd[3:]
        elif cmd.startswith('live:'):
            live, cmd = True, cmd[5:]
        else:
            break
    if pid == '0':
        pid = str(os.getppid())
    if pgid is None:
        pgid = pid
    if start == 'respawn':
        pid = str(int(pid) + look)
    # appear:S is a young job that is not there until S seconds after the
    # first jobs query; until:S is a young one that is gone from then on.
    if start.startswith('appear:'):
        if since < float(start[7:]):
            continue
        start = 'now'
    if start.startswith('until:'):
        if since >= float(start[6:]):
            continue
        start = 'now'
    if start == 'bad':
        et = '-1:-30'
    elif start == 'minus5':
        et = '-5:00'
    elif start == 'minus30s':
        et = '00:-30'
    else:
        a = 0 if start in ('now', 'respawn') else int(float(start) + since)
        d, a = divmod(a, 86400)
        h, a = divmod(a, 3600)
        m, sec = divmod(a, 60)
        et = '%02d:%02d' % (m, sec)
        if h or d:
            et = '%02d:' % h + et
        if d:
            et = '%d-' % d + et
    stat = 'Z' if cmd.startswith('(') and not live else 'Ss'
    sys.stdout.write('%5s %5s %5s %5s %11s %-4s %s\n' % (pid, ppid, pgid, uid, et, stat, cmd))
`,
		// launchctl unload: FAKE_UNLOAD=ok takes the System agent out of ps.txt;
		// anything else leaves it and fails, as launchctl does. As it runs,
		// FAKE_UNLOAD_STARTS_JOB=1 adds a young latecomerd (pid 990) to
		// jobs.txt, FAKE_UNLOAD_JOBS replaces jobs.txt (lines split at ";"),
		// and FAKE_UNLOAD_BREAKS_PS or FAKE_UNLOAD_FIXES_PS breaks or mends the
		// jobs query.
		"launchctl": rec + `if os.environ.get('FAKE_UNLOAD_STARTS_JOB'):
    open(os.environ['FAKE_JOBS'], 'a').write('990 1 now /usr/sbin/latecomerd\n')
if os.environ.get('FAKE_UNLOAD_JOBS'):
    open(os.environ['FAKE_JOBS'], 'w').write(os.environ['FAKE_UNLOAD_JOBS'].replace(';', '\n') + '\n')
if os.environ.get('FAKE_UNLOAD_BREAKS_PS'):
    open(os.environ['FAKE_JOBS'] + '.broken', 'w').close()
if os.environ.get('FAKE_UNLOAD_FIXES_PS'):
    os.remove(os.environ['FAKE_JOBS'] + '.broken')
if os.environ.get('FAKE_UNLOAD', 'ok') == 'ok':
    lines = open(os.environ['FAKE_PS']).read().splitlines(True)
    open(os.environ['FAKE_PS'], 'w').write(''.join(l for l in lines if '(System)' not in l))
    sys.exit(0)
sys.stderr.write('launchctl: Error unloading: ' + sys.argv[-1] + '\n')
sys.exit(1)
`,
		"logger":   rec,
		"killall":  rec + "sys.exit(1)\n", // no ManagedClient running
		"shutdown": rec,
		"id":       rec + "print(os.environ.get('FAKE_UID', '0'))\n",
	}
	for name, code := range fakes {
		p := filepath.Join(r.fake, name)
		if err := os.WriteFile(p, []byte("#!"+py+" -I\n"+code), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The helper, every command path pointed at the fakes.
	h := shutdownHelper(t)
	for _, rw := range [][2]string{
		{"/usr/bin/python ", py + " "},
		{"'/usr/lib/libSystem.B.dylib'", "'" + lib + "'"},
		{"'/bin/ps'", "'" + filepath.Join(r.fake, "ps") + "'"},
		{"'/bin/launchctl'", "'" + filepath.Join(r.fake, "launchctl") + "'"},
		{"'/usr/bin/logger'", "'" + filepath.Join(r.fake, "logger") + "'"},
		{"exec /sbin/shutdown ", "exec " + filepath.Join(r.fake, "shutdown") + " "},
	} {
		if !strings.Contains(h, rw[0]) {
			t.Fatalf("the helper no longer has %q; the rig would not fake it", rw[0])
		}
		h = strings.ReplaceAll(h, rw[0], rw[1])
	}
	// No other real command may be left in the copy.
	left := strings.ReplaceAll(strings.ReplaceAll(h, r.fake, "FAKE"), py, "PYTHON")
	if strings.HasPrefix(left, "#!") { // its interpreter line names /bin/sh, which is not run
		left = left[strings.IndexByte(left, '\n')+1:]
	}
	if m := regexp.MustCompile(`/(usr/)?s?bin/`).FindString(left); m != "" {
		t.Fatalf("the helper copy still names a real command (%s...); refusing to run it", m)
	}
	r.script = filepath.Join(r.dir, "guest-macosx-shutdown")
	if err := os.WriteFile(r.script, []byte(h), 0o644); err != nil {
		t.Fatal(err)
	}

	// The helper's own Python (not the fakes': they run with -I) loads this
	// sitecustomize, and says so in calls.log.site: runWith fails any run
	// whose Python ran without it, since every sleep would go unseen.
	//   - It records every time.sleep the helper asks for, before it
	//     sleeps, in calls.log.sleeps, and in calls.log.times as
	//     "sleep <seconds>", in order among the calls and clock reads: how
	//     long the helper chose to wait, whatever the host made of it.
	//   - With $FAKE_NO_SLEEP set, the first sleep ends the helper's Python
	//     there and then, so a wait that should not happen fails at once,
	//     by what the helper did, not at a deadline.
	//   - Once the sleeps asked for pass $FAKE_SLEEP_BUDGET seconds in all,
	//     it says so in calls.log.overbudget and ends the Python too: a wait
	//     without a bound is the helper's, seen in what it asks for, however
	//     fast or slow the host.
	//   - $FAKE_PY_CLOCK, if set, replaces its time.time (see pyClock).
	site := filepath.Join(r.dir, "site")
	if err := os.Mkdir(site, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "sitecustomize.py"), []byte(`import itertools, os, time
_calls = os.environ['FAKE_CALLS']
open(_calls + '.site', 'w').close()
_sleep = time.sleep
_budget = float(os.environ.get('FAKE_SLEEP_BUDGET') or 'inf')
_asked = [0.0]
def _recorded_sleep(s):
    with open(_calls + '.sleeps', 'a') as f:
        f.write('%r\n' % s)
    with open(_calls + '.times', 'a') as f:
        f.write('sleep %r\n' % s)
    if os.environ.get('FAKE_NO_SLEEP'):
        os._exit(97)
    _asked[0] += s
    if _asked[0] > _budget:
        with open(_calls + '.overbudget', 'w') as f:
            f.write('%r\n' % _asked[0])
        os._exit(98)
    _sleep(s)
time.sleep = _recorded_sleep
if os.environ.get('FAKE_PY_CLOCK'):
    _t = time.time()
    _c = itertools.count(1)
    _n = lambda: next(_c)
    time.time = eval('lambda: ' + os.environ['FAKE_PY_CLOCK'])
`), 0o644); err != nil {
		t.Fatal(err)
	}

	r.env = []string{
		"PATH=" + r.fake,
		"PYTHONPATH=" + site,
		"HOME=" + r.dir,
		"FAKE_CALLS=" + r.calls,
		"FAKE_PS=" + r.ps,
		"FAKE_JOBS=" + r.jobsFile,
		"FAKE_DIRTY_DIR=" + r.dirty,
	}
	return r
}

// relink rebuilds the fake libSystem with these -D defines.
func (r *helperRig) relink(defines ...string) {
	r.t.Helper()
	args := []string{"-shared", "-fPIC", "-o", r.lib}
	for _, d := range defines {
		args = append(args, "-D"+d)
	}
	if out, err := exec.Command(r.cc, append(args, r.src)...).CombinedOutput(); err != nil {
		r.t.Fatalf("cc: %v\n%s", err, out)
	}
}

// agents writes the ps table.
func (r *helperRig) agents(lines ...string) {
	r.t.Helper()
	text := "    1 /sbin/launchd\n"
	for _, l := range lines {
		text += l + "\n"
	}
	if err := os.WriteFile(r.ps, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// job is one line of jobs.txt: a process, its parent, and its age.
type job struct {
	pid, ppid int
	age       time.Duration // or justStarting, respawning, badAge
	cmd       string
}

// Special ages: always just starting; a new pid on every look; an age ps
// prints as a clock step does.
const (
	justStarting = -1 * time.Nanosecond
	respawning   = -2 * time.Nanosecond
	badAge       = -3 * time.Nanosecond
)

// jobs writes jobs.txt, each job with its age at the helper's first look.
func (r *helperRig) jobs(js ...job) {
	r.t.Helper()
	text := ""
	for _, j := range js {
		start := "now"
		switch j.age {
		case respawning:
			start = "respawn"
		case badAge:
			start = "bad"
		}
		if j.age >= 0 {
			start = strconv.FormatFloat(j.age.Seconds(), 'f', 3, 64)
		}
		text += strconv.Itoa(j.pid) + " " + strconv.Itoa(j.ppid) + " " + start + " " + j.cmd + "\n"
	}
	if err := os.WriteFile(r.jobsFile, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// rawJobs writes jobs.txt lines as the fake ps reads them.
func (r *helperRig) rawJobs(lines ...string) {
	r.t.Helper()
	if err := os.WriteFile(r.jobsFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// dirtyFor makes pid read dirty (flags 5) for d from the helper's first look
// at it, or for good when d is 0. Not from now: the helper may take any time
// to get to its first look.
func (r *helperRig) dirtyFor(pid int, d time.Duration) {
	r.t.Helper()
	v := "5"
	if d > 0 {
		v += " " + strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
	}
	if err := os.WriteFile(filepath.Join(r.dirty, strconv.Itoa(pid)), []byte(v+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

type helperRun struct {
	rc      int
	stderr  string
	calls   []string
	stamps  []stamp   // the fake calls, each when it was made
	events  []stamp   // the same, each clock read, as "clock" at what it read, and each sleep, as "sleep" at its seconds
	sleeps  []float64 // each time.sleep the helper asked for, in seconds
	elapsed time.Duration
}

// stamp is one fake call and when it was made.
type stamp struct {
	at   float64
	call string
}

// span is the time from the first call starting with from to the first call
// after it starting with to.
func (h helperRun) span(t *testing.T, from, to string) time.Duration {
	t.Helper()
	for i, s := range h.stamps {
		if !strings.HasPrefix(s.call, from) {
			continue
		}
		for _, e := range h.stamps[i+1:] {
			if strings.HasPrefix(e.call, to) {
				return time.Duration((e.at - s.at) * float64(time.Second))
			}
		}
		break
	}
	t.Fatalf("no call %q followed by %q in %q", from, to, h.calls)
	return 0
}

// callsBetween counts the calls starting with prefix after the first call
// starting with from and before the first call after it starting with to.
func (h helperRun) callsBetween(t *testing.T, from, to, prefix string) int {
	t.Helper()
	for i, c := range h.calls {
		if !strings.HasPrefix(c, from) {
			continue
		}
		n := 0
		for _, e := range h.calls[i+1:] {
			if strings.HasPrefix(e, to) {
				return n
			}
			if strings.HasPrefix(e, prefix) {
				n++
			}
		}
		break
	}
	t.Fatalf("no call %q followed by %q in %q", from, to, h.calls)
	return 0
}

// wantWaited checks the one logged message matching re, whose first group is
// a wait in seconds as the helper measured it, against what the rig saw of
// that wait. The helper starts its clock after the call starting with from
// (a fake's stamp is taken while it runs, before the helper goes on), and
// stops it before the message's logger call. So the wait is no longer than
// the time from that call to the message's, and no shorter than the time
// from the call after from to the call before the message. Allowing for the
// rounding of what it printed, what it said must lie between the two,
// however loaded the host: a wait timed by a wrong clock, or read in the
// wrong units, does not.
func wantWaited(t *testing.T, h helperRun, from, re string) {
	t.Helper()
	rx := regexp.MustCompile(re)
	i := -1
	for k, s := range h.stamps {
		if strings.HasPrefix(s.call, from) {
			i = k
			break
		}
	}
	if i < 0 {
		t.Errorf("no call %q in %q", from, h.calls)
		return
	}
	for j := i + 1; j < len(h.stamps); j++ {
		msg, ok := strings.CutPrefix(h.stamps[j].call, logged)
		if !ok {
			continue
		}
		m := rx.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		said, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Errorf("%q: %q is not seconds", msg, m[1])
			return
		}
		slack := 0.5
		if _, frac, ok := strings.Cut(m[1], "."); ok {
			slack = 0.5 / math.Pow10(len(frac))
		}
		hi := h.stamps[j].at - h.stamps[i].at
		lo := 0.0
		if j-1 > i+1 {
			lo = h.stamps[j-1].at - h.stamps[i+1].at
		}
		t.Logf("%q: said %s s; the rig saw between %.3f and %.3f s", msg, m[1], lo, hi)
		if said < lo-slack || said > hi+slack {
			t.Errorf("%q: the helper said %s s for a wait the rig saw take between %.3f and %.3f s", msg, m[1], lo, hi)
		}
		return
	}
	t.Errorf("no message matching %q after the call %q; logged: %q", re, from, h.logged())
}

// helperDeadline guards against a hang (a fake, or a call that blocks) and
// nothing else: no claim here rests on it. A wait that should not happen
// ends the helper at its first sleep (noWait), and one without a bound at
// its sleep budget, both by what the helper asks for. It is far above the
// slowest correct run measured under load (see the shutdown-helper findings:
// a correct helper must never be what reaches it).
const helperDeadline = 60 * time.Second

// sleepBudget is how long, in all, a run's helper may ask to sleep: three
// times the test limit it is given and a second, or, without one, 5 s,
// more than any case here waits.
func sleepBudget(extraEnv []string) float64 {
	for _, e := range extraEnv {
		if v, ok := strings.CutPrefix(e, "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT="); ok {
			if x, err := strconv.ParseFloat(v, 64); err == nil && x > 0 && x <= 30 {
				return 3*x + 1
			}
		}
	}
	return 5
}

func (r *helperRig) run(extraEnv []string, args ...string) helperRun {
	r.t.Helper()
	return r.runWith(nil, extraEnv, args...)
}

// runWith runs the helper with stderr going to errFile when it is given
// (a broken pipe, say), or to a buffer otherwise.
func (r *helperRig) runWith(errFile *os.File, extraEnv []string, args ...string) helperRun {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), helperDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{r.script}, args...)...)
	cmd.Env = append(append([]string{}, r.env...), extraEnv...)
	cmd.Env = append(cmd.Env, "FAKE_SLEEP_BUDGET="+strconv.FormatFloat(sleepBudget(extraEnv), 'f', -1, 64))
	if r.noWait {
		cmd.Env = append(cmd.Env, "FAKE_NO_SLEEP=1")
	}
	// Its own process group, all of it killed at the deadline: killing only
	// the sh would leave its Python running, holding stderr open, and the
	// run would never return.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var errb strings.Builder
	if errFile != nil {
		cmd.Stderr = errFile
	} else {
		cmd.Stderr = &errb
	}
	start := time.Now()
	err := cmd.Run()
	run := helperRun{stderr: errb.String(), elapsed: time.Since(start)}
	r.t.Logf("the helper ran in %v", run.elapsed)
	if ctx.Err() == context.DeadlineExceeded {
		r.t.Fatalf("the helper did not finish within %v: it hung", helperDeadline)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		run.rc = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	if b, err := os.ReadFile(r.calls + ".overbudget"); err == nil {
		r.t.Fatalf("the helper asked to sleep %s s in all, past its %v s budget: a wait has no bound", strings.TrimSpace(string(b)), sleepBudget(extraEnv))
	}
	if b, err := os.ReadFile(r.calls); err == nil {
		run.calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	// Its Python ran if anything but id did: the sh stops before it (bad
	// arguments, not root) having run id at most, and the Python's first
	// look or message is a fake call. Whatever its exit status.
	ranPython := false
	for _, c := range run.calls {
		if c != "" && !strings.HasPrefix(c, "id ") {
			ranPython = true
		}
	}
	if _, err := os.Stat(r.calls + ".site"); ranPython && err != nil {
		r.t.Fatalf("the helper's Python never loaded the rig's sitecustomize, so no sleep it asked for was seen (status %d): %v", run.rc, err)
	}
	if b, err := os.ReadFile(r.calls + ".times"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if kind, v, ok := strings.Cut(l, " "); ok && (kind == "clock" || kind == "sleep") {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					r.t.Fatalf("calls.log.times: %q", l)
				}
				run.events = append(run.events, stamp{f, kind})
				continue
			}
			at, call, _ := strings.Cut(l, " ")
			f, err := strconv.ParseFloat(at, 64)
			if err != nil {
				r.t.Fatalf("calls.log.times: %q", l)
			}
			run.stamps = append(run.stamps, stamp{f, call})
			run.events = append(run.events, stamp{f, call})
		}
	}
	if b, err := os.ReadFile(r.calls + ".sleeps"); err == nil {
		for _, l := range strings.Fields(string(b)) {
			f, err := strconv.ParseFloat(l, 64)
			if err != nil {
				r.t.Fatalf("calls.log.sleeps: %q", l)
			}
			run.sleeps = append(run.sleeps, f)
		}
	}
	return run
}

// slept is how long, in all, the helper asked to sleep.
func (h helperRun) slept() float64 {
	n := 0.0
	for _, s := range h.sleeps {
		n += s
	}
	return n
}

// wantNoWait: the helper never slept, so it waited for nothing; with
// r.noWait set, its first sleep would have ended it then and there. And it
// looked as often as a run with no wait does, no more: the jobs twice (the
// first wait, the last look), the agents four times (before the unload, the
// unload's wait, after it, the dirty wait). A wait by anything but
// time.sleep, or one that loops without sleeping, looks more. Not a bound on
// how long the run took, which is the host's, not the helper's.
func wantNoWait(t *testing.T, h helperRun) {
	t.Helper()
	if len(h.sleeps) != 0 {
		t.Errorf("slept %v s in %d sleeps; want no wait", h.slept(), len(h.sleeps))
	}
	for _, q := range []struct {
		look string
		want int
	}{{jobsQuery, 2}, {agentsQuery, 4}} {
		n := 0
		for _, c := range h.calls {
			if strings.HasPrefix(c, q.look) {
				n++
			}
		}
		if n != q.want {
			t.Errorf("%d looks %q; a run with no wait makes %d", n, q.look, q.want)
		}
	}
}

// wantGaveUpAt: a wait bounded at limit s, looking every poll s, ended at
// its bound, neither before it nor later, by its own clock. The wait is the
// helper's clock reads and sleeps (the fake mach_absolute_time and the
// rig's sitecustomize record each, in order) after the call starting with
// from and before its message, the call starting with to. Its first read is
// its start. Each later read is a check, after a look. A check that goes on
// is followed by one sleep of poll, and reads under start + limit; every
// check but the last must go on. The last gives up: at a read of at least
// start + limit, or, should its looks run out first (it goes on, sleeps,
// looks once more and stops without reading), after limit / poll checks.
// These are the helper's own decisions, exact however loaded the host.
func wantGaveUpAt(t *testing.T, h helperRun, limit, poll float64, from, to string) {
	t.Helper()
	i, j := -1, -1
	for k, s := range h.events {
		if i < 0 && strings.HasPrefix(s.call, from) {
			i = k
		} else if i >= 0 && strings.HasPrefix(s.call, to) {
			j = k
			break
		}
	}
	if i < 0 || j < 0 {
		t.Errorf("no call %q followed by %q in %q", from, to, h.calls)
		return
	}
	// Each read, and the sleeps after it before the next read.
	type check struct {
		at     float64
		sleeps []float64
	}
	var reads []check
	for _, s := range h.events[i+1 : j] {
		switch {
		case s.call == "clock":
			reads = append(reads, check{at: s.at})
		case s.call == "sleep" && len(reads) > 0:
			reads[len(reads)-1].sleeps = append(reads[len(reads)-1].sleeps, s.at)
		case s.call == "sleep":
			t.Errorf("the wait slept %v s before it read its clock", s.at)
		}
	}
	if len(reads) < 2 {
		t.Errorf("the wait read its clock %d times; want a start and at least one check", len(reads))
		return
	}
	if len(reads[0].sleeps) != 0 {
		t.Errorf("the wait slept %v s before its first check", reads[0].sleeps)
	}
	start, checks := reads[0].at, reads[1:]
	for k, c := range checks {
		wentOn := len(c.sleeps) > 0
		if wentOn && (len(c.sleeps) != 1 || c.sleeps[0] != poll) {
			t.Errorf("check %d slept %v s; want one sleep of its %v s poll", k+1, c.sleeps, poll)
		}
		if wentOn && c.at-start >= limit {
			t.Errorf("check %d read %.3f s into the wait and went on; want it to give up at its %v s bound", k+1, c.at-start, limit)
		}
		if !wentOn && k < len(checks)-1 {
			t.Errorf("check %d, %.3f s into the wait, neither slept nor ended it", k+1, c.at-start)
		}
	}
	last, looks := checks[len(checks)-1], int(limit/poll)
	if len(last.sleeps) > 0 && len(checks) != looks {
		t.Errorf("the wait stopped without a check after %d of them; want that only after its %d looks", len(checks), looks)
	}
	if len(last.sleeps) == 0 && last.at-start < limit {
		t.Errorf("the wait gave up at a check %.3f s in; want it at its %v s bound", last.at-start, limit)
	}
}

// logged is what the helper said through logger, one message per call.
func (h helperRun) logged() []string {
	var out []string
	for _, c := range h.calls {
		if m, ok := strings.CutPrefix(c, "logger -t guest-macosx-shutdown "); ok {
			out = append(out, m)
		}
	}
	return out
}

// commands is every fake call but ps, id and logger, in order.
func (h helperRun) commands() []string {
	var out []string
	for _, c := range h.calls {
		if !strings.HasPrefix(c, "ps ") && !strings.HasPrefix(c, "id ") && !strings.HasPrefix(c, "logger ") {
			out = append(out, c)
		}
	}
	return out
}

func wantOneLog(t *testing.T, h helperRun, re string) {
	t.Helper()
	n := 0
	for _, l := range h.logged() {
		if regexp.MustCompile(re).MatchString(l) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("logged %d messages matching %q; want 1. logged: %q", n, re, h.logged())
	}
}

func wantOrder(t *testing.T, h helperRun, want ...string) {
	t.Helper()
	if got := h.commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

var (
	unloadSystem = "launchctl unload " + systemPlist
	killMC       = "killall ManagedClient"
	logged       = "logger -t guest-macosx-shutdown " // a message's call, as the fake logger records it
	jobsQuery    = "ps -axwwo pid=,ppid="             // a look at the jobs launchd started
	agentsQuery  = "ps -axwwo pid=,command="          // a look at the agents
)

func TestTheShutdownHelperRefusesBadArgumentsAndNonRoot(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	for _, tc := range []struct {
		env  []string
		args []string
	}{
		{nil, nil},
		{nil, []string{"-x"}},
		{[]string{"FAKE_UID=501"}, []string{"-r"}},
	} {
		h := r.run(tc.env, tc.args...)
		if h.rc != 2 {
			t.Errorf("%v %v: status %d; want 2", tc.env, tc.args, h.rc)
		}
		if c := h.commands(); len(c) != 0 {
			t.Errorf("%v %v: ran %q; want nothing", tc.env, tc.args, c)
		}
	}
}

// The rig sees a wait only as a time.sleep (wantNoWait, the sleep budget).
// So the helper waits by nothing else: its Python imports only what it
// does, sleeps only through time.sleep, and neither it nor its sh waits by
// any other call; its subprocesses block only on the rig's fakes.
func TestTheShutdownHelperWaitsOnlyByTimeSleep(t *testing.T) {
	h := shutdownHelper(t)
	sh, py, ok := strings.Cut(h, "<<'GUEST_MACOSX_PY_EOF'\n")
	if !ok {
		t.Fatal("the helper has no Python heredoc")
	}
	py, rest, ok := strings.Cut(py, "\nGUEST_MACOSX_PY_EOF\n")
	if !ok {
		t.Fatal("the helper's Python heredoc never ends")
	}
	code := func(text string) []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			if c := strings.TrimSpace(l); c != "" && !strings.HasPrefix(c, "#") {
				out = append(out, l)
			}
		}
		return out
	}
	var imports []string
	for _, l := range code(py) {
		if f := strings.Fields(l); f[0] == "import" || f[0] == "from" {
			imports = append(imports, strings.TrimSpace(l))
		}
	}
	if want := []string{"import ctypes, os, re, subprocess, sys, time"}; strings.Join(imports, "\n") != strings.Join(want, "\n") {
		t.Errorf("the helper's Python imports %q; want %q", imports, want)
	}
	waits := regexp.MustCompile(`\bselect\b|\bthreading\b|\bsignal\b|\.wait\(|\bpause\(|\bEvent\(|\bsleep\(`)
	for _, l := range code(py) {
		for _, m := range waits.FindAllString(strings.ReplaceAll(l, "time.sleep(", ""), -1) {
			t.Errorf("the helper's Python waits by %q, which the rig does not see: %q", m, strings.TrimSpace(l))
		}
	}
	if n := strings.Count(py, "time.sleep("); n != 3 {
		t.Errorf("the helper's Python has %d time.sleep calls; want 3, one a wait", n)
	}
	shWaits := regexp.MustCompile(`\b(sleep|wait|read)\b`)
	for _, l := range code(sh + "\n" + rest) {
		if m := shWaits.FindString(l); m != "" {
			t.Errorf("the helper's sh waits by %q, which the rig does not see: %q", m, strings.TrimSpace(l))
		}
	}
}

// The System agent with a write pending: unloaded (so it saves through
// cfprefsd and exits), then ManagedClient, then the shutdown, each once, and
// the log says a write was pending. A wrong AGENT prefix finds no agent, and
// a dirty() that never says yes misses the write: both fail here.
func TestTheShutdownHelperUnloadsADirtySystemAgentThenShutsDown(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(11, 0)
	h := r.run(nil, "-r")
	if h.rc != 0 {
		t.Fatalf("status %d; stderr %q", h.rc, h.stderr)
	}
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9.]+s, with a write pending$`)
	if !strings.Contains(h.stderr, "guest-macosx-shutdown: UserEventAgent (System) stopped in") {
		t.Errorf("the message never reached stderr, so the caller never sees it: %q", h.stderr)
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "ps ") && !strings.HasPrefix(c, "ps -axwwo pid=,") {
			t.Errorf("ps ran as %q; want -axwwo, or a narrow terminal truncates the agent's name", c)
		}
	}
}

func TestTheShutdownHelperSaysWhenNothingWasPending(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	h := r.run(nil, "-h")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9.]+s$`)
}

// Not running: the unload still runs (the job may be loaded and idle) and
// the log says the agent was not running, not that it was stopped. A failed
// launchctl is logged with its status.
func TestTheShutdownHelperSaysWhenTheSystemAgentWasNotRunning(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("  130 " + lwAgent)
	h := r.run([]string{"FAKE_UNLOAD=notloaded"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^UserEventAgent \(System\) was not running$`)
	wantOneLog(t, h, `^launchctl unload `+regexp.QuoteMeta(systemPlist)+` exited 1$`)
	for _, l := range h.logged() {
		if strings.Contains(l, "stopped in") {
			t.Errorf("logged %q for an agent that was not running", l)
		}
	}
}

// An unload that fails and leaves the agent running: the helper gives up at
// its bound, says so, and shuts down anyway.
func TestTheShutdownHelperGivesUpOnAnAgentThatWillNotGo(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	h := r.run([]string{"FAKE_UNLOAD=fail", "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^launchctl unload .* exited 1$`)
	wantOneLog(t, h, `^UserEventAgent \(System\) still running 1s after its unload; shutting down anyway$`)
	wantGaveUpAt(t, h, 1, 0.05, logged+"could not tell which jobs", logged+"UserEventAgent (System) still running")
}

func TestTheShutdownHelperWaitsOutADirtyLoginWindowAgent(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(130, 700*time.Millisecond)
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^waited [0-9.]+s for UserEventAgent \(LoginWindow\) to settle$`)
	// The positive control for wantNoWait: a wait the helper really makes
	// is seen, as the dirty wait's own 0.1 s sleeps, and nothing else.
	if len(h.sleeps) == 0 {
		t.Error("the rig saw no sleep in a wait the helper made; it would see none anywhere")
	}
	for _, d := range h.sleeps {
		if d != 0.1 {
			t.Errorf("slept %v s; want only the dirty wait's 0.1 s sleeps: %v", d, h.sleeps)
		}
	}
}

func TestTheShutdownHelperGivesUpOnALoginWindowAgentThatNeverSettles(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(130, 0)
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-h")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^UserEventAgent \(LoginWindow\) still dirty after 1s; shutting down anyway$`)
	wantGaveUpAt(t, h, 1, 0.1, logged+"UserEventAgent (System) stopped", logged+"UserEventAgent (LoginWindow) still dirty")
}

// An OS without proc_get_dirty: the helper says it cannot tell what is
// pending, and still unloads the agent and shuts down. The unload is the fix,
// and it does not need proc_get_dirty.
func TestTheShutdownHelperStillUnloadsWithoutProcGetDirty(t *testing.T) {
	r := newHelperRig(t, false)
	r.agents("   11 " + systemAgent)
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^no proc_get_dirty in libSystem: cannot tell what UserEventAgent or AppleIDAuthAgent has pending$`)
	wantOneLog(t, h, `^no mach_absolute_time in libSystem: timing the waits by the wall clock$`)
	wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9.]+s$`)
}

// A caller that goes away (a timeout, an interrupted build, a dropped SSH
// session) leaves the helper's stderr a broken pipe. That must change
// nothing: the waits still happen, and system.log still gets every line.
func TestTheShutdownHelperDoesItsJobWithABrokenStderr(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(11, 0)
	r.dirtyFor(130, 700*time.Millisecond)
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rd.Close() // nobody reads: every write to wr is EPIPE
	h := r.runWith(wr, nil, "-r")
	wr.Close()
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9.]+s, with a write pending$`)
	wantOneLog(t, h, `^waited [0-9.]+s for UserEventAgent \(LoginWindow\) to settle$`)
}

// The test seam reaches a guest through `sudo -E`, `sudo VAR=...`, Vagrant's
// own `sudo -E -H`, or root without sudo. Only 0 < x <= 30 is taken; any
// other value is said to be ignored and the bound stays 30 s, with the
// unload and the waits as ever.
func TestTheShutdownHelperIgnoresABadTestLimit(t *testing.T) {
	for _, v := range []string{"", "abc", "nan", "inf", "-inf", "0", "-1", "31"} {
		t.Run("limit="+v, func(t *testing.T) {
			r := newHelperRig(t, true)
			r.agents("   11 "+systemAgent, "  130 "+lwAgent)
			r.dirtyFor(130, 700*time.Millisecond)
			h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=" + v}, "-r")
			wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
			wantOneLog(t, h, `^GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=.* ignored: waiting at most 30s$`)
			wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9.]+s$`)
			wantOneLog(t, h, `^waited [0-9.]+s for UserEventAgent \(LoginWindow\) to settle$`)
		})
	}
}

// A good value is taken, and said once in the log, so a shortened bound is
// never silent.
func TestTheShutdownHelperTakesAGoodTestLimit(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(130, 0)
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=0.5"}, "-r")
	wantOneLog(t, h, `^GUEST_MACOSX_SHUTDOWN_TEST_LIMIT='0.5' in effect: waiting at most 0.5s$`)
	wantOneLog(t, h, `^UserEventAgent \(LoginWindow\) still dirty after 0.5s; shutting down anyway$`)
	for _, l := range h.logged() {
		if strings.Contains(l, "ignored") {
			t.Errorf("logged %q for a good value", l)
		}
	}
}

// A bound too small for even one pass of a wait still checks its condition
// once, and says what it found: a dirty LoginWindow agent is never skipped
// in silence.
func TestTheShutdownHelperChecksOnceUnderATinyTestLimit(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(130, 0)
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1e-9"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^GUEST_MACOSX_SHUTDOWN_TEST_LIMIT='1e-9' in effect: waiting at most 1e-09s$`)
	wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9.]+s$`)
	wantOneLog(t, h, `^UserEventAgent \(LoginWindow\) still dirty after 1e-09s; shutting down anyway$`)
}

// The jobs a warm boot starts last are still starting 5-8 s in, and a
// shutdown then gives each a SIGTERM it is not ready for (ADR 0021). The
// helper waits, before anything else, until no job launchd started is under
// 20 s old, and says what it waited for. A job long running, even by hours
// or days, is not waited for.
func TestTheShutdownHelperWaitsForJobsStillStarting(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 2*24*time.Hour + time.Second, "/usr/libexec/kextd"},
		job{13, 1, time.Hour + 5*time.Second, "/usr/sbin/notifyd"},
		job{70, 1, 19300 * time.Millisecond, "/System/Library/CoreServices/SleepServicesD"},
		job{11, 1, 300 * time.Second, systemAgent})
	h := r.run(nil, "-r")
	if h.rc != 0 {
		t.Fatalf("status %d; stderr %q", h.rc, h.stderr)
	}
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^waited [0-9.]+s for jobs still starting: SleepServicesD$`)
	// the wait comes first: no unload while a job is still starting
	jobsAt, unloadAt := -1, -1
	for i, c := range h.calls {
		if jobsAt < 0 && strings.HasPrefix(c, "ps -axwwo pid=,ppid=,pgid=,uid=,etime=,stat=,command=") {
			jobsAt = i
		}
		if unloadAt < 0 && c == unloadSystem {
			unloadAt = i
		}
	}
	if jobsAt < 0 || unloadAt < 0 || jobsAt > unloadAt {
		t.Errorf("the jobs were checked at call %d and UserEventAgent unloaded at %d; want the check first. calls: %q", jobsAt, unloadAt, h.calls)
	}
}

// A job that keeps starting (one launchd respawns, say) holds the helper
// only to its bound; it says so, and shuts down anyway.
func TestTheShutdownHelperGivesUpOnJobsThatKeepStarting(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{77, 1, justStarting, "/usr/sbin/netbiosd"})
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-h")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^jobs still starting after 1s: netbiosd; shutting down anyway$`)
	// the last look does not report again what the wait gave up on
	wantOneLog(t, h, `^no new job started during the waits$`)
	wantGaveUpAt(t, h, 1, 0.5, logged+"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=", logged+"jobs still starting after")
}

// Not jobs that are starting: an SSH session's launchproxy (this one's own,
// too), the per-user launchd an SSH login starts, and anything whose parent
// is not launchd. None of them is waited for; the log says nothing was.
func TestTheShutdownHelperDoesNotWaitForSSHSessions(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{400, 1, justStarting, "/usr/libexec/launchproxy /usr/sbin/sshd -i"},
		job{405, 1, justStarting, "/sbin/launchd"},
		job{410, 400, justStarting, "sshd-session: vagrant@notty"},
		job{412, 410, justStarting, "sudo /usr/local/sbin/guest-macosx-shutdown -r"})
	r.noWait = true
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	for _, l := range h.logged() {
		if strings.Contains(l, "still starting") || strings.Contains(l, "waited") {
			t.Errorf("logged %q; want no wait for SSH sessions", l)
		}
	}
	wantNoWait(t, h)
}

// ps prints a per-user launchd whose arguments it cannot read (one
// exiting, say) as "(launchd)": still a per-user launchd, not a job
// starting (seen on a base-box warm boot, 2026-10-15).
func TestTheShutdownHelperDoesNotWaitForAPerUserLaunchdInParentheses(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{405, 1, justStarting, "live:(launchd)"},
		job{406, 1, justStarting, "live:(launchproxy)"})
	r.noWait = true
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	for _, l := range h.logged() {
		if strings.Contains(l, "still starting") || strings.Contains(l, "waited") {
			t.Errorf("logged %q; want no wait for a per-user launchd", l)
		}
	}
	wantNoWait(t, h)
}

// A settled guest, realistically: launchd's long-running jobs, aged in
// minutes, hours (01:00:05, which reads as 5 s if the hours are dropped) and
// days. The helper waits 0 s, and the log says it found nothing to wait for,
// and how many jobs it looked at, so it is never silent about it (I1). Once
// more, just before the shutdown, it says no new job started.
func TestTheShutdownHelperSaysWhenNoJobIsStarting(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 5 * time.Minute, "/usr/libexec/kextd"},
		job{13, 1, time.Hour + 5*time.Second, "/usr/sbin/notifyd"},
		job{14, 1, 2*24*time.Hour + 3*time.Hour, "/usr/sbin/cfprefsd daemon"},
		job{15, 1, 25 * time.Second, "/usr/sbin/netbiosd"},
		job{16, 1, justStarting, "(newsyslog)"}, // a zombie is not starting
		job{405, 1, justStarting, "/sbin/launchd"})
	r.noWait = true
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^no job launchd started is under 20s old \(4 checked\)$`)
	wantOneLog(t, h, `^no new job started during the waits$`)
	for _, l := range h.logged() {
		if strings.Contains(l, "waited") || strings.Contains(l, "still starting") {
			t.Errorf("logged %q for a settled guest", l)
		}
	}
	wantNoWait(t, h)
}

// A ps that fails (or lists no job launchd started) is not a settled guest.
// The helper says so in its own words, does not wait, and still shuts down.
func TestTheShutdownHelperSaysWhenPsFails(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, justStarting, "/usr/libexec/kextd"})
	r.noWait = true
	h := r.run([]string{"FAKE_PS_FAIL=1"}, "-r")
	if h.rc != 0 {
		t.Fatalf("status %d; stderr %q", h.rc, h.stderr)
	}
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^could not tell which jobs are still starting \(ps exited 1\); not waiting for any$`)
	for _, l := range h.logged() {
		if strings.Contains(l, "no job launchd started") {
			t.Errorf("logged %q for a ps that failed", l)
		}
	}
	wantNoWait(t, h)
}

func TestTheShutdownHelperSaysWhenPsListsNoJobs(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	h := r.run(nil, "-h") // no jobs.txt: ps prints nothing
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^could not tell which jobs are still starting \(ps listed no job launchd started\); not waiting for any$`)
}

// An age ps prints that the helper cannot read (a clock step makes etimes
// negative) is not waited for, and the log says how many it could not read.
func TestTheShutdownHelperSaysWhenItCannotReadAnAge(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 5 * time.Minute, "/usr/libexec/kextd"},
		job{13, 1, badAge, "/usr/sbin/notifyd"},
		job{14, 1, badAge, "/usr/sbin/warmd"})
	r.noWait = true
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^could not read the age of 2 process\(es\) launchd started; not waited for$`)
	wantOneLog(t, h, `^no job launchd started is under 20s old \(1 checked\)$`)
	wantNoWait(t, h)
}

// A job launchd keeps respawning, here with a young copy, under a new pid,
// at every look (one whose copies outlive launchd's 10 s throttle; one that
// dies on start is usually gone between copies, and the wait ends then).
// The helper does not try to tell it from jobs still starting: it waits to
// its bound, names it, and shuts down anyway. The last look's copy is one
// the wait never saw, so it is reported too: it did start during the waits.
// A 4 s bound, eight looks: with 1 s and two, one slow look carried a
// helper that gave up at half its bound past the whole of it.
func TestTheShutdownHelperWaitsOutAJobThatKeepsRespawning(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 5 * time.Minute, "/usr/libexec/kextd"},
		job{500, 1, respawning, "/usr/sbin/crashd"})
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=4"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^jobs still starting after 4s: crashd; shutting down anyway$`)
	wantOneLog(t, h, `^jobs started during the waits: crashd; shutting down anyway$`)
	for _, l := range h.logged() {
		if strings.Contains(l, "respawning") {
			t.Errorf("logged %q; want no respawn detection", l)
		}
	}
	wantGaveUpAt(t, h, 4, 0.5, logged+"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=", logged+"jobs still starting after")
}

// The helper's own parents are not jobs to wait for. A backgrounded call
// (`nohup sudo guest-macosx-shutdown -r &`) is reparented to launchd when its shell
// exits, and is as young as the call.
func TestTheShutdownHelperDoesNotWaitForItsOwnAncestors(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 5 * time.Minute, "/usr/libexec/kextd"},
		job{600, 1, justStarting, "sudo /usr/local/sbin/guest-macosx-shutdown -r"},
		job{601, 600, justStarting, "/bin/sh /usr/local/sbin/guest-macosx-shutdown -r"},
		job{0, 601, justStarting, "/usr/bin/python -"})
	r.noWait = true
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^no job launchd started is under 20s old \(1 checked\)$`)
	wantNoWait(t, h)
}

// The log names at most eight jobs and counts the rest.
func TestTheShutdownHelperNamesAtMostEightJobs(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	var js []job
	for i := 0; i < 10; i++ {
		js = append(js, job{100 + i, 1, justStarting, "/usr/sbin/job" + strconv.Itoa(i)})
	}
	r.jobs(js...)
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-h")
	wantOneLog(t, h, `^jobs still starting after 1s: job0, job1, job2, job3, job4, job5, job6, job7 and 2 more; shutting down anyway$`)
}

// A wait for many jobs names at most eight, in order, so the one that held
// it longest can be past the eighth: it says which were the last to settle,
// the jobs still young at its last look before none was (F14: the gate's
// 20 s waits read "... and 62 more", which hid what held them).
func TestTheShutdownHelperNamesTheLastJobsToSettle(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	var js []job
	for i := 0; i < 10; i++ {
		js = append(js, job{100 + i, 1, 19700 * time.Millisecond, "/usr/sbin/job" + strconv.Itoa(i)})
	}
	js = append(js, job{150, 1, 18800 * time.Millisecond, "/usr/libexec/sandboxd -n PluginProcess -n"})
	r.jobs(js...)
	h := r.run(nil, "-r")
	if h.rc != 0 {
		t.Fatalf("status %d; stderr %q", h.rc, h.stderr)
	}
	wantOneLog(t, h, `^waited [0-9.]+s for jobs still starting: job0, job1, job2, job3, job4, job5, job6, job7 and 3 more; the last to settle: sandboxd$`)
}

// When every job it waited for settled at the same look, the list already
// says it all: no "last to settle".
func TestTheShutdownHelperNamesNoLastJobWhenAllSettleTogether(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{70, 1, 19300 * time.Millisecond, "/System/Library/CoreServices/SleepServicesD"},
		job{71, 1, 19300 * time.Millisecond, "/usr/libexec/warmd"})
	h := r.run(nil, "-r")
	wantOneLog(t, h, `^waited [0-9.]+s for jobs still starting: SleepServicesD, warmd$`)
}

// ps prints a live process whose arguments it cannot read (one exiting,
// say) as "(name)": the same job as "name", named and counted once.
func TestTheShutdownHelperNamesAJobPsPrintsInParenthesesOnce(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	js := []job{{99, 1, justStarting, "live:(fontworker)"}}
	for i := 0; i < 9; i++ {
		js = append(js, job{100 + i, 1, justStarting, "/usr/sbin/job" + strconv.Itoa(i)})
	}
	js = append(js, job{120, 1, justStarting, "/System/Library/Frameworks/fontworker"})
	r.jobs(js...)
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-h")
	wantOneLog(t, h, `^jobs still starting after 1s: fontworker, job0, job1, job2, job3, job4, job5, job6 and 2 more; shutting down anyway$`)
}

// A job that starts during the helper's own waits (the unload, the dirty
// wait) meets the shutdown young. The helper looks once more just before
// it, and says what it found; it does not wait again.
func TestTheShutdownHelperSaysWhenAJobStartedDuringItsWaits(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 5 * time.Minute, "/usr/libexec/kextd"})
	r.noWait = true
	h := r.run([]string{"FAKE_UNLOAD_STARTS_JOB=1"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^no job launchd started is under 20s old \(1 checked\)$`)
	wantOneLog(t, h, `^jobs started during the waits: latecomerd; shutting down anyway$`)
	wantNoWait(t, h)
}

// The first wait is bounded by how many times it looked as well as by the
// clock: a backstop, should the clock not move. Here neither clock moves.
func TestTheShutdownHelperBoundsItsJobsWaitWithAFrozenClock(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{77, 1, justStarting, "/usr/sbin/netbiosd"})
	h := r.run(append(frozenClock(), "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"), "-h")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^jobs still starting after 1s: netbiosd; shutting down anyway$`)
}

// A clean AppleIDAuthAgent, running, is not waited for and not mentioned.
func TestTheShutdownHelperDoesNotWaitForACleanAppleIDAuthAgent(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  140 "+appleIDAgent)
	r.noWait = true
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	for _, l := range h.logged() {
		if strings.Contains(l, "AppleIDAuthAgent") || strings.Contains(l, "settle") {
			t.Errorf("logged %q for a clean agent", l)
		}
	}
	wantNoWait(t, h)
}

// AppleIDAuthAgent's account check, 10-15 s after it starts, can leave a
// preference write pending for 3-4 s, and a shutdown then stalls on it as on
// UserEventAgent's (ADR 0021). The helper waits it out with the
// UserEventAgents, and names it.
func TestTheShutdownHelperWaitsOutADirtyAppleIDAuthAgent(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent, "  140 "+appleIDAgent)
	r.dirtyFor(140, 700*time.Millisecond)
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^waited [0-9.]+s for AppleIDAuthAgent to settle$`)
}

// Both at once: each is named once, in one line.
func TestTheShutdownHelperNamesEveryAgentItWaitedFor(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent, "  140 "+appleIDAgent)
	r.dirtyFor(130, 400*time.Millisecond)
	r.dirtyFor(140, 700*time.Millisecond)
	h := r.run(nil, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^waited [0-9.]+s for AppleIDAuthAgent, UserEventAgent \(LoginWindow\) to settle$`)
}

func TestTheShutdownHelperGivesUpOnAnAppleIDAuthAgentThatNeverSettles(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  140 "+appleIDAgent)
	r.dirtyFor(140, 0)
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-h")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^AppleIDAuthAgent still dirty after 1s; shutting down anyway$`)
}

// root's xpcd and _softwareupdate's (uid 200) are two jobs with one name, a
// few seconds apart on every warm boot, and the second is one of the
// measured stallers. The wait is for every young process, whatever its
// name: a second one of a name, starting while the first settles, is waited
// for to the bound. The first is 18 s old at the helper's first look, so
// it settles 2 s in, before the 4 s bound: only the second holds the wait
// to it.
func TestTheShutdownHelperWaitsForTwoLiveJobsOfOneName(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.rawJobs("25 1 18 /usr/libexec/xpcd",
		"144 1 appear:1.5 uid=200:/usr/libexec/xpcd")
	h := r.run([]string{"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=4"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^jobs still starting after 4s: xpcd; shutting down anyway$`)
	wantGaveUpAt(t, h, 4, 0.5, logged+"GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=", logged+"jobs still starting after")
}

// ps failing after the helper has waited says how long it waited, and for
// what, as well as that it can no longer tell.
func TestTheShutdownHelperSaysWhatItWaitedForWhenPsFailsLater(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.rawJobs("700 1 until:5 /usr/sbin/slowd")
	h := r.run([]string{"FAKE_PS_FAIL_AFTER=0.8"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^waited [0-9.]+s for jobs still starting: slowd$`)
	wantOneLog(t, h, `^could not tell which jobs are still starting \(ps exited 1\); not waiting for any$`)
	for _, l := range h.logged() {
		if strings.Contains(l, "still starting after") {
			t.Errorf("logged %q; the wait did not give up", l)
		}
	}
}

// ps failing mid-wait leaves no last look before none was young: the jobs
// it saw last never settled, so the helper names no "last to settle".
func TestTheShutdownHelperNamesNoLastJobWhenPsFailsLater(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.rawJobs("700 1 until:5 /usr/sbin/slowd", "701 1 19.7 /usr/sbin/fastd")
	h := r.run([]string{"FAKE_PS_FAIL_AFTER=1.2"}, "-r")
	if h.rc != 0 {
		t.Fatalf("status %d; stderr %q", h.rc, h.stderr)
	}
	wantOneLog(t, h, `^waited [0-9.]+s for jobs still starting: fastd, slowd$`)
	wantOneLog(t, h, `^could not tell which jobs are still starting \(ps exited 1\); not waiting for any$`)
}

// The last look's ps failing is said, too.
func TestTheShutdownHelperSaysWhenItsLastLookCannotTell(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.jobs(job{12, 1, 5 * time.Minute, "/usr/libexec/kextd"})
	h := r.run([]string{"FAKE_UNLOAD_BREAKS_PS=1"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^no job launchd started is under 20s old \(1 checked\)$`)
	wantOneLog(t, h, `^could not tell whether a job started during the waits \(ps exited 1\)$`)
}

// A /bin/ps that cannot be run at all is said, and does not stop the
// shutdown.
func TestTheShutdownHelperSaysWhenPsCannotBeRun(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	b, err := os.ReadFile(r.script)
	if err != nil {
		t.Fatal(err)
	}
	h := string(b)
	from := "'" + filepath.Join(r.fake, "ps") + "', '-axwwo', 'pid=,ppid="
	if !strings.Contains(h, from) {
		t.Fatal("the jobs query's ps call moved")
	}
	h = strings.Replace(h, from, "'"+filepath.Join(r.fake, "no-such-ps")+"', '-axwwo', 'pid=,ppid=", 1)
	if err := os.WriteFile(r.script, []byte(h), 0o644); err != nil {
		t.Fatal(err)
	}
	run := r.run(nil, "-r")
	wantOrder(t, run, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, run, `^could not tell which jobs are still starting \(ps: .*no-such-ps.*\); not waiting for any$`)
}

// The rest of a backgrounded pipeline (`sudo guest-macosx-shutdown -r | tee log &`)
// is adopted by launchd too, in the helper's process group, and lives as
// long as the helper does: not waited for.
func TestTheShutdownHelperDoesNotWaitForItsProcessGroup(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.rawJobs("12 1 now pg:/usr/bin/tee -a /tmp/log", "13 1 300 /usr/libexec/kextd")
	r.noWait = true
	h := r.run(nil, "-r")
	wantOneLog(t, h, `^no job launchd started is under 20s old \(1 checked\)$`)
	wantNoWait(t, h)
}

// A clock stepped back leaves etimes like "-5:00" (not 300 s old) and, for
// a process under a minute old, "00:-30" (not -30 s old, which is young).
// Neither is an age.
func TestTheShutdownHelperReadsANegativeAgeAsUnreadable(t *testing.T) {
	for _, kind := range []string{"minus5", "minus30s"} {
		t.Run(kind, func(t *testing.T) {
			r := newHelperRig(t, true)
			r.agents("   11 " + systemAgent)
			r.rawJobs("12 1 "+kind+" /usr/sbin/notifyd", "13 1 300 /usr/libexec/kextd")
			r.noWait = true
			h := r.run(nil, "-r")
			wantOneLog(t, h, `^could not read the age of 1 process\(es\) launchd started; not waited for$`)
			wantOneLog(t, h, `^no job launchd started is under 20s old \(1 checked\)$`)
			wantNoWait(t, h)
		})
	}
}

// pyClock replaces the helper's time.time (not its fakes': they run with
// -I) with body, a Python expression in _t, the time at its start, and
// _n(), a count of calls so far (the rig's sitecustomize reads it).
func pyClock(body string) string {
	return "FAKE_PY_CLOCK=" + body
}

// frozenClock stops both clocks the helper could read: time.time, and
// libSystem's mach_absolute_time.
func frozenClock() []string {
	return []string{pyClock("_t"), "FAKE_MACH_FROZEN=1"}
}

// The other two waits are bounded by looks as well as by the clock.
func TestTheShutdownHelperBoundsItsOtherWaitsWithAFrozenClock(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.dirtyFor(130, 0)
	h := r.run(append(frozenClock(), "FAKE_UNLOAD=fail", "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"), "-h")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
	wantOneLog(t, h, `^UserEventAgent \(System\) still running 1s after its unload; shutting down anyway$`)
	wantOneLog(t, h, `^UserEventAgent \(LoginWindow\) still dirty after 1s; shutting down anyway$`)
}

// The last look compares processes, not names. A fresh copy of a job the
// first wait gave up on, started during the unload, is a job that started
// during the waits: said, not hidden behind the name it shares.
func TestTheShutdownHelperReportsAFreshCopyOfAJobItGaveUpOn(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.rawJobs("77 1 now /usr/sbin/latecomerd")
	h := r.run([]string{"FAKE_UNLOAD_STARTS_JOB=1", "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^jobs still starting after 1s: latecomerd; shutting down anyway$`)
	wantOneLog(t, h, `^jobs started during the waits: latecomerd; shutting down anyway$`)
}

// A process is its pid. On 10.9 a job's uid changes under its pid in its
// first second (_softwareupdate's xpcd is root's until its setuid,
// MEASURED), so a pid the first wait gave up on, seen again with another
// uid, did not start during the waits: not reported. (Darwin hands pids
// out in sequence up to 99999: one is not reused within the helper's run.)
func TestTheShutdownHelperDoesNotReportAPidWhoseUidChanged(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 " + systemAgent)
	r.rawJobs("166 1 now /usr/libexec/xpcd")
	h := r.run([]string{"FAKE_UNLOAD_JOBS=166 1 now uid=200:/usr/libexec/xpcd", "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^jobs still starting after 1s: xpcd; shutting down anyway$`)
	wantOneLog(t, h, `^no new job started during the waits$`)
}

// When the first wait could not tell what was starting, the last look cannot
// say what started since. It says what is still starting at the shutdown.
func TestTheShutdownHelperSaysWhatIsStartingAtTheShutdownWhenItCouldNotTellBefore(t *testing.T) {
	for _, tc := range []struct {
		name, job, want string
	}{
		{"young", "12 1 now /usr/sbin/workd", `^jobs still starting at the shutdown: workd; shutting down anyway$`},
		{"settled", "12 1 300 /usr/sbin/workd", `^no job launchd started is under 20s old at the shutdown$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHelperRig(t, true)
			r.agents("   11 " + systemAgent)
			r.rawJobs(tc.job)
			if err := os.WriteFile(r.jobsFile+".broken", nil, 0o644); err != nil {
				t.Fatal(err)
			}
			h := r.run([]string{"FAKE_UNLOAD_FIXES_PS=1"}, "-r")
			wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
			wantOneLog(t, h, `^could not tell which jobs are still starting \(ps exited 1\); not waiting for any$`)
			wantOneLog(t, h, tc.want)
			for _, l := range h.logged() {
				if strings.Contains(l, "during the waits") {
					t.Errorf("logged %q; the first wait never saw what was starting", l)
				}
			}
		})
	}
}

// The waits are timed by mach_absolute_time, a clock that does not step, so
// a wall clock that steps back (ntpd, a host resume) neither stretches a
// wait nor garbles what it says: each says how long it took. Here time.time
// goes 10 s back on every call.
func TestTheShutdownHelperTimesItsWaitsByAClockThatDoesNotStep(t *testing.T) {
	r := newHelperRig(t, true)
	r.agents("   11 "+systemAgent, "  130 "+lwAgent)
	r.rawJobs("700 1 until:0.3 /usr/sbin/slowd", "13 1 300 /usr/libexec/kextd")
	r.dirtyFor(130, 700*time.Millisecond)
	h := r.run([]string{pyClock("_t - 10 * _n()")}, "-r")
	wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
	wantOneLog(t, h, `^waited [0-9.]+s for jobs still starting: slowd$`)
	wantWaited(t, h, "id ", `^waited (-?[0-9.]+)s for jobs still starting: slowd$`)
	wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9]+\.[0-9]{2}s$`)
	wantWaited(t, h, logged+"waited", `^UserEventAgent \(System\) stopped in (-?[0-9.]+)s$`)
	wantOneLog(t, h, `^waited [0-9.]+s for UserEventAgent \(LoginWindow\) to settle$`)
	wantWaited(t, h, logged+"UserEventAgent (System) stopped", `^waited (-?[0-9.]+)s for UserEventAgent \(LoginWindow\) to settle$`)
}

// Each wait's bound is on the clock, so looks that are slow (each ps here
// takes longer) do not stretch it by its number of looks: each ends once
// its 1 s is up, not after its number of looks, each with its interval and a
// look's work (4 s for the first, 5 s for the unload, 3.3 s for the dirty
// wait). The wall clock is frozen: only mach_absolute_time ends them. Each
// waits out its 1 s, and looks no more often than its looks' own floor
// allows: a look takes at least its delay, so within 1 s the first wait
// looks once (1 s a look), the unload's at most 4 times (0.2 s a look and a
// 0.05 s sleep, after the look at the agent before the unload) and once
// more after it, and the dirty wait at most 4 times (0.2 s and 0.1 s). A
// wait bounded only by its number of looks makes 3, 23 and 11. Counted, not
// timed: a loaded host makes looks slower, so fewer, never more.
func TestTheShutdownHelperBoundsEachWaitOfSlowLooksByTheClock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		jobs     bool
		dirtyLW  bool
		env      []string
		from, to string
		look     string
		most     int
	}{
		{"jobs", true, false, []string{"FAKE_PS_DELAY_JOBS=1"},
			logged + "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=", logged + "jobs still starting after 1s", jobsQuery, 1},
		{"unload", false, false, []string{"FAKE_PS_DELAY_AGENTS=0.2", "FAKE_UNLOAD=fail"},
			logged + "could not tell which jobs", logged + "UserEventAgent (System) still running 1s", agentsQuery, 6},
		{"dirty", false, true, []string{"FAKE_PS_DELAY_AGENTS=0.2"},
			logged + "UserEventAgent (System) stopped in", logged + "UserEventAgent (LoginWindow) still dirty after 1s", agentsQuery, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHelperRig(t, true)
			r.agents("   11 "+systemAgent, "  130 "+lwAgent)
			if tc.jobs {
				r.rawJobs("77 1 now /usr/sbin/netbiosd")
			}
			if tc.dirtyLW {
				r.dirtyFor(130, 0)
			}
			h := r.run(append([]string{pyClock("_t"), "GUEST_MACOSX_SHUTDOWN_TEST_LIMIT=1"}, tc.env...), "-h")
			wantOrder(t, h, unloadSystem, killMC, "shutdown -h now")
			d := h.span(t, tc.from, tc.to)
			n := h.callsBetween(t, tc.from, tc.to, tc.look)
			t.Logf("the %s wait took %v, in %d looks", tc.name, d, n)
			if d < time.Second {
				t.Errorf("the %s wait took %v; want it to wait out its 1 s bound", tc.name, d)
			}
			if n < 1 || n > tc.most {
				t.Errorf("the %s wait looked %d times; want 1 to %d, as its 1 s bound allows", tc.name, n, tc.most)
			}
		})
	}
}

// mach_absolute_time counts in ticks of mach_timebase_info, not always in
// ns, and in 64 bits. A tick of 125/3 ns, or of 1 ps (a 32-bit count wraps
// every 4 ms, so one read as a C int is wrong on every wait): each wait
// reads as long as it took.
func TestTheShutdownHelperReadsTheMachTimebase(t *testing.T) {
	for _, tb := range []string{"125/3", "1/1000"} {
		t.Run(strings.ReplaceAll(tb, "/", "over"), func(t *testing.T) {
			r := newHelperRig(t, true)
			r.agents("   11 "+systemAgent, "  130 "+lwAgent)
			r.dirtyFor(130, 700*time.Millisecond)
			h := r.run([]string{"FAKE_TIMEBASE=" + tb}, "-r")
			wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
			wantOneLog(t, h, `^UserEventAgent \(System\) stopped in [0-9]+\.[0-9]{2}s$`)
			wantWaited(t, h, logged+"could not tell which jobs", `^UserEventAgent \(System\) stopped in (-?[0-9.]+)s$`)
			wantOneLog(t, h, `^waited [0-9.]+s for UserEventAgent \(LoginWindow\) to settle$`)
			wantWaited(t, h, logged+"UserEventAgent (System) stopped", `^waited (-?[0-9.]+)s for UserEventAgent \(LoginWindow\) to settle$`)
		})
	}
}

// When the mach clock cannot be read, the log says which call failed, and
// the waits are timed by the wall clock.
func TestTheShutdownHelperSaysWhyItCannotReadTheMachClock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		defines []string
		env     []string
		want    string
	}{
		{"no-timebase-symbol", []string{"FAKE_NO_TIMEBASE"}, nil, `no mach_timebase_info in libSystem`},
		{"timebase-fails", nil, []string{"FAKE_TIMEBASE_RC=5"}, `mach_timebase_info returned 5, denominator 1`},
		{"zero-denominator", nil, []string{"FAKE_TIMEBASE=1/0"}, `mach_timebase_info returned 0, denominator 0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHelperRig(t, true)
			if tc.defines != nil {
				r.relink(tc.defines...)
			}
			r.agents("   11 "+systemAgent, "  130 "+lwAgent)
			r.dirtyFor(130, 700*time.Millisecond)
			h := r.run(tc.env, "-r")
			wantOrder(t, h, unloadSystem, killMC, "shutdown -r now")
			wantOneLog(t, h, `^`+tc.want+`: timing the waits by the wall clock$`)
			wantOneLog(t, h, `^waited [0-9.]+s for UserEventAgent \(LoginWindow\) to settle$`)
			wantWaited(t, h, logged+"UserEventAgent (System) stopped", `^waited (-?[0-9.]+)s for UserEventAgent \(LoginWindow\) to settle$`)
		})
	}
}
