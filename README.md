# Mavericks plugin for Packer

Build a VM image of Mac OS X 10.9 Mavericks. Unattended.

## Install

You'll need fast network access, plenty of disk space.

### Supported host platforms

- Linux with KVM enabled

(NetBSD and Mac OS X are not yet supported.)

### Prerequisites

- `packer`
- `qemu-system-x86_64`
- `dmg2img`
- `mkfs.hfsplus`
- `gcc`

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
