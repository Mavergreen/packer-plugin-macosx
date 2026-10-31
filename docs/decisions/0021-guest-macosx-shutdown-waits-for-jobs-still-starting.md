# 0021 — guest-macosx-shutdown waits for jobs still starting, and for AppleIDAuthAgent

Date: 2026-10-14 (measured 2026-10-14/15)
Status: accepted, on measurement

Names: this project's internal names were renamed on 2026-10-17, from
the `mqg` stem to `guest-macosx` (the helper was `/usr/local/sbin/mqg-shutdown`,
logging as `mqg-shutdown:`). The measurements below ran under the old
names, and nothing else about what ran has changed.

## Decision

`/usr/local/sbin/guest-macosx-shutdown` (ADR 0019) changes in two ways:

1. **First, it waits until no job that launchd started is under 20 s old.**
   - "A job launchd started" is a process whose parent is launchd (pid 1),
     aged by `ps`'s `etime`.
   - Not counted: `launchproxy` (every SSH session's sshd, the helper's own
     among them), per-user `launchd`s (an SSH login starts one), zombies,
     and the helper itself with every parent of it that launchd adopted
     (`nohup sudo guest-macosx-shutdown -r &` is reparented to launchd as its shell
     exits, and would otherwise wait for itself), and the helper's process
     group (the rest of a backgrounded pipeline, `… | tee log &`).
   - Every young process is waited for, whatever its name: root's `xpcd`
     and `_softwareupdate`'s start seconds apart on every warm boot, and
     the second is one of the measured stallers.
   - A process an earlier session or command left behind (`nohup … &`) is
     counted: launchd adopted it, and it is as young as it looks. One left
     behind by the helper's own command (`ssh guest 'nohup foo & sudo
     guest-macosx-shutdown -r'`, a shell without job control) is in the helper's
     process group, and is not.
2. **Its wait for a dirty `UserEventAgent` now covers `AppleIDAuthAgent`
   too.** The log names whichever it waited for: "waited N s for
   AppleIDAuthAgent, UserEventAgent (LoginWindow) to settle". It is inert
   unless the agent is dirty, and it did not fire in any of the 266
   shutdowns measured with it. It is here for the mechanism (below), not
   for a measurement; the measured results belong to the first wait.

The rest is as before: unload `UserEventAgent (System)` and wait until it
is gone, stop `ManagedClient`, shut down. Each wait is bounded at 30 s.
A guest that keeps starting jobs still shuts down. The first wait does not
try to tell a job that launchd respawns from one still starting. It holds
the wait only while a copy of it is young at each look (Limits).

The bounds are on `mach_absolute_time`, which does not step when the wall
clock does (ntpd, a host resume); Python 2.7 has no `time.monotonic`. So
each wait ends within 30 s and one look (a `ps`, or a `ps` and
`proc_get_dirty`): reasoned, about 90 s for the three, which
`bin/gate-vm.sh`'s 105 s covers. A hang inside `ps` or `launchctl` itself
is not bounded. Each wait is also bounded by its number of looks (60 for
the first, 600 for the unload, 300 for the dirty wait), a backstop should
that clock not move; a wait ended by that backstop takes its looks times
its interval plus one look's work each, which was not measured. A
libSystem without `mach_absolute_time` (not 10.9) is said in the log, and
the waits are timed by the wall clock.

Every outcome of the first wait is logged to `system.log`, and to the
caller on stderr, so a settled guest can be told from a blind check:
- "waited N s for jobs still starting: …", ending "; the last to settle:
  …" with the jobs still young at its last look, when those were not all
  of them and `ps` did not fail mid-wait (`docs/decisions/0033`);
- "jobs still starting after 30s: …; shutting down anyway";
- "no job launchd started is under 20s old (N checked)", when there was
  nothing to wait for;
- "could not tell which jobs are still starting (…); not waiting for any",
  when `ps` failed or listed no job launchd started;
