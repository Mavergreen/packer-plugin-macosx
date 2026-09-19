# 0003 — What the build downloads and makes lives in Packer's cache, on local storage

Date: 2026-09-17
Status: accepted

## Context

On the primary host (`docs/host-profile.md` §1) the repository is not on
local disk. `~/Documents/trees` is an NFSv3 mount from `ap-juicer`, while
`/` and `/home` are btrfs on the local NVMe.

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
| `firmware-build/` | The OpenCore and OVMF build tree, kept warm across builds |
| `media-build/` | The media build's scratch: raw conversions and the image before it moves into `media/` |
