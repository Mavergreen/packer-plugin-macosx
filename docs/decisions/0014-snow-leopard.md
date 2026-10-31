# 0014 — Mac OS X 10.6 Snow Leopard, from the user's own disc

Date: 2026-10-04
Status: accepted, on measurement

## Decision

The plugin builds 10.6 guests the way it builds 10.9 ones, with three
data sources of their own (`snowleopard-installer`, `snowleopard-firmware`,
`snowleopard-media`; `macosx-snowleopard-*` in a template) and a template
of its own (`templates/snowleopard/`). What differs, and why:

1. **The installer is the user's.** Apple serves 10.9 free through
   osrecovery; 10.6 was a paid DVD with no Apple download. The plugin
   ships no URL for a 10.6 image and its docs name no source; the user
   passes `installer`, a path to their own image.
2. **The disc is verified by its contents, not its bytes.** Any faithful
   copy of a retail disc -- an ISO, a Disk Utility master, a `.dmg`, the
   volume alone -- passes, because `internal/disc` reads the HFS+ volume
   out of whatever made the image and holds every installer package on it
   to `assets/pins/snowleopard-packages.sha256`. One disc is known:
   **10A432**, the 10.6.0 retail DVD. Any other build is refused by name
   until one is read and pinned the same way.
3. **The boot stack differs by one quirk and one model**
   (`assets/firmware/README.md`): `RebuildAppleMemoryMap` on, which 10.6.0's
   kernel needs under KVM, and `iMac9,1`, a Mac older than 10.6.0.
   `DevirtualiseMmio`, which the prior art also sets, stays off: with it,
   the installer's `bless` panicked writing NVRAM.
4. **The guest keeps Apple's OpenSSH 5.2.** The family's OpenSSH targets
   10.9; making one for 10.6 is its own spike, in `Mavergreen/openssh`.
   Packer's SSH client logs in to 5.2 as it is; `vagrant ssh`, which runs
   the host's OpenSSH, is told to accept `ssh-rsa` by the box's
   Vagrantfile.
5. **Passwordless sudo by `/etc/sudoers` itself.** 10.6's sudo is 1.7.0,
   older than `#includedir` (1.7.2), so `firstboot.sh` appends the rule to
   `/etc/sudoers` where sudo is that old, and writes a `sudoers.d` fragment
   where it is not.
6. **Updates: `none` or `security`, default `security`.** `security` is
   Apple's client 10.6.8 combo update and then Security Update 2013-004,
   the last for 10.6, all pinned from Apple's own catalogue. The combo is
   a catalogue product (041-98179): a distribution and 18 packages. The
   guest gets all 19 and runs `installer` once, on the distribution, which
   installs the packages in its own order and picks the Rosetta,
   QuickTime 7 and X11 ones by what the guest has. They go in last, at
   the end of first boot, each on a boot of its own (the combo, a
   restart, then 2013-004: ADR 0023), and the guest restarts before sshd
   first starts. No `all`: no 10.6 application updates are chosen.
7. **USB and OpenCore's disk: the same as 10.9's (amended 2026-10-27,
   `docs/decisions/0035`).** OpenCore's disk is a `virtio-blk-pci` disk in slot `0x10`,
   which OVMF boots and neither release has a driver for, and USB carries only a keyboard and a tablet, on EHCI alone at
   `0x1d.0`: no UHCI, whose 10.6.0 driver hung installs (Measurements),
   and no XHCI. That holds in the build and the box, on both releases, so
   this is no longer a difference. Until then OpenCore's disk was a
   `usb-storage` device on `qemu-xhci`, which 10.6 has no driver for, and
   the window took no input.
8. **One CPU by default.** With two, 10.6 under KVM panics when the host
   is busy (Measurements, and F26: 10.6.0 and 10.6.8 alike, after warm
   resets). 1 CPU ships as a documented deviation, cause not yet
   established; F26 is root-caused after the release (the owner's ruling
   R49, 2026-10-27). `cpus` stays a variable.
