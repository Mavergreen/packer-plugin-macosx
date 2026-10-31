package templatetest

import (
	"strings"
	"testing"
)

// theShape is a build's argv in the one shape, as BuildMachine reads it.
var theShape = []string{
	"-drive", "id=target,if=none,format=qcow2,file={{ .OutputDir }}/{{ .Name }}",
	"-device", "ide-hd,bus=ide.0,drive=target",
	"-drive", "id=installer,if=none,format=raw,snapshot=on,file=${data.x.media.path}",
	"-device", "ide-hd,bus=ide.1,drive=installer",
	"-drive", "id=opencore,if=none,format=raw,snapshot=on,file=${data.x.fw.opencore_image}",
	"-device", "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10",
	"-netdev", "user,id=net0,restrict=on",
	"-device", "${var.nic},netdev=net0,mac=${local.build_mac}",
	"-device", "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0",
	"-device", "usb-kbd,bus=ehci.0",
	"-device", "usb-tablet,bus=ehci.0",
	"-device", "VGA,vgamem_mb=64",
}

func TestTheShapeHasNoErrors(t *testing.T) {
	if errs := MachineShapeErrors("q35,vmport=off", theShape); len(errs) != 0 {
		t.Errorf("the shape itself: %q", errs)
	}
	if errs := MachineShapeErrors("q35,vmport=off,accel=kvm,usb=off", theShape); len(errs) != 0 {
		t.Errorf("usb=off: %q", errs)
	}
}

// plus is theShape with more argv after it.
func plus(more ...string) []string { return append(append([]string{}, theShape...), more...) }

// with is theShape with each old value replaced by its new one ("" drops
// the flag and the value; a new value with a tab adds rows after it).
func with(t *testing.T, edits map[string]string) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(theShape); i += 2 {
		flag, v := theShape[i], theShape[i+1]
		nv, ok := edits[v]
		if !ok {
			out = append(out, flag, v)
			continue
		}
		seen[v] = true
		for _, part := range strings.Split(nv, "\t") {
			if part == "" {
				continue
			}
			out = append(out, flag, part)
		}
	}
	for k := range edits {
		if !seen[k] {
			t.Fatalf("no %q in the shape", k)
		}
	}
	return out
}

