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

The most recent verified 10.9 install on QEMU, through UTM on Apple
Silicon, so TCG rather than KVM: OVMF plus OpenCore, with `get.sh`'s dmg
converted to a CD image as the installer. His UTM bundle,
`Mavericks-OSX-10.9-Config.utm.zip`, is pinned as `utm-bundle` in
`assets/pins/sources.tsv` (Tier 2: evidence, never an ingredient).

Inspected 2026-09-17. It carries `q35` with `vmport=off`; `Penryn` with
exactly `ssse3`, `sse4.1` and `sse4.2`; 8 CPUs; 8192 MB; the OpenCore
image on USB and the target on IDE; `usb-net`; a 2 MB-class OVMF used as
`-bios`; and no `isa-applesmc` device: the SMC comes from kexts OpenCore
injects. Its OpenCore image is khronokernel's, unmodified (the `EFI/`
trees hash identically). `docs/configuration-register.md` says which of
its settings this project kept and why.

His other findings: 8 GB assigned but ~2.9 GB used at idle; graphics very
slow, about 3 MB of VRAM; DNS needing 1.1.1.1 (**not needed here**); the
modern web mostly broken in stock Safari, a TLS-era limitation. His OVMF
was about five years old. **Tested here:** Debian's current stock OVMF
(2024.02) does not boot macOS with OpenCore, and an OVMF built from
acidanthera's audk does (`docs/decisions/0004`).

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

Chameleon plus SeaBIOS on KVM, covering 10.9: `-cpu core2duo,vendor=GenuineIntel`,
input by `-usb -device usb-kbd -device usb-mouse`, boot by `-kernel
<chameleon boot file>` plus `-smbios type=2`. The host no longer resolves
in DNS (checked 2026-09-21); read it through the Internet Archive.

Two claims attributed to him were believed here and **did not survive**:
that `kvm.ignore_msrs` is required (the archaeology below), and that
10.9's first boot after install fails without SMP (no such claim is on
his page in any revision; an installed guest boots here with one CPU).
He also confirmed `pmj/virtio-net-osx` working on 10.9.

### royalgraphx/DarwinKVM

An OpenCore Mavericks guide under `installguides/11-Mavericks/`, which
specifies `e1000-82545em` as the NIC for 10.9. **Tested here**: it works
with 10.9's own driver, and it is the template's default
(`docs/decisions/0008`). `docs.darwinkvm.com` serves a parked page (checked
2026-09-21); read the source in the repository.

### kholia/OSX-KVM

`OpenCore-Boot-macOS.sh`, OpenCore image tooling, and the `isa-applesmc`
OSK value, which this project does not need. Its CPU choice is
`Haswell-noTSX` plus `+invtsc`. Its `fetch-macOS` script does not offer
10.9.

### thenickdude/KVM-Opencore

OpenCore configuration details.

## Installing and booting 10.6 under QEMU

Read on 2026-10-04 -- after the first 10.6 boot had already hung, which
is the wrong order; next time, here first.

### jprx/how-to-install-snow-leopard-in-qemu

10.6 on OSX-KVM's OpenCore. Its `config.plist` changes include
`Booter > Quirks > RebuildAppleMemoryMap` and `DevirtualiseMmio`, one
core, and `e1000-82545em`, and it warns that the install ends in a kernel
panic when the installer blesses the new disk, fixed by running `bless`
by hand. **Tested here** (2026-10-04, `assets/firmware/README.md`):
`RebuildAppleMemoryMap` is what gets 10.6.0's kernel past
`mig_table_max_displ` under KVM; `DevirtualiseMmio` is what makes `bless`
panic, writing NVRAM, and with it off the install blesses and reboots on
its own, so nothing is blessed by hand.

### royalgraphx/LegacyOSXKVM

