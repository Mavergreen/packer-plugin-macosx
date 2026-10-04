// This file checks the box itself, statically: the "vagrant"
// post-processor block in mavericks.pkr.hcl (its include list and
// vagrantfile_template, against the layout the box's Vagrantfile expects), the provisioners
// that make the box's Vagrantfile a file, and box.Vagrantfile.pkrtpl,
// both as source and as rendered with the build's variables. Nothing
// here runs `packer build` -- that a box built this way actually boots
// is measured with a real build. This is deliberately narrower than
// template_test.go's acceptance test: a static check of the HCL and the
// Vagrantfile text, not an assertion that they behave -- which is why it
// lives in its own file instead of the "no isolated HCL assertions"
// package doc atop template_test.go.
package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// findBlock returns the first child block of body with the given type
// (and, if labels is non-empty, matching labels), or nil.
func findBlock(body *hclsyntax.Body, typeName string, labels ...string) *hclsyntax.Block {
	for _, b := range body.Blocks {
		if b.Type != typeName {
			continue
		}
		if len(labels) > 0 && !equalLabels(b.Labels, labels) {
			continue
		}
		return b
	}
	return nil
}

func equalLabels(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// traversalString renders an absolute traversal as the dotted path it
// reads in source, e.g. "data.macosx-mavericks-firmware.fw.ovmf_code".
func traversalString(t hcl.Traversal) string {
	var parts []string
	for _, step := range t {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		}
	}
	return strings.Join(parts, ".")
}