- "could not read the age of N process(es) launchd started; not waited
  for", when `ps` printed an age the helper cannot read (a clock step
  prints negative ones).

Just before the shutdown it looks once more, because the unload and the
dirty wait take time, and says "jobs started during the waits: …; shutting
down anyway" or "no new job started during the waits". A job that started
is a pid the first wait never saw: not one it gave up on, but a fresh copy
of one is. A pid, not its name or uid: on 10.9 a job's uid changes under
its pid in its first second (`_softwareupdate`'s `xpcd` is root's until
its setuid, measured), and Darwin hands pids out in sequence up to 99999,
so none is reused within the helper's run (reasoned). When the first wait
could not tell, the last look says "jobs still starting at the shutdown:
…" or that none is. It does not wait again. `bin/gate-vm.sh` gives the
command 105 s, not 75: three 30 s waits and 15 s for the rest.

The cost:
- On an idle guest nothing new starts after a boot's last wave until the
  next SSH login, so the wait usually costs nothing there. REASONED, not
  measured: every one of the 266 measured shutdowns waited at least 5.6 s.
  But that login can start a job. With OpenSSH up to 10.6p1-mavericks.2,
  the boot's first one started `sandboxd` (that OpenSSH's sandbox), and a
  shutdown it delivered waited about 19 s for it: 39 of 40 halts and
  restarts measured (F14, `docs/decisions/0033`). The fix is in
  Mavergreen/openssh, and the pin moved to 10.6p1-mavericks.3, which
  carries it.
  Jobs that launchd runs periodically (`newsyslog`, `periodic-daily`,
  `backupd`, Spotlight's `mdworker`, `NetBootClientStatus`) can be under
  20 s old at any time, and an idle guest shut down just after one waits
  for it.
- Soon after a boot it is up to about 30 s. Of 220 waits measured: a
  restart as soon as SSH answers waited 21-23 s (7 waited 24 s or longer,
  the longest 30.3 s), one about 22 s after boot 5.6-7.3 s, and the first
  restart of a cold boot about 50 s in 5.6-8.3 s. Two of the 30.3 s waits
  ran to the bound's last look, and the jobs happened to settle on it; none
  gave up. The boot gate's pace (11-12 s) was not measured; by the same
  timeline it waits about 17 s.

## Context

With ADR 0019's helper, about one restart in thirteen at the pace of F6's
measurements still stalled 20 s and left a `.shutdownStall` report, which
fails the boot gate: 14 of 180, in `AppleIDAuthAgent`, `netbiosd`, `xpcd`,
`kextd` (with `diskarbitrationd`), `SleepServicesD` and `warmd`. None was
`UserEventAgent (System)`.

### When

Every one of them came in a boot's first seconds:
- The restarts F6 measured: 9 of 50 whose boot was checked at 5-6 s of
  uptime stalled, and none of 110 at 7 s or later.
- This decision's own runs, which take each restart at a measured uptime
  (`SHUTDOWN_TIME` minus `BOOT_TIME`):
  - restarts at 6-7.7 s: 15 of 100 stalled (and 10 of 80 in earlier runs);
  - restarts at the boot gate's real pace, 11-12 s, its three checks
    included: none of 20;
  - restarts 20 s or more after boot: none of 200.
- `AppleIDAuthAgent`, at the first restart of a cold boot of the base box
  (50-52 s in): every time, 14 of 14.

A warm boot starts its jobs in two waves:
- the core daemons, in its first 2 s;
- 5-8 s in: `warmd`, `SleepServicesD`, `netbiosd`, loginwindow and its
  session (`WindowServer`, `AppleIDAuthAgent`, the LoginWindow
  `UserEventAgent`, `ManagedClient`, `tccd`), and `softwareupdated` with
  its user's `xpcd`.

A cold boot starts the second wave behind its 25 s DSMOS wait (F3), at
about 38 s. Nothing more starts until an SSH login.

### Why a job still starting holds the shutdown

launchd's shutdown signals every job at once and relaunches none. 10.9
writes launchd's own log of it on every shutdown of a verbose boot. A
clean job that allows idle exit gets SIGKILL at once; a dirty one gets
SIGTERM and up to 20 s (ADR 0019).

In a shutdown 20 s or more after boot, `AppleIDAuthAgent`, `xpcd`,
`SleepServicesD` and `netbiosd` are clean and killed at once. In their
first seconds they are dirty (`proc_get_dirty`, sampled every 0.1 s from
boot):
- `AppleIDAuthAgent` opens a transaction as it starts and ends it 3 s
  later (its `main`: `xpc_transaction_begin`, then `dispatch_after(3 s)`);
- `SleepServicesD` for its first second or so;
- `softwareupdated`'s `xpcd` for a few hundred ms;
- `warmd` for its first 13 s.

So they get SIGTERM, which they are not ready for. In the shutdowns that
stalled, launchd's log shows two ways it goes wrong.

**The SIGTERM is lost.**
- `warmd`'s `main` sets SIGTERM to `SIG_IGN`. Then it sets up its queues,
  its XPC listener and its loginwindow notifications. Only then does it
  create its SIGTERM dispatch source. launchd sent SIGTERM 7.8 ms into the
  shutdown, but `warmd` checked in its MachService only 91 ms in. So its
  handler did not exist yet, and the ignored signal was gone. Every thread
  then sat idle until the SIGKILL at 20 s.
- `SleepServicesD` did the same. Its SIGTERM came at 7.9 ms. The function
  that creates its SIGTERM source also checks in its MachService, and that
  check-in came at 33 ms. It then sat idle in its run loop.

**Its start-up waits on a daemon the shutdown already stopped.** During a
shutdown, launchd still answers a lookup for a stopped daemon's service
with that service's port. It now holds that port's receive right itself,
and it never relaunches the daemon, so a request sent there is never
answered.
- `xpcd` (for `_softwareupdate`) was starting as the shutdown began. It got
  SIGTERM at 7 ms, opendirectoryd was gone at 43 ms, and at 64 ms launchd
  handed `xpcd` the port of `com.apple.system.opendirectoryd.libinfo`. Its
  main thread then waited on a semaphore until it was killed.
- `kextd` was loading a kext the boot had asked for. It got SIGTERM at
  36 ms, launchd answered its lookup of `com.apple.SecurityServer` at
  53 ms, and securityd was gone at 58 ms. `kextd` sat in
  `ucsp_client_setup` (its code-signature check of the kext) until it was
  killed, and `diskarbitrationd`, which it is a client of, with it.
- `netbiosd` retried configd and syslogd for 18 s.

### AppleIDAuthAgent's account check

`AppleIDAuthAgent` also schedules an account check 10 s after it starts,
with 5 s of leeway (`startScheduledAccountCheckTask(10, 5)`). When the
check has accounts to update, it writes `com.apple.coreservices
.appleidauthenticationinfo` with `CFPreferencesSetMultiple` and flushes it
3 s later. That holds it dirty for 3-4 s. A shutdown in that window
SIGTERMs it after launchd has killed cfprefsd, and it stalls 20 s: ADR
0019's cause 2, in another agent.

On the base box this happens at every cold boot, 49-53 s in, because its
`ByHost` preferences are not found then (F8, below). On the layer box, and
on warm boots, it found nothing to update in our runs. The window soon
after a boot is carried by the first wait, which was measured: 20 s after
a boot's second wave the agent is past its check (10 s + 5 s of leeway +
4 s is 19 s). The dirty wait did not fire in any of the 266 shutdowns
measured with it, because none came while the agent was dirty. It covers
a later check at any uptime, in a guest that has been up a long time.
That is the mechanism, and it has not been measured as a stall. Its path
through `proc_get_dirty` has logged once in a real guest: 1.3 s, "waited 1.3s
for AppleIDAuthAgent to settle", in F11's E1 set-up restart right after a
deliberate backward clock step (2026-10-15), a test artifact; whether that
restart would otherwise have stalled was not measured. The same restart
logged "could not read the age of 71 process(es) launchd started": after a
backward step the young-job wait is blind to every job, as the Limits say.

