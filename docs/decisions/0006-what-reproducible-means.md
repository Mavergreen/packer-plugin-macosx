# 0006 — What "reproducible" means for this build

Date: 2026-09-17
Status: accepted

## The claim

**The same inputs produce a guest that behaves identically. Not a disk
that is byte-identical.**

Byte-identity is not achievable and not worth achieving. An OS X install
writes:

- **timestamps** on nearly every installed file;
- **a volume UUID**, generated when the installer partitions the disk;
- **a machine UUID and a hardware serial**, derived at first boot;
- **caches** (`dyld`'s shared cache, the kernel cache, icon and font
  caches), built at first boot in whatever order things loaded;
- **Spotlight and FSEvents indexes**, which depend on when the indexer ran;
- **random seeds**, `/private/var/db/uuidtext` and SSH host keys.

A qcow2 adds one more layer: a sparse image records the *order* blocks
were allocated in, so two runs of an identical install differ in the file
where the filesystem does not. Chasing byte-identity would mean patching
all of that out of an operating system this project does not control.

## The inputs are named, and each output is filed under them

Every data source files its output under the digest of a **listing** of
what it was made of, one `key<TAB>value` row per input (`internal/inputs`,
`internal/store`):

| Data source | Its listing names |
|---|---|
| `mavericks-installesd` | the pinned sha256 and URL of `InstallESD.dmg` |
| `mavericks-firmware` | every boot-stack pin, each firmware patch's sha256, the compiler EDK II will run, the build options, `config.plist`'s sha256, the kext pins, the SMBIOS model |
| `mavericks-media` | the first-boot payload's files and the autoinstall hooks by sha256, the privops microVM's scripts, the ESD pin, the OpenSSH release and the two packages its `SHA256SUMS` names, the account, the authorized key's sha256 and fingerprints, the updates selection and each update's pin, the extra space |

Each listing also carries a `recipe` row, standing for the Go that shapes
that data source's output. It changes whenever that code changes what an
output holds, and the tests hold it to the digest of that code's expected
outputs, so a newer plugin never reuses an output an older one built
differently.

## What is deterministic, and what is not

| Output | Deterministic? | Why |
|---|---|---|
| `InstallESD.dmg` | yes | Apple's bytes, held to the pinned sha256 |
| The firmware (OVMF, OpenCore's `.efi` files) | **per toolchain, build directory and UTC day** | `docs/decisions/0004`, measured |
| The OpenCore EFI image | yes, given the same files | GUIDs and the FAT serial are derived from fixed seeds, not drawn at random |
| The first-boot payload package | yes | fixed timestamps, uid and gid, sorted entries, gzip with no clock in it: the same inputs give byte-identical packages |
| The installer media **file** | **no** | `mkfs.hfsplus` stamps the clock into the volume header, a read-write mount rewrites it, and the catalog's layout follows write order |
| The installer media **contents** | yes | `mavericks-media` outputs `content_digest`: one sha256 over a sorted list of every file's own sha256, taken in the privops microVM from a read-only mount |
| The installed guest | **no**, by the list above | but it behaves identically |

MEASURED: the content digest does what it says. On 2026-09-18 two media
builds from different working copies, hours apart, contained byte-for-byte
the same 39,413 files while their image files differed. On 2026-09-25 two
builds of the media from the same inputs listed the same 39,415 files,
identical, with one digest.

## Inputs a run would otherwise modify

**Anything handed to QEMU without `snapshot=on` or `readonly=on` is
writable, and a guest writes to more of it than you expect.** MEASURED
2026-09-18: without `snapshot=on`, every boot rewrote the OpenCore image
(four boots, four checksums), and macOS wrote a `.Spotlight-V100` store
onto the installer media, so two builds from one ESD recorded different
content digests. So the template attaches both with `snapshot=on`, and the
OVMF variable store the build writes is its own copy in the output
directory, never the cached template.

## The installed guest behaves identically: how that was checked

MEASURED 2026-09-18 on the primary host: two guests built from the same
pinned inputs, each booted with **no installer media attached**, compared
three ways:

1. **Boot and SSH.** Both booted unattended and accepted SSH with the key
   each was built for.
2. **Identity.** `sw_vers` (10.9.5 13F34), `hw.model` (`iMac14,2`), CPU
   count, memory, the account's uid, gid and groups, Remote Login, sleep
   settings, auto-login, `.AppleSetupDone`, and the first-boot
   LaunchDaemon having removed itself: identical, apart from the clock
   reading of when the first boot ran.
3. **Files.** Every regular file on the boot volume as size and path:
   **321,104 files each, zero paths in only one, zero sizes that differ.**

Excluded from the file comparison, each for a reason: `/private/var` and
`/var` (logs, receipts, caches; written continuously), `/Users` (created
at first boot, written at login), `/System/Library/Caches` and
`/Library/Caches`, `/.Spotlight-V100` and `/.fseventsd`, and five
per-machine identities found by the comparison itself: CrashReporter's
`AnonymousIdentifier_<UUID>.plist`, `System.keychain`, `apsd.keychain`,
`com.apple.PowerManagement.plist` and the SSH host keys. A guest that
shipped identical SSH host keys would be the defect.

The same checks passed between a guest built from a fresh clone of the
repository, with nothing on the machine but Apple's installer, and one
built in a working copy: 321,104 files each, no differences, although the
two had built their firmware in different directories and so booted
different firmware bytes (`docs/decisions/0004`). A fresh clone
is the stronger test, because it cannot pass by drawing on something a
working copy accumulated.

## Consequences

- A measurement against a guest built from the same inputs is a
  measurement anyone can repeat.
- The accelerator, CPU, NIC, memory and CPU count are template variables,
  and the box's Vagrantfile is rendered with the values the guest was
  built with, so the box boots the way its image was installed. Nothing
  else records them: `packer-manifest.json` is Packer's own, and a built
  box does not carry the data sources' listings.
- **Byte-identity stays out of scope.** The place to start, if it is ever
  wanted, is a fixed clock in the guest and a pass that normalizes UUIDs,
  both of which change what the image *is* and would need their own
  decision.
