# 0043: OpenCore counts a merged last memory-map entry once

Date: 2026-11-05

Status: Accepted by the owner, 2026-11-05 ("patch and prove").

## Context

Snow Leopard's OpenCore config turns `RebuildAppleMemoryMap` on (the
10.6.0 installer's kernel stops without it, `docs/decisions/0014`). Under
it, OpenCore sorts the memory map it hands `boot.efi` and joins adjacent
descriptors with `OcShrinkMemoryMap` (`Library/OcMemoryLib/MemoryMap.c`,
called from `OcAfterBootCompatLib/ServiceOverrides.c`). Mavericks' config
leaves the quirk off and never runs it.

`OcShrinkMemoryMap` joins each descriptor it can into the one before it
(`PrevDesc`), and counts a descriptor into `*MemoryMapSize` only when it
cannot join it. `PrevDesc` is always already counted. Yet when the map's
last descriptors were joined, it ended with

```c
  if (HasEntriesToRemove) {
    *MemoryMapSize += DescriptorSize;
  }
```

counting one descriptor more than the map holds: the caller got, after
`PrevDesc`, the first descriptor that had been joined into it, its range
inside `PrevDesc`'s. READ in OpenCorePkg 1.0.7 (our pin, the tarball
whose sha256 `assets/pins/sources.tsv` pins) and in acidanthera's master
on 2026-11-03. nfzerox/VirtualIntelMacOnAppleSilicon carries the same fix
(`Source/vendor/patches/opencore-memory-map-tail-count.patch`, MIT).

MEASURED 2026-11-05 with the function itself, compiled from the pinned
source in a small harness, on a map shaped like the top of a 4 GiB q35
guest's (two descriptors below 1 MiB, then RAM above 4 GiB that ends in
free memory with a Boot Services allocation beside it, the shape whose
last descriptors join): it returned 4 descriptors where the map holds 3,
the fourth a Boot Services Data descriptor at `0x17f000000` inside the
joined free range `0x100000000`-`0x180000000`. With the patch below it
returns the 3.

Whether a real 10.6 boot's map ends in a join depends on what the
firmware and `boot.efi` allocated at the top of RAM; it was not dumped.
A 4 GiB guest (the template's default) has 2 GiB above 4 GiB, which is
where that top is.

## Decision

The OpenCore build carries `assets/firmware/patches/0008-opencore-count-a-merged-last-memory-map-entry-once.patch`,
which drops the closing increment. It is the first patch to OpenCorePkg's
own sources (0001 patches its build script), so the OpenCore build now
applies them as the OVMF build applies its own (`openCorePatches` in
`internal/firmware/opencore.go`):

- each is skipped when its file already carries its marker,
  `packer-plugin-macosx: assets/firmware/patches/0008`, and the build is
  refused where it did not take ("still counts a shrunk memory map's
  merged last entry twice");
- each is stamped before it is applied, in `.guest-macosx-opencore-patches`
  at the tree's root, and a tree whose stamp is not a prefix of this
  binary's set (another revision, or a tree from before the stamp) is
  unpacked afresh, its EDK II tree with it.

The patch is in the firmware's input listing, so every cached guest's key
changes and the next build of each is cold, Mavericks' too (the listing
names every patch for both builds).

## Consequences

- A 10.6 kernel no longer gets a descriptor that overlaps another, on any
  boot whose map ends in a join.
- Mavericks' firmware bytes change by this patch only in code its config
  never runs.
- Upstream: a report and the one-line fix, for the owner to file with
  acidanthera/OpenCorePkg.
- `TestOpenCorePatchesItsOwnSourcesAndChecksThem`,
  `TestOpenCoreRefusesASourcePatchThatDidNotTake` and
  `TestOpenCoreUnpacksAfreshASourceTreeAnotherPatchSetTouched` hold it.
