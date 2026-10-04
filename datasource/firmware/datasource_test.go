package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/firmware"
	"github.com/Mavergreen/packer-plugin-macosx/internal/inputs"
	"github.com/Mavergreen/packer-plugin-macosx/internal/lock"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

func decodeOutput(t *testing.T, v cty.Value) DatasourceOutput {
	t.Helper()
	m := v.AsValueMap()
	get := func(k string) string {
		f, ok := m[k]
		if !ok {
			t.Fatalf("output %#v is missing %q", m, k)
		}
		return f.AsString()
	}
	return DatasourceOutput{
		OVMFCode:      get("ovmf_code"),
		OVMFVars:      get("ovmf_vars"),
		OpenCoreImage: get("opencore_image"),
		SHA256Sums:    get("sha256sums"),
	}
}

func TestConfigureDefaultsAndRejectsABadSMBIOS(t *testing.T) {
	var empty Datasource
	if err := empty.Configure(map[string]interface{}{}); err != nil {
		t.Fatalf("Configure(no smbios): %v", err)
	}
	if empty.config.SMBIOS != firmware.DefaultSMBIOS {
		t.Fatalf("smbios = %q, want the default %q", empty.config.SMBIOS, firmware.DefaultSMBIOS)
	}
	if empty.config.Ccache != false {
		t.Fatalf("ccache = %v, want false", empty.config.Ccache)
	}
	if empty.config.Debug != false {
		t.Fatalf("debug = %v, want false", empty.config.Debug)
	}

	var set Datasource
	if err := set.Configure(map[string]interface{}{
		"smbios": "MacPro5,1", "debug": true, "ccache": true, "compiler": "gcc 15.1.0", "cache_dir": "/tmp/somewhere",
	}); err != nil {
		t.Fatalf("Configure(everything set): %v", err)
	}
	if set.config.SMBIOS != "MacPro5,1" || !set.config.Debug || !set.config.Ccache || set.config.Compiler != "gcc 15.1.0" || set.config.CacheDir != "/tmp/somewhere" {
		t.Fatalf("config = %+v", set.config)
	}

	var bad Datasource
	err := bad.Configure(map[string]interface{}{"smbios": "not a valid model!"})
	if err == nil || !strings.Contains(err.Error(), "not a usable SMBIOS model identifier") {
		t.Fatalf("a malformed smbios must be refused with the helper's message, got %v", err)
	}

	var unknown Datasource
	if err := unknown.Configure(map[string]interface{}{"bogus": "y"}); err == nil {
		t.Fatal("an unknown configuration key must be refused")
	}
}

