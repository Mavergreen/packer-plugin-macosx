# Host profile and generalization ledger

Two jobs. It records what the hosts this project has run on actually are,
and it keeps every host-specific assumption in one list (§4), so that
porting to another host is a matter of working through the list rather
than rediscovering what was baked in.

**Anything that relies on something specific to a host goes in §4.**

## 1. Primary host, probed 2026-09-17

| | |
|---|---|
| Model | `Macmini8,1` (Mac mini 2018), board `Mac-7BA5B2DFE22DDD8C` |
| Vendor | Apple Inc.: **the host is Apple hardware** |
| CPU | Intel Core i7-8700B @ 3.20 GHz, Coffee Lake, family 6 model 158 stepping 10 |
| Topology | 1 socket, 6 cores, 2 threads per core, 12 logical CPUs, 1 NUMA node |
| Virtualization | VT-x; `vmx`, `ept`, `vpid`, `ept_ad`; `kvm_intel.nested = Y` |
| Notable ISA | `avx`, `avx2`, `aes`, `rdrand`, `rdseed`, `bmi1`, `bmi2`, `mpx`, `intel_pt`, `xsaves`; **no AVX-512** |
| RAM | 62 GiB |
| Storage (local) | `/dev/nvme0n1p2`, **btrfs**, on `/` and `/home`: 1.9 TB |
| Storage (repository) | **NFSv3** `ap-juicer:/export/code/trees` on `~/Documents/trees`, `vers=3,proto=tcp,hard,rsize=wsize=1M` |
| GPU | Intel UHD 630 (CoffeeLake-H GT2) `[8086:3e9b]` at `00:02.0`, **the only display device** |
| T2 | Apple T2 Bridge Controller `[106b:1801]` and Secure Enclave `[106b:1802]` |
| OS | Linux Mint 22.3 "Zena" (Ubuntu/Debian derived) |
| Kernel | `7.2.6-1-t2-noble`: **a T2-patched kernel, not stock Ubuntu** |
| Hostname | `pet-power-plant` |
| `/dev/kvm` | `crw-rw----+ root:kvm`; the user is in group `kvm` |
| QEMU | 8.2.2 (Debian `1:8.2.2+ds-0ubuntu1.18`) |
| Compiler | gcc 13.3.0 (Ubuntu `13.3.0-6ubuntu2~24.04.1`), target `x86_64-linux-gnu`, **defaults to `-std=gnu17`**, which is why the C23 breakage in G22 was invisible here |
| IOMMU | enabled, 14 groups |
| `kvm.ignore_msrs` | `Y` since 2026-09-17, not persistent; not needed (§3, G21) |
| `net.ipv4.ping_group_range` | `1 0`, an empty range: QEMU's user-mode networking cannot carry `ping` here, so a guest's `ping` fails for a host reason. Test a guest's network with TCP |

Consequences:

- **Licensing is clean.** Apple's licence permits virtualizing OS X on
  Apple-branded hardware.
- **No GPU passthrough.** One iGPU, no PCIe slots:
  `docs/decisions/0001-no-gpu-passthrough.md`.
- **The repository lives on NFS; the build's state must not.**
  `docs/decisions/0003-build-state-on-local-storage.md`.

## 2. The other hosts

| Host | Hardware | OS, kernel | QEMU | gcc | Notes |
|---|---|---|---|---|---|
| `squirrel-zapper` | MacBook Air 11" 2015, i7-5650U (Broadwell), **2 cores, 4 threads**, 7.8 GiB | EndeavourOS, `7.2.6-arch2-1`, unpatched | **11.1.1** | **16.2.1** | Apple hardware without the T2 kernel. btrfs with reflinks; repository on NFS. `kvm.ignore_msrs=N` |
| `ap-juicer` | Mac Pro 1,1, **Xeon 5150 (Woodcrest, 2006)**, 4 cores of which **one is offline** (`CPU3 failed to report alive state` at SMP boot), 9.9 GiB, **VT-x without EPT**, no SSE4.1 | Debian 13 trixie, `7.1.8+deb13-amd64` | **11.0.2** | **14.2.0** | Headless, reached over SSH. **The NFS server**: the repository is on its local ZFS; images on ext2/ext3, no reflinks. `kvm.ignore_msrs=N`. Needed `usermod -aG kvm` before `/dev/kvm` was writable |

What each host measured is in `docs/test-hosts.md`.

Other hardware, with a role and no runs yet:

| Host | Role |
|---|---|
| Apple Silicon Mac | A future CI-like host: TCG only, faster and less constrained than GitHub's 3-core, 7 GB runners, so no substitute for one |
| Intel Mac running macOS | Ran Mavericks Forever's `get.sh` unmodified on 2026-09-17 to make the Mac-built reference installer media the Linux-built media is compared against |
| Mavericks-capable Mac | Ground truth for how 10.9 behaves on real hardware |

