// Package hostcheck is whether this host can build Mavericks media and
// run the guest at all: an Intel CPU (AMD is a known-harder case for
// macOS guests under KVM, and nothing here has run on one), VT-x, and a
// /dev/kvm this user can write. The media build's privops microVM runs
// under KVM, and so does the guest.
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

// A Fact is one thing hostcheck judged: its name (cpu-vendor, vmx,
// kvm-device), whether it holds, and what was found, in words.
type Fact struct {
	Check  string
	OK     bool
	Detail string
}

// Facts judges h: on Linux, the CPU vendor and VT-x from /proc/cpuinfo,
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
	case "AuthenticAMD":
		facts = append(facts, Fact{"cpu-vendor", false, "AMD: a known-harder case for 10.9 under KVM, and untested here; an Intel host is needed"})
	default:
		facts = append(facts, Fact{"cpu-vendor", false, "unrecognised CPU vendor: " + vendor})
	}
	if strings.Contains(" "+flags+" ", " vmx ") {
		facts = append(facts, Fact{"vmx", true, "VT-x present"})
	} else {
		facts = append(facts, Fact{"vmx", false, "no VT-x; KVM acceleration unavailable"})
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
