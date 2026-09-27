# 0008 — The guest's NIC is `e1000-82545em`, and a NIC is build-time state

Date: 2026-09-21
Status: accepted, on measurement

## Decision

**The template's `nic` variable defaults to `e1000-82545em`.** It also
accepts `virtio-net-pci`, though stock 10.9 cannot use it (§Why not
`virtio-net-pci`), and does not accept `usb-net` (§Why not `usb-net`).
The box's Vagrantfile is rendered with the same value, because a guest
must boot with the NIC it was installed with (below).

## Why

MEASURED 2026-09-21 on the primary host (`pet-power-plant`, i7-8700B,
QEMU 8.2.2, KVM, `-cpu Penryn,+ssse3,+sse4.1,+sse4.2`, 2 vCPUs, 4096 MB),
one changed `-device` line, QEMU's user-mode (slirp) networking, plain
HTTP against a host-local server that generates and discards bytes from
memory, 200 MB per transfer, three transfers each way. Not over SSH:
OpenSSH encrypting on an emulated Penryn would have measured the cipher.

| device | host → guest | guest → host | link the guest reports |
|---|---|---|---|
| `usb-net` (CDC-ECM) | 1.24 MB/s | 1.27 MB/s | `10baseT/UTP <full-duplex>` |
| `e1000-82545em` | 174 MB/s | 23.3 MB/s | `1000baseT <full-duplex>` |
| `virtio-net-pci` | no link | no link | no interface at all |

That is **140x receive and 18x send**. It is not a tuning difference: the
guest reports the CDC-ECM link as 10baseT, and 1.24 MB/s is 9.9 Mbit/s.
`usb-net` runs at exactly the speed it says it does. `usb-net` repeated to
within 0.3%, `e1000-82545em` to within 5%, and both kept their numbers
across a reboot (to within 0.02% for `usb-net`).

`e1000-82545em` is also a device 10.9 has a driver for:
`AppleIntel8254XEthernet` 3.1.4b1 loads and claims it with nothing added,
and `networksetup` calls the result "Ethernet". And it frees the USB
controller of a device.

## Why not `virtio-net-pci`

**Stock 10.9 has no virtio networking.** MEASURED on a guest booted with
all three NICs at once, so the failing device could be examined over a
working one: QEMU presents the virtio device and IOKit enumerates it
(`compatible = <"pci1af4,1","pci1af4,1000","pciclass,020000">`), and
**nothing matches it**. The node has no driver child and no
`IOEthernetInterface`, while the `e1000` beside it in the same boot has
both, and no `Info.plist` under `/System/Library/Extensions` mentions
`1af4`. The third-party `pmj/virtio-net-osx` kext is what would be needed.

`virtio-net-pci` stays among the accepted values, because it is what
someone would try after installing that kext. Without it, a guest built
with it has no network, so the build never reaches SSH.

## A NIC is build-time state in 10.9

**This outranks the benchmark.** 10.9 records the interfaces it has seen
in `/Library/Preferences/SystemConfiguration/NetworkInterfaces.plist` and
creates network *services* for them then. Boot an installed guest with a
NIC it has never met and you get:

- the device enumerated and its driver matched,
- a BSD interface (`en1`) and a hardware port,
- **no network service, no DHCP, no route, no SSH.**

MEASURED 2026-09-21: a guest installed with `usb-net` and booted with
`e1000-82545em` never answered SSH (420 s, sitting at its login window).
The same guest booted with both NICs showed `en1`, the driver loaded, and
`networksetup -getinfo Ethernet` answering "Ethernet is not a recognized
network service". A guest *installed* with `e1000-82545em` got `en0`,
DHCP, DNS and a service named "Ethernet", and answered SSH.

So:

1. **`nic` belongs to the build.** It changes what is in the image, like
   `updates`.
2. **The box boots with the NIC its image was installed with.** The box's
   Vagrantfile is rendered with the build's `nic` for exactly this reason,
   and a project's own Vagrantfile should not override it.
3. **A guest built with one NIC keeps it** until it is rebuilt with
   another.

## Why not `usb-net`

MEASURED 2026-09-27 on the primary host (`pet-power-plant`), `user=builder`,
everything else as in a passing `e1000-82545em` build: a build with
`nic=usb-net` never answered SSH within the 1 h `install_timeout`. The QEMU
arguments matched the 2026-09-21 `usb-net` machine exactly. Not
investigated further; another Packer build (an m68k guest) was running on
the host concurrently. A NIC the template cannot build a guest with is not
one it offers, so `usb-net` is not an accepted value.

## What would change this

- **A different QEMU.** Everything here was measured on QEMU 8.2.2 alone;
  `docs/host-profile.md` G24 is the hypothesis that the ranking holds on
  others.
- **Tap or vhost instead of slirp.** Every number above went through
  user-mode networking, which is what the template uses because it needs
  no root. A faster transport would raise the `e1000` numbers and almost
  certainly not the `usb-net` ones, since 10baseT is the device model's.
- **The virtio kext.** If `pmj/virtio-net-osx` works, the ranking is open
  again, but that is a kext decision, a larger commitment than a `-device`
  line.

## What was not measured

`e1000` (the 82540em alias), `e1000e` and `vmxnet3`.