// shortCacheDir is a new, empty directory for cache_dir that
// CheckBuildPath allows on any host: under /tmp, not t.TempDir(), whose
// path already names the test and can run past the 64-byte budget the
// ovmf build allows below the workspace's cache_dir once
// "/firmware-build" is joined on.
func shortCacheDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "vf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// fakeSources is an httptest server standing in for every registry
// source firmware.SourceNames() names: each is served, by name, as a few
// bytes unique to it, and a *pins.Registry pinning each to that content's
// real sha256 -- so fetch.Getter.Pinned's own verification (never
// mocked) passes.
func fakeSources(t *testing.T, names []string) (reg *pins.Registry, srv *httptest.Server, requests *int32) {
	t.Helper()
	content := map[string][]byte{}
	var rows []string
	for _, n := range names {
		b := []byte("pinned bytes for " + n)
		content[n] = b
		sum := sha256.Sum256(b)
		rows = append(rows, n+"\t__BASE__/"+n+"\t"+hex.EncodeToString(sum[:]))
	}
	var n int32
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		name := strings.TrimPrefix(r.URL.Path, "/")
		b, ok := content[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	text := strings.ReplaceAll(strings.Join(rows, "\n")+"\n", "__BASE__", srv.URL)
	var err error
	reg, err = pins.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return reg, srv, &n
}

// fakeBuild is a buildFirmware seam that writes known bytes at the
// workspace paths a real build would have shipped to, without running
// Toolchain.Check, requireTools or anything through b.Runner: no test
// runs a real EDK II build.
func fakeBuild(calls *int32) func(context.Context, *firmware.Builder, firmware.Inputs, firmware.Release, string, bool) error {
	return func(_ context.Context, b *firmware.Builder, _ firmware.Inputs, _ firmware.Release, _ string, _ bool) error {
		atomic.AddInt32(calls, 1)
		if err := os.MkdirAll(b.Paths.Firmware(), 0o755); err != nil {
			return err
		}
		for _, n := range firmware.OVMFFiles {
			if err := os.WriteFile(filepath.Join(b.Paths.Firmware(), n), []byte("built "+n), 0o644); err != nil {
				return err
			}
		}
		return os.WriteFile(b.Paths.OpenCoreImage(), []byte("built opencore.img"), 0o644)
	}
}

func TestExecuteBuildsAndReusesTheStore(t *testing.T) {
	reg, _, requests := fakeSources(t, firmware.SourceNames())

	savedLoad, savedBuild := loadRegistry, buildFirmware
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	var buildCalls int32
	buildFirmware = fakeBuild(&buildCalls)
	t.Cleanup(func() { loadRegistry, buildFirmware = savedLoad, savedBuild })

	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": shortCacheDir(t)}); err != nil {
		t.Fatal(err)
	}

	v, err := d.Execute()
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	out := decodeOutput(t, v)

	for path, want := range map[string]string{
		out.OVMFCode:      "built OVMF_CODE.fd",
		out.OVMFVars:      "built OVMF_VARS.fd",
		out.OpenCoreImage: "built opencore.img",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	sums, err := os.ReadFile(out.SHA256Sums)
	if err != nil {
		t.Fatalf("reading %s: %v", out.SHA256Sums, err)
	}
	var wantSums strings.Builder
	for _, n := range []string{"OVMF_CODE.fd", "OVMF_VARS.fd", "opencore.img"} {
		sum, err := fetch.SHA256File(filepath.Join(filepath.Dir(out.OVMFCode), n))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&wantSums, "%s  %s\n", sum, n)
	}
	if string(sums) != wantSums.String() {
		t.Fatalf("SHA256SUMS = %q, want %q", sums, wantSums.String())
	}

	if got := atomic.LoadInt32(&buildCalls); got != 1 {
		t.Fatalf("buildFirmware ran %d times after the first Execute, want 1", got)
	}
	firstRequests := atomic.LoadInt32(requests)
	if firstRequests == 0 {
		t.Fatal("no source was fetched at all")
	}

	v2, err := d.Execute()
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	out2 := decodeOutput(t, v2)
	if out2 != out {
		t.Fatalf("second Execute = %+v, want the same as the first %+v", out2, out)
	}
	if got := atomic.LoadInt32(&buildCalls); got != 1 {
		t.Fatalf("buildFirmware ran %d times after a second Execute, want still 1 (the store must be reused)", got)
	}
	if got := atomic.LoadInt32(requests); got != firstRequests {
		t.Fatalf("%d fetch requests after a second Execute, want still %d (the store must be reused, no re-fetch)", got, firstRequests)
	}

	// TestExecuteRefusesACacheHitWhoseOVMFWasTampered, inline: a cache
	// hit is not a fresh build, and store.Get's own "complete" marker
	// only says the make finished, not that nothing has touched the
	// pflash images since. Corrupting one in the store directory
	// TestExecuteBuildsAndReusesTheStore already built and reused must
	// fail the third Execute, naming the tampered file, without running
	// buildFirmware again (a corrupt cache entry is not a reason to
	// rebuild silently).
	if err := os.WriteFile(out.OVMFVars, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Execute(); err == nil {
		t.Fatal("Execute must refuse a cache hit whose OVMF_VARS.fd no longer matches SHA256SUMS")
	} else if !strings.Contains(err.Error(), out.OVMFVars) {
		t.Fatalf("err = %v, want it to name %s", err, out.OVMFVars)
	}
	if got := atomic.LoadInt32(&buildCalls); got != 1 {
		t.Fatalf("buildFirmware ran %d times after a tampered cache hit, want still 1 (tampering is refused, not silently rebuilt)", got)
	}
}

// TestExecuteRefusesACacheHitWhoseOpenCoreImageWasTampered: opencore.img
// is booted as the guest's first disk, so it is held to the entry's
// SHA256SUMS on every Execute exactly as the pflash pair is.
func TestExecuteRefusesACacheHitWhoseOpenCoreImageWasTampered(t *testing.T) {
	reg, _, _ := fakeSources(t, firmware.SourceNames())
	savedLoad, savedBuild := loadRegistry, buildFirmware
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	var buildCalls int32
	buildFirmware = fakeBuild(&buildCalls)
	t.Cleanup(func() { loadRegistry, buildFirmware = savedLoad, savedBuild })

	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": shortCacheDir(t)}); err != nil {
		t.Fatal(err)
	}
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	out := decodeOutput(t, v)
	// Replaced, not written through: the entry's file is a hard link to
	// the workspace's, which a real tamper need not respect but this
	// test should not corrupt.
	if err := os.Remove(out.OpenCoreImage); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out.OpenCoreImage, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Execute(); err == nil || !strings.Contains(err.Error(), out.OpenCoreImage) {
		t.Fatalf("Execute of a cache hit with a tampered opencore.img = %v, want a refusal naming it", err)
	}
	if got := atomic.LoadInt32(&buildCalls); got != 1 {
		t.Fatalf("buildFirmware ran %d times, want 1", got)
	}
}

