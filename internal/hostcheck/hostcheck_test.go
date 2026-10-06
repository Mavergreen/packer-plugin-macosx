package hostcheck

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const (
	intelCPU = "processor\t: 0\nvendor_id\t: GenuineIntel\nflags\t\t: fpu vme vmx sse4_2 ept\n\nprocessor\t: 1\nvendor_id\t: GenuineIntel\nflags\t\t: fpu\n"
	amdCPU   = "processor\t: 0\nvendor_id\t: AuthenticAMD\nflags\t\t: fpu svm sse4_2\n"
	noVMXCPU = "processor\t: 0\nvendor_id\t: GenuineIntel\nflags\t\t: fpu sse4_2 hypervisor\n"
	noSVMCPU = "processor\t: 0\nvendor_id\t: AuthenticAMD\nflags\t\t: fpu sse4_2 hypervisor\n"
)

// fake is a Linux host with this cpuinfo text and a /dev/kvm that exists
// or not, and is writable or not; it reads nothing of the machine running
// the test.
func fake(t *testing.T, cpuinfo string, kvmExists, kvmWritable bool) Host {
	t.Helper()
	return Host{
		GOOS: "linux",
		ReadFile: func(p string) ([]byte, error) {
			if p == "/proc/cpuinfo" {
				return []byte(cpuinfo), nil
			}
			t.Errorf("read %s, which hostcheck has no business reading", p)
			return nil, os.ErrNotExist
		},
		Exists:   func(p string) bool { return p == KVMDevice && kvmExists },
		Writable: func(p string) bool { return p == KVMDevice && kvmWritable },
	}
}

func fact(t *testing.T, facts []Fact, check string) Fact {
	t.Helper()
	for _, f := range facts {
		if f.Check == check {
			return f
		}
	}
	t.Fatalf("no %s fact in %+v", check, facts)
	return Fact{}
}

func TestCPUInfoTakesTheFirstVendorAndFlags(t *testing.T) {
	vendor, flags := CPUInfo(intelCPU)
	if vendor != "GenuineIntel" || flags != "fpu vme vmx sse4_2 ept" {
		t.Fatalf("CPUInfo = %q, %q", vendor, flags)
	}
}

func TestAnIntelHostWithVTxAndAWritableKVMPasses(t *testing.T) {
	h := fake(t, intelCPU, true, true)
	for _, f := range Facts(h) {
		if !f.OK {
			t.Errorf("%+v fails", f)
		}
	}
	if err := CheckHost(h); err != nil {
		t.Fatalf("CheckHost = %v, want nil", err)
	}
}

// An AMD host builds and runs the guest (measured 2026-10-06 on GitHub's AMD
// EPYC runners, Mavergreen/mavericks-vm Actions runs 37519070682 and 37524541544), as long as
// the guest's CPU says GenuineIntel, which the templates' default cpu does.
func TestAnAMDHostWithAMDVAndAWritableKVMPasses(t *testing.T) {
	h := fake(t, amdCPU, true, true)
	for _, f := range Facts(h) {
		if !f.OK {
			t.Errorf("%+v fails", f)
		}
	}
	if f := fact(t, Facts(h), "cpu-vendor"); !strings.Contains(f.Detail, "vendor=GenuineIntel") {
		t.Errorf("cpu-vendor = %+v, want it to say the guest needs vendor=GenuineIntel", f)
	}
	if err := CheckHost(h); err != nil {
		t.Fatalf("CheckHost = %v, want nil", err)
	}
}

// AMD's virtualization is svm (AMD-V), never vmx: asking an AMD host for VT-x
// would refuse every one of them.
func TestAnAMDHostWithoutSVMIsRefused(t *testing.T) {
	err := CheckHost(fake(t, noSVMCPU, true, true))
	if err == nil || !strings.Contains(err.Error(), "virtualization: no AMD-V") || strings.Contains(err.Error(), "cpu-vendor") {
		t.Fatalf("CheckHost = %v, want it to name AMD-V alone", err)
	}
}

func TestAnIntelHostWithoutVMXIsRefused(t *testing.T) {
	h := fake(t, noVMXCPU, true, true)
	if f := fact(t, Facts(h), "cpu-vendor"); !f.OK {
		t.Fatalf("cpu-vendor = %+v", f)
	}
	err := CheckHost(h)
	if err == nil || !strings.Contains(err.Error(), "virtualization: no VT-x") || strings.Contains(err.Error(), "cpu-vendor") {
		t.Fatalf("CheckHost = %v, want it to name VT-x alone", err)
	}
}

func TestAnUnrecognisedVendorIsRefused(t *testing.T) {
	err := CheckHost(fake(t, "vendor_id\t: CentaurHauls\nflags\t\t: vmx\n", true, true))
	if err == nil || !strings.Contains(err.Error(), "unrecognised CPU vendor: CentaurHauls") {
		t.Fatalf("CheckHost = %v", err)
	}
}

func TestTheKVMDevice(t *testing.T) {
	for _, tc := range []struct {
		name             string
		exists, writable bool
		want             string
	}{
		{"missing", false, false, "/dev/kvm does not exist"},
		{"not writable", true, false, "/dev/kvm exists but is not writable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckHost(fake(t, intelCPU, tc.exists, tc.writable))
			if err == nil || !strings.Contains(err.Error(), "kvm-device: "+tc.want) {
				t.Fatalf("CheckHost = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestAnotherOSSaysTheBuildNeedsLinuxAndProbesNothing(t *testing.T) {
	for _, goos := range []string{"darwin", "netbsd"} {
		h := Host{
			GOOS:     goos,
			ReadFile: func(p string) ([]byte, error) { t.Fatalf("read %s on %s", p, goos); return nil, nil },
			Exists:   func(p string) bool { t.Fatalf("stat %s on %s", p, goos); return false },
			Writable: func(p string) bool { t.Fatalf("access %s on %s", p, goos); return false },
		}
		if f := Facts(h); f != nil {
			t.Fatalf("Facts on %s = %+v, want none", goos, f)
		}
		err := CheckHost(h)
		if !errors.Is(err, ErrNeedsLinux) || !strings.Contains(err.Error(), goos) || !strings.Contains(err.Error(), "needs Linux today") {
			t.Fatalf("CheckHost on %s = %v", goos, err)
		}
	}
}
