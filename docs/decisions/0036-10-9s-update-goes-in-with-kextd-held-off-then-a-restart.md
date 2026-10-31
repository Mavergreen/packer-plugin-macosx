# 0036 — 10.9's update goes in with kextd held off, then a restart into it

Date: 2026-10-28
Status: accepted (F29; owner approved the hold/restart sequence after the 2026-10-29 continuation review)

## Decision

On 10.9, `firstboot.sh` installs Apple's updates first, as before
(2016-004 must go in before the family's OpenSSH), and now treats an
update that installs a kernel the way 10.6 does (`docs/decisions/0026`):

1. **kextd held off while it goes in.** `install_updates` calls
   `hold_kextd_for` before each update's `installer`: where the update's
   `Bom` lists `./mach_kernel` (`update_installs_kernel`; 2016-004 does,
   as `guest-macosx-update-01-SecUpd2016-004Mavericks.pkg`), kextd's job is
   unloaded and stays unloaded until the restart. Every other update leaves
   kextd running, and the log says which.
2. **The first boot ends in a restart into it.** When an update installed
   a kernel (kextd held off), or asks for a restart (its `PackageInfo`'s
   `postinstall-action="restart"`, `update_wants_restart`; 2016-004 does
   both), `install_updates` records why, and:
   - `remote_login_on` writes launchd's `com.openssh.sshd` override only, so
     sshd is on from the next boot and starts on none before: no SSH session,
     the build's included, meets the restart ("remote login: on from the
     next boot, after the restart into the updates (…)");
   - `restart_into_updates`, at the very end of the first boot (marker
     written, LaunchDaemon gone), makes the prelinked kernel current without
     kextd where kextd's own test finds it stale (`kernelcache_current`),
     then restarts through `/usr/local/sbin/guest-macosx-shutdown -r`,
     which every 10.9 shutdown takes (`docs/decisions/0019`): "restarting
     into the updated system: … installed a kernel, and kextd stays off
     until that kernel runs". A restart that does not happen is logged; the
     guest then stays on that boot with no SSH, and the build times out.
3. **Inert elsewhere.** `updates = none`, an update with no kernel that
   asks for no restart, and 10.6 (whose updates take `snow_leopard_updates`)
   are untouched: Remote Login comes on at once, as before, and nothing
   restarts.
4. **The guard.** `templates/mavericks/verify.sh` prints `kextd-hold=` and
   `restart-into-updates=`, and fails the build when the first boot's log
   says "kextd: COULD NOT unload", or, with `updates = security` or `all`,
   never says "kextd: held off until the restart:" or "restarting into the
   updated system:".

The build's provisioners and its last shutdown then run on a boot of the
updated system, under the update's kernel, which is the state every box
starts from anyway.

## Context: F29

One 10.9.5 build's last shutdown waited 61 s (usb round 2b): `shutdown`
said "/ is busy updating; waiting for lock" and kextd "'/' updating,
delaying reboot". kextd's reboot lock (`_kextmanager_lock_reboot`,
kext_tools-326) answers EBUSY while a volume it watches is locked by a
`kextcache`, or while its caches are stale, and `shutdown(8)` waits for it.
MEASURED, unless marked (10.9.5 builds at HEAD a7b42be, one VM, host load
1.2-2.4 on 12 CPUs; a process sampler from the first boot's first seconds,
1 s apart, and the guest's logs read offline from each image):

**Who runs a `kextcache` on the first boot.** Two, in 8 of 8 first boots:
- the installer's own, `PackageKit: kextcache -update-volume / -Installer`,
  some 6 s after 2016-004's payload lands ("Shoving"), 74-80 s into the
  boot;
- kextd's, `/usr/sbin/kextcache -F -system-prelinked-kernel` (its parent
  kextd), started 72-78 s into the boot. That is the kernel's request: once
  per boot, 60 s after the IOKit registry last went quiet (`IOService.cpp`
  calls `OSKext::considerUnloads()` on every registry quiet;
  `considerRebuildOfPrelinkedKernel`, xnu-2422), within its first 300 s,
  when a loaded kext is not in the prelinked kernel, the kernel asks kextd
  for one, and kextd runs that command, which rebuilds the prelinked kernel
  unconditionally (`createPrelinkedKernel`, kext_tools-326; READ). Offline
  (`docs/decisions/0034`) one such build took 20-255 s here.

