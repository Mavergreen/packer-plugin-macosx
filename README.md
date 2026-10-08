# Packer builder for old Mac OS X VMs

Build a VM image, unattended, of Mac OS X 10.9 Mavericks or 10.6 Snow Leopard.

The image, and any box made from it, contain Apple's operating system.
Never publish either one.

## Install

You'll need fast network access and plenty of disk space.

### Supported host platforms

- Linux with KVM enabled (a writable `/dev/kvm`), on an Intel CPU with VT-x
  or an AMD CPU with AMD-V

(On AMD, keep `vendor=GenuineIntel` in the `cpu` variable, as its default has it.
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

To ask for a guest CPU by the instructions it has, set `MAVERICKS_CPU_ISA`
to `none` (no AVX, the default's CPU), `avx` (Sandy Bridge: AVX but not
AVX2, FMA or BMI) or `avx2` (AVX2, FMA, BMI1 and BMI2 too):

```sh
MAVERICKS_CPU_ISA=avx vagrant up --provider qemu
```

`avx` and `avx2` need `kvm.ignore_msrs=Y` on the host, and the box warns
when it is not. Under KVM a level is what the guest is *told*: `none` makes
the AVX family fault, but on a host with AVX2 every other instruction above
a level still runs (`docs/decisions/0009`).

Snow Leopard's window shows the screen but takes no keyboard or mouse;
use `vagrant ssh`.
