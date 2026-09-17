# 0001 — GPU passthrough is out of scope

Date: 2026-09-17
Status: accepted

## Context

Without a GPU that 10.9 has drivers for, the guest has no Quartz Extreme
or Core Image, and the CPU draws everything. GPU passthrough is the only
route to real graphics acceleration, and it would mean surveying PCIe
slots, the power supply, IOMMU groups and candidate cards (NVIDIA Kepler
or AMD Radeon HD 7000-class).

## Decision

No GPU passthrough. The guest has no 3D acceleration, and the
documentation says so rather than leaving it to be discovered.

## Reasoning

The primary host (`docs/host-profile.md` §1) is a `Macmini8,1`. It has
**no PCIe slots** and exactly one display device, the CoffeeLake-H UHD 630
iGPU at `00:02.0`. There is no card to pass through and nowhere to install
one. IOMMU is enabled with 14 groups, but that is moot.

Passing through the iGPU itself is not viable: it is the host's only
display output, and Intel GVT-g does not cover Coffee Lake in a way 10.9
could use even if it did.

A Thunderbolt eGPU is the only physical possibility. It would mean
acquiring hardware, and eGPU passthrough on a T2 Mac running a
T2-patched Linux kernel is an unproven combination stacked on another.

## Consequences

- The guest has no 3D acceleration. This is a known, accepted limitation.
- If the primary host ever changes to a machine with slots, revisit this.
