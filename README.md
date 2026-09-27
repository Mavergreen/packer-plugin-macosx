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

`template/` is the Packer template built on those data sources, and ships
attached to every release: a `qemu` builder boots the installer
unattended (no `boot_command` -- Apple's own installer reads its
configuration off the media), waits for first boot to finish, verifies
the guest over SSH, and a `vagrant` post-processor packages the result as
a libvirt box.

## Never publish the image or the box

**The built image and the box both contain Apple's operating system.**
Never publish either one -- not as a release asset, not as a package, not
anywhere reachable without authentication. This repository's own
releases carry the plugin binaries, the template and their checksums
only, and never Apple's bytes; `bin/no-apple-bytes.sh` is the gate that
checks every release artifact against that rule. The same rule applies to
anything you build with them.

## Building an image

**Host prerequisites:** Linux on an Intel CPU with VT-x and a writable
`/dev/kvm`; Packer; `qemu-system-x86_64`, `dmg2img` and `mkfs.hfsplus`
on `PATH`; gcc 13 through 16 for the firmware build; and, to run the box,
Vagrant with the [vagrant-qemu](https://github.com/ppggff/vagrant-qemu)
plugin. The data sources check these themselves and name whatever is
missing, but only once `packer build` has started.

The plugin has no release yet, so `packer init` has no tagged version to
resolve. Install it from your checkout first:

```sh
PACKER=packer ./bin/dev-install.sh
```

That builds `cmd/packer-plugin-mavericks` and installs it with `packer
plugins install --path` (`PACKER` picks which `packer` binary runs this;
it defaults to whatever `packer` is on `PATH`). Once a release exists,
`packer init` will install the plugin itself and this step goes away.

```sh
cd template
packer init .
packer build -var updates=none .
```

That fetches Apple's installer, builds the firmware and the installer
media, and boots the result under QEMU to install and configure
Mavericks unattended. It then checks the guest (`verify.sh`) and fails
the build, rather than package a broken guest, unless the guest is
10.9.5, its first boot finished, and passwordless sudo works.
`-var updates=none` skips Apple's post-10.9.5 updates; the default,
`-var updates=security`, adds the last one Apple shipped for 10.9. The
build writes `output/mavericks-10.9.5-libvirt.box`, relative to where
`packer` runs (here, `template/`), beside `output-mavericks/` and
`packer-manifest.json`.

To build again, add `-force`. The qemu builder refuses to overwrite an
existing `output-mavericks/`:

```sh
packer build -force -var updates=none .
```

The data sources cache what they build under Packer's cache directory
(`PACKER_CACHE_DIR`), so a rebuild with the same inputs goes straight to
the install.

## Running it with Vagrant

From the same `template/` directory:

```sh
vagrant box add --name mavericks output/mavericks-10.9.5-libvirt.box
mkdir ../mavericks-vm && cd ../mavericks-vm
vagrant init mavericks
vagrant up --provider qemu
vagrant ssh -c sw_vers
vagrant halt
```

`--provider qemu` matters. The box is in the `libvirt` format that
vagrant-qemu reads, so a plain `vagrant up` would pick whatever default
provider is installed, such as VirtualBox or vagrant-libvirt. You can
set `VAGRANT_DEFAULT_PROVIDER=qemu` instead.

The box carries its own Vagrantfile plus the OVMF firmware and OpenCore
image it needs. It boots headless by default; set `MAVERICKS_DISPLAY` to
a QEMU display type for a window instead
(`template/box.Vagrantfile.pkrtpl`):

```sh
MAVERICKS_DISPLAY=gtk vagrant up --provider qemu
```

That Vagrantfile carries the build's own settings. It is rendered from
the `user`, `cpu`, `memory`, `cpus`, `nic` and `accelerator` the box was
built with, so the box logs in and boots the way its image was built.
Your project's Vagrantfile can still override any of them.

## Variables

`template/variables.pkr.hcl` documents every variable the template takes
-- `updates`, `openssh`, `smbios`, `debug`, `cpu`, `memory`, `cpus`,
`disk_size`, `nic`, `accelerator`, `headless`, `install_timeout`, `user`,
`authorized_key` and `ssh_private_key_file` -- each with its own
`validation` block.

`debug = true` turns on three OpenCore and kernel debug settings for
diagnosing a boot (docs/configuration-register.md §7); off by default.

## Where things are

| Path | What |
|---|---|
| `cmd/packer-plugin-mavericks/` | The plugin binary's entry point |
| `datasource/` | The three data sources |
| `internal/` | The libraries the data sources are built from |
| `template/` | The Packer template, attached to every release |
| `assets/` | What the plugin embeds: pins, the OpenCore config and patches, the guest's first-boot payload, the privops microVM's scripts |
| `docs/decisions/` | Why the plugin is built the way it is, one decision per file |
| `docs/configuration-register.md` | Every setting, why it has its value, and what would change it |
| `docs/host-profile.md`, `docs/test-hosts.md` | The hosts it has run on, and what each measured |
| `INGREDIENTS.md` | Every pinned input: where it's tracked, what a bump does |
