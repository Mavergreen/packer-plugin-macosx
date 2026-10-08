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
| `Nehalem` | BOOTED 13F34; **LOOPS on 13F1911** | a 13F34 guest booted, primary host, 2026-09-21. A 13F1911 guest (Security Update 2016-004) resets in a loop, on the primary host and on GitHub's Intel and AMD runners alike, 2026-10-06: its kernel reads `MSR_FLEX_RATIO` (`0x194`), which KVM emulates on neither vendor, and the read faults (the `kvm_msr` tracepoint showed no other). With `kvm.ignore_msrs=Y` it boots. Whether 13F34's kernel never reads `0x194`, or something else changed, is not measured |
| `SandyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,enforce` (`avx`) | BOOTED 13F1911 | primary host, KVM with `kvm.ignore_msrs=Y`, and TCG, 2026-10-08 (below) |
| `IvyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,+avx2,+fma,+bmi1,+bmi2,+movbe,+abm,enforce` (`avx2`) | BOOTED 13F1911 | primary host, KVM with `kvm.ignore_msrs=Y`, and TCG, 2026-10-08 (below) |
| `Haswell-noTSX` | **HANGS 13F1911** | no SSH in 180 s under KVM with `kvm.ignore_msrs=Y`, nor in 15 minutes under TCG, primary host, 2026-10-08 (below) |
| `Westmere`, `host`, `qemu64` | NOT TESTED | never booted |

VERIFIED and BOOTED are kept apart on purpose. `docs/decisions/0008`
showed a NIC is build-time state in this guest: "booted with X" and
"installs with X" are different claims. Nothing yet suggests the CPU model
behaves that way, and nothing rules it out.

**Why the default is not `Conroe`**, though it is lower and verified: the
default line has the most installs behind it, on two hosts and two QEMUs.
What would move the default is a second `Conroe` install on a second host.

## The instruction-set levels: `none`, `avx` and `avx2`

A guest can be asked for by what it can run rather than by a model name.
These are the rows, and the box's `MAVERICKS_CPU_ISA` and mavericks-vm's
`cpu-isa` take the same names:

| Level | `-cpu` line |
|---|---|
| `none` | `Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2`, the default |
| `avx` | `SandyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,enforce` |
| `avx2` | `IvyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,+avx2,+fma,+bmi1,+bmi2,+movbe,+abm,enforce` |

All results below were MEASURED 2026-10-08 on the primary host (Coffee Lake
i7-8700B, which has every one of these instructions; QEMU 8.2.2), against a
qcow2 overlay on an installed 13F1911 guest. In each guest,
`isa-probe.py` (Mavergreen/mavericks-vm `tests/`) ran one instruction:
`vxorps ymm` (avx), `vpxor ymm` (avx2), `vfmadd231ps ymm` (fma), `andn`
(bmi1) or `bzhi` (bmi2). "ran" means exit 0; "SIGILL" means the process
died on the instruction.

| Level | Engine | SSH | 10.9 reports | avx | avx2 | fma | bmi1 | bmi2 |
|---|---|---|---|---|---|---|---|---|
| `none` | KVM | 30 s | no AVX; `leaf7_features` empty | SIGILL | SIGILL | SIGILL | ran | ran |
| `none` | TCG | 62 s | the same | SIGILL | SIGILL | SIGILL | SIGILL | SIGILL |
| `avx` | KVM | 30 s | `XSAVE OSXSAVE AVX1.0`; no FMA; `leaf7_features` empty | ran | ran | ran | ran | ran |
| `avx` | TCG | 62 s | the same | ran | SIGILL | SIGILL | SIGILL | SIGILL |
| `avx2` | KVM | 31 s | `FMA MOVBE … AVX1.0 RDRAND F16C`; `leaf7_features` `SMEP ENFSTRG RDWRFSGS BMI1 AVX2 BMI2`; `hw.optional.avx2_0`, `bmi1`, `bmi2` all 1 | ran | ran | ran | ran | ran |
| `avx2` | TCG | 58 s | `leaf7_features` `SMEP ENFSTRG RDWRFSGS BMI1 AVX2 BMI2` | ran | ran | ran | ran | ran |

### 1. Under KVM a level is what the guest is told, not what it can run

A CPU model sets what CPUID tells the guest. Under KVM the guest's code
runs on the host's CPU, and VT-x and AMD-V have no switch that takes an ISA
extension away. The one gate is the XSAVE state the guest's kernel turns
on, and AVX, AVX2 and FMA share one state component (YMM). A guest told
"no AVX" never enables it, so on `none` the whole AVX family faults. A
guest told "AVX" enables YMM for all three. BMI needs no state at all. So
on an AVX2 host, `none` still runs BMI, and `avx` runs everything. The same
holds, REASONED from VT-x and AMD-V rather than measured, for any hardware
accelerator: HVF, NVMM, WHPX.

TCG, QEMU's emulator, implements only what the model says, so there the
levels are exact. It costs about 4-6x on CPU-bound work: SHA-256 of 128 MiB
in the guest took 4-6 s under TCG `avx`, against 0-1 s under KVM `none`. A
Python loop took 4 s against 1 s.

**Why KVM is the default anyway.** The first consumer, Mavergreen/avxemu,
decides from CPUID, never from faults. Whether to arm at all is
`cpu_has_everything()`, and which sites to rewrite is `detect_features()` /
`tramp_faults()`. So on KVM `avx` it arms and trampolines its sites as it
would on a real Sandy Bridge. What KVM cannot exercise is its SIGILL
fallback, and a site it misses: those run natively.

### 2. `avx2` cannot be a Haswell model

- `Haswell-noTSX` hangs this guest, under KVM (with `kvm.ignore_msrs=Y`) and
  under TCG alike. 10.9 makes a Haswell model the Haswell cpufamily. Under
  KVM, the last thing the host saw was the guest reading MSRs `0x621`,
  `0x690`, `0x6b0`, `0xe7` and `0xe8`, which `ignore_msrs` answered with 0.
- Sandy Bridge plus the Haswell flags boots, and CPUID tells user code AVX2,
  BMI1 and BMI2. But 10.9 never reads leaf 7 on it: xnu-2422.115.4's
  `osfmk/i386/cpuid.c` reads leaf 7 only `if (info_p->cpuid_model >=
  CPUID_MODEL_IVYBRIDGE)`. Its sysctls and commpage then say no AVX2.
- Ivy Bridge plus the flags is both: it boots, and 10.9 reports them.

### 3. One table for every engine

`enforce` makes QEMU refuse a feature it cannot give, instead of starting a
guest that quietly tests as a lower level. QEMU 8.2.2's TCG cannot give `x2apic` or
`tsc-deadline` (nor Haswell's `pcid` and `invpcid`), and none of them is
part of the instruction set. So the lines drop them, and the same line
starts under KVM and TCG with `enforce` (checked against paused VMs). HVF
and NVMM are not measured: whether they accept these models, and how they
answer MSR `0x194` and the others without an `ignore_msrs`, is open.

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

**Under TCG this is about the emulator, not the host.** The emulator
implements SSE4.1 itself whatever the host has, so every ISA row passes
there; what it refuses is the few non-ISA features it lacks (see "One
table for every engine").

## What was not measured

- **`Nehalem`, `Westmere`, `SandyBridge`, `IvyBridge`** installing. The levels
  are runtime only: the image is installed on the default line.
- **The levels under HVF and NVMM**, and on AMD hosts (GitHub's runners
  will answer AMD).
- **Throughput per model.** Only KVM against TCG was compared (above), not one model against another.
