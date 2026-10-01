variable "updates" {
  type        = string
  default     = "security"
  description = "Which of Apple's post-10.9.5 updates the media carries: none, security or all."

  validation {
    condition     = contains(["none", "security", "all"], var.updates)
    error_message = "Updates must be one of: none, security, all."
  }
}

variable "openssh" {
  type        = bool
  default     = true
  description = "Carry the family's OpenSSH packages, installed at first boot in place of 10.9's OpenSSH 6.2."

  validation {
    condition     = contains([true, false], var.openssh)
    error_message = "Openssh must be true or false."
  }
}

variable "smbios" {
  type        = string
  default     = "iMac14,2"
  description = "The guest's SMBIOS model (SystemProductName)."

  validation {
    condition     = can(regex("^[A-Za-z]+[A-Za-z0-9]*,[0-9]+$", var.smbios))
    error_message = "Smbios wants a Mac model identifier, such as iMac14,2."
  }
}

variable "debug" {
  type        = bool
  default     = false
  description = "Turn on the three OpenCore/kernel debug settings for diagnosing a boot: AppleDebug, a DisplayLevel with DEBUG_INFO, and debug=0x100 in boot-args."

  validation {
    condition     = contains([true, false], var.debug)
    error_message = "Debug must be true or false."
  }
}

variable "cpu" {
  type        = string
  default     = "Penryn,+ssse3,+sse4.1,+sse4.2"
  description = "The QEMU -cpu model line."

  # The box's Vagrantfile puts this line in a Ruby string, so it is held
  # to what a QEMU -cpu line is made of: a model, then +flag, -flag or
  # key=value, comma-separated.
  validation {
    condition     = can(regex("^[A-Za-z0-9][A-Za-z0-9_.,+=-]*$", var.cpu))
    error_message = "Cpu wants a QEMU -cpu line: a model name, then comma-separated +flag, -flag or key=value, such as Penryn,+ssse3,+sse4.1,+sse4.2."
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

variable "disk_size" {
  type        = string
  default     = "60G"
  description = "The target disk's size, as the qemu builder's disk_size wants it: digits plus an optional b/k/m/g/t suffix."

  validation {
    condition     = can(regex("^[0-9]+[bBkKmMgGtT]?$", var.disk_size))
    error_message = "Disk_size wants digits with an optional b, k, m, g or t suffix, such as 60G."
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

variable "install_timeout" {
  type        = string
  default     = "1h"
  description = "How long the build waits for the guest's SSH communicator to come up."

  validation {
    condition     = can(regex("^[0-9]+(ns|us|ms|s|m|h)$", var.install_timeout))
    error_message = "Install_timeout wants a Go duration, such as 1h or 90m."
  }
}

variable "user" {
  type        = string
  default     = "vagrant"
  description = "The guest account the first-boot payload creates, with passwordless sudo. The box's own Vagrantfile logs in as this user too."

  validation {
    condition     = can(regex("^[A-Za-z_][A-Za-z0-9_-]*$", var.user))
    error_message = "User wants a usable account name: letters, digits, underscore or dash, not starting with a digit or dash, and no dot (sudo ignores a sudoers.d file named with one, and the first boot names the account's after it)."
  }
}

variable "authorized_key" {
  type        = string
  default     = ""
  description = "An SSH public key file's path, authorized for var.user instead of Vagrant's well-known insecure key. \"\" means Vagrant's key, and ssh_private_key_file may then stay \"\" too."

  validation {
    condition     = var.authorized_key == trimspace(var.authorized_key)
    error_message = "Authorized_key must not have leading or trailing whitespace."
  }
}

variable "ssh_private_key_file" {
  type        = string
  default     = ""
  description = "The private half of authorized_key, for the build's own SSH login. Required whenever authorized_key is set; \"\" otherwise means template/vagrant-standard-insecure-first-boot-only.key.rsa, the matching half of Vagrant's well-known insecure key, replaced on first boot."

  validation {
    condition     = var.ssh_private_key_file == trimspace(var.ssh_private_key_file)
    error_message = "Ssh_private_key_file must not have leading or trailing whitespace."
  }
}
