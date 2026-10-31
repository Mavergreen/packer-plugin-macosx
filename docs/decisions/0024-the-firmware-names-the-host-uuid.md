# 0024 — The host UUID comes from what runs the box

Date: 2026-10-17
Status: proposed, on measurement

## Decision

Distinct machines have distinct host UUIDs (the owner, 2026-10-17), and a
guest has its host UUID before its first daemon asks for one.

1. **Both `config.plist`s: `PlatformInfo > Generic > SystemUUID` = `OEM`.**
   OpenCore takes the UUID the firmware holds (a `system-id` NVRAM variable
   if it keeps one, as a real Mac does, else the SMBIOS Type 1 UUID) and
   makes it `system-id`, from which the kernel derives `IOPlatformUUID` at
   boot. That UUID is whatever runs the box gives: QEMU's `-uuid`, libvirt's
   domain UUID, VMware's, VirtualBox's, a real Mac's. OpenCore takes none
   with four or more 0x00 or 0xFF bytes: QEMU's without `-uuid` (all zeroes),
   and some PC boards' placeholders.
2. **No `platform-uuid` in the config.** A firmware default would be one
   constant for every machine whose firmware gives no usable UUID, whatever
   its NIC; the guest itself derives one from its MAC there
   (`TestConfigPlistNamesNoPlatformUUIDOfItsOwn`).