9. **The boot gate: both releases (added 2026-10-28, G8).** Until then
   `bin/gate.sh` booted 10.9 alone, so a change to the boot stack both
   releases share (OpenCore, Lilu, VirtualSMC, OVMF) was gated on one of
   them. Now a plain `bin/gate.sh --installer <disc>` gates both, 10.9
   and then 10.6.8, each built from the checkout, with the same steps and
   the same checks, each release's evidence and verdict its own and a
   combined verdict that passes only when both pass (the owner's ruling
   R52, 2026-10-28); without the disc it refuses, naming the disc and
   `--release 10.9` / `--release 10.6`, so 10.6 is never skipped in
   silence. This is no longer a difference. A box names its release on a line of
   its own Vagrantfile (`# guest-macosx-release: 10.6`, rendered by the
   template), and `bin/gate-vm.sh` reads it, and nothing else, for the
   three things it does differently on 10.6, each what the box's own
   Vagrantfile does:
   - its SSH client also accepts `ssh-rsa` (item 4);
   - restarts and shutdowns are a bare `sudo /sbin/shutdown -r|-h now`:
     10.6's images carry no `guest-macosx-shutdown`, whose causes 10.6
     does not have (`docs/decisions/0019`; its shutdowns took 0.4 s,
     MEASURED 2026-10-14);
   - one CPU (item 8).

   And the gate itself sets aside what 10.6 has nothing for, each step
   `n/a` in `steps.tsv` with its reason, never a pass:
   - `build-layer`: 10.6 has no Aqua Transport layer yet (the parity
     design's G5), so the gate boots the base box, and `versions` holds
     OpenCore, Lilu and VirtualSMC to their pins but has no Aqua
     Transport pin to hold;
   - `reach`, which proves that layer's TLS: without the layer, 10.6's
     system curl has none the three hosts accept (MEASURED 2026-10-29:
     the gate's `reach` check, run by hand on the fresh 10.6.8 box,
     failed all three with curl error 35, `tlsv1 alert protocol version`).

   `health` wants 10.6.8 (10K549), the `updates = security` build.
   `host-uuid` reads 10.6's own `system.log` form: its `bootlog` line is
   `BOOT_TIME: <n>`, about 6 s in, after the daemons that log "Error:
   35" (`docs/decisions/0024`), so its boot starts at launchd's
   "launchd[1] has started up" line before it (the form MEASURED in a
   10.6.8 box's log, F18, 2026-10-19). `health` also fails, on both
   releases, a `/var/log/launchd-pid1.crash`, as this template's
   `verify.sh` does: 10.6's launchd samples itself there when it crashes,
   then reboots (F16, MEASURED); 10.9's launchd (launchd-842.92.1) names
   the same file but writes nothing to it, that code compiled out (#if 0),
   so on 10.9 it is REASONED from the source never to appear, and checked
   anyway. `bin/gate-summary.sh` never compares two releases' runs. The checks that 10.9's first-boot
   fixes made pass (`docs/decisions/0020`, `0022`, `0031`) are the same
   checks on 10.6, which never needed those fixes.

   **MEASURED 2026-10-29**, `pet-power-plant`, KVM: fresh builds through
   `bin/gate.sh --release 10.6` and `--release 10.9`, one restart each,
   both exit 0. On 10.6.8 (10K549), all applicable steps pass, including
   the real `versions` and `host-uuid` parsers, and only `build-layer`
   and `reach` are `n/a`. On 10.9.5 (13F1911), the Aqua Transport
   2026.10.25 layer passes every step, TLS reach before and after the
   restart included. This measures the individual release paths, not a
   real combined-default invocation or a stress run.

## Measurements

All 2026-10-04 on `pet-power-plant` (i7-8700B, KVM), from the 10A432
disc (sha256 `cbeeb237...`, `docs/test-hosts.md`):

- Without `RebuildAppleMemoryMap`: the installer's 64-bit kernel stops
  after `mig_table_max_displ = 73`, with one CPU or two, and with
  `idlehalt=0`.
- With it and `DevirtualiseMmio`: the unattended install runs to its end,
  then `bless` panics in `AppleEFIRuntime`, and OpenCore finds no boot
  entry on the disk it leaves.
- With it alone: the install completes, blesses, reboots into 10.6, and
  the first-boot payload -- 10.9's, unchanged -- creates the account,
  turns on auto-login and Remote Login, and answers SSH. (Since
  2026-10-15 the payload leaves auto-login off unless the build sets a
  password, and the default build sets none; see
  `docs/configuration-register.md`, "auto-login".)
- The guest: 10A432, the `RELEASE_X86_64` kernel (KernelArch `Auto`),
  sudo 1.7.0, OpenSSH 5.2p1 with only `ssh-rsa` and `ssh-dss` host keys,
  X11 installed, Rosetta and QuickTime Player 7 not.
- With QEMU's UHCI controllers (q35's default USB companions, needed
  for a `usb-kbd` and `usb-mouse`), installs wedged at random, at one CPU
  or two: CPU 0 spinning with interrupts off on `in` from port `0x60e2`,
  the first UHCI controller's status register, which read `0x20`
  (halted). That is 10.6.0's `AppleUSBUHCI` polling a halted controller.
  With OpenCore on `qemu-xhci` and no UHCI, none wedged. (Added
  2026-10-27: these were 10.6.0's installer, some of them before the
  OVMF restart fix, ADR 0018. They say nothing of `AppleUSBEHCI`, which
  ADR 0035 measures in 10.6.0's installer and 10.6.8's boxes, or of
  10.6.8's `AppleUSBUHCI` 4.2.0; no shape since has UHCI.)
- With two CPUs: under host load, spinlock and `pmap_flush_tlbs`
  timeout panics; pinned to cores, none. With one CPU, none.
  Measured again 2026-10-11 with 0015's RTC patch, under host load: both
  builds at two CPUs panicked in 10.6.0, on CPU 1, one in the installer
  (`Spinlock acquisition timed out` from `pmap_enter`) and one at its
  first boot's login window (a double fault in `IOConfigThread`'s
  `doServiceMatch`). The 2026-10-06 build's 10.6.8 disk at two CPUs
  answered SSH on 19 boots of 20; at one CPU, on 20 of 20 (0015).
- The combo's packages installed one at a time on a running 10.6.0 break
  it: `SUBaseSystemCombo` brings a new `libSystem` while `dyld` stays
  10.6.0's, and every process after it crashes. The catalogue's other
  10.6.8 combo (041-98121, client and server) has the same packages.
- The client product installed by its own distribution, in one
  `installer -pkg MacOSXUpdCombo10.6.8.dist -target /` run with its 18
  packages beside it: 156 s, exit 0, `sw_vers` 10.6.8 (10K549); it
  reboots under this firmware; receipts for all 13 parts, the base
  system and X11 (the distribution chose X11, not Rosetta or QuickTime 7).
  Security Update 2013-004 then installs, and its receipt
  (`com.apple.pkg.update.security.10.6.8.10K1136.2013.004`) survives a
  reboot. 10.6's `installer` takes Apple's packages, whose certificates
  expired in 2019, without complaint.
- The same two at first boot with no restart after them: sshd answered,
  and every session hung (Packer's upload failed after 13 minutes). The
  combo replaces `libSystem`, `dyld` and `launchd` under the running
  system. An earlier hang at two CPUs, first put down to SMP, followed
  the same sequence.

## What would change this

- A second retail disc (10D573, the 10.6.3 one) read and pinned: a new
  section of `snowleopard-packages.sha256`, nothing else.
- `Mavergreen/openssh` building for 10.6: `snowleopard-media` gains
  `openssh` as 10.9's has it.
- `Macmini3,1` measured as the fallback model, or `iMac9,1` failing.
- Two CPUs measured clean on a busy host, or installs run on one CPU
  and the box booted with two once it is 10.6.8: F26's cause, found
  after the release (R49).
- A USB keyboard and mouse 10.6.0 can drive without UHCI, for the window:
  done, EHCI alone (`docs/decisions/0035`).
- A 10.6 Aqua Transport layer, or another TLS route (G5): the gate's
  `build-layer` and `reach` apply to 10.6 too, and `versions` gets its
  pin.
- `Mavergreen/openssh` on 10.6: the box's Vagrantfile and
  `bin/gate-vm.sh` stop accepting `ssh-rsa`.