// TestEveryWayBackToAnotherShapeIsCaught: round 1's mutations and its
// review's (2026-10-27), each a way the old shapes, or a wedging
// controller, could come back.
func TestEveryWayBackToAnotherShapeIsCaught(t *testing.T) {
	const (
		ehci   = "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0"
		kbd    = "usb-kbd,bus=ehci.0"
		tablet = "usb-tablet,bus=ehci.0"
		oc     = "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10"
		ocd    = "id=opencore,if=none,format=raw,snapshot=on,file=${data.x.fw.opencore_image}"
		vga    = "VGA,vgamem_mb=64"
	)
	for _, tc := range []struct {
		name    string
		machine string
		edits   map[string]string
		argv    []string
	}{
		{name: "usb=on in the machine", machine: "q35,usb=on,vmport=off"},
		{name: "a bare -usb", argv: append(append([]string{}, theShape...), "-usb")},
		{name: "-machine usb=on in the argv", argv: append(append([]string{}, theShape...), "-machine", "q35,usb=on")},
		{name: "-usbdevice", argv: append(append([]string{}, theShape...), "-usbdevice", "tablet")},
		{name: "a UHCI companion", edits: map[string]string{vga: "ich9-usb-uhci1,masterbus=ehci.0,firstport=0\t" + vga}},
		{name: "a UHCI controller", edits: map[string]string{vga: "ich9-usb-uhci1,id=u2\t" + vga}},
		{name: "a UHCI row with an interpolated id", edits: map[string]string{vga: "ich9-usb-uhci1,id=u${var.cpus}\t" + vga}},
		{name: "a one-string -device row", argv: append(append([]string{}, theShape...), "-device ich9-usb-uhci1,id=u2")},
		{name: "a -device whose model interpolates", edits: map[string]string{vga: "${var.usb},id=u2\t" + vga}},
		{name: "an XHCI controller", edits: map[string]string{vga: "qemu-xhci,id=usb\t" + vga}},
		{name: "an OHCI controller", edits: map[string]string{vga: "pci-ohci,id=o\t" + vga}},
		{name: "EHCI is UHCI", edits: map[string]string{ehci: "ich9-usb-uhci1,id=ehci,bus=pcie.0,addr=0x1d.0"}},
		{name: "EHCI in the first free slot, sharing the NIC's line", edits: map[string]string{ehci: "ich9-usb-ehci1,id=ehci"}},
		{name: "EHCI at Mavericks' old 0x1d.7", edits: map[string]string{ehci: "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.7"}},
		{name: "EHCI as a multifunction device", edits: map[string]string{ehci: "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0,multifunction=on"}},
		{name: "another device in EHCI's slot", edits: map[string]string{vga: "VGA,vgamem_mb=64,bus=pcie.0,addr=0x1d.1"}},
		{name: "EHCI is ich9-usb-ehci2", edits: map[string]string{ehci: "ich9-usb-ehci2,id=ehci,bus=pcie.0,addr=0x1d.0"}},
		{name: "EHCI with a companion's masterbus", edits: map[string]string{ehci: "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0,masterbus=x.0"}},
		{name: "no EHCI", edits: map[string]string{ehci: ""}},
		{name: "two EHCI", edits: map[string]string{ehci: ehci + "\tich9-usb-ehci1,id=ehci2"}},
		{name: "EHCI's id duplicates another's", edits: map[string]string{ehci: "ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0\tVGA,id=ehci"}},
		{name: "no keyboard", edits: map[string]string{kbd: ""}},
		{name: "two keyboards", edits: map[string]string{kbd: kbd + "\t" + kbd}},
		{name: "the keyboard on another bus", edits: map[string]string{kbd: "usb-kbd,bus=usb.0"}},
		{name: "the keyboard on a port", edits: map[string]string{kbd: "usb-kbd,bus=ehci.0,port=1"}},
		{name: "the tablet is a mouse", edits: map[string]string{tablet: "usb-mouse,bus=ehci.0"}},
		{name: "a mouse beside the tablet", edits: map[string]string{tablet: tablet + "\tusb-mouse,bus=ehci.0"}},
		{name: "a virtio tablet", edits: map[string]string{vga: "virtio-tablet-pci\t" + vga}},
		{name: "a virtio keyboard", edits: map[string]string{vga: "virtio-keyboard-pci\t" + vga}},
		{name: "a wacom tablet", edits: map[string]string{tablet: tablet + "\tusb-wacom-tablet,bus=ehci.0"}},
		{name: "OpenCore on usb-storage", edits: map[string]string{oc: "usb-storage,bus=ehci.0,drive=opencore"}},
		{name: "another USB device", edits: map[string]string{vga: "usb-hub,bus=ehci.0\t" + vga}},
		{name: "OpenCore on another bus", edits: map[string]string{oc: "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.1,addr=0x10"}},
		{name: "OpenCore without bootindex", edits: map[string]string{oc: "virtio-blk-pci,drive=opencore"}},
		{name: "OpenCore in the first free slot", edits: map[string]string{oc: "virtio-blk-pci,drive=opencore,bootindex=0"}},
		{name: "OpenCore in slot 5, on the NIC's line", edits: map[string]string{oc: "virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x5"}},
		{name: "another device in OpenCore's slot", edits: map[string]string{vga: "VGA,vgamem_mb=64,bus=pcie.0,addr=0x10.1"}},
		{name: "OpenCore on AHCI, which 10.6 calls unreadable", edits: map[string]string{oc: "ide-hd,bus=ide.2,drive=opencore,bootindex=0"}},
		{name: "OpenCore as a CD", edits: map[string]string{oc: "ide-cd,bus=ide.2,drive=opencore,bootindex=0"}},
		{name: "OpenCore on virtio-scsi", edits: map[string]string{oc: "scsi-hd,drive=opencore,bootindex=0"}},
		{name: "OpenCore's drive writable", edits: map[string]string{ocd: "id=opencore,if=none,format=raw,file=${data.x.fw.opencore_image}"}},
		{name: "no OpenCore drive", edits: map[string]string{ocd: ""}},
		{name: "another virtio device", edits: map[string]string{vga: "virtio-blk-pci,drive=target\t" + vga}},
		{name: "another disk with a bootindex", edits: map[string]string{"ide-hd,bus=ide.0,drive=target": "ide-hd,bus=ide.0,drive=target,bootindex=1"}},
		// The final review's (2026-10-28) survivors and near-misses.
		{name: "-M usb=on with no machine type", argv: plus("-M", "usb=on")},
		{name: "-machine usb=on with no machine type", argv: plus("-machine", "usb=on")},
		{name: "-machine usb=on after a type", argv: plus("-machine", "q35,usb=on")},
		{name: "a value that is a traversal, not a literal or a template", argv: plus("-device", NotLiteral+"local.install_log")},
		{name: "an implicit virtio-blk from -drive if=virtio", argv: plus("-drive", "if=virtio,format=raw,snapshot=on,file=x")},
		{name: "an implicit IDE disk from -drive if=ide", argv: plus("-drive", "if=ide,format=raw,file=x")},
		{name: "an implicit disk from -drive with no if=", argv: plus("-drive", "format=raw,file=x")},
		{name: "an implicit SCSI disk", argv: plus("-drive", "if=scsi,file=x")},
		{name: "-hda", argv: plus("-hda", "x")},
		{name: "-cdrom", argv: plus("-cdrom", "x")},
		{name: "-fda", argv: plus("-fda", "x")},
		{name: "a device in an ICH9 slot, beside EHCI's line", edits: map[string]string{vga: "e1000-82545em,netdev=n2,bus=pcie.0,addr=0x1e.0\t" + vga}},
		{name: "a device in slot 0x1d's other function", edits: map[string]string{vga: "VGA,vgamem_mb=64,bus=pcie.0,addr=0x1d.1"}},
		{name: "VGA in OpenCore's slot", edits: map[string]string{vga: "VGA,vgamem_mb=64,bus=pcie.0,addr=0x10.0"}},
		{name: "a second usb-kbd", edits: map[string]string{kbd: kbd + "\t" + kbd}},
		{name: "virtio-rng", edits: map[string]string{vga: "virtio-rng-pci\t" + vga}},
		// The re-review's (2026-10-28): a -device QEMU reads by key or JSON,
		// and a machine the check cannot read.
		{name: "a -device by driver= key", argv: plus("-device", "driver=usb-storage,bus=ehci.0,drive=x")},
		{name: "a -device as JSON", argv: plus("-device", `{"driver":"qemu-xhci","id":"x"}`)},
		{name: "a -machine value that interpolates", argv: plus("-machine", "q35,${var.machine_opts}")},
		{name: "a machine_type that interpolates", machine: "q35,${var.machine_opts}"},
		{name: "a machine_type that is a traversal", machine: NotLiteral + "local.machine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.machine
			if m == "" {
				m = "q35,vmport=off"
			}
			argv := tc.argv
			if argv == nil {
				argv = theShape
				if tc.edits != nil {
					argv = with(t, tc.edits)
				}
			}
			if errs := MachineShapeErrors(m, argv); len(errs) == 0 {
				t.Errorf("not caught: %q %q", m, argv)
			}
		})
	}
}

