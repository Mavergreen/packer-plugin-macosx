package snowleopardinstaller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx/internal/disc"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// fakeVM answers as verify-disc.sh would for a faithful 10A432 disc,
// unless build says otherwise, and counts its runs.
type fakeVM struct {
	build string
	runs  int
}

func (f *fakeVM) Run(_ context.Context, _ string, _ []byte, _ []privops.Disk) ([]byte, error) {
	f.runs++
	known, err := disc.KnownDiscs()
	if err != nil {
		return nil, err
	}
	build := f.build
	if build == "" {
		build = known[0].Build
	}
	var b strings.Builder
	b.WriteString("MQG-DISC-BUILD " + build + "\n")
	for n, s := range known[0].Sums {
		b.WriteString("MQG-SUM-DISC " + s + "  " + n + "\n")
	}
	return []byte(b.String()), nil
}

// withFakes points Execute at vm and a runner that runs nothing, for the
// test's duration.
func withFakes(t *testing.T, vm *fakeVM) {
	t.Helper()
	oldVM, oldRunner := newVM, runner
	newVM = func(time.Duration) (disc.VM, error) { return vm, nil }
	runner = &proc.Fake{}
	t.Cleanup(func() { newVM, runner = oldVM, oldRunner })
}

// bareVolume is a disc image that is the HFS+ volume alone.
func bareVolume(t *testing.T) string {
	t.Helper()
	v := make([]byte, 16384)
	copy(v[1024:], "H+")
	p := filepath.Join(t.TempDir(), "Snow Leopard.hfs")
	if err := os.WriteFile(p, v, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func configured(t *testing.T, raw map[string]interface{}) *Datasource {
	t.Helper()
	d := new(Datasource)
	if err := d.Configure(raw); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return d
}

func TestConfigureRequiresPath(t *testing.T) {
	err := new(Datasource).Configure(map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("err = %v; want one naming path", err)
	}
}

func TestConfigureReadsNothingFromTheHost(t *testing.T) {
	// packer validate runs Configure where the disc may not exist yet.
	missing := filepath.Join(t.TempDir(), "no such disc.iso")
	if err := new(Datasource).Configure(map[string]interface{}{"path": missing}); err != nil {
		t.Fatalf("Configure = %v; want nil, since it must not look for the file", err)
	}
}

func TestExecuteRefusesAMissingFile(t *testing.T) {
	withFakes(t, &fakeVM{})
	missing := filepath.Join(t.TempDir(), "no such disc.iso")
	_, err := configured(t, map[string]interface{}{"path": missing, "cache_dir": t.TempDir()}).Execute()
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v; want one naming %s", err, missing)
	}
}

func TestExecuteExtractsVerifiesAndFiles(t *testing.T) {
	vm := &fakeVM{}
	withFakes(t, vm)
	cache := t.TempDir()
	d := configured(t, map[string]interface{}{"path": bareVolume(t), "cache_dir": cache})
	v, err := d.Execute()
	if err != nil {
		t.Fatal(err)
	}
	path := v.GetAttr("path").AsString()
	if !strings.HasPrefix(path, cache) {
		t.Fatalf("path %s is not under the store at %s", path, cache)
	}
	if b, err := os.ReadFile(path); err != nil || string(b[1024:1026]) != "H+" {
		t.Fatalf("%s is not the extracted volume (%v)", path, err)
	}
	if got := v.GetAttr("build").AsString(); got != "10A432" {
		t.Fatalf("build = %q; want 10A432", got)
	}
}

func TestExecuteReusesTheStoreEntry(t *testing.T) {
	vm := &fakeVM{}
	withFakes(t, vm)
	raw := map[string]interface{}{"path": bareVolume(t), "cache_dir": t.TempDir()}
	for i := 0; i < 2; i++ {
		if _, err := configured(t, raw).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if vm.runs != 1 {
		t.Fatalf("the microVM ran %d times; want once, the second Execute reusing the entry", vm.runs)
	}
}

func TestAFailedVerifyLeavesNoStoreEntry(t *testing.T) {
	withFakes(t, &fakeVM{build: "10D573"})
	cache := t.TempDir()
	_, err := configured(t, map[string]interface{}{"path": bareVolume(t), "cache_dir": cache}).Execute()
	if err == nil || !strings.Contains(err.Error(), "10D573") {
		t.Fatalf("err = %v; want the unknown build refused by name", err)
	}
	entries, _ := os.ReadDir(filepath.Join(cache, storeKind))
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("a failed verify left %s in the store", e.Name())
		}
	}
}

func TestTheListingNamesTheImageSum(t *testing.T) {
	img := bareVolume(t)
	rows, err := listing(img)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "image-sha256") || !strings.Contains(joined, "recipe") {
		t.Fatalf("listing = %q; want the image's sha256 and the recipe", joined)
	}
}
