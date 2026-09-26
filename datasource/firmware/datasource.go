// Package firmware is the mavericks-firmware data source: OVMF and the
// OpenCore EFI image, built once from pinned source into a persistent
// firmware workspace under the shared cache, and reused from
// internal/store on every later Execute whose listing (the pinned
// sources, the patches, the compiler, the smbios model and the debug
// switch) hasn't changed.
package firmware

//go:generate packer-sdc mapstructure-to-hcl2 -type Config,DatasourceOutput -output datasource.hcl2spec.go

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/hcl2helper"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/config"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/fetch"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/firmware"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/inputs"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/lock"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/store"
)

// Config is mavericks-firmware's HCL configuration.
type Config struct {
	// SMBIOS is the guest's SMBIOS model (SystemProductName); "" means
	// firmware.DefaultSMBIOS. Refused, at Configure time, when
	// firmware.SMBIOSWellformed says it cannot go into config.plist.
	SMBIOS string `mapstructure:"smbios"`
	// Debug turns on OpenCore's and the kernel's three debug settings
	// (firmware.DebugSettings: AppleDebug, a DisplayLevel with
	// DEBUG_INFO, and debug=0x100 in boot-args), for diagnosing a boot.
	// Off by default: the image then ships assets/firmware/config.plist
	// as it is.
	Debug bool `mapstructure:"debug"`
	// Ccache compiles the firmware through ccache, if it is installed
	// (off by default: firmware.CcacheDefault).
	Ccache bool `mapstructure:"ccache"`
	// Compiler treats the host compiler as "NAME VERSION" for the
	// firmware's range check, instead of asking the real compiler.
	// "" asks the real compiler.
	Compiler string `mapstructure:"compiler"`
	// CacheDir names where the built store -- and, under it, the shared
	// download cache and the firmware build workspace -- live. ""
	// means packer.CachePath("mavericks").
	CacheDir string `mapstructure:"cache_dir"`
}

// DatasourceOutput is Execute's result: the three files a firmware build
// ships, and the SHA256SUMS naming all three. (The entry also holds
// OVMF.fd, the combined image, which nothing downstream reads.)
type DatasourceOutput struct {
	OVMFCode      string `mapstructure:"ovmf_code"`
	OVMFVars      string `mapstructure:"ovmf_vars"`
	OpenCoreImage string `mapstructure:"opencore_image"`
	SHA256Sums    string `mapstructure:"sha256sums"`
}

// recipe is a row in every listing, standing for the Go that shapes this
// data source's output and that no other row names: internal/firmware
// (the builder, efi.go's EFI image and serial seed, smbios.go's and
// debug.go's config.plist edits), internal/diskimg's FAT and GPT
// writers, and make below (which files the entry holds, and how). Bump
// its number whenever that code changes what an entry holds, so users'
// cached firmware from an older plugin is rebuilt rather than reused.
// The goldens part is the digest of that code's goldens (recipe_test.go's
// recipeGoldens), and TestRecipePinsTheGoldens fails when they change
// and this does not.
const recipe = "1 goldens:2fc9fb39af54abc0"

// Datasource is mavericks-firmware.
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
	if d.config.SMBIOS == "" {
		d.config.SMBIOS = firmware.DefaultSMBIOS
	}
	if !firmware.SMBIOSWellformed(d.config.SMBIOS) {
		return fmt.Errorf("smbios %q is not a usable SMBIOS model identifier (letters, digits, comma, dot, dash, underscore; 64 at most)", d.config.SMBIOS)
	}
	return nil
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(DatasourceOutput).FlatMapstructure().HCL2Spec()
}

