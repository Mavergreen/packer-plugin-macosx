package templatetest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// The guest machine's devices: one shape for both releases, in every build
// and every box (docs/decisions/0035).
//
//   - OpenCore's disk is a virtio-blk-pci device with bootindex=0, from a
//     -drive id=opencore that is if=none and snapshot=on. OVMF drives it
//     and boots it first; neither 10.6 nor 10.9 has a virtio driver, so the
//     guest never sees the disk, and 10.6's DiskArbitration never calls it
//     unreadable (MEASURED 2026-10-27, docs/decisions/0035).
//   - USB carries input alone: one ich9-usb-ehci1 with id=ehci at the
//     ICH9's own USB slot, pcie.0 0x1d.0, which avoids sharing the NIC's
//     interrupt line (that no other device shares 0x1d's line is REASONED
//     from q35's routing); no companion; and on it one usb-kbd and one usb-tablet. In the
//     first free slot instead it shared the NIC's line, and 10.6.0's
//     installer spun in the NIC's interrupt filter (MEASURED 2026-10-27).
//   - Nothing else on USB: no XHCI, UHCI or OHCI controller, no
//     usb-storage, no mouse, no other input device, and no q35 USB by
//     other means (usb=on in the machine, -usb, -usbdevice).
//
// 10.6.0's AppleUSBUHCI wedged installs polling a halted QEMU UHCI
// controller (docs/decisions/0014), and both releases' AppleUSBEHCI drive
// QEMU's high-speed keyboard and tablet with no companion (0035).

// OpenCoreDevice is OpenCore's disk's -device value, exactly. Slot 0x10's
// INTA is PIRQE (GSI 20): not the NIC's (slot 1, PIRQF) or EHCI's (0x1d,
// PIRQD), and above the slots QEMU hands out to the devices placed before
// it (MEASURED 2026-10-28, docs/decisions/0035).
const OpenCoreDevice = "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10"

// OpenCoreSlot is the PCI slot OpenCore's disk has to itself.
const OpenCoreSlot = "0x10"

// EHCIAddr is the EHCI controller's PCI address on pcie.0.
const EHCIAddr = "0x1d.0"

// usbHostRe is a QEMU USB host controller model.
var usbHostRe = regexp.MustCompile(`(^|-)(xhci|ehci|uhci|ohci)($|[0-9-])`)

// inputRe is a QEMU input device model of any kind.
var inputRe = regexp.MustCompile(`^(usb-(kbd|mouse|tablet|wacom-tablet)|virtio-(keyboard|mouse|tablet|input-host)(-pci|-device|-ccw)?|.*-kbd|.*-mouse|.*-tablet)$`)

// nicModel is the one -device model a build interpolates: its NIC row.
const nicModel = "${var.nic}"

// opts is a QEMU option string's key=value pairs, after its first field;
// a bare word is a key with value "".
func opts(v string) (string, map[string][]string) {
	parts := strings.Split(v, ",")
	o := map[string][]string{}
	for _, p := range parts[1:] {
		k, val, _ := strings.Cut(p, "=")
		o[k] = append(o[k], val)
	}
	return parts[0], o
}

func one(o map[string][]string, k string) string {
	if len(o[k]) != 1 {
		return "<" + strings.Repeat(k+" ", len(o[k])) + ">"
	}
	return o[k][0]
}

// machineOpts is a -machine value's options. A value whose first field is
// itself an option (-M usb=on) has no type: QEMU merges it into the
// machine already given, so every field is an option.
func machineOpts(v string) map[string][]string {
	if first, _, _ := strings.Cut(v, ","); strings.Contains(first, "=") {
		v = "," + v
	}
	_, o := opts(v)
	return o
}

// NotLiteral starts a value BuildMachine could not read as a literal or a
// template (a traversal such as local.x): whatever it holds, the shape
// check cannot say, so it is refused.
const NotLiteral = "<not a literal or a template> "

// implicitDisk is every QEMU flag that makes a disk device with no -device
// row: a -drive whose if= is not none or pflash, and the shorthands.
var implicitDisk = map[string]bool{"-hda": true, "-hdb": true, "-hdc": true, "-hdd": true, "-cdrom": true, "-fda": true, "-fdb": true, "-sd": true, "-mtdblock": true}

