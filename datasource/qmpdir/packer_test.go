package qmpdir_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/templatetest"
)

// The data source, through a real packer and the plugin binary: its directory is made 0700 under a wide-open
// umask, and a second run finds it as it was. Skipped, as the
// template tests are, with no packer or no seeded plugins.
func TestPackerRunsItAndTheDirectoryIsPrivateAndFoundAgain(t *testing.T) {
	root := templatetest.RepoRoot(t)
	packer := templatetest.PackerBinary(t)
	seed := templatetest.SeedPluginDir(t)
	tmp := t.TempDir()
	plugins := filepath.Join(tmp, "plugins")
	if err := templatetest.CopyTree(seed, plugins); err != nil {
		t.Fatal(err)
	}
	templatetest.DevInstall(t, root, packer, plugins)

	work := filepath.Join(tmp, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	tpl := filepath.Join(work, "t.pkr.hcl")
	hcl := `packer {
  required_plugins {
    macosx = {
      source  = "github.com/mavergreen/macosx"
      version = ">= 0.0.0"
    }
  }
}
data "macosx-qmp-dir" "qmp" {
  dir = "output-x-qmp"
}
source "null" "x" {
  communicator = "none"
}
build {
  sources = ["null.x"]
  provisioner "shell-local" {
    inline = ["echo socket=${data.macosx-qmp-dir.qmp.socket}"]
  }
}
`
	if err := os.WriteFile(tpl, []byte(hcl), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(packer, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "PACKER_PLUGIN_PATH="+plugins, "CHECKPOINT_DISABLE=1")
		old := syscall.Umask(0)
		defer syscall.Umask(old)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("packer %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("validate", "-evaluate-datasources", tpl)
	fi, err := os.Lstat(filepath.Join(work, "output-x-qmp"))
	if err != nil {
		t.Fatalf("packer did not make the directory, relative to its working directory: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v under umask 0, want 0700", fi.Mode().Perm())
	}
	// the second run (a build) finds the directory as it was: what is inside is untouched, and a mode
	// that had been widened is made 0700 again
	keep := filepath.Join(work, "output-x-qmp", "keep")
	if err := os.WriteFile(keep, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work, "output-x-qmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := run("build", tpl)
	if fi, err := os.Lstat(filepath.Join(work, "output-x-qmp")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("after the second run: %v, %v; want mode 0700", fi, err)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "inside" {
		t.Errorf("what was inside the directory after the second run: %q, %v", b, err)
	}
	if !strings.Contains(out, "socket=output-x-qmp/qmp.sock") {
		t.Errorf("the template's view of the socket is not the relative path:\n%s", out)
	}
}
