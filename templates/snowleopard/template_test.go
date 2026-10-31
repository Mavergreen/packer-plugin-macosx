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

	"github.com/zclconf/go-cty/cty"

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

	templatetest.ValidateDisplay(t, run, "-var", disc)
}

// TestTheBuilderGetsTheDisplay: the qemu builder's display is the display
// variable, checked against headless.
func TestTheBuilderGetsTheDisplay(t *testing.T) {
	templatetest.QemuSourceTakesDisplay(t, osName, osName+".pkr.hcl")
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

// TestTheVariablesNameNoMavericksPath: what `packer inspect` shows of
// this template's variables is about 10.6 and this directory. The
// default private key is the one beside snowleopard.pkr.hcl
// (effective_ssh_private_key_file), not templates/mavericks/'s.
func TestTheVariablesNameNoMavericksPath(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templatetest.RepoRoot(t), "templates", osName, "variables.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(b), "\n") {
		if strings.Contains(strings.ToLower(line), "mavericks") {
			t.Errorf("variables.pkr.hcl line %d names Mavericks: %s", i+1, strings.TrimSpace(line))
		}
	}
	const key = "templates/snowleopard/vagrant-standard-insecure-first-boot-only.key.rsa"
	if !bytes.Contains(b, []byte(key)) {
		t.Errorf("ssh_private_key_file's description never names %s", key)
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

// TestTheBuildsMachineIsTheOneShape: the build's QEMU has the devices
// every build and box of both releases has (templatetest.MachineShapeErrors):
// OpenCore's disk on virtio-blk, and USB for input alone, EHCI with no companion,
// a usb-kbd and a usb-tablet. No UHCI: 10.6.0's AppleUSBUHCI wedged installs
// polling a halted QEMU UHCI controller with interrupts off (MEASURED
// 2026-10-04, docs/decisions/0014). 10.6's AppleUSBEHCI drives the keyboard
// and tablet, 10.6.0's in the installer included (docs/decisions/0035).
func TestTheBuildsMachineIsTheOneShape(t *testing.T) {
	templatetest.CheckBuildMachine(t, osName, osName+".pkr.hcl")
}

// TestTheBoxsMachineIsTheOneShape: the box boots the same devices as its
// build, through every argv setting its Vagrantfile makes.
func TestTheBoxsMachineIsTheOneShape(t *testing.T) {
	templatetest.CheckBoxMachine(t, osName, boxOverrides)
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

// TestTheLastProvisionerZeroFillsFreeSpace: as templates/mavericks' test
// of the same name, which has the measurements. Here it also zeroes the
// blocks a first boot freed, among them the old firstboot.conf's, which
// held a configured password in plain text until the first boot rewrote
// it. That 10.6.8's diskutil has `secureErase freespace` is REASONED
// (Apple's diskutil(8) has had it since 10.5), not measured.
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
	block = block[:strings.Index(block, "\n  }\n")]
	if !strings.Contains(block, "sudo diskutil secureErase freespace 0 /") {
		t.Fatalf("the last provisioner does not zero free space:\n%s", block)
	}
	if collect := strings.LastIndex(s[:last], "local.box_vagrantfile"); collect < 0 {
		t.Fatal("the zero-fill does not come after the box's Vagrantfile is collected")
	}
}

// TestTheTargetDriveStoresZerosAsHoles: as templates/mavericks' test of the
// same name. Without it, the zero-fill writes out all of the guest's free
// space into the qcow2 (51.7 GB measured for 10.9).
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

// boxOverrides is what the box Vagrantfile's rendering needs beyond the
// defaults: installer has none.
var boxOverrides = map[string]cty.Value{"installer": cty.StringVal("/path/to/your/Snow Leopard.iso")}

// TestBoxVagrantfileReadsTheGuestEnvironment: GUEST_MACOSX_GUI and
// GUEST_MACOSX_QEMU_DISPLAY, as every box reads them
// (templatetest.CheckGuestEnvironment).
func TestBoxVagrantfileReadsTheGuestEnvironment(t *testing.T) {
	templatetest.CheckGuestEnvironment(t, osName, boxOverrides)
}

// TestBoxVagrantfileSaysItIgnoresTheCPUISA: no instruction-set level has
// been measured on 10.6, so GUEST_MACOSX_CPU_ISA, set, is ignored out
// loud: a warning naming it and why, the build's own cpu line, and
// nothing stopped. Unset or empty, there is nothing to say.
func TestBoxVagrantfileSaysItIgnoresTheCPUISA(t *testing.T) {
	text := templatetest.RenderBoxVagrantfile(t, osName, boxOverrides)
	build := templatetest.TemplateVars(t, osName, boxOverrides)["cpu"].AsString()
	for _, c := range []struct {
		env   map[string]string
		warns bool
	}{
		{nil, false},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": ""}, false},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": "avx"}, true},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": "none"}, true},
		{map[string]string{"GUEST_MACOSX_CPU_ISA": "bogus"}, true},
	} {
		r := templatetest.RunBoxVagrantfile(t, text, "linux", c.env)
		if r.Err != nil {
			t.Errorf("%v: the Vagrantfile raised: %v\n%s", c.env, r.Err, r.Stderr)
			continue
		}
		if got := r.Provider("qemu")["cpu"]; got != build {
			t.Errorf("%v: cpu = %v, want the build's own %q", c.env, got, build)
		}
		warned := strings.Contains(r.Stderr, "GUEST_MACOSX_CPU_ISA="+c.env["GUEST_MACOSX_CPU_ISA"]+" is ignored on 10.6") &&
			strings.Contains(r.Stderr, "no instruction-set level has been measured")
		if warned != c.warns {
			t.Errorf("%v: warned %v, want %v: %q", c.env, warned, c.warns, r.Stderr)
		}
	}
}