func TestTheListingCarriesTheRecipe(t *testing.T) {
	reg, _, _ := fakeSources(t, firmware.SourceNames())
	savedLoad, savedBuild := loadRegistry, buildFirmware
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	var buildCalls int32
	buildFirmware = fakeBuild(&buildCalls)
	t.Cleanup(func() { loadRegistry, buildFirmware = savedLoad, savedBuild })

	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": shortCacheDir(t)}); err != nil {
		t.Fatal(err)
	}
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(decodeOutput(t, v).OVMFCode), "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "recipe\t"+recipe+"\n") {
		t.Fatalf("the listing does not carry recipe %q:\n%s", recipe, b)
	}
}

// TestALongCacheRootIsRefusedBeforeAnyWork: a cache_dir too long for the
// firmware workspace's EDK II build is refused, naming PACKER_CACHE_DIR,
// before any source is fetched or buildFirmware is ever called.
// TestDebugIsARowAndReachesTheBuild: debug off and debug on are two
// store entries in one cache -- the second is built, not reused from the
// first -- and each listing says which it is, and the build is asked for
// that one.
func TestDebugIsARowAndReachesTheBuild(t *testing.T) {
	reg, _, _ := fakeSources(t, firmware.SourceNames())
	savedLoad, savedBuild := loadRegistry, buildFirmware
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	var buildCalls int32
	var asked []bool
	fake := fakeBuild(&buildCalls)
	buildFirmware = func(ctx context.Context, b *firmware.Builder, in firmware.Inputs, rel firmware.Release, model string, debug bool) error {
		asked = append(asked, debug)
		return fake(ctx, b, in, rel, model, debug)
	}
	t.Cleanup(func() { loadRegistry, buildFirmware = savedLoad, savedBuild })

	cacheDir := shortCacheDir(t)
	dirs := map[bool]string{}
	for _, debug := range []bool{false, true} {
		var d Datasource
		raw := map[string]interface{}{"cache_dir": cacheDir}
		if debug {
			raw["debug"] = true
		}
		if err := d.Configure(raw); err != nil {
			t.Fatal(err)
		}
		v, err := d.Execute()
		if err != nil {
			t.Fatalf("debug %v: %v", debug, err)
		}
		dirs[debug] = filepath.Dir(decodeOutput(t, v).OVMFCode)
		b, err := os.ReadFile(filepath.Join(dirs[debug], "inputs"))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("debug\t%v\n", debug); !strings.Contains(string(b), want) {
			t.Fatalf("debug %v: the listing does not carry %q:\n%s", debug, want, b)
		}
	}
	if dirs[false] == dirs[true] {
		t.Fatalf("debug off and on share one store entry, %s", dirs[false])
	}
	if got := atomic.LoadInt32(&buildCalls); got != 2 {
		t.Fatalf("buildFirmware ran %d times, want 2 (debug on is not debug off's entry)", got)
	}
	if want := []bool{false, true}; fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Fatalf("buildFirmware was asked for debug %v, want %v", asked, want)
	}
}

