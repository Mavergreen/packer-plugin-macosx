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
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/payload"
)

// repoRoot is this file's own location, walked up past templates/mavericks/: robust
// to whatever directory `go test` runs from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// packerBinary is the packer this test drives.
func packerBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("PACKER")
	if bin == "" {
		bin = "packer"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("packer not found (%s); set $PACKER to its path to run this test", err)
	}
	return path
}

// seedPluginDir is where the qemu and vagrant plugins this test needs are
// already downloaded.
func seedPluginDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("PACKER_PLUGIN_PATH")
	if dir == "" {
		t.Skip("PACKER_PLUGIN_PATH not set; nowhere to find the qemu and vagrant plugins this test needs")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("PACKER_PLUGIN_PATH %s: %v", dir, err)
	}
	return dir
}

// copyTree copies src onto dst, creating directories as needed and
// preserving each file's mode: the seeded plugin binaries need their +x,
// and dev-install.sh's own `packer plugins install` writes its manifest
// files alongside them, so the copy -- not the shared seed itself --
// takes that write.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// devInstall builds the plugin from this checkout and installs it into
// pluginDir with bin/dev-install.sh, the same script a developer or CI
// uses -- not a separate, parallel build path this test invents for
// itself.
func devInstall(t *testing.T, root, packer, pluginDir string) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(root, "bin", "dev-install.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PACKER="+packer,
		"PACKER_PLUGIN_PATH="+pluginDir,
		"CHECKPOINT_DISABLE=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bin/dev-install.sh: %v\n%s", err, out)
	}
}

// shippedFiles is the template archive's own file list, read out of
// .goreleaser.yml's "- src: templates/mavericks/<name>" lines: exactly what a
// release carries, which tests/release.bats holds to the tracked
// template files.
func shippedFiles(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^\s*- src: templates/mavericks/(\S+)\s*$`).FindAllStringSubmatch(string(b), -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatal(".goreleaser.yml names no template files")
	}
	return names
}

// shippedCopy copies the files a release's template archive carries --
// and nothing else: no build output, no test -- into a fresh directory,
// and returns it. Validating that copy is what shows templates/mavericks/ is
// self-contained, and a build's leftovers in the checkout's own
// templates/mavericks/ (output-mavericks/, which packer validate refuses) cannot
// affect it.
func shippedCopy(t *testing.T, root string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "template")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range shippedFiles(t, root) {
		b, err := os.ReadFile(filepath.Join(root, "templates", "mavericks", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// dirEmpty reports whether dir has no entries. A PACKER_CACHE_DIR that
// packer validate never asked for stays exactly as empty as one it never
// even created -- both count as "no data source executed".
func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func TestValidate(t *testing.T) {
	root := repoRoot(t)
	packer := packerBinary(t)
	seed := seedPluginDir(t)

	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "plugins")
	if err := copyTree(seed, pluginDir); err != nil {
		t.Fatalf("seeding plugin dir: %v", err)
	}
	devInstall(t, root, packer, pluginDir)

	cacheDir := filepath.Join(tmp, "cache")

	// The template as a release ships it, validated from a working
	// directory that is not the template's own: every file it reads from
	// beside itself must be named through ${path.root}, as CI's
	// `packer validate templates/mavericks/` from the repository root needs.
	tmpl := shippedCopy(t, root)
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
		empty, err := dirEmpty(cacheDir)
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