// TestTheBuildGivesQEMUAUUIDOfItsOwn: every build's guest gets a host UUID
// of its own, there from its first second (docs/decisions/0024).
func TestTheBuildGivesQEMUAUUIDOfItsOwn(t *testing.T) {
	templatetest.CheckBuildUUID(t, osName, "snowleopard.pkr.hcl")
}

// TestEachVagrantMachineHasItsOwnHostUUID: distinct machines, distinct host
// UUIDs and MACs, each kept with its machine (templatetest.CheckMachineIdentity).
func TestEachVagrantMachineHasItsOwnHostUUID(t *testing.T) {
	templatetest.CheckMachineIdentity(t, osName, boxOverrides)
}

// TestTheBuildGivesItsNICAnAddressOfItsOwn: the build's guest's NIC
// address, derived from its UUID by the one rule (docs/decisions/0024).
func TestTheBuildGivesItsNICAnAddressOfItsOwn(t *testing.T) {
	templatetest.CheckBuildMAC(t, osName, "snowleopard.pkr.hcl")
}

// TestTheInstallLogIsOutsideTheOutputDirectory: the guest's first serial
// port, where the media's autoinstall hook follows the install's logs, is a
// file beside the output directory, never in it: Packer deletes the output
// directory when a build fails, and a failed install is the build whose
// logs matter (docs/decisions/0027).
func TestTheInstallLogIsOutsideTheOutputDirectory(t *testing.T) {
	templatetest.CheckInstallLog(t, osName, osName+".pkr.hcl")
}

// TestAFailedInstallEndsTheBuild: refused logins end it (docs/decisions/0028).
func TestAFailedInstallEndsTheBuild(t *testing.T) {
	templatetest.CheckHandshakeAttempts(t, osName, osName+".pkr.hcl")
}

// TestTheBuildsGuestReachesNothingOffLink: the build's guest network is
// restricted, so nothing in the guest -- Apple's own daemons included --
// fetches anything during the build (docs/decisions/0034).
func TestTheBuildsGuestReachesNothingOffLink(t *testing.T) {
	templatetest.CheckGuestNetwork(t, osName, osName+".pkr.hcl")
}

// TestBoxVagrantfileNamesItsRelease: the box says it is 10.6, on the one line
// bin/gate-vm.sh reads to accept 10.6's ssh-rsa, shut down with a bare
// shutdown and boot one CPU, as this box's own Vagrantfile does.
func TestBoxVagrantfileNamesItsRelease(t *testing.T) {
	templatetest.CheckBoxRelease(t, osName, boxOverrides, "10.6")
}
