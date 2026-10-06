# 0005 — Nested virtualization, and portability across hosts

Date: 2026-09-17
Status: accepted. The host is Linux on Intel with KVM; the seams for
others are kept open, and nested virtualization is a recorded requirement
with no build behind it yet.

## 1. Where the plugin runs

**A Linux host with an Intel CPU and VT-x or an AMD CPU and AMD-V, and a
writable `/dev/kvm`.** `mavericks-media` checks this before anything else
and refuses, by name, a CPU without its vendor's virtualization, any
other vendor, an unwritable `/dev/kvm` and any OS but Linux
(`internal/hostcheck`). AMD was refused until 2026-10-06, when Mavericks
built and booted on GitHub's AMD runners with `vendor=GenuineIntel` on
the guest's `-cpu` line (`docs/host-profile.md` G2, MEASURED).

Three hosts have built and installed a guest (`docs/test-hosts.md`):
Linux Mint on a Coffee Lake Mac mini, EndeavourOS on a Broadwell MacBook
Air, and Debian on a 2006 Woodcrest Mac Pro with no EPT.

### The seams, kept open

What ties the plugin to Linux today is small, and it is kept in known
places:

- **The privops microVM is an interface** (`internal/privops`). The media
  build needs root inside a filesystem, and gets it by booting the host's
  own Linux kernel with a busybox initramfs under QEMU, with no host root.
  A second backend could boot a kernel and initramfs shipped with the
  plugin, under any accelerator, which would make media building work on
  any host with QEMU. Nothing like it exists yet.
- **Linux-only checks stay inside the Linux backend and the host check.**
  Everything else is portable Go.
- **Host tools run through `proc.Runner`**, one seam for every external
  program, faked in tests.
- **The accelerator is a template variable** (`accelerator`: kvm, tcg,
  hvf, whpx, xen, hax, nvmm, none). Only `kvm` has built a guest. Another
  is a value plus a measurement, not a redesign.
- **The plugin cross-builds** for linux, darwin and netbsd on amd64 and
  arm64, and CI builds them, so nothing Linux-only creeps into the shared
  code. A release ships only what `internal/hostcheck` supports, Linux on
  amd64 (decided 2026-10-06, after `v0.20261005.1` shipped all six): a
  binary for a host the plugin refuses would install and then fail.
- **The firmware needs a host C toolchain.** A prebuilt, checksummed
  firmware would remove that requirement; `docs/decisions/0004` says why
  the firmware's bytes depend on the toolchain.

### What another host changes

`docs/host-profile.md` §4 is the ledger of host-specific assumptions, each
a hypothesis until a second host has tried to falsify it. Some findings
are about the guest and hold anywhere: 10.9 cannot drive QEMU's XHCI
controller, so the machine has EHCI with UHCI companions (G13, confirmed
under three QEMUs). Others are about KVM: `MacPro5,1` SMBIOS panics under
KVM on every host and boots under TCG (`docs/decisions/0010`).

**NetBSD with NVMM** is a different accelerator, not KVM (`-accel nvmm`).
What would carry over: the data sources' downloads, the firmware build,
the OpenCore configuration and the template's machine. What would not:
the Linux privops backend and the host check. It is a direction, not a
plan.

## 2. Nested virtualization

The user wants to run **Mavergreen/container-tools**, which provides Docker
tooling on top of **VMware Fusion**, inside the guest. That makes the
guest a hypervisor host.

### What is measured

- **The primary host supports it.** `/sys/module/kvm_intel/parameters/nested`
  reads `Y` there, with no configuration needed.
- **QEMU can expose it.** No base CPU model advertises `vmx` — `Penryn`,
  `Nehalem`, `Westmere` and `Haswell-noTSX` all report `vmx=False` — so it
  must be asked for, and `-cpu Penryn,+vmx` is accepted under KVM.
- **`Nehalem` boots the guest.** MEASURED 2026-09-21 on the primary host:
  an installed guest booted with `-cpu Nehalem`, answered SSH in 40 s and
  reported `Intel Core i7 9xx (Nehalem Class Core i7)` with `POPCNT`. No
  guest has been *installed* on it (`docs/decisions/0009`).

### What is not

**Penryn (2008) predates EPT**, which arrived with Nehalem, and VMware's
hypervisor generally wants EPT for 64-bit guests. So supporting Fusion
may force a CPU model newer than the default. Whether Fusion of the right
vintage requires EPT, and whether it refuses to run when it detects being
virtualized, are both untested.

The cheap fact to establish first: does VMware Fusion of the right
vintage install and start on 10.9 under KVM with `cpu` set to
`Nehalem,+vmx`? That one experiment decides whether the rest matters.
`ap-juicer` cannot answer it: Woodcrest has VT-x and no EPT.
