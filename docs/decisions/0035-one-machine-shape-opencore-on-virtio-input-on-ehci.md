# 0035 — one machine shape: OpenCore's disk on virtio-blk, USB for input alone

Date: 2026-10-27
Status: proposed (issue #9; the owner's rulings R47 and R48, 2026-10-27,
the ruling that measured OpenCore off USB first, and R50, 2026-10-27,
which moved it from AHCI to virtio-blk)

## Decision

1. **Every build and every box, on both releases, has the same devices**:

   ```
   -drive id=opencore,if=none,format=raw,snapshot=on,file=.../opencore.img
   -device virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10
   -device ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0
   -device usb-kbd,bus=ehci.0
   -device usb-tablet,bus=ehci.0
   ```

   in `templates/mavericks/mavericks.pkr.hcl`,
   `templates/snowleopard/snowleopard.pkr.hcl`,
   `templates/mavericks-aquatransport/mavericks-aquatransport.pkr.hcl`, both
   box Vagrantfiles (the layer's box keeps the base's), `bin/gate-vm.sh` and
   mavericks-vm's `scripts/boot.sh`. The OS disk is AHCI port 0 and the
   installer, in a build, port 1.
   - **OpenCore's disk is a `virtio-blk-pci` disk.** Nothing needed it on
     USB. On USB it forced the two shapes: UHCI companions on 10.9,
     because OVMF finds a full-speed `usb-storage` on EHCI only through a
     companion, and XHCI on 10.6, because 10.6.0's `AppleUSBUHCI` wedged
     installs (ADR 0014). OVMF has a virtio-blk driver and boots it first
     (`bootindex=0`); neither 10.6 nor 10.9 has one, so the guest never
     sees the disk. On AHCI, where round 2 put it, the guest saw it, and
     10.6's DiskArbitration called it unreadable at every desktop login
     (Limits). `ScanPolicy` (`assets/firmware/README.md`) lists no entry
     for it: it allows HFS+ on SATA alone, and the disk is neither.
   - **The virtio disk sits in slot `0x10`**, in every launcher. In the
     first free slot it was slot 1 in a build and slot 2 in a box. Slot
     `0x10`'s INTA is PIRQE, GSI 20: not the NIC's (slot 1, PIRQF, GSI 21)
     nor EHCI's (`0x1d`, GSI 19), and higher than the slots QEMU gives the
     devices before it (Round 2c).
   - **USB carries the keyboard and the pointer alone**, on one EHCI
     controller with no companion. QEMU's `usb-kbd` and `usb-tablet` are
     high-speed devices, so they attach at 480 Mb/s with no companion, and
     10.9.5's `AppleUSBEHCI` 660.4.0, 10.6.8's 4.2.4 and 10.6.0's 3.7.5
     drive them. Neither release loads `AppleUSBUHCI`, and 10.9's
     `AppleUSBXHCI` has no XHCI controller to look at.
   - **EHCI sits at `0x1d.0`, the ICH9's own USB slot.** In the first free
     slot it shared the NIC's interrupt line, and 10.6.0's installer spun
     in the NIC's interrupt filter (Measurements). The pin **avoids** that
     stall; it does not explain it. The stall came in 4 of 4 runs with
     EHCI on the NIC's GSI 21 and in none elsewhere, and the IOAPIC's
     counts match q35's routing, but why 10.6.0's `AppleUSBEHCI` never
     services its own interrupt there is not established. That no device
     a launcher adds shares `0x1d`'s line is REASONED: slots `0x19`-`0x1f`
     alone reach PIRQA-D, through the ICH9's own registers, and the shape
     test refuses any device there but EHCI.
   - **A tablet on both releases, not a mouse** (R48): it points without a
     grab. It is absolute but not one to one, by Apple's design (Limits).