### What a user sees

A restart or a shutdown soon after a boot takes 20 s longer and leaves a
`.shutdownStall` report. Files on disk are safe: the kernel unmounts the
volumes only after launchd's userspace shutdown, whether jobs were killed
or not. What a killed job had not yet saved is lost, as with
`AppleIDAuthAgent`'s account record. So, often, is the boot's whole
`system.log`: of 100 restarts at about 7 s, 16 left no line from the boot
they ended, not even its `BOOT_TIME`; of 100 with this decision's wait,
none. That was syslogd waiting on a throttled aslmanager for the first
12 s of a boot, writing nothing, and dying with the boot's log at the
shutdown's SIGTERM: `docs/decisions/0031` has the mechanism and its fix,
which holds for every shutdown, not only the helper's.

### F8, found on the way (not fixed here)

Early in a cold boot cfprefsd logs "_CFXPreferencesGetByHostIdentifierString:
unable to determine UUID for host... Giving up and returning
00000000-0000-0000-0000-000000000000". After that, root's `ByHost`
preferences are read and written as `….00000000-0000-0000-0000-000000000000
.plist` on that boot, and as `….525400123456.plist` on warm boots. The base
box carries only the second file, and the layer box both. So a `ByHost`
preference set on a warm boot is not there on a cold one, and the reverse.

