// Package snowleopardmedia is the snowleopard-media data source: 10.6
// installer media, made unattended, from snowleopard-installer's verified
// volume. It shares mavericks-media's store, listing, payload, updates
// and outputs (datasource/media.ExecuteSnowLeopard); only its HCL is its
// own -- an installer volume where 10.9 takes an ESD, and no openssh,
// since the family's OpenSSH is built for 10.9.
package snowleopardmedia

//go:generate packer-sdc mapstructure-to-hcl2 -type Config -output datasource.hcl2spec.go

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2/hcldec"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-macosx/datasource/media"
	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
)

// Config is snowleopard-media's HCL configuration.
type Config struct {
	// Installer is snowleopard-installer's verified volume -- the template
	// passes data.macosx-snowleopard-installer.<name>.path. Required.
	Installer string `mapstructure:"installer"`
	// User is the guest account the first-boot payload creates, with
	// passwordless sudo. Default "vagrant".
	User string `mapstructure:"user"`
	// AuthorizedKey is the path of an SSH public key file the payload
	// authorizes for User. "" means Vagrant's own insecure key, whose
	// private half this data source then outputs as ssh_private_key_file.
	AuthorizedKey string `mapstructure:"authorized_key"`
	// Updates is none or security: the 10.6.8 combo update and Security
	// Update 2013-004. Default security.
	Updates string `mapstructure:"updates"`
	// ExtraSpaceMiB enlarges the media's partition beyond what the
	// updates already bring with them. Default 0.
	ExtraSpaceMiB int `mapstructure:"extra_space_mib"`
	// PrivopsTimeout bounds one privops microVM pass. Default 15m.
	PrivopsTimeout time.Duration `mapstructure:"privops_timeout"`
	// CacheDir names where the built store lives. "" means
	// packer.CachePath("mavericks"), the store the other data sources
	// share.
	CacheDir string `mapstructure:"cache_dir"`
}

// Datasource is snowleopard-media.
type Datasource struct {
	config Config
}

var _ packersdk.Datasource = new(Datasource)

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return d.config.FlatMapstructure().HCL2Spec()
}

// Configure decodes and checks the configuration, and fills in the
// defaults. It reads nothing from the host: packer validate runs it
// where the installer volume may not exist yet.
func (d *Datasource) Configure(raws ...interface{}) error {
	if err := configHelper.Decode(&d.config, nil, raws...); err != nil {
		return err
	}
	c := &d.config
	if c.User == "" {
		c.User = "vagrant"
	}
	if c.Updates == "" {
		c.Updates = config.DefaultUpdates
	}
	if c.PrivopsTimeout == 0 {
		c.PrivopsTimeout = privops.DefaultTimeout
	}
	var errs []error
	if c.Installer == "" {
		errs = append(errs, errors.New("installer is required: the verified installer volume (data.macosx-snowleopard-installer.<name>.path)"))
	}
	if p := media.UserProblem(c.User); p != "" {
		errs = append(errs, errors.New(p))
	}
	if choices := config.UpdateChoicesFor("snowleopard"); !slices.Contains(choices, c.Updates) {
		errs = append(errs, fmt.Errorf("updates %q: choose one of %s", c.Updates, strings.Join(choices, ", ")))
	}
	if c.ExtraSpaceMiB < 0 {
		errs = append(errs, fmt.Errorf("extra_space_mib wants a whole number of MiB, not %d", c.ExtraSpaceMiB))
	}
	if c.PrivopsTimeout < 0 {
		errs = append(errs, fmt.Errorf("privops_timeout wants a positive duration, such as 30m, not %v", c.PrivopsTimeout))
	}
	return errors.Join(errs...)
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(media.DatasourceOutput).FlatMapstructure().HCL2Spec()
}

func (d *Datasource) Execute() (cty.Value, error) {
	c := d.config
	return media.ExecuteSnowLeopard(media.SnowLeopard{
		Installer: c.Installer, User: c.User, AuthorizedKey: c.AuthorizedKey, Updates: c.Updates,
		ExtraSpaceMiB: c.ExtraSpaceMiB, PrivopsTimeout: c.PrivopsTimeout, CacheDir: c.CacheDir,
	})
}
