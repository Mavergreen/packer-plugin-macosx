# 0007 — What this project ships

Date: 2026-09-19
Status: accepted

## Context

This repository is one of about forty `mavericks-*` projects in the
Mavergreen family. The family has conventions, and its governing rule is:
**match the family unless the product genuinely differs, and when you
deviate, say so.** A silent deviation reads as a mistake; a documented one
reads as a decision.

Every sibling cross-builds one upstream thing into a **Mac OS X 10.9**
compatible `.pkg`, on a modern runner, with no 10.9 build machine
anywhere. This project inverts that: what it makes *runs* Mavericks, as a
guest, on a Linux host.

## Decision 1 — what a release carries

A release of `packer-plugin-macosx` carries exactly three kinds of
file, and nothing else:

1. **The plugin binaries**, `packer-plugin-macosx_v<ver>_x5.0_<os>_<arch>`,
   zipped, for linux, darwin and netbsd on amd64 and arm64: the names
   `packer init` expects for the plugin source
   `github.com/mavergreen/macosx`.
2. **The template**, `template/` as one zip: `mavericks.pkr.hcl`,
   `variables.pkr.hcl`, the box's Vagrantfile template, the two guest-side
   scripts the build runs, and Vagrant's insecure private key. A build
   needs this directory and the plugin, not a checkout.
3. **A `SHA256SUMS`** over both. `packer init` checks the plugin binary
   against it.

`.goreleaser.yml` produces all three. The version scheme is
`docs/decisions/0012`.

## Decision 2 — never publish Apple's bytes

**The built disk and the box both contain Apple's operating system. They
are never published**: not as a release asset, not as a package, not
anywhere reachable without authentication. The same holds for everything
the data sources fetch: Apple's installer and update packages are
downloaded on the user's own machine, at build time, from Apple, and never
leave it.

This is what separates the product from a Vagrant box someone could
download. What ships is the **recipe** (the plugin and the template); the
box is made, and stays, on the machine that runs the recipe. So the
template has **no upload post-processor**, and must never gain one.

`bin/no-apple-bytes.sh` is the gate:
- on the tree a release is built from (CI runs it on every push), nothing
  may look like Apple's bytes: names (`.dmg`, `.pkg`, `InstallESD`, ...),
  magic numbers (`xar!`, `H+`/`HX`, `koly`, Mach-O) and size;
- with `--archives`, on goreleaser's output before anything is uploaded,
  every file and every member of every zip is judged the same way, and a
  disk image, box or installer package by name is refused. The plugin
  binary is the one exemption.

It passes by construction, because nothing here ever holds Apple's bytes.
That is the reason to assert it rather than assume it: a 6 GB `.dmg`
committed "just for a minute" is one `git add -A` away and looks like
nothing in a large diff.

## Decision 3 — what the box is

The template's `vagrant` post-processor makes a **libvirt-format box**
that `vagrant-qemu` runs. It carries:

- the installed disk;
- the OVMF code and variable-store images and the OpenCore EFI image, the
  firmware's own outputs, beside the disk;
- a Vagrantfile rendered from `template/box.Vagrantfile.pkrtpl` with the
  build's own `user`, `cpu`, `memory`, `cpus`, `nic` and `accelerator`, so
  the box logs in and boots the way its image was installed. It wires up
  the firmware and the machine, and adds a `before :halt` trigger that
  shuts the guest down from inside, because 10.9 ignores the ACPI power
  button (`docs/test-hosts.md` has the measurements).

The box authorizes Vagrant's own well-known insecure key by default, which
Vagrant replaces at the first `vagrant up`. That key is public by design
(`assets/vagrant/README.md`).

## Decision 4 — the guest-side payload

The first-boot payload (`internal/payload`, `assets/guest/`) is a real
flat `.pkg` that Apple's own installer installs, with 10.9.5 as its
floor. It is built on the host, with no Apple tool, and rides on the
installer media. It is a family-shaped artifact in its own right, but it
ships only inside media a user builds, never on its own.

## The sibling boundary

`mavericks-vm-host` (publishing as `Mavergreen/vm-host`) ships
Hypervisor.framework for 10.9, and a QEMU to go with it, so that modern
QEMU can use hardware acceleration *on* Mavericks. That is Mavericks as a
**host**; this repository is Mavericks as a **guest**. They are orthogonal
and stay separate repositories. Nothing in the guest stack depends on the
host project.

## What the family gives, and where this project deviates

The family's ingredient apparatus fits well: `INGREDIENTS.md` with
file-based pins, a Renovate manager for each pin, and the tier scheme of
`docs/decisions/0004` under the family's names. A bump there changes what
the recipe builds, which is exactly what the apparatus is for.

Declared deviations, transcribed into `INGREDIENTS.md` scoped to filename
globs:

| Deviation | Reason |
|---|---|
| Version scheme is not `<upstream>-mavericks.N` | This product is its own upstream: it takes the family's self-upstream `YYYYMMDD.N` shape (`docs/decisions/0012`). |
| No Sparkle updater for the plugin | Sparkle is a macOS framework, and the plugin runs on the build host, whose primary OS is Linux. Packer installs and updates plugins itself (`packer init`). The guest payload, a 10.9 `.pkg`, is a different product and takes the family's shape unchanged. |

## Later guests: named for, not designed for

A later version of this product will also want to build Snow Leopard and
Tiger guests (noted 2026-09-22 by the user). So the plugin is named for
Mac OS X, not for one release of it (decided 2026-10-04, before the first
release, while renaming cost nothing): `packer-plugin-macosx`, source
`github.com/mavergreen/macosx`. Each release gets its own data sources,
named for it -- today `mavericks-installesd`, `mavericks-firmware` and
`mavericks-media`, which a template names as `macosx-mavericks-*` -- so a
later guest is new data sources beside these, never an OS-version
parameter on them.

**Nothing here adds a 10.6 or 10.4 data source until there is a 10.6 or
10.4 guest to test it against.** A parameter with one value is honest; a
parameter with one value and a second branch nobody has run is a claim
nobody can support.

What is expected to transfer, and is not measured: for 10.6, the Intel,
EFI, OpenCore and KVM boot stack and the minstallconfig.xml unattended
install (both exist from 10.5 on). What will not: 10.6 has no
osrecovery download -- it shipped on DVD, so its installer would be
supplied by the user and pinned by checksum -- and its DVD is not an
InstallESD.dmg, so its media is built differently. Tiger is a different
bring-up, not a variation: Intel Tiger shipped only on machine-specific
discs and needs 32-bit EFI, and PowerPC Tiger needs qemu-system-ppc and
OpenBIOS, with no OVMF, no OpenCore and no KVM.