// theBox is a box's argv in the one shape: its firmware pair, the varstore
// snapshot=on, then the devices.
var theBox = append([]string{
	"-parallel", "none",
	"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/b/OVMF_CODE.fd",
	"-drive", "if=pflash,format=raw,unit=1,snapshot=on,file=/b/OVMF_VARS.fd",
}, theShape[8:]...)

func TestTheBoxShapeHasNoErrors(t *testing.T) {
	if errs := BoxShapeErrors("q35,vmport=off,accel=kvm", theBox); len(errs) != 0 {
		t.Errorf("the box shape itself: %q", errs)
	}
}

// TestABoxsFirmwareIsReadOnly: a box boots the pristine varstore with
// snapshot=on and the firmware read-only, so no boot writes either.
func TestABoxsFirmwareIsReadOnly(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{"a writable varstore", "if=pflash,format=raw,unit=1,snapshot=on,file=/b/OVMF_VARS.fd", "if=pflash,format=raw,unit=1,file=/b/OVMF_VARS.fd"},
		{"a writable firmware", "if=pflash,format=raw,unit=0,readonly=on,file=/b/OVMF_CODE.fd", "if=pflash,format=raw,unit=0,file=/b/OVMF_CODE.fd"},
		{"no varstore", "if=pflash,format=raw,unit=1,snapshot=on,file=/b/OVMF_VARS.fd", "if=none,id=x,file=/b/OVMF_VARS.fd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := append([]string{}, theBox...)
			for i := range argv {
				if argv[i] == tc.old {
					argv[i] = tc.new
				}
			}
			if errs := BoxShapeErrors("q35,vmport=off", argv); len(errs) == 0 {
				t.Errorf("not caught: %q", argv)
			}
		})
	}
}
