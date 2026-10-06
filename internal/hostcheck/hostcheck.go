// Package hostcheck is whether this host can build Mavericks media and
// run the guest at all: an Intel CPU with VT-x or an AMD one with AMD-V,
// and a /dev/kvm this user can write. The media build's privops microVM
// runs under KVM, and so does the guest. On AMD the guest's CPU must say
// GenuineIntel, which the templates' default cpu does
// (docs/host-profile.md G2).
//
// It is Linux-only today, and says so on any other OS rather than
// probing Linux paths there (docs/decisions/0005: the Linux-only checks
// stay behind this seam and the privops backend's). Everything it reads comes
// through Host, so a test describes a machine instead of depending on the
// one it runs on.
package hostcheck

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// KVMDevice is the device QEMU's -enable-kvm opens.
const KVMDevice = "/dev/kvm"

// Host is everything hostcheck reads from the machine.
type Host struct {
	GOOS     string
	ReadFile func(string) ([]byte, error)
	Exists   func(string) bool
	Writable func(string) bool
}

// Real is this host: runtime.GOOS, os.ReadFile, os.Stat, and access(2)
// for write.
func Real() Host {
	return Host{
		GOOS:     runtime.GOOS,
		ReadFile: os.ReadFile,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		Writable: func(p string) bool { return syscall.Access(p, 2) == nil }, // 2 is W_OK
	}
}

// A Fact is one thing hostcheck judged: its name (cpu-vendor,
// virtualization, kvm-device), whether it holds, and what was found, in words.
type Fact struct {
	Check  string
	OK     bool
	Detail string
}

// Facts judges h: on Linux, the CPU vendor and its virtualization (VT-x or
// AMD-V) from /proc/cpuinfo,
// and whether /dev/kvm exists and is writable. On any other OS it probes
// nothing and returns nil; Supported says why.
func Facts(h Host) []Fact {
	if !Supported(h) {
		return nil
	}
	var facts []Fact
	info, _ := h.ReadFile("/proc/cpuinfo")
	vendor, flags := CPUInfo(string(info))
	switch vendor {
	case "GenuineIntel":
		facts = append(facts, Fact{"cpu-vendor", true, "Intel: the documented KVM path"})
		facts = append(facts, virtualization(flags, "vmx", "VT-x"))
	case "AuthenticAMD":
		// KVM gives a guest the host's own vendor unless its -cpu line names
		// one, and 10.9's kernel hangs at once on AuthenticAMD (measured
		// 2026-10-06: Mavergreen/mavericks-vm Actions run 37524541544).
		facts = append(facts, Fact{"cpu-vendor", true, "AMD: the guest's cpu must carry vendor=GenuineIntel, as the templates' default does"})
		facts = append(facts, virtualization(flags, "svm", "AMD-V"))
	default:
		facts = append(facts, Fact{"cpu-vendor", false, "unrecognised CPU vendor: " + vendor})
	}
	switch {
	case h.Writable(KVMDevice):
		facts = append(facts, Fact{"kvm-device", true, KVMDevice + " is writable by this user"})
	case h.Exists(KVMDevice):
		facts = append(facts, Fact{"kvm-device", false, KVMDevice + " exists but is not writable; is this user in group kvm?"})
	default:
		facts = append(facts, Fact{"kvm-device", false, KVMDevice + " does not exist"})
	}
	return facts
}

// virtualization is the fact that this CPU has its vendor's hardware
// virtualization: flag in /proc/cpuinfo's flags, called name.
func virtualization(flags, flag, name string) Fact {
	if strings.Contains(" "+flags+" ", " "+flag+" ") {
		return Fact{"virtualization", true, name + " present"}
	}
	return Fact{"virtualization", false, "no " + name + "; KVM acceleration unavailable"}
}

// Supported is whether hostcheck knows how to judge h's OS at all: Linux
// alone, today.
func Supported(h Host) bool { return h.GOOS == "linux" }

// ErrNeedsLinux is Check's answer on any OS but Linux.
var ErrNeedsLinux = errors.New("the Mavericks media build needs Linux today: its privops microVM boots the host's own Linux kernel under KVM")

// CheckHost is nil when h can build media and run the guest, or else an
// error naming every fact that fails. On an OS other than Linux it is
// ErrNeedsLinux, naming that OS.
func CheckHost(h Host) error {
	if !Supported(h) {
		return fmt.Errorf("this host runs %s: %w", h.GOOS, ErrNeedsLinux)
	}
	var failed []string
	for _, f := range Facts(h) {
		if !f.OK {
			failed = append(failed, f.Check+": "+f.Detail)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("this host cannot build or run the Mavericks guest -- %s", strings.Join(failed, "; "))
	}
	return nil
}

// Check is CheckHost on this host.
func Check() error { return CheckHost(Real()) }

// CPUInfo is the first vendor_id and the first flags line of
// /proc/cpuinfo's text.
func CPUInfo(s string) (vendor, flags string) {
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "vendor_id":
			if vendor == "" {
				vendor = strings.TrimSpace(v)
			}
		case "flags":
			if flags == "" {
				flags = strings.TrimSpace(v)
			}
		}
	}
	return vendor, flags
}
