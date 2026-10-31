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

	"github.com/Mavergreen/packer-plugin-macosx/internal/templatetest"
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

// renderBoxVagrantfile is this template's box Vagrantfile, rendered as
// mavericks.pkr.hcl renders it, with variables.pkr.hcl's defaults plus
// overrides (templatetest.RenderBoxVagrantfile).
func renderBoxVagrantfile(t *testing.T, overrides map[string]cty.Value) string {
	t.Helper()
	return templatetest.RenderBoxVagrantfile(t, osName, overrides)
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
// to and halted, less its comments -- with the cpu line's
// vendor=GenuineIntel, which the 2026-10-06 builds on GitHub's Intel and
// AMD runners carried (TestTheDefaultCPUSaysGenuineIntel), the
// GUEST_MACOSX_CPU_ISA switch, whose unset arm is that same line
// (TestBoxVagrantfileOffersTheThreeLevels), and the GUEST_MACOSX_GUI and
// GUEST_MACOSX_QEMU_DISPLAY switches and the VirtualBox and VMware blocks,
// whose unset values leave the measured headless run as it was:
// daemonized, -display none (TestBoxVagrantfileReadsTheGuestEnvironment);
// and the per-machine host UUID and MAC triggers, run by Vagrant 2.4.9 and
// vagrant-qemu 0.6.3 (docs/decisions/0024, TestEachVagrantMachineHasItsOwnHostUUID).
// Rendered with the template's defaults, the box's Vagrantfile must be
// this code exactly.
const measuredBoxVagrantfile = `Vagrant.configure("2") do |config|
  boxdir = File.expand_path(__dir__)
  bad = nil
  gui_env = ENV["GUEST_MACOSX_GUI"].to_s
  gui = case gui_env.downcase
    when "1", "true", "yes" then true
    when "", "0", "false", "no" then false
    else
      bad = "GUEST_MACOSX_GUI: want 1, true or yes for a window, or 0, false, no or nothing for none (got '#{gui_env}')"
      false
    end
  qemu_display = ENV["GUEST_MACOSX_QEMU_DISPLAY"].to_s
  unless qemu_display.match?(/\A([A-Za-z][A-Za-z0-9_.,:=+-]*)?\z/)
    bad ||= "GUEST_MACOSX_QEMU_DISPLAY: want a QEMU -display value, a backend then comma-separated options, such as sdl or vnc=127.0.0.1:1 (got '#{qemu_display}')"
  end
  qemu_display = "" if qemu_display == "none"
  qemu_display = Vagrant::Util::Platform.darwin? ? "cocoa" : "gtk" if gui && qemu_display.empty?
  config.vm.guest = :darwin
  config.ssh.username = "vagrant"
  config.vm.synced_folder ".", "/vagrant", disabled: true
  config.trigger.before :halt do |t|
    t.name = "guest shutdown"
    t.on_error = :continue
    t.run_remote = { inline: "sudo /usr/local/sbin/guest-macosx-shutdown -h" }
  end
  config.trigger.before :up, :reload do |t|
    t.name = "guest-macosx host UUID"
    t.ruby do |_env, machine|
      next unless machine.provider_name.to_s == "qemu"
      qc = machine.provider_config
      args = qc.extra_qemu_args
      if (own = args.index("-uuid"))
        uuid = args[own + 1].to_s
        said = "the project's own -uuid"
      else
        store = machine.data_dir.join("guest-macosx-host-uuid")
        uuid = File.exist?(store) ? File.read(store).strip : ""
        if uuid.match?(/\A[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\z/)
          said = "kept in #{store}"
        else
          said = uuid.empty? ? "new" : "new (the stored value was not a UUID)"
          require "securerandom"
          uuid = SecureRandom.uuid
          File.write(store, uuid + "\n")
          said += ", kept in #{store}"
        end
        args.push("-uuid", uuid)
      end
      if qc.advanced_network && machine.config.vm.networks.any? { |type, _| type == :private_network }
        nic = "vagrant-qemu's own dual-NIC addresses"
      elsif qc.net_device.to_s.include?("mac=")
        nic = "the project's own address"
      else
        require "digest"
        h = Digest::SHA256.hexdigest(uuid)
        i = 0
        i += 6 while h[i, 4] == "1234"
        mac = "52:54:00:#{h[i, 2]}:#{h[i + 2, 2]}:#{h[i + 4, 2]}"
        qc.net_device = "#{qc.net_device},mac=#{mac}"
        nic = "#{mac}, derived from the UUID"
      end
      machine.ui.detail("host UUID #{uuid} (#{said}); first NIC: #{nic}")
    end
  end
  config.vm.provider "qemu" do |qe|
    raise bad if bad
    qe.arch = "x86_64"
    qe.machine = "q35,vmport=off,accel=kvm"
    isa = ENV["GUEST_MACOSX_CPU_ISA"].to_s
    qe.cpu = case isa
      when "" then "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"
      when "none" then "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"
      when "avx" then "SandyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,enforce"
      when "avx2" then "IvyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,+avx2,+fma,+bmi1,+bmi2,+movbe,+abm,enforce"
      else raise "GUEST_MACOSX_CPU_ISA: want none, avx or avx2 (got '#{isa}')"
      end
    msrs = "/sys/module/kvm/parameters/ignore_msrs"
    if "kvm" == "kvm" && %w(avx avx2).include?(isa) && File.readable?(msrs) && File.read(msrs).strip != "Y"
      $stderr.puts "GUEST_MACOSX_CPU_ISA=#{isa} needs kvm.ignore_msrs=Y on this host, or 10.9 resets in a loop: echo Y | sudo tee #{msrs}"
    end
    qe.smp = "2"
    qe.memory = "4G"
    qe.net_device = "e1000-82545em"
    qe.drive_interface = "ide"
    qe.no_daemonize = true unless qemu_display.empty?
    qe.other_default = %W(-parallel none)
    qe.extra_qemu_args = %W(
      -drive if=pflash,format=raw,unit=0,readonly=on,file=#{boxdir}/OVMF_CODE.fd
      -drive if=pflash,format=raw,unit=1,snapshot=on,file=#{boxdir}/OVMF_VARS.fd
      -drive id=opencore,if=none,format=raw,snapshot=on,file=#{boxdir}/opencore.img
      -device virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10
      -device ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0
      -device usb-kbd,bus=ehci.0
      -device usb-tablet,bus=ehci.0
      -device VGA,vgamem_mb=64
    ) + %W(-display #{qemu_display.empty? ? "none" : qemu_display})
  end
  config.vm.provider "virtualbox" do |vb|
    raise bad if bad
    vb.gui = true if gui
  end
  config.vm.provider "vmware_desktop" do |v|
    raise bad if bad
    v.gui = true if gui
  end
end`

// measuredHaltTrigger is the halt trigger, comment and all, as measured
// (docs/test-hosts.md, 2026-09-27), with the guest command the image's own
// guest-macosx-shutdown since 2026-10-14 (docs/decisions/0019: a bare `shutdown` can
// stall 20 s and leave a .shutdownStall report in the consumer's guest):
// every rendering carries it byte for byte.
const measuredHaltTrigger = `  # A clean ` + "`vagrant halt`" + `. vagrant-qemu's halt presses the ACPI power
  # button, which 10.9 answers with a dialog rather than a shutdown, and
  # after 60s it quits QEMU under the running guest (MEASURED 2026-09-27:
  # no SHUTDOWN_TIME in the guest's system.log). It never asks the guest
  # itself, so this does: passwordless sudo lets the shutdown run, and
  # QEMU exits when the guest powers off. The image's own guest-macosx-shutdown,
  # never a bare ` + "`shutdown`" + `, or the guest can wait out a 20 s exit
  # timeout on the way down and keep a .shutdownStall report for it
  # (docs/decisions/0019).
  config.trigger.before :halt do |t|
    t.name = "guest shutdown"
    t.on_error = :continue
    t.run_remote = { inline: "sudo /usr/local/sbin/guest-macosx-shutdown -h" }
  end
`

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
		`when "" then "${cpu}"`,
		`ENV["GUEST_MACOSX_CPU_ISA"]`,
		`qe.smp = "${cpus}"`,
		`accel=${accelerator}`,
		`${memory / 1024}G`,
		`ENV["GUEST_MACOSX_GUI"]`,
		`ENV["GUEST_MACOSX_QEMU_DISPLAY"]`,
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
				`      when "" then "Haswell-noTSX,vendor=GenuineIntel"`,
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
			// The devices, as measured, each once (the whole shape:
			// TestTheBoxsMachineIsTheOneShape).
			for _, d := range []string{"-device virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10", "-device ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0", "-device usb-kbd,bus=ehci.0", "-device usb-tablet,bus=ehci.0", "-device VGA,vgamem_mb=64"} {
				if strings.Count(strings.Join(extra, "\n"), d) != 1 {
					t.Errorf("extra_qemu_args lacks %q: %q", d, extra)
				}
			}
		})
	}
}

