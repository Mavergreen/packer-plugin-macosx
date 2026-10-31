# The Mavericks Packer plugin's own template: the three data sources, the
# build, and the post-processors.
#
# NO APPLE BYTES. This template names local paths the data sources produce;
# it contains none of Apple's software, and must never be changed so that
# it does. A build from it produces a disk image that contains Apple's
# operating system and can never be shared, so there is deliberately no
# upload post-processor here (bin/no-apple-bytes.sh checks every release).
#
# NO boot_command. Apple's own installer reads rc.cdrom.local,
# minstallconfig.xml and OSInstall.collection off the media, so the install
# runs unattended with nobody typing at a console.
#
# DRIVES ARE IN qemuargs. Supplying any -drive in qemuargs replaces every
# one of the qemu builder's own default drives (packer-plugin-qemu's
# applyUserOverrides: a key present in qemuargs is never filled in from the
# defaults), so the target disk, the installer media and OpenCore are all
# attached here explicitly, and the OVMF pflash pair is too -- not because
# efi_boot wouldn't add them on its own, but because our own qemuargs
# already claims the -drive key.
#
# THE VARS FILE. efi_boot copies efi_firmware_vars into this build's own
# output directory as efivars.fd (packer-plugin-qemu's stepPrepareEfivars,
# unconditional on qemuargs), so the qemuargs pflash row for unit=1 points
# at {{ .OutputDir }}/efivars.fd -- the build's own writable copy -- never
# at the firmware data source's own ovmf_vars path, which stays read-only
# in the shared cache.
#
# PATHS. Every file this template reads from beside itself is named
# through ${path.root} -- the template's own directory -- so a build works
# from any working directory (`packer build templates/mavericks/` from the repository
# root, as CI validates, as well as `cd template && packer build .`). The
# one exception is templatefile()'s argument, which Packer itself resolves
# against that same directory (local.box_vagrantfile). What
# the build WRITES is relative to the working directory, as is usual for
# Packer: output-mavericks/, output/mavericks-10.9.5-libvirt.box and
# packer-manifest.json all land wherever packer runs.
#
# THE BOX'S VAGRANTFILE CARRIES THE BUILD'S SETTINGS. box.Vagrantfile.pkrtpl
# is rendered with this build's user, cpu, memory, cpus, nic and
# accelerator (local.box_vagrantfile), so the box logs in and boots the
# way its image was built. The vagrant post-processor reads
# vagrantfile_template as a path, and interpolates only Packer's legacy
# Go template context in it, so the rendered text must become a file
# before the post-processor runs. Packer's built-ins do that without a
# host shell: a file provisioner uploads the text to the guest, a second
# downloads it into this build's output directory, and a shell step
# removes the guest's copy (the build block below). Packer 1.16.1 also
# hands HCL2 variables to plugins as legacy user variables (GH-13686), so
# a `user` template function in the file would reach the post-processor
# directly. This template does not use that: an older Packer fails it,
# and the NIC and memory choices would become Go template logic inside
# Ruby.
#
# THE SSH KEY. templates/mavericks/ ships as its own release artifact (a build only
# needs this directory, not a checkout of the whole repository), so
# nothing here may point outside it. ssh_private_key_file therefore names
# templates/mavericks/vagrant-standard-insecure-first-boot-only.key.rsa -- a copy of
# assets/vagrant/vagrant-standard-insecure-first-boot-only.key.rsa,
# held byte-identical to it by this package's own
# TestVagrantKeyMatchesEmbedded -- rather than reaching out via path.root
# to assets/ (a templates/mavericks/ shipped alone has no such path to reach), and
# rather than data.macosx-mavericks-media.media.ssh_private_key_file, even
# though that data source output is the very same bytes for the default
# authorized_key (internal/payload's VagrantPrivateKey reads this exact
# file). MEASURED: packer-plugin-sdk's communicator config stats
# ssh_private_key_file unconditionally in Prepare, which plain `packer
# validate` also runs; data sources are not executed then, so Packer
# substitutes the literal string "<unknown>" for
# it, and the stat fails -- on ANY data source's string output wired into
# this field, not particular to media's. Naming a checked-in file here
# instead keeps `packer validate` -- and this package's own TestValidate
# -- green without ever executing a data source, while authenticating
# with the identical key bytes at build time. Vagrant's insecure keypair
# is deliberately public (assets/vagrant/README.md), so a second copy of
# it, checked in here too, costs nothing.
#
# A CUSTOM KEY. authorized_key names a public key file the payload
# authorizes instead (passed straight to mavericks-media); the matching
# private half can only come from the caller, as ssh_private_key_file,
# since nothing here can derive one from the other. local.effective_ssh_private_key_file
# picks var.ssh_private_key_file when it is set, or the checked-in
# default otherwise, and refuses -- naming both variables -- a custom
# authorized_key with no matching ssh_private_key_file. Packer's own
# variable validation blocks can only reference the variable they belong
# to (MEASURED: "can only refer to the variable itself"), so this
# cross-variable check lives in a local instead; file() is used only for
# its error text, which is why the string handed to it reads like a
# sentence rather than a path.

