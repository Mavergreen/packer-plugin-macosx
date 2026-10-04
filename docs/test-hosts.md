# Test hosts, and what each has measured

`docs/host-profile.md`'s ledger records every assumption this project
makes about its host. **Each is a hypothesis, and another host is the only
way to test one.** This file says what each machine has measured, and
which open questions each can still settle.

Two axes, not one. The machines below test *the same software on
different hardware*. The hypervisors at the end would test *the same guest
disk on a different virtualization stack*, a different question.

## The plugin, end to end

MEASURED 2026-09-27 on the primary host (Macmini8,1, i7-8700B, KVM), with
Packer 1.16.1, packer-plugin-qemu 1.1.6, packer-plugin-vagrant 1.1.7,
Vagrant 2.4.9 and vagrant-qemu 0.6.3. The plugin was installed from the
checkout with `bin/dev-install.sh`, into a fresh `PACKER_CACHE_DIR` whose
download cache was seeded with hard links to the pinned files already on
disk, so nothing was downloaded (Packer's log has no fetch URLs).

**A build from a cold cache**, `cd template && packer init . && packer
build .`: **39m40s**, exit 0.
- The data sources took 10 minutes: `mavericks-installesd` 20 s;
  `mavericks-firmware` including OpenCore's build (3m33s, gcc 13.3.0, no
  ccache); `mavericks-media`.
- The install and first boot took 23 minutes, until SSH answered.
- `verify.sh` found 10.9.5 build 13F1911; `OpenSSH_10.5p1, LibreSSL
  4.3.2`; user `vagrant` in admin; the receipt
  `com.apple.pkg.update.security.2016-004Mavericks.13F1911`; `iMac14,2`;
  the first-boot daemon removed.
- `sudo shutdown -h now`, the `shutdown_command`, powered the guest off in
  5 s.
- `output/mavericks-10.9.5-libvirt.box` was 7.7 GB; the cache, 14 GB.

**A second build, reusing everything**, `packer build -force .`:
**30m03s**, exit 0. All three data sources logged "already built" within
the first second; the rest was the install. `-force` is needed because the
qemu builder refuses an existing `output-mavericks/`.

**The box.** `vagrant box add` took 2m09s. `vagrant up --provider qemu`
took 64 s, and Vagrant replaced the insecure key. `vagrant ssh -c sw_vers`
answered 10.9.5 13F1911, and `sudo -n true` succeeded.

**The halt.** Without help, `vagrant halt` was **not clean**: vagrant-qemu
pressed the ACPI power button, which 10.9 does not act on, and after 60 s
sent `quit` to QEMU. That halt took 61 s, exited 1 the first time and 0
the second, and the guest's `system.log` got no `SHUTDOWN_TIME` either
time. With the box's `before :halt` trigger, which runs the guest's own
`sudo /sbin/shutdown -h now`: halts of 3 s and 23 s, exit 0, no `quit`
sent, and `SHUTDOWN_TIME` logged.

**Measured again the same day**, from a fresh seeded cache, with the
template's `${path.root}` paths, the data sources' `recipe` rows and
`verify.sh`'s verdict: `packer init` and `packer build` in
**36m54s**, exit 0 (data sources 9 minutes, SSH after 22 minutes of
install); `verify.sh` said "verify: ok: 10.9.5, first boot finished,
passwordless sudo for vagrant, updates=security as asked";
`vagrant up --provider qemu` 72 s; `vagrant ssh -c sw_vers` 10.9.5
13F1911; `vagrant halt` 4 s, exit 0.

