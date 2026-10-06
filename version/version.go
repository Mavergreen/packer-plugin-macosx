// Package version carries the plugin's own version. Version is "0.0.0"
// in a plain go build, and 0.<UPSTREAM_VERSION>.<N> in bin/dev-install.sh's
// (still -dev, so the templates' constraint takes it); .goreleaser.yml's
// release build sets both vars
// with -ldflags, from the release tag ({{ .Version }}, the
// v0.<UPSTREAM_VERSION>.<N> semver INGREDIENTS.md's version-scheme
// deviation describes), never from UPSTREAM_VERSION directly -- there is no
// N (the release count on that date-line) to read outside the tags.
package version

import "github.com/hashicorp/packer-plugin-sdk/version"

var (
	Version           = "0.0.0"
	VersionPrerelease = "dev"
	PluginVersion     = version.NewPluginVersion(Version, VersionPrerelease, "")
)
