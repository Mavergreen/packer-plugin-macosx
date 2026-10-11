# The Aqua Transport layer: a Mavericks base box, as templates/mavericks/
# builds it, with Wowfunhappy's Aqua Transport and its
# ModernRootCertificates installed, so that the guest's own /usr/bin/curl
# -- and every other Secure Transport client -- reaches github.com and
# GitHub's release downloads (the done-check's hosts, MEASURED 2026-10-11:
# docs/decisions/0016), which a stock 10.9.5 guest's cannot.
# It boots the base box's own disk, firmware and OpenCore image, uploads
# the pinned DMG, installs it without tap.kext, reboots, verifies, zeroes
# free space and packages a box. The base is never rebuilt for it: a new
# Aqua Transport is a new pin here (aquatransport.pkr.hcl), and a new
# layer over the same base box.
#
# NO APPLE BYTES. Like the base template, this one names local paths and
# contains none of Apple's software. Its box holds Apple's operating
# system and can never be shared, so there is no upload post-processor
# (bin/no-apple-bytes.sh checks every release).
#
# NOR AQUA TRANSPORT'S. The DMG is fetched at build time, into the
# builder's own Packer cache, by the data source "macosx-download", which
# refuses any bytes but the pinned SHA-256's -- before anything reaches
# the guest, which could not fetch it anyway: fixing that is the point.
# It is never committed, mirrored or put in a release. Because the URL is
# overwritten in place, a plugin release's layer stops building once the
# download moves past its pin; the error names both SHA-256s.
#
# THE DMG AT VALIDATE TIME. `packer validate` runs no data source: the
# DMG's path is the literal string "<unknown>" then (templates/mavericks/
# mavericks.pkr.hcl's THE SSH KEY has the measurement), and the file
# provisioner would refuse to upload a source that is not there. So it
# says `generated = true`, which defers that check to the build, when the
# data source has run and the path is the verified file.
#
# THE BASE BOX. base_box is the directory a base box unpacks to (`tar
# -xzf` the .box, or Vagrant's own copy under ~/.vagrant.d/boxes); its
# validation names each of the five files used here, and wants the
# Vagrantfile to be one templates/mavericks/mavericks.pkr.hcl rendered,
# so a Snow Leopard box, which holds the same five names, is refused.
# Its box_0.img is the qemu builder's iso_url with disk_image = true, so
# the builder converts it into this build's own output directory and the
# base box is never written to (use_backing_file = false: the box is
# standalone, with no backing file to carry). skip_resize_disk keeps the
# base's disk size.
#
# DRIVES ARE IN qemuargs, as in templates/mavericks/mavericks.pkr.hcl,
# whose header says why; these are its qemuargs with no installer drive,
# and with OpenCore read from the base box's own opencore.img, snapshot=on
# so the build never writes it. efi_boot copies the base box's
# OVMF_VARS.fd to {{ .OutputDir }}/efivars.fd, which the unit=1 pflash
# row names.
#
# THE REBOOT. Aqua Transport's postinstall patches Security.framework, and
# a process that loaded Security before it keeps the old one: the pkg asks
# for a restart for that alone. verify.sh runs after it, in a fresh boot,
# as every later boot of the box will be. MEASURED 2026-10-11: the new
# boot's kernel started 31 s after `shutdown -r`, and SSH answered 48 s
# after it (docs/decisions/0016).
#
# THE BOX'S VAGRANTFILE is the vagrant post-processor's own libvirt
# stanza, in front of the base box's Vagrantfile (vagrantfile_template),
# so the base's stanza comes along: one more, inert under vagrant-qemu,
# for every layer. The base's was rendered for its user, cpu, memory,
# cpus, nic and accelerator, which this build shares with it
# (variables.pkr.hcl), and it names OVMF_CODE.fd, OVMF_VARS.fd and
# opencore.img beside itself, which `include` copies from the base box.
# The OVMF_VARS.fd the box carries is therefore the base
# box's, not this build's efivars.fd, as the base template's box carries
# the firmware data source's pristine copy.
#
# PATHS AND THE SSH KEY. As in templates/mavericks/: what this template
# reads from beside itself is named through ${path.root}, and it carries
# its own byte-identical copy of Vagrant's insecure private key
# (TestVagrantKeyMatchesEmbedded), since this directory ships alone. What
# the build writes is relative to the working directory:
# output-mavericks-aquatransport/,
# output/mavericks-10.9.5-aquatransport-libvirt.box and
# packer-manifest.json.

