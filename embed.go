// Package vmguest is the repository root. It exists to carry files into
// the plugin's binary with go:embed, which cannot reach outside the
// directory of the package that uses it.
package vmguest

import "embed"

// UpstreamVersion is this product's own version line, a bare YYYYMMDD,
// exactly as UPSTREAM_VERSION holds it (docs/decisions/0012).
//
//go:embed UPSTREAM_VERSION
var UpstreamVersion string

// Files is the data the plugin carries inside its binary, at the paths
// the repository keeps them under assets/.
//
// pins.Ingredients globs components/*/version to find every component
// pin: naming a second component's version file here is what makes that
// glob find it, since go:embed only carries paths named explicitly below.
//
// assets/firmware/patches/*.patch are also embedded: the firmware build
// applies them from the binary, not from the checkout, so a build can
// run from nothing but the plugin's binary itself.
//
// The microVM's scripts travel in the binary for the same reason: its
// /init (assets/privops/init.sh) and the media build's payloads
// (assets/privops/*.sh) run inside the privops guest, and the
// unattended-install hooks (assets/guest/autoinstall/*) are injected into
// the installer media it assembles.
//
// assets/vagrant/vagrant.{pub,key}.{rsa,ed25519} are Vagrant's own
// published insecure keypair (assets/vagrant/README.md): payload.VagrantDefaults
// and payload.VagrantPrivateKey read them from here, not from disk, so a
// binary built from this commit carries the keypair itself, not merely a
// path to it.
//
//go:embed assets/pins/sources.tsv components/openssh/version assets/firmware/config.plist assets/pins/apple-packages.sha256 assets/guest/firstboot.sh assets/guest/postinstall assets/guest/com.mqg.firstboot.plist assets/firmware/patches/*.patch assets/privops/*.sh assets/guest/autoinstall/autoinstall.sh assets/guest/autoinstall/minstallconfig.xml assets/guest/autoinstall/OSInstall.collection assets/vagrant/vagrant.pub.rsa assets/vagrant/vagrant.pub.ed25519 assets/vagrant/vagrant.key.rsa assets/vagrant/vagrant-standard-insecure-first-boot-only.key.ed25519
var Files embed.FS
