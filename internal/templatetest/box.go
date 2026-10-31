package templatetest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// TemplateVars is templates/<osName>/variables.pkr.hcl's defaults,
// converted to each variable's type, with overrides applied the way -var
// would. A variable with no default must be given in overrides.
func TemplateVars(t *testing.T, osName string, overrides map[string]cty.Value) map[string]cty.Value {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, "variables.pkr.hcl")
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

// RenderBoxVagrantfile evaluates templates/<osName>/<osName>.pkr.hcl's own
// local.box_vagrantfile expression -- its templatefile() call and the
// variables it passes -- with variables.pkr.hcl's defaults plus
// overrides. templatefile() here is Packer's: the file named relative to
// the template's directory, rendered as an HCL template with the given
// object as its variables. (It offers no functions: the template calls
// none, and a call would fail here first.)
func RenderBoxVagrantfile(t *testing.T, osName string, overrides map[string]cty.Value) string {
	t.Helper()
	dir := filepath.Join(RepoRoot(t), "templates", osName)
	path := filepath.Join(dir, osName+".pkr.hcl")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	var attr *hclsyntax.Attribute
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if a, ok := b.Body.Attributes["box_vagrantfile"]; b.Type == "locals" && ok {
			attr = a
		}
	}
	if attr == nil {
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
			"var":  cty.ObjectVal(TemplateVars(t, osName, overrides)),
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

// Ruby is a ruby to run a box's Vagrantfile with: the one on PATH, or
// Vagrant's own embedded one, which is what reads a box's Vagrantfile on
// a host with Vagrant. With neither, the test is skipped.
func Ruby(t *testing.T) string {
	t.Helper()
	if ruby, err := exec.LookPath("ruby"); err == nil {
		return ruby
	}
	const embedded = "/opt/vagrant/embedded/bin/ruby"
	if _, err := os.Stat(embedded); err == nil {
		return embedded
	}
	t.Skip("no ruby on PATH, and no Vagrant-embedded ruby at " + embedded)
	return ""
}

// ProviderPhase marks, on a run's stderr, an error raised inside a
// provider block: the phase where Vagrant itself rescues an error into its
// clean "There was an error loading a Vagrantfile" message. An error raised
// in Vagrant.configure's own block, outside every provider block, is not
// rescued by Vagrant (Config::Loader#load), and reaches the user as a Ruby
// backtrace; here it reaches stderr without the marker.
const ProviderPhase = "PROVIDER-PHASE: "

// inProvider ends a ProviderPhase line with the provider whose block
// raised: " [in provider qemu]".
const inProvider = " [in provider "

// vagrantStandIn is what a Vagrantfile sees of Vagrant: Vagrant.configure,
// handing it a config that records every setting, every call and every
// block; and Vagrant::Util::Platform's darwin?, the host OS, as ARGV[1]
// says. Provider blocks are run as Vagrant runs them: not where they are
// written, but after the whole file has loaded (VMConfig#finalize!, which
// runs every provider's block, installed or not, on every command), each
// inside a rescue that reports its error with ProviderPhase. Vagrant stops
// at the first block that raises; the stand-in runs every one, and reports
// each one's error with its provider's name, so a test can see that every
// block refuses, not just the first. What was
// recorded is printed as JSON: an assignment as its value, a call as an
// object keyed by its name and arguments (`vm`, `provider qemu`, `before
// halt`). A trigger's ruby block (t.ruby) is not run where it is written:
// it is recorded (as "ruby": true), and with STANDIN_FIRE set to a
// trigger's key (`before up reload`) every ruby block of that trigger is
// called after the provider blocks, as Vagrant calls it, with (env,
// machine): a machine whose name is "default", whose provider_name is
// STANDIN_PROVIDER (qemu by default), whose data_dir is STANDIN_DATA_DIR,
// whose provider_config is the qemu block's own settings, so what the
// block changes there is what is printed (with STANDIN_ADVANCED=1 its
// advanced_network is true; STANDIN_NET_DEVICE, set, replaces the block's
// net_device first, and STANDIN_EXTRA_ARGS's words are appended to its
// extra_qemu_args, as a project Vagrantfile would), whose
// config.vm.networks holds one private network when
// STANDIN_PRIVATE_NETWORK=1, and whose ui.detail prints "UI: " and its
// message on stderr.
const vagrantStandIn = `require "json"
module Vagrant
  module Util
    class Platform
      def self.darwin?
        ARGV[1] == "darwin"
      end
    end
  end
  def self.configure(_version)
    yield $recorded
  end
end
class Recorder
  def initialize
    @h = {}
  end
  attr_accessor :key
  def get(k)
    @h[k]
  end
  def method_missing(name, *args, &blk)
    n = name.to_s
    if n.end_with?("=")
      @h[n.chomp("=")] = args[0]
    elsif n == "ruby" && blk
      @h["ruby"] = true
      $ruby_blocks << [@key, blk]
      self
    else
      k = ([n] + args.map(&:to_s)).join(" ")
      r = (@h[k] ||= Recorder.new)
      r.key = k if r.is_a?(Recorder)
      if blk && n == "provider"
        $provider_blocks << [args[0].to_s, r, blk]
      elsif blk
        blk.call(r)
      end
      r
    end
  end
  def respond_to_missing?(*)
    true
  end
  def to_h
    @h.transform_values { |v| v.is_a?(Recorder) ? v.to_h : v }
  end
end
$recorded = Recorder.new
$provider_blocks = []
$ruby_blocks = []
require "pathname"
class StandInProviderConfig
  def initialize(r)
    @r = r
  end
  def method_missing(name, *args)
    n = name.to_s
    return @r.send(name, *args) if n.end_with?("=")
    return ENV["STANDIN_ADVANCED"] == "1" if n == "advanced_network" && @r.get(n).nil?
    @r.get(n)
  end
  def respond_to_missing?(*)
    true
  end
end
StandInMachine = Struct.new(:name, :provider_name, :data_dir, :provider_config, :config, :ui)
class StandInUI
  def detail(msg)
    $stderr.puts "UI: #{msg}"
  end
end
StandInVM = Struct.new(:networks)
StandInConfig = Struct.new(:vm)
load ARGV[0]
failed = false
$provider_blocks.each do |name, r, blk|
  begin
    blk.call(r)
  rescue => e
    $stderr.puts "` + ProviderPhase + `#{e.message}` + inProvider + `#{name}]"
    failed = true
  end
end
exit 1 if failed
if ENV["STANDIN_FIRE"]
  qemu = $recorded.get("vm").get("provider qemu")
  qemu.net_device = ENV["STANDIN_NET_DEVICE"] if ENV["STANDIN_NET_DEVICE"]
  qemu.get("extra_qemu_args").push(*ENV["STANDIN_EXTRA_ARGS"].split) if ENV["STANDIN_EXTRA_ARGS"]
  networks = ENV["STANDIN_PRIVATE_NETWORK"] == "1" ? [[:private_network, { ip: "192.168.105.10" }]] : []
  machine = StandInMachine.new(:default, (ENV["STANDIN_PROVIDER"] || "qemu").to_sym,
    Pathname.new(ENV["STANDIN_DATA_DIR"]), StandInProviderConfig.new(qemu),
    StandInConfig.new(StandInVM.new(networks)), StandInUI.new)
  $ruby_blocks.each do |key, blk|
    blk.call({}, machine) if key == ENV["STANDIN_FIRE"]
  end
end
puts JSON.generate($recorded.to_h)
`

// BoxRun is what one run of a box's Vagrantfile did.
type BoxRun struct {
	Config map[string]any // what it set, as vagrantStandIn records it
	Stderr string         // its warnings, or the error that stopped it
	Err    error          // non-nil when the Vagrantfile raised
}

// Refused reports whether the run stopped with an error saying want, raised
// where Vagrant gives it its clean message: inside a provider block.
func (r BoxRun) Refused(want string) bool {
	return r.Err != nil && strings.Contains(r.Stderr, ProviderPhase) && strings.Contains(r.Stderr, want)
}

// RefusedIn reports whether the run's error saying want was raised in the
// block of every one of providers.
func (r BoxRun) RefusedIn(want string, providers ...string) bool {
	if !r.Refused(want) {
		return false
	}
	for _, p := range providers {
		found := false
		for _, line := range strings.Split(r.Stderr, "\n") {
			if strings.Contains(line, ProviderPhase) && strings.Contains(line, want) && strings.HasSuffix(line, inProvider+p+"]") {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Provider is what the Vagrantfile set in config.vm.provider name's
// block, or nil.
func (r BoxRun) Provider(name string) map[string]any {
	vm, _ := r.Config["vm"].(map[string]any)
	p, _ := vm["provider "+name].(map[string]any)
	return p
}

// RunBoxVagrantfile runs a box's rendered Vagrantfile under ruby, as
// Vagrant would on a host whose OS is host ("darwin", or anything else
// for not), with env as the whole of its environment besides PATH.
func RunBoxVagrantfile(t *testing.T, text, host string, env map[string]string) BoxRun {
	t.Helper()
	ruby := Ruby(t)
	dir := t.TempDir()
	vagrantfile := filepath.Join(dir, "Vagrantfile")
	standIn := filepath.Join(dir, "vagrant-stand-in.rb")
	if err := os.WriteFile(vagrantfile, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standIn, []byte(vagrantStandIn), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ruby, standIn, vagrantfile, host)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	run := BoxRun{Err: cmd.Run(), Stderr: stderr.String()}
	if run.Err == nil {
		if err := json.Unmarshal(stdout.Bytes(), &run.Config); err != nil {
			t.Fatalf("the Vagrantfile's recorded settings: %v\n%s", err, stdout.String())
		}
	}
	return run
}

// qemuArgs is what a qemu provider block passes QEMU of its own (its
// extra_qemu_args), as one string.
func qemuArgs(t *testing.T, qe map[string]any) string {
	t.Helper()
	raw, ok := qe["extra_qemu_args"].([]any)
	if !ok {
		t.Fatalf("the qemu provider sets no extra_qemu_args: %v", qe)
	}
	var args []string
	for _, a := range raw {
		args = append(args, a.(string))
	}
	return strings.Join(args, " ")
}

// everyProvider is every provider a box's Vagrantfile has a block for.
// Vagrant runs every one of them on every command, so each must refuse a
// bad value itself: the qemu block raising first today is no reason for
// the others not to.
var everyProvider = []string{"qemu", "virtualbox", "vmware_desktop"}

// CheckGuestEnvironment runs templates/<osName>'s box Vagrantfile, rendered
// with overrides, through the GUEST_MACOSX_ settings every box reads the
// same way: GUEST_MACOSX_GUI's values, on and off and refused; the qemu
// display each host gets by default; GUEST_MACOSX_QEMU_DISPLAY's override,
// which asks for a display on its own and is held to a -display value's
// characters; and the VirtualBox and VMware blocks' gui.
func CheckGuestEnvironment(t *testing.T, osName string, overrides map[string]cty.Value) {
	t.Helper()
	text := RenderBoxVagrantfile(t, osName, overrides)
	Ruby(t)

	// headless asserts a run gave QEMU no display, daemonized, and left
	// VirtualBox's and VMware's gui unset.
	headless := func(t *testing.T, r BoxRun) {
		t.Helper()
		if r.Err != nil {
			t.Fatalf("the Vagrantfile raised: %v\n%s", r.Err, r.Stderr)
		}
		qe := r.Provider("qemu")
		if got := qemuArgs(t, qe); !strings.HasSuffix(got, " -display none") || strings.Count(got, "-display") != 1 {
			t.Errorf("headless, extra_qemu_args = %q, want it to end -display none", got)
		}
		if _, ok := qe["no_daemonize"]; ok {
			t.Errorf("headless, the qemu provider sets no_daemonize = %v", qe["no_daemonize"])
		}
		for _, p := range []string{"virtualbox", "vmware_desktop"} {
			if g, ok := r.Provider(p)["gui"]; ok {
				t.Errorf("headless, the %s provider sets gui = %v", p, g)
			}
		}
	}
	// window asserts a run gave QEMU display, in the foreground.
	window := func(t *testing.T, r BoxRun, display string) {
		t.Helper()
		if r.Err != nil {
			t.Fatalf("the Vagrantfile raised: %v\n%s", r.Err, r.Stderr)
		}
		qe := r.Provider("qemu")
		if got := qemuArgs(t, qe); !strings.HasSuffix(got, " -display "+display) || strings.Count(got, "-display") != 1 {
			t.Errorf("extra_qemu_args = %q, want it to end -display %s", got, display)
		}
		if qe["no_daemonize"] != true {
			t.Errorf("with a display, no_daemonize = %v, want true", qe["no_daemonize"])
		}
	}

	t.Run("unset is headless", func(t *testing.T) {
		headless(t, RunBoxVagrantfile(t, text, "linux", nil))
	})
	for _, off := range []string{"", "0", "false", "no", "FALSE", "No"} {
		t.Run("GUEST_MACOSX_GUI="+off+" is headless", func(t *testing.T) {
			headless(t, RunBoxVagrantfile(t, text, "linux", map[string]string{"GUEST_MACOSX_GUI": off}))
		})
	}
	for _, on := range []string{"1", "true", "yes", "TRUE", "Yes"} {
		t.Run("GUEST_MACOSX_GUI="+on+" opens a window", func(t *testing.T) {
			env := map[string]string{"GUEST_MACOSX_GUI": on}
			for host, display := range map[string]string{"linux": "gtk", "netbsd": "gtk", "darwin": "cocoa"} {
				r := RunBoxVagrantfile(t, text, host, env)
				window(t, r, display)
				for _, p := range []string{"virtualbox", "vmware_desktop"} {
					if g := r.Provider(p)["gui"]; g != true {
						t.Errorf("on %s, the %s provider's gui = %v, want true", host, p, g)
					}
				}
			}
		})
	}
	for _, bad := range []string{"2", "on", "off", "y", "ture", " 1"} {
		t.Run("GUEST_MACOSX_GUI="+bad+" is refused", func(t *testing.T) {
			r := RunBoxVagrantfile(t, text, "linux", map[string]string{"GUEST_MACOSX_GUI": bad})
			if r.Err == nil {
				t.Fatalf("the Vagrantfile accepted GUEST_MACOSX_GUI=%q", bad)
			}
			for _, want := range []string{ProviderPhase + "GUEST_MACOSX_GUI: want", "1, true or yes", "0, false, no", "'" + bad + "'"} {
				if !r.RefusedIn(want, everyProvider...) {
					t.Errorf("its error, raised in every provider block (%v), doesn't say %q:\n%s", everyProvider, want, r.Stderr)
				}
			}
		})
	}
	for _, d := range []string{"sdl", "vnc=:1", "gtk,zoom-to-fit=on", "vnc=127.0.0.1:5"} {
		t.Run("GUEST_MACOSX_QEMU_DISPLAY="+d, func(t *testing.T) {
			for host := range map[string]bool{"linux": true, "darwin": true} {
				// On its own it asks for a display, and it overrides the
				// host's default when GUEST_MACOSX_GUI asks for one too.
				r := RunBoxVagrantfile(t, text, host, map[string]string{"GUEST_MACOSX_QEMU_DISPLAY": d})
				window(t, r, d)
				for _, p := range []string{"virtualbox", "vmware_desktop"} {
					if g, ok := r.Provider(p)["gui"]; ok {
						t.Errorf("the qemu-only GUEST_MACOSX_QEMU_DISPLAY set the %s provider's gui = %v", p, g)
					}
				}
				window(t, RunBoxVagrantfile(t, text, host, map[string]string{"GUEST_MACOSX_QEMU_DISPLAY": d, "GUEST_MACOSX_GUI": "1"}), d)
			}
		})
	}
	for _, bad := range []string{"sdl gl=on", "gtk;true", `sdl"`, "-gtk", "vnc=unix:/tmp/s", "$(true)"} {
		t.Run("GUEST_MACOSX_QEMU_DISPLAY="+bad+" is refused", func(t *testing.T) {
			r := RunBoxVagrantfile(t, text, "linux", map[string]string{"GUEST_MACOSX_QEMU_DISPLAY": bad})
			if r.Err == nil {
				t.Fatalf("the Vagrantfile accepted GUEST_MACOSX_QEMU_DISPLAY=%q", bad)
			}
			if !r.RefusedIn(ProviderPhase+"GUEST_MACOSX_QEMU_DISPLAY: want a QEMU -display value", everyProvider...) {
				t.Errorf("its error, raised in every provider block (%v), doesn't name the variable and what it wants:\n%s", everyProvider, r.Stderr)
			}
		})
	}
	t.Run("GUEST_MACOSX_QEMU_DISPLAY=none is headless", func(t *testing.T) {
		// QEMU's own name for no display: the same run as unset,
		// daemonized, not a foreground QEMU with nothing to show.
		headless(t, RunBoxVagrantfile(t, text, "linux", map[string]string{"GUEST_MACOSX_QEMU_DISPLAY": "none"}))
		// With GUEST_MACOSX_GUI it is no override, and the host's window opens.
		window(t, RunBoxVagrantfile(t, text, "linux", map[string]string{"GUEST_MACOSX_QEMU_DISPLAY": "none", "GUEST_MACOSX_GUI": "1"}), "gtk")
	})
	t.Run("no other variable is read", func(t *testing.T) {
		// The names before GUEST_MACOSX_ are gone, not kept beside it.
		headless(t, RunBoxVagrantfile(t, text, "linux", map[string]string{"MAVERICKS_DISPLAY": "gtk"}))
		if strings.Contains(text, "MAVERICKS_") {
			t.Error("the box's Vagrantfile still names a MAVERICKS_ variable")
		}
	})
}

// CheckMachineIdentity holds templates/<osName>'s box Vagrantfile to giving
// each Vagrant machine its own host UUID and NIC address, kept with that
// machine (docs/decisions/0024): a trigger before up and reload, under the
// qemu provider only, that reads guest-macosx-host-uuid from the machine's
// data dir (writing a fresh UUID there the first time) and passes QEMU
// -uuid, and gives the first NIC alone, through net_device, a locally
// administered 52:54:00 address derived from the UUID, never one of QEMU's
// own defaults (52:54:00:12:34:56 and up, which a project's second NIC
// without an address of its own gets); not under vagrant-qemu's dual-NIC
// mode, which gives each of its NICs an address of its own; and not over
// an address a project gave the NIC. It tells the user which of these it
// did. Nothing of the box's forgets the UUID: Vagrant empties the data dir
// itself on a destroy that happens (and not on one the user declines).
func CheckMachineIdentity(t *testing.T, osName string, overrides map[string]cty.Value) {
	t.Helper()
	text := RenderBoxVagrantfile(t, osName, overrides)
	Ruby(t)
	nic := TemplateVars(t, osName, overrides)["nic"].AsString()
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	macRe := regexp.MustCompile(`^52:54:00:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$`)
	const store = "guest-macosx-host-uuid"

	// up runs the Vagrantfile and fires its before-up trigger for a
	// machine whose data dir is dir, as `vagrant up` does on every run:
	// a fresh read of the Vagrantfile each time. It returns the -uuid,
	// the net_device, and extra_qemu_args joined.
	var said string // what the last up's trigger told the user (ui.detail)
	up := func(t *testing.T, dir string, env map[string]string) (uuid, netDevice, args string) {
		t.Helper()
		e := map[string]string{"STANDIN_FIRE": "before up reload", "STANDIN_DATA_DIR": dir}
		for k, v := range env {
			e[k] = v
		}
		r := RunBoxVagrantfile(t, text, "linux", e)
		if r.Err != nil {
			t.Fatalf("the Vagrantfile raised: %v\n%s", r.Err, r.Stderr)
		}
		qe := r.Provider("qemu")
		raw, _ := qe["extra_qemu_args"].([]any)
		var a []string
		for i, x := range raw {
			s := x.(string)
			a = append(a, s)
			if s == "-uuid" && i+1 < len(raw) {
				uuid = raw[i+1].(string)
			}
		}
		netDevice, _ = qe["net_device"].(string)
		said = ""
		for _, l := range strings.Split(r.Stderr, "\n") {
			if strings.HasPrefix(l, "UI: ") {
				said += strings.TrimPrefix(l, "UI: ") + "\n"
			}
		}
		return uuid, netDevice, strings.Join(a, " ")
	}
	// countWord is how many of args' words are w (a temporary dir's
	// name can hold "-uuid" too).
	countWord := func(args, w string) int {
		n := 0
		for _, a := range strings.Fields(args) {
			if a == w {
				n++
			}
		}
		return n
	}
	// mac is the address net_device gives the first NIC, or "".
	mac := func(netDevice string) string {
		if i := strings.Index(netDevice, ",mac="); i >= 0 {
			return netDevice[i+len(",mac="):]
		}
		return ""
	}

	t.Run("the first up makes the machine a UUID, keeps it in its data dir, and gives the first NIC alone an address", func(t *testing.T) {
		dir := t.TempDir()
		uuid, nd, args := up(t, dir, nil)
		if !uuidRe.MatchString(uuid) {
			t.Fatalf("-uuid %q is not a UUID; extra_qemu_args %q", uuid, args)
		}
		if !strings.HasPrefix(nd, nic+",mac=") || !macRe.MatchString(mac(nd)) {
			t.Errorf("net_device %q, want %s,mac=52:54:00:xx:xx:xx", nd, nic)
		}
		b, err := os.ReadFile(filepath.Join(dir, store))
		if err != nil || strings.TrimSpace(string(b)) != uuid {
			t.Errorf("%s holds %q (%v), want %s", store, b, err, uuid)
		}
		if countWord(args, "-uuid") != 1 || strings.Contains(args, "-global") || strings.Contains(args, "mac=") {
			t.Errorf("extra_qemu_args %q: want one -uuid, and no address for every NIC of a model", args)
		}
		if want := "host UUID " + uuid + " (new, kept in " + filepath.Join(dir, store) + "); first NIC: " + mac(nd) + ", derived from the UUID\n"; said != want {
			t.Errorf("the trigger said %q, want %q", said, want)
		}
		if !strings.Contains(text, `t.name = "guest-macosx host UUID"`) {
			t.Error("the trigger has no name: Vagrant would print only \"Running trigger...\"")
		}
		up(t, dir, nil)
		if want := "host UUID " + uuid + " (kept in " + filepath.Join(dir, store) + "); first NIC: " + mac(nd) + ", derived from the UUID\n"; said != want {
			t.Errorf("the second up said %q, want %q", said, want)
		}
	})
	t.Run("every later up, halt and reload of that machine keeps the same UUID and MAC", func(t *testing.T) {
		dir := t.TempDir()
		u1, n1, _ := up(t, dir, nil)
		for i := 0; i < 3; i++ {
			if u, n, _ := up(t, dir, nil); u != u1 || n != n1 {
				t.Fatalf("run %d: %s %s, the first run %s %s", i+2, u, n, u1, n1)
			}
		}
	})
	t.Run("two machines get two UUIDs and two MACs", func(t *testing.T) {
		u1, n1, _ := up(t, t.TempDir(), nil)
		u2, n2, _ := up(t, t.TempDir(), nil)
		if u1 == u2 || mac(n1) == mac(n2) {
			t.Errorf("two machines share %s %s / %s %s", u1, n1, u2, n2)
		}
	})
	t.Run("the address is never one QEMU gives a NIC by default, so a project's second NIC cannot share it", func(t *testing.T) {
		// A UUID whose SHA-256 begins 1234: the first three address bytes
		// it would give are 12:34:xx, QEMU's own range.
		var u string
		for i := 0; ; i++ {
			c := fmt.Sprintf("00000000-0000-4000-8000-%012x", i)
			if strings.HasPrefix(fmt.Sprintf("%x", sha256.Sum256([]byte(c))), "1234") {
				u = c
				break
			}
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, store), []byte(u+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, nd, _ := up(t, dir, nil)
		if got != u {
			t.Fatalf("-uuid %s, the stored %s", got, u)
		}
		if m := mac(nd); !macRe.MatchString(m) || strings.HasPrefix(m, "52:54:00:12:34:") {
			t.Errorf("for %s the address is %q: QEMU's own range, or none", u, m)
		}
	})
	t.Run("under vagrant-qemu's dual-NIC mode its own addresses stand", func(t *testing.T) {
		// advanced_network with a private network: vagrant-qemu gives each
		// NIC an address of its own from the machine's id, and appends it
		// to net_device for both, so ours would be a second mac on each.
		u, nd, _ := up(t, t.TempDir(), map[string]string{"STANDIN_ADVANCED": "1", "STANDIN_PRIVATE_NETWORK": "1"})
		if !uuidRe.MatchString(u) || nd != nic {
			t.Errorf("dual-NIC: -uuid %q, net_device %q, want a UUID and %s untouched", u, nd, nic)
		}
		if !strings.HasSuffix(said, "; first NIC: vagrant-qemu's own dual-NIC addresses\n") {
			t.Errorf("the trigger said %q", said)
		}
		// advanced_network with no private network is one NIC, as without it
		if _, nd, _ := up(t, t.TempDir(), map[string]string{"STANDIN_ADVANCED": "1"}); !macRe.MatchString(mac(nd)) {
			t.Errorf("advanced_network alone: net_device %q, want an address", nd)
		}
	})
	t.Run("an address a project gave the NIC is left alone", func(t *testing.T) {
		own := nic + ",mac=02:00:00:00:00:01"
		if _, nd, _ := up(t, t.TempDir(), map[string]string{"STANDIN_NET_DEVICE": own}); nd != own {
			t.Errorf("net_device %q, want the project's %q", nd, own)
		}
		if !strings.HasSuffix(said, "; first NIC: the project's own address\n") {
			t.Errorf("the trigger said %q", said)
		}
	})
	t.Run("only under qemu: another provider names its own machine", func(t *testing.T) {
		dir := t.TempDir()
		if u, nd, _ := up(t, dir, map[string]string{"STANDIN_PROVIDER": "virtualbox"}); u != "" || nd != nic {
			t.Errorf("under virtualbox the trigger passed -uuid %q, net_device %q", u, nd)
		}
		if _, err := os.Stat(filepath.Join(dir, store)); err == nil {
			t.Error("under virtualbox the trigger wrote " + store)
		}
	})
	t.Run("a stored value that is not a UUID is replaced, not passed to QEMU", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, store), []byte("-display gtk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if u, _, args := up(t, dir, nil); !uuidRe.MatchString(u) || strings.Contains(args, "gtk") {
			t.Errorf("-uuid %q, extra_qemu_args %q", u, args)
		}
		if !strings.Contains(said, "(new (the stored value was not a UUID), kept in ") {
			t.Errorf("the trigger said %q, want it to say it replaced the stored value", said)
		}
	})
	t.Run("a destroy Vagrant carries out forgets the UUID, by emptying the data dir: the next machine is a new one", func(t *testing.T) {
		dir := t.TempDir()
		u1, n1, _ := up(t, dir, nil)
		// vagrant-qemu's destroy sets machine.id = nil, and Vagrant then
		// deletes every child of the data dir (Machine#id=).
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
		if u2, n2, _ := up(t, dir, nil); u2 == u1 || mac(n2) == mac(n1) {
			t.Errorf("the machine after destroy kept %s %s", u2, n2)
		}
	})
	t.Run("a destroy the user declines keeps the machine's identity", func(t *testing.T) {
		// An after-destroy trigger fires whether or not the user said yes
		// (the action trigger wraps DestroyConfirm), so the box has none.
		if strings.Contains(text, "trigger.after :destroy") || strings.Contains(text, "trigger.after(:destroy") {
			t.Error("the box's Vagrantfile has an after-destroy trigger: it runs on a declined destroy too")
		}
		dir := t.TempDir()
		u1, n1, _ := up(t, dir, nil)
		r := RunBoxVagrantfile(t, text, "linux", map[string]string{"STANDIN_FIRE": "after destroy", "STANDIN_DATA_DIR": dir})
		if r.Err != nil {
			t.Fatalf("the Vagrantfile raised: %v\n%s", r.Err, r.Stderr)
		}
		if u2, n2, _ := up(t, dir, nil); u2 != u1 || n2 != n1 {
			t.Errorf("after a declined destroy: %s %s, before %s %s", u2, n2, u1, n1)
		}
	})
	t.Run("a -uuid of the project's own is the machine's: no second one, nothing stored, its address from it", func(t *testing.T) {
		dir := t.TempDir()
		const own = "0a1b2c3d-0000-4000-8000-00000000abcd"
		u, nd, args := up(t, dir, map[string]string{"STANDIN_EXTRA_ARGS": "-uuid " + own})
		if u != own || countWord(args, "-uuid") != 1 {
			t.Errorf("-uuid %q, extra_qemu_args %q: want the project's %s, once", u, args, own)
		}
		if _, err := os.Stat(filepath.Join(dir, store)); err == nil {
			t.Error("the trigger stored a UUID of its own beside the project's")
		}
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(own)))
		if want := "52:54:00:" + h[0:2] + ":" + h[2:4] + ":" + h[4:6]; mac(nd) != want {
			t.Errorf("first NIC %q, want %s, derived from the project's UUID", mac(nd), want)
		}
		if !strings.HasPrefix(said, "host UUID "+own+" (the project's own -uuid); ") {
			t.Errorf("the trigger said %q", said)
		}
	})
}

// releaseLine is the line a box's Vagrantfile names its release on, for the
// tools that boot a box without Vagrant (bin/gate-vm.sh): a comment, so
// Vagrant itself never reads it, of exactly this form.
var releaseLine = regexp.MustCompile(`(?m)^# guest-macosx-release: (.*)$`)

// CheckBoxRelease: the box's Vagrantfile, rendered with variables.pkr.hcl's
// defaults plus overrides, names its release on exactly one line, and it is
// want. bin/gate-vm.sh reads that line to log in and shut down as the
// release needs (docs/decisions/0014, "The boot gate").
func CheckBoxRelease(t *testing.T, osName string, overrides map[string]cty.Value, want string) {
	t.Helper()
	text := RenderBoxVagrantfile(t, osName, overrides)
	m := releaseLine.FindAllStringSubmatch(text, -1)
	if len(m) != 1 {
		t.Fatalf("the box Vagrantfile has %d '# guest-macosx-release: ' lines, want 1", len(m))
	}
	if m[0][1] != want {
		t.Errorf("the box Vagrantfile names release %q, want %q", m[0][1], want)
	}
}