func TestVagrantPostProcessorBlock(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "templates", "mavericks", "mavericks.pkr.hcl")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	fileBody := f.Body.(*hclsyntax.Body)

	build := findBlock(fileBody, "build")
	if build == nil {
		t.Fatal("no build block")
	}
	buildBody := build.Body

	pp := findBlock(buildBody, "post-processor", "vagrant")
	if pp == nil {
		t.Fatal(`no post-processor "vagrant" block`)
	}

	attrs, diags := pp.Body.JustAttributes()
	if diags.HasErrors() {
		t.Fatalf("post-processor \"vagrant\" attributes: %v", diags)
	}

	// The box's Vagrantfile is the one the provisioners rendered into
	// the build's output directory: a file that exists only once the
	// build has run, so the post-processor must not stat it at validate
	// time.
	vagrantfileAttr, ok := attrs["vagrantfile_template"]
	if !ok {
		t.Fatal("post-processor \"vagrant\" has no vagrantfile_template")
	}
	pathRoot := &hcl.EvalContext{Variables: map[string]cty.Value{
		"path": cty.ObjectVal(map[string]cty.Value{"root": cty.StringVal("ROOT")}),
		"local": cty.ObjectVal(map[string]cty.Value{
			"output_directory":      cty.StringVal("OUT"),
			"box_vagrantfile":       cty.StringVal("RENDERED"),
			"box_vagrantfile_guest": cty.StringVal("GUEST"),
		}),
	}}
	vagrantfileVal, diags := vagrantfileAttr.Expr.Value(pathRoot)
	if diags.HasErrors() || vagrantfileVal.AsString() != "OUT/Vagrantfile" {
		t.Errorf("vagrantfile_template = %v (diags %v), want \"${local.output_directory}/Vagrantfile\"", vagrantfileVal, diags)
	}
	if a, ok := attrs["vagrantfile_template_generated"]; !ok {
		t.Error("post-processor \"vagrant\" lacks vagrantfile_template_generated")
	} else if v, diags := a.Expr.Value(nil); diags.HasErrors() || !v.True() {
		t.Errorf("vagrantfile_template_generated = %v (diags %v), want true", v, diags)
	}

	// The qemu builder writes into that same output directory.
	qemuSrc := findBlock(fileBody, "source", "qemu", "mavericks")
	if qemuSrc == nil {
		t.Fatal(`no source "qemu" "mavericks" block`)
	}
	srcAttrs, diags := qemuSrc.Body.JustAttributes()
	if diags.HasErrors() {
		t.Fatalf("source attributes: %v", diags)
	}
	if v, diags := srcAttrs["output_directory"].Expr.Value(pathRoot); diags.HasErrors() || v.AsString() != "OUT" {
		t.Errorf("source output_directory = %v (diags %v), want local.output_directory", v, diags)
	}

	// The provisioners that make it a file: the rendered text up to the
	// guest, back down into the output directory under the name the
	// post-processor reads, and the guest's copy removed.
	var files []map[string]string
	var removed bool
	for _, b := range buildBody.Blocks {
		if b.Type != "provisioner" {
			continue
		}
		pa, diags := b.Body.JustAttributes()
		if diags.HasErrors() {
			t.Fatalf("provisioner %v attributes: %v", b.Labels, diags)
		}
		vals := map[string]string{}
		for name, a := range pa {
			v, diags := a.Expr.Value(pathRoot)
			if diags.HasErrors() || !v.Type().Equals(cty.String) {
				continue
			}
			vals[name] = v.AsString()
		}
		switch {
		case equalLabels(b.Labels, []string{"file"}):
			files = append(files, vals)
		case equalLabels(b.Labels, []string{"shell"}):
			if a, ok := pa["inline"]; ok {
				v, diags := a.Expr.Value(pathRoot)
				if !diags.HasErrors() && v.LengthInt() == 1 && v.Index(cty.NumberIntVal(0)).AsString() == "rm -f GUEST" {
					removed = len(files) == 2
				}
			}
		}
	}
	wantFiles := []map[string]string{
		{"content": "RENDERED", "destination": "GUEST"},
		{"direction": "download", "source": "GUEST", "destination": "OUT/Vagrantfile"},
	}
	if len(files) != len(wantFiles) {
		t.Fatalf("file provisioners = %v, want %v", files, wantFiles)
	}
	for i := range wantFiles {
		if len(files[i]) != len(wantFiles[i]) {
			t.Errorf("file provisioner %d = %v, want %v", i, files[i], wantFiles[i])
		}
		for k, v := range wantFiles[i] {
			if files[i][k] != v {
				t.Errorf("file provisioner %d: %s = %q, want %q", i, k, files[i][k], v)
			}
		}
	}
	if !removed {
		t.Error("no shell provisioner removes the guest's copy after the download")
	}

	// The shell provisioners' scripts are named the same way.
	var scripts []string
	for _, b := range buildBody.Blocks {
		if b.Type != "provisioner" || !equalLabels(b.Labels, []string{"shell"}) {
			continue
		}
		pa, diags := b.Body.JustAttributes()
		if diags.HasErrors() {
			t.Fatalf("provisioner \"shell\" attributes: %v", diags)
		}
		sa, ok := pa["script"]
		if !ok {
			continue
		}
		v, diags := sa.Expr.Value(pathRoot)
		if diags.HasErrors() {
			t.Fatalf("provisioner script: %v", diags)
		}
		scripts = append(scripts, v.AsString())
	}
	if want := []string{"ROOT/firstboot-wait.sh", "ROOT/verify.sh"}; !equalLabels(scripts, want) {
		t.Errorf("provisioner scripts = %v, want %v (each through ${path.root})", scripts, want)
	}

	outputAttr, ok := attrs["output"]
	if !ok {
		t.Fatal("post-processor \"vagrant\" has no output")
	}
	outputVal, diags := outputAttr.Expr.Value(nil)
	if diags.HasErrors() || outputVal.AsString() != "output/mavericks-10.9.5-{{ .Provider }}.box" {
		t.Errorf("output = %v (diags %v), want \"output/mavericks-10.9.5-{{ .Provider }}.box\"", outputVal, diags)
	}

	includeAttr, ok := attrs["include"]
	if !ok {
		t.Fatal("post-processor \"vagrant\" has no include")
	}
	elems, diags := hcl.ExprList(includeAttr.Expr)
	if diags.HasErrors() {
		t.Fatalf("include list: %v", diags)
	}
	var got []string
	for _, e := range elems {
		trav, diags := hcl.AbsTraversalForExpr(e)
		if diags.HasErrors() {
			t.Fatalf("include element traversal: %v", diags)
		}
		got = append(got, traversalString(trav))
	}
	want := []string{
		"data.macosx-mavericks-firmware.fw.ovmf_code",
		"data.macosx-mavericks-firmware.fw.ovmf_vars",
		"data.macosx-mavericks-firmware.fw.opencore_image",
	}
	if len(got) != len(want) {
		t.Fatalf("include = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("include[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// templateVars is variables.pkr.hcl's defaults, converted to each
// variable's type, with overrides applied the way -var would.
func templateVars(t *testing.T, overrides map[string]cty.Value) map[string]cty.Value {
	t.Helper()
	path := filepath.Join(repoRoot(t), "templates", "mavericks", "variables.pkr.hcl")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	types := map[string]cty.Type{
		"string": cty.String,
		"number": cty.Number,
		"bool":   cty.Bool,
	}
	vars := map[string]cty.Value{}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "variable" {
			continue
		}
		name := b.Labels[0]
		typ := b.Body.Attributes["type"]
		if typ == nil {
			t.Fatalf("variable %q has no type", name)
		}
		ty, ok := types[hcl.ExprAsKeyword(typ.Expr)]
		if !ok {
			t.Fatalf("variable %q: unexpected type %s", name, hcl.ExprAsKeyword(typ.Expr))
		}
		v, ok := overrides[name]
		if !ok {
			def := b.Body.Attributes["default"]
			if def == nil {
				t.Fatalf("variable %q has no default", name)
			}
			var diags hcl.Diagnostics
			if v, diags = def.Expr.Value(nil); diags.HasErrors() {
				t.Fatalf("variable %q default: %v", name, diags)
			}
		}
		cv, err := convert.Convert(v, ty)
		if err != nil {
			t.Fatalf("variable %q = %#v: %v", name, v, err)
		}
		vars[name] = cv
	}
	for name := range overrides {
		if _, ok := vars[name]; !ok {
			t.Fatalf("override for undeclared variable %q", name)
		}
	}
	return vars
}

// renderBoxVagrantfile evaluates mavericks.pkr.hcl's own
// local.box_vagrantfile expression -- its templatefile() call and the
// variables it passes -- with variables.pkr.hcl's defaults plus
// overrides. templatefile() here is Packer's: the file named relative
// to the template's directory, rendered as an HCL template with the
// given object as its variables. (It offers no functions: the template
// calls none, and a call would fail here first.)
func renderBoxVagrantfile(t *testing.T, overrides map[string]cty.Value) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "templates", "mavericks")
	path := filepath.Join(dir, "mavericks.pkr.hcl")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	locals := findBlock(f.Body.(*hclsyntax.Body), "locals")
	if locals == nil {
		t.Fatal("no locals block")
	}
	attr, ok := locals.Body.Attributes["box_vagrantfile"]
	if !ok {
		t.Fatal("no local.box_vagrantfile")
	}

	templatefile := function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "path", Type: cty.String},
			{Name: "vars", Type: cty.DynamicPseudoType},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			name := args[0].AsString()
			if !filepath.IsAbs(name) {
				name = filepath.Join(dir, name)
			}
			b, err := os.ReadFile(name)
			if err != nil {
				return cty.NilVal, err
			}
			expr, diags := hclsyntax.ParseTemplate(b, name, hcl.InitialPos)
			if diags.HasErrors() {
				return cty.NilVal, diags
			}
			v, diags := expr.Value(&hcl.EvalContext{Variables: args[1].AsValueMap()})
			if diags.HasErrors() {
				return cty.NilVal, diags
			}
			return v, nil
		},
	})
	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var":  cty.ObjectVal(templateVars(t, overrides)),
			"path": cty.ObjectVal(map[string]cty.Value{"root": cty.StringVal(dir)}),
		},
		Functions: map[string]function.Function{"templatefile": templatefile},
	}
	v, diags := attr.Expr.Value(ctx)
	if diags.HasErrors() {
		t.Fatalf("local.box_vagrantfile: %v", diags)
	}
	return v.AsString()
}