**Three orderings of the two**, set by a race of a few seconds between the
kernel's request and the update's payload (mav1-mav4 are builds, t01-t03
and i01 replays of mav4's first boot from its image as the OS installer
left it, with the build's own argv and provisioners):

| first boot (N) | kextd's `kextcache` | the installer's | the shutdown |
|---|---|---|---|
| request before the payload lands (7: mav2, mav3, t01-t03, and both 2026-10-27 `mavfix` builds, read from their logs) | runs 109-258 s holding the volume, then fails: "Source item /mach_kernel has changed since starting; not saving cache file" (exit 4) | waits for the volume ("/ locked; waiting for lock"), then builds it | not delayed |
| request after the payload lands, before the installer takes the volume (2: mav1, mav4) | builds it, 20-109 s | finds it current | not delayed |
| request after the installer took the volume (1, induced: i01) | waits for the installer's build, then builds the same kernel again, 208 s, into the provisioners | builds it | **waited**: "'/' updating, delaying reboot" at the halt, until kextd's build ended |

The third is F29. It was induced by hot-plugging a USB keyboard 50 s into
the first boot (and unplugging it 3 s later): the registry went busy and
quiet, and the kernel's request came 60 s after the unplug (112 s into the
boot, against 72-78 s in every boot not induced), after the installer had
taken the volume. Its halt came as that build finished, so it waited 3 s;
round 2b's, earlier in it, 61 s. That round 2b's first boot had a late
registry quiet of its own is REASONED (its guest's logs were not kept); its
log lines are the induced run's, line for line. Natural rate: 0 of 9 first
boots here (7 with the sampler, 2 read from their logs); with the four
before it (usb rounds 2, 2b, 2c), 1 of 13.

The first two orderings cost as well: in the first, kextd's discarded
build held the volume 109-258 s before the installer could start its own,
and the installer-reboot-to-SSH time was 462 and 580 s (mav2, mav3) against
124 and 215 s in the second ordering (mav1, mav4).

**Why the build met it at all.** 2016-004 installs a kernel and asks for a
restart. Taken on the boot that installed it, the build ran its
provisioners and its last shutdown under 10.9.5's own kernel, whose one
request for a prelinked kernel could come at any time in its first five
minutes. 10.6's first boot already restarts into each update that installs
a kernel, with kextd held off (`docs/decisions/0026`); 10.9's did not
(`firstboot.sh`: "A RESTART IS WANTED AND IS NOT TAKEN HERE").

## Measured with it

10.9.5 builds from HEAD plus this change (and the probe), same host and
sampler, host load 1.4-2.4:

| | stock (N 4 builds + 3 replays) | with this (N 3) |
|---|---|---|
| kextd's `kextcache` on the first boot | 7 of 7 (and 1 induced) | **0 of 3** (kextd exits at the unload, "Exited with code: 2", 11 s into the boot) |
| any `kextcache` on the build's last boot | — (no second boot) | **0 of 3** |
| the prelinked kernel at the restart | — | current by kextd's test, 3 of 3 (the installer's build stamps it); fix3 ran `kernelcache_current` and logged "prelinked kernel: up to date" (fix1 and fix2 ran an earlier draft that applied 10.6's cache test, which 10.9's caches never pass; see Limits) |
| "delaying reboot" at the build's halt | 0 of 7 natural, 1 of 1 induced (and round 2b's) | **0 of 3** |
| installer reboot to SSH | 124, 462, 580, 215 s | 393, 413, 443 s |
| SSH to halted (the provisioners and the halt) | 172-179 s | 185, 196, 182 s |
| `verify.sh` | ok | ok, `kextd-hold=held off for an update that installs a kernel`, `restart-into-updates=yes` |

The restart costs about 27-31 s (11 s down, 16-20 s to SSH). The first
boot's `kextcache` is now always the installer's, offline, about 230 s; the
time the second ordering saved is gone with the time the first wasted. At
N 3 against N 4 no difference in the build's length is claimed.

### Fresh integrated validation after approval (2026-10-29)

The default dual-release gate passed on KVM with freshly built boxes, 10.9
then 10.6, N=1 restart each: 10.9.5/13F1911 with Aqua Transport 2026.10.25
and TLS reach before/after restart; 10.6.8/10K549 with its documented layer
and TLS `n/a` results. No retry or bound change. Main's 804/804 Bats tests,
suite lint, and Go vet/tests passed as well.

The fresh 10.9 base's first-boot log records the hold at 18:30:59Z and the
**final prelinked-kernel checker** finding the cache current and requesting
the restart at 18:36:20Z; the provisioner verified both hold and restart.
Its retained system log has no "delaying reboot" line. This run had no
process sampler, so it does **not** extend the process-count table above
or establish a new race rate. Base/layer builds took 1694/337 s; no controlled
build-speed comparison is claimed.

## Alternatives

- **Wait for kextd's caches before the build's halt.** The shutdown
  already waits for them; that wait is F29. Moving it earlier does not
  shorten it, and the request can come after any such wait.
- **Hold kextd off without the restart**, loading it again after the
  update. The kernel's request, made while kextd is away, waits for kextd's
  next start (kextd reads the kernel's pending requests as it starts), so
  the same rebuild runs then, into the provisioners. REASONED from
  `kextd_request.c`; not measured.
- **Kill a redundant `kextcache -system-prelinked-kernel`.** It would need
  to know the cache it is rebuilding is current, and it races Apple's own
  bookkeeping (kextd counts a killed child as an error). Rejected.
- **A build-only revocation setting**, so each offline `kextcache` is fast
  (`docs/decisions/0034`, R45): declined there; it would shorten F29's
  wait, not remove it.

## Limits

- The natural third ordering was not caught with the sampler running; the
  mechanism rests on the induced run, on round 2b's identical log lines, and
  on the source (READ: kext_tools-326's `kextd_watchvol.c` and
  `kextd_request.c`, xnu-2422.115.4's `OSKext.cpp` and `IOService.cpp`).
- That kextd's request waits for its next start when kextd is away, and so
  dies with the restart, is REASONED; what was measured is the outcome: no
  `kextcache` of kextd's on either boot, 3 of 3.
- 10.9's own kext caches (`KextIdentifiers`; `KextPropertyValues_OSBundleHelper`)
  are not 10.6's: `kext_caches_are_current`'s test is 10.6's alone, so 10.9
  judges its prelinked kernel instead (`kernelcache_is_current`, kextd's
  `check_kext_boot_cache_file`).

## What would change it

An update for 10.9 that installs no kernel and asks for no restart (inert,
by design); an offline `kextcache` that is fast; a build that needs SSH on
the first boot.
