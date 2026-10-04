# Packer builder for old Mac OS X VMs

Build a VM image, unattended, of Mac OS X 10.9 Mavericks or 10.6 Snow Leopard.

The image, and any box made from it, contain Apple's operating system.
Never publish either one.

## Install

You'll need fast network access and plenty of disk space.

### Supported host platforms

- Linux with KVM enabled (a writable `/dev/kvm`), on an Intel CPU with VT-x

(AMD hosts are untested, so the build refuses them for now.
NetBSD and Mac OS X hosts are not yet supported.)

### Prerequisites

- `packer`
- `qemu-system-x86_64`
- `dmg2img`
- `mkfs.hfsplus`
- `gcc` 13 through 16 are known to work

Download and extract
[the template](https://github.com/Mavergreen/packer-plugin-macosx/releases/latest)
for the OS you want.

`cd` into it.

## Build a VM

### Mavericks

To produce 10.9.5 with Security Update 2016-004:

```sh
packer init .
packer build .
```

### Snow Leopard

Bring your own retail 10.6 install DVD image.

To produce 10.6.8 with Security Update 2013-004:

```sh
packer init .
packer build -var installer=/path/to/your/dvd-image .
```

## Run it

For example, with Vagrant:

```sh
vagrant plugin install vagrant-qemu
vagrant box add --name mavericks output/mavericks-10.9.5-libvirt.box
# or: vagrant box add --name snowleopard output/snowleopard-10.6-libvirt.box
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

Snow Leopard's window shows the screen but takes no keyboard or mouse;
use `vagrant ssh`.
