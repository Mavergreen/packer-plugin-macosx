# 0020 — UserEventAgent's telemetry plugin is set aside

Date: 2026-10-14
Status: accepted, on measurement

## Decision

A 10.9 guest's `/System/Library/UserEventPlugins/com.apple.telemetry.plugin`
is moved, whole, to `/System/Library/UserEventPlugins-disabled/`:

| When | Where | Why there |
|---|---|---|
| at install time, on the target volume | `assets/guest/postinstall`, "THE TELEMETRY PLUGIN" | UserEventAgent starts before `firstboot.sh` does, so only the install can keep the first boot from loading it |
| again, after Apple's updates | `assets/guest/firstboot.sh`, "UserEventAgent's telemetry plugin, again" | Security Update 2016-004 carries the plugin (its Bom lists it) and puts it back (reasoned from the Bom, never observed); UserEventAgent, started without it, does not load it on that boot |

`templates/mavericks/verify.sh` fails a build whose guest still has the plugin
in `UserEventPlugins`, and one whose `postinstall.log` lacks "telemetry plugin
set aside:" (the first boot moves the plugin again, so the end state alone
would hide a failed install-time move). Snow Leopard is not known to have such
a plugin (reasoned, not checked); the install's directory check makes it a no-op there. To put it back: move
it into `UserEventPlugins` and restart.

## Context

`UserEventAgent (System)` crashed in 11 of 335 boots in the runs that saw it, a second or two
into the boot, and launchd restarted it. ReportCrash filed a `.crash`
report, and the boot gate fails on any report: with 11 boots a run, about 1
gate run in 3. Every crash had a `com.apple.telemetry` thread executing code
in an address range that was no longer IOUSBLib's, while the main thread was
in `dlopen` of `com.apple.time`, the last plugin it loads.

### What the crash is

Symbolicated against the guest's own binaries (offsets in the crash reports
against `IOUSBLib`'s symbol table and disassembly):

- **Variant A** (6 of the 9 reports): telemetry `+ 0x3d4f` →
  `IOCreatePlugInInterfaceForService` → `_CFPFactoryCreateInstance` →
  `IOUSBLibFactory`, whose call to `IOUSBDeviceClass::alloc` returns to
  `+ 0xd80`; `alloc` calls the `IOUSBIUnknown` constructor, which registers
  the instance (`CFPlugInAddInstanceForFactory`), and faults fetching its
  own return address, `+ 0x13cb`. The page is unmapped, or already holds
  `com.apple.time`.
- **Variant B** (3 of 9): telemetry `+ 0x4061` calls a USB device
  interface's `Release` (`IOUSBIUnknown::release`). The last reference
  deletes the object; its destructor unregisters the instance; the deleting
  destructor returns to `release + 0x1b` (`+ 0x1055`), which by then is
  `com.apple.time`'s code.

Neither plugin has `CFPlugInFactories` in its Info.plist: `com.apple.time`
is not involved at all. It is mapped into the hole IOUSBLib left, because it
is the next thing the main thread loads.

### Why IOUSBLib goes away under its own code

dtrace on `pid` probes in a reloaded `UserEventAgent (System)`, held at its
first plugin by a `SIGSTOP` from a second dtrace:

1. The main thread checks each plugin's code signature
   (`SecStaticCodeCheckValidityWithErrors`) before it `dlopen`s it. Freeing
   that check's `CFBundle` runs `__CFBundleDeallocate` →
   `CFBundleUnloadExecutable` → **`_CFBundleUnloadScheduledBundles`**, which
   unloads every bundle CoreFoundation has scheduled for unloading, in the
   whole process. So the main thread runs it once before every plugin,
   `com.apple.time` last.
2. `com.apple.telemetry`, loaded just before, walks every `IOUSBDevice` (7
   in this VM: 4 root hubs, the OpenCore disk, the keyboard and the mouse)
   on its own queue: `IOCreatePlugInInterfaceForService` (IOUSBLib, a
   CFPlugIn), a query, `IODestroyPlugInInterface`, the device interface's
   `Release`. The last `Release` leaves IOUSBLib no instances, so
   `_CFPlugInRemovePlugInInstance` calls
   **`_CFBundleScheduleForUnloading`** — from inside IOUSBLib's own
   destructor, before that code has returned. The next device's factory call
   runs IOUSBLib code before its constructor unschedules it. 7 such windows
   in about 4 ms, starting about 1 ms before the main thread's last
   `_CFBundleUnloadScheduledBundles`.
