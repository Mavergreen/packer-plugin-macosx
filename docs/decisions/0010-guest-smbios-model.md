# 0010 — The guest's SMBIOS model is a variable, and why `MacPro5,1` panics

Date: 2026-09-21
Status: accepted, on measurement. The default is `iMac14,2`.

## Decision

**The template's `smbios` variable, passed to `mavericks-firmware`, sets
the guest's SMBIOS product name** in the `config.plist` written onto the
OpenCore EFI image. `internal/firmware/smbios.go` carries a table of the
models there is evidence for, each with its status and evidence; the table
is guidance, and an unlisted model is logged as such and built. The one
thing refused is a value that cannot be written into the plist safely
(anything but letters, digits, comma, dot, dash and underscore, or more
than 64 characters).

**The default is `iMac14,2`**, the model with completed installs behind it
on three hosts and three QEMUs (`docs/test-hosts.md`). The installed guest
reports `hw.model=iMac14,2`, so the setting reaches the installed system
and not only the installer.

## What `smbios` changes, and what it leaves alone

OpenCore's `PlatformInfo > Generic` has seven fields. `smbios` touches
**one**:

| Field | What `smbios` does | Why |
|---|---|---|
| `SystemProductName` | **changes it** | what the guest reports as `hw.model`, and what a kext matches on |
| `SystemSerialNumber` (`W00000000001`) | nothing | a placeholder, valid for no Mac |
| `MLB` (`M0000000000000001`) | nothing | the same |
| `ROM` | nothing | the same |
| `SystemUUID` (all zeros) | nothing | the same |
| `SpoofVendor`, `SystemMemoryStatus` | nothing | not model-specific |

Changing the product name alone is coherent because `PlatformInfo >
Automatic` is `true`: OpenCore looks the product name up in its own Apple
model database and derives the board id and firmware features from it.
The evidence is in the panic below: it printed `MacPro5,1 (Mac-F221BEC8)`,
a board id nobody here ever typed.

The serials stay placeholders on purpose: this guest talks to no Apple
service, and minting realistic serials for a machine that does not exist
is not something this project does. Leaving them fixed also keeps the
setting a one-variable experiment.

The edit is **surgical text**, not a plist round-trip: one value changes
and the bytes around it do not, so the default build carries the checked-in
`config.plist` byte for byte. A config with other than exactly one
`SystemProductName` key is refused rather than guessed at.

## Why `MacPro5,1` panics: MEASURED

The UTM bundle this project started from set `MacPro5,1`, and under KVM
the guest panicked in `AppleTyMCEDriver`, the Xeon machine-check driver.
MEASURED on the primary host (i7-8700B Coffee Lake, not a Xeon, KVM):

- 2026-09-17: the installer panicked, under the bundle's OpenCore 0.6.6
  and firmware;
- 2026-09-21: an already-installed guest panicked at 40 s on this
  project's own OpenCore 1.0.7, OVMF and `config.plist`, with `iMac14,2`
  reaching the Finder desktop in 60 s under the same conditions;
- 2026-09-22: again, as a one-variable control on a fresh overlay: panic
  at 40 s, no SSH in 180 s, where the default answers in 20–40 s.

```
Kernel trap at 0xffffff7f8aefc6b7, type 13=general protection, registers:
RAX: 0xffffff7f8aefc6ac, RBX: 0x0000000000000000, RCX: 0x0000000000000280
...
com.apple.driver.AppleTyMCEDriver :
    __ZN16AppleTyMCEDriver47enableInterruptForCorrectableMemoryCoreRegisterEPv + 0xb
System model name: MacPro5,1 (Mac-F221BEC8)
```

MEASURED 2026-09-21 on `ap-juicer`, which **is** a Xeon (5150): an install
with `-cpu Conroe` and `MacPro5,1` stopped on the same kind of text screen
and never answered SSH, where the same host had installed with `iMac14,2`
an hour earlier. So it is not about the host CPU being a Xeon.

**The cause.** `RCX` is the MSR index register for `rdmsr`/`wrmsr`, and
`0x280` is `IA32_MC0_CTL2`, the first CMCI (Corrected Machine Check
Interrupt) control register, which is what a function named
`enableInterruptForCorrectableMemoryCoreRegister` touches. Since Linux
commit `281b5278` ("KVM: x86: Add emulation for MSR_IA32_MCx_CTL2 MSRs",
first released in v6.0, 2022-10-02), KVM dispatches the whole range
`0x280`–`0x29F` to `get_msr_mce`/`set_msr_mce`, which return `1`, not the
`KVM_MSR_RET_UNSUPPORTED` sentinel, when `MCG_CMCI_P` is clear. So KVM
injects `#GP` without consulting `kvm.ignore_msrs` and without logging
anything. QEMU never sets `MCG_CMCI_P` (`MCE_CAP_DEF = MCG_CTL_P|MCG_SER_P`,
and `kvm.c` masks down, never up). This fits the one other piece of
evidence: on the primary host, with `report_ignored_msrs=Y`, the panic run
logged exactly one ignored MSR, `0x300`, and nothing in the `0x280` range.

**The control.** MEASURED 2026-09-22 on the primary host: the same
overlay, OpenCore image, firmware and `-cpu` line, **one** variable,
`-accel tcg` instead of `kvm`:

| accelerator | SMBIOS | Result |
|---|---|---|
| `kvm` | `iMac14,2` | SSH in 20–40 s |
| `kvm` | `MacPro5,1` | **panic at 40 s**, `RCX=0x280`, no SSH in 180 s |
| `tcg` | `MacPro5,1` | **SSH at 80 s**; the guest reports 10.9.5 and `hw.model=MacPro5,1` |

TCG emulates the MSR instead of delegating it, and the panic does not
happen. So `MacPro5,1` is **unusable under KVM on any host, and usable
under TCG**: a property of KVM's machine-check emulation, not of
Mavericks and not of any hardware.

**What would falsify this:** before Linux 6.0 the range fell through to
`UNSUPPORTED`, where `kvm.ignore_msrs=1` would have suppressed the fault
and logged `ignored rdmsr: 0x280`. So on a host kernel older than 6.0,
with `ignore_msrs=1`, `MacPro5,1` should boot. No host this has run on is
that old.

**What did not work.** The bundle's config carried a disabled
`Kernel > Block` entry for `com.apple.driver.AppleTyMCEDriver`. Enabled,
it changed nothing: same panic, same backtrace. So this project's
`config.plist` has no `Kernel > Block` entry at all.

**One lead, untried.** `kholia/OSX-KVM` ships
`AppleMCEReporterDisabler.kext`, commented "Fix kernel panic MacPro
SMBIOS", which overrides the driver's IOKit personality so it never
matches; that would also explain why a bundle-id block did nothing. Its
`MinKernel` is 21.0.0 (macOS 12), so it is not directly usable on 10.9,
and nobody here has tried its mechanism as an OpenCore patch
(`docs/prior-art.md`).

## Alternatives considered

- **Refuse `MacPro5,1`.** It works under TCG, and a variable that refuses
  the interesting value cannot ask the question. The table says it
  panics under KVM, and the build proceeds.
- **Expose the whole `PlatformInfo` block.** Six more settings with no
  evidence behind any of them, and a larger surface for a failure that is
  about the edit rather than about Apple's driver.