10.0 through 10.12 on QEMU, by the author of DarwinKVM (above); archived
2025-11-16. Its 10.6 OpenCore image (`opencore/opencore-SLeopard.qcow2`,
read here 2026-10-04) has `RebuildAppleMemoryMap` on and
`DevirtualiseMmio` off -- what this project measured -- plus
`ProvideCurrentCpuInfo`, `ForceExitBootServices`, and boot-args
`cpus=2`, none of which turned out to be needed here. It boots OVMF with
`-bios` (no NVRAM variable store) and uses QEMU's `isa-applesmc` with
Apple's OSK string, which this project does not (VirtualSMC instead).

### Gabriel Somlo's 10.6-era notes

His page's older revision (`index_old.html`, above) says 10.6 and early
10.7 use MONITOR/MWAIT, which KVM did not support, and need `idlehalt=0`
or `AppleIntelCPUPowerManagement.kext` removed; and that 10.6 panics with
"HPET not found" on a PIIX machine with more than one CPU. **Tested
here** 2026-10-04: `idlehalt=0` did not move the `mig_table_max_displ`
hang, and this project's machine is q35, not PIIX.

### Infinite Mac

infinitemac.org runs classic Mac OS and PowerPC Mac OS X, up to 10.4, in
a browser (INHERITED, not verified here). Prior art for a PowerPC Tiger,
not for an Intel 10.6 under KVM.

## Installer media

### Mavericks Forever `get.sh`
<https://mavericksforever.com/get.sh>

Read in full 2026-09-17 (its header: "Last updated 2026/02/09", by
Wowfunhappy with Krackers, Jazzzny and dosdude1). It authenticates to
`osrecovery.apple.com`, downloads `InstallESD.dmg` over plain HTTP and
checks its sha256; `internal/fetch` implements the same handshake, and
`mavericks-installesd` enforces the same checksum. Everything after the
checksum is `hdiutil`, macOS-only: attach the ESD, resize BaseSystem to
6,550,020,096 bytes, copy `Packages`, `BaseSystem.chunklist` and
`BaseSystem.dmg` in. `mavericks-media` does that assembly on Linux.

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

## Other ways to build a macOS image with Packer

Searched 2026-10-04, before the first release, to ask whether this
project duplicates one that already exists. None does: no Packer
plugin installs a pre-Big Sur OS X unattended, and nothing found does it
from a Linux host with no Mac anywhere in the pipeline. A search cannot
prove a negative; these are the near misses, and why each is not this.
Each entry's claims are INHERITED from its own documentation, unverified
here.

### Templates for old OS X: osx-vm-templates and its forks

`timsutton/osx-vm-templates` (above) and forks such as
`dwtj/osx-vmware-builder` and `improbable-io/osx-vm-templates` are Packer
*templates*, not a plugin. They cover 10.7 through 10.12, and their media
step, `prepare_iso.sh`, runs on a Mac (it needs `hdiutil` and
`pkgbuild`; for 10.6, a Mac with Xcode 3.2.6 for `pkgbuild`). They build
for VMware, VirtualBox and Parallels, not QEMU, and are unmaintained.

### Plugins for modern macOS: Tart, Anka, Parallels, IPSW

`cirruslabs/packer-plugin-tart`, `veertuinc/packer-plugin-veertu-anka`
and the Parallels plugin's `ipsw` builder are real Packer plugins that
build macOS images, and `torarnv/ipsw` is a data source that finds Apple's
IPSW firmware for them. All of them install macOS 11 or later from an
IPSW, on an Apple Silicon Mac (Anka also on Intel Macs): Apple's
virtualization framework, not an installer this project could drive,
and a host this project does not run on.

### macOS on Linux KVM: OSX-KVM and its descendants

`kholia/OSX-KVM` (above), `Coopydood/ultimate-macOS-KVM` and the
one-command `macOS-kvm` scripts run macOS on a Linux host, like this
project. What they automate is the setup -- fetching a recovery image,
the firmware, the disk -- and the install itself is still clicked
through by hand. OSX-KVM lists automating it (with OpenCV) as an idea.
None is a Packer plugin, and none reaches back to 10.9 or earlier with
offline media.

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

