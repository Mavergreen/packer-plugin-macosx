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
| 2026-09-17 | `shellcheck` installed on the primary host, for the repository's tests | yes | `sudo apt remove shellcheck` |
| 2026-09-17 | `nasm` 2.16.01 and `acpica-tools` (`iasl` 20230628) installed on the primary host, for the firmware build | yes | `sudo apt remove nasm acpica-tools` |
## 4. Generalization ledger

**Each row is a hypothesis until a second host has tried to falsify it.**
`docs/test-hosts.md` says which machine can settle which row. A row ends
up confirmed host-specific, demoted to portable, or corrected; a row
nobody has tried to falsify is not knowledge.

| # | Assumption, and what is known | What another host would need |
|---|---|---|
| G1 | The host is Apple hardware, so running OS X in a VM is licensed. | Non-Apple hosts are outside Apple's licence. A legal constraint, not a technical one. |
| G2 | An Intel CPU with VT-x. **INHERITED**: the brief this project started from treated AMD as a hard stop, and AMD is a known-harder case for macOS guests. `mavericks-media` refuses an AMD host by name. Nothing here has run on one. | An AMD host that installs a guest would move this. |
| G5 | **RESOLVED by design.** A distribution's OVMF at `/usr/share/OVMF/` varies by distribution. `mavericks-firmware` builds its own from pinned source (`docs/decisions/0004`), so no distribution OVMF is read. | Nothing, unless a host cannot build EDK II at all, which is a build-prerequisite question. |
| G14 | **SMBIOS must not be `MacPro5,1` under KVM, and the cause is measured.** The panic's register dump shows `RCX=0x280`, `IA32_MC0_CTL2`, the first CMCI control register; KVM injects `#GP` there without consulting `ignore_msrs` since Linux 6.0, and QEMU never sets `MCG_CMCI_P`. It panicked on a Coffee Lake **and on the Xeon** `ap-juicer`, and boots under TCG. Three reasoned explanations were refuted on the way (the driver wanting a Xeon; an MSR `ignore_msrs` would paper over; `MacPro5,1` being what Somlo and OSX-KVM ran). `docs/decisions/0010`. | **A falsifier written in advance:** on a host kernel older than 6.0 with `ignore_msrs=1`, `MacPro5,1` should boot, and `dmesg` should name `0x280`. No host here is that old. |
| G15 | **RESOLVED by design.** EFI variables persist: the firmware is a split CODE/VARS pflash pair, and each build writes its own copy of the variable store (MEASURED 2026-09-17: 0 variables to 24 across power cycles, including ones macOS wrote). | Nothing. |
| G17 | `kvm_intel.nested = Y` here, so nested virtualization is available without configuration. | Other hosts may have it disabled: it is a module parameter. Needed for VMware Fusion in the guest (`docs/decisions/0005`). |
| G18 | The default guest CPU, `Penryn`, predates EPT. **`ap-juicer` has no EPT at all**, so nested virtualization is out of reach there whatever the `cpu`. `Nehalem`, the first model with EPT, boots an installed guest here (MEASURED 2026-09-21). | If VMware Fusion needs EPT, the work is to install on `Nehalem` and make it the default for that use. |
| G22 | **The host C compiler builds the boot stack, and nothing pins it.** **REFUTED twice on 2026-09-20 on `squirrel-zapper`**: a C23-default gcc failed OpenCorePkg (`typedef BOOLEAN bool`), and, with the dialect stated, gcc 16.2.1 failed OVMF on a new warning under upstream's `-Werror`. Both fixed: `-std=gnu17` stated, `-Wno-error` appended, the eight artifacts unchanged on gcc 13.3.0. **A declared range** now stands in for a pin: **gcc 13 through 16, verified at 13.3.0 (here), 14.2.0 (`ap-juicer`, 2026-09-21) and 16.2.1 (`squirrel-zapper`, 2026-09-21, a complete build and install)**. The same sources still compile to different bytes on different compilers: `OVMF_CODE.fd` `e3d0c6f5…` on 16.2.1 against `195c4dcf…` on 13.3.0. `docs/decisions/0004`. | Every host tests it by building. A host outside the range is told so before it builds, not in a checksum diff. |
| G23 | **RESOLVED by design.** The host's kernel image was assumed to be at `/boot/vmlinuz-$(uname -r)`, the Debian spelling. On `squirrel-zapper` it is under `/lib/modules/<release>/vmlinuz`, its modules are `.ko.zst`, and `hfsplus.ko` needs `cdrom` loaded first. The privops backend now searches for the kernel, version-keyed paths first (a generically named kernel of another version would reject the running release's modules), decompresses modules, loads their dependencies, and names every requirement it cannot meet. Busybox must be **static**; the backend reads its ELF headers to check. | A new distribution's kernel layout. The class is the finding: a path that exists on the host you wrote it on is not a fact about Linux. |
