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
  output_directory = "output-snowleopard"

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

  # The build's window. The qemu builder ignores display when headless
  # is true, so a display set with headless left true is refused here,
  # naming both, rather than dropped. headless and display are two
  # variables, so the check is a local (templates/mavericks/
  # mavericks.pkr.hcl's A CUSTOM KEY says why), and file() is used only
  # for its error text.
  display = (
    var.headless && var.display != ""
    ? file("display is set: headless must be false too, or the build has no window to show it in")
    : var.display
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
  display      = local.display
  boot_wait    = "0s"

  efi_boot          = true
  efi_firmware_code = data.macosx-snowleopard-firmware.fw.ovmf_code
  efi_firmware_vars = data.macosx-snowleopard-firmware.fw.ovmf_vars

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

  shutdown_command = "sudo shutdown -h now"

  qemuargs = [
    ["-uuid", local.build_uuid],
    ["-drive", "if=pflash,format=raw,unit=0,readonly=on,file=${data.macosx-snowleopard-firmware.fw.ovmf_code}"],
    ["-drive", "if=pflash,format=raw,unit=1,file={{ .OutputDir }}/efivars.fd"],
    ["-drive", "id=target,if=none,format=qcow2,detect-zeroes=unmap,discard=unmap,file={{ .OutputDir }}/{{ .Name }}"],
    ["-device", "ide-hd,bus=ide.0,drive=target"],
    ["-drive", "id=installer,if=none,format=raw,snapshot=on,file=${data.macosx-snowleopard-media.media.path}"],
    ["-device", "ide-hd,bus=ide.1,drive=installer"],
    # OpenCore's disk is a virtio-blk disk: OVMF drives it and boots it
    # first (bootindex), and neither 10.6 nor 10.9 has a virtio driver, so
    # the guest never sees it. On AHCI, 10.6's DiskArbitration called it
    # unreadable at every desktop login (docs/decisions/0035).
    ["-drive", "id=opencore,if=none,format=raw,snapshot=on,file=${data.macosx-snowleopard-firmware.fw.opencore_image}"],
    ["-device", "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10"],
    # restrict=on: the guest reaches nothing off-link for the whole build,
    # installer included, and the communicator's forward still works.
    # Without it 10.6.8's XProtectUpdater and 2013-004's crsud fetched from
    # Apple mid-build, and ocspd's revocation caches for the TLS that crsud
    # made stayed in the image (docs/decisions/0034,
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

  # Zeroed free space, as templates/mavericks/ does: it compresses away,
  # and it overwrites the blocks the first boot freed -- among them the old
  # firstboot.conf's, which held a configured password in plain text until
  # the first boot rewrote the conf without it. The target drive's
  # detect-zeroes=unmap keeps the zeros out of the qcow2. Unmeasured on
  # 10.6: that its diskutil has `secureErase freespace` is REASONED (Apple's
  # diskutil(8) has had it since 10.5), and its cost is reasoned from
  # 10.9's (184 s for a 60 GB disk with detect-zeroes, measured 2026-10-07):
  # 10.6 leaves a little more free space, so about 3-4 minutes more build.
  # Last, because nothing after it writes to the disk.
  provisioner "shell" {
    inline          = ["sudo diskutil secureErase freespace 0 /"]
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