func TestALongCacheRootIsRefusedBeforeAnyWork(t *testing.T) {
	reg, _, requests := fakeSources(t, firmware.SourceNames())

	savedLoad, savedBuild := loadRegistry, buildFirmware
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	buildFirmware = func(context.Context, *firmware.Builder, firmware.Inputs, firmware.Release, string, bool) error {
		t.Fatal("buildFirmware must not run when the cache root is too long")
		return nil
	}
	t.Cleanup(func() { loadRegistry, buildFirmware = savedLoad, savedBuild })

	// Many reasonably-sized components (well under a filesystem's
	// per-component NAME_MAX), so building it -- unlike the EDK II
	// workspace path beneath it -- never itself fails with ENAMETOOLONG.
	parts := []string{t.TempDir()}
	for i := 0; i < 40; i++ {
		parts = append(parts, "segment")
	}
	long := filepath.Join(parts...)
	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": long}); err != nil {
		t.Fatal(err)
	}

	_, err := d.Execute()
	if err == nil {
		t.Fatal("a too-long cache_dir must be refused")
	}
	if !strings.Contains(err.Error(), "PACKER_CACHE_DIR") {
		t.Fatalf("err = %v, want it to mention PACKER_CACHE_DIR", err)
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("%d fetch requests before the refusal, want 0", got)
	}
}

// TestAConcurrentBuildInTheSameWorkspaceIsRefused: the firmware-build
// workspace is shared by every store key, so a second Execute whose
// listing isn't already in the store must not build there while another
// holds the workspace lock -- it is refused, naming the holder, exactly
// as internal/store's own lock refuses a repeated key (store_test.go).
// Once the lock is released, the same Execute succeeds.
func TestAConcurrentBuildInTheSameWorkspaceIsRefused(t *testing.T) {
	reg, _, requests := fakeSources(t, firmware.SourceNames())

	savedLoad, savedBuild := loadRegistry, buildFirmware
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	var buildCalls int32
	buildFirmware = fakeBuild(&buildCalls)
	t.Cleanup(func() { loadRegistry, buildFirmware = savedLoad, savedBuild })

	cacheDir := shortCacheDir(t)
	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": cacheDir, "smbios": "MacPro5,1"}); err != nil {
		t.Fatal(err)
	}

	l, err := lock.Acquire(filepath.Join(cacheDir, "firmware-build.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.Execute()
	if err == nil {
		t.Fatal("a build while the workspace lock is held elsewhere must be refused")
	}
	for _, want := range []string{"already building", fmt.Sprintf("pid %d", os.Getpid())} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if got := atomic.LoadInt32(&buildCalls); got != 0 {
		t.Fatalf("buildFirmware ran %d times while the workspace was held elsewhere, want 0", got)
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("%d fetch requests while the workspace was held elsewhere, want 0", got)
	}

	if err := l.Release(); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Execute(); err != nil {
		t.Fatalf("Execute after the lock is released: %v", err)
	}
	if got := atomic.LoadInt32(&buildCalls); got != 1 {
		t.Fatalf("buildFirmware ran %d times after the lock was released, want 1", got)
	}
}

func listingFor(t *testing.T, d *Datasource) []string {
	t.Helper()
	if err := d.Configure(map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.listing(reg, "gcc (GCC) 13.3.0 -std=gnu17")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestTheTwoReleasesNeverShareAnEFIImage: the firmware store is shared,
// so a 10.6 listing that matched a 10.9 one would hand back 10.9's EFI
// image -- 10.9's config.plist -- for a 10.6 guest.
func TestTheTwoReleasesNeverShareAnEFIImage(t *testing.T) {
	mav := listingFor(t, new(Datasource))
	sl := listingFor(t, &Datasource{Release: firmware.SnowLeopard})
	if inputs.Digest(mav) == inputs.Digest(sl) {
		t.Fatal("the Mavericks and Snow Leopard firmware listings are the same")
	}
	if !slices.Contains(sl, "release\tsnowleopard") {
		t.Fatalf("the Snow Leopard listing does not name its release: %v", sl)
	}
}

// TestTheMavericksListingIsUnchanged: a zero Release is Mavericks, and
// its listing carries no release row, so every Mavericks user's cached
// firmware stays valid.
func TestTheMavericksListingIsUnchanged(t *testing.T) {
	for _, row := range listingFor(t, new(Datasource)) {
		if strings.HasPrefix(row, "release\t") {
			t.Fatalf("the Mavericks listing gained %q", row)
		}
	}
}

func TestSnowLeopardFirmwareDefaultsToItsModel(t *testing.T) {
	d := &Datasource{Release: firmware.SnowLeopard}
	if err := d.Configure(map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	if d.config.SMBIOS != "iMac9,1" {
		t.Fatalf("smbios = %q; want 10.6's default, iMac9,1", d.config.SMBIOS)
	}
}
