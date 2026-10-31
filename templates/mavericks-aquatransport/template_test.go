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
	"time"

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
const mavericksVagrantfile = "# RENDERED FROM THE BUILD'S OWN SETTINGS. mavericks.pkr.hcl renders this\n" + oneShapeRow

// oneShapeRow is the OpenCore row of a base box built with the one machine
// shape (docs/decisions/0035). The layer's box keeps the base's
// Vagrantfile, so a base box from before it would give the layer's box the
// old shape while the layer's build boots the new one.
const oneShapeRow = "      -device virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10\n"

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

	templatetest.ValidateDisplay(t, run, "-var", box)
}

// TestTheBuilderGetsTheDisplay: the qemu builder's display is the display
// variable, checked against headless.
func TestTheBuilderGetsTheDisplay(t *testing.T) {
	templatetest.QemuSourceTakesDisplay(t, osName, osName+".pkr.hcl")
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
		"an empty directory":                               t.TempDir(),
		"the .box file itself":                             dotBox,
		"a directory with no Vagrantfile":                  fakeBaseBox(t, "Vagrantfile", ""),
		"a Snow Leopard box":                               fakeBaseBox(t, "", "# RENDERED FROM THE BUILD'S OWN SETTINGS. snowleopard.pkr.hcl renders this\n"),
		"an empty Vagrantfile":                             fakeBaseBox(t, "", ""),
		"a base box from before the one shape":             fakeBaseBox(t, "", "# RENDERED FROM THE BUILD'S OWN SETTINGS. mavericks.pkr.hcl renders this\n      -device usb-storage,bus=usb.0,drive=opencore\n"),
		"a base box with OpenCore on virtio-blk, unpinned": fakeBaseBox(t, "", "# RENDERED FROM THE BUILD'S OWN SETTINGS. mavericks.pkr.hcl renders this\n      -device virtio-blk-pci,drive=opencore,bootindex=0\n"),
		"a base box with OpenCore on AHCI":                 fakeBaseBox(t, "", "# RENDERED FROM THE BUILD'S OWN SETTINGS. mavericks.pkr.hcl renders this\n      -device ide-hd,bus=ide.2,drive=opencore,bootindex=0\n"),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run("validate", "-var", "base_box="+dir)
			if err == nil {
				t.Fatalf("packer validate accepted base_box=%s:\n%s", dir, out)
			}
			if !strings.Contains(out, "Base_box") {
				t.Errorf("validate's error doesn't name the variable (Base_box):\n%s", out)
			}
			if strings.Contains(name, "base box") && !strings.Contains(out, "built before this release's machine shape") {
				t.Errorf("validate's error doesn't say the base box predates the shape:\n%s", out)
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

// TestBaseBoxMarkerIsInTheBaseTemplatesVagrantfile ties base_box's
// validation to the text it depends on: the header of the Vagrantfile
// that templates/mavericks renders. The variable accepts a base box whose
// Vagrantfile matches the pattern it names, which must match the Mavericks
// template and not the Snow Leopard one; and the line the validation
// tests use as a "real" Mavericks Vagrantfile must be in the template. An
// edit to either header that broke this would otherwise refuse every new
// base box with a misleading error, or let a Snow Leopard one through.
func TestBaseBoxMarkerIsInTheBaseTemplatesVagrantfile(t *testing.T) {
	root := templatetest.RepoRoot(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// The patterns, as base_box's validation spells them: regexall("...", file(...)).
	all := regexp.MustCompile(`regexall\("((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(read("templates/mavericks-aquatransport/variables.pkr.hcl"), -1)
	if len(all) != 2 {
		t.Fatalf("variables.pkr.hcl: %d regexall(\"...\") in base_box's validation, want 2 (the template's marker, the one shape's OpenCore row)", len(all))
	}
	shape, err := strconv.Unquote(`"` + all[1][1] + `"`)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(shape).MatchString(read("templates/mavericks/box.Vagrantfile.pkrtpl")) {
		t.Errorf("base_box's one-shape pattern /%s/ is not in templates/mavericks/box.Vagrantfile.pkrtpl: every new base box would be refused", shape)
	}
	if !regexp.MustCompile(shape).MatchString(oneShapeRow) {
		t.Errorf("base_box's one-shape pattern /%s/ does not match the tests' row %q", shape, oneShapeRow)
	}
	m := all[0]
	pattern, err := strconv.Unquote(`"` + m[1] + `"`)
	if err != nil {
		t.Fatalf("unquoting %s: %v", m[1], err)
	}
	marker := regexp.MustCompile(pattern)

	mav := read("templates/mavericks/box.Vagrantfile.pkrtpl")
	sl := read("templates/snowleopard/box.Vagrantfile.pkrtpl")
	if !marker.MatchString(mav) {
		t.Errorf("base_box's marker /%s/ is not in templates/mavericks/box.Vagrantfile.pkrtpl: every Mavericks base box would be refused", pattern)
	}
	if marker.MatchString(sl) {
		t.Errorf("base_box's marker /%s/ is in templates/snowleopard/box.Vagrantfile.pkrtpl: Snow Leopard boxes would pass", pattern)
	}
	for _, line := range strings.SplitAfter(mavericksVagrantfile, "\n") {
		if line != "" && !strings.Contains(mav, line) {
			t.Errorf("the validation tests' Mavericks Vagrantfile line %q is not in box.Vagrantfile.pkrtpl", line)
		}
	}
}

// TestTheBootVariablesMatchTheBaseTemplate: this template boots the base
// template's own image, so the variables that say how to boot it -- and
// how to log in -- are the base's, defaults and validations included. A
// default changed in one and not the other would boot the layer a way the
// base never was.
func TestTheBootVariablesMatchTheBaseTemplate(t *testing.T) {
	shared := []string{"cpu", "memory", "cpus", "nic", "accelerator", "headless", "display", "user", "ssh_private_key_file"}
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

// buildProvisioners returns the build block's provisioner blocks, in order.
func buildProvisioners(t *testing.T) []*hclsyntax.Block {
	t.Helper()
	_, body := parseHCL(t, "templates", osName, osName+".pkr.hcl")
	var out []*hclsyntax.Block
	for _, b := range body.Blocks {
		if b.Type != "build" {
			continue
		}
		for _, p := range b.Body.Blocks {
			if p.Type == "provisioner" {
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no provisioners in the build block")
	}
	return out
}

// attrDur is the literal duration string attribute name of b, or 0.
func attrDur(t *testing.T, b *hclsyntax.Block, name string) time.Duration {
	t.Helper()
	a, ok := b.Body.Attributes[name]
	if !ok {
		return 0
	}
	v, diags := a.Expr.Value(nil)
	if diags.HasErrors() || v.Type() != cty.String {
		t.Fatalf("%s is not a literal string: %v", name, diags)
	}
	d, err := time.ParseDuration(v.AsString())
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return d
}

// TestNoProvisionerSetsBothPauseBeforeAndTimeout: Packer's HCL2 path applies
// only ONE of them. packer v1.16.1 hcl2template/types.packer_config.go:562-569
// is `if pb.PauseBefore != 0 { PausedProvisioner } else if pb.Timeout != 0 {
// TimeoutProvisioner }`, so a provisioner with both silently loses its
// timeout (a real build printed no "Setting a ... timeout" line before it).
func TestNoProvisionerSetsBothPauseBeforeAndTimeout(t *testing.T) {
	for i, p := range buildProvisioners(t) {
		_, pause := p.Body.Attributes["pause_before"]
		_, timeout := p.Body.Attributes["timeout"]
		if pause && timeout {
			t.Errorf("provisioner %d sets both pause_before and timeout; Packer drops the timeout", i)
		}
	}
}

// TestThePostRebootPauseIsAHostSideSleepBeforeVerify: the pause after the
// reboot is a shell-local `sleep` immediately between the reboot provisioner
// and verify.sh's, because verify.sh's own provisioner cannot carry both a
// pause_before and a timeout (TestNoProvisionerSetsBothPauseBeforeAndTimeout).
func TestThePostRebootPauseIsAHostSideSleepBeforeVerify(t *testing.T) {
	ps := buildProvisioners(t)
	src := mustRead(t, "templates", osName, osName+".pkr.hcl")
	text := func(p *hclsyntax.Block) string { return string(p.Range().SliceBytes(src)) }
	verify := -1
	for i, p := range ps {
		if strings.Contains(text(p), "verify.sh") && p.Body.Attributes["script"] != nil {
			verify = i
		}
	}
	if verify < 2 {
		t.Fatalf("verify.sh provisioner at index %d, want after a reboot and a pause", verify)
	}
	if !strings.Contains(text(ps[verify-2]), "sudo /usr/local/sbin/guest-macosx-shutdown -r") {
		t.Errorf("the provisioner two before verify is not the reboot:\n%s", text(ps[verify-2]))
	}
	pause := ps[verify-1]
	if len(pause.Labels) == 0 {
		t.Fatalf("the provisioner just before verify has no type label:\n%s", text(pause))
	}
	if pause.Labels[0] != "shell-local" || !strings.Contains(text(pause), `"sleep 30"`) {
		t.Errorf("the provisioner just before verify is not a shell-local sleep 30:\n%s", text(pause))
	}
}

// TestTheGuestScriptsAreTimeBounded: a hung installer, or a pkgutil or
// kextstat that hangs on a broken Security patch, would otherwise hang
// `packer build` forever.
// Packer's per-provisioner timeout wraps the start retries too, so
// verify.sh's must exceed the shell-local pause before it (30s) plus its
// start_retry_timeout, or a slow reboot is cut short by the very bound
// meant for a hang.
func TestTheGuestScriptsAreTimeBounded(t *testing.T) {
	const pause = 30 * time.Second // the shell-local `sleep 30`
	src := mustRead(t, "templates", osName, osName+".pkr.hcl")
	seen := map[string]bool{}
	for _, b := range buildProvisioners(t) {
		a, ok := b.Body.Attributes["script"]
		if !ok {
			continue
		}
		expr := string(a.Expr.Range().SliceBytes(src))
		for _, name := range []string{"install.sh", "verify.sh"} {
			if !strings.Contains(expr, name) {
				continue
			}
			seen[name] = true
			timeout := attrDur(t, b, "timeout")
			if timeout <= 0 {
				t.Errorf("the %s provisioner has no timeout", name)
			}
			need := attrDur(t, b, "start_retry_timeout")
			if name == "verify.sh" {
				need += pause
			}
			if timeout <= need {
				t.Errorf("the %s provisioner's timeout %v does not exceed its pause plus start_retry_timeout, %v", name, timeout, need)
			}
		}
	}
	if !seen["install.sh"] || !seen["verify.sh"] {
		t.Fatalf("found script provisioners %v, want install.sh and verify.sh", seen)
	}
}

func mustRead(t *testing.T, elem ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{templatetest.RepoRoot(t)}, elem...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
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

// TestEveryShutdownGoesThroughTheGuestsHelper: a bare `shutdown` over SSH can
// hold launchd's userspace shutdown for a job's 20 s exit timeout, and the
// shutdown monitor then files a .shutdownStall report into the image. Two
// causes, both MEASURED (docs/decisions/0019): loginwindow's `ManagedClient
// -s` holds a DiskArbitration session that only loginwindow would close (8 of
// 8 restarts stalled without stopping it, 2026-10-13), and UserEventAgent
// cannot exit while it has a preference to write once cfprefsd is gone. The
// box's /usr/local/sbin/guest-macosx-shutdown (assets/guest/firstboot.sh) handles both,
// so every shutdown and restart the template issues is that helper, and none
// is a bare `shutdown`, `reboot` or `halt`.
func TestEveryShutdownGoesThroughTheGuestsHelper(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, osName+".pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	var code []string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	src := strings.Join(code, "\n")
	if bare := regexp.MustCompile(`shutdown -[hr] now|killall|\breboot\b|\bhalt\b`).FindString(src); bare != "" {
		t.Errorf("the template issues %q itself; want only sudo /usr/local/sbin/guest-macosx-shutdown", bare)
	}
	all := regexp.MustCompile(`"[^"\n]*shutdown[^"\n]*"`).FindAllString(src, -1)
	if len(all) == 0 {
		t.Fatal("no shutdown in the template")
	}
	for _, s := range all {
		if s != `"sudo /usr/local/sbin/guest-macosx-shutdown -h"` && s != `"sudo /usr/local/sbin/guest-macosx-shutdown -r"` {
			t.Errorf("a shutdown is %s; want \"sudo /usr/local/sbin/guest-macosx-shutdown -h\" or -r", s)
		}
	}
}

// TestTheBuildGivesQEMUAUUIDOfItsOwn: every build's guest gets a host UUID
// of its own, there from its first second (docs/decisions/0024).
func TestTheBuildGivesQEMUAUUIDOfItsOwn(t *testing.T) {
	templatetest.CheckBuildUUID(t, osName, "mavericks-aquatransport.pkr.hcl")
}

// TestTheBuildGivesItsNICAnAddressOfItsOwn: the build's guest's NIC
// address, derived from its UUID by the one rule (docs/decisions/0024).
func TestTheBuildGivesItsNICAnAddressOfItsOwn(t *testing.T) {
	templatetest.CheckBuildMAC(t, osName, "mavericks-aquatransport.pkr.hcl")
}

// TestTheBuildsGuestReachesNothingOffLink: the layer's build is restricted
// like the base templates' (docs/decisions/0034): nothing it installs is
// fetched by the guest, and verify.sh's checks read the guest's own state.
// That the finished box's /usr/bin/curl reaches the network is shown where
// the box boots unrestricted: bin/gate.sh's reach step and mavericks-vm's
// self-test.
func TestTheBuildsGuestReachesNothingOffLink(t *testing.T) {
	templatetest.CheckGuestNetwork(t, osName, "mavericks-aquatransport.pkr.hcl")
}

// TestTheBuildsMachineIsTheOneShape: the layer's build boots the base box
// with the devices every build and box has (templatetest.MachineShapeErrors,
// docs/decisions/0035).
func TestTheBuildsMachineIsTheOneShape(t *testing.T) {
	templatetest.CheckBuildMachine(t, osName, "mavericks-aquatransport.pkr.hcl")
}
