# The Snow Leopard template: Mac OS X 10.6 from your own retail install
# disc, by the plugin's snowleopard-* data sources. It is the Mavericks
# template's twin, and every reason written at the top of
# templates/mavericks/mavericks.pkr.hcl -- no Apple bytes, no boot_command,
# drives in qemuargs, the vars file, paths through ${path.root}, the box's
# Vagrantfile, the checked-in Vagrant key, a custom key -- holds here the
# same way. What differs:
#
# THE INSTALLER IS YOURS. Apple offers 10.6 for no download, so the
# plugin fetches none: var.installer names your own image of a retail
# 10.6 install DVD -- an ISO or a Disk Utility master of the disc, a .dmg,
# or its HFS+ volume -- and snowleopard-installer refuses any disc whose
# packages are not a known retail disc's (docs/decisions/0014).
#
# NO OPENSSH VARIABLE. The family's OpenSSH is built for 10.9; the guest
# keeps Apple's OpenSSH 5.2, which Packer's own SSH client logs in to as
# it is (MEASURED 2026-10-04).

packer {
  required_plugins {
    macosx = {
      source  = "github.com/mavergreen/macosx"
      version = ">= 0.0.0"
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

data "macosx-snowleopard-installer" "dvd" {
  path = var.installer
}

data "macosx-snowleopard-firmware" "fw" {
  smbios = var.smbios
  debug  = var.debug
}

data "macosx-snowleopard-media" "media" {
  installer      = data.macosx-snowleopard-installer.dvd.path
  user           = var.user
  authorized_key = var.authorized_key
  updates        = var.updates
}

locals {
  # The qemu builder's output directory, relative to the working
  # directory. The box's rendered Vagrantfile is downloaded into it.
  output_directory = "output-snowleopard"

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
  })

  # Where box_vagrantfile passes through the guest on its way to the
  # output directory.
  box_vagrantfile_guest = "/tmp/snowleopard-box.Vagrantfile"

  effective_ssh_private_key_file = (
    var.ssh_private_key_file != ""
    ? var.ssh_private_key_file
    : (
      var.authorized_key == ""
      ? "${path.root}/vagrant-standard-insecure-first-boot-only.key.rsa"
      : file("authorized_key is set: ssh_private_key_file must be set too, naming the private key that matches it")
    )
  )
}

source "qemu" "snowleopard" {
  iso_url      = data.macosx-snowleopard-media.media.path
  iso_checksum = "none"
  disk_image   = false
  disk_size    = var.disk_size
  format       = "qcow2"

  output_directory = local.output_directory
  vm_name          = "snowleopard.qcow2"

  accelerator  = var.accelerator
  machine_type = "q35,vmport=off"
  cpu_model    = var.cpu
  memory       = var.memory
  cpus         = var.cpus
  net_device   = var.nic
  headless     = var.headless
  boot_wait    = "0s"

  efi_boot          = true
  efi_firmware_code = data.macosx-snowleopard-firmware.fw.ovmf_code
  efi_firmware_vars = data.macosx-snowleopard-firmware.fw.ovmf_vars

  communicator         = "ssh"
  ssh_username         = var.user
  ssh_private_key_file = local.effective_ssh_private_key_file
  ssh_timeout          = var.install_timeout

  shutdown_command = "sudo shutdown -h now"

  qemuargs = [
    ["-drive", "if=pflash,format=raw,unit=0,readonly=on,file=${data.macosx-snowleopard-firmware.fw.ovmf_code}"],
    ["-drive", "if=pflash,format=raw,unit=1,file={{ .OutputDir }}/efivars.fd"],
    # XHCI, not Mavericks' EHCI with UHCI companions: 10.6.0's AppleUSBUHCI
    # hangs polling a halted QEMU UHCI controller with interrupts off,
    # wedging the install (MEASURED 2026-10-04, docs/decisions/0014). OVMF
    # boots OpenCore's disk from XHCI, and 10.6 has no XHCI driver, so it
    # never touches it. For the same reason, no USB keyboard or mouse: the
    # install is unattended.
    ["-device", "qemu-xhci,id=usb"],
    ["-drive", "id=opencore,if=none,format=raw,snapshot=on,file=${data.macosx-snowleopard-firmware.fw.opencore_image}"],
    ["-device", "usb-storage,bus=usb.0,drive=opencore"],
    ["-drive", "id=target,if=none,format=qcow2,file={{ .OutputDir }}/{{ .Name }}"],
    ["-device", "ide-hd,bus=ide.0,drive=target"],
    ["-drive", "id=installer,if=none,format=raw,snapshot=on,file=${data.macosx-snowleopard-media.media.path}"],
    ["-device", "ide-hd,bus=ide.1,drive=installer"],
    ["-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:{{ .SSHHostPort }}-:22"],
    # The PCI NICs need no bus.
    ["-device", "${var.nic},netdev=net0"],
    ["-device", "VGA,vgamem_mb=64"],
  ]
}

build {
  sources = ["source.qemu.snowleopard"]

  provisioner "shell" {
    script          = "${path.root}/firstboot-wait.sh"
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # verify.sh judges the guest, and fails the build -- before a broken
  # guest becomes a box -- unless it is 10.6.8 (10.6 with updates =
  # "none"), its first boot finished, passwordless sudo works and, with
  # updates, the security update's receipt is there. UPDATES tells it which updates to expect.
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

  # THE BOX. Packer's stock vagrant post-processor already makes a
  # libvirt-provider box from a qemu build (its builtins map sends
  # BuilderId "transcend.qemu" to provider name "libvirt"); no custom
  # custom box post-processor is needed (docs/decisions/0013). `include`
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
    output = "output/snowleopard-10.6-{{ .Provider }}.box"
    include = [
      data.macosx-snowleopard-firmware.fw.ovmf_code,
      data.macosx-snowleopard-firmware.fw.ovmf_vars,
      data.macosx-snowleopard-firmware.fw.opencore_image,
    ]
    vagrantfile_template           = "${local.output_directory}/Vagrantfile"
    vagrantfile_template_generated = true
  }

  post-processor "manifest" {}
}
