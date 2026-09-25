package firmware

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/config"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// physicalTemp is shortHome with every symlink resolved (macOS's /tmp is
// one), so a test can build a path of an exact physical length on it.
func physicalTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(shortHome(t))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// homeOfLength is a path of exactly n bytes below base, none of it
// existing past base.
func homeOfLength(t *testing.T, base string, n int) string {
	t.Helper()
	pad := n - len(base) - 1
	if pad < 1 {
		t.Fatalf("base %s is already %d bytes", base, len(base))
	}
	return filepath.Join(base, strings.Repeat("h", pad))
}

func TestTheDeepestDebugPathsGiveTheHomeLimits(t *testing.T) {
	// the workspace + "/build/OpenCorePkg-1.0.7/UDK" + "/" + the deepest
	// path below UDK must fit in ImageTool's 255 bytes.
	if got := MaxHomeLength("opencore"); got != 96 {
		t.Errorf("opencore: a workspace path of at most %d bytes, want 96", got)
	}
	if got := MaxHomeLength("ovmf"); got != 64 {
		t.Errorf("ovmf: a workspace path of at most %d bytes, want 64", got)
	}
}

func TestCheckBuildPathAllowsTheLongestHomeAndNoLonger(t *testing.T) {
	base := physicalTemp(t)
	for _, c := range []struct {
		target string
		max    int
	}{{"opencore", 96}, {"ovmf", 64}} {
		if err := CheckBuildPath(config.Paths{Home: homeOfLength(t, base, c.max)}, c.target); err != nil {
			t.Errorf("%s, a %d-byte home: %v", c.target, c.max, err)
		}
		err := CheckBuildPath(config.Paths{Home: homeOfLength(t, base, c.max+1)}, c.target)
		if err == nil {
			t.Fatalf("%s, a %d-byte home: allowed", c.target, c.max+1)
		}
		for _, want := range []string{"255", strconv.Itoa(c.max + 1), strconv.Itoa(c.max), "shorter workspace path"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal lacks %q: %v", c.target, want, err)
			}
		}
	}
}

func TestCheckBuildPathOnlyChecksTheEDKBuilds(t *testing.T) {
	long := config.Paths{Home: "/" + strings.Repeat("h", 200)}
	if err := CheckBuildPath(long, "efi"); err != nil {
		t.Errorf("efi builds nothing with EDK II: %v", err)
	}
}

// The build sees the home through its symlinks: a short name for a long
// directory is a long path.
func TestCheckBuildPathCountsThePhysicalPath(t *testing.T) {
	base := physicalTemp(t)
	long := homeOfLength(t, base, 90)
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "l")
	if err := os.Symlink(long, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckBuildPath(config.Paths{Home: link}, "opencore"); err != nil {
		t.Errorf("a 90-byte physical home, opencore: %v", err)
	}
	err := CheckBuildPath(config.Paths{Home: link}, "ovmf")
	if err == nil || !strings.Contains(err.Error(), long) {
		t.Fatalf("a 90-byte physical home behind a short link, ovmf: %v", err)
	}
}

// ...and EDK II's WORKSPACE is the logical $PWD (edksetup.sh), so a long
// name for a short directory is a long path too.
func TestCheckBuildPathCountsTheLogicalPath(t *testing.T) {
	base := physicalTemp(t)
	link := homeOfLength(t, base, 90)
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckBuildPath(config.Paths{Home: link}, "ovmf"); err == nil {
		t.Fatal("a 90-byte logical home over a short directory, ovmf: allowed")
	}
}

func TestOpenCoreAndOVMFRefuseALongHomeFirst(t *testing.T) {
	for _, target := range []string{"opencore", "ovmf"} {
		f := newFixture(t)
		f.b.Paths.Home = homeOfLength(t, physicalTemp(t), MaxHomeLength(target)+1)
		var err error
		if target == "opencore" {
			_, err = f.openCore()
		} else {
			_, err = f.ovmf()
		}
		if err == nil || !strings.Contains(err.Error(), "shorter workspace path") {
			t.Fatalf("%s: err = %v", target, err)
		}
		if len(f.fake.Calls) != 0 {
			t.Errorf("%s ran %v before refusing", target, f.fake.Calls)
		}
		if _, err := os.Stat(f.b.Paths.Home); !os.IsNotExist(err) {
			t.Errorf("%s made %s before refusing (%v)", target, f.b.Paths.Home, err)
		}
	}
}

// If a path grows past what was measured (a pin bump that did not
// re-measure), the build's own words are caught and explained.
func TestABuildThatHitsTheDebugPathLimitSaysWhy(t *testing.T) {
	f := newFixture(t)
	f.buildOut = "GenFw ...\nERROR: Debug symbol path exceeds maximum allowed range of 255 bytes!\n"
	f.buildErr = &proc.ExitError{Cmd: "./build_oc.tool", Code: 1}
	_, err := f.openCore()
	if err == nil || !strings.Contains(err.Error(), "255") || !strings.Contains(err.Error(), "shorter workspace path") {
		t.Fatalf("err = %v", err)
	}

	f = ovmfFixture(t)
	f.buildOut = "ERROR: Debug symbol path exceeds maximum allowed range of 255 bytes!\n"
	f.buildErr = &proc.ExitError{Cmd: "bash", Code: 1}
	_, err = f.ovmf()
	if err == nil || !strings.Contains(err.Error(), "shorter workspace path") {
		t.Fatalf("ovmf: err = %v", err)
	}
}