packer {
  required_plugins {
    macosx = {
      source  = "github.com/mavergreen/macosx"
      version = "~> 0.20261005.1"
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

data "macosx-download" "aquatransport" {
  url    = local.aquatransport_url
  sha256 = local.aquatransport_sha256
}

locals {
  # Vagrant's insecure key unless the base box was built with another.
  effective_ssh_private_key_file = (
    var.ssh_private_key_file != ""
    ? var.ssh_private_key_file
    : "${path.root}/vagrant-standard-insecure-first-boot-only.key.rsa"
  )

  # Where the DMG lands in the guest; install.sh reads it from there.
  aquatransport_guest_dmg = "/private/tmp/AquaTransport.dmg"
}

source "qemu" "mavericks-aquatransport" {
  iso_url          = "${var.base_box}/box_0.img"
  iso_checksum     = "none"
  disk_image       = true
  use_backing_file = false
  skip_resize_disk = true
  format           = "qcow2"

  output_directory = "output-mavericks-aquatransport"
  vm_name          = "mavericks-aquatransport.qcow2"

  accelerator  = var.accelerator
  machine_type = "q35,vmport=off"
  cpu_model    = var.cpu
  memory       = var.memory
  cpus         = var.cpus
  net_device   = var.nic
  headless     = var.headless
  boot_wait    = "0s"

  efi_boot          = true
  efi_firmware_code = "${var.base_box}/OVMF_CODE.fd"
  efi_firmware_vars = "${var.base_box}/OVMF_VARS.fd"

  communicator         = "ssh"
  ssh_username         = var.user
  ssh_private_key_file = local.effective_ssh_private_key_file
  # A finished guest, booting: minutes, not the base's install hour.
  ssh_timeout = "30m"

  shutdown_command = "sudo shutdown -h now"

  qemuargs = [
    ["-drive", "if=pflash,format=raw,unit=0,readonly=on,file=${var.base_box}/OVMF_CODE.fd"],
    ["-drive", "if=pflash,format=raw,unit=1,file={{ .OutputDir }}/efivars.fd"],
    ["-device", "ich9-usb-ehci1,id=usb,bus=pcie.0,addr=0x1d.7,multifunction=on"],
    ["-device", "ich9-usb-uhci1,masterbus=usb.0,firstport=0,bus=pcie.0,addr=0x1d.0,multifunction=on"],
    ["-device", "ich9-usb-uhci2,masterbus=usb.0,firstport=2,bus=pcie.0,addr=0x1d.1"],
    ["-device", "ich9-usb-uhci3,masterbus=usb.0,firstport=4,bus=pcie.0,addr=0x1d.2"],
    ["-drive", "id=opencore,if=none,format=raw,snapshot=on,file=${var.base_box}/opencore.img"],
    ["-device", "usb-storage,bus=usb.0,drive=opencore"],
    # detect-zeroes: the zero-fill provisioner's zeros become holes
    # (TestTheTargetDriveStoresZerosAsHoles).
    ["-drive", "id=target,if=none,format=qcow2,detect-zeroes=unmap,discard=unmap,file={{ .OutputDir }}/{{ .Name }}"],
    ["-device", "ide-hd,bus=ide.0,drive=target"],
    ["-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:{{ .SSHHostPort }}-:22"],
    # The PCI NICs need no bus.
    ["-device", "${var.nic},netdev=net0"],
    ["-device", "usb-kbd,bus=usb.0"],
    ["-device", "usb-mouse,bus=usb.0"],
    ["-device", "VGA,vgamem_mb=64"],
  ]
}

build {
  sources = ["source.qemu.mavericks-aquatransport"]

  # The verified DMG, from the host's Packer cache to the guest.
  provisioner "file" {
    source      = data.macosx-download.aquatransport.path
    destination = local.aquatransport_guest_dmg
    generated   = true
  }

  # Installs AquaTransport.pkg with tap.kext deselected, then removes the
  # DMG, which it is told the path of. timeout: a hung `installer` must not
  # hang the build forever. MEASURED 2026-10-11: 12 s with the upload
  # (docs/decisions/0016); 15m is over seventy times that.
  provisioner "shell" {
    timeout          = "15m"
    script           = "${path.root}/install.sh"
    environment_vars = ["AQUATRANSPORT_DMG=${local.aquatransport_guest_dmg}"]
    execute_command  = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  provisioner "shell" {
    inline            = ["sudo shutdown -r now"]
    expect_disconnect = true
    execute_command   = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # The post-reboot pause: the guest is down, and SSH must not be reached on
  # the old boot. A host-side sleep, not verify's pause_before, because
  # Packer's HCL2 applies only ONE of pause_before and timeout (packer
  # v1.16.1 hcl2template/types.packer_config.go:562-569: `if pb.PauseBefore
  # != 0 { PausedProvisioner } else if pb.Timeout != 0 {
  # TimeoutProvisioner }`), so verify's timeout would be silently dropped.
  provisioner "shell-local" {
    inline = ["sleep 30"]
  }

  # verify.sh judges the guest, and fails the build -- before a broken
  # guest becomes a box -- unless it is still 10.9.5 (13F1911), the pinned
  # Aqua Transport and ModernRootCertificates are installed, tap is
  # neither installed nor loaded, and the system's own curl reaches all
  # three of the done-check's hosts.
  #
  # start_retry_timeout is how long Packer keeps retrying to upload and
  # start the script while SSH is not back up (packer v1.16.1,
  # provisioner/shell/provisioner.go: StartRetryTimeout, "can be set high
  # to allow for reboots", 5m by default) -- 30m, the same as ssh_timeout,
  # for a slow boot. timeout bounds a verify.sh that hangs (a curl on a
  # broken Security patch, say) and wraps those start retries too, so it
  # must exceed the 30s pause before it plus 30m: 45m leaves 14.5 minutes
  # for a script MEASURED at 4 s. There is no pause_before here (see the
  # pause above), and no max_retries: that re-runs a verify.sh
  # that ran and failed, and a failing verdict is the verdict.
  provisioner "shell" {
    start_retry_timeout = "30m"
    timeout             = "45m"

    script           = "${path.root}/verify.sh"
    environment_vars = ["AQUATRANSPORT_VERSION=${local.aquatransport_version}"]
    execute_command  = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # Zeroed free space compresses away (templates/mavericks/'s own
  # zero-fill has the measurements). Last, because nothing after it
  # writes to the disk.
  provisioner "shell" {
    inline          = ["sudo diskutil secureErase freespace 0 /"]
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sh {{ .Path }}"
  }

  # THE BOX, in the base box's shape: the stock vagrant post-processor
  # makes a libvirt-provider box (docs/decisions/0013), with the base
  # box's firmware files and Vagrantfile.
  post-processor "vagrant" {
    output = "output/mavericks-10.9.5-aquatransport-{{ .Provider }}.box"
    include = [
      "${var.base_box}/OVMF_CODE.fd",
      "${var.base_box}/OVMF_VARS.fd",
      "${var.base_box}/opencore.img",
    ]
    vagrantfile_template           = "${var.base_box}/Vagrantfile"
    vagrantfile_template_generated = true
  }

  post-processor "manifest" {}
}
