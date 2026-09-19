# 0004 — The boot stack: what it is made of, and how it is rebuilt

Date: 2026-09-17
Status: accepted

## Context

Everything the guest runs before the macOS kernel starts is the **boot
stack**: the firmware, the bootloader, its configuration, its drivers and
the kexts it injects. `mavericks-firmware` builds it. This document records
what the boot stack is made of, where each part comes from, and what
"rebuilt from source" does and does not promise, so that the question "is
this the same boot stack" has an answer someone can check.

## The tiers

- **Tier 0**: built from pinned source by `mavericks-firmware`.
- **Tier 1**: vanilla upstream release binaries, pinned to an exact
  version and checksummed.
- **Tier 2**: someone else's custom blob. Never in the boot path.

Two Tier 2 artifacts are pinned in `assets/pins/sources.tsv` as evidence
rather than ingredients: `utm-bundle` (the UTM bundle that booted 10.9
under TCG before this project built its own boot stack) and
`opencore-legacy-img` (khronokernel's OpenCore 0.6.6 image inside it).
Nothing the plugin builds or boots reads either one. Deriving an image
from a Tier 2 artifact does not launder it.