// MachineShapeErrors holds a machine option string (q35,...) and a QEMU
// argv to the shape above, and returns what is wrong, one line each.
func MachineShapeErrors(machine string, argv []string) []string {
	var errs []string
	bad := func(s string) { errs = append(errs, s) }

	checkMachine := func(flag, v string) {
		if strings.HasPrefix(v, NotLiteral) {
			bad(flag + " is neither a literal nor a template, so the shape check cannot read it: " + strings.TrimPrefix(v, NotLiteral))
			return
		}
		if strings.Contains(v, "${") {
			bad(flag + " interpolates, so the shape check cannot say what it adds: " + v)
		}
		for _, u := range machineOpts(v)["usb"] {
			if u != "off" {
				bad(flag + " " + v + " sets usb=" + u + ": q35 then adds EHCI with UHCI companions")
			}
		}
	}
	checkMachine("the machine", machine)
	var devices, drives []string
	for i, a := range argv {
		switch a {
		case "-usb", "-usbdevice":
			bad("the argv passes " + a + ": q35 then adds EHCI with UHCI companions")
		case "-machine", "-M":
			if i+1 < len(argv) {
				checkMachine(a, argv[i+1])
			}
		case "-device":
			if i+1 < len(argv) {
				d := argv[i+1]
				if first, _, _ := strings.Cut(d, ","); strings.Contains(first, "=") || strings.HasPrefix(d, "{") {
					bad("a -device whose model is not its first field (a driver= key, or JSON), which this test does not read: " + d)
					continue
				}
				devices = append(devices, d)
			}
		case "-drive":
			if i+1 < len(argv) {
				drives = append(drives, argv[i+1])
				if f := machineOpts(argv[i+1])["if"]; len(f) != 1 || (f[0] != "none" && f[0] != "pflash") {
					bad("a -drive that makes a disk device of its own (if=" + strings.Join(f, ",") + "; QEMU's default is ide): " + argv[i+1])
				}
			}
		}
		if implicitDisk[a] {
			bad("the argv passes " + a + ", which makes a disk device of its own")
		}
		if strings.HasPrefix(a, NotLiteral) {
			bad("a qemuargs value that is neither a literal nor a template, so the shape check cannot read it: " + strings.TrimPrefix(a, NotLiteral))
		}
		if strings.HasPrefix(a, "-") && strings.ContainsAny(a, " \t\n") {
			bad("one argument holds a flag and its value, which QEMU reads as one word: " + strings.TrimSpace(a))
		}
	}

	ids := map[string]int{}
	ehci := 0
	var kbd, tablet, oc []string
	for _, d := range devices {
		model, o := opts(d)
		if id := one(o, "id"); len(o["id"]) == 1 {
			ids[id]++
		}
		switch {
		case strings.Contains(model, "${"):
			if model != nicModel {
				bad("a -device whose model does not evaluate, so this test cannot say what it is: " + d)
			}
		case usbHostRe.MatchString(model):
			if model != "ich9-usb-ehci1" {
				bad("a USB host controller other than EHCI alone: " + d)
				continue
			}
			ehci++
			if one(o, "id") != "ehci" {
				bad("the EHCI controller's id is not ehci: " + d)
			}
			if one(o, "bus") != "pcie.0" || one(o, "addr") != EHCIAddr || len(o["multifunction"]) != 0 {
				bad("the EHCI controller is not at pcie.0 " + EHCIAddr + ", the slot that avoids sharing the NIC's interrupt line: " + d)
			}
			if len(o["masterbus"]) != 0 || len(o["firstport"]) != 0 {
				bad("a companion controller: " + d)
			}
		case model == "usb-kbd":
			kbd = append(kbd, d)
		case model == "usb-tablet":
			tablet = append(tablet, d)
		case inputRe.MatchString(model):
			bad("an input device besides the one usb-kbd and the one usb-tablet: " + d)
		case strings.HasPrefix(model, "usb-"):
			bad("a USB device besides the keyboard and the tablet: " + d)
		}
		if a := one(o, "addr"); len(o["addr"]) == 1 && strings.HasPrefix(a, "0x1d.") && model != "ich9-usb-ehci1" {
			bad("a device in EHCI's slot, 0x1d, whose interrupt line it would share: " + d)
		}
		if a := one(o, "addr"); len(o["addr"]) == 1 && ichSlot(a) && one(o, "id") != "ehci" {
			bad("a device in the ICH9's slots 0x19-0x1f, which route through the chipset's own PIRQA-D, EHCI's among them: " + d)
		}
		if a := one(o, "addr"); len(o["addr"]) == 1 && (a == OpenCoreSlot || strings.HasPrefix(a, OpenCoreSlot+".")) && !strings.Contains(d, "drive=opencore") {
			bad("a device in OpenCore's slot, " + OpenCoreSlot + ": " + d)
		}
		if strings.Contains(d, "drive=opencore") {
			oc = append(oc, d)
		} else if len(o["bootindex"]) != 0 {
			bad("a device besides OpenCore's disk with a bootindex: " + d)
		}
		if strings.HasPrefix(model, "virtio-") && !strings.Contains(d, "drive=opencore") {
			bad("a virtio device besides OpenCore's disk, which no guest drives: " + d)
		}
	}
	for id, n := range ids {
		if n > 1 {
			bad("id " + id + " on " + itoa(n) + " devices")
		}
	}
	if ehci != 1 {
		bad("EHCI controllers: " + itoa(ehci) + ", want 1")
	}
	for name, got := range map[string][]string{"usb-kbd": kbd, "usb-tablet": tablet} {
		if len(got) != 1 {
			bad(name + ": " + itoa(len(got)) + ", want 1")
			continue
		}
		if _, o := opts(got[0]); one(o, "bus") != "ehci.0" || len(o) != 1 {
			bad(name + " is not on ehci.0 alone: " + got[0])
		}
	}
	if len(oc) != 1 {
		bad("devices on OpenCore's drive: " + itoa(len(oc)) + ", want 1")
	} else if oc[0] != OpenCoreDevice {
		bad("OpenCore's disk is not " + OpenCoreDevice + ": " + oc[0])
	}
	n := 0
	for _, d := range drives {
		_, o := opts("x," + d)
		if one(o, "id") != "opencore" {
			continue
		}
		n++
		if one(o, "if") != "none" || one(o, "snapshot") != "on" || one(o, "format") != "raw" {
			bad("OpenCore's -drive is not if=none, format=raw, snapshot=on: " + d)
		}
	}
	if n != 1 {
		bad("-drive id=opencore: " + itoa(n) + ", want 1")
	}
	return errs
}

