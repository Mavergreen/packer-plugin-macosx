# Prior art

> **Prior art has a date.** These sources are mostly 2016–2021, and four
> of their claims were disproven by testing here, each because the world
> moved on after it was written:
>
> - *"OS X cannot use QEMU's `usb-tablet`"*: fixed in QEMU in **2017**, by
>   the very author whose workaround kext was cited as evidence.
> - *"DNS needs pointing at 1.1.1.1"*: DNS worked untouched.
> - *"Security Update 2016-001 is the last"*: it is 2016-004.
> - *"`kvm.ignore_msrs=1` is required"*: a Yosemite-era claim about a bug
>   fixed in Linux 4.7 (the archaeology below).
>
> One held: stock 10.9 has no virtio networking.
>
> Treat every "X does not work" below as **a claim with an expiry date**,
> not a constraint, and re-test before building around it. The failure
> mode is subtle: prior art *fits the symptom*, which is exactly what makes
> a wrong diagnosis convincing.

Where a source's claim has been tested here, the entry says what was
found and when.

## Installing and booting 10.9 under QEMU

### Adam Kostarelas, March 2026: the reference configuration
<https://adam.kostarelas.com/blog/mavericks-in-utm-on-silicon/>

### Mykola Grymalyuk (khronokernel), 2021: the guide Kostarelas followed
<https://khronokernel.com/apple/silicon/2021/01/17/QEMU-AS.html>

UTM settings, CPU flags and troubleshooting. His prebuilt OpenCore images
live in `khronokernel/khronokernel.github.io` under `Binaries/OpenCore/`;
`EFI-LEGACY.img` covers 10.6–10.14 and is pinned as `opencore-legacy-img`,
**Tier 2**, reference only. His CPU string:
`Penryn,+ssse3,+sse4.1,+sse4.2,+popcnt,+xsave,+xsaveopt,check` plus
`vendor=GenuineIntel` (the bundle used fewer flags). He attached every
disk over USB and used `vmxnet3`. Under TCG on an M1: 17 minutes to macOS
recovery, 8 with `-accel tcg,thread=multi`.

His `scutil` recipe for DNS in the installer environment (`d.init`,
`d.add ServerAddresses * ...`, then
`set State:/Network/Service/PRIMARY_SERVICE_ID/DNS`) was never needed here.

**Never use** his `Catalina-SETUP.qcow2` or any other prebuilt macOS disk
image: the OS comes from Apple only.

### Gabriel Somlo, CMU
<https://www.contrib.andrew.cmu.edu/~somlo/OSXKVM/>

Two claims attributed to him were believed here and **did not survive**:
that `kvm.ignore_msrs` is required (the archaeology below), and that
10.9's first boot after install fails without SMP (no such claim is on
his page in any revision; an installed guest boots here with one CPU).
He also confirmed `pmj/virtio-net-osx` working on 10.9.

### kholia/OSX-KVM

`OpenCore-Boot-macOS.sh`, OpenCore image tooling, and the `isa-applesmc`
OSK value, which this project does not need. Its CPU choice is
`Haswell-noTSX` plus `+invtsc`. Its `fetch-macOS` script does not offer
10.9.

### thenickdude/KVM-Opencore

OpenCore configuration details.

## Installer media

### Mavericks Forever `get.sh`
<https://mavericksforever.com/get.sh>

`System/Installation/Packages` in BaseSystem is a symlink into the ESD
volume, dangling once BaseSystem stands alone; the assembly replaces it
with the ESD's real `Packages`.

### eprigorodov/mkosxinstallusb
<https://github.com/eprigorodov/mkosxinstallusb>

`get.sh`'s assembly with Linux tools: `dmg2img`, `kpartx`/`losetup -P`,
`mkfs.hfsplus -v "OS X Base System"`, `rsync -aAEHW`, then the ESD's
`Packages`. It needs root for the mounts; `mavericks-media` needs none,
because it assembles inside the privops microVM.

Its README warns that Korean localization can be lost and that HFS+
compression may not survive the copy. **Tested here** (2026-09-17, against
Mac-made media): the Linux-built media holds every path in the reference,
40,200 files and 6,414,899,267 bytes on each side, no file a different
size; neither image stores any file compressed, so there is no
compression to lose; the apparent loss of localized names was the
*measurement's* Unicode normalization, not the copy's.

### Rejected installer sources

