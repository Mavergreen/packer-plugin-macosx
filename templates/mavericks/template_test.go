// Package template holds the Packer template's own acceptance test: it
// drives a real `packer` binary against mavericks.pkr.hcl and
// variables.pkr.hcl, the same way a developer or CI would, rather than
// asserting anything about the HCL in isolation.
//
// The test is skipped, not failed, when it cannot do that: no `packer`
// binary (set $PACKER to one, or put it on $PATH), or no pre-fetched
// qemu/vagrant plugins to seed from (set $PACKER_PLUGIN_PATH, exactly as
// `packer` itself would read it, to the directory that already holds
// them -- this test never fetches a plugin over the network). A host
// without either of those is not a broken build.
package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/payload"
	"github.com/Mavergreen/packer-plugin-macosx/internal/templatetest"
)

// osName is the template this package is: templates/<osName>/.
const osName = "mavericks"

func repoRoot(t *testing.T) string { return templatetest.RepoRoot(t) }

func TestValidate(t *testing.T) {
	root := repoRoot(t)
	packer := templatetest.PackerBinary(t)
	seed := templatetest.SeedPluginDir(t)

	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "plugins")
	if err := templatetest.CopyTree(seed, pluginDir); err != nil {
		t.Fatalf("seeding plugin dir: %v", err)
	}
	templatetest.DevInstall(t, root, packer, pluginDir)

	cacheDir := filepath.Join(tmp, "cache")

	// The template as a release ships it, validated from a working
	// directory that is not the template's own: every file it reads from
	// beside itself must be named through ${path.root}, as CI's
	// `packer validate templates/mavericks/` from the repository root needs.
	tmpl := templatetest.ShippedCopy(t, root, osName)
	elsewhere := filepath.Join(tmp, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}

	runIn := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(packer, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"PACKER_PLUGIN_PATH="+pluginDir,
			"PACKER_CACHE_DIR="+cacheDir,
			"CHECKPOINT_DISABLE=1",
		)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// run validates the shipped copy from elsewhere; args before the
	// template's path.
	run := func(args ...string) (string, error) {
		return runIn(elsewhere, append(args, tmpl)...)
	}

	t.Run("valid template", func(t *testing.T) {
		out, err := run("validate")
		if err != nil {
			t.Fatalf("packer validate: %v\n%s", err, out)
		}
		empty, err := templatetest.DirEmpty(cacheDir)
		if err != nil {
			t.Fatalf("checking cache dir: %v", err)
		}
		if !empty {
			t.Errorf("packer validate ran a data source: %s is not empty", cacheDir)
		}
	})

	t.Run("from inside the template, as the README runs it", func(t *testing.T) {
		if out, err := runIn(tmpl, "validate", "."); err != nil {
			t.Fatalf("packer validate .: %v\n%s", err, out)
		}
	})

	for _, nic := range []string{"e1000-82545em", "virtio-net-pci"} {
		t.Run("nic="+nic, func(t *testing.T) {
			if out, err := run("validate", "-var", "nic="+nic); err != nil {
				t.Fatalf("packer validate -var nic=%s: %v\n%s", nic, err, out)
			}
		})
	}

	t.Run("nic=usb-net is rejected", func(t *testing.T) {
		out, err := run("validate", "-var", "nic=usb-net")
		if err == nil {
			t.Fatalf("packer validate accepted nic=usb-net:\n%s", out)
		}
		if !strings.Contains(out, "Nic") {
			t.Errorf("validate's error doesn't name the variable (nic):\n%s", out)
		}
	})

	t.Run("a user name with a dot", func(t *testing.T) {
		// sudo's #includedir skips a sudoers.d file named with a dot, and
		// the first boot names the account's fragment after it.
		out, err := run("validate", "-var", "user=first.last")
		if err == nil {
			t.Fatalf("packer validate accepted user=first.last:\n%s", out)
		}
		if !strings.Contains(out, "user") {
			t.Errorf("validate's error doesn't name the variable (user):\n%s", out)
		}
	})

	t.Run("a custom user", func(t *testing.T) {
		if out, err := run("validate", "-var", "user=alice", "-var", "nic=virtio-net-pci"); err != nil {
			t.Fatalf("packer validate -var user=alice -var nic=virtio-net-pci: %v\n%s", err, out)
		}
	})

	t.Run("a cpu line that is not a QEMU -cpu line", func(t *testing.T) {
		// The box's Vagrantfile puts cpu in a Ruby string: a quote or
		// an interpolation there would be Ruby code.
		out, err := run("validate", "-var", `cpu=host"; system("true`)
		if err == nil {
			t.Fatalf("packer validate accepted a cpu line with a quote in it:\n%s", out)
		}
		if !strings.Contains(out, "cpu") {
			t.Errorf("validate's error doesn't name the variable (cpu):\n%s", out)
		}
	})

	t.Run("variable outside its choices", func(t *testing.T) {
		out, err := run("validate", "-var", "updates=bogus")
		if err == nil {
			t.Fatalf("packer validate accepted updates=bogus:\n%s", out)
		}
		if !strings.Contains(out, "updates") {
			t.Errorf("validate's error doesn't name the variable (updates):\n%s", out)
		}
	})

	t.Run("authorized_key without ssh_private_key_file", func(t *testing.T) {
		out, err := run("validate", "-var", "authorized_key=/some/key.pub")
		if err == nil {
			t.Fatalf("packer validate accepted authorized_key with no ssh_private_key_file:\n%s", out)
		}
		if !strings.Contains(out, "ssh_private_key_file") {
			t.Errorf("validate's error doesn't name ssh_private_key_file:\n%s", out)
		}
	})

	t.Run("authorized_key with ssh_private_key_file", func(t *testing.T) {
		keyDir := t.TempDir()
		pub := filepath.Join(keyDir, "custom.pub")
		priv := filepath.Join(keyDir, "custom")
		// Any bytes will do for authorized_key: nothing at plain validate
		// time reads or parses it (mavericks-media's own Configure
		// doesn't touch the file; Execute, which does, never runs). But
		// ssh_private_key_file is genuinely parsed as a private key by
		// packer-plugin-sdk's communicator config (helper/ssh.FileSigner),
		// so it must be a real one -- reusing Vagrant's well-known
		// insecure key is as good as generating a fresh one for that.
		if err := os.WriteFile(pub, []byte("ssh-rsa AAAAfake test-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(priv, payload.VagrantPrivateKey(), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := run("validate", "-var", "authorized_key="+pub, "-var", "ssh_private_key_file="+priv)
		if err != nil {
			t.Fatalf("packer validate: %v\n%s", err, out)
		}
	})
}

// TestTheDefaultCPUSaysGenuineIntel: under KVM a guest gets the host's
// own CPU vendor unless the -cpu line names one, and 10.9's kernel hangs
// on AuthenticAMD before it prints a line, at install and at boot alike
// (measured 2026-10-06 on GitHub's AMD EPYC runners, Mavergreen/mavericks-vm
// Actions run 37524541544). On an Intel host the vendor it names is the one the
// guest would get anyway.
func TestTheDefaultCPUSaysGenuineIntel(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, "variables.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, `variable "cpu" {`)
	if i < 0 {
		t.Fatal("no cpu variable")
	}
	block := s[i:]
	block = block[:strings.Index(block, "\n}\n")]
	if !strings.Contains(block, `default     = "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"`) {
		t.Fatalf("cpu does not default to Penryn with vendor=GenuineIntel:\n%s", block)
	}
}