// theLevels are the instruction-set levels, each a measured -cpu line
// (docs/decisions/0009, "The instruction-set levels").
var theLevels = []struct{ name, line string }{
	{"none", "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"},
	{"avx", "SandyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,enforce"},
	{"avx2", "IvyBridge,vendor=GenuineIntel,-x2apic,-tsc-deadline,+avx2,+fma,+bmi1,+bmi2,+movbe,+abm,enforce"},
}

// TestBoxVagrantfileOffersTheThreeLevels: whatever the build's own cpu,
// GUEST_MACOSX_CPU_ISA offers none, avx and avx2 as their measured lines,
// and unset it keeps the build's cpu.
func TestBoxVagrantfileOffersTheThreeLevels(t *testing.T) {
	for name, o := range map[string]map[string]cty.Value{
		"defaults": nil,
		"conroe":   {"cpu": cty.StringVal("Conroe,vendor=GenuineIntel")},
	} {
		t.Run(name, func(t *testing.T) {
			code := codeLines(renderBoxVagrantfile(t, o))
			has := func(want string) {
				t.Helper()
				for _, l := range code {
					if l == want {
						return
					}
				}
				t.Errorf("rendered box Vagrantfile lacks the line %q", want)
			}
			for _, lv := range theLevels {
				has(`      when "` + lv.name + `" then "` + lv.line + `"`)
			}
			build := "Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2"
			if o != nil {
				build = o["cpu"].AsString()
			}
			has(`      when "" then "` + build + `"`)
		})
	}
}