packer {
  required_plugins {
    macosx = {
      source  = "github.com/mavergreen/macosx"
      version = "~> 0.20261031.1"
    }
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "~> 1"
    }
    vagrant = {
      source  = "github.com/hashicorp/vagrant"
      version = "~> 1"
    }
  }
}

data "macosx-mavericks-installesd" "esd" {}

data "macosx-mavericks-firmware" "fw" {
  smbios = var.smbios
  debug  = var.debug
}

data "macosx-mavericks-media" "media" {
  installesd     = data.macosx-mavericks-installesd.esd.path
  user           = var.user
  authorized_key = var.authorized_key
  openssh        = var.openssh
  updates        = var.updates
}

locals {
  # The build's guest's host UUID: a fresh one per build (Packer
  # evaluates a local once), passed as QEMU's -uuid below, which OpenCore
  # makes the guest's system-id (SystemUUID OEM). The guest has it before
  # its first daemon asks, and no two builds share one
  # (docs/decisions/0024).
  build_uuid = uuidv4()
  # And its NIC's address, derived from that UUID by the box Vagrantfiles'
  # rule (lib/common.sh's derived_mac): 52:54:00, then three bytes of its
  # SHA-256, skipping a leading "1234" (twice at most here; a third has a
  # chance of 2^-48), so never 52:54:00:12:34:xx, QEMU's one default.
  build_uuid_sha = sha256(local.build_uuid)
  build_mac_at   = substr(local.build_uuid_sha, 0, 4) == "1234" ? (substr(local.build_uuid_sha, 6, 4) == "1234" ? 12 : 6) : 0
  build_mac      = format("52:54:00:%s:%s:%s", substr(local.build_uuid_sha, local.build_mac_at, 2), substr(local.build_uuid_sha, local.build_mac_at + 2, 2), substr(local.build_uuid_sha, local.build_mac_at + 4, 2))

  # The qemu builder's output directory, relative to the working
  # directory. The box's rendered Vagrantfile is downloaded into it.
  output_directory = "output-mavericks"

  # The install's own logs, as the installer writes them: the media's
  # autoinstall hook follows them to the guest's first serial port, and
  # QEMU writes that port here, beside the output directory and not in it,
  # because Packer deletes the output directory when a build fails -- and a
  # failed install is the build whose logs matter (docs/decisions/0027).
  # Each build's QEMU starts it afresh.
  install_log = "${local.output_directory}-install.log"

  # The box's own Vagrantfile, rendered with this build's settings.
  # templatefile() resolves a relative path against the template's own
  # directory already (Packer's basedir, which is also path.root), so
  # the name goes in bare: "${path.root}/..." would join a relative
  # path.root twice (MEASURED, as this directory was then named: `packer validate template/` looked for
  # template/template/box.Vagrantfile.pkrtpl).
  box_vagrantfile = templatefile("box.Vagrantfile.pkrtpl", {
    user        = var.user
    cpu         = var.cpu
    memory      = var.memory
    cpus        = var.cpus
    nic         = var.nic
    accelerator = var.accelerator

    # The instruction-set levels the box's GUEST_MACOSX_CPU_ISA offers, each
    # a -cpu line measured on this image (docs/decisions/0009, "The
    # instruction-set levels"). They leave out x2apic and tsc-deadline,
    # which TCG cannot give, so one line serves every accelerator.
    cpu_none = "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"
    cpu_avx  = "SandyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,enforce"
    cpu_avx2 = "IvyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,+avx2,+fma,+bmi1,+bmi2,+movbe,+abm,enforce"
  })

  # Where box_vagrantfile passes through the guest on its way to the
  # output directory.
  box_vagrantfile_guest = "/tmp/mavericks-box.Vagrantfile"

  effective_ssh_private_key_file = (
    var.ssh_private_key_file != ""
    ? var.ssh_private_key_file
    : (
      var.authorized_key == ""
      ? "${path.root}/vagrant-standard-insecure-first-boot-only.key.rsa"
      : file("authorized_key is set: ssh_private_key_file must be set too, naming the private key that matches it")
    )
  )

  # The build's window. The qemu builder ignores display when headless
  # is true, so a display set with headless left true is refused here,
  # naming both, rather than dropped. headless and display are two
  # variables, so the check is a local (A CUSTOM KEY, above, says why),
  # and file() is used only for its error text.
  display = (
    var.headless && var.display != ""
    ? file("display is set: headless must be false too, or the build has no window to show it in")
    : var.display
  )
}