- **OpenCore's `macrecovery.py`** (10.9 invocation `-b
  Mac-F60DEB81FF30ACF6 -m 00000000000FNN100`) fetches only a recovery
  image, which needs internet inside the guest: not offline media.
- **gibMacOS**: its catalogs do not reach 10.9. `corpnewt/gibMacOS` on
  GitHub is the only real one.

### timsutton/osx-vm-templates

The first-boot automation payload (creating the user, SSH, skipping Setup
Assistant) and **the unattended install itself**: `prepare_iso/prepare_iso.sh`
and `prepare_iso/support/` are where `/etc/rc.cdrom.local`,
`minstallconfig.xml` and `OSInstall.collection` come from, all read by
Apple's own `/etc/rc.install` inside the installer environment. The
`minstallconfig.xml` schema is upstream's, which took it from **Greg
Neagle's `createOSXInstallPkg`** (munki). Its `OSInstall.collection` lists
`OSInstall.mpkg` twice, which reads like a typo and is load-bearing:
MEASURED 2026-09-17, with one entry the installer refused with "There was
a problem with the automated installation" and wrote nothing.

It is also a Packer template that produces Vagrant boxes, the same shape
this project has.

## Performance

- **pmj/virtio-net-osx 0.9.4**: <https://github.com/pmj/virtio-net-osx>.
  Reports 10.7–10.9 working under QEMU/KVM, and under VirtualBox about 2x
  faster TCP send and 4x faster receive than the emulated Intel NIC. The
  only route to virtio networking on 10.9, which has none of its own.
- **ivanagui2/VMQemuVGA**: <https://github.com/ivanagui2/VMQemuVGA>.
  Release notes claim 10.6 through 10.10, mentioning only VirtualBox.
- **VMsvga2**: <https://sourceforge.net/projects/vmsvga2/>. 10.5+,
  abandoned in 2014. **QiuMike/VMsvga2ForQEMU** adapted it to Catalina on
  QEMU/KVM.
- **Stock QEMU `vmware-svga`** implements VMware's display device
  minimally, per **qemus/qemu-vmvga**, which adds 3D through DXVK/Vulkan
  but targets Windows guests.
- **Docker-OSX issue #867** (2025) asks for VMware's graphics driver with
  `vmware-svga`; unresolved.
- **steelbrain/reims-vgpu** needs macOS 11+ guests with Metal; it does
  not apply.
- **adespoton/utmconfigs**: UTM configurations for other macOS versions.

## Guest integration (none built)

- **pmj/QemuUSBTablet-OSX**: <https://github.com/pmj/QemuUSBTablet-OSX>.
  A driver letting OS X guests use QEMU's `usb-tablet`. **Not needed.**
  `usb-tablet` works natively on 10.9 on an EHCI controller; MEASURED
  2026-09-17 on a guest that never had the kext. An earlier failure was
  `qemu-xhci`, which 10.9 cannot drive at all. The kext worked around a
  *QEMU* bug that its own author fixed upstream: Phil Dennis-Jordan's QEMU
  commit `0cd089e937f2`, 2017-01-25, "hw/usb/dev-hid: Improve guest
  compatibility of usb-tablet", changed the tablet's boot protocol and HID
  usage because macOS's HID stack treated it as an analog stick. Any QEMU
  from 2.9 has it.
- **pmj/virtio-net-osx**: its README says other virtio device types
  would attach to the same `VirtioPCIDriver`; a 2018 commit began a
  driver for standardized virtio PCI devices.
- **pmj/kextgizmos**: helpers for kext development.
- **QEMU's built-in SPICE agent host side**
  (<https://www.kraxel.org/blog/2021/05/qemu-cut-paste/>): QEMU 6.1+ wires
  a SPICE agent chardev to its own clipboard:
  `-chardev qemu-vdagent,id=vdagent -device virtserialport,chardev=vdagent,name=com.redhat.spice.0`.
- **utmapp/vd_agent**: a macOS SPICE guest agent, clipboard only, built
  for Apple Silicon with Homebrew GLib.
- **proxmox-mac-guest**: a QEMU guest agent for macOS over an ISA serial
  port, and a `spice-vdagent` on a virtio serial port.
- **litecreator/virtio-gpu-macos**: an alpha `virtio-gpu` framebuffer
  driver for 10.15+.
- **QEMU source**: `hw/display/vmware_vga.c`, `hw/display/virtio-gpu*.c`,
  `ui/vdagent.c`, `qga/`.

Ground rules for any guest-side kernel code: build it on 10.9 with a
period-appropriate Xcode and SDK and record the versions; test only on
throwaway guests; debug through QEMU's gdb stub (`-s`) with Apple's Kernel
Debug Kit for the exact 10.9.x build; prefer fixes upstream over forks.

## Running a guest in CI

- **vmactions/anyvm**: the workflow shape to copy: boot, sync files in,
  run over SSH, sync back.

---