// codeLines is a Vagrantfile's lines less its comments and blank lines.
func codeLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if tl := strings.TrimSpace(l); tl == "" || strings.HasPrefix(tl, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// measuredBoxVagrantfile is the code of the box's Vagrantfile that the
// measured builds of 2026-09-27 (docs/test-hosts.md) booted, logged in
// to and halted, less its comments.
// Rendered with the template's defaults, the box's Vagrantfile must be
// this code exactly.
const measuredBoxVagrantfile = `Vagrant.configure("2") do |config|
  boxdir = File.expand_path(__dir__)
  config.vm.guest = :darwin
  config.ssh.username = "vagrant"
  config.vm.synced_folder ".", "/vagrant", disabled: true
  config.trigger.before :halt do |t|
    t.name = "guest shutdown"
    t.on_error = :continue
    t.run_remote = { inline: "sudo /sbin/shutdown -h now" }
  end
  config.vm.provider "qemu" do |qe|
    qe.arch = "x86_64"
    qe.machine = "q35,vmport=off,accel=kvm"
    qe.cpu = "Penryn,+ssse3,+sse4.1,+sse4.2"
    qe.smp = "2"
    qe.memory = "4G"
    qe.net_device = "e1000-82545em"
    qe.drive_interface = "ide"
    display = ENV["MAVERICKS_DISPLAY"]
    qe.no_daemonize = true if display
    qe.other_default = %W(-parallel none)
    qe.extra_qemu_args = %W(
      -drive if=pflash,format=raw,unit=0,readonly=on,file=#{boxdir}/OVMF_CODE.fd
      -drive if=pflash,format=raw,unit=1,snapshot=on,file=#{boxdir}/OVMF_VARS.fd
      -device ich9-usb-ehci1,id=usb,bus=pcie.0,addr=0x1d.7,multifunction=on
      -device ich9-usb-uhci1,masterbus=usb.0,firstport=0,bus=pcie.0,addr=0x1d.0,multifunction=on
      -device ich9-usb-uhci2,masterbus=usb.0,firstport=2,bus=pcie.0,addr=0x1d.1
      -device ich9-usb-uhci3,masterbus=usb.0,firstport=4,bus=pcie.0,addr=0x1d.2
      -drive id=opencore,if=none,format=raw,snapshot=on,file=#{boxdir}/opencore.img
      -device usb-storage,bus=usb.0,drive=opencore
      -device usb-kbd,bus=usb.0
      -device usb-mouse,bus=usb.0
      -device VGA,vgamem_mb=64
    ) + (display ? %W(-display #{display}) : %W(-display none))
  end
end`

// measuredHaltTrigger is the halt trigger, comment and all, as measured
// (docs/test-hosts.md, 2026-09-27): every rendering carries it byte for byte.
const measuredHaltTrigger = `  # A clean ` + "`vagrant halt`" + `. vagrant-qemu's halt presses the ACPI power
  # button, which 10.9 answers with a dialog rather than a shutdown, and
  # after 60s it quits QEMU under the running guest (MEASURED 2026-09-27:
  # no SHUTDOWN_TIME in the guest's system.log). It never asks the guest
  # itself, so this does: passwordless sudo lets the shutdown run, and
  # QEMU exits when the guest powers off.
  config.trigger.before :halt do |t|
    t.name = "guest shutdown"
    t.on_error = :continue
    t.run_remote = { inline: "sudo /sbin/shutdown -h now" }
  end
`

// usbControllers are the EHCI controller and its UHCI companions, on
// extra_qemu_args.
var usbControllers = []string{
	"-device ich9-usb-ehci1,id=usb,bus=pcie.0,addr=0x1d.7,multifunction=on",
	"-device ich9-usb-uhci1,masterbus=usb.0,firstport=0,bus=pcie.0,addr=0x1d.0,multifunction=on",
	"-device ich9-usb-uhci2,masterbus=usb.0,firstport=2,bus=pcie.0,addr=0x1d.1",
	"-device ich9-usb-uhci3,masterbus=usb.0,firstport=4,bus=pcie.0,addr=0x1d.2",
}

// rubyArray is the lines of the Ruby %W( ... ) array that begins on the
// line starting with prefix, trimmed.
func rubyArray(t *testing.T, text, prefix string) []string {
	t.Helper()
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), prefix) {
			continue
		}
		var out []string
		for _, m := range lines[i+1:] {
			if strings.HasPrefix(strings.TrimSpace(m), ")") {
				return out
			}
			out = append(out, strings.TrimSpace(m))
		}
	}
	t.Fatalf("no %q array in:\n%s", prefix, text)
	return nil
}

