# 0019 — Every 10.9 shutdown goes through guest-macosx-shutdown

Date: 2026-10-14
Status: accepted, on measurement

Names: this project's internal names were renamed on 2026-10-17, from
the `mqg` stem to `guest-macosx` (the helper was `/usr/local/sbin/mqg-shutdown`,
logging as `mqg-shutdown:`). The measurements below ran under the old
names, and nothing else about what ran has changed.

## Decision

The base recipe's first boot (`assets/guest/firstboot.sh`, "the shutdown
helper") writes `/usr/local/sbin/guest-macosx-shutdown` on 10.9, root's, mode 755.
Every shutdown and restart this project issues to a 10.9 guest is
`sudo /usr/local/sbin/guest-macosx-shutdown -h` or `-r`, never a bare `shutdown`:

| Who | Where |
|---|---|
| the base build's `shutdown_command` | `templates/mavericks/mavericks.pkr.hcl` |
| the layer build's `shutdown_command` and its restart provisioner | `templates/mavericks-aquatransport/mavericks-aquatransport.pkr.hcl` |
| the box's `vagrant halt` trigger | `templates/mavericks/box.Vagrantfile.pkrtpl` |
| the boot gate's restarts and shutdown | `bin/gate-vm.sh` |

The helper, in order (each wait bounded at 30 s; what it did, including an
agent that was not running, a failed `launchctl` and an OS without
`proc_get_dirty`, goes to `system.log` as `guest-macosx-shutdown:` and to stderr,
which `bin/gate-vm.sh` logs, giving the command 105 s):
1. refuses to run as anyone but root;
2. waits until no job launchd started is under 20 s old, up to about 30 s
   in all (ADR 0021);
3. unloads `UserEventAgent (System)` (`launchctl unload`, not `-w`: the next
   boot loads it), while cfprefsd still runs, and waits until it is gone;
4. waits until no other `UserEventAgent` (the LoginWindow one), and no
   `AppleIDAuthAgent`, is dirty (the second is ADR 0021's, from the
   mechanism: it did not fire in any measured shutdown);
5. stops `ManagedClient` (`killall`), last, so loginwindow has no time to
   start another;
6. `exec /sbin/shutdown -h|-r now`.

`templates/mavericks/verify.sh` fails a build whose guest has no executable
helper. Snow Leopard does not get the helper (below).

## Context

A shutdown or restart of 10.9 over SSH sometimes took 20 s longer than it
should, and 10.9's shutdown monitor (`com.apple.shutdown_monitor`,
`spindump -shutdownstall 2`, which samples from 2 s in) filed a
`.shutdownStall` report for it: every 20 s stall left one, and none of
29 shutdowns of 2-3 s did. The boot gate's health check fails on any such
report, and every box carried several, from its own build. Each stall is
launchd's userspace shutdown waiting out one job's 20 s exit timeout:
"Exit timeout elapsed (20 seconds). Killing", then "Userspace shutdown took
approximately 20 seconds". There are two causes.

How launchd shuts jobs down, which both causes turn on (its log, with
`/var/db/.launchd_shutdown_debugging`):
- A job with `EnableTransactions` that is **clean** (its sudden
  termination enabled) gets SIGKILL at once.
- One that is **dirty** gets SIGTERM, and launchd waits for it to exit, up
  to its exit timeout, 20 s.
- Anonymous processes (not launchd jobs) are signalled only once userspace
  shutdown is over.

### Cause 1: ManagedClient's DiskArbitration session