**Measured again 2026-09-27**, with the firmware's debug settings off
(the default; one switch turns them on): `packer build -var
user=builder` from a pre-seeded cache (firmware and media built fresh):
**34m46s**, exit 0; `verify.sh` said "ok: 10.9.5, first boot finished,
passwordless sudo for builder, updates=security as asked". The box's
Vagrantfile rendered from the build's own settings; `vagrant up
--provider qemu` 69 s, SSH username `builder`; `sw_vers` 10.9.5 13F1911;
`sudo -n true` succeeded; `dscl` RealName `builder`; `kern.bootargs` "-v
keepsyms=1" (debug off by default); `vagrant halt` 23 s, exit 0,
`SHUTDOWN_TIME` logged.

Not measured: a box run with a display (`MAVERICKS_DISPLAY`).

**10.6, `templates/snowleopard/`, 2026-10-04**, from the 10A432 retail
disc image (`docs/decisions/0014`), default settings (`updates =
security`, one CPU, XHCI): `packer build -var installer=...` in
**24m41s**, exit 0, with `snowleopard-installer` and the firmware
already built and the media built fresh (data sources 2 minutes; the
install, the first boot with 10.6.8 and 2013-004, and its restart, 22
minutes until SSH answered). `verify.sh` said "verify: ok: 10.6.8,
first boot finished, passwordless sudo for vagrant, updates=security as
asked"; 16 update receipts (13 combo parts, the base system, X11,
2013-004). `vagrant up --provider qemu` 48 s; `vagrant ssh -c sw_vers`
10.6.8 10K549; `sudo -n true` succeeded; `vagrant halt` 7 s. With `-var
updates=none`: **14m08s**, exit 0 (SSH after 12 minutes of install and
first boot); `verify.sh` "ok: 10.6, ... updates=none as asked";
hostname `snowleopard`; `vagrant up --provider qemu` 64 s;
`vagrant ssh -c sw_vers` 10.6 10A432; `vagrant halt` 8 s.

**10.9 again after the 10.6 work, 2026-10-04**, the default
`templates/mavericks/` build from a seeded InstallESD, the media built
fresh: **21m53s**, exit 0; `verify.sh` "ok: 10.9.5, first boot
finished, passwordless sudo for vagrant, updates=security as asked";
13F1911, the 2016-004 receipt.

## The hosts

### `pet-power-plant`: Mac mini 2018, Linux Mint 22.3, primary

i7-8700B, Coffee Lake, 6 cores, 62 GB, T2 kernel, btrfs on NVMe,
repository on NFS; the full profile is `docs/host-profile.md` §1.
Everything was first measured here.

Nested virtualization is available (`kvm_intel.nested = Y`) and the CPU is
Nehalem-or-newer, so **this is the only host that can test the VMware
Fusion requirement** of `docs/decisions/0005`.

### `squirrel-zapper`: MacBook Air 11" 2015, EndeavourOS

i7-5650U Broadwell, 2 cores and 4 threads, 7.8 GiB, stock Arch kernel,
QEMU 11.1.1, gcc 16.2.1, `kvm.ignore_msrs=N`.

**What it settled:**
- **G4 refuted** (2 physical cores), **G6** (another QEMU: 11.1.1),
  **G7** (Apple hardware without the T2 kernel): 2026-09-20.
- **G22, twice**, 2026-09-20: its C23-default gcc failed OpenCorePkg,
  and, with the dialect stated, gcc 16.2.1 failed OVMF under upstream's
  `-Werror`. Both fixed (`docs/decisions/0004`).
- **G23**: its kernel layout (`/lib/modules/<release>/vmlinuz`, `.ko.zst`
  modules, `hfsplus` depending on `cdrom`) broke the privops microVM's
  Debian assumptions; fixed.
- **G20 confirmed portable**, 2026-09-20: media built there passed the
  check of the finished media against Apple's checksums.
- **A complete build and install**, 2026-09-21, gcc 16.2.1: a guest
  installed, booted without installer media and answered SSH. That is
  what verified gcc 16.2.1 in the compiler range. Its `OVMF_CODE.fd` is
  `e3d0c6f5…` against the primary host's `195c4dcf…`, which is what an
  unpinned compiler means.
- **G21 refuted**: installed with `ignore_msrs=N`.
- **G13 and G16 confirmed** under QEMU 11.1.1.

### `ap-juicer`: Mac Pro 1,1, Debian 13

Xeon 5150 (Woodcrest, 2006), 4 cores with one offline, 9.9 GiB, no SSE4.1,
**no EPT**, QEMU 11.0.2, gcc 14.2.0, headless, and the NFS server the other
two hosts mount the repository from.

**What it settled:**
- **The Xeon counter-example for G14**, 2026-09-21: with `-cpu Conroe` and
  SMBIOS `MacPro5,1`, the install stopped on the same text screen for 46
  minutes and never answered SSH, an hour after the same host had
  installed with `iMac14,2`. So `MacPro5,1`'s panic is not about the host
  CPU being a Xeon; the measured cause is `docs/decisions/0010`.
- **G3 and G25**: it refuses the default `-cpu` line under KVM (no
  `sse4.1`, no `sse4.2`) and accepts `Conroe`, as predicted from CPU
  generations before the machine ran anything.
- **`Conroe` installs**: 2026-09-21, a complete install, booted without
  installer media, answering SSH (`docs/decisions/0009`).
- **G26 by measurement**: media built over SSH on a host with no seat,
  with the HFS+ assembly inside the privops microVM.
- **G22**: gcc 14.2.0 verified.
- **G18 refuted** (no EPT), **G10 refuted** (ext2/ext3, no reflinks),
  **G4 refuted** (no SMT), **G12** measured from the server's end
  (0.39 ms per file create on local ZFS against 10–15 ms over NFS).
- **G21 refuted** again, independently: `ignore_msrs=N`.
- **G13 and G16 confirmed** under QEMU 11.0.2.

Because it is the NFS server, a build there competes with the service the
other two hosts depend on for their own repository access. Do not build
there while another host is building.

### Timings across hosts

| | `pet-power-plant` (6 cores, 2018) | `squirrel-zapper` (2 cores, 2015) | `ap-juicer` (3 of 4 cores, 2006, no EPT) |
|---|---|---|---|
| media build | 108–119 s (2026-09-22) | 118 s (2026-09-21) | 426 s (2026-09-21) |
| install, until SSH | 780–963 s (2026-09-18 to 09-22) | 1332 s (2026-09-21) | 1656 s (2026-09-21) |
| OpenCore build, cold | 3m23s (2026-09-17) | 393 s (2026-09-21) | |
| OVMF build, cold | 1m22s (2026-09-17) | 229 s (2026-09-21) | |

The install is 1.7x the primary host's on the 2-core Broadwell and 2.1x on
the 2006 Xeon, less than feared for a machine with no EPT. Budget from the
ratio, not from a modern desktop's wall clock; TCG will be slower still.

### MacBook Air 2017 13", macOS Sequoia: not yet run

Apple hardware running macOS, Intel. What it could test: QEMU with the
Hypervisor framework (`accelerator = "hvf"`), a third accelerator. The
plugin cross-builds for darwin, but `mavericks-media` needs Linux today
(`docs/decisions/0005`), so this host cannot build installer media.

### NetBSD with NVMM: aspirational

A different accelerator entirely (`-accel nvmm`). It would test the
deepest assumption: that KVM is incidental rather than load-bearing.

### Mavericks itself, under `vm-host`: the recursive case

The sibling `mavericks-vm-host` (`Mavergreen/vm-host`) back-ports
Hypervisor.framework to 10.9 and is expected to ship a QEMU with it. When
it does, **Mavericks can host Mavericks**, a host that varies the *era*
rather than the CPU or the distribution. Nothing here depends on it.

## Asking a new host

The deliverable of a run on a new host is not "it worked" but **an
updated ledger**: every row the run speaks to ends up confirmed, refuted
or corrected, with the date and what was observed.

- **Which `-cpu` lines the host can provide** (G3, G25) needs no build: for
  each line, start QEMU paused with no disks, no network and no display,
  under KVM, with `enforce` appended to the line, and quit it. With
  `enforce`, a missing feature is an error that names the feature instead
  of a warning. Under TCG the answer describes the emulator, not the host.
- **Everything else** comes from a build: `PACKER_LOG=1 packer build` in
  `templates/mavericks/`, keeping the log. `verify.sh` prints what the guest is
  (`sw_vers`, the CPU it decided it got, the disk bus, the receipts, the
  OpenSSH version) before it judges it. Record the host's CPU, kernel,
  QEMU and gcc beside the result.
- **One install per host at a time** (G19).
- **G14 is settled**, and G19 needs a deliberate two-install experiment,
  which no ordinary build performs.

## Other hypervisors as targets

The useful split is **QEMU-family or not**, because it decides how much of
this project carries over.

### QEMU underneath: the whole stack carries over

OpenCore, OVMF and the machine all apply; only the configuration format
differs.

| Target | Notes |
|---|---|
| **Vagrant with `vagrant-qemu`** | **Built.** The box is a libvirt-format box carrying the firmware, with a Vagrantfile that wires the machine up (`docs/decisions/0007`). |
| **`vagrant-libvirt`, libvirt, virt-manager** | The box's format is libvirt's, but its Vagrantfile configures `vagrant-qemu`; a libvirt domain would need the same firmware and machine wired up in libvirt's terms. Not tried. |
| **Proxmox VE** | The template's machine maps nearly line for line onto a Proxmox VM config. Not tried. |
| **UTM** | A macOS and iOS front end to QEMU, and where this project's reference configuration came from. Not tried. |
| **Containerised QEMU** (`dockur/macos`, `sickcodes/Docker-OSX`) | QEMU inside a container with `/dev/kvm` passed through. Both fetch Apple's installer at runtime, inside the container, rather than baking it into a published layer, the same line this project draws. Not tried. |

### Not QEMU: only the disk carries over

Each brings its own EFI and SMC emulation, so the boot stack is bypassed
and the macOS disk is the only artifact that crosses.

| Target | Notes |
|---|---|
| **VMware Fusion or Workstation** | Real macOS guest support on Apple hardware; also what `Mavergreen/container-tools` needs inside the guest (`docs/decisions/0005`). |
| **VirtualBox** | The prior art's `VMQemuVGA` and `virtio-net-osx` measurements were taken under it. |
| **Parallels** | macOS hosts only, commercial. |

Testing one of these would answer one question for the whole family: is
the macOS disk a portable artifact, independent of this boot stack?

**One constraint shapes all of it**: a box, a container layer or a disk
image of this guest is never shared (`docs/decisions/0007`). Whatever the
target, the guest is built and run on the same trusted machine.
