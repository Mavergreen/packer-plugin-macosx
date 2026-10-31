# 0026 — kextd held off while an update's kernel goes in

Date: 2026-10-18
Status: proposed, on measurement

10.9 (2026-10-28): the same hold, for Security Update 2016-004's kernel,
followed by a restart into it at the end of the first boot
(`docs/decisions/0036`, F29). Where this decision says 10.9 is untouched,
read it as of 2026-10-18.

## Decision

On 10.6, before `snow_leopard_updates` runs the installer on an update,
`firstboot.sh` asks whether the update puts a kernel at `/mach_kernel`
(`update_installs_kernel`: a flat package's `Bom`, or the `Bom`s of the
packages a distribution's `pkg-ref`s name, each read with `xar -xf <pkg>
Bom` and `lsbom -s -f`). Where it does, it unloads kextd's job
(`launchctl unload /System/Library/LaunchDaemons/com.apple.kextd.plist`,
`hold_kextd_for`) and leaves it unloaded until the restart after that
update, when launchd starts kextd from its plist on the new kernel.

- **The precondition, checked:** 10.6.8's combo installs a kernel (its
  `SUBaseSystemCombo10.6.8.pkg` lists `./mach_kernel`, with `System.kext`
  and `AudioAUUC.kext`); Security Update 2013-004 does not. So
  `updates = security` holds kextd off for the combo's boot only; 2013-004's
  boot, `updates = none` and 10.9 are untouched. A package whose `Bom`
  xar cannot extract, or lsbom cannot list (lsbom's own status, not
  grep's of whatever it printed), or a distribution that names no package,
  counts as one that installs a kernel (the safe side: holding kextd off
  costs nothing the restart does not give back), and the log says so
  ("kextd: cannot read the Bom of ...", "kextd: cannot list the Bom of
  ...", "kextd: cannot read the packages of ...").
- **The log, each way:** "kextd: held off until the restart:
  041-...dist installs a kernel (SUBaseSystemCombo10.6.8.pkg), and kextd
  would hand its kexts to the running kernel (10.0.0) whenever it found
  its caches out of date; launchd starts it again with the new kernel";
  "kextd: SecUpd2013-004.pkg installs no kernel; kextd left running"; an
  unload that does not take: "kextd: COULD NOT unload com.apple.kextd ..."
  and the update goes in anyway, as before this decision: skipping the
  combo would leave a worse guest.
- **The guard:** `templates/snowleopard/verify.sh` prints
  `kextd-hold=held off for an update that installs a kernel|COULD NOT|none`
  and fails the build when the first boot's log says "kextd: COULD NOT
  unload" (the update went in exposed, the race this closes), or, with
  `updates = security`, never says "kextd: held off until the restart:"
  (the combo installs a kernel, so a build without that line went in
  unprotected, whatever the reason). A guest made through the race is not
  shipped quietly; the build fails loudly after it.
- **What the check proves, and what is REASONED:** after the unload,
  `launchctl list com.apple.kextd` failing means launchd has taken the job
  out (its `job_find` skips a job being removed), not that kextd has
  exited; kextd did exit within the second wherever it was logged (fix1:
  "com.apple.kextd[10]: Exited with exit code: 2" at the unload's second),
  and launchd kills it at its exit timeout if not. That the same check
  answers 0 for a job still loaded, which the COULD NOT branch relies on,
  is REASONED from launchctl's source: in every real run (f01-f04, fix1)
  it was asked only after the unload took, and no VM was run to show its
  other side.
- **The restart is owed:** an update that installed a kernel is followed
  by a restart before the next update even when neither its metadata nor a
  deferred install asks for one (the combo has both), since kextd may not
  come back under the old kernel: it sends every kext's personalities on
  every start (REASONED from `kextd_main.c`'s `main`).
- **Nothing else changes.** `kext_caches_current` still makes the caches
  current before that restart (`kextcache` works without kextd: it takes
  kextd's volume lock only from a kextd that is there, `kextcache_main.c`'s
  `takeVolumeForPath`), and `shutdown -r now` restarts without kextd's
  reboot lock when there is no kextd to ask.

Fix round 1 (the F24 review's M1-M3, N1, 2026-10-18): the lsbom status,
the verify.sh guard and the REASONED labels above; and the tests'
`launchctl` stub now models `load` and `start` too, and their `shutdown`
stub records whether kextd's job was loaded at the restart, so a reload
before the restart fails the state assertions, not only the call log's.
No VM ran for this round: the code it touched runs only on the paths
above, which the tests cover.

## Context: F24

One of F22's two HEAD builds (`updates = security`, 2026-10-17, the host
at load average 16-26) panicked 5 min into its first boot, while 10.6.8's
combo installed on the running 10.6.0: kextd logged "Rescanning kernel
extensions" at 21:35:56, and 10.6.0's kernel took a page fault in
`com.apple.driver.AudioAUUC` 1.57's own code, under
`OSKext::loadKextWithIdentifier` ← `OSKext::loadFromMkext` ←
`kext_request`, on kextd's thread. ADR 0023's `kext_caches_current`
covers the restart after the combo; this was before it.

### The mechanism (source, then MEASURED 2026-10-18)

From kext_tools-177 (10.6.0's kextd):

1. kextd watches `/System/Library/Extensions` (and the mkext and booter
   paths of every volume with a `bootcaches.plist`) with
   `notify_monitor_file` (`kextd_watchvol.c`, `watch_path`).
2. For its first 300 s it holds off (`kAutoUpdateDelay`, "5519500";
   `sAutoUpdateDelayer`): a change to the folder schedules nothing. At
   300 s the timer (`checkAutoUpdate`) checks every watched volume
   (`checkAllWatched` → `check_vol_busy` → `check_rebuild`). After it,
   each change schedules `check_now` 5 s later (`kWatchSettleTime`),
   which calls `check_rebuild` too.
3. `check_rebuild` on `/`, when `plistCachesNeedRebuild` finds a kext
   cache out of date (each cache is current only at the folder's mtime
   + 1 s, ADR 0023), sends kextd SIGHUP: `rescanExtensions` logs
   "Rescanning kernel extensions", rereads every kext on the disk and
   sends all their personalities to the running kernel
   (`sendPersonalitiesToKernel`). The kernel matches them and asks kextd
   to load the drivers it now wants.
4. While the combo installs, the kexts on the disk are 10.6.8's from the
   moment PackageKit shoves its payload in, and the caches are out of
   date until PackageKit's own `kextcache` run, and again from the
   combo's postinstall actions (`removeGeForceDriver`,
   `removeAppleHDAPlatformDriver`, `touchBrailleDrivers`) until
   `kext_caches_current`. The running kernel is 10.6.0's until the
   restart.
5. 10.6.8 has kexts 10.6.0 has none of (`AudioAUUC.kext`,
   `AppleMCCSControl.kext`: not in a 10.6.0 image's
   `/System/Library/Extensions`, 161 kexts) and whose personalities match
   this virtual hardware, so the rescan has 10.6.0's kernel load 10.6.8's
   AudioAUUC 1.57, linked against 10.6.0's IOAudioFamily 1.7.0fc16 and
   IOGraphicsFamily 2.0. Most of the time that load survives; F24's
   build did not.

Nothing in Apple's install flow holds kextd off: 10.6.0's and 10.6.8's
PackageKit name kextd nowhere (their strings: `kextcache` and
`-system-caches` only), and none of the combo's scripts touch it. On a
Mac that had been up 5 minutes the watcher is live through any install;
what makes it hurt here is the jump from 10.6.0 to 10.6.8 under one
running kernel and a hardware match no 10.6.0 kext makes.

So whether a build meets it is timing. The first boot starts the combo
about 20 s after kextd. On this host, idle, the combo's install takes
about 190 s and its payload lands about 120 s in, so the install and
`kext_caches_current` end just inside kextd's 300 s; with the host
loaded (F22's build: the payload at 196 s into the install, its end near
290 s), the 300 s timer fires mid-install with the caches out of date,
or, slower still, the payload lands with the watcher already live.

### Measured

A 10.6.0 box (this tree's `updates = none` build, 2026-10-18) running the
combo from a LaunchDaemon, as the first boot does, started when kextd had
been up <at> seconds; then the first boot's kext step and `shutdown -r now`
(`f24/trials/`):

| | combo started (kextd age) | trials with a mid-install rescan (its kextd ages) | 10.6.8 kexts the 10.6.0 kernel loaded | next boot |
|---|---|---|---|---|
| as today, 105 s | 107-295 s | 0 of 1 (the 300 s timer fired as the kext step finished) | msdosfs 1.6.3 | 10.6.8 |
| as today, 150 s | 152-357 s | 1 of 1: at 301 s (the timer) and 345 s | AudioAUUC 1.57, AppleSMBusPCI | 10.6.8 |
| as today, 200 s | 201-394 s | 2 of 2: at 334-338 s and 380-383 s (5 s after a change) | AudioAUUC 1.57, AppleSMBusPCI | 10.6.8 |
| kextd held off, 105, 150, 200 s | | 0 of 4 | none | 10.6.8, all 4 |

None of those panicked: the load F24's build died of (AudioAUUC 1.57 into
10.6.0) happened in 3 of 4 without the hold and survived each time. The
panic is the rare outcome; the load is the hazard, and the hold takes it
away. With the hold the installer's transcript is the same line for line
(`install.log`, b01 against f01, temporary names and free space apart),
its kextcache runs return 0 without kextd, and so do the first boot's.

Two `updates = security` builds, each pinned with a busy loop to one
host core (a loaded host for that build alone; the combo's payload landed
377-381 s into kextd's life, past its 300 s):

| | combo (kextd age) | rescans under 10.6.0 | "Refusing new kext" under 10.6.0 | verify |
|---|---|---|---|---|
| HEAD (5b8fb03) | 27-575 s | 2: at 422 s (mid-shove) and 535 s (5 s after `removeGeForceDriver`) | 57 | ok (no panic) |
| this decision | 29-544 s | 0 ("kextd: held off until the restart: ... (SUBaseSystemCombo10.6.8.pkg)") | 0 | ok, CoreFoundation 6.6.6, no reports |

In the HEAD build kextd's own rescans and `kextcache -u` left the caches
current before the restart, so ADR 0023's step had nothing to do; in this
one it rebuilt them, with no kextd, and they were current after.

## Rejected

- **A kextd restart after the install:** kextd sends every kext's
  personalities at every start, the same hazard.
- **Stopping kextd (`kill -STOP`) for the install:** PackageKit's
  `kextcache -update-volume /` asks kextd for the volume lock and would
  wait on a stopped kextd for good.
- **Keeping the caches current by touching them to the folder's mtime + 1
  during the install:** kextd would then trust caches that describe
  10.6.0's kexts while it loads 10.6.8's from the disk; a lie to kextd, not
  a fix.
- **The combo installed onto the target before its first boot** (from the
  installer's environment, as InstaDMG-style image builders do), where no
  10.6.0 kernel runs under 10.6.8's kexts: the root of the hazard, but a
  rebuild of how 10.6 updates install, unmeasured; for the owner.

## What would change this

- kextd "Rescanning kernel extensions" under 10.6.0's kernel (`uname -r`
  10.0.0) on a build with this: something else sent it SIGHUP, or it ran
  despite the unload.
- A 10.6 update that installs a kernel and needs kextd during its
  install (a kext its scripts load): it would fail with kextd away, and its
  log would show it.
- 10.9: not examined. Its kextd is another (kext_tools 3xx) and its one
  update installs onto 10.9.5's own kernel family.
