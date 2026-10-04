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
   the end of first boot, and the guest restarts before sshd first
   starts. No `all`: no 10.6 application updates are chosen.
7. **USB on XHCI, no USB keyboard or mouse.** OpenCore's disk is a
   `usb-storage` device on `qemu-xhci`. 10.6 has no XHCI driver, so it
   never touches that controller; it does have a UHCI driver, and that
   driver hangs the guest. The template's window therefore shows the
   screen but takes no input. Work over SSH.
8. **One CPU by default.** With two, 10.6.0 under KVM panics when the
   host is busy (Measurements). `cpus` stays a variable.

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
  turns on auto-login and Remote Login, and answers SSH.
- The guest: 10A432, the `RELEASE_X86_64` kernel (KernelArch `Auto`),
  sudo 1.7.0, OpenSSH 5.2p1 with only `ssh-rsa` and `ssh-dss` host keys,
  X11 installed, Rosetta and QuickTime Player 7 not.
- With QEMU's UHCI controllers (q35's default USB companions, needed
  for a `usb-kbd` and `usb-mouse`), installs wedged at random, at one CPU
  or two: CPU 0 spinning with interrupts off on `in` from port `0x60e2`,
  the first UHCI controller's status register, which read `0x20`
  (halted). That is 10.6.0's `AppleUSBUHCI` polling a halted controller.
  With OpenCore on `qemu-xhci` and no UHCI, none wedged.
- With two CPUs: under host load, spinlock and `pmap_flush_tlbs`
  timeout panics; pinned to cores, none. With one CPU, none.
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
  and the box booted with two once it is 10.6.8.
- A USB keyboard and mouse 10.6.0 can drive without UHCI, for the window.
