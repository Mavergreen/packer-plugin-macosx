// Package template holds the Aqua Transport layer template's own tests:
// real packer against mavericks-aquatransport.pkr.hcl, as
// templates/mavericks' tests do for the base template (internal/templatetest
// has the shared machinery), and static checks of the HCL where a rule is
// about the source itself: the pin's shape, which Renovate rewrites, and
// the variables this template shares with the base.
package template

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	macosx "github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/payload"
	"github.com/Mavergreen/packer-plugin-macosx/internal/templatetest"
)

// osName is the template this package is: templates/<osName>/.
const osName = "mavericks-aquatransport"

// baseBoxFiles is what an unpacked Mavericks base box holds that this
// template reads.
var baseBoxFiles = []string{"box_0.img", "OVMF_CODE.fd", "OVMF_VARS.fd", "opencore.img", "Vagrantfile"}

// mavericksVagrantfile is the line of a real Mavericks base box's
// Vagrantfile that names the template that rendered it (measured on a
// base box built 2026-10-10); a Snow Leopard box's names
// snowleopard.pkr.hcl instead, and holds the same five file names.
const mavericksVagrantfile = "# RENDERED FROM THE BUILD'S OWN SETTINGS. mavericks.pkr.hcl renders this\n"

// fakeBaseBox is a directory holding each of baseBoxFiles, empty except
// for omit, which it leaves out, and the Vagrantfile, which holds
// vagrantfile. packer validate runs no build, so empty files are as good
// as a real box's.
func fakeBaseBox(t *testing.T, omit, vagrantfile string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range baseBoxFiles {
		if n == omit {
			continue
		}
		var content []byte
		if n == "Vagrantfile" {
			content = []byte(vagrantfile)
		}
		if err := os.WriteFile(filepath.Join(dir, n), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// validator returns a function that runs `packer validate` with args on a
// copy of the template as a release ships it, from a working directory
// that is not the template's own, against a plugin directory holding this
// checkout's build; and the cache directory a data source would write to.
func validator(t *testing.T) (run func(args ...string) (string, error), runIn func(dir string, args ...string) (string, error), tmpl, cacheDir string) {
	t.Helper()
	root := templatetest.RepoRoot(t)
	packer := templatetest.PackerBinary(t)
	seed := templatetest.SeedPluginDir(t)

	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "plugins")
	if err := templatetest.CopyTree(seed, pluginDir); err != nil {
		t.Fatalf("seeding plugin dir: %v", err)
	}
	templatetest.DevInstall(t, root, packer, pluginDir)

	cacheDir = filepath.Join(tmp, "cache")
	tmpl = templatetest.ShippedCopy(t, root, osName)
	elsewhere := filepath.Join(tmp, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	runIn = func(dir string, args ...string) (string, error) {
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
	run = func(args ...string) (string, error) {
		return runIn(elsewhere, append(args, tmpl)...)
	}
	return run, runIn, tmpl, cacheDir
}

func TestValidate(t *testing.T) {
	run, runIn, tmpl, cacheDir := validator(t)
	box := "base_box=" + fakeBaseBox(t, "", mavericksVagrantfile)

	t.Run("valid template", func(t *testing.T) {
		out, err := run("validate", "-var", box)
		if err != nil {
			t.Fatalf("packer validate: %v\n%s", err, out)
		}
		// The DMG is the data source's to fetch, and only at build time.
		empty, err := templatetest.DirEmpty(cacheDir)
		if err != nil {
			t.Fatalf("checking cache dir: %v", err)
		}
		if !empty {
			t.Errorf("packer validate ran a data source: %s is not empty", cacheDir)
		}
	})

	t.Run("from inside the template, as the README runs it", func(t *testing.T) {
		if out, err := runIn(tmpl, "validate", "-var", box, "."); err != nil {
			t.Fatalf("packer validate .: %v\n%s", err, out)
		}
	})

	t.Run("no base_box", func(t *testing.T) {
		out, err := run("validate")
		if err == nil {
			t.Fatalf("packer validate accepted a build with no base_box:\n%s", out)
		}
		if !strings.Contains(out, "base_box") {
			t.Errorf("validate's error doesn't name the variable (base_box):\n%s", out)
		}
	})

	for _, nic := range []string{"e1000-82545em", "virtio-net-pci"} {
		t.Run("nic="+nic, func(t *testing.T) {
			if out, err := run("validate", "-var", box, "-var", "nic="+nic); err != nil {
				t.Fatalf("packer validate -var nic=%s: %v\n%s", nic, err, out)
			}
		})
	}

	t.Run("nic=usb-net is rejected", func(t *testing.T) {
		out, err := run("validate", "-var", box, "-var", "nic=usb-net")
		if err == nil {
			t.Fatalf("packer validate accepted nic=usb-net:\n%s", out)
		}
		if !strings.Contains(out, "Nic") {
			t.Errorf("validate's error doesn't name the variable (nic):\n%s", out)
		}
	})

	t.Run("a custom user and key", func(t *testing.T) {
		// ssh_private_key_file is parsed as a private key at validate
		// time (packer-plugin-sdk's communicator config), so it must be a
		// real one: Vagrant's well-known insecure key will do.
		priv := filepath.Join(t.TempDir(), "custom")
		if err := os.WriteFile(priv, payload.VagrantPrivateKey(), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := run("validate", "-var", box, "-var", "user=alice", "-var", "ssh_private_key_file="+priv); err != nil {
			t.Fatalf("packer validate: %v\n%s", err, out)
		}
	})
}

// TestValidateRejectsANonBox: base_box must be what the base template's
// box unpacks to. The .box tarball itself, a directory short of a file, or
// a Snow Leopard box -- the same five names, another OS -- is refused at
// validate time, naming base_box, rather than a build failing later with
// a QEMU or post-processor error about one file, or verify.sh finding
// 10.6.
func TestValidateRejectsANonBox(t *testing.T) {
	run, _, _, _ := validator(t)

	dotBox := filepath.Join(t.TempDir(), "mavericks-10.9.5-libvirt.box")
	if err := os.WriteFile(dotBox, []byte("not unpacked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"an empty directory":              t.TempDir(),
		"the .box file itself":            dotBox,
		"a directory with no Vagrantfile": fakeBaseBox(t, "Vagrantfile", ""),
		"a Snow Leopard box":              fakeBaseBox(t, "", "# RENDERED FROM THE BUILD'S OWN SETTINGS. snowleopard.pkr.hcl renders this\n"),
		"an empty Vagrantfile":            fakeBaseBox(t, "", ""),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run("validate", "-var", "base_box="+dir)
			if err == nil {
				t.Fatalf("packer validate accepted base_box=%s:\n%s", dir, out)
			}
			if !strings.Contains(out, "Base_box") {
				t.Errorf("validate's error doesn't name the variable (Base_box):\n%s", out)
			}
		})
	}
}

// parseHCL parses one of the repository's HCL files.
func parseHCL(t *testing.T, rel ...string) ([]byte, *hclsyntax.Body) {
	t.Helper()
	path := filepath.Join(append([]string{templatetest.RepoRoot(t)}, rel...)...)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	return src, f.Body.(*hclsyntax.Body)
}

// variableShape is a variable block as its source text, attribute by
// attribute and validation block by validation block, with every
// description left out: what it does, not what it says about itself.
func variableShape(src []byte, b *hclsyntax.Block) map[string]string {
	shape := map[string]string{}
	var walk func(prefix string, body *hclsyntax.Body)
	walk = func(prefix string, body *hclsyntax.Body) {
		for name, a := range body.Attributes {
			if name == "description" {
				continue
			}
			shape[prefix+name] = string(a.Expr.Range().SliceBytes(src))
		}
		counts := map[string]int{}
		for _, nb := range body.Blocks {
			n := counts[nb.Type]
			counts[nb.Type]++
			walk(prefix+nb.Type+"["+strconv.Itoa(n)+"].", nb.Body)
		}
	}
	walk("", b.Body)
	return shape
}

// TestTheBootVariablesMatchTheBaseTemplate: this template boots the base
// template's own image, so the variables that say how to boot it -- and
// how to log in -- are the base's, defaults and validations included. A
// default changed in one and not the other would boot the layer a way the
// base never was.
func TestTheBootVariablesMatchTheBaseTemplate(t *testing.T) {
	shared := []string{"cpu", "memory", "cpus", "nic", "accelerator", "headless", "user", "ssh_private_key_file"}
	baseSrc, base := parseHCL(t, "templates", "mavericks", "variables.pkr.hcl")
	layerSrc, layer := parseHCL(t, "templates", osName, "variables.pkr.hcl")

	var names []string
	for _, b := range layer.Blocks {
		if b.Type == "variable" && len(b.Labels) == 1 {
			names = append(names, b.Labels[0])
		}
	}
	sort.Strings(names)
	want := append([]string{"base_box"}, shared...)
	sort.Strings(want)
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("variables = %v, want %v", names, want)
	}

	for _, name := range shared {
		bb := findBlock(base, "variable", name)
		lb := findBlock(layer, "variable", name)
		if bb == nil || lb == nil {
			t.Errorf("variable %q: in the base %v, in the layer %v", name, bb != nil, lb != nil)
			continue
		}
		bs, ls := variableShape(baseSrc, bb), variableShape(layerSrc, lb)
		if len(bs) != len(ls) {
			t.Errorf("variable %q: the base has %v, the layer %v", name, bs, ls)
			continue
		}
		for k, v := range bs {
			if ls[k] != v {
				t.Errorf("variable %q: %s is %s in the base, %s in the layer", name, k, v, ls[k])
			}
		}
	}
}

// findBlock returns the first child block of body with the given type
// and labels, or nil.
func findBlock(body *hclsyntax.Body, typeName string, labels ...string) *hclsyntax.Block {
	for _, b := range body.Blocks {
		if b.Type == typeName && strings.Join(b.Labels, "\x00") == strings.Join(labels, "\x00") {
			return b
		}
	}
	return nil
}

// TestTheTargetDriveStoresZerosAsHoles: the zero-fill writes zeros over
// all of the guest's free space, which a plain qcow2 drive stores
// (templates/mavericks' test of the same name has the measurements).
func TestTheTargetDriveStoresZerosAsHoles(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, osName+".pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"id=target,`) {
			if !strings.Contains(line, "detect-zeroes=unmap") || !strings.Contains(line, "discard=unmap") {
				t.Fatalf("the target drive does not store zeros as holes:\n%s", line)
			}
			return
		}
	}
	t.Fatal("no target drive")
}

// TestTheLastProvisionerZeroFillsFreeSpace: the box's image is
// compressed, so zeroing the guest's free space before shutdown makes it
// smaller (templates/mavericks' test of the same name has the
// measurements). It runs last, once verify.sh has passed and the guest
// has nothing more to write.
func TestTheLastProvisionerZeroFillsFreeSpace(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, osName+".pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	last := strings.LastIndex(s, "\n  provisioner \"")
	if last < 0 {
		t.Fatal("no provisioner")
	}
	block := s[last:]
	end := strings.Index(block, "\n  }\n")
	if end < 0 {
		t.Fatal("the last provisioner block does not end")
	}
	block = block[:end]
	if !strings.Contains(block, "sudo diskutil secureErase freespace 0 /") {
		t.Fatalf("the last provisioner does not zero free space:\n%s", block)
	}
	if !regexp.MustCompile(`(?m)^\s*script\s*=\s*"\$\{path\.root\}/verify\.sh"$`).MatchString(s[:last]) {
		t.Fatal("the zero-fill does not come after the verify.sh provisioner")
	}
}

// TestThePinIsWellFormed: aquatransport.pkr.hcl is the pin and nothing
// else, because Renovate rewrites it. Its regex manager captures
// aquatransport_version, then aquatransport_sha256 further on, in one
// match: so one locals block, those three names and no others, the
// version before the SHA-256, and each a plain string. The URL is the
// one download there is (it is overwritten in place, never versioned).
func TestThePinIsWellFormed(t *testing.T) {
	src, body := parseHCL(t, "templates", osName, "aquatransport.pkr.hcl")
	if len(body.Attributes) != 0 || len(body.Blocks) != 1 || body.Blocks[0].Type != "locals" {
		var got []string
		for _, b := range body.Blocks {
			got = append(got, b.Type)
		}
		t.Fatalf("aquatransport.pkr.hcl holds blocks %v and %d top-level attributes, want exactly one locals block", got, len(body.Attributes))
	}
	attrs := body.Blocks[0].Body.Attributes
	if len(body.Blocks[0].Body.Blocks) != 0 {
		t.Error("the locals block holds a nested block")
	}
	want := []string{"aquatransport_sha256", "aquatransport_url", "aquatransport_version"}
	var names []string
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("locals = %v, want %v", names, want)
	}
	value := func(name string) string {
		v, diags := attrs[name].Expr.Value(nil)
		if diags.HasErrors() || !v.Type().Equals(cty.String) {
			t.Fatalf("%s is not a plain string: %s (%v)", name, attrs[name].Expr.Range().SliceBytes(src), diags)
		}
		return v.AsString()
	}
	if got := value("aquatransport_url"); got != "https://mavericksforever.com/downloads/AquaTransport.dmg" {
		t.Errorf("aquatransport_url = %q", got)
	}
	if got := value("aquatransport_sha256"); !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Errorf("aquatransport_sha256 = %q, want 64 lowercase hex characters", got)
	}
	// The versioning Renovate compares versions with.
	if got := value("aquatransport_version"); !regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}$`).MatchString(got) {
		t.Errorf("aquatransport_version = %q, want YYYY.MM.DD", got)
	}
	if v, s := attrs["aquatransport_version"].SrcRange.Start.Byte, attrs["aquatransport_sha256"].SrcRange.Start.Byte; v > s {
		t.Error("aquatransport_version comes after aquatransport_sha256: Renovate's regex reads the version first")
	}
}

// TestVagrantKeyMatchesEmbedded: the template's copy of Vagrant's insecure
// key is the plugin's, byte for byte, as the base template's is.
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
		t.Fatal("templates/mavericks-aquatransport's Vagrant key is not the embedded one")
	}
}
