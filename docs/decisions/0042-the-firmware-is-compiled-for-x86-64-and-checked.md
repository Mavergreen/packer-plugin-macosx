# 0042: The firmware is compiled for x86-64, and checked

Date: 2026-11-04

Status: Accepted by the owner, 2026-11-04.

## Context

EDK II's tools_def sets no `-march`, so OpenCore and OVMF were compiled
for whatever CPU the host compiler defaults to. Every host this project
had built on defaulted to x86-64 itself. Ubuntu 26.04's gcc 15.2.0
(15.2.0-16ubuntu1) is configured `--with-arch-64=x86-64-v3`: by default it
targets AVX2, BMI and the rest (`gcc -Q --help=target`: `-march=x86-64-v3`,
`-mavx2 [enabled]`). 24.04's gcc 13.3.0 says `-march=x86-64` (mavericks-vm
Actions run 41760803304).

GitHub's `ubuntu-latest` is now 26.04. The first cold base build there that
got past the privops microVM (`docs/decisions/0041`) built OpenCore with it
and booted: OVMF ran and started OpenCore's `Bootstrap`, which took #UD at
RVA 0x2ADA (mavericks-vm Actions run 41751719776, an AMD EPYC runner whose
guest CPU model is Penryn). A probe rebuilt the same firmware on 26.04 and
disassembled that address (run 41761015528): `vmovaps %xmm7,-0xd0(%rbp)`
(`c5 f8 29`), in `_ModuleEntryPointReal`, where the UEFI calling convention
saves the callee-saved XMM registers. An x86-64-v3 compiler saves them with
VEX-encoded moves; Penryn has no AVX. The guest sat at the exception for
the hour the build allows, and the install never began.

Assembly is a different matter. Built with gcc 13, `OpenCore.efi` already
holds 878 AVX instructions, from OpenCore's own `Sha512Avx` routine, and
five more from code with no line information; they run only where CPUID
says they may, and every guest has always booted with them. Of the 123
modules of a gcc 13 build (the five OpenCore ships and every OVMF module),
none holds an instruction above x86-64 whose debug line names a C source.
The gcc 15 `Bootstrap.dll` holds 120.

## Decision

The CPU is stated, like the C dialect (`docs/decisions/0004`): both builds
compile for `-march=x86-64 -mtune=generic`. OpenCore takes it through
upstream's `$(OCPKG_BUILD_OPTIONS)` hook, which `checkFlags` already
confirms reaches the generated makefiles; OvmfPkg takes it from patch
`0007-firmware-state-the-cpu-baseline.patch`.

x86-64, because it is what every guest CPU model offers (the guest's are
Penryn and up; `cpu-isa` adds AVX levels for the guest's own code, never the
firmware's) and what every earlier build was compiled for, so on a host
that was already correct the bytes do not change.

And it is checked, because a stated flag is a claim about the compiler,
not about its output: a distribution's specs file, a wrapper, or the next
default could still put a newer instruction in. After each build,
`internal/firmware/baseline.go` disassembles the debug image of every
module that ships (the five OpenCore artifacts; every OVMF module, since
all of them go into the flash image) with `objdump -d -l -w`, and refuses
the build, shipping nothing, if any instruction the compiler generated
needs more than x86-64: anything VEX, EVEX or XOP encoded, and the SSE3,
SSSE3, SSE4.1, SSE4.2, POPCNT, LZCNT, MOVBE and CMPXCHG16B instructions
by mnemonic. An instruction is the compiler's when its debug line names a
C source; one from a `.nasm`, `.iii`, `.asm` or `.S` source, or from none,
is upstream's hand-written assembly. `tzcnt` is not counted: gcc emits it
for x86-64 itself, as `rep bsf`, which a CPU without BMI runs as BSF.

`objdump` joins the build's required tools (binutils, beside the `ld` and
`objcopy` EDK II already runs).

## Consequences

- Cold builds on Ubuntu 26.04 produce firmware a Penryn guest can run.
  MEASURED 2026-11-04 (mavericks-vm Actions run 41765199515, gcc 15.2.0):
  the stated flag survives the link-time code generation EDK II uses, all
  123 shipped modules pass the check, and Bootstrap's entry point saves its
  XMM registers with `movaps` again.
- On gcc 13.3.0 the eight firmware artifacts are byte-identical with and
  without this change (MEASURED 2026-11-04 on pet-power-plant, the same
  workspace path both times: see the release note).
- The firmware's input listing gains the flags and patch `0007`, so every
  cached guest's key changes and the next build of each is cold.
- A host compiler that leaks a newer instruction into the firmware fails
  the build by name (module, address, instruction, source line) instead
  of building a guest that faults at boot.
- Not covered: an instruction above x86-64 in hand-written assembly that
  does not check CPUID first. That is upstream's code, as it always was.
- `TestScanBaselineFindsTheCompilersInstructionsOnly`,
  `TestAboveBaselineNamesWhatAnInstructionNeeds`,
  `TestOpenCoreRefusesAModuleBuiltForANewerCPU`,
  `TestOVMFRefusesAModuleBuiltForANewerCPU` and
  `TestOpenCoreRefusesACPUBaselineThatDidNotArrive` hold it.
