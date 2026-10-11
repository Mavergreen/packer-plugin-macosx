# The layer's variables. base_box is its own; the rest are
# templates/mavericks/variables.pkr.hcl's, defaults and validations
# included, because this template boots that template's image the same
# way (TestTheBootVariablesMatchTheBaseTemplate holds them identical; only
# a description may differ). They should be what the base box was built
# with: its Vagrantfile, which the layer's box keeps, boots it that way.

variable "base_box" {
  type        = string
  description = "An unpacked Mavericks base box, built by templates/mavericks/: the directory holding box_0.img, OVMF_CODE.fd, OVMF_VARS.fd, opencore.img and Vagrantfile."

  # Each file by name, so a .box tarball, a directory short of a file or
  # a box from another template is refused at validate time, before QEMU
  # or the vagrant post-processor fails on one file. try(): fileexists()
  # is an error, not false, for a path under a file -- base_box naming
  # the .box itself.
  validation {
    condition = alltrue([
      for f in ["box_0.img", "OVMF_CODE.fd", "OVMF_VARS.fd", "opencore.img", "Vagrantfile"] :
      try(fileexists("${var.base_box}/${f}"), false)
    ])
    error_message = "Base_box must be an unpacked Mavericks base box: a directory holding box_0.img, OVMF_CODE.fd, OVMF_VARS.fd, opencore.img and Vagrantfile (tar -xzf the .box, or Vagrant's own copy under ~/.vagrant.d/boxes)."
  }
}

variable "cpu" {
  type        = string
  default     = "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"
  description = "The QEMU -cpu model line. Keep vendor=GenuineIntel on an AMD host. Nehalem and later need kvm.ignore_msrs=Y on the host (docs/decisions/0009)."

  # vendor=GenuineIntel: under KVM a guest gets the host's own CPU vendor
  # unless this line names one, and 10.9's kernel hangs on AuthenticAMD
  # before it prints a line, installing and booting alike (measured
  # 2026-10-06, docs/host-profile.md G2). On Intel it names what the guest
  # gets anyway.

  # The box's Vagrantfile puts this line in a Ruby string, so it is held
  # to what a QEMU -cpu line is made of: a model, then +flag, -flag or
  # key=value, comma-separated.
  validation {
    condition     = can(regex("^[A-Za-z0-9][A-Za-z0-9_.,+=-]*$", var.cpu))
    error_message = "Cpu wants a QEMU -cpu line: a model name, then comma-separated +flag, -flag or key=value, such as Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2."
  }
}

variable "memory" {
  type        = number
  default     = 4096
  description = "Guest memory, in MiB."

  validation {
    condition     = var.memory >= 512
    error_message = "Memory wants at least 512 MiB."
  }
}

variable "cpus" {
  type        = number
  default     = 2
  description = "Guest CPU count."

  validation {
    condition     = var.cpus >= 1
    error_message = "Cpus wants at least 1."
  }
}

variable "nic" {
  type        = string
  default     = "e1000-82545em"
  description = "The guest NIC model."

  validation {
    condition     = contains(["e1000-82545em", "virtio-net-pci"], var.nic)
    error_message = "Nic must be one of: e1000-82545em, virtio-net-pci."
  }
}

variable "accelerator" {
  type        = string
  default     = "kvm"
  description = "The QEMU accelerator. Only kvm has built a guest; the others are the portability seam (docs/decisions/0005)."

  validation {
    condition     = contains(["kvm", "tcg", "hvf", "whpx", "xen", "hax", "nvmm", "none"], var.accelerator)
    error_message = "Accelerator must be one of: kvm, tcg, hvf, whpx, xen, hax, nvmm, none."
  }
}

variable "headless" {
  type        = bool
  default     = true
  description = "Run without a GUI."

  validation {
    condition     = contains([true, false], var.headless)
    error_message = "Headless must be true or false."
  }
}

variable "user" {
  type        = string
  default     = "vagrant"
  description = "The base box's guest account, with passwordless sudo: the user its build was given. The box keeps the base box's Vagrantfile, which logs in as that user too."

  validation {
    condition     = can(regex("^[A-Za-z_][A-Za-z0-9_-]*$", var.user))
    error_message = "User wants a usable account name: letters, digits, underscore or dash, not starting with a digit or dash, and no dot (sudo ignores a sudoers.d file named with one, and the first boot names the account's after it)."
  }
}

variable "ssh_private_key_file" {
  type        = string
  default     = ""
  description = "The private key the build logs in to the base box's guest with: the private half of the base box's authorized_key, when its build set one. \"\" means templates/mavericks-aquatransport/vagrant-standard-insecure-first-boot-only.key.rsa, the matching half of Vagrant's well-known insecure key."

  validation {
    condition     = var.ssh_private_key_file == trimspace(var.ssh_private_key_file)
    error_message = "Ssh_private_key_file must not have leading or trailing whitespace."
  }
}
