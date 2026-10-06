# 0009 — The guest CPU is a variable, and Mavericks does not need SSE4.1

Date: 2026-09-21
Status: accepted, on measurement

The `-cpu` line `Penryn,+ssse3,+sse4.1,+sse4.2` came from the UTM bundle
that booted 10.9 under TCG before this project built its own boot stack
(INHERITED). Every host that ran it afterwards was newer than the model it
names, so which part of it Mavericks requires could not come up by
accident. It matters because of one machine: the Mac Pro 1,1 is a 2006
Woodcrest Xeon with no SSE4.1, and "Penryn with SSE4.1" was read as "this
host cannot run the project".

## Decision

**The template's `cpu` variable takes any QEMU `-cpu` line** (a model,
then comma-separated `+flag`, `-flag` or `key=value`). The table below is
guidance, not a whitelist. The box's Vagrantfile is rendered with the same
line.

**The default is `Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2`.**
`vendor=GenuineIntel` was added on 2026-10-06 for AMD hosts, where KVM
would otherwise give the guest the host's AuthenticAMD and 10.9 hangs
before its kernel prints a line; on Intel it names what the guest gets
anyway (`docs/host-profile.md` G2). The measurements below predate it.

## The measurements

MEASURED 2026-09-21 on the primary host (`pet-power-plant`, i7-8700B,
QEMU 8.2.2, KVM): one changed `-cpu` line, a qcow2 overlay on an installed
guest so the guest itself was never written, and the guest asked over SSH
what it got:

| `-cpu` line | SSH | guest's `machdep.cpu.features` | SHA-256 of 64 MiB of zeros |
|---|---|---|---|
| `Penryn,+ssse3,+sse4.1,+sse4.2` | 20 s | …SSSE3 CX16 **SSE4.1 SSE4.2** | correct |
| `Penryn` | 20 s | …SSSE3 CX16 **SSE4.1** | correct |
| `Conroe` | 20 s | …**SSSE3** | correct |

All three reported `10.9.5 (13F34)` and hashed 64 MiB to
`3b6a07d0…c421351`, the right answer, so each guest did real work rather
than merely reaching a login window. MEASURED the same way, the same day,
`Nehalem` booted too: SSH in 40 s, `Intel Core i7 9xx (Nehalem Class Core
i7)`, `POPCNT` gained.

### 1. Mavericks does not need SSE4.1

`Conroe` is SSSE3 without SSE4.1, the feature set of the Mac Pro 1,1's
Woodcrest. The guest booted on it and said so itself: no `SSE4.1` in the
list it printed. 10.9's floor is SSSE3, which is what was always cited;
the SSE4.1 in the default line came from the bundle, not from the OS.

### 2. Two of the three flags are redundant, and the third is not

Bare `Penryn` already reports SSSE3 and SSE4.1, so `+ssse3` and `+sse4.1`
ask for what the model gives anyway. `+sse4.2` does not: QEMU's
`Penryn-v1` has no SSE4.2, because real Penryn had none (it arrived with
Nehalem in 2008). The line asks for a feature the CPU it names never had.

### 3. `Conroe` installs, on the machine it was for

MEASURED 2026-09-21 on `ap-juicer` (Mac Pro 1,1, Xeon 5150, QEMU 11.0.2,
KVM): with `-cpu Conroe`, a guest installed unattended, booted without
installer media and answered SSH. The host's own CPU refuses the default
line under KVM (below), so on that host `Conroe` is not an option but the
only way in.

## The table

| `-cpu` line | Status | Evidence |
|---|---|---|
| `Penryn,+ssse3,+sse4.1,+sse4.2` (default) | **VERIFIED** | complete installs on the primary host (QEMU 8.2.2) and `squirrel-zapper` (QEMU 11.1.1) |
| `Conroe` | **VERIFIED** | a complete install on `ap-juicer` (QEMU 11.0.2), 2026-09-21 |
| `Penryn` | BOOTED | an installed guest booted, primary host, 2026-09-21 |
| `Nehalem` | BOOTED | an installed guest booted, primary host, 2026-09-21 |
| `Westmere`, `SandyBridge`, `IvyBridge`, `Haswell-noTSX`, `host`, `qemu64` | NOT TESTED | never booted |

VERIFIED and BOOTED are kept apart on purpose. `docs/decisions/0008`
showed a NIC is build-time state in this guest: "booted with X" and
"installs with X" are different claims. Nothing yet suggests the CPU model
behaves that way, and nothing rules it out.

**Why the default is not `Conroe`**, though it is lower and verified: the
default line has the most installs behind it, on two hosts and two QEMUs.
What would move the default is a second `Conroe` install on a second host.

## What a host can provide

Which lines a host can hand to a guest under KVM is a property of the host
CPU and its QEMU, not of Mavericks. QEMU's `enforce` flag makes a missing
feature an error instead of a warning, which answers the question in a
fraction of a second against a paused VM with no disks.

- **The primary host** (Coffee Lake, QEMU 8.2.2) accepts every row but
  `qemu64`, which QEMU's own model defines with `CPUID.80000001H:ECX.svm`,
  AMD's virtualization bit, on an Intel machine. Nothing 10.9 needs is
  involved.
- **`ap-juicer`** (Woodcrest, QEMU 11.0.2) accepts `Conroe`, `host` and
  `qemu64`, and refuses every `Penryn` row (missing `sse4.1`, and for the
  default `sse4.2` too) and everything newer.

Name the missing feature when a line is refused: a bare "rejected" reads
as "this host is too old", which is the misreading that wrote the Mac Pro
off.

**Under TCG none of this means anything.** The emulator implements SSE4.1
itself whatever the host has, so every row passes there.

## What was not measured

- **`Nehalem`, `Westmere`, `SandyBridge`, `IvyBridge`, `Haswell-noTSX`**
  installing; the newer rows booting at all.
- **Throughput per model.** Nothing here compared performance.
