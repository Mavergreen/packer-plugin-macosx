// Package template holds the Snow Leopard template's own tests: real
// packer against snowleopard.pkr.hcl, as templates/mavericks' tests do
// for Mavericks (internal/templatetest has the shared machinery).
package template

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	macosx "github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/templatetest"
)

const osName = "snowleopard"

func TestValidate(t *testing.T) {
	root := templatetest.RepoRoot(t)
	packer := templatetest.PackerBinary(t)
	seed := templatetest.SeedPluginDir(t)

	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "plugins")
	if err := templatetest.CopyTree(seed, pluginDir); err != nil {
		t.Fatalf("seeding plugin dir: %v", err)
	}
	templatetest.DevInstall(t, root, packer, pluginDir)
	cacheDir := filepath.Join(tmp, "cache")
	tmpl := templatetest.ShippedCopy(t, root, osName)
	elsewhere := filepath.Join(tmp, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(packer, append(args, tmpl)...)
		cmd.Dir = elsewhere
		cmd.Env = append(os.Environ(), "PACKER_PLUGIN_PATH="+pluginDir, "PACKER_CACHE_DIR="+cacheDir, "CHECKPOINT_DISABLE=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	const disc = "installer=/path/to/your/Snow Leopard.iso"

	t.Run("valid template", func(t *testing.T) {
		if out, err := run("validate", "-var", disc); err != nil {
			t.Fatalf("packer validate: %v\n%s", err, out)
		}
		if empty, err := templatetest.DirEmpty(cacheDir); err != nil || !empty {
			t.Errorf("packer validate ran a data source: %s is not empty (%v)", cacheDir, err)
		}
	})

	t.Run("no installer", func(t *testing.T) {
		out, err := run("validate")
		if err == nil {
			t.Fatalf("packer validate accepted a build with no installer:\n%s", out)
		}
		if !strings.Contains(out, "installer") {
			t.Errorf("validate's error doesn't name the variable (installer):\n%s", out)
		}
	})

	t.Run("updates=all is not 10.6's", func(t *testing.T) {
		out, err := run("validate", "-var", disc, "-var", "updates=all")
		if err == nil {
			t.Fatalf("packer validate accepted updates=all:\n%s", out)
		}
	})

	t.Run("openssh is not 10.6's", func(t *testing.T) {
		out, err := run("validate", "-var", disc, "-var", "openssh=true")
		if err == nil {
			t.Fatalf("packer validate accepted an openssh variable:\n%s", out)
		}
	})
}

// TestVagrantKeyMatchesEmbedded: the template's copy of Vagrant's insecure
// key is the plugin's, byte for byte, as Mavericks' is.
func TestVagrantKeyMatchesEmbedded(t *testing.T) {
	got, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, "vagrant-standard-insecure-first-boot-only.key.rsa"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(macosx.Files, "assets/vagrant/vagrant-standard-insecure-first-boot-only.key.rsa")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("templates/snowleopard's Vagrant key is not the embedded one")
	}
}

// TestBoxVagrantfileReEnablesSSHRSA: `vagrant ssh` runs the host's own
// OpenSSH client, which refuses 10.6's ssh-rsa host keys and signatures
// unless told otherwise (MEASURED 2026-10-04).
func TestBoxVagrantfileReEnablesSSHRSA(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, "box.Vagrantfile.pkrtpl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HostKeyAlgorithms=+ssh-rsa", "PubkeyAcceptedAlgorithms=+ssh-rsa", "config.ssh.extra_args"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("box.Vagrantfile.pkrtpl does not carry %q", want)
		}
	}
}

// TestNoUHCIForSnowLeopard: 10.6.0's AppleUSBUHCI hangs polling a halted
// QEMU UHCI controller with interrupts off, wedging the install at one
// CPU or two (MEASURED 2026-10-04, docs/decisions/0014). So neither the
// build nor the box gives 10.6 a UHCI controller, or the USB 1.1 keyboard
// and mouse that would need one; OpenCore's disk is on XHCI, which OVMF
// boots from and 10.6 has no driver for.
func TestNoUHCIForSnowLeopard(t *testing.T) {
	for _, f := range []string{"snowleopard.pkr.hcl", "box.Vagrantfile.pkrtpl"} {
		b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"uhci", "usb-kbd", "usb-mouse", "ich9-usb-ehci1"} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s attaches %s", f, bad)
			}
		}
		if !bytes.Contains(b, []byte("qemu-xhci")) {
			t.Errorf("%s puts OpenCore's disk on no XHCI controller", f)
		}
	}
}

// TestOneCPUByDefault: with two vCPUs on a busy host, 10.6.0 under KVM
// panics (MEASURED 2026-10-04, docs/decisions/0014), so the template asks
// for one unless told otherwise.
func TestOneCPUByDefault(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, "variables.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(b, []byte(`variable "cpus" {`))
	if i < 0 {
		t.Fatal("no cpus variable")
	}
	block := b[i:]
	block = block[:bytes.Index(block, []byte("\n}\n"))]
	if !bytes.Contains(block, []byte("default     = 1\n")) {
		t.Fatalf("cpus does not default to 1:\n%s", block)
	}
}
