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

### Mavericks with Aqua Transport

A stock 10.9.5 guest's `curl` fails on github.com and GitHub's release
downloads with "Invalid certificate chain" (downloads.claude.ai works).
This template builds
a box over the Mavericks box you've already built, with Wowfunhappy's
[Aqua Transport](https://mavericksforever.com/) and its modern root
certificates installed, so the guest's own `curl`, and everything else that
uses the system's TLS, gets through.

Download and extract the `mavericks-aquatransport` template, and `cd` into
it. Unpack your Mavericks box, then build the layer over it:

```sh
mkdir mavericks-base
tar -xzf /path/to/output/mavericks-10.9.5-libvirt.box -C mavericks-base
packer init -upgrade .
packer build -var base_box=mavericks-base .
```

`-upgrade` matters here: the layer needs the plugin release its template came
with, and a plain `packer init` keeps the older plugin you built the base box
with, which fails with "Unknown data type macosx-download".

That makes `output/mavericks-10.9.5-aquatransport-libvirt.box` in a few
minutes. The build restarts the guest, checks it, and makes no box unless
Aqua Transport and its root certificates are installed and its AirDrop
kernel extension is not. Like every build here, the guest has no network
while it builds (`docs/decisions/0034`), so the build does not try the
box's `/usr/bin/curl` itself: the boot gate (`bin/gate.sh`) and
mavericks-vm's self-test show it reaching github.com, GitHub's release
downloads and downloads.claude.ai.

Aqua Transport is downloaded at build time from its one URL, which each new
version replaces, and the build accepts only the version its template pins.
So once Aqua Transport moves on, an older plugin release can no longer build
the layer: the build fails, naming both checksums. Use the latest release.

### Snow Leopard

Bring your own retail 10.6 install DVD image.

To produce 10.6.8 with Security Update 2013-004:

```sh
packer init .
packer build -var installer=/path/to/your/dvd-image .
```

### Watch a build

A build runs headless. To see its screen, add `-var headless=false` to the
template's own `packer build` line above. `display` picks QEMU's backend
for the window; left empty it is `gtk`:

```sh
packer build -var headless=false .                                         # Mavericks
packer build -var headless=false -var base_box=mavericks-base .            # Aqua Transport
packer build -var headless=false -var installer=/path/to/your/dvd-image .  # Snow Leopard
packer build -var headless=false -var display=sdl .                        # another backend
```

A macOS host's QEMU usually has `cocoa` rather than `gtk`, so there it
would be `-var display=cocoa`, once macOS hosts are supported. A `display`
without `headless=false` is refused. No build has been run with a window
yet.

## Run it

For example, with Vagrant:

```sh
vagrant plugin install vagrant-qemu
vagrant box add --name mavericks output/mavericks-10.9.5-libvirt.box
# or: vagrant box add --name mavericks output/mavericks-10.9.5-aquatransport-libvirt.box
# or: vagrant box add --name snowleopard output/snowleopard-10.6-libvirt.box
mkdir ~/mavericks-vm
cd ~/mavericks-vm
vagrant init mavericks
vagrant up --provider qemu
vagrant ssh -c sw_vers
vagrant halt
```

Known issue: vagrant-qemu, through its 0.7.0, forwards the guest's SSH
on every interface of the host, not only on 127.0.0.1, and ignores a
`host_ip` set for it. So while a box runs, anything that can reach the
host on that port (50022 unless you set `ssh_port`) can reach the guest's
sshd. Run boxes on a trusted network, or block the port in the host's
firewall. Nothing in the box can change it; the fix belongs in
vagrant-qemu.

It runs headless. To see its screen in a window (`cocoa` on macOS, `gtk`
elsewhere):

```sh
GUEST_MACOSX_GUI=1 vagrant up --provider qemu
```

`GUEST_MACOSX_GUI` takes `1`, `true` or `yes` for a window, and `0`,
`false`, `no` or nothing for none; anything else stops the command, naming
it. The box's VirtualBox and VMware settings read it too, for the day a
box runs on either; today's boxes are QEMU's alone.

The screen shows the login window, not a desktop. That is the owner's
decision: the default build sets no password, so the account cannot log
in at the console, and `vagrant ssh` is the way in. A desktop comes only
when a builder configures a password (`docs/configuration-register.md`,
"auto-login").

To pick QEMU's display yourself, set `GUEST_MACOSX_QEMU_DISPLAY` to what
QEMU's `-display` takes, such as `sdl`. It opens that display without
`GUEST_MACOSX_GUI`, so a VNC server is how to see a guest on a host with no
screen; this one listens on the host's own port 5901 only:

```sh
GUEST_MACOSX_QEMU_DISPLAY=vnc=127.0.0.1:1 vagrant up --provider qemu
```

It wins over `GUEST_MACOSX_GUI=0`, and `none` means no display, the same
as leaving it unset.

To ask for a guest CPU by the instructions it has, set
`GUEST_MACOSX_CPU_ISA` to `none` (no AVX, the default's CPU), `avx` (Sandy
Bridge: AVX but not AVX2, FMA or BMI) or `avx2` (AVX2, FMA, BMI1 and BMI2
too):

```sh
GUEST_MACOSX_CPU_ISA=avx vagrant up --provider qemu
```

`avx` and `avx2` need `kvm.ignore_msrs=Y` on the host, and the box warns
when it is not. Under KVM a level is what the guest is *told*: `none` makes
the AVX family fault, but on a host with AVX2 every other instruction above
a level still runs (`docs/decisions/0009`). Snow Leopard has no levels
measured, so its box warns that it ignores `GUEST_MACOSX_CPU_ISA`, and
boots its build's own CPU.

The window takes a keyboard and a pointer, on both releases. The pointer
is a USB tablet, so the window needs no grab. It is not one to one: Apple's
HID driver trims 15% off each axis of an absolute pointer
(`IOHIDEventDriver`'s `AbsoluteAxisBoundsRemovalPercentage`), so the
guest's pointer lands about 1.18 times as far from the centre of the
screen as the host's, and reaches each edge a little before the host's
pointer leaves the window (`docs/decisions/0035`).

## Check a change: the boot gate

`bin/gate.sh` builds a box from this checkout, with the plugin built from
it, boots the box, and checks inside the guest that it runs the checkout's
boot stack (OpenCore, Lilu, VirtualSMC), that it is healthy, and that it
comes back from restarts; then it shuts the box down. It does that for
Mavericks and then for Snow Leopard, because the firmware and boot stack
are both releases', so it needs your Snow Leopard disc image. It needs KVM
and 60 GiB free, and takes hours: a run has not been timed end to end yet,
and a Snow Leopard build alone took 93 minutes to answer SSH on a busy
host (`docs/decisions/0027`):

```sh
bin/gate.sh --installer /path/to/your/dvd-image
```

Without `--installer` it refuses, and says how to go on. To gate one
release on purpose:

```sh
bin/gate.sh --release 10.9                                       # Mavericks: the base and Aqua Transport
bin/gate.sh --release 10.6 --installer /path/to/your/dvd-image   # Snow Leopard 10.6.8
```

The verdict is the first line of `evidence/summary.md` under the work
directory (`${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}/gate` by
default). It passes only when both releases pass, and each release's own
evidence and verdict are in `evidence/10.9/` and `evidence/10.6/`. A Snow
Leopard run has no Aqua Transport layer to build or to reach the network
with, so it marks those two steps not applicable, saying why
(`docs/decisions/0014`), and runs every other check as Mavericks does. The
evidence never holds an image, a box or your disc, and the gate reads the
disc where it is.
