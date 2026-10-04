// Package snowleopardinstaller is the snowleopard-installer data source:
// the user's own image of a retail Mac OS X 10.6 install disc -- the
// plugin never downloads one (docs/decisions/0007) -- with its HFS+
// volume extracted and every installer package on it verified against
// assets/pins/snowleopard-packages.sha256, then reused from
// internal/store on every later Execute whose image is the same.
package snowleopardinstaller

//go:generate packer-sdc mapstructure-to-hcl2 -type Config,DatasourceOutput -output datasource.hcl2spec.go

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/hcl2helper"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/disc"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/inputs"
	"github.com/Mavergreen/packer-plugin-macosx/internal/media"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
	"github.com/Mavergreen/packer-plugin-macosx/internal/store"
)

// Config is snowleopard-installer's HCL configuration.
type Config struct {
	// Path is the user's image of a retail 10.6 install disc: an ISO or
	// a Disk Utility master of the DVD, a .dmg, or the HFS+ volume
	// alone. Required.
	Path string `mapstructure:"path"`
	// PrivopsTimeout bounds the microVM pass that reads the disc.
	// Default 15m.
	PrivopsTimeout time.Duration `mapstructure:"privops_timeout"`
	// CacheDir names where the built store lives. "" means
	// packer.CachePath("mavericks"), the store the other data sources
	// share.
	CacheDir string `mapstructure:"cache_dir"`
}

// DatasourceOutput is Execute's result: the verified volume's path in
// the store, and the disc's build.
type DatasourceOutput struct {
	Path  string `mapstructure:"path"`
	Build string `mapstructure:"build"`
}

// recipe is a row in every listing, standing for the Go that shapes this
// data source's store entry: the volume's file name, the build file
// beside it, and how they are made. Bump it when that changes.
const recipe = "1"

const (
	storeKind  = "snowleopard-installer"
	volumeName = "snowleopard.hfs"
	buildName  = "build"
)

// Datasource is snowleopard-installer.
type Datasource struct {
	config Config
}

var _ packersdk.Datasource = new(Datasource)

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return d.config.FlatMapstructure().HCL2Spec()
}

// Configure decodes and checks the configuration. It reads nothing from
// the host: packer validate runs it where the disc image may not exist.
func (d *Datasource) Configure(raws ...interface{}) error {
	if err := configHelper.Decode(&d.config, nil, raws...); err != nil {
		return err
	}
	c := &d.config
	if c.PrivopsTimeout == 0 {
		c.PrivopsTimeout = privops.DefaultTimeout
	}
	var errs []error
	if c.Path == "" {
		errs = append(errs, errors.New("path is required: your own image of a retail Mac OS X 10.6 install disc"))
	}
	if c.PrivopsTimeout < 0 {
		errs = append(errs, fmt.Errorf("privops_timeout wants a positive duration, such as 30m, not %v", c.PrivopsTimeout))
	}
	return errors.Join(errs...)
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(DatasourceOutput).FlatMapstructure().HCL2Spec()
}

// Test seams: production runs real programs and this host's privops
// microVM.
var (
	runner proc.Runner = proc.Exec{}
	newVM              = func(timeout time.Duration) (disc.VM, error) {
		b, err := privops.NewBackend(proc.Exec{}, config.DefaultQEMU, logf)
		if err != nil {
			return nil, err
		}
		if missing := b.Missing(); len(missing) > 0 {
			return nil, fmt.Errorf("this host cannot run the privops microVM: %s", strings.Join(missing, "; "))
		}
		b.Timeout = timeout
		return b, nil
	}
)

func logf(format string, a ...any) { log.Printf("snowleopard-installer: "+format, a...) }

// listing is the store's key for image: its sha256, which hashing a
// 7.8 GB image costs about 20 s of, and the recipe.
func listing(image string) ([]string, error) {
	sum, err := fetch.SHA256File(image)
	if err != nil {
		return nil, err
	}
	return inputs.Listing(nil, []inputs.Row{{Key: "image-sha256", Value: sum}, {Key: "recipe", Value: recipe}}), nil
}

func (d *Datasource) Execute() (cty.Value, error) {
	c := d.config
	if _, err := os.Stat(c.Path); err != nil {
		return cty.NullVal(cty.EmptyObject), fmt.Errorf("the disc image %s: %w", c.Path, err)
	}
	rows, err := listing(c.Path)
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}
	cacheDir := c.CacheDir
	if cacheDir == "" {
		if cacheDir, err = packersdk.CachePath("mavericks"); err != nil {
			return cty.NullVal(cty.EmptyObject), err
		}
	}

	st := store.Store{Root: cacheDir, Log: logf}
	dir, _, err := st.Get(context.Background(), storeKind, rows, func(ctx context.Context, dir string) error {
		vol := filepath.Join(dir, volumeName)
		logf("extracting the installer volume from %s", c.Path)
		if err := disc.Extract(ctx, runner, c.Path, vol); err != nil {
			return err
		}
		scratch := filepath.Join(dir, ".verify-scratch.img")
		if err := media.CreateHFS(ctx, runner, scratch, 16, "scratch"); err != nil {
			return err
		}
		defer os.Remove(scratch)
		vm, err := newVM(c.PrivopsTimeout)
		if err != nil {
			return err
		}
		logf("checking every installer package against the pinned retail discs")
		build, err := disc.Verify(ctx, vm, scratch, vol)
		if err != nil {
			return err
		}
		logf("build %s, as it shipped", build)
		return os.WriteFile(filepath.Join(dir, buildName), []byte(build+"\n"), 0o644)
	})
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}
	build, err := os.ReadFile(filepath.Join(dir, buildName))
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}
	out := DatasourceOutput{Path: filepath.Join(dir, volumeName), Build: strings.TrimSpace(string(build))}
	return hcl2helper.HCL2ValueFromConfig(out, d.OutputSpec()), nil
}