source "qemu" "mavericks" {
  iso_url      = data.macosx-mavericks-media.media.path
  iso_checksum = "none"
  disk_image   = false
  disk_size    = var.disk_size
  format       = "qcow2"

  output_directory = local.output_directory
  vm_name          = "mavericks.qcow2"

  accelerator  = var.accelerator
  machine_type = "q35,vmport=off"
  cpu_model    = var.cpu
  memory       = var.memory
  cpus         = var.cpus
  net_device   = var.nic
  headless     = var.headless
  display      = local.display
  boot_wait    = "0s"

  efi_boot          = true
  efi_firmware_code = data.macosx-mavericks-firmware.fw.ovmf_code
  efi_firmware_vars = data.macosx-mavericks-firmware.fw.ovmf_vars

  communicator         = "ssh"
  ssh_username         = var.user
  ssh_private_key_file = local.effective_ssh_private_key_file
  ssh_timeout          = var.install_timeout
  # A failed install ends the build within a minute or two, not at
  # install_timeout: the media's autoinstall hook then answers SSH with a
  # disconnect saying the install failed and where its log is, and Packer's
  # qemu builder, which does not notice QEMU or the guest doing anything
  # else while it waits for SSH, counts it as a refused login and ends the
  # build after this many, with the guest's words in its error
  # (docs/decisions/0028). A good build's guest turns SSH on only once its
  # key is in place, so it refuses none.
  ssh_handshake_attempts = 10

  # The guest's own helper, never a bare `shutdown`: launchd waits out a
  # job's 20 s exit timeout, and the shutdown monitor files a .shutdownStall
  # report into the image, when loginwindow's ManagedClient still holds a
  # DiskArbitration session or UserEventAgent still has a preference to
  # write. /usr/local/sbin/guest-macosx-shutdown (assets/guest/firstboot.sh) stops
  # both, UserEventAgent while cfprefsd still runs (docs/decisions/0019).
  shutdown_command = "sudo /usr/local/sbin/guest-macosx-shutdown -h"

  qemuargs = [
    ["-uuid", local.build_uuid],
    ["-drive", "if=pflash,format=raw,unit=0,readonly=on,file=${data.macosx-mavericks-firmware.fw.ovmf_code}"],
    ["-drive", "if=pflash,format=raw,unit=1,file={{ .OutputDir }}/efivars.fd"],
    # detect-zeroes: the zero-fill provisioner's zeros become holes, not 50 GB of stored zeros
    # (TestTheTargetDriveStoresZerosAsHoles).
    ["-drive", "id=target,if=none,format=qcow2,detect-zeroes=unmap,discard=unmap,file={{ .OutputDir }}/{{ .Name }}"],
    ["-device", "ide-hd,bus=ide.0,drive=target"],
    ["-drive", "id=installer,if=none,format=raw,snapshot=on,file=${data.macosx-mavericks-media.media.path}"],
    ["-device", "ide-hd,bus=ide.1,drive=installer"],
    # OpenCore's disk is a virtio-blk disk: OVMF drives it and boots it
    # first (bootindex), and neither 10.6 nor 10.9 has a virtio driver, so
    # the guest never sees it. On AHCI, 10.6's DiskArbitration called it
    # unreadable at every desktop login (docs/decisions/0035).
    ["-drive", "id=opencore,if=none,format=raw,snapshot=on,file=${data.macosx-mavericks-firmware.fw.opencore_image}"],
    ["-device", "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10"],
    # restrict=on: the guest reaches nothing off-link for the whole build,
    # installer included, and the communicator's forward still works.
    # Without it Apple's own jobs fetched from Apple mid-build -- 10.9's
    # first-run Software Update scan, apsd's device activation, ocspd's
    # revocation checks for kextcache's kext signatures -- and the image
    # kept what they fetched (docs/decisions/0034,
    # TestTheBuildsGuestReachesNothingOffLink).
    ["-netdev", "user,id=net0,restrict=on,hostfwd=tcp:127.0.0.1:{{ .SSHHostPort }}-:22"],
    # The PCI NICs need no bus.
    ["-device", "${var.nic},netdev=net0,mac=${local.build_mac}"],
    # USB carries the keyboard and pointer alone: an EHCI controller of its
    # own, no UHCI companions and no XHCI, the same on both releases. QEMU's
    # usb-kbd and usb-tablet are high-speed devices, so they attach to EHCI
    # with no companion; 10.9's and 10.6's AppleUSBEHCI drive them, and
    # neither release loads AppleUSBUHCI, the driver that wedged 10.6.0's
    # installs (docs/decisions/0014). EHCI sits at the ICH9's own USB slot,
    # 0x1d.0, which avoids a measured stall: in the first free slot it shared
    # the NIC's interrupt line, and 10.6.0's installer spun in the NIC's
    # interrupt filter (MEASURED; why 10.6.0's EHCI never serviced its own
    # interrupt there is not established). That no device a launcher adds
    # shares 0x1d's line is REASONED from q35's routing, and the shape test
    # refuses any device in slots 0x19-0x1f but EHCI.
    # A tablet, not a mouse: it points without a grab (docs/decisions/0035).
    ["-device", "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0"],
    ["-device", "usb-kbd,bus=ehci.0"],
    ["-device", "usb-tablet,bus=ehci.0"],
    ["-device", "VGA,vgamem_mb=64"],
    ["-serial", "file:${local.install_log}"],
  ]
}

