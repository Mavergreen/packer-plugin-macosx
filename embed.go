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
//go:embed assets/pins/sources.tsv components/openssh/version assets/firmware/config.plist assets/pins/apple-packages.sha256 assets/firmware/patches/*.patch assets/privops/*.sh
var Files embed.FS
