# packer-plugin-mavericks

A [Packer](https://www.packer.io/) plugin, and a template built on it,
that produce an unattended OS X 10.9 (Mavericks) guest from Apple's own
installer -- no Mac needed to build one. It targets **a Linux host with
Intel hardware and KVM**; AMD hosts are a known-harder case for macOS
guests, and the build refuses one by name (`docs/host-profile.md` G2,
`docs/decisions/0005`).