// TestBoxVagrantfileSource checks box.Vagrantfile.pkrtpl as written:
// it finds its firmware files relative to itself (OVMF_CODE.fd,
// OVMF_VARS.fd and opencore.img, beside
// box.img/box_N.img, which `include` and the vagrant post-processor's
// libvirt provider place there), and takes the build's settings from
// templatefile()'s variables.
func TestBoxVagrantfileSource(t *testing.T) {
	path := filepath.Join(repoRoot(t), "templates", "mavericks", "box.Vagrantfile.pkrtpl")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	// The rendered file passes through Packer's legacy Go template
	// engine (the file provisioner's content, the post-processor's
	// interpolate.Render), so a double open brace would be interpolated
	// -- or fail the build.
	if strings.Contains(text, "{"+"{") {
		t.Error("box.Vagrantfile.pkrtpl contains a double open brace, which Packer would interpolate")
	}
	if !strings.Contains(text, "File.expand_path(__dir__)") {
		t.Error(`box.Vagrantfile.pkrtpl doesn't locate itself with File.expand_path(__dir__)`)
	}
	for _, want := range []string{
		"boxdir}/OVMF_CODE.fd",
		"boxdir}/OVMF_VARS.fd",
		"boxdir}/opencore.img",
		`config.vm.guest = :darwin`,
		`config.ssh.username = "${user}"`,
		`qe.cpu = "${cpu}"`,
		`qe.smp = "${cpus}"`,
		`accel=${accelerator}`,
		`${memory / 1024}G`,
		`ENV["MAVERICKS_DISPLAY"]`,
		measuredHaltTrigger,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("box.Vagrantfile.pkrtpl lacks %q", want)
		}
	}
	// Nothing in its code is fixed at a default.
	code := strings.Join(codeLines(text), "\n")
	for _, fixed := range []string{`"vagrant"`, `accel=kvm`, `"4G"`, `"e1000-82545em"`, `Penryn`} {
		if strings.Contains(code, fixed) {
			t.Errorf("box.Vagrantfile.pkrtpl still hard-codes %s", fixed)
		}
	}
}