## 3. Host state changes

Anything here needed `sudo`, so it was the user's to make. Recorded with
whether it survives a reboot and how to revert it.

| Date | Change | Persistent? | Revert |
|---|---|---|---|
| 2026-09-17 | `kvm.ignore_msrs=1` on the primary host (`echo 1 \| sudo tee /sys/module/kvm/parameters/ignore_msrs`). **Not needed, and nobody should be asked to set it**: see G21 and `docs/prior-art.md`. It is still `Y` only because nobody has turned it off | no, resets on reboot | `echo 0 \| sudo tee /sys/module/kvm/parameters/ignore_msrs`, or reboot |
| 2026-09-17 | `shellcheck` installed on the primary host, for the repository's tests | yes | `sudo apt remove shellcheck` |
| 2026-09-17 | `nasm` 2.16.01 and `acpica-tools` (`iasl` 20230628) installed on the primary host, for the firmware build | yes | `sudo apt remove nasm acpica-tools` |
| 2026-09-21 | `usermod -aG kvm` for the user on `ap-juicer` | yes | `sudo gpasswd -d $USER kvm` |

## 4. Generalization ledger

**Each row is a hypothesis until a second host has tried to falsify it.**
`docs/test-hosts.md` says which machine can settle which row. A row ends
up confirmed host-specific, demoted to portable, or corrected; a row
nobody has tried to falsify is not knowledge.

