# 0023 — Snow Leopard's updates, one boot each

Date: 2026-10-17
Status: accepted, on measurement

## Decision

On 10.6, `firstboot.sh` installs Apple's updates one at a time
(`snow_leopard_updates`), and restarts before the next one whenever the one
it just installed needs it:

- PackageKit left a deferred install (`/etc/rc.deferred_install`): files it
  could not replace under the running system, which launchd puts in place at
  the end of the next shutdown; or
- the update's own metadata asks for a restart: a distribution's
  `onConclusion="RequireRestart"`, a flat package's
  `postinstall-action="restart"` (read with `xar`). Metadata it cannot read
  counts as asking, and the log says so.

Where neither holds, the next update goes in on the same boot, as before
(`tests/firstboot_updates.bats`, "inert"). The list of updates still to
install is `/private/var/db/.guest-macosx-firstboot/updates.left`; while it is there,
empty or not, the LaunchDaemon stays, and its next run resumes the updates
before anything else, since the account and the rest were done on the
first boot. The first boot writes it whole (to a `.new`, then `mv`), and it
goes only after the marker. Beside it, `updates.current` names the update
being installed, from before it leaves the list until its installer
returns: a boot that finds one (the guest went down in the middle of that
update) logs it and installs none of the rest, so `verify.sh` fails the
build on the missing receipt instead of the next update going onto the
wrong base, or the first boot and every update running again.
The boot that installs the last update, or stops them (an update cut
short, or a list it cannot write or rewrite), turns Remote Login on for
the boot after it (`finish_snow_leopard`), writes the marker, removes the
LaunchDaemon and restarts. So no SSH session ever meets a restart between
updates, and the first sshd runs on a fully updated system, as ADR 0014
already required.

For `updates = security` that is: first boot (10.6.0) sets up the guest and
installs the 10.6.8 combo, which leaves a deferred install; restart; the
second boot (10.6.8) installs Security Update 2013-004; restart; the third
boot answers SSH. It costs one boot, about a minute.

Before each of those restarts, `kext_caches_current` brings the kext caches
up to date with `/System/Library/Extensions` where kextd would not use them
(`kextcache -system-caches`, then `-update-volume /`), and otherwise does
nothing and says so. It asks kextd's own question of each cache kextd's
rescan decision reads: kextd rescans when `check_rebuild` finds any of
`KextIdentifiers.plist.gz`, `IOKitPersonalities_<arch>.ioplist.gz` and
`KextPropertyValues_OSBundleHelper_<arch>.plist.gz` (in
`com.apple.kext.caches/Directories/System/Library/Extensions` under
`/System/Library/Caches`, `<arch>` the running kernel's) stale (`bootcaches.c`'s
`directoryPlistCachesNeedRebuild`). Each is current only when its mtime is
exactly the folder's + 1 s, which is how kextcache writes it and the only
state kextd reads it in ("Cache file %s is out of date; not using."
otherwise). One stale is a rebuild, and the log names which, before the
rebuild and, if any is still stale, after it. The rule and the three
files are the same in 10.6.0's source and 10.6.8's (IOKitUser-502 and
-514.16.50, `OSKext.c`'s `_OSKextWriteCache`, `__OSKextCacheNeedsUpdate`
and `__OSKextCreateCacheFileURL`; kext_tools-177 and -180.2.1), so it does
not matter which kextd runs at the restart. A cache merely newer than the
folder (written in the second the folder changed, or across a backward
clock step) is one kextd rescans. The mkext is judged by the same rule but
apart: a stale one is rebuilt (`kextcache -u`) with no rescan, so it is not
this step's precondition. That `uname -m` names the arch kextd uses (the
registry's kernel CPU type) is REASONED.
The combo's postinstall actions change `/System/Library/Extensions` after
its own kextcache run, so its caches are stale when it ends; 2013-004's
are not, so before the last restart this is inert.