What was measured here instead: the screen resolution is the framebuffer
OpenCore sets, not a driver's (`docs/configuration-register.md` §7), and
the guest cannot change it at runtime.

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

GitHub's arm64 macOS runners (`macos-14`, `macos-15`, `macos-latest`,
`macos-26`) are documented at 3 M1 cores, 7 GB RAM and 14 GB of SSD, with
jobs capped at 6 hours and `actions/cache` evicting entries unused for 7
days, 10 GB per repository by default (INHERITED; unverified here). Apple's
licence permits macOS VMs only on Apple hardware running macOS, and arm64
has no x86 hardware virtualization, so a guest there runs under TCG.
Whether such a runner can build the firmware is `docs/open-questions.md`
Q3.

---

# Dated archaeology: where `kvm.ignore_msrs` actually came from

Researched 2026-09-21, after G21 was refuted on two hosts. The entry had sat
in `docs/host-profile.md` §3 since 2026-09-17 citing "Somlo and OSX-KVM" as
its authority, and **nobody had ever read what those two sources actually
say.** They do not say what this project believed they said.

Sources were reached through `web.archive.org` replays (Somlo's host
`contrib.andrew.cmu.edu` **no longer resolves in DNS**, checked 2026-09-21),
`marc.info` (mailing lists — `lore.kernel.org` is behind an anti-bot
challenge and was unreachable), and git clones. The Wayback CDX API was
returning 504 all session, so the snapshot inventory below is from
year-targeted replays rather than a complete capture list.

## 1. Somlo never claimed `ignore_msrs` fixes anything on 10.9

**He named one MSR, one guest OS, and an expiry date.** From the revision of
his page last updated **2015-05-04** (snapshot
`web.archive.org/web/20150602182549/`), verbatim:

> "NEW: As of Yosemite (OS X 10.10), we need to tell KVM to ignore unhandled
> MSR accesses (During boot, Yosemite attempts to read from **MSR 0x199**,
> which is related to CPU frequency scaling, and is clearly not applicable to
> a VM guest)"

The instruction is **absent** from the 2015-02-26 snapshot (page last updated
2014-09-02) and present in the next revision, so it was added between
**2014-09-02 and 2015-05-04**.

By the revision last updated **2016-09-05** he had already scoped it to dead
kernels:

> "As of Yosemite (OS X 10.10), **on kernels older than 4.7**, we need to tell
> KVM to ignore unhandled MSR accesses …"

And from the **2017-05-30** revision onward the page requires "Linux kernel
≥ 4.7" and **drops the `ignore_msrs` instruction entirely.**

