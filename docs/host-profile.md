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
## 3. Host state changes

Anything here needed `sudo`, so it was the user's to make. Recorded with
whether it survives a reboot and how to revert it.

| Date | Change | Persistent? | Revert |
|---|---|---|---|
| 2026-09-17 | `shellcheck` installed on the primary host, for the repository's tests | yes | `sudo apt remove shellcheck` |
## 4. Generalization ledger

**Each row is a hypothesis until a second host has tried to falsify it.**
`docs/test-hosts.md` says which machine can settle which row. A row ends
up confirmed host-specific, demoted to portable, or corrected; a row
nobody has tried to falsify is not knowledge.

| # | Assumption, and what is known | What another host would need |
|---|---|---|
| G1 | The host is Apple hardware, so running OS X in a VM is licensed. | Non-Apple hosts are outside Apple's licence. A legal constraint, not a technical one. |
| G2 | An Intel CPU with VT-x. **INHERITED**: the brief this project started from treated AMD as a hard stop, and AMD is a known-harder case for macOS guests. `mavericks-media` refuses an AMD host by name. Nothing here has run on one. | An AMD host that installs a guest would move this. |
