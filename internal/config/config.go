// Package config holds the defaults and cache layout the data sources
// and internal/store share. What goes inside a directory is its owner's
// business: firmware places its own files under Build()/Firmware(),
// media under Build()/MediaWork(), and so on.
package config

import (
	"os"
	"path/filepath"
)

// DefaultQEMU is the QEMU binary the media data source's privops backend
// runs when a template gives it none of its own.
const DefaultQEMU = "qemu-system-x86_64"

// Which post-10.9.5 updates an image carries (docs/decisions/0011): a
// build-time choice whose default is a decision.
var UpdateChoices = []string{"none", "security", "all"}

const DefaultUpdates = "security"

// Paths is the layout under a data source's cache directory.
type Paths struct{ Home string }

func (p Paths) Build() string    { return filepath.Join(p.Home, "build") }
func (p Paths) Firmware() string { return filepath.Join(p.Build(), "firmware") }
func (p Paths) Work() string     { return filepath.Join(p.Home, "work") }
func (p Paths) Cache() string    { return filepath.Join(p.Home, "cache") }

// InstallerMedia is where the media data source writes the installer
// disk image it builds: a build output, so under build/ beside the
// firmware.
func (p Paths) InstallerMedia() string { return filepath.Join(p.Build(), "installer-media.img") }

// MediaWork is the media build's scratch: the raw conversions of the ESD
// and BaseSystem (several GB), the injectables' tar, the microVM console.
func (p Paths) MediaWork() string { return filepath.Join(p.Work(), "media") }

// CacheFile is where a downloaded input with this checksum and filename
// lives: content-addressed, so a changed pin is a different file and a
// cached file can always be re-verified against its own directory name.
// A file here is read-only: never open it for writing.
func (p Paths) CacheFile(sha256, filename string) string {
	return filepath.Join(p.Cache(), sha256, filename)
}

// OpenSSHSums is where a release's own SHA256SUMS is cached, keyed by tag
// (not by which server it came from): cache/openssh/<tag>/SHA256SUMS.
func (p Paths) OpenSSHSums(tag string) string {
	return filepath.Join(p.Cache(), "openssh", tag, "SHA256SUMS")
}

// OpenCoreImage is where the firmware data source builds the OpenCore
// EFI image: build/opencore.img.
func (p Paths) OpenCoreImage() string { return filepath.Join(p.Build(), "opencore.img") }

// RegularFile reports whether p is a regular file, symlinks followed.
func RegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
