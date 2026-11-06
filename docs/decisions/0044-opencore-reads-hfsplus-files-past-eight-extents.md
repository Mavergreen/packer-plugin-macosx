# 0044: OpenCore reads HFS+ files past their eight inline extents

Date: 2026-11-05

Status: Accepted by the owner, 2026-11-05 ("patch OpenHfsPlus too").

## Context

Both OpenCore configs load `OpenHfsPlus.efi`, the HFS+ driver through
which OpenCore reads `boot.efi`, and `boot.efi` reads the kernel and the
kernelcache (10.9) or `Extensions.mkext` (10.6). Its `fsw_hfsplus_get_ext`
looks only at the eight extents in a file's catalog record. A file
fragmented past them is read to the end of the eighth, and then the read
fails ("FIXME: more than 8 fragments not yet supported!"). The driver
never opens the extents-overflow B-tree that holds the rest. READ in
OpenCorePkg 1.0.7 (our pin) and in acidanthera's master on 2026-11-03.

How far our guests are from it, MEASURED 2026-11-05 by reading the
catalog and extents B-trees of four built disks (10.9 base and layer of
2026-10-17; 10.6.8 of 2026-10-17 and 2026-10-30):

- every file the boot reads is in 1 or 2 inline extents: `boot.efi`,
  `mach_kernel`, 10.9's `kernelcache`, 10.6's `Extensions.mkext`;
- the overflow B-tree holds 2 to 5 records, for logs, directory-service
  databases, a sandbox cache, a Spotlight journal, and 10.9's
  `/System/Library/Speech/Voices/Alex.SpeechVoice/Contents/Resources/PCMWave`.

So a fresh build does not hit it. A long-lived box can: a disk that fills
and fragments, then rebuilds its kernelcache, stops booting.

## Decision

The OpenCore build carries
`assets/firmware/patches/0009-opencore-read-hfsplus-files-past-eight-extents.patch`,
applied like 0008 (`docs/decisions/0043`: by marker, stamped, refused if
it did not take):

- **nfzerox's fix**, from nfzerox/VirtualIntelMacOnAppleSilicon's
  `Source/vendor/patches/opencore-hfs-overflow-extents.patch` (MIT). It
  opens the extents B-tree at mount, before the catalog, which can need
  it too. When a read runs past an extent record, it looks up the next
  one by its exact key (file ID, data fork, first block), and it never
  looks up the extents file in itself. Reviewed against 1.0.7: the leaf
  search returns only an exact match, so a lookup cannot take another
  file's extents.
- **Ours, in `fsw_hfsplus_bt_search`.** Its `lo` and `hi` were
  `fsw_u16`, so a search key below a node's first record set `hi` to
  0 - 1, 65535, and the search read record offsets past the node. They
  are `int` now, and an index node with no record at or below the key
  ends the search as not found.

MEASURED 2026-11-05: the driver's own `fsw_hfsplus.c`, `fsw_core.c` and
`fsw_lib.c` were compiled on the host with a small block-device shim and
run against the 10.9 base disk:

- `PCMWave` (422,010,632 bytes, 103,030 blocks in two extent records):
  - unpatched, the read stopped at byte 246,611,968 with "got_here";
  - patched, it read the whole file, byte-identical (sha256 `9a279341…`)
    to an independent extraction through the same two records;
  - patched, under AddressSanitizer and UBSan, there were no errors. The
    one UBSan note is a misaligned 4-byte load in upstream's
    `fsw_hfsplus_bt_idx_get_child`, from before this patch and harmless
    on x86.
- `mach_kernel`, `kernelcache` and both `boot.efi` read the same bytes
  unpatched and patched.
- A search of the extents B-tree for a key below every record:
  - unpatched, it segfaulted under AddressSanitizer in
    `fsw_hfsplus_btnode_get_rec`;
  - patched, it returned `FSW_NOT_FOUND`.

The patch is in the firmware's input listing, so cached guests rebuild
cold once.

## Consequences

- A guest whose boot files fragment past eight extents still boots.
- Nothing in CI builds an HFS+ disk with a fragmented file: the
  measurement above used a built guest's disk, which is Apple's, and is
  not published. The build's patch tests hold that 0009 is applied and
  checked.
- Upstream: both bugs, for the owner to report to acidanthera/OpenCorePkg.