3. **Every launcher this project owns gives QEMU a `-uuid` of its own**:
   - `bin/gate-vm.sh boot`: a fresh one per boot (`new_uuid`,
     `lib/common.sh`), kept in the state dir and logged, and the NIC's
     address derived from it (`derived_mac`, the rule below);
   - the three Packer templates: `["-uuid", local.build_uuid]`, a `uuidv4()`
     Packer evaluates once per build, and the NIC's address derived from it
     by the same rule (`local.build_mac`);
   - both box Vagrantfiles: one per Vagrant machine, made the first time it
     comes up and kept in its data dir
     (`.vagrant/machines/<name>/qemu/guest-macosx-host-uuid`), the same at
     every later up, halt and reload; a NIC address derived from it too,
     `52:54:00:xx:xx:xx` (locally administered, as QEMU's own), on the first
     NIC alone (vagrant-qemu puts `net_device` into that NIC's `-device`
     only), and never in `52:54:00:12:34:xx`, the range QEMU gives a NIC
     with no address of its own: a second NIC a project adds without one
     gets QEMU's `52:54:00:12:34:56`, never ours. An address a project gave
     the NIC, and vagrant-qemu's dual-NIC mode (`advanced_network` with a
     private network), which gives each of its NICs an address of its own
     from the machine's id, are left alone. A project's own `-uuid` is the
     machine's: nothing is stored, and the address derives from it. The
     trigger is named (`guest-macosx host UUID`) and says which of these it
     did: `host UUID <u> (new | kept in <store> | the project's own -uuid);
     first NIC: <mac>, derived from the UUID | the project's own address |
     vagrant-qemu's own dual-NIC addresses`. A `vagrant destroy` that
     happens forgets the UUID by itself: vagrant-qemu sets `machine.id =
     nil`, and Vagrant empties the data dir. Nothing in the box deletes it,
     since an after-destroy trigger runs on a declined destroy too (the
     action trigger wraps the confirmation), and made a live machine a new
     host (fix round 1's design; MEASURED with Vagrant 2.4.9, fix round 2).
     Under another provider the provider names its machine.

   The address rule, one in all three places: `52:54:00` (locally
   administered, QEMU's own prefix), then three bytes of the UUID's
   SHA-256, skipping six hex digits past each leading `1234`, so never
   `52:54:00:12:34:xx`, the range QEMU gives a NIC with no address of its
   own. 24 bits: two machines' addresses collide with a chance of about
   n²/2^25 among n machines, distinct in practice, not by construction. A
   copied project directory copies `.vagrant/`, and so the machine's UUID
   and address: it is a clone (vagrant-qemu shares the copy's pidfile and
   socket paths too, so running both is unsupported).
4. **The build's `ByHost` settings hold under any host UUID.** A `ByHost`
   preference lives in a file named for the host, and a box's host is never
   the build's. firstboot records each `ByHost` setting it makes (today one:
   the user's screensaver `idleTime 0`) and installs `guest-macosx-byhost`,
   run at every boot by the `guest-macosx.byhost` LaunchDaemon, both
   releases: it waits for the host UUID (at most 120 s), logs where it came
   from (`host UUID <u>, from the firmware's system-id`, or `made by the
   guest from its NIC's MAC after N s`), and adds each recorded setting this
   host's file lacks; a setting the file already has, the build's or the
   user's, it leaves alone, and says so. `verify.sh`, both releases, fails a
   build without it.
5. **The boot gate checks it**: `gate/guest-checks.sh host-uuid`, a step of
   its own after `reach`, on the cold boot only (a restart keeps the NVRAM
   its boot wrote). At least 45 s into the boot (bounded), it fails if this
   boot's `system.log` holds CoreFoundation's "unable to determine UUID for
   host", or if the guest has no `system-id` (the gate passed a `-uuid`, so
   the firmware dropped it); this boot is the newest `BOOT_TIME` within 10 s
   of `kern.boottime`, as `bin/gate-vm.sh` allows, since xnu adds every clock
   step to the boot time.

A launcher that passes no `-uuid` (a user's own QEMU line) gets the guest's
own behavior, as on a Mac with no firmware UUID: the UUID comes from the
NIC's MAC once configd has named the NIC, 10.9 waits 30 s for it at every
cold boot, and that boot's root `ByHost` preferences are keyed by zeros.

## Why (F3, F8, F19: one cause)

Every cold boot of a box is the first boot of an empty variable store: the
box ships OVMF's pristine `OVMF_VARS.fd`, and the gate and the Vagrantfile
attach it `snapshot=on`. The config's `SystemUUID` was the sample's all-zero
placeholder, which OpenCore skips (`IsZeroGuid`, `OpenCorePlatform.c`), so
no `system-id` either. The kernel publishes `IOPlatformUUID` in
`IOPlatformExpert::registerNVRAMController` from `/efi/platform`'s
`system-id`, else from `/options`' `platform-uuid`, else not at all
(read in 10.9's `mach_kernel`). So on that boot the guest had no host UUID.

Mac OS X then makes one in userspace: configd's InterfaceNamer, once it has
named the built-in interfaces, sets `IOPlatformUUID` to
`00000000-0000-1000-8000-<MAC>`, the lowest built-in Ethernet MAC (Wi-Fi's
where there is none; en0's on these boxes), through
`IOPlatformExpertDevice::setProperties`, which also writes NVRAM
`platform-uuid`. That is why the box's store, after one boot, holds about 25
variables and no later boot in the same QEMU process waits.

Until then, everything that asks for the host UUID waits:

- **10.9 (F3, F8).** CoreFoundation's
  `_CFXPreferencesGetByHostIdentifierString` calls `gethostuuid()` once per
  process with a 30 s timeout (the `{30, 0}` in CoreFoundation's data).
  cfprefsd asks about 3 s into the boot and waits the whole 30 s, then logs
  "unable to determine UUID for host... Giving up and returning 00000000-…",
  and keys the boot's root `ByHost` preferences by zeros (F8). While it
  waits, every preferences request it serves waits too. Sampled 8 s into a
  cold boot: cfprefsd's threads in that `dispatch_once`, one in
  `__gethostuuid`; securityd's main thread in `MDS_Install` →
  `CFBundleCopyResourceURLsOfType` → `CFPreferencesCopyAppValue` (the
  language list) → cfprefsd; configd's plugin loader in `EAPOLController`'s
  `load` → the same call; kextd's main thread in `DASessionCreate`, and 12 s
  later in `IOCatalogueSendData`. The kernel prints "Waiting for DSMOS..."
  when an Apple-encrypted binary first faults, about 7 s in, and "DSMOS has
  arrived" (the prelinked `Dont Steal Mac OS X.kext` registering its page
  hook) came 0-2 s after cfprefsd gave up, in every cold boot measured:
  26-28 s of waiting on an idle host (F3). Which kernel wait links kextd's message to
  DSMOS's start was not traced; the timing is exact and the cause, below,
  removes both.
- **10.6 (F19).** No cfprefsd: each process's CoreFoundation asks for
  itself, and `_CFGetHostUUIDString` logs "unable to determine UUID for host.
  Error: 35" (`EWOULDBLOCK`, the kernel's timeout) from configd,
  diskarbitrationd, DirectoryService, mDNSResponder, securityd and kextd
  about 2 s into every cold boot. 10.6's CoreFoundation passes a zero
  timeout, which 10.6's kernel treats as "do not wait" (10.9's as "wait
  forever"), and caches nothing on failure, so 10.6 loses no time: those
  daemons simply go without a host UUID for their first lookups.

## The measurements

### Before, and the first fix (2026-10-17, 10.9 gate base box built at de09ce1)

The first fix (33d3ad1) also put a default `platform-uuid` in the config;
fix round 1 took it out (Decision 2). These runs found the cause and
measured both of its parts; the `platform-uuid` rows show the kernel's
order, not today's config. Each cold boot: a fresh QEMU, a fresh disk overlay of the box, the store as
named. "Waited" is "Waiting for DSMOS..." to "DSMOS has arrived".

| Variable store and OpenCore | Cold boots | Waited | cfprefsd gave up | SSH |
|---|---|---|---|---|
| the box's empty store, as shipped (`snapshot=on`) | 6 | 6 (26-28 s) | 6 | 60-68 s |
| a copy a first boot wrote | 3 | 0 | 0 | 30-32 s |
| that copy, with only `platform-uuid` taken out | 3 | 3 (27-28 s) | 3 | 61-66 s |
| empty, OpenCore adding only `platform-uuid` | 3 | 0 | 0 | 22-30 s |
| empty, `SystemUUID` `OEM` only, no `-uuid` | 2 | 2 (26-27 s) | 2 | 61-66 s |
| empty, `SystemUUID` `OEM` only, `-uuid` | 5 | 0 | 0 | 30-31 s |
| empty, this decision, no `-uuid` | 3 | 0 | 0 | 30-31 s |
| empty, this decision, `-uuid` | 3 | 0 | 0 | 30-32 s |

The rates before are deterministic (6 of 6 as shipped, 11 of 11 whenever the
kernel had no host UUID at boot) and after 0 of 14; with N that size the
point is the switch, not a rate. With `-uuid`, `IOPlatformUUID` is the
kernel's SHA-1 name hash of the `-uuid` (recomputed by hand for two), a
different one per VM, and the same on the warm restart after; the default
`platform-uuid` is in NVRAM and unused. Without `-uuid` it is
`00000000-0000-1000-8000-525400123456`, as on every warm boot before, and the
cold boot writes no `….00000000-0000-0000-0000-000000000000.plist` (it did,
for `AppleIDAuthAgent`, every time before: F7's K2).

And 10.6.8 (a box built at ace862e, the same way, its own device set):

| Variable store and OpenCore | Cold boots | Boots with "Error: 35" | Lines per boot | SSH |
|---|---|---|---|---|
| the box's empty store, as shipped | 3 | 3 | 6 | 22 s |
| empty, this decision, no `-uuid` | 3 | 0 | 0 | 21-38 s |
| empty, this decision, `-uuid` | 3 | 0 | 0 (`system-id` set) | 23-52 s |

The warm restart after each: 0 lines in all 9. The spread in SSH times
after is the host: someone's CPU burners started during the fix's second
boot and ran through the rest. 10.6 never waited for the UUID.

### Fix round 1 (each machine its own UUID)

A 10.9.5 box built from this decision (cf04fca; the build's QEMU had its
own `-uuid`, `verify: ok`), each boot a fresh QEMU and a fresh disk overlay
(`fnv-report.md`, "Fix round 1"):

| Launcher | Cold boots | Gave up | DSMOS wait | `IOPlatformUUID` | `idleTime` CF reads | SSH |
|---|---|---|---|---|---|---|
| `bin/gate-vm.sh` | 2 | 0 | none | a different one each, from `system-id` | 0 (job: "set") | 21-30 s |
| Vagrant 2.4.9, machine A, up, halt, up | 2 | 0 | none | the same both times, and the same NIC address | 0 ("set", then "already set") | 23-35 s (`vagrant up`) |
| Vagrant, machine B | 1 | 0 | none | its own, and its own NIC address | 0 ("set") | 34 s |
| QEMU with no `-uuid` | 1 | 1 | 27 s | `00000000-0000-1000-8000-525400123456`, the guest's own, 33 s in | 0 (job: "made by the guest ... after 33 s", MAC-named file) | 66 s |
| a box built before it, `bin/gate-vm.sh` | 1 | 0 | none | from `system-id` | does not exist | 31 s |

The gate's `host-uuid` check passed the three `bin/gate-vm.sh` boots (each
with a `-uuid`) and failed the one without, naming cfprefsd's give-up. Every QEMU
the two Vagrant machines started carried `-uuid <the stored UUID>` and an
address of its own, then given as a `-global` default for the NIC model,
and en0 had that address and its DHCP lease. The address is now given to
the first NIC alone, through `net_device`: the same derivation, checked
with Vagrant 2.4.9, vagrant-qemu 0.6.3 and a stand-in QEMU (`-device
e1000-82545em,mac=52:54:00:..,netdev=net0`, the same over two starts; a
project's second NIC `e1000-82545em,netdev=n2`, QEMU's default), not yet
booted in a guest. `vagrant destroy` left the machine's data dir empty.

Fix round 2 (Vagrant 2.4.9, vagrant-qemu 0.6.3, a stand-in QEMU; no guest
booted): with no after-destroy trigger, a declined `vagrant destroy` (answered
`n` on a TTY) kept the stored UUID, and the next `up` passed the same `-uuid`
and `mac=`; `vagrant destroy -f` left the data dir empty, and the next `up`
made a new UUID and address. Each `up` printed `Running trigger: guest-macosx
host UUID...` and `host UUID <u> (new, kept in <store>); first NIC: <mac>,
derived from the UUID`, or `(kept in <store>)`. Gate boots and builds now take
the derived address too; that, and the job's new session line, wait for the
next real-guest run.

And a 10.6.8 box built from it (its build's QEMU had its own `-uuid`,
`verify: ok`, 16 receipts):

| Launcher | Cold boots | "Error: 35" lines | `IOPlatformUUID` | `idleTime` CF reads |
|---|---|---|---|---|
| Vagrant, machine A, up, halt, up | 2 | 0 | the same both times, from `system-id`, and the same NIC address | 0 ("set", then "already set") |
| Vagrant, machine B | 1 | 0 | its own, and its own NIC address | 0 ("set") |
| QEMU with no `-uuid` | 1 | 6 (configd, DirectoryService, diskarbitrationd, kextd, mDNSResponder, securityd) | `00000000-0000-1000-8000-525400123456`, the guest's own | 0 (MAC-named file) |
| a box built before it, with a `-uuid` | 1 | 0 | from `system-id` | does not exist |

`vagrant up` 25-26 s, `vagrant halt` 7 s; none of the three Vagrant boots
lost SSH.

## What it means

- **Boxes already built** carry the old `opencore.img`, and the old
  Vagrantfile and image: the 26-28 s wait and the zeros on every cold boot
  (or, for boxes built from 33d3ad1 to fix round 1, QEMU's default-MAC UUID
  shared by every machine), until rebuilt.
- **Every box under this project's launchers has a host UUID of its own**
  from the first second of its boot: per build, per gate boot, per Vagrant
  machine (stable across its halts and reloads, new after a destroy). Two
  machines no longer share one, and nor do their NIC addresses: every gate
  boot, build and Vagrant machine has had QEMU's one default before.
- **Under another hypervisor that gives a UUID**, the guest's is that
  machine's own. Before, it was derived from the MAC.
- **A launcher that gives no usable UUID** (QEMU without `-uuid`, a firmware
  whose SMBIOS UUID OpenCore refuses) gets the guest's own MAC-derived UUID,
  as before this decision: distinct wherever the MAC is, late, and on 10.9 a
  30 s wait at every cold boot.
- **The build's `ByHost` settings** reach each machine through the boot job,
  which writes this host's file at its first boot. `AppleIDAuthAgent` still
  writes its own record on a machine's first boot (it is the system's, not
  the build's).

## Alternatives

- **A default `platform-uuid` in the config** (the first fix): one UUID for
  every machine whose firmware gives none, whatever its NIC. Rejected by the
  owner: distinct machines must have distinct UUIDs.
- **Ship the variable store a build wrote.** The same shared UUID, plus the
  build VM's boot entries, and it changes the one provenance row expected to
  match OVMF's own (`docs/decisions/0004`).
- **A fixed `SystemUUID`.** Every box on every hypervisor and Mac would share
  it.
- **A per-machine UUID derived from the Vagrant machine's id** instead of a
  stored one: the id exists only after the first `up` has imported the box,
  after the trigger that needs it.
- **The build's `ByHost` settings written for every possible host**, or as
  any-host preferences: the host is not known until the machine boots, and
  the build wrote this one `-currentHost` because that is where the
  screensaver's own settings live; whether it would read an any-host
  `idleTime` was not tested.

## Limits

- The kernel-side wait between kextd and DSMOS's start is not traced.
- The gate's check reads `system.log`; a boot whose `BOOT_TIME` line is in
  neither `system.log` nor `system.log.0.bz2` fails it, said so.
- The `ByHost` job writes this host's file at boot, directly, as root
  (never through a symlink, a missing folder made the user's). A user whose
  session already runs (its `cfprefsd` or `loginwindow`: a login in the
  first seconds of a boot whose UUID came late, or an opt-in auto-login)
  sees the setting from its next read of the domain, and the job says so
  in `system.log`. If that session then writes the domain from what it had
  read, the key is lost again until the next boot's job adds it back. Not
  observed.
