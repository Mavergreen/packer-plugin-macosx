package template

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/templatetest"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/box.Vagrantfile.golden from the template's rendering")

// TestBoxVagrantfileRendersTheMeasuredDefaults: with the template's default
// variables, the rendered box Vagrantfile is the measured one, byte for
// byte (testdata/box.Vagrantfile.golden), as Mavericks' is. A change to it
// is deliberate, reviewed in the golden's diff, and measured on a real
// box: the shape tests say what must hold, this says that nothing else
// moved. Rewrite it with `go test ./templates/snowleopard -run
// MeasuredDefaults -update`.
func TestBoxVagrantfileRendersTheMeasuredDefaults(t *testing.T) {
	got := templatetest.RenderBoxVagrantfile(t, osName, boxOverrides)
	path := filepath.Join("testdata", "box.Vagrantfile.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the default box Vagrantfile is not the measured one (%s); if the change is deliberate, rewrite the golden with -update and review its diff", path)
	}
}