`templates/snowleopard/verify.sh` now also fails a build whose guest has a
crash report in `/Library/Logs/DiagnosticReports` or a
`/var/log/launchd-pid1.crash`, and, with `updates = security`, one whose
CoreFoundation is not 2013-004's 6.6.6. It also fails one whose first
boot's log says it stopped the updates (`updates: STOPPED`, `updates:
cannot ...`), so a stop fails the build whatever the receipts and
`sw_vers` happen to show.

## Why (F16)

Every 10.6.8 build's first boot left three crash reports in the image:
`MRTAgent_*.crash` (SIGABRT), `configd_*.crash` (SIGSEGV) and
`/var/log/launchd-pid1.crash`. It also left a fourth problem nobody had seen.
All four come from one thing: until this decision, the first boot installed
the combo and then 2013-004 *on the same boot*, and restarted once, after
both.

The combo replaces part of the running 10.6.0 under it (libSystem, dyld,
launchd, securityd's clients...) and defers 363 files (CoreFoundation,
Security, CoreServices, ApplicationServices) to the next shutdown. Until
that restart the guest is half one release and half the other.
2013-004 installed into that state (MEASURED 2026-10-15, 3 of 3: the
2026-10-10 and 2026-10-15 builds, and a reproduction from a 10.6.0 box
running the same sequence from a LaunchDaemon):

1. **MRTAgent.** 2013-004's `mrt` postinstall action (re)loads MRT's agent
   and daemon in launchd. Its new Security.framework asked 10.6.0's
   still-running securityd for the security session:
   `SessionGetInfo(0xffffffff) -> Mach -303` (MIG_BAD_ID), and
   LaunchServices aborted (`GetOurLSSessionIDInit() returned err #1,
   securitySessionID == 0x0`). The same agent, started by the combo's own
   `loadMRT` on consistent 10.6.0 libraries, and by 2013-004 on a restarted
   10.6.8, logs that it may not reach the window server before login and
   carries on.
2. **launchd.** The MRT daemon, for the same reason, could not create the
   AuthorizationRef it removes its own jobs with (`AuthorizationCreate ...
   failed with 1`), so both jobs were still loaded at the shutdown, the
   agent watching the daemon (`KeepAlive` `OtherJobEnabled`). launchd
   10.6.0's (launchd-328) shutdown loop removed the inactive daemon;
   removing it dispatched the agent that watched it, which, the system
   shutting down, was removed and freed too; it was the next job in the
   loop, which then read it (`jobmgr_do_garbage_collection` → `job_log`,
   the stack in `launchd-pid1.crash`; launchd's own comment in
   `job_dispatch` describes the hazard). No shutdown log was written,
   because launchd crashed before its first flush.
3. **configd.** launchd's crash handler samples it, then calls `reboot(0)`,
   so every process got the kernel's SIGTERM at once instead of launchd's
   ordered shutdown. The old configd crashed in its SIGTERM path
   (`stopComplete` → `SCLog`). Not pinned to a line: it is seen only on this
   path (3 of 3), never on an ordered shutdown, and not on a kernel-forced
   reboot of a consistent 10.6.0 (1 of 1).
4. **The deferred install ran last, and reverted 2013-004.** launchd never
   reached the end of its shutdown, where it runs `/etc/rc.deferred_install`,
   so the next boot ran it 0.8 s in and restarted again (an extra boot,
   visible only as a tiny `launchd-shutdown.log`). It put the combo's files
   in place, over the newer ones 2013-004 had installed live. The two
   carry 26 files in common, and the 19 of those compared in the shipped
   image were all the combo's, among them CoreFoundation (6.6.5, not
   2013-004's 6.6.6), Security (build 9, not 17), Spotlight's Metadata and
   `md*` (the other 7 were not compared). The image had 2013-004's
   receipt without all of its fixes. That every 10.6.8 image built before
   this decision is so (the release notes say so) rests on one image
   checked, the 2026-10-15 build's, and on the mechanism, which has no
   race in it: launchd's crash came in 3 of 3 first boots, and the
   deferred install runs after it.

With the combo, a restart, then 2013-004 and a restart (the trial harness,
from the same 10.6.0 box): no crash report, an ordered shutdown each time
with the deferred install applied at its end, and MRTAgent and MRT running
normally.

None of this is QEMU's: it is the order of Apple's updates on a live system.
Apple's own sequence is one update, then its restart.

## Why the kext caches, too

The first builds with the combo restarted straight after its install
panicked 10.6.0's kernel in 2 of 3, at the end of the combo, before any
crash report could be written: kextd loading a kext from its mkext
(`OSKext::loadFromMkext` → `OSKext::setVMProtections`, a page fault in
`nextsegfromheader`). The old order never did (0 of 3), and neither did the
combo-then-restart on a booted 10.6.0 box in the trial harness (0 of 6). What
differs is the kext caches at the restart: the combo's actions leave them
older than `/System/Library/Extensions`, kextd "Rescanning kernel
extensions" at the shutdown and sends the new personalities to the running
10.6.0 kernel (the harness logs it in 6 of 6 restarts after the combo),
while in the old order 2013-004's own kextcache run, after the combo's
actions, had left them current. With them rebuilt before the restart, kextd
had nothing to rescan (0 of 4 in the harness). That the rescan is what loads
the kext that panics is REASONED from the panic's stack and its timing (the
panicked builds' last logs end seconds before it); the builds after this
step are the measurement.

Those builds and harness runs gated the step on a looser test, the cache no
older than the folder (`find -newer`). In each build it found the caches
older than the folder after the combo (stale by either test) and not older
after 2013-004, and kextd rescanned at none of those restarts, so its own
test passed there too. The exact test replaced the looser one because the
two disagree in between (a cache in the folder's own second, or more than a
second newer), where the looser one would skip a rebuild and kextd would
then rescan at the restart. That the exact test changes nothing in the
measured runs is REASONED from their logs (which carry no mtimes), not
re-measured.

## Measurements

All 2026-10-15/17, this host (KVM, one guest CPU), from the 10A432 disc.

| | first-boot crash reports | CoreFoundation | 10.6.0 panics |
|---|---|---|---|
| before: combo and 2013-004 on one boot (2 builds, 1 trial) | 3 of 3, all three | 6.6.5 (the combo's) | 0 of 3 |
| one boot per update, no kext step (3 builds) | 0 of 1 that finished | 6.6.6 | 2 of 3 |
| this decision (4 builds) | 0 of 4 | 6.6.6 | 0 of 4 |

The trial harness (a 10.6.0 box running the same steps from a LaunchDaemon):
the old order 1 of 1 with all three; the combo, a restart, 2013-004, a
restart: none; a restart straight after the combo, 6 of 6 with kextd
rescanning, no panic; with the kext step, 4 of 4 with no rescan. A box from
this decision restarted 5 times: no report.

## Failure propagation correction (2026-10-30, F30)

Final whole-branch source review found that the best-effort command runner
hid an installer's nonzero/timeout status. The update loop removed its
in-progress marker and could proceed to a dependent update on an
unsuccessfully updated base. A missing package was also skipped rather than
stopping that dependency chain. An interrupted-update guard alone was not
enough.

The runner now has an internal `--checked` form that returns the original
command status; ordinary nicety callers stay best-effort. Only the 10.6
update installer uses it. Nonzero or timed-out installation, or a missing
package, leaves the in-progress marker, stops successors and logs
`updates: STOPPED`. The existing verifier rejects that line even if receipts
happen to exist. First boot can still finish and expose the failed image to
verification; it never retries the update or calls the next installer.
Production timeout bounds and successful-update/restart policy are unchanged.

**HOST FAULT FIXTURES:** nonzero and timeout tests failed before and passed
after; a missing-predecessor regression did likewise. The current update
suite passes 63 cases; verifier fixtures reject the new failure lines, and
the static command reader recognizes the checked wrapper without mistaking
its bound for a command. These are not measurements of an actual Apple
installer failure or substitutes for fresh integrated success-path builds.

## What would change this

- An update set whose updates need no restart: they go in on one boot, as
  before, by the same code.
- 10.9: unchanged. Its one security update installs on the first boot and
  the build's own shutdown is its restart (ADR 0011); nothing here runs
  there.
- A crash report from the first boot, or a 10.6.0 panic at one of its
  restarts, on a build with this: the mechanism above is not the whole
  story. `verify.sh` fails the first; the second stops the build.
- kextd "Rescanning kernel extensions" at a restart for which
  `kext_caches_current` logged the caches up to date: its test is not the
  one that kextd applies there.
- A CoreFoundation other than 6.6.6 with `updates = security`: 2013-004
  reverted again, by a deferred install or anything else. `verify.sh`
  fails it.
- A 10.6 update that is a product archive (a distribution-style flat
  package) rather than a component package: it has no top-level
  `PackageInfo`, so `xar -xf <pkg> PackageInfo` finds nothing and it always
  gets a restart after it, logged as unreadable. That is the safe side, but
  its own signal is in its `Distribution`'s `onConclusion`, which
  `update_wants_restart` would then have to read.