2. **Keyboard Setup Assistant is told QEMU's keyboard at first boot**
   (R47). QEMU's `usb-kbd` is no keyboard Mac OS X knows. On 10.6.8 the
   assistant opens over the login window at every boot, on 10.9.5 in every
   desktop session, until it is quit once; quitting it writes
   `"1-1575-0" = 40` (QEMU's product 1 and vendor 0x0627, country 0; ANSI)
   into `/Library/Preferences/com.apple.keyboardtype.plist`'s
   `keyboardtype` dictionary. `assets/guest/firstboot.sh`'s
   `seed_keyboard_type` writes that entry, on both releases, only when the
   dictionary has none for that key; it never changes one that is there,
   keeps every other keyboard's, and logs which. Any other keyboard is
   still asked about. Both templates' `verify.sh` fail a guest without it.
3. **Tests.**
   - `internal/templatetest`'s `MachineShapeErrors` is the shape, one
     function; `TestTheBuildsMachineIsTheOneShape` (all three templates)
     and `TestTheBoxsMachineIsTheOneShape` (both boxes) hold every argv
     source to it: a build's `machine_type` and every `qemuargs` row as HCL
     parses it, an interpolated value kept as written; a box's
     `qe.machine`, `other_default` and `extra_qemu_args` as its Vagrantfile
     sets them. It refuses `usb=on`, `-usb`, `-usbdevice`, a one-string
     row, a `-device` whose model does not evaluate (the NIC's, only),
     any USB host controller but the one EHCI, a companion, EHCI anywhere
     but `pcie.0` `0x1d.0`, another device in slot `0x1d`, any input device
     but one `usb-kbd` and one `usb-tablet` on `ehci.0`, any other USB
     device, OpenCore's disk anything but exactly
     `virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10` from
     an `if=none,format=raw,snapshot=on` drive, another device in slot
     `0x10`, any device in the ICH9's slots `0x19`-`0x1f` but EHCI, any
     other virtio device, another bootindex, a `-machine`/`-M` value that
     sets `usb=on` with or without a machine type, a `-drive` whose `if=` is
     not `none` or `pflash` and the shorthand disk flags (`-hda`, `-cdrom`,
     ...), each of which makes a device with no `-device` row, and a build
     value that is neither a literal nor a template (a traversal; only
     `-uuid`'s is allowed). A box is also held to a read-only firmware and
     its varstore booted `snapshot=on` (`BoxShapeErrors`).
     `TestEveryWayBackToAnotherShapeIsCaught` is its regression set: round
     1's mutations, its review's, and the final review's (2026-10-28).
   - Snow Leopard's box Vagrantfile has a whole-file golden
     (`templates/snowleopard/testdata/box.Vagrantfile.golden`), as
     Mavericks' has; the shape tests carry every case on their own.
   - The layer's `base_box` must carry this shape's OpenCore row: its box
     keeps the base's Vagrantfile, so a base box built before would give
     the layer's box the old shape.
   - `internal/firmware`'s ScanPolicy test refuses
     `OC_SCAN_ALLOW_DEVICE_PCI`, OpenCore's class for its own virtio-blk
     disk.
   - `tests/gate_vm.bats` holds `bin/gate-vm.sh`'s argv to the same
     devices; `tests/firstboot_keyboard_type.bats` holds
     `seed_keyboard_type`; `tests/template_verify.bats` both `verify.sh`.

## Context

Mavericks had EHCI with UHCI companions, OpenCore's `usb-storage`,
`usb-kbd` and `usb-mouse` all on it. Snow Leopard had OpenCore on
`qemu-xhci` and no input at all, since UHCI's driver wedged 10.6.0's
installs (ADR 0014); round 1 of #9 gave its box EHCI with a keyboard and a
tablet beside that XHCI, in the box only. The owner's rule is parity by
default: a difference stays only when the releases are inherently
different and hiding it costs too much, or there is no way around it. The
first shape measured here (the owner's ruling) takes the boot disk off USB
altogether. It held, so the brief's two other candidates (Snow Leopard's
round-1 shape everywhere, or Mavericks' everywhere) were not carried out;
one probe of the first on 10.9 is under Measurements. With OpenCore on
AHCI it cost one behaviour difference, an unreadable-disk dialog in a 10.6
desktop session; R50 asked for a layout that ends it, and virtio-blk does
(Measurements, "Round 2b").

## Measurements

MEASURED 2026-10-27 on `pet-power-plant` (i7-8700B, 12 threads, KVM,
QEMU 8.2.2), 10.6 at one vCPU (R49), 10.9 at two. Two `yes` processes of
the same user ran throughout, at about 17% of a CPU each (six earlier in
the day); the one-minute load average is in each log line, and stayed
between 0.4 and 4 all day.

### The firmware boots OpenCore from AHCI (round 2)

- **OVMF picks it.** Without `bootindex`, on a 10.9.5 box's varstore,
  OVMF first tried the OS disk's entry and moved on: "BdsDxe: failed to
  load Boot0002 "UEFI QEMU HARDDISK QM00001" ... Not Found", then
  "starting Boot0003 ... Sata(0x2,0xFFFF,0x0)". With `bootindex=0` its
  first entry is OpenCore's: "loading Boot0002 "UEFI QEMU HARDDISK
  QM00005" from ...Sata(0x2,0xFFFF,0x0)" (`usb/probe/mav-old-box`,
  serial logs).
- **The picker is unchanged.** On that box, with OpenCore on AHCI and
  with it on Mavericks' old EHCI, OpenCore's picker listed the same two
  entries, "Mavericks" (the default) and "Recovery 10.9.5", and timed out
  into the first (screenshots `c0-t5.png`, `m-t6.png`). OpenCore's disk,
  FAT, is not listed. OpenCore's own log could not be had: with the
  config's `DisplayLevel` raised and file and serial targets on, the log
  file stayed zeros and the serial port got nothing (four boots, a copy
  of the image).
- **The guest's disks.** The OS disk stays `disk0` on both releases
  (`BSD root: disk0s2`); OpenCore's disk is the next one, an unmounted
  `EFI` partition, as it already was on USB on 10.9. `autoinstall.sh`
  picks only an unpartitioned disk, and OpenCore's has a GPT. Nothing in
  `assets/guest`, `gate` or `bin` names a disk number but `verify.sh`'s
  printed `diskbus` (`disk0`).

### 10.6.0's installer, and where EHCI sits

The first 10.6.8 build with EHCI in the first free slot was still
extracting `Essentials.pkg` 30 minutes in; `BaseSystem.pkg` had taken
17 minutes, against 54 s in an earlier build, and the vCPU ran at 100%
(`usb/builds/sl-first-slot`). It was stopped. The installer alone,
booted from the same media with the same argv, timed to the first three
packages (`usb/installer-shapes`):

| PCI order | EHCI's slot | BaseSystem | Essentials | BootCamp |
|---|---|---|---|---|
| OpenCore on XHCI, no input (the old shape) | none | 46 s | 96 s | 171 s |
| EHCI first | 1 | 70 s | 96 s | 176 s |
| the same, a VNC client connected as Packer's | 1 | 46 s | 101 s | 176 s |
| the build's order, the NIC first | 2 | none in 600 s | | |
| the same, again | 2 | none in 200 s | | |
| the NIC first, EHCI at `0x1d.0` | 29 | 45 s | 101 s | 176 s |

In the stalled guest (MEASURED, `c0p-dbg`, `c0p-dbg2`, `c0p-regs`):
- the vCPU sat with interrupts off (`RFL=0x86`) at one instruction, every
  sample, just after `mov 0xc0(%rdx),%eax`, and `%rdx` maps to the e1000's
  BAR (`gva2gpa`: `0x84080000`): it was reading the NIC's interrupt cause
  register, in the NIC driver's interrupt filter, over and over;
- IOAPIC pin 21, a level-triggered PCI line, was unmasked with its IRR and
  Remote IRR set: asserted and never cleared;
- EHCI's own registers held `USBINTR` `0x1f` and `USBSTS.USBINT` pending.
  OVMF left them clear (it polls, `USBINTR` 0) and halted the controller;
  10.6.0's `AppleUSBEHCI` started it with interrupts on, and the pending
  interrupt was never taken.

REASONED from QEMU's q35 routing: a device in slot 1 pin A and one in
slot 2 pin D (the e1000 and EHCI in the build's order, and in a box,
where vagrant-qemu puts its NIC first) land on the same PIRQ, PIRQF, GSI
21; slots `0x19`-`0x1f` route through the ICH9's own registers instead.
Why 10.6.0's EHCI handler never takes its interrupt there is not
established. That the NIC first and EHCI in slot 1 never came up is
because each launcher adds its NIC before the USB rows.

### Builds (round 2, OpenCore on AHCI)

| Build | Result | Time | Load |
|---|---|---|---|
| 10.6.8 (`updates = security`) 1 | rc 0, `verify: ok: 10.6.8`, `keyboardtype=40` | 1305 s | 1.1-1.6 |
| 10.6.8 2 | the same | 1201 s | 1.1-2.9 (the test suite ran beside it) |
| 10.6.0 (`updates = "none"`) | rc 0, `verify: ok: 10.6`, `keyboardtype=40` | 963 s | 1.0-2.8 |
| 10.9.5 (`updates = security`) 1 | rc 0, `verify: ok: 10.9.5`, `keyboard type 40` | 1717 s (the media rebuilt for the new payload) | 0.4-2.3 |
| 10.9.5 2 | the same | 1413 s | 0.7-2.3 |
| Aqua Transport layer, over build 1's box | rc 0, `verify: ok` | 288 s | 1.6-2.1 |

All with OpenCore on AHCI and EHCI at `0x1d.0`. In each 10.6 build
`BaseSystem.pkg` took 51-58 s and `Essentials.pkg` 72-77 s, as
before this change. For comparison, the last builds before it took
1415-1465 s (10.6.8) and 1444-1765 s (10.9.5), at load 6.5-10.
Each build's QEMU argv, read from `/proc` (`usb/builds/`), has the five
rows above and no other USB device.

### Boxes, through the real launcher (round 2, OpenCore on AHCI)

`vagrant box add`, then `vagrant up --provider qemu` (vagrant-qemu 0.6.3,
Vagrant 2.4.9). The project Vagrantfile changes only `other_default`, to
add a QMP socket: a monitor, not a device. A warm restart is the guest's
own (10.9: `sudo /usr/local/sbin/guest-macosx-shutdown -r`; 10.6: `sudo
/sbin/shutdown -r now`), hung if SSH does not answer on a new
`kern.boottime` within 300 s (ADR 0018). A cold boot is `vagrant halt`
(the box's trigger shuts the guest down) and `vagrant up`: a new QEMU.
After every boot: the guest's `ioreg -p IOUSB` names, one HMP `sendkey
shift` and one QMP absolute pointer move, each of which must reset
IOHIDSystem's `HIDIdleTime` from over 5 s to under 2 s, and whether
Keyboard Setup Assistant runs. The argv each campaign measured is the
box's own, read from `/proc` (`usb/box-*/box-argv.txt`): vagrant-qemu's
NIC in slot 1 and its disk on AHCI port 0, then the box's rows.

| Box | Warm restarts | Cold boots | Hung | Key, pointer, keyboard and tablet in `ioreg` | Keyboard Setup Assistant | Load |
|---|---|---|---|---|---|---|
| 10.9.5, build 1 | 150 | 30 | **0** | 180 of 180 each | 0 of 180 | 0.6-1.5 |
| 10.6.8, build 1 | 150 | 30 | **0** | 180 of 180 each | 0 of 180 | 0.6-2.2 |
| 10.6.0, the `updates = "none"` build | 30 | 5 | **0** | 35 of 35 each | 0 of 35 | 0.6-1.1 |

The 10.6.0 box's N is smaller, as round 1's was, because 10.6.0 is outside
parity (the spec's `updates = "none"` note); its crash reports were not
looked at. SSH answered on the new boot 20-27 s after the restart command
returned, and `vagrant up` took 18-33 s to a cold guest. After
the campaigns: no file in `/Library/Logs/DiagnosticReports` and no panic
on either; on 10.9 no `.shutdownStall`, and one `AppleUSBEHCI` debug line
in 182 boots ("unlinkAsyncEndpoint ... still be the AsyncListAddr after
doorbell"); on 10.6.8 no USB kernel line but `CheckSleepCapability`'s.
In the guests: `AppleUSBEHCI` 660.4.0 (10.9.5), 4.2.4 (10.6.8), 3.7.5
(10.6.0); no `AppleUSBUHCI`; EHCI on its own line (QEMU's `info pci`:
IRQ 19, the NIC 21).

The other launchers, on the 10.9.5 box: `bin/gate-vm.sh` booted it,
restarted it 3 times through `guest-macosx-shutdown` and shut it down,
with the keyboard and tablet in the guest (`usb/gate-vm`); mavericks-vm's
`scripts/boot.sh`, with this work's diff (`usb/mavericks-vm.diff`),
booted it to SSH in 60 s with the same devices (`usb/mavericks-vm-boot`).
The layer build booted it too (Builds).

### Snow Leopard's round-1 shape on 10.9, once

Before the shape above was measured, one boot of an earlier 10.9.5 box
(built 2026-10-14) with OpenCore on `qemu-xhci` and EHCI alone with
`usb-kbd` and `usb-tablet` (MEASURED, N 1): it answered SSH in 60 s;
`AppleUSBXHCI` 683.4.0 loaded but attached to nothing (`IOKitDiagnostics`
count 0; no driver under the XHCI's `IOPCIDevice`), so 10.9 never touched
OpenCore's disk; the keyboard and tablet worked at 480 Mb/s on
`AppleUSBEHCI` 660.4.0, a login typed and clicked at the login window
logged in, and the session's clicks landed where the trimmed mapping put
them. It was not carried further: the shape above held.

### Keyboard Setup Assistant

| Release | Without the entry | Its key, written by quitting it | With the entry |
|---|---|---|---|
| 10.9.5 | not at the login window (N 1, and the review's N 1); in the desktop session after a login typed through `usb-kbd`, 2 of 2 logins | `"1-1575-0" = 40`, `root:wheel 0644` | not in 2 of 2 sessions (`defaults write`) |
| 10.6.8 | over the login window, 3 of 3 boots (the review's) | `"1-1575-0" = 40`, `root:admin 0644` (the review's) | 0 of 2 (the review's) |

With the first boot's seed, on this shape's builds:
- every build's `verify.sh` printed `keyboardtype=40`, and each guest's
  first-boot log says it added the entry ("had no entry for QEMU's
  keyboard; 1-1575-0 = 40 (ANSI) added");
- the entry written by 10.6.0's first boot survived the 10.6.8 combo and
  2013-004 (`root:admin 0644`, as the assistant's own);
- Keyboard Setup Assistant ran at none of the 180 + 180 boots above (the
  campaigns' `ps` at the login window), nor at the 10.6.0 box's. On 10.6
  that check means something: unseeded, the assistant came up over the
  login window at 3 of 3 boots (round 1's review). **On 10.9 it cannot
  fail**: 10.9's assistant never appears at the login window, seeded or
  not, so the 10.9 boot counts say nothing about the seed. 10.9's
  evidence is the desktop logins: none at 1 (round 2), 3 (round 2b) and
  3 (round 2c) seeded logins, against 2 of 2 unseeded;
- **10.9.5**: no assistant in a desktop session (one login typed through
  `usb-kbd` on the seeded box), where the unseeded box showed it at 2 of
  2 logins;
- **10.6.8**: none over the login window (screenshots) and none in a
  desktop session (2 of 2 logins).

### Round 2b: OpenCore's disk on virtio-blk (R50)

**The dialog on AHCI (round 2, MEASURED).** With OpenCore's disk on AHCI
port 2, 10.6.8 showed "The disk you inserted was not readable by this
computer." (Initialize, Ignore, Eject) at every desktop login (2 of 2),
never at the login window or over SSH; 10.9.5 said nothing.

**What triggers it (READ, DiskArbitration-213, 10.6.8's: its
`SourceVersion` is 2130000).** When a console user logs in,
`diskarbitrationd` (`DAStage.c`, the `gDAConsoleUser` pass) asks
`_DAUnitIsUnreadable` (`DAPrivate.c:305-381`) of every whole disk. A disk
is unreadable when none of its media objects is busy, claimed, mountable
or kept from automounting, and each has a leaf below it. So any one of
these keeps it quiet:
- a mountable volume anywhere on the disk. That is why a real Mac's own
  EFI partition never asks: its disk also holds the HFS+ volume;
- `kDADiskOptionMountAutomatic` off on one of its media objects: an
  `autodiskmount` IORegistry property on the media or a parent that is not
  `true` (`DADisk.c:776-791`), or a file-system media type that matches it
  with `autodiskmount` false (`DAStage.c:938-944`).

OpenCore's disk has a GPT and one EFI System Partition, FAT32. 10.6.8's
`msdos.fs` has no media type for the ESP's GUID (MEASURED: its
`FSMediaTypes`), so nothing probes the partition, it is not mountable, it
keeps automount on, and the disk is unreadable. 10.9.5's `msdos.fs` lists
that GUID with `autodiskmount` false (MEASURED), so 10.9 is quiet. 10.6
reads the volume when asked (`mount -t msdos -o rdonly`, MEASURED).

**Tried, cheapest first:**
- **Retype the partition Apple_Boot** (round 2), which 10.6's `hfs.fs`
  matches with `autodiskmount` false: no. OpenCore then lists the
  partition, "EFI", as the default entry and boots into itself (MEASURED,
  one boot; not repeated, R50).
- **A second, mountable or never-automounted partition** beside the ESP:
  not tried. A volume 10.6 mounts would appear on both desktops, and the
  only `autodiskmount`-false types 10.6's file systems list are
  Apple_Boot's, which OpenCore lists.
- **A guest-side ignore** (fstab, a DiskArbitration preference): not
  tried. `fstab` names volumes, and the ESP never becomes one; it would
  also be a guest change, the least preferred.
- **Take the disk out of the guest's sight: `virtio-blk-pci`.** OVMF has
  a virtio-blk driver, and neither release has one (round 1: no virtio
  driver on 10.6.8's disk; 10.9's `virtio-net-pci` gets no interface, ADR
  0008). This is the change made.

**Measured on virtio-blk:**
- OVMF loads OpenCore from it, first: on a bare firmware with no OS disk,
  "BdsDxe: starting Boot0002 "UEFI Misc Device" from
  PciRoot(0x0)/Pci(0x3,0x0)" and OpenCore's own "System has no boot
  entries" (probe boot 1).
- **10.6.8**: one build (rc 0, `verify: ok`, 1178 s, BaseSystem.pkg 53 s, the
  10.6.0 installer at its old pace). Its box through `vagrant up`: the
  guest's disks are `disk0` alone; QEMU's `info pci` shows the virtio
  device in slot 2 with no guest driver; **no dialog at 3 of 3 desktop
  logins**, and no Keyboard Setup Assistant; 30 warm restarts and 5 cold
  boots, none hung, the key, the pointer and the keyboard and tablet at
  35 of 35; no crash report; OpenCore's menu "Snow Leopard" alone, as
  before, into which it timed out.
- **10.9.5**: one build (rc 0, `verify: ok`, 1324 s; its shutdown waited 61 s
  for a first-boot `kextcache` still updating `/`, which kextd logged as
  "'/' updating, delaying reboot": the offline `kextcache` of ADR 0034,
  seen before this change in `offline-report.md`, not a virtio effect,
  REASONED). Its box: `disk0` alone; no dialog and no Keyboard Setup
  Assistant at 3 of 3 desktop logins (the first through the Apple ID
  sign-in); 30 warm restarts and 5 cold boots, none hung, the key, the
  pointer and the keyboard and tablet at 35 of 35; no crash report and no
  `.shutdownStall`; OpenCore's menu "Mavericks" (the default) and
  "Recovery 10.9.5", as before. `bin/gate-vm.sh` (boot, restart,
  shutdown) and mavericks-vm's `boot.sh` with `usb/mavericks-vm-r2.diff`
  booted it with the same devices and `disk0` alone.

The rest of this ADR's measurements (the installer shapes, the builds, the
150/30 campaigns) were made with OpenCore on AHCI port 2, round 2's
layout; nothing in them involves OpenCore's disk once OVMF has loaded it,
but they are that layout's, not this one's. Round 2c measures ADR
0018's bar on the shipped shape.

### Round 2c: the virtio disk pinned to slot `0x10`, and ADR 0018's bar on the shipped shape

**The slot.** QEMU's q35 routes a device in slots 0-0x18 to PIRQ
`(slot + pin) mod 4 + 4` (E-H, GSI 20-23) and slots `0x19`-`0x1f`
through the ICH9's own registers (REASONED from QEMU's routing; the
numbers below MEASURED). Slot `0x10`, pin A, is PIRQE, GSI 20; the NIC in
slot 1 is PIRQF, GSI 21, and EHCI at `0x1d.0` GSI 19. A paused QEMU with
each launcher's device order (the box's, the NIC first; the build's, the
virtio row first) put the NIC in slot 1, VGA in 2 and the virtio disk in
16 both ways. In a 10.6.8 build's own argv (the installer, booted again
with QMP) and in both boxes: e1000 IRQ 21, virtio-blk IRQ 20, EHCI IRQ
19; IOAPIC pins 19 and 21 unmasked and level, pin 20 masked (no driver),
no IRR or Remote IRR set (`usb/r2c/build-irq`, `usb/r2c/box-*`).

**Builds.** 10.6.8 (rc 0, 1156 s) and 10.9.5 (rc 0, 1186 s). Neither
shutdown waited on kextd's "'/' updating, delaying reboot" (F29: seen
once in round 2b, not in these).

**Boxes, through `vagrant up`'s argv** (`usb/r2c/box-*/box-argv.txt`):

| Box | Desktop logins with no dialog, no Keyboard Setup Assistant | Warm | Cold | Hung | Key / pointer / keyboard and tablet | Keyboard Setup Assistant at boot | Load |
|---|---|---|---|---|---|---|---|
| 10.9.5 | 3 of 3 | 150 | 30 | **0** | 180 of 180 each | 0 of 180 | 0.6-3.6 |
| 10.6.8 | 3 of 3 | 150 | 30 | **0** | 180 of 180 each | 0 of 180 | 0.5-1.6 |

After each campaign: no crash report, no panic, no `.shutdownStall`
(10.9). `disk0` was the guest's only disk throughout. (On 10.9 the
per-boot Keyboard Setup Assistant column cannot fail; see Keyboard Setup
Assistant.)

**The final review's own runs on this shape (MEASURED, 2026-10-28,
`reviews/usb-review.md`):**
- **OpenCore's menu after the pin**: "Mavericks" (the default) and
  "Recovery 10.9.5" on a 10.9.5 box, "Snow Leopard" alone on a 10.6.0 box,
  each timing out into the first; OVMF loading `Boot0002 "UEFI Misc
  Device"` from `PciRoot(0x0)/Pci(0x10,0x0)`.
- **10.6.0 on the final shape**: an `updates = "none"` build (rc 0, 907 s,
  `keyboard type 40`), its box with no Keyboard Setup Assistant at the
  login window, and 30 warm restarts and 5 cold boots, none hung, key,
  pointer and keyboard and tablet at 35 of 35.
- **A box built before this change, booted in the new shape**
  (`bin/gate-vm.sh`'s argv, which is mavericks-vm's): boot, the menu,
  3 restarts, input, `disk0` alone.
- **The varstore**: no box carries its build's varstore. Every box ships
  the firmware data source's pristine `OVMF_VARS.fd` and boots it
  `snapshot=on`; bootindex is honoured on it, and OVMF's per-boot
  BootOrder rewrite programs no more flash than the old shape did (3667
  bytes a boot either way).

## Limits

- **The tablet is absolute, not one to one, on both releases.**
  IOHIDEventDriver trims each absolute axis by
  `AbsoluteAxisBoundsRemovalPercentage`, 15 by default: it takes
  `(max - min) * 15 / 200` off each end of the logical range (READ:
  IOHIDFamily-315.7.16, 10.6.8's, `IOHIDEventDriver.cpp:541,561-562,
  593-597,611-616`; IOHIDFamily-503.215.2, 10.9.5's, `:77,106,188-190`
  and `calibrateDigitizerElement`, `:645-652`). QEMU's tablet is
  0..32767, so the guest maps 2457..30310 onto the screen and clamps the
  rest: positions land 1/0.85, about 1.18, times as far from the centre as
  the host's, and the guest's pointer reaches each edge 7.5% of the window
  early. Every point is reachable, and clicks land under the guest's
  pointer, the only one shown (QEMU's gtk and cocoa hide the host's in
  absolute mode). MEASURED: 1.17-1.19 on 10.6.8, 1.173-1.175 on 10.9.5
  (round 1's review), and every click in this work's 10.9 logins landed
  where that mapping put it. With the property 0 the 10.6.8 pointer
  landed 1:1 (the review, one boot of a codeless personality). A 1:1
  personality for both releases comes after the release (R48).
- **10.6.0's EHCI and a shared line.** The cause of the stall is known as
  far as the stuck line and the NIC's filter; why 10.6.0's `AppleUSBEHCI`
  never took its own interrupt there is not. The fixed address avoids the
  known trigger, a line shared with the NIC; it is not a fix of the
  cause, which stays open ("What would change this").

## Differences between the releases that stay

None in the machine's devices, and none in what a desktop session shows
(Round 2b). The ones that stay, each with its own decision:
- **CPUs**: 10.6 at one, 10.9 at two. 1 CPU ships as a documented
  deviation, cause not yet established; F26 is root-caused after the
  release (R49, ADR 0014).
- **Firmware, SSH, updates, sudo**: ADR 0014's items 1-6, each a property
  of the release.

## What would change this

- A device that needs a full-speed port: it would need a companion again.
- A QEMU whose XHCI 10.9 drives: nothing, since USB no longer boots.
- A guest with a virtio-blk driver: it would see OpenCore's disk again,
  and 10.6's DiskArbitration would ask about it (Round 2b).
- 10.6.0's `AppleUSBEHCI` on a shared line explained, or a launcher that
  must put a device in slot `0x1d`.
- A 1:1 tablet personality (R48, after the release).

## Landing

mavericks-vm's `scripts/boot.sh` hard-codes the box's argv. Its change
(the new shape) lands in the same pull request as the bump of its
`components/packer-plugin-macosx/version` to the release that carries
this decision, so that its self-test boots new images with the new argv.
Old images under the new argv booted in the final review's one run, but
that is one box, not the self-test.
