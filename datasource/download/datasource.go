// Package download is the macosx-download data source: any pinned URL,
// fetched once through internal/fetch and verified against its SHA-256,
// into the shared, content-addressed download cache
// (<cache_dir>/cache/<sha256>/<filename>). No store entry is built: the
// cached file is already verified and read-only, so it is the output.
package download

//go:generate packer-sdc mapstructure-to-hcl2 -type Config,DatasourceOutput -output datasource.hcl2spec.go

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/hcl2helper"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
)

// Config is macosx-download's HCL configuration. url and sha256 are
// required; cache_dir empty means packer.CachePath("mavericks").
type Config struct {
	URL      string `mapstructure:"url"`
	SHA256   string `mapstructure:"sha256"`
	CacheDir string `mapstructure:"cache_dir"`
}

// DatasourceOutput is Execute's result: the verified file's absolute
// path (read-only: never open it for writing) and its sha256.
type DatasourceOutput struct {
	Path   string `mapstructure:"path"`
	SHA256 string `mapstructure:"sha256"`
}

// Datasource is macosx-download.
type Datasource struct {
	config Config
}

var _ packersdk.Datasource = new(Datasource)

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return d.config.FlatMapstructure().HCL2Spec()
}

func (d *Datasource) Configure(raws ...interface{}) error {
	if err := configHelper.Decode(&d.config, nil, raws...); err != nil {
		return err
	}
	d.config.SHA256 = strings.ToLower(d.config.SHA256)
	var missing []string
	if d.config.URL == "" {
		missing = append(missing, "url")
	}
	if d.config.SHA256 == "" {
		missing = append(missing, "sha256")
	}
	if len(missing) > 0 {
		return fmt.Errorf("macosx-download: %s required", strings.Join(missing, " and "))
	}
	if !sha256Hex.MatchString(d.config.SHA256) {
		return fmt.Errorf("macosx-download: sha256 %q must be 64 hex characters", d.config.SHA256)
	}
	return nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(DatasourceOutput).FlatMapstructure().HCL2Spec()
}

// logf reaches Packer's log (PACKER_LOG=1): a data source gets no UI, and
// nothing here may print to stdout, which the plugin's RPC owns.
func logf(format string, a ...any) { log.Printf("macosx-download: "+format, a...) }

func (d *Datasource) Execute() (cty.Value, error) {
	null := cty.NullVal(cty.EmptyObject)
	cacheDir := d.config.CacheDir
	if cacheDir == "" {
		var err error
		if cacheDir, err = packersdk.CachePath("mavericks"); err != nil {
			return null, err
		}
	}
	name, err := fetch.Filename(d.config.URL)
	if err != nil {
		return null, err
	}
	g := &fetch.Getter{Paths: config.Paths{Home: cacheDir}, Log: logf}
	path, err := g.Get(context.Background(), fetch.Item{
		Name:     name,
		URL:      d.config.URL,
		SHA256:   d.config.SHA256,
		Filename: name,
	})
	if err != nil {
		var moved *fetch.DownloadMismatchError
		if errors.As(err, &moved) {
			err = fmt.Errorf("%w: the file at this URL has changed since it was pinned; a newer release of packer-plugin-macosx may pin it", err)
		}
		return null, err
	}
	output := DatasourceOutput{Path: path, SHA256: d.config.SHA256}
	return hcl2helper.HCL2ValueFromConfig(output, d.OutputSpec()), nil
}
