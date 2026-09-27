# 0013 — A Packer plugin and template, written in Go

Date: 2026-09-26
Status: accepted

## Context

The job is to build an unattended OS X 10.9 guest from Apple's own
installer, on a Linux host, with no Mac, and hand it to people as
something they can boot. Most of that job is generic, and one part of it
is not.

**Generic:** boot a VM from installer media; wait for it; reach it over
SSH; run checks in it; shut it down; package the disk as a Vagrant box.
Packer already does all of this: a `qemu` builder, SSH and WinRM
communicators, shell and file provisioners, a `vagrant` post-processor,
and declarative HCL templates that people already know how to read,
override and pin.

**Mavericks-specific:** Apple's osrecovery handshake and download; OVMF
and OpenCore built from pinned source (`docs/decisions/0004`); HFS+
installer media carrying Apple's unattended-install hooks and a first-boot
payload, made without a Mac, without root and without mounting anything on
the host. Packer has none of it.

## Decision

**A Packer plugin that does the Mavericks-specific part, and a Packer
template that does the rest with Packer's own builder, provisioners and
post-processors.**

The plugin is three **data sources**, which run before the build, return
local paths and checksums, and cache their outputs by the content of
their inputs (`docs/decisions/0006`):

| Data source | Does |
|---|---|
| `mavericks-installesd` | Apple's `InstallESD.dmg`, downloaded and verified against its pinned sha256 |
| `mavericks-firmware` | OVMF and the OpenCore EFI image, built from pinned source |
| `mavericks-media` | installer media carrying the unattended-install hooks and the first-boot payload |

The template (`template/`) wires them to Packer's stock `qemu` builder,
which boots the installer with no `boot_command` (Apple's installer reads
its configuration off the media), and to the stock `vagrant`
post-processor, which already makes a libvirt-provider box from a qemu
build.

**Go**, because a Packer plugin is written against `packer-plugin-sdk`,
and because the work is: HTTP with checksum pinning; archives (zip, tar,
cpio, xar); GPT and FAT images; HFS+ volume headers; process supervision
with timeouts; and a great deal of string handling. Go's standard library
and `x/crypto` cover all of it with their own TLS, and the plugin
cross-builds for every host as a single binary.

## Alternatives considered

**A standalone tool with its own subcommands** (fetch, firmware, media,
install, run, ssh, package). It would have to reimplement, and then
maintain, everything Packer already does well: VM supervision, SSH waiting,
provisioning and box packaging, plus a configuration language nobody
already knows. And it would leave a user who wants something Packer
already offers (a different post-processor, an HCL override, a build
matrix) with nothing. A plugin puts the Mavericks-specific part where
Packer users already look for it, and lets the rest be Packer's.

**A custom builder instead of data sources.** A builder owns the VM's
lifecycle, which would mean re-implementing the qemu builder to add three
preparation steps in front of it. Data sources are exactly "work that must
happen before the build, whose outputs the build consumes", and they
compose with any builder, not only this one.

**A custom `mavericks-box` post-processor.** Not needed: the stock
`vagrant` post-processor maps the qemu builder to the `libvirt` provider,
its `include` carries the firmware files into the box, and its
`vagrantfile_template` supplies the Vagrantfile that wires them up. The
box was measured to build, boot and halt cleanly with it
(`docs/test-hosts.md`).

**Shell.** Much of the work is process-spawning glue, which shell is good
at. But the parts that must fail closed (a release gate, a checksum
check, a parser of downloaded bytes) are exactly where shell's hazards
live: a failure inside a pipeline or a process substitution that never
reaches the caller, `pipefail` and `SIGPIPE` turning a match into a miss.
The repository's own tooling (`bin/`, `build/`, `lib/`, `tests/*.bats`) is
shell, and is tested and linted as such.

**C.** It would need libcurl or OpenSSL, libarchive and an SSH library on
three operating systems, and it would parse downloaded bytes without
memory safety.

## Consequences

- **A build is `packer init` and `packer build`**, and a guest is `vagrant
  up`. What the plugin adds is three data sources a template names.
- **Development needs a Go toolchain; users do not.** They get one binary
  per host, installed by `packer init`.
- **The guest's own scripts stay what they are**: the first-boot payload
  and the unattended-install hook are shell, because they run inside
  10.9, and the privops microVM's scripts are shell because they run in a
  busybox initramfs. The plugin embeds them.
- **Portability follows Packer's**: the plugin cross-builds for linux,
  darwin and netbsd, and what ties it to Linux today is kept behind named
  seams (`docs/decisions/0005`).
