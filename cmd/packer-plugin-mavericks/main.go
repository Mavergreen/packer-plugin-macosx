// Command packer-plugin-mavericks is the Mavericks Packer plugin: three
// data sources (mavericks-installesd, mavericks-firmware, mavericks-media)
// that wrap this repository's fetch/firmware/media/privops libraries.
//
// It lives under cmd/, not the repository root, because the root is
// itself a Go package (vmguest, in embed.go) carrying assets into the
// data sources with go:embed -- go:embed cannot reach outside the
// directory of the package that uses it, and a "package main" at the
// root would make that package unimportable by everything that uses it
// today (internal/fetch, internal/firmware, internal/media, ...).
package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/packer-plugin-sdk/plugin"

	"github.com/Mavergreen/packer-plugin-mavericks/datasource/firmware"
	"github.com/Mavergreen/packer-plugin-mavericks/datasource/installesd"
	"github.com/Mavergreen/packer-plugin-mavericks/datasource/media"
	"github.com/Mavergreen/packer-plugin-mavericks/version"
)

func main() {
	pps := plugin.NewSet()
	pps.RegisterDatasource("installesd", new(installesd.Datasource))
	pps.RegisterDatasource("firmware", new(firmware.Datasource))
	pps.RegisterDatasource("media", new(media.Datasource))
	pps.SetVersion(version.PluginVersion)
	if err := pps.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
