package templatetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ValidateDisplay runs every template's `display` cases through run, which
// is a template's own `packer validate` (its required -var arguments in
// base): display takes a QEMU -display value when headless = false, is
// held to what one is made of, and is refused, naming headless, when the
// build is headless and would never show it.
func ValidateDisplay(t *testing.T, run func(args ...string) (string, error), base ...string) {
	t.Helper()
	validate := func(args ...string) (string, error) {
		return run(append(append([]string{"validate"}, base...), args...)...)
	}
	for _, d := range []string{"gtk", "sdl", "cocoa", "none", "vnc=:1", "gtk,zoom-to-fit=on", "vnc=127.0.0.1:5"} {
		t.Run("display="+d+" with headless=false", func(t *testing.T) {
			if out, err := validate("-var", "headless=false", "-var", "display="+d); err != nil {
				t.Fatalf("packer validate -var headless=false -var display=%s: %v\n%s", d, err, out)
			}
		})
	}
	for _, d := range []string{`sdl"; system("true`, "sdl gl=on", "-gtk", "gtk;true", "vnc=unix:/tmp/s"} {
		t.Run("display="+d+" is refused", func(t *testing.T) {
			out, err := validate("-var", "headless=false", "-var", "display="+d)
			if err == nil {
				t.Fatalf("packer validate accepted display=%q:\n%s", d, out)
			}
			if !strings.Contains(out, "Display wants") {
				t.Errorf("validate's error doesn't name the variable (display):\n%s", out)
			}
		})
	}
	t.Run("a display on a headless build is refused", func(t *testing.T) {
		out, err := validate("-var", "display=sdl")
		if err == nil {
			t.Fatalf("packer validate accepted display=sdl with headless left true:\n%s", out)
		}
		if !strings.Contains(out, "display is set: headless must be false") {
			t.Errorf("validate's error doesn't name display and headless:\n%s", out)
		}
	})
}

// QemuSourceTakesDisplay checks that templates/<osName>/<file>'s qemu
// source hands the qemu builder local.display, the display variable once
// it has been checked against headless: validate shows the variable is
// checked, and this that the builder gets it.
func QemuSourceTakesDisplay(t *testing.T, osName, file string) {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	var found bool
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "source" || len(b.Labels) == 0 || b.Labels[0] != "qemu" {
			continue
		}
		found = true
		a, ok := b.Body.Attributes["display"]
		if !ok {
			t.Errorf("%s: source %v sets no display", file, b.Labels)
			continue
		}
		if got := strings.TrimSpace(string(a.Expr.Range().SliceBytes(src))); got != "local.display" {
			t.Errorf("%s: source %v: display = %s, want local.display", file, b.Labels, got)
		}
		if h, ok := b.Body.Attributes["headless"]; !ok || strings.TrimSpace(string(h.Expr.Range().SliceBytes(src))) != "var.headless" {
			t.Errorf("%s: source %v: headless is not var.headless", file, b.Labels)
		}
	}
	if !found {
		t.Fatalf("%s has no qemu source", file)
	}
}