// TestBoxVagrantfileRendersTheMeasuredDefaults: with the template's
// default variables, the rendered Vagrantfile is the measured one --
// the same code line for line (so the same qemu arguments and the same
// user), and the halt trigger byte for byte.
func TestBoxVagrantfileRendersTheMeasuredDefaults(t *testing.T) {
	text := renderBoxVagrantfile(t, nil)
	got, want := codeLines(text), strings.Split(measuredBoxVagrantfile, "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the default box Vagrantfile's code is not the measured code.\ngot:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), measuredBoxVagrantfile)
	}
	if !strings.Contains(text, measuredHaltTrigger) {
		t.Error("the default box Vagrantfile's halt trigger is not the measured one, byte for byte")
	}
	if strings.Contains(text, "{"+"{") {
		t.Error("the rendered box Vagrantfile contains a double open brace")
	}
}

// TestBoxVagrantfileCarriesTheBuildsSettings: rendered with a build's
// own settings, the box's Vagrantfile logs in as that user and boots
// with that CPU, memory, SMP, NIC and accelerator.
func TestBoxVagrantfileCarriesTheBuildsSettings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides map[string]cty.Value
		want      []string
	}{
		{
			name: "a custom user, e1000 and tcg",
			overrides: map[string]cty.Value{
				"user": cty.StringVal("alice"), "nic": cty.StringVal("e1000-82545em"),
				"cpu": cty.StringVal("Haswell-noTSX,vendor=GenuineIntel"), "memory": cty.NumberIntVal(3000),
				"cpus": cty.NumberIntVal(4), "accelerator": cty.StringVal("tcg"),
			},
			want: []string{
				`  config.ssh.username = "alice"`,
				`    qe.machine = "q35,vmport=off,accel=tcg"`,
				`    qe.cpu = "Haswell-noTSX,vendor=GenuineIntel"`,
				`    qe.smp = "4"`,
				`    qe.memory = "3000M"`,
				`    qe.net_device = "e1000-82545em"`,
			},
		},
		{
			name: "virtio-net-pci, 8G and no accelerator",
			overrides: map[string]cty.Value{
				"user": cty.StringVal("bob_2"), "nic": cty.StringVal("virtio-net-pci"),
				"memory": cty.NumberIntVal(8192), "accelerator": cty.StringVal("none"),
			},
			want: []string{
				`  config.ssh.username = "bob_2"`,
				`    qe.machine = "q35,vmport=off"`,
				`    qe.smp = "2"`,
				`    qe.memory = "8G"`,
				`    qe.net_device = "virtio-net-pci"`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := renderBoxVagrantfile(t, tc.overrides)
			lines := codeLines(text)
			for _, w := range tc.want {
				found := false
				for _, l := range lines {
					if l == w {
						found = true
					}
				}
				if !found {
					t.Errorf("rendered box Vagrantfile lacks the line %q:\n%s", w, text)
				}
			}
			if !strings.Contains(text, measuredHaltTrigger) {
				t.Error("the halt trigger is not the measured one, byte for byte")
			}
			if strings.Contains(text, "{"+"{") {
				t.Error("the rendered box Vagrantfile contains a double open brace")
			}
			if strings.Contains(text, "qe.qemu_bin") {
				t.Error("the rendered box Vagrantfile sets qe.qemu_bin, which no accepted NIC needs")
			}

			extra := rubyArray(t, text, "qe.extra_qemu_args = %W(")
			// Each controller exactly once, on extra_qemu_args.
			for _, c := range usbControllers {
				if n := strings.Count(strings.Join(extra, "\n"), c); n != 1 {
					t.Errorf("%s: %d on extra_qemu_args, want 1", c, n)
				}
			}
			// The rest of the devices, as measured.
			for _, d := range []string{"-device usb-storage,bus=usb.0,drive=opencore", "-device usb-kbd,bus=usb.0", "-device usb-mouse,bus=usb.0", "-device VGA,vgamem_mb=64"} {
				if strings.Count(strings.Join(extra, "\n"), d) != 1 {
					t.Errorf("extra_qemu_args lacks %q: %q", d, extra)
				}
			}
		})
	}
}

// TestBoxVagrantfileIsRuby: every rendering parses as Ruby, when a ruby
// is on PATH to ask (Vagrant's own embedded one works: set PATH to
// include /opt/vagrant/embedded/bin).
func TestBoxVagrantfileIsRuby(t *testing.T) {
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH")
	}
	for name, o := range map[string]map[string]cty.Value{
		"defaults": nil,
		"virtio":   {"nic": cty.StringVal("virtio-net-pci"), "user": cty.StringVal("alice")},
		"none":     {"accelerator": cty.StringVal("none"), "memory": cty.NumberIntVal(1536)},
	} {
		f := filepath.Join(t.TempDir(), "Vagrantfile")
		if err := os.WriteFile(f, []byte(renderBoxVagrantfile(t, o)), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(ruby, "-c", f).CombinedOutput(); err != nil {
			t.Errorf("%s: ruby -c: %v\n%s", name, err, out)
		}
	}
}