| # | Assumption, and what is known | What another host would need |
|---|---|---|
| G1 | The host is Apple hardware, so running OS X in a VM is licensed. | Non-Apple hosts are outside Apple's licence. A legal constraint, not a technical one. |
| G2 | An Intel CPU with VT-x, or an AMD CPU with AMD-V. **MEASURED** 2026-10-06 on GitHub's hosted runners (Mavergreen/mavericks-vm Actions runs 37519070682 and 37524541544), which are AMD EPYC (7763, 9V74, 9V45) or Intel Xeon (8370C, 8573C) by chance: Mavericks built and booted on both. AMD needs **`vendor=GenuineIntel`** on the guest's `-cpu` line: KVM gives a guest the host's own vendor unless the line names one, and without it 10.9 hung at OpenCore's "Loading kernel cache file" on three AMD boots of three and the one AMD install (run 37524541544); on Intel it names the vendor the guest gets anyway. AMD does **not** need `kvm.ignore_msrs`: run 37519070682 installed and booted with it off. A guest installed on AMD booted on Intel (run 37519070682). Snow Leopard has not run on AMD. | A guest that fails on one vendor with this `-cpu` line and boots on the other. |
| G3 | **MEASURED 2026-09-21: the guest CPU must be masked down, and it can go much further down than the default implies.** `-cpu Conroe` (SSSE3, no SSE4.1 or SSE4.2) boots an installed guest here, and **installed** one on `ap-juicer` the same day. **10.9 does not need SSE4.1.** `+ssse3` and `+sse4.1` are redundant with `Penryn`; `+sse4.2` is not (`docs/decisions/0009`). | A host that cannot provide SSE4.1 is not excluded: it takes a lower `cpu`. `ap-juicer` refuses the default line under KVM and installs with `Conroe`. |
| G4 | 6 physical cores, SMT siblings identifiable. **REFUTED 2026-09-20** on `squirrel-zapper` (2 cores, 4 threads) and on `ap-juicer` (3 online of 4, no SMT). | Any tuned CPU count has to be a rule derived from the host's topology, not a number. |
| G5 | **RESOLVED by design.** A distribution's OVMF at `/usr/share/OVMF/` varies by distribution. `mavericks-firmware` builds its own from pinned source (`docs/decisions/0004`), so no distribution OVMF is read. | Nothing, unless a host cannot build EDK II at all, which is a build-prerequisite question. |
| G6 | QEMU 8.2.2. **REFUTED 2026-09-20**: `squirrel-zapper` runs 11.1.1 and `ap-juicer` 11.0.2, and both installed a guest. The device findings (G13, G16) held on both. | The throughput numbers of G24 are still 8.2.2's alone. |
| G7 | The T2-patched kernel. **REFUTED 2026-09-20**: `squirrel-zapper` is Apple hardware on a stock kernel, which separates the two variables the primary host confounds. | Anything seen here and not there is evidence the T2 patches caused it. |
| G8 | 62 GB of RAM and TBs free: no pressure on sizes. | `squirrel-zapper` (7.8 GiB) and `ap-juicer` (9.9 GiB) built and installed with the default 4096 MB guest. A 7 GB CI runner has not been tried. |
| G9 | The repository is on NFS, so the build's state must be elsewhere. **MEASURED**: NFS costs 60 against 567 MB/s and 18 against 0.06 ms per file create; see G12. The data sources keep everything in Packer's cache directory, local by default (`docs/decisions/0003`). | A host whose repository is local needs no split; one whose home directory is on NFS should point `PACKER_CACHE_DIR` at local storage. |
| G10 | btrfs reflinks. **RESOLVED by design**: nothing depends on them. The data sources hard-link within their cache directory and copy when a link is not possible. | Nothing. |
| G11 | btrfs copy-on-write fragments a qcow2 written in place, which `chattr +C` on its directory, before anything is written, avoids. **REASONED**; nothing here sets it. | Not relevant off btrfs. |
| G12 | **The NFS penalty is NFS's, measured from both ends on 2026-09-21**: `ap-juicer`, which serves the export, creates a file in its local ZFS copy in **0.39 ms**, against **10–15 ms** for the same files over NFS on the two client hosts. | A host with the repository on local disk has no penalty. |
| G13 | **CONFIRMED, portable.** The guest needs **EHCI with UHCI companions**, not `qemu-xhci`: 10.9's `AppleUSBXHCI` cannot drive QEMU's XHCI (MEASURED 2026-09-17). Guests installed over EHCI+UHCI under QEMU 8.2.2, 11.0.2 and 11.1.1. | Nothing: a property of the guest OS. |
| G14 | **SMBIOS must not be `MacPro5,1` under KVM, and the cause is measured.** The panic's register dump shows `RCX=0x280`, `IA32_MC0_CTL2`, the first CMCI control register; KVM injects `#GP` there without consulting `ignore_msrs` since Linux 6.0, and QEMU never sets `MCG_CMCI_P`. It panicked on a Coffee Lake **and on the Xeon** `ap-juicer`, and boots under TCG. Three reasoned explanations were refuted on the way (the driver wanting a Xeon; an MSR `ignore_msrs` would paper over; `MacPro5,1` being what Somlo and OSX-KVM ran). `docs/decisions/0010`. | **A falsifier written in advance:** on a host kernel older than 6.0 with `ignore_msrs=1`, `MacPro5,1` should boot, and `dmesg` should name `0x280`. No host here is that old. |
| G15 | **RESOLVED by design.** EFI variables persist: the firmware is a split CODE/VARS pflash pair, and each build writes its own copy of the variable store (MEASURED 2026-09-17: 0 variables to 24 across power cycles, including ones macOS wrote). | Nothing. |
| G16 | **CONFIRMED on three QEMUs.** `ide-hd` on q35's `ide.N` presents to the guest as **SATA/AHCI**, not legacy IDE; the guest reports connection bus SATA. | True of q35 generally; another machine type would change it. |
| G17 | `kvm_intel.nested = Y` here, so nested virtualization is available without configuration. | Other hosts may have it disabled: it is a module parameter. Needed for VMware Fusion in the guest (`docs/decisions/0005`). |
| G18 | The default guest CPU, `Penryn`, predates EPT. **`ap-juicer` has no EPT at all**, so nested virtualization is out of reach there whatever the `cpu`. `Nehalem`, the first model with EPT, boots an installed guest here (MEASURED 2026-09-21). | If VMware Fusion needs EPT, the work is to install on `Nehalem` and make it the default for that use. |
| G19 | **Two concurrent guest installs wedge one of them.** Observed once, 2026-09-18: the second install stopped writing to its target disk 6 minutes in and never resumed, while its QEMU kept running and the installer's spinner kept animating. Alone afterwards, the same build completed in 940 s. Memory, disk space, shared paths and the privops microVM were ruled out. **Cause unproven.** | The two candidate causes generalize in opposite directions: this host's storage saturating (host-specific) or 10.9's `AppleAHCI` not recovering from a timeout (portable). To settle it: re-run the pair with `cache=none`, or on different devices, and watch `/proc/<pid>/io` of the stalled guest. **Until then, one install per host.** |
| G20 | **CONFIRMED portable 2026-09-20 on `squirrel-zapper`: a second writer to installer media corrupts it, and only a check of the finished media notices.** Three media builds in six on 2026-09-17/18 carried a corrupt `Essentials.pkg`, in a different place each time (about 110 MB into the file in one, 1.99 GB in another), while the copy reported success and a read-back through the writing mount passed. The logs of that time record concurrent use of the image file; ten builds afterwards at the same geometry, one at a time on an idle host, were byte-perfect (401,920 file comparisons against the Mac-built reference, zero mismatches). The installer's `offset=13899638` in both failures is where `Essentials.pkg`'s `Payload` member begins, a constant of the file, not a location of the fault. | **Portable, like G13**: a property of the file and the filesystem. What any host needs, and what the plugin does: `snapshot=on` wherever media is attached to a guest; one builder per media (the store's and the build's locks refuse a second); and a check of the finished media by a fresh microVM, with no page cache shared with the writer, against Apple's own checksums (`assets/pins/apple-packages.sha256`). That check passed on `squirrel-zapper` and `ap-juicer`. |
| G21 | **REFUTED 2026-09-21, and obsolete: `kvm.ignore_msrs=1` is not required.** `squirrel-zapper` and `ap-juicer`, both with `ignore_msrs=N`, installed guests that answered SSH. The claim was Somlo's, about MSR `0x199` on Yosemite 10.10 on kernels older than 4.7, fixed in Linux 4.7 (2016-07-24); see `docs/prior-art.md`. MEASURED here with `report_ignored_msrs=Y`: every MSR it papered over for a macOS guest is power or energy telemetry, and `0x199` never appears (`docs/configuration-register.md` §1). | **What would bring it back:** a named MSR in `dmesg` with `report_ignored_msrs=1`, and a guest that misbehaves without it. |
| G22 | **The host C compiler builds the boot stack, and nothing pins it.** **REFUTED twice on 2026-09-20 on `squirrel-zapper`**: a C23-default gcc failed OpenCorePkg (`typedef BOOLEAN bool`), and, with the dialect stated, gcc 16.2.1 failed OVMF on a new warning under upstream's `-Werror`. Both fixed: `-std=gnu17` stated, `-Wno-error` appended, the eight artifacts unchanged on gcc 13.3.0. **A declared range** now stands in for a pin: **gcc 13 through 16, verified at 13.3.0 (here), 14.2.0 (`ap-juicer`, 2026-09-21) and 16.2.1 (`squirrel-zapper`, 2026-09-21, a complete build and install)**. The same sources still compile to different bytes on different compilers: `OVMF_CODE.fd` `e3d0c6f5…` on 16.2.1 against `195c4dcf…` on 13.3.0. `docs/decisions/0004`. | Every host tests it by building. A host outside the range is told so before it builds, not in a checksum diff. |
| G23 | **RESOLVED by design.** The host's kernel image was assumed to be at `/boot/vmlinuz-$(uname -r)`, the Debian spelling. On `squirrel-zapper` it is under `/lib/modules/<release>/vmlinuz`, its modules are `.ko.zst`, and `hfsplus.ko` needs `cdrom` loaded first. The privops backend now searches for the kernel, version-keyed paths first (a generically named kernel of another version would reject the running release's modules), decompresses modules, loads their dependencies, and names every requirement it cannot meet. Busybox must be **static**; the backend reads its ELF headers to check. | A new distribution's kernel layout. The class is the finding: a path that exists on the host you wrote it on is not a fact about Linux. |
| G24 | **The NIC ranking is QEMU's, and only QEMU 8.2.2 has been asked.** MEASURED 2026-09-21 here with user-mode networking: `usb-net` 1.24 MB/s down and 1.27 up, `e1000-82545em` 174 and 23.3, `virtio-net-pci` no interface. The guest names the cause (CDC-ECM reports `10baseT`, the Intel NIC `1000baseT`), so the ranking should be the device models' and 10.9's, and portable; the ceiling is this CPU's. `docs/decisions/0008`. | A host with another QEMU, measured the same way. A host whose QEMU cannot offer the device refutes the default outright. |
| G25 | **Every `-cpu` line in `docs/decisions/0009`'s table can be provided by the host.** **REFUTED on both hosts that answered**: here QEMU's own `qemu64` model asks for AMD's `svm` bit on an Intel machine; `ap-juicer` refuses every `Penryn` row (no `sse4.1`) and everything newer, and accepts `Conroe`, `host` and `qemu64`. | Answered in a fraction of a second per row with QEMU's `enforce` against a paused, diskless VM. Only meaningful under a hardware accelerator: under TCG every row passes, because the emulator implements the features itself. |
| G26 | **RESOLVED by design, and MEASURED on a headless host.** Building media with a loop device and an HFS+ mount would need udisks2, whose polkit policy grants that to a user *at a seat*: on `ap-juicer` over SSH it answered `NotAuthorizedCanObtain`. The whole HFS+ assembly happens inside the privops microVM, genuinely uid 0, with the source images attached as further virtio disks, so **the host attaches no loop device and mounts nothing**. MEASURED 2026-09-21: `ap-juicer` built media over SSH with nobody logged in, and a guest installed from it. The cost, measured here: 117 s, against 79 s for a build through a host mount, all of it busybox's `sha256sum` being about 2.7x slower than coreutils' on the two package checks. | Nothing: no seat, no udisks2, no loop devices, no rsync. |
