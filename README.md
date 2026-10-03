# Mavericks plugin for Packer

Build a VM image of Mac OS X 10.9 Mavericks. Unattended.

The image, and any box made from it, contain Apple's operating system.
Never publish either one.

## Install

You'll need fast network access, plenty of disk space.

### Supported host platforms

- Linux with KVM enabled (a writable `/dev/kvm`), on an Intel CPU with VT-x

(AMD hosts are untested, so the build refuses them for now. NetBSD and Mac
OS X are not yet supported.)

### Prerequisites

- `packer`
- `qemu-system-x86_64`
- `dmg2img`
- `mkfs.hfsplus`
- `gcc` 13 through 16

Download and extract
[the template](https://github.com/Mavergreen/packer-plugin-mavericks/releases/latest).

`cd` into it.

## Build your own Mavericks VM

```sh
packer init .
packer build .
```

## Run it

For example, with Vagrant:

```sh
vagrant plugin install vagrant-qemu
vagrant box add --name mavericks output/mavericks-10.9.5-libvirt.box
mkdir ~/mavericks-vm
cd ~/mavericks-vm
vagrant init mavericks
vagrant up --provider qemu
vagrant ssh -c sw_vers
vagrant halt
```

It runs headless. To see its screen:

```sh
MAVERICKS_DISPLAY=gtk vagrant up --provider qemu
```