// ichSlot is whether a PCI address (0xSS or 0xSS.F) is in slots 0x19-0x1f.
func ichSlot(addr string) bool {
	slot, _, _ := strings.Cut(addr, ".")
	n, err := strconv.ParseUint(strings.TrimPrefix(slot, "0x"), 16, 8)
	return err == nil && n >= 0x19 && n <= 0x1f
}

// BoxShapeErrors is MachineShapeErrors, and what only a box must hold: its
// firmware read-only and its varstore, the firmware data source's pristine
// one, booted snapshot=on, so no boot writes either.
func BoxShapeErrors(machine string, argv []string) []string {
	errs := MachineShapeErrors(machine, argv)
	code, vars := 0, 0
	for i, a := range argv {
		if a != "-drive" || i+1 >= len(argv) {
			continue
		}
		o := machineOpts(argv[i+1])
		if one(o, "if") != "pflash" {
			continue
		}
		switch one(o, "unit") {
		case "0":
			code++
			if one(o, "readonly") != "on" {
				errs = append(errs, "the box's firmware is not readonly=on: "+argv[i+1])
			}
		case "1":
			vars++
			if one(o, "snapshot") != "on" {
				errs = append(errs, "the box's varstore is not snapshot=on: "+argv[i+1])
			}
		}
	}
	if code != 1 || vars != 1 {
		errs = append(errs, "the box's pflash pair: "+itoa(code)+" firmware, "+itoa(vars)+" varstore, want 1 each")
	}
	return errs
}