build {
  sources = ["source.qemu.mavericks"]

  provisioner "shell" {
    script          = "${path.root}/firstboot-wait.sh"
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # verify.sh judges the guest, and fails the build -- before a broken
  # guest becomes a box -- unless it is 10.9.5, its first boot finished,
  # passwordless sudo works and, with updates, the security update's
  # receipt is there. UPDATES tells it which updates to expect.
  provisioner "shell" {
    script           = "${path.root}/verify.sh"
    environment_vars = ["UPDATES=${var.updates}"]
    execute_command  = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # The box's Vagrantfile, made a file in the output directory, where
  # the vagrant post-processor reads it: up to the guest, back down, and
  # the guest's copy removed.
  provisioner "file" {
    content     = local.box_vagrantfile
    destination = local.box_vagrantfile_guest
  }

  provisioner "file" {
    direction   = "download"
    source      = local.box_vagrantfile_guest
    destination = "${local.output_directory}/Vagrantfile"
  }

  provisioner "shell" {
    inline          = ["rm -f ${local.box_vagrantfile_guest}"]
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # Zeroed free space compresses away: the box's image, and anything that
  # caches it compressed, got 12% smaller (zstd -10: 6.83 GB to 6.03 GB)
  # for about 136 s more build, measured 2026-10-06 on the primary host.
  # Last, because nothing after it writes to the disk.
  provisioner "shell" {
    inline          = ["sudo diskutil secureErase freespace 0 /"]
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # THE BOX. Packer's stock vagrant post-processor already makes a
  # libvirt-provider box from a qemu build (its builtins map sends
  # BuilderId "transcend.qemu" to provider name "libvirt"); no custom
  # mavericks-box post-processor is needed (docs/decisions/0013). `include`
  # copies the firmware data source's own output files into the box
  # (their basenames are already OVMF_CODE.fd, OVMF_VARS.fd and
  # opencore.img -- what box.Vagrantfile.pkrtpl expects beside itself),
  # and vagrantfile_template supplies the Vagrantfile the box carries:
  # the one the provisioners above rendered into the output directory,
  # which exists only once the build has run (hence
  # vagrantfile_template_generated).
  # vagrant-qemu 0.6.3 reads the target disk's path out of the box's own
  # metadata.json (its "disks" array, which the libvirt provider writes
  # unconditionally), so the box's Vagrantfile never has to name the
  # disk file itself.
  post-processor "vagrant" {
    output = "output/mavericks-10.9.5-{{ .Provider }}.box"
    include = [
      data.macosx-mavericks-firmware.fw.ovmf_code,
      data.macosx-mavericks-firmware.fw.ovmf_vars,
      data.macosx-mavericks-firmware.fw.opencore_image,
    ]
    vagrantfile_template           = "${local.output_directory}/Vagrantfile"
    vagrantfile_template_generated = true
  }

  post-processor "manifest" {}
}