## Alternatives

- **Disable the jobs a CI guest does not need** (`AppleIDAuthAgent`,
  `netbiosd`, `SleepServicesD`, `warmd`). Rejected:
  - `kextd` and `xpcd`, which a guest needs, stall the same way, and so
    would the next job Apple starts at boot;
  - `AppleIDAuthAgent` may matter to the Aqua Transport layer's Apple ID
    services.
- **Stop the jobs in order before `shutdown`**, as the helper does for
  `UserEventAgent (System)`. Rejected:
  - there is no list of them;
  - a lost SIGTERM is lost whoever sends it;
  - jobs that start during the stop would be in the same place.
- **Wait for uptime.** Rejected: a cold boot's jobs start about 30 s later
  than a warm boot's.
- **Wait until nothing is dirty.** Rejected: securityd, syslogd,
  opendirectoryd, diskarbitrationd, distnoted, authd and others are dirty
  for as long as they run.
- **Lower `ExitTimeOut`** (the community's advice for AppleIDAuthAgent's
  shutdown stalls on 10.8 and 10.9). Rejected: it hides the stall and
  keeps the kill.

## Measurements

Setup:
- the boot gate's boxes (`gate-f2b`), with the telemetry plugin set aside
  (ADR 0020);
- each restart through the frozen `bin/gate-vm.sh`;
- the shutdown's uptime from the guest's own `SHUTDOWN_TIME` and
  `BOOT_TIME`;
- a stall is an "Exit timeout elapsed" in launchd's shutdown log, and each
  one came with a `.shutdownStall` report.

Before is ADR 0019's helper, as `firstboot.sh` writes it at this
decision's parent. After is that helper with this decision's two waits.
The arms alternate round by round.

| Restarts | Before | After |
|---|---|---|
| layer box, as soon as SSH answers (6-7.7 s before; 28-37 s after the wait) | **15 of 100** | **0 of 100** |
| base box, first restart of a cold boot (50 s in) | **8 of 8** | **0 of 8** |
| the same, with only a 10 s wait for young jobs and no AppleIDAuthAgent wait (a 10 s wait, not 20 s, so it does not show that the dirty wait is needed) | | 7 of 8 |
| base box, as soon as SSH answers | 1 of 8 | 0 of 8 |
| layer box, about 22 s after boot | 0 of 100 | 0 of 100 |
| layer box, the gate's pace (11-12 s; before only) | 0 of 20 | |

