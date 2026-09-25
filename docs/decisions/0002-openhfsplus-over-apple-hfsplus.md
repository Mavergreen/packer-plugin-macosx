# 0002 — Use `OpenHfsPlus.efi`, not Apple's `HfsPlus.efi`

Date: 2026-09-17
Status: accepted; the cost is measured

## Context

OVMF cannot read HFS+, so OpenCore needs an HFS+ EFI driver to find and
boot the installer and the installed system.

Two options exist. `HfsPlus.efi` is Apple's own driver, extracted from Mac
firmware images; it is the common choice and reportedly the faster one.
`OpenHfsPlus.efi` is built from source as part of OpenCorePkg.

## Decision

Use `OpenHfsPlus.efi`.

## Reasoning

Apple's `HfsPlus.efi` is Tier 2 by construction (`docs/decisions/0004`):
a binary extracted from somewhere, with no source and no way to rebuild
it. Nothing that cannot be built from source goes into the boot path.

`OpenHfsPlus.efi` is Tier 0: pinned source, built by `mavericks-firmware`
as part of OpenCorePkg's default target (`Staging/OpenHfsPlus`), with no
extra build step.

## Measured cost

MEASURED 2026-09-17 on the primary host (`docs/host-profile.md` §1):
**`OpenHfsPlus.efi` costs 3.3 s more than Apple's `HfsPlusLegacy.efi` to
reach the Mavericks desktop: 49.1 s against 45.8 s (+7.2%).** That is nine
times the run-to-run spread, so it is real, and it is small.

| Driver | Runs | Launch → desktop | Spread |
|---|---|---|---|
| `OpenHfsPlus.efi` (Tier 0) | 5 | **49.1 s** mean | 48.8–49.7 s |
| `HfsPlusLegacy.efi` (Tier 2, Apple's) | 5 | **45.8 s** mean | 45.7–46.0 s |

Method:
- ten boots, strictly alternating, so any host drift falls on both arms;
- each run from identical state: a fresh qcow2 overlay on one installed
  guest and a pristine NVRAM, since 10.9 ignores the ACPI power button and
  every run ended in a hard stop;
- **exactly one line of the QEMU command differs between the arms**, the
  OpenCore image, and the two images differ only in which driver is in
  `EFI/OC/Drivers/` and which one `config.plist` names;
- t=0 is QEMU's exec, and the end is the first 1 Hz `screendump` over the
  monitor socket with more than half the frame lit. That frame is the
  Finder desktop with the Dock drawn, checked by eye. Sampling resolution
  is ±1 s, which is why there are five runs per arm.

**Apple's driver does not load on the firmware this project builds.**
With `HfsPlusLegacy.efi` on our own OVMF, the boot stops:

```
OC: Driver HfsPlusLegacy.efi at 2 cannot be loaded - Not started!
Halting on critical error
```

`EFI_NOT_STARTED` comes out of `gBS->LoadImage`, from
`UefiImageInitializeContextPreHash` in audk's
`MdeModulePkg/Core/Dxe/Image/Image.c`. audk replaces EDK II's tolerant PE
loader with acidanthera's strict one, and Apple's extracted binary is not
a conformant PE image. `FixupAppleEfiImages` was enabled and does not
help: it fixes images OpenCore loads, not ones the firmware loads.

So the comparison above ran on the Tier 2 reference OVMF from a UTM bundle
(2021-era EDK II, whose loader accepts the blob), with our OpenCore, our
config and our kexts held constant. The number is honest about the driver
and says nothing about the firmware. On our own firmware, `OpenHfsPlus`
measured 48.7–50.0 s, the same as on the reference firmware.

**acidanthera's own EDK II will not load Apple's `HfsPlus` driver.**
Overruling this decision would mean giving up the self-built firmware
too, trading 3.3 seconds for two unbuildable blobs instead of one.

Two caveats:

- One host, one guest. The *ratio* is unlikely to be host-specific even
  where the absolute times are.
- It measures **boot**, which is all this decision is about.
  `OpenHfsPlus` is out of the path once macOS has mounted the volume with
  its own driver, so nothing about steady-state performance follows.

`HfsPlusLegacy.efi`, for anyone re-measuring: 22,912 bytes, sha256
`5ab216689ee8b6918ef70a22928fe7bc205a39b096e8711cd5711ae95a8df7f2`, from
khronokernel's `EFI-LEGACY.img` (`opencore-legacy-img` in
`assets/pins/sources.tsv`). Deriving an image from a Tier 2 artifact does
not launder it: a modified copy is still Tier 2.