// TestBoxVagrantfileIsRuby: every rendering parses as Ruby, when there
// is a ruby to ask (templatetest.Ruby: PATH's, or Vagrant's own).
func TestBoxVagrantfileIsRuby(t *testing.T) {
	ruby := templatetest.Ruby(t)
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

// TestBoxVagrantfileReadsTheGuestEnvironment: GUEST_MACOSX_GUI and
// GUEST_MACOSX_QEMU_DISPLAY, as every box reads them
// (templatetest.CheckGuestEnvironment).
func TestBoxVagrantfileReadsTheGuestEnvironment(t *testing.T) {
	templatetest.CheckGuestEnvironment(t, osName, nil)
}

// TestBoxVagrantfilePicksALevel: GUEST_MACOSX_CPU_ISA, run. Unset or
// empty it keeps the build's own cpu line; none, avx and avx2 are their
// measured lines; anything else is refused, naming the three.
func TestBoxVagrantfilePicksALevel(t *testing.T) {
	text := renderBoxVagrantfile(t, nil)
	cpu := func(env map[string]string) (string, templatetest.BoxRun) {
		r := templatetest.RunBoxVagrantfile(t, text, "linux", env)
		got, _ := r.Provider("qemu")["cpu"].(string)
		return got, r
	}
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{nil, theLevels[0].line},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": ""}, theLevels[0].line},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": "none"}, theLevels[0].line},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": "avx"}, theLevels[1].line},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": "avx2"}, theLevels[2].line},
	} {
		if got, r := cpu(c.env); r.Err != nil || got != c.want {
			t.Errorf("%v: cpu %q, %v %s; want %q", c.env, got, r.Err, r.Stderr, c.want)
		}
	}
	for _, bad := range []string{"AVX", "avx512", "bogus"} {
		_, r := cpu(map[string]string{"GUEST_MACOSX_CPU_ISA": bad})
		if !r.Refused(templatetest.ProviderPhase + "GUEST_MACOSX_CPU_ISA: want none, avx or avx2 (got '" + bad + "')") {
			t.Errorf("GUEST_MACOSX_CPU_ISA=%q: %v %q; want a failure naming the variable, none, avx and avx2", bad, r.Err, r.Stderr)
		}
	}
	// The variable before GUEST_MACOSX_ is gone, not kept beside it.
	if got, r := cpu(map[string]string{"MAVERICKS_CPU_ISA": "avx"}); r.Err != nil || got != theLevels[0].line {
		t.Errorf("MAVERICKS_CPU_ISA=avx: cpu %q, %v; want the build's own line", got, r.Err)
	}
}