The 8 of 8 to 0 of 8 and the 15 of 100 to 0 of 100 are the 20 s wait for
jobs still starting. The AppleIDAuthAgent dirty wait took no part in
either: it did not fire in any of the 266 shutdowns measured.

Fisher's exact test, one-sided: p = 1.7e-5 for the layer box, 7.8e-5 for
the cold boots. The jobs that stalled before were `kextd` (7), `xpcd` (2),
`SleepServicesD` (4), `warmd` (4) and `softwareupdated` (1, still checking
in 101 ms into the shutdown, with cfprefsd already gone).

## Limits

- **The precondition is "parent is launchd".** A per-user `launchd`'s own
  children are not counted. The measured boxes had no Aqua session: the
  default build sets no password (the owner's decision, 2026-10-15), so
  it does not auto-log in. A build that sets one would have that session,
  whose agents are children of the user's `launchd`, so a young one there
  is not waited for. `AppleIDAuthAgent` and `UserEventAgent` are still
  matched by the dirty wait. Unmeasured.
- **A job that starts after the first wait** (during the unload, or a
  dirty wait of 3-4 s on a long-running guest) meets the shutdown young.
  Measured, the gap was 0.02-0.16 s (the unload). The helper looks once
  more before the shutdown and logs what it finds, but does not wait
  again.
- **A job launchd respawns** is waited for like any other, and holds the
  wait only while a copy of it is young at each look. One whose copies
  live past launchd's 10 s throttle holds it to 30 s, and is named in
  "jobs still starting after 30s: …". One that dies on start (a KeepAlive
  job that crashes) is usually gone, waiting out the throttle, so the wait
  ends between copies, after a "waited N s" line. A copy that then meets
  the shutdown is reported by the last look ("jobs started during the
  waits: …") if it is there then. A gone copy cannot hold the shutdown.
  That is intended. Each rule tried for telling a respawn from a job
  still starting (a new pid of a name; a new pid after an earlier one had
  gone) stopped waiting for real starting jobs in some case,
  `_softwareupdate`'s `xpcd` among them.
- **Ages are `ps`'s `etime`, from the wall clock.** A clock stepped forward
  reads a young job as settled; one stepped back reads a settled job as
  young (still bounded at 30 s), or prints an age that is not one. The
  guest's clock comes from the host's RTC. Unmeasured.
- **`netbiosd`**: its block was classed by timing as a job still starting;
  its exact wait was not traced.
- **Job names in the log** keep their arguments ("universalaccessd
  launchd"); cosmetic.
- **A process `ps` prints in parentheses.** `ps` prints a live process
  whose arguments it cannot read (one exiting, say) as "(name)". Since
  2026-10-15 the helper reads it as "name": it is waited for
  under its plain name, and an exiting per-user launchd, "(launchd)", is not
  waited for (one was, on a base-box warm boot). It is the same job as a
  "name" only where `ps`'s name (the bare executable, cut at 16 characters)
  equals the helper's: "(UserEventAgent)" beside "UserEventAgent (System)"
  is still two names, cosmetically.
- **ManagedClient's 30 s give-up** (2 of 16 base-box restarts as soon as
  SSH answered, 2026-10-15, no stall): not seen in 16 more restarts
  made as soon as SSH answered. Eight further boots, watched for 120 s, made
  no probe restart, so no give-up could occur in them. In all 24 boots the
  late `ManagedClient` start that caused it was absent. The `ManagedClient` daemon started 6-7 s into each
  boot, idle-exited about 20 s later, and was never started again; there is
  no relaunch loop to detect. A `ManagedClient` started 16.5 s or more into
  a boot outlives the bound on a restart made as soon as SSH answers, and
  the bound is what ends the wait then. What started it late in those two
  boots is not known.
