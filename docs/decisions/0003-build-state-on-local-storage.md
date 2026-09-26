# 0003 — What the build downloads and makes lives in Packer's cache, on local storage

Date: 2026-09-17
Status: accepted

## Context

On the primary host (`docs/host-profile.md` §1) the repository is not on
local disk. `~/Documents/trees` is an NFSv3 mount from `ap-juicer`, while
`/` and `/home` are btrfs on the local NVMe.

MEASURED 2026-09-17 on the primary host: NFS bulk transfer runs at
60 MB/s against 567 MB/s local, a 9.4x gap; creating a file takes 18 ms
over NFS against 0.06 ms locally, a 156x gap. MEASURED 2026-09-21 from
both ends of the same export: `ap-juicer`, which serves it, creates a file
in its local ZFS copy in 0.39 ms, against 10–15 ms for the same files over
NFS on the two client hosts. So the cost belongs to NFS, not to the
storage or the tree, and it is per operation more than per byte.

What the data sources handle is exactly the shape NFS punishes: Apple's
5.3 GB installer, several GB of raw conversions while the media is
assembled, and an EDK II build tree of many thousands of small files.

## Decision

Everything the data sources download, build or reuse lives under
`mavericks/` in **Packer's cache directory**: `PACKER_CACHE_DIR` when it is
set, otherwise `$XDG_CACHE_HOME/packer`, otherwise `~/.cache/packer`. Each
data source's `cache_dir` overrides it. Nothing is written into the
repository.

Under that directory:

| Path | What |
|---|---|
| `cache/<sha256>/<name>` | Downloads, content-addressed: a changed pin is a different file, and a cached file can always be re-verified against its own directory name. Shared by all three data sources, so Apple's installer is downloaded once |
| `installesd/`, `firmware/`, `media/` | Each data source's finished outputs, one directory per digest of the inputs that made it (`docs/decisions/0006`) |
| `firmware-build/` | The OpenCore and OVMF build tree, kept warm across builds |
| `media-build/` | The media build's scratch: raw conversions and the image before it moves into `media/` |

## Reasoning

- **Speed.** A firmware build writes thousands of files; at 18 ms each over
  NFS against 0.06 ms locally, the tree's location decides the build's
  time.
- **The repository stays a repository.** It holds code, docs and pins,
  never Apple's bytes (`bin/no-apple-bytes.sh`, `docs/decisions/0007`).

## Consequences

- The default is local on every host this has run on. A host whose home
  directory is itself on NFS should point `PACKER_CACHE_DIR` at local
  storage.
- **The firmware workspace's path is an input and has a limit.** EDK II
  writes each module's debug-symbol path into the PE image it emits and
  refuses one over 255 bytes. MEASURED 2026-09-25 for this pin: the
  deepest paths below the EDK II tree are 130 bytes for OpenCore and 162
  for OVMF, so `<cache_dir>/firmware-build` can be at most 96 bytes long
  for the OpenCore build and 64 for OVMF. `mavericks-firmware` refuses a
  longer one before it fetches anything.
- The qemu builder's own outputs (`output-mavericks/`, `output/`) are
  written relative to wherever `packer` runs, as usual for Packer, so the
  finished disk lands wherever the build was started. Nobody has measured
  what an NFS working directory costs the install.
- On btrfs, copy-on-write fragments a qcow2 that is written in place;
  `chattr +C` on the directory before anything is written avoids it
  (REASONED; the plugin sets no file attributes).