// TestBoxVagrantfileWarnsOfIgnoreMSRs: a level that needs
// kvm.ignore_msrs=Y on a KVM host that lacks it warns, and only warns,
// run against a stand-in for the kvm parameter.
func TestBoxVagrantfileWarnsOfIgnoreMSRs(t *testing.T) {
	const sysfs = "/sys/module/kvm/parameters/ignore_msrs"
	text := renderBoxVagrantfile(t, nil)
	if !strings.Contains(text, sysfs) {
		t.Fatal("the box Vagrantfile reads no ignore_msrs")
	}
	param := filepath.Join(t.TempDir(), "ignore_msrs")
	text = strings.Replace(text, sysfs, param, 1)
	for _, c := range []struct {
		isa, param string
		warns      bool
	}{
		{"avx", "N", true},
		{"avx2", "N", true},
		{"avx", "Y", false},
		{"none", "N", false},
		{"", "N", false},
		{"avx", "", false}, // no such parameter: not a KVM host to judge
	} {
		os.Remove(param)
		if c.param != "" {
			if err := os.WriteFile(param, []byte(c.param+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		r := templatetest.RunBoxVagrantfile(t, text, "linux", map[string]string{"GUEST_MACOSX_CPU_ISA": c.isa})
		if r.Err != nil {
			t.Errorf("isa %q, ignore_msrs %q: %v\n%s", c.isa, c.param, r.Err, r.Stderr)
		}
		if got := strings.Contains(r.Stderr, "GUEST_MACOSX_CPU_ISA="+c.isa+" needs kvm.ignore_msrs=Y"); got != c.warns {
			t.Errorf("isa %q, ignore_msrs %q: warned %v, want %v: %q", c.isa, c.param, got, c.warns, r.Stderr)
		}
	}
}

// TestEachVagrantMachineHasItsOwnHostUUID: distinct machines, distinct host
// UUIDs and MACs, each kept with its machine (templatetest.CheckMachineIdentity).
func TestEachVagrantMachineHasItsOwnHostUUID(t *testing.T) {
	templatetest.CheckMachineIdentity(t, osName, nil)
}

// TestBoxVagrantfileNamesItsRelease: the box says it is 10.9, on the one line
// bin/gate-vm.sh reads to log in and shut down as 10.9 needs.
func TestBoxVagrantfileNamesItsRelease(t *testing.T) {
	templatetest.CheckBoxRelease(t, osName, nil, "10.9")
}