// dedupeRows is rows with every repeat of an identical (Key, Value) pair
// dropped, keeping the first: inputs.Listing sorts anyway, so only
// whether a row is there at all -- not which occurrence -- can affect
// the digest.
func dedupeRows(rows []inputs.Row) []inputs.Row {
	seen := make(map[inputs.Row]bool, len(rows))
	out := make([]inputs.Row, 0, len(rows))
	for _, r := range rows {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// loadRegistry is a test seam: production always reads the registry this
// binary was built with.
var loadRegistry = pins.Embedded

// buildFirmware is what make asks of firmware itself: build OpenCore,
// OVMF, the kexts and the EFI image into b's workspace (b.Paths), from
// in, shipping model's config.plist with the debug settings on or off
// as debug says. A test replaces this var with a fake that writes known
// bytes at b.Paths.Firmware()'s three files and b.Paths.OpenCoreImage(),
// so no test runs a real EDK II build.
var buildFirmware = func(ctx context.Context, b *firmware.Builder, in firmware.Inputs, model string, debug bool) error {
	if _, err := b.OpenCore(ctx, in); err != nil {
		return fmt.Errorf("opencore: %w", err)
	}
	if _, err := b.OVMF(ctx); err != nil {
		return fmt.Errorf("ovmf: %w", err)
	}
	if _, err := b.Kexts(ctx, in); err != nil {
		return fmt.Errorf("kexts: %w", err)
	}
	if _, err := b.EFIImage(ctx, model, debug); err != nil {
		return fmt.Errorf("efi image: %w", err)
	}
	return nil
}

// logf reaches Packer's log (PACKER_LOG=1): a data source gets no UI, and
// nothing here may print to stdout, which the plugin's RPC owns.
func logf(format string, a ...any) { log.Printf("mavericks-firmware: "+format, a...) }

func (d *Datasource) Execute() (cty.Value, error) {
	ctx := context.Background()

	reg, err := loadRegistry()
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}

	tc := firmware.Toolchain{Runner: proc.Exec{}, GCCBin: os.Getenv("GCC_BIN"), Override: d.config.Compiler}
	compiler := tc.CompilerLine(ctx)

	var repo []inputs.Row
	for _, part := range []string{"opencore", "ovmf", "efi"} {
		rows, err := inputs.RepoRows(reg, part, compiler)
		if err != nil {
			return cty.NullVal(cty.EmptyObject), err
		}
		repo = append(repo, rows...)
	}
	// opencore and ovmf share the boot-stack pins, the patch rows, the
	// compiler and build-options rows: deduped so the listing (and its
	// digest) does not carry the same row twice.
	listing := inputs.Listing(dedupeRows(repo), []inputs.Row{
		{Key: "smbios", Value: d.config.SMBIOS},
		{Key: "debug", Value: strconv.FormatBool(d.config.Debug)},
		{Key: "recipe", Value: recipe},
	})

	cacheDir := d.config.CacheDir
	if cacheDir == "" {
		if cacheDir, err = packersdk.CachePath("mavericks"); err != nil {
			return cty.NullVal(cty.EmptyObject), err
		}
	}

	st := store.Store{Root: cacheDir, Log: logf}
	dir, _, err := st.Get(ctx, "firmware", listing, func(ctx context.Context, dir string) error {
		return d.make(ctx, cacheDir, reg, tc, dir)
	})
	if err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}

	// A fresh build just wrote dir/SHA256SUMS itself, but a cache hit
	// reuses a directory this process did not just write: the store's
	// own "complete" marker says the make finished, not that nothing has
	// touched the pflash images or opencore.img since. Checked on every
	// Execute, fresh build or cache hit alike, before anything
	// downstream trusts them.
	if err := firmware.VerifyShipped(dir); err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}

	output := DatasourceOutput{
		OVMFCode:      filepath.Join(dir, "OVMF_CODE.fd"),
		OVMFVars:      filepath.Join(dir, "OVMF_VARS.fd"),
		OpenCoreImage: filepath.Join(dir, "opencore.img"),
		SHA256Sums:    filepath.Join(dir, "SHA256SUMS"),
	}
	return hcl2helper.HCL2ValueFromConfig(output, d.OutputSpec()), nil
}

// make is store.Get's make: it fetches every pinned firmware source
// through the Getter into the shared cache, builds OpenCore, OVMF, the
// kexts and the EFI image in a persistent workspace under cacheDir (warm
// across store keys, so a listing change -- another smbios, say --
// rebuilds nothing that is still pinned the same), then links the OVMF
// images and opencore.img into dir and writes dir/SHA256SUMS for the
// three it ships (firmware.ShippedFiles).
//
// The workspace is shared by every store key (it is not itself part of
// the digest), so two Executes whose listings differ -- another smbios,
// compiler or ccache -- could otherwise build in it at once and corrupt
// each other's tree: store.Get's own lock only serializes same-listing
// callers. make takes its own lock on the workspace, held for all of
// make, so a second build refuses rather than races, naming the holder,
// exactly as internal/store's own lock does for a repeated key.
func (d *Datasource) make(ctx context.Context, cacheDir string, reg *pins.Registry, tc firmware.Toolchain, dir string) (err error) {
	l, err := lock.Acquire(filepath.Join(cacheDir, "firmware-build.lock"), 0)
	if err != nil {
		return err
	}
	defer func() {
		if rerr := l.Release(); err == nil {
			err = rerr
		}
	}()

	workspace := config.Paths{Home: filepath.Join(cacheDir, "firmware-build")}
	// A cache_dir too long for EDK II otherwise fails minutes into the
	// build; refused before anything is fetched.
	for _, target := range []string{"opencore", "ovmf"} {
		if err := firmware.CheckBuildPath(workspace, target); err != nil {
			return fmt.Errorf("%w -- for mavericks-firmware, that means a shorter cache_dir (or PACKER_CACHE_DIR, which packer.CachePath honors, since cache_dir defaults to it)", err)
		}
	}

	g := &fetch.Getter{Paths: config.Paths{Home: cacheDir}, Log: logf}
	in := firmware.Inputs{}
	for _, n := range firmware.SourceNames() {
		p, err := g.Pinned(ctx, reg, n)
		if err != nil {
			return err
		}
		in[n] = p
	}

	b := &firmware.Builder{
		Paths:     workspace,
		Registry:  reg,
		Runner:    proc.Exec{},
		Toolchain: tc,
		Ccache:    d.config.Ccache,
		Env:       os.Environ(),
		Log:       logf,
	}
	if err := buildFirmware(ctx, b, in, d.config.SMBIOS, d.config.Debug); err != nil {
		return err
	}

	for _, n := range firmware.OVMFFiles {
		if err := linkOrCopy(filepath.Join(workspace.Firmware(), n), filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	if err := linkOrCopy(workspace.OpenCoreImage(), filepath.Join(dir, "opencore.img")); err != nil {
		return err
	}
	return firmware.WriteSums(dir, firmware.ShippedFiles)
}

// linkOrCopy places src at dst: a hard link -- cheap, and src and dst
// always share a filesystem here, since the store made dst's directory
// itself -- or, failing that, a full copy. A later build overwriting src
// (an atomic rename onto the same workspace path) leaves an
// already-linked dst untouched: rename replaces the directory entry, not
// the inode a hard link shares.
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
