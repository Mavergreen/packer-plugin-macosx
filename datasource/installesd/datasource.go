// Package installesd is the mavericks-installesd data source: Apple's
// InstallESD.dmg, fetched once through internal/fetch's osrecovery
// handshake and verified against the repository's pinned checksum, then
// reused from internal/store on every later Execute whose pin (the ESD
// source's checksum and URL) hasn't changed.
package installesd

//go:generate packer-sdc mapstructure-to-hcl2 -type Config,DatasourceOutput -output datasource.hcl2spec.go

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/hcl2helper"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/config"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/fetch"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/inputs"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/store"
)

// Config is mavericks-installesd's HCL configuration: cache_dir alone.
// It names where the built store lives, and, under cache_dir/cache/, the
// one shared, content-addressed download cache every data source's
// fetches land in (a listing change never re-downloads Apple's 5.2 GB
// twice). Empty means packer.CachePath("mavericks").
type Config struct {
	CacheDir string `mapstructure:"cache_dir"`
}

// DatasourceOutput is Execute's result: the verified InstallESD.dmg's
// path in the store, and its pinned sha256.
type DatasourceOutput struct {
	Path   string `mapstructure:"path"`
	SHA256 string `mapstructure:"sha256"`
}

// recipe is a row in every listing, standing for the Go that shapes this
// data source's store entry. There are no goldens to pin it to, and
// little for it to cover: the entry's one file is Apple's InstallESD.dmg,
// whose bytes are held to the pinned sha256 (the listing's own
// source:apple-installesd row), whatever code fetched them. Bump it only
// when what an entry holds changes shape -- its file's name, another file
// beside it, a different way of placing it there -- so that entries an
// older plugin made are not reused as if they had the new shape.
const recipe = "1"

// Datasource is mavericks-installesd.
type Datasource struct {
	config Config
}

var _ packersdk.Datasource = new(Datasource)

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return d.config.FlatMapstructure().HCL2Spec()
}

func (d *Datasource) Configure(raws ...interface{}) error {
	return configHelper.Decode(&d.config, nil, raws...)
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(DatasourceOutput).FlatMapstructure().HCL2Spec()
}

// loadRegistry and recoveryBase are test seams. Production always reads
// the registry this binary was built with and talks to the real
// osrecovery; a test overrides both to a fixture registry and an
// httptest server, the way internal/fetch's own tests do (fakeApple in
// esd_test.go), since Execute takes no arguments a test could pass them
// through instead.
var (
	loadRegistry = pins.Embedded
	recoveryBase = ""
)

// logf reaches Packer's log (PACKER_LOG=1): a data source gets no UI, and
// nothing here may print to stdout, which the plugin's RPC owns.
func logf(format string, a ...any) { log.Printf("mavericks-installesd: "+format, a...) }

func (d *Datasource) Execute() (cty.Value, error) {
	reg, err := loadRegistry()
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}

	rows, err := inputs.RepoRows(reg, "esd", "")
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}
	listing := inputs.Listing(rows, []inputs.Row{{Key: "recipe", Value: recipe}})

	cacheDir := d.config.CacheDir
	if cacheDir == "" {
		if cacheDir, err = packersdk.CachePath("mavericks"); err != nil {
			return cty.NullVal(cty.EmptyObject), err
		}
	}

	st := store.Store{Root: cacheDir, Log: logf}
	dir, _, err := st.Get(context.Background(), "installesd", listing, func(ctx context.Context, dir string) error {
		// The download itself goes to the shared cache at
		// cacheDir/cache/<sha256>/InstallESD.dmg -- config.Paths{Home:
		// cacheDir}, not the store's own per-listing dir -- so every data
		// source (and every listing that still pins the same sha256)
		// downloads Apple's installer at most once. Only the store entry
		// itself is per-listing: this hard-links (or, failing that,
		// copies) the shared, verified file into it under its own name.
		g := &fetch.Getter{Paths: config.Paths{Home: cacheDir}, Log: logf}
		rc := fetch.Recovery{Base: recoveryBase, Log: logf}
		p, err := g.InstallESD(ctx, reg, rc)
		if err != nil {
			return err
		}
		return linkOrCopy(p, filepath.Join(dir, "InstallESD.dmg"))
	})
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}

	src, err := reg.Lookup(fetch.ESDSource)
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}

	output := DatasourceOutput{Path: filepath.Join(dir, "InstallESD.dmg"), SHA256: src.SHA256}
	return hcl2helper.HCL2ValueFromConfig(output, d.OutputSpec()), nil
}

// linkOrCopy places src (fetch's verified, read-only file in the shared
// cache) at dst, the store entry's own InstallESD.dmg name: a hard link
// -- cheap, and ordinarily possible, since the shared cache and the store
// both live under the same cache_dir -- or, failing that (a cache_dir
// spanning more than one filesystem), a full copy. src is never removed:
// fetch's invariant that a verified download is read-only holds either
// way, and the shared cache keeps serving every other data source and
// listing that pins the same sha256.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