func itoa(n int) string { return strconv.Itoa(n) }

// BuildMachine is templates/<osName>/<file>'s qemu source as QEMU gets it:
// its machine_type, and its qemuargs rows flattened in order, as HCL parses
// them (a commented-out row is no row). A value that interpolates keeps its
// ${...} as written, so the shape check sees what it cannot evaluate.
func BuildMachine(t *testing.T, osName, file string) (string, []string) {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	var body *hclsyntax.Body
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type == "source" && len(b.Labels) == 2 && b.Labels[0] == "qemu" {
			body = b.Body
		}
	}
	if body == nil {
		t.Fatalf("%s: no qemu source", file)
	}
	text := func(e hclsyntax.Expression) string {
		if v, d := e.Value(nil); !d.HasErrors() && v.Type() == cty.String {
			return v.AsString()
		}
		r := e.Range()
		raw := strings.Trim(string(src[r.Start.Byte:r.End.Byte]), `"`)
		switch e.(type) {
		case *hclsyntax.TemplateExpr, *hclsyntax.TemplateWrapExpr:
			return raw
		}
		return NotLiteral + raw
	}
	machine := ""
	if a := body.Attributes["machine_type"]; a != nil {
		machine = text(a.Expr)
	}
	qa := body.Attributes["qemuargs"]
	if qa == nil {
		t.Fatalf("%s: the qemu source has no qemuargs", file)
	}
	rows, ok := qa.Expr.(*hclsyntax.TupleConsExpr)
	if !ok {
		t.Fatalf("%s: qemuargs is not a list", file)
	}
	var argv []string
	for _, r := range rows.Exprs {
		row, ok := r.(*hclsyntax.TupleConsExpr)
		if !ok {
			t.Errorf("%s: a qemuargs row that is not a list: %s", file, text(r))
			continue
		}
		for i, e := range row.Exprs {
			v := text(e)
			// -uuid's value is the one traversal a build passes: the
			// build's own UUID (CheckBuildUUID holds it).
			if i == 1 && len(argv) > 0 && argv[len(argv)-1] == "-uuid" {
				v = strings.TrimPrefix(v, NotLiteral)
			}
			argv = append(argv, v)
		}
	}
	return machine, argv
}

// CheckBuildMachine holds templates/<osName>/<file>'s build to the shape.
func CheckBuildMachine(t *testing.T, osName, file string) {
	t.Helper()
	machine, argv := BuildMachine(t, osName, file)
	for _, e := range MachineShapeErrors(machine, argv) {
		t.Errorf("%s: %s", file, e)
	}
}

// BoxMachine is what templates/<osName>'s box Vagrantfile, rendered with
// overrides, has vagrant-qemu give QEMU of its own: the machine, and every
// argv setting (other_default, then extra_qemu_args). vagrant-qemu adds the
// box's disk itself, as -drive if=ide (port 0), and nothing on USB.
func BoxMachine(t *testing.T, osName string, overrides map[string]cty.Value) (string, []string) {
	t.Helper()
	r := RunBoxVagrantfile(t, RenderBoxVagrantfile(t, osName, overrides), "linux", nil)
	if r.Err != nil {
		t.Fatalf("the Vagrantfile raised: %v\n%s", r.Err, r.Stderr)
	}
	qe := r.Provider("qemu")
	machine, _ := qe["machine"].(string)
	var argv []string
	for _, k := range []string{"other_default", "extra_qemu_args"} {
		raw, _ := qe[k].([]any)
		for _, a := range raw {
			s, _ := a.(string)
			argv = append(argv, s)
		}
	}
	return machine, argv
}

// CheckBoxMachine holds templates/<osName>'s box to the shape.
func CheckBoxMachine(t *testing.T, osName string, overrides map[string]cty.Value) {
	t.Helper()
	machine, argv := BoxMachine(t, osName, overrides)
	for _, e := range BoxShapeErrors(machine, argv) {
		t.Errorf("box Vagrantfile: %s", e)
	}
}
