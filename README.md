# packer-plugin-mavericks

A [Packer](https://www.packer.io/) plugin, and a template built on it,
that produce an unattended OS X 10.9 (Mavericks) guest from Apple's own
installer -- no Mac needed to build one. It targets **a Linux host with
Intel hardware and KVM**; AMD hosts are a known-harder case for macOS
guests, and the build refuses one by name (`docs/host-profile.md` G2,
`docs/decisions/0005`).

## What it is

Three Packer data sources fetch and verify Apple's installer, build OVMF
and OpenCore from pinned source, and assemble unattended installer media:

| Data source | Does |
|---|---|
| `mavericks-installesd` | Downloads and verifies Apple's `InstallESD.dmg` |
| `mavericks-firmware` | Builds OVMF and the OpenCore EFI image from pinned source |
| `mavericks-media` | Builds unattended installer media carrying a first-boot payload |

Each caches its own output by the content of its inputs, so a rerun with
unchanged inputs costs seconds, not the whole build again.

## Never publish the image or the box

**The built image and the box both contain Apple's operating system.**
Never publish either one -- not as a release asset, not as a package, not
anywhere reachable without authentication. This repository's own
releases carry the plugin binaries, the template and their checksums
only, and never Apple's bytes; `bin/no-apple-bytes.sh` is the gate that
checks every release artifact against that rule. The same rule applies to
anything you build with them.