3. When that call lands in a window, it `dlclose`s IOUSBLib, and dyld
   unmaps it, under the telemetry thread. **Caught**: with the telemetry
   thread held 30 ms (dtrace `chill`) right after scheduling, the crash
   came with the main thread at `_CFBundleUnloadScheduledBundles` →
   `CFBundleUnloadExecutable` → `dlclose` → `garbageCollectImages` →
   `ImageLoaderMachO::UnmapSegments` → `munmap`, and the telemetry thread at
   `+ 0x4061` → `IOUSBLib + 0x1055` → an `IOUSBLib` stub whose lazy pointer
   page was gone: the natural crash's variant B, frame for frame.

That is Apple's bug, in CoreFoundation's deferred plugin unloading: it
schedules a plugin for unloading from the plugin's own code, and lets any
thread carry the schedule out. Nothing in our image causes it. The
telemetry plugin is the only code in `UserEventAgent (System)` that creates
and destroys CFPlugIn instances while the main thread is still loading
plugins; it feeds `systemstatsd`'s power and usage statistics (its only
consumer: `com.apple.systemstatsd.plist`'s `LaunchEvents`), which nothing in
a CI guest reads.

**Amended by ADR 0022 (2026-10-15).** "Nothing in a CI guest reads" the
statistics, but `systemstats --daily` still asks for them, at 00:15 local
time. Without the plugin `systemstatsd` cannot answer, stays dirty for the
rest of the boot, and holds every later shutdown 20 s. The install now sets
its three jobs aside with the plugin.

## Measurements

All 2026-10-14 on `pet-power-plant` (QEMU 8.2.2, KVM), one guest at a time,
through `bin/gate-vm.sh`'s QEMU command line (2 vCPUs), boxes derived from the
boot gate's base and layer boxes with the plugin set aside (fix)
or not (stock), a fresh overlay per round of a cold boot and 9 restarts at
the gate's pace. A crash is a `UserEventAgent_*.crash` in the guest after a
boot.

**At its natural rate, this crash is too rare under some conditions to measure a fix
by.** The earlier boot-gate runs of 2026-10-13/14 saw 11 in 335 boots (9 reports,
2 respawns), 8 of them
with a dtrace probe running from boot and 3 untraced, on the then-new
freshly built boxes between 06:35 and 06:46. These runs saw none:

| Box | Condition | Stock | Fix |
|---|---|---|---|
| gate layer and base | gate pace | 0 / 100 | 0 / 100 |
| earlier gate layer | gate pace | 0 / 40 | |
| gate layer | both vCPUs pinned to one host core | 0 / 16 | |
| gate layer | 12 host cores busy | 0 / 20 | |
| gate layer | the earlier runs' boot-time dtrace probe, 5 s or 1 s polling | 0 / 64 | 0 / 10 |

0 of 240 against the earlier runs' 11 of 335 (Fisher one-sided p = 0.002): the rate
depends on conditions these runs did not reproduce, so no after-rate from
them can be claimed against it (0 of 110 after: p = 0.04 against the earlier
runs' counts, 0.14 against all). What the fix rests on is the mechanism: with the
plugin aside, `UserEventAgent (System)` never loads IOUSBLib (`lsof` shows neither plugin in
it; 12 traced reloads saw no scheduled unload and no `dlclose`), so no thread is ever in the window.

**Induced** (dtrace: main thread's check of `com.apple.time` delayed 15 ms,
telemetry held 30 ms after each scheduling; reload of the job):

| Box | Runs | Runs that crashed | Scheduled-unload holds |
|---|---|---|---|
| base, stock | 18 | 3 (4 reports; this crash's frames) | 7 a run |
| base, fix | 12 | 0 | 0: nothing is ever scheduled |


## Alternatives considered

- **Fewer USB devices.** Fewer windows, not none: the 4 root hubs are
  `IOUSBDevice`s too, and the keyboard and mouse are what make the guest
  usable.
- **Keep IOUSBLib loaded.** CoreFoundation schedules a plugin for unloading
  only when it is "load on demand", which is the default unless its
  Info.plist asks for dynamic registration. Editing
  `IOUSBFamily.kext`'s plugin's Info.plist would break the kext's seal, for
  every process.
- **Allowlist the report, or retry the gate.** Hides a crash the owner
  wants gone (the standing rule).
- **Delete the plugin.** Moving it aside leaves the image restorable.

## What would change this

A crash report from `UserEventAgent (System)` on a box built with this
decision; a reason to want `systemstatsd`'s statistics in a guest; any other
plugin creating CFPlugIn instances on a thread of its own at load.