`MSR 0x199` is `IA32_PERF_CTL`. His own dmesg, quoted on kvm@vger
**2016-05-26** inside Radim Krčmář's reply
(<https://marc.info/?l=kvm&m=146436257129374&w=2>):

> "After setting /sys/module/kvm/parameters/ignore_msrs, all I get in dmesg
> after firing up OS X is: `vcpu0 ignored rdmsr: 0x199`"

**Three things follow, and all three bind on us:**

1. **The guest was 10.10, not 10.9.** No dated source found ties
   `ignore_msrs` to Mavericks or earlier. Our guest is 10.9.5.
2. **The symptom was not a panic.** Krčmář's patch posting, **2016-05-27**,
   describes the real mechanism: "KVM's vCPU model behaves exactly as a real
   CPU in this case by injecting a fault when MSR_IA32_PERF_CTL is called
   (which KVM does not support). However, some operating systems use this
   register during an early boot stage in which their kernel is not capable
   of handling #GP correctly, causing #DP and finally a triple fault
   effectively resetting the vCPU." That triple-fault vCPU reset is the
   "bootloop" every downstream guide cites. It is not a kernel panic and it
   does not look like one.
3. **It was fixed upstream in Linux 4.7** (released 2016-07-24), by Dmitry
   Bilunov's dummy `MSR_IA32_PERF_CTL` handler. Our kernel is 7.2.6. **The
   claim expired nine years and roughly thirty kernel releases before this
   project asked the user to `sudo` for it.**

### What Somlo's era actually was

| Page revision | QEMU | Host kernel / distro | Guest `-cpu` | SMBIOS |
|---|---|---|---|---|
| 2013-01-22 | kvm-kmod on 3.6+ | Fedora 16+ | `core2duo` | SeaBIOS patches |
| 2014-05-16 | not pinned | Fedora 20, 3.13.6 | `core2duo -machine q35` | `-smbios type=2` |
| 2015-05-04 | **2.1.0+** | Fedora 20, **3.15.3** | `core2duo` | `-smbios type=2` |
| 2016-03-21 | 2.1.0+ | Fedora 20, 3.15.3 | `core2duo,vendor=GenuineIntel` | `-smbios type=2` |
| 2017-05-30 | **2.6.0+** | **≥ 4.7**, Fedora 24 | **`Penryn -smp 4,cores=2`** | **none** |
| FINAL UPDATE 2018-10-21 | 2.6.0+ | ≥ 4.7, Fedora 26 | `Penryn -smp 4,cores=2` | none |

His **host CPU model is never stated anywhere.** He says only that he has run
"on a genuine Mac computer … exclusively since cca. 2006", and his
instructions show `kvm_intel`. Earliest page snapshot 2013-02-24; content
froze at the 2018-10-21 FINAL UPDATE, in which he says he no longer has the
cycles.

**`-cpu Penryn` is dated and attributed on his page: "Thanks Jim Burns for the
Penryn hint, which is needed instead of core2duo *as of Sierra*."** Sierra is
10.12. Our Penryn line does not come from him and is not justified by him.

## 2. The `MacPro5,1` hypothesis is refuted — Somlo used no product name at all

The live guess was that Somlo and OSX-KVM needed `ignore_msrs` because they
ran `MacPro5,1`, which would have explained why they needed a knob we do not.
**It is wrong, in both directions.**

- **Somlo:** from 2014 to 2016 his only SMBIOS argument is the bare
  `-smbios type=2` — no product name, no manufacturer. His page explains why
  it is there at all: a "Type 2 (Baseboard) entry, required for booting
  [Mountain]Lion". From 2017 onward `-smbios` disappears from his command
  lines entirely. The one full SMBIOS string he ever wrote, on qemu-devel
  **2017-04-04**, is `-smbios type=1,manufacturer='Apple Inc.',product='iMac2'`
  — and that was to coax the **Linux** `applesmc` module into loading in a
  **Linux** guest as a test, not to boot macOS.
- **kholia/OSX-KVM:** `Macmini6,2` (2017-10-01 through 2020-03-18), then
  `iMacPro1,1` (**2020-03-19**, commit `59a9825`), then `iMac19,1`
  (**2026-01-26**, commit `4c378a4`, "Support for macOS Tahoe"). **MacPro5,1
  was never its configured `SystemProductName`** — the string appears only
  inside binary Clover blobs.

So the SMBIOS explanation for why they needed the knob and we do not is dead.
The actual explanation is simpler and is above: **they were on pre-4.7
kernels running 10.10.**

## 3. kholia/OSX-KVM never gave a reason, and still has not

Its public history is squashed (45 commits, oldest 2021-02-13); the
pre-squash history survives in 2020-era forks. In the root commit of the
recoverable history — author date **2016-01-26**, commit date **2017-02-04** —
the README already says, in full:

> "Host machine may need the following tweak for this to work,
> `echo 1 > /sys/module/kvm/parameters/ignore_msrs`"

**That is the entire justification, and it has never been improved.** A GitHub
commit search for `repo:kholia/OSX-KVM ignore_msrs` returns `total_count: 0`
— no commit message in the repo's history has ever mentioned it. Current
master (`4c378a4`, 2026-01-26) still says "KVM **may** need the following
tweak on the host machine to work." Ten years, thirteen files, no reason.

Its `kvm.conf` (present since 2021-02-13) ships
`options kvm ignore_msrs=1 report_ignored_msrs=0`, and
`run-diagnostics.sh` (same date) actively nags the user if the setting is
not applied.

**This is the cargo-cult vector, and it has a name and a date.** Nicholas
Sherlock's widely-copied Proxmox guides use byte-identical wording six years
apart — 2016-10-05 (Sierra, QEMU 2.7.1) and 2022-10-25 (Ventura, Proxmox 7.2)
— saying only "run `echo 1 > /sys/module/kvm/parameters/ignore_msrs` to avoid
a bootloop during macOS boot". No MSR number, no message, no version scope.

## 4. Two maintained projects run macOS on KVM without it

- **foxlet/macOS-Simple-KVM** (2019-04-22 → 2020-07-23, 79 commits, history
  intact and unsquashed): **every blob of every commit** was grepped. Zero
  `ignore_msrs`, zero mention of MSRs at all. Targets QEMU 3.1+.
- **royalgraphx/DarwinKVM** (228 commits, 2023-06-11 → 2026-04-12): zero
  occurrences; `git log --all -S ignore_msrs` across all 228 commits returns
  nothing. It uses OpenCore's `ProvideCurrentCpuInfo=True` instead,
  documented since **2023-06-22**: "On KVM and other hypervisors it provides
  precomputed MSR 35h values to avoid some kernel panics."

**`docs.darwinkvm.com` is still a parked page, re-verified 2026-09-21**
(Cloudflare Registrar parking, HTTP 200, no redirect). Prior content could
not be checked — archive.org was offline all session.

## 5. Upstream KVM's own opinion, dated

Paolo Bonzini, **2024-12-19**, commit titled "KVM: x86: let it be known that
ignore_msrs is a bad idea", condemns precisely the configuration OSX-KVM
ships:

> "Running KVM with `ignore_msrs=1` and `report_ignored_msrs=0` is not a
> supported configuration. Lying to the guest about the existence of MSRs may
> cause the guest operating system to hang or produce errors … the user has no
> clue that the guest is being lied to."

Worth noting against our own host: `report_ignored_msrs` is `Y` here, which is
the *supported* half of that pair and is what made the measurement in
`docs/configuration-register.md` possible at all.

## 6. The lead this turned up for G14, which is not about MSRs

`kholia/OSX-KVM`'s 2026-01-26 commit — the same one that moved the SMBIOS from
`iMacPro1,1` to `iMac19,1` — also ships **`AppleMCEReporterDisabler.kext`**,
with `<key>Comment</key><string>Fix kernel panic MacPro SMBIOS</string>` and
`MinKernel 21.0.0`.

So "a `MacPro*` SMBIOS makes macOS panic in a machine-check driver" is a
**documented, named, community-known phenomenon with a remedy** — and the
remedy is neither `ignore_msrs` nor OpenCore's `Kernel > Block`, which is what
was tried here and watched do nothing. It is a codeless kext that overrides the
driver's IOKit personality so it never matches in the first place.

`MinKernel 21.0.0` is macOS 12, so that kext is not aimed at 10.9 and nothing
here says it would load or help. **This is a lead, not a finding.** What it
does establish is that G14's observation is not peculiar to this project, and
that the class of fix known to work on the same symptom operates at the
matching layer rather than the MSR layer.

**Note also that DarwinKVM documents `MacPro5,1` as one of its two supported
SMBIOS configurations** (58 references, alongside `MacPro7,1`). Somebody is
running that model under KVM. On which macOS version, with what else set, is
not established here.