- About 30 s after boot, loginwindow's anonymous `ManagedClient -s` opens a
  DiskArbitration session (`diskarbitrationd -d`'s log: `created session,
  id = ManagedClient [126]:24591`) that only loginwindow would close.
- `shutdown` bypasses loginwindow, and launchd does not signal the
  anonymous ManagedClient until the end.
- diskarbitrationd stays dirty while a client's session is open, so it gets
  SIGTERM, cannot exit, and is killed at 20 s. "Stray process at
  shutdown: … ManagedClient" follows.
- A shutdown before the session exists is clean, so only shutdowns more
  than about 30 s after boot stalled this way.

### Cause 2: UserEventAgent cannot save what it has pending

- `UserEventAgent (System)` hosts the XPC activity scheduler, the
  `com.apple.time` plugin. As activities are submitted and completed it
  records them in root's `com.apple.xpc.activity` preferences
  (`DateSubmitted`, `DateCompleted`).
- Each `CFPreferencesSetValue` disables the process's sudden termination
  until CFPreferences has synchronized the write with cfprefsd, seconds
  later. Caught with dtrace on `proc_info`'s dirty-control call, with the
  return addresses symbolicated by `atos` in the running guest:
  `_CFSuddenTerminationDisable` ← `-[CFPrefsPlistSource
  alreadylocked_setValue:forKey:]` ← `_CFPreferencesSetValueWithContainer`
  ← `com.apple.time` at boot + 37.699 s; then
  `_CFSuddenTerminationEnable` ← `-[CFPrefsPlistSource
  alreadylocked_synchronize]` at + 41.900 s. The write was
  `com.apple.metadata.mds_stores.power`'s `DateCompleted`.
- **On SIGTERM, UserEventAgent saves what is pending, through cfprefsd, and
  exits.** Traced on a `launchctl unload` that met it dirty: cfprefsd took
  a message 2 ms after `launchctl` started, and 1 ms later UserEventAgent
  re-enabled sudden termination and exited normally.
- **A shutdown breaks exactly that.** launchd SIGKILLs cfprefsd (clean)
  within tens of milliseconds, and only then gets to UserEventAgent
  (dirty) with SIGTERM. Its save cannot land, it stays dirty (spindump:
  "Sudden Term: Dirty" for the whole sample, all its threads idle), and
  launchd kills it at 20 s. In a traced stall, cfprefsd exited 58 ms into
  the shutdown and UserEventAgent made no further dirty-control call.
- securityd and diskarbitrationd wait for UserEventAgent, their client:
  they exit within milliseconds of its death, at the same 20 s, or are
  killed at their own timeouts a moment before. Hence the three timeouts,
  and the report names `diskarbitrationd,UserEventAgent_…` (with
  `AppleIDAuthAgent`, which takes about 3 s to exit, or packer's own
  anonymous `sshd-session`, when those were still there at 2 s; neither
  held anything).
- **The window falls where boot-time activity falls**: about 37-42 s after
  a cold boot (just after SSH first answers, since a cold boot first waits
  about 25 s for DSMOS), and 5-12 s after a warm one. The gate restarts
  within seconds of SSH answering, and a build shuts down right after its
  last provisioner, so they landed there often.
- **launchd's log tells it every time.** Of 152 shutdowns logged without
  the helper's unload, 22 sent UserEventAgent (System) SIGTERM, and all 22
  stalled. None of the 130 that sent it SIGKILL stalled on that path.

**Why unloading, and not waiting for it to be clean.** The first version of
the helper only waited. That left a race: an activity can start and record
itself in the 50 ms between the check and the shutdown. dtrace caught one:
an `IOPMAssertionCreate` from `com.apple.time` at + 11.045 s, the check
clean, the `setValue` at + 11.103 s, `shutdown` at + 11.132 s, a stall. One
of 40 gate-paced restarts stalled that way. Unloading leaves no window:
UserEventAgent is gone before the shutdown starts, and whatever it had
pending has been saved, because cfprefsd was still running.

**Why not kill it.** SIGKILL would drop the activity scheduler's record of
what ran. The unload's SIGTERM saves it.

**The LoginWindow UserEventAgent** is often dirty in the second after it
starts, about 7 s into a warm boot. In the 11 logged shutdowns that sent it
SIGTERM, it exited at once 10 times. Once (a warm restart 7 s after boot), it
did not, and was killed at 20 s, with securityd waiting for it. Why is not
known. It lives in the LoginWindow session, which a root `launchctl` does not
reach, so the helper waits until it is clean instead; that narrows the window
and does not close it.

## Measurements

All 2026-10-13 and 14 on `pet-power-plant` (QEMU 8.2.2, KVM), one guest at
a time, through `bin/gate-vm.sh`'s QEMU command line, 2 vCPUs, with the
firmware's patches 0004 and 0005. A stall is an exit timeout in launchd's
shutdown log, which also always came with a `.shutdownStall` report and a
restart about 20 s longer.

Cause 1 (2026-10-13; the layer box, restarts 30-86 s after boot, once the
session existed):

| Shutdown command | Restarts | Stalled |
|---|---|---|
| `sudo shutdown -r now` | 8 | 8 |
| `sudo killall ManagedClient; sudo shutdown -r now` | 12 | 0 |

Cause 2, spread over the time after boot (2026-10-14; the gate's layer box
from the IGVM fix, `/tmp/mm-build/gate-f1/box-layer`, fresh each round).
Each round: a cold boot (a new QEMU process, the box's empty NVRAM, as the
gate's own boot), a wait after SSH first answered, a restart; then a second
restart as soon as SSH answered on that warm boot. Waits 0, 1, 2, 5, 15, 30,
60 and 120 s, 20 rounds, 40 shutdowns each way:

| Shutdown command | Shutdowns | Stalled | Where |
|---|---|---|---|
| `sudo killall ManagedClient; sudo shutdown -r now` (cause 1's fix alone) | 40 | 10 | cold, waits 0-5 s: 8 of 12; cold, 15-120 s: 0 of 8; warm, 7-12 s after boot: 2 of 20 (one of them the LoginWindow agent) |
| the first helper (wait until clean, no unload) | 40 | 0 | it waited 0.2-5.6 s, 7 times |
| `sudo /usr/local/sbin/guest-macosx-shutdown -r` (this decision) | 40 | **0** | it unloaded UserEventAgent with a write pending 11 times; restarts 18-27 s |

Cause 2, at the gate's own pace (`bin/gate-vm.sh restart`, polling every
5 s, so 5-12 s after boot):

| Box | Shutdown command | Restarts | UserEventAgent stalls | Other jobs' stalls |
|---|---|---|---|---|
| layer, from this branch | `killall ManagedClient; shutdown -r now` | 30 | 11 | 3: netbiosd once, xpcd twice |
| layer, from the IGVM fix | the same | 30 | 14 | 0 |
| layer, from this branch | the first helper | 70 | 1 (the race above; dtrace caught another in a separate probe run) | 0 |
| layer, from this branch, dtrace probe | `guest-macosx-shutdown` | 60 | **0**; unloaded with a write pending 30 times | 0 |
| base, from this branch | `guest-macosx-shutdown` | 60 | **0**; unloaded with a write pending 28 times | 2: AppleIDAuthAgent once, kextd once |

So before the fix, 25 of 60 gate-paced restarts stalled on UserEventAgent and
3 more on other jobs; with this decision's helper, 0 of 120 stalled on
UserEventAgent, and 2 on other jobs. (An earlier count of this table, in
commit `fdc9f92`'s message, put all 14 of the first row's stalls on
UserEventAgent; 3 of them were netbiosd's and xpcd's.)

One-sided Fisher exact p for 0 of the 10 stalls of the first table
falling in this decision's 40: 0.0005. The other jobs' stalls are below,
under "What would change this".

How it was observed, in a probe overlay of a box only, never in a box:
- `/var/db/.launchd_shutdown_debugging`;
- dtrace on `syscall::proc_info:entry` with `arg0 == 8`
  (`PROC_INFO_CALL_DIRTYCONTROL`), with user stacks. dtrace must not start
  before DSMOS on a cold boot: started at once, it held the boot at
  "Waiting for DSMOS..." for good, twice. A probe that waits for the
  `Dont_Steal_Mac_OS_X` kext booted every time;
- `proc_get_dirty` from 10.9's Python, through ctypes, on every process
  before each shutdown.

## Alternatives considered

- **Wait until UserEventAgent is clean, then shut down.** The first
  version. It leaves the 50 ms race above, and makes some restarts 5-9 s
  longer.
- **Kill UserEventAgent before the shutdown.** It would end the stall and
  lose the very write that holds it; launchd would also restart it at once.
- **Keep the inline `sudo killall ManagedClient; sudo shutdown …`, and add
  the rest inline too.** The same Python in five places, quoted three
  different ways (HCL, Ruby, bash). One file in the image is one place to
  read, test and change.
- **Shut down through loginwindow** (an Apple event to it), which would
  close ManagedClient's session itself. launchd's order is the same, so
  UserEventAgent would still stall, and loginwindow's path shows a dialog
  to a logged-in user.
- **Wider timeouts, or an allowlist row for these reports.** They hide a
  20 s delay that every consumer's `vagrant halt` and every restart would
  still pay.

## What would change this

- **A box from before this decision** has no helper: its build used the
  inline command, and its `vagrant halt` trigger a bare `shutdown`. Rebuild
  it.
- **A shutdown that does not go through the helper**, such as a
  consumer's own `sudo shutdown -r now`, can still stall, by either cause.
  The helper is the safe way to restart a box.
- **Other jobs busy in a boot's first seconds** can still hold a shutdown
  that comes that early, each for its own 20 s exit timeout. Seen, 5-12 s
  after a warm boot unless noted:
  - a just-started LoginWindow UserEventAgent (above), once;
  - AppleIDAuthAgent, blocked on a semaphore in its scheduled account
    check, once, 54 s after a cold boot (with the helper);
  - kextd, validating a kext's signature (`SecStaticCodeCheckValidity`,
    down to `CSSM_ModuleLoad`), once, 6 s after boot, with
    diskarbitrationd waiting (with the helper);
  - netbiosd, with no thread doing anything the spindump could see, once
    (without the helper);
  - xpcd for the `_softwareupdate` user (uid 200), its main thread waiting
    on a semaphore, twice (without the helper).

  They were not taken up here. netbiosd's and xpcd's came in 3 of 60
  gate-paced restarts without the helper and in none of 190 with either
  version of it (Fisher p ≈ 0.01). ADR 0021 traced xpcd's cause (a job
  still starting when the shutdown begins, so the helper first waits for
  that). It classed netbiosd's, by timing, as the same, without tracing
  its exact wait, and no netbiosd stall came in its before-arm.
- **A guest that never settles** shuts down after the helper's 30 s waits
  anyway and says so in `system.log`; the stall then shows, in the gate's
  health check too.
- **Snow Leopard** has neither cause, and keeps a bare `shutdown`
  (`templates/snowleopard`'s `shutdown_command` and halt trigger, and the
  first boot's restart after 10.6's updates). 10.6.8 has no cfprefsd and
  no XPC activity scheduler, and with `ManagedClient -s` running, 8
  shutdowns 29-144 s after boot took 0.4 s of userspace shutdown each.
  Its shutdowns about 10 s after boot took 16.4-16.7 s instead, 3 of 3, held
  by `com.apple.crsud`, which runs at load and does not exit on SIGTERM
  until it is done: a different cause, not taken up here. A 10.6 that grows
  either cause would need the helper too.
