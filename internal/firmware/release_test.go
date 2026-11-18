package firmware

import (
	"context"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	macosx "github.com/Mavergreen/packer-plugin-macosx"
)

func releasePlist(t *testing.T, r Release) []byte {
	t.Helper()
	b, err := fs.ReadFile(macosx.Files, r.ConfigPlist)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// boolKey is the <true/> or <false/> after <key>name</key>.
func boolKey(plist []byte, name string) string {
	m := regexp.MustCompile(`<key>` + name + `</key>\s*<(true|false)/>`).FindSubmatch(plist)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// TestSnowLeopardConfigDiffersOnlyAsDocumented: 10.6's config.plist is
// 10.9's with exactly the changes assets/firmware/README.md explains --
// RebuildAppleMemoryMap, which 10.6.0's kernel needs to get past
// mig_table_max_displ under KVM, its own default model, the
// description naming the OS, and 10.6.8's one Kernel>Patch
// (TestSnowLeopardSuspendsLockTimeoutsUnderAHypervisor holds what it
// is) -- and nothing else. DevirtualiseMmio stays off: on, the
// installer's bless panicked writing NVRAM (both MEASURED 2026-10-04).
func TestSnowLeopardConfigDiffersOnlyAsDocumented(t *testing.T) {
	mav, sl := releasePlist(t, Mavericks), releasePlist(t, SnowLeopard)
	if boolKey(mav, "RebuildAppleMemoryMap") != "false" || boolKey(sl, "RebuildAppleMemoryMap") != "true" {
		t.Errorf("RebuildAppleMemoryMap: Mavericks %s, Snow Leopard %s; want false and true", boolKey(mav, "RebuildAppleMemoryMap"), boolKey(sl, "RebuildAppleMemoryMap"))
	}
	if boolKey(sl, "DevirtualiseMmio") != "false" {
		t.Error("DevirtualiseMmio is on for 10.6: bless panics writing NVRAM with it")
	}
	if got := ProductName(sl); got != SnowLeopard.DefaultSMBIOS {
		t.Errorf("10.6's config.plist names %s; want its default model %s", got, SnowLeopard.DefaultSMBIOS)
	}
	normalize := func(b []byte) string {
		s := string(b)
		s = regexp.MustCompile(`(<key>RebuildAppleMemoryMap</key>\s*)<(true|false)/>`).ReplaceAllString(s, "${1}<X/>")
		s = strings.Replace(s, "<string>"+ProductName(b)+"</string>", "<string>MODEL</string>", 1)
		s = regexp.MustCompile(`for (OS X 10\.9\.5|Mac OS X 10\.6)`).ReplaceAllString(s, "for OS")
		// Kernel>Patch is the first Patch after <key>Kernel</key>: 10.9's
		// empty array, 10.6's one entry.
		if k := strings.Index(s, "<key>Kernel</key>"); k >= 0 {
			if loc := regexp.MustCompile(`(?s)<key>Patch</key>\s*(<array/>|<array>.*?</array>)`).FindStringIndex(s[k:]); loc != nil {
				s = s[:k+loc[0]] + "<key>Patch</key><X/>" + s[k+loc[1]:]
			}
		}
		return s
	}
	if n := len(kernelPatches(t, mav)); n != 0 {
		t.Errorf("10.9's Kernel>Patch has %d entries; want none", n)
	}
	if normalize(mav) != normalize(sl) {
		t.Fatal("10.6's config.plist differs from 10.9's beyond the documented changes")
	}
}

// TestEveryReleaseShrinksTheRTCToTheBankQEMUHas: QEMU's DSDT gives the
// RTC (PNP0B00) ports 0x70 through 0x77, but QEMU answers only 0x70 and
// 0x71, one 128-byte CMOS bank. Told of eight ports, AppleRTC reads its
// sleep trace point from a second bank nothing answers, gets all ones,
// and every boot reports a sleep failure with every PCI device at fault:
// 10.6's configd then indexes past the end of PCITopLevel and crashes
// until it happens to survive, so the guest often never configures its
// network (MEASURED 2026-10-10, docs/decisions/0015). Every release carries the one DSDT patch that
// shortens the RTC's I/O descriptor to two ports, and nothing else in
// ACPI>Patch.
func TestEveryReleaseShrinksTheRTCToTheBankQEMUHas(t *testing.T) {
	for _, r := range []Release{Mavericks, SnowLeopard} {
		patches, ok := plistPath(t, parsePlist(t, releasePlist(t, r)), "ACPI", "Patch").([]any)
		if !ok || len(patches) != 1 {
			t.Errorf("%s: ACPI>Patch has %d entries; want exactly the RTC one", r.Name, len(patches))
			continue
		}
		p, _ := patches[0].(map[string]any)
		// IO (Decode16, 0x0070, 0x0070, 0x01, Length): the length is the last byte.
		want := map[string]any{
			"Enabled":        true,
			"TableSignature": []byte("DSDT"),
			"Find":           []byte{0x47, 0x01, 0x70, 0x00, 0x70, 0x00, 0x01, 0x08},
			"Replace":        []byte{0x47, 0x01, 0x70, 0x00, 0x70, 0x00, 0x01, 0x02},
			"Count":          int64(1),
			"Skip":           int64(0),
			"Limit":          int64(0),
			"Mask":           []byte{},
			"ReplaceMask":    []byte{},
		}
		for k, v := range want {
			if !reflect.DeepEqual(p[k], v) {
				t.Errorf("%s: ACPI>Patch[0]>%s = %v; want %v", r.Name, k, p[k], v)
			}
		}
	}
}

func kernelPatches(t *testing.T, plist []byte) []any {
	t.Helper()
	patches, ok := plistPath(t, parsePlist(t, plist), "Kernel", "Patch").([]any)
	if !ok && plistPath(t, parsePlist(t, plist), "Kernel", "Patch") != nil {
		t.Fatal("Kernel>Patch is not an array")
	}
	return patches
}

// TestSnowLeopardSuspendsLockTimeoutsUnderAHypervisor: 10.6.8's kernel
// (xnu-1504.15.3, Darwin 10.8.0) gets the term xnu-1699 (10.7) added to
// machine_timeout_suspended(), `virtualized`, the CPUID VMM bit: under a
// hypervisor its spinlock, TLB-shootdown and wait-queue timeouts keep
// waiting instead of panicking, as 10.7 through 10.9 do. Without it, two
// vCPUs on a busy host panic 10.6.8 with "Spinlock acquisition timed
// out" (F26, docs/decisions/0046). The patch rewrites the 63-byte
// function in place, same length: the VMM test (bit 7 of the last byte
// of the kernel's own cpuid_cpu_info.cpuid_features) first, then the
// four stock terms with the same targets. Only the x86_64 kernel of
// Darwin 10.8.0 is patched: 10.6.0's kernel has no such function, and
// 10.9's has the term already.
func TestSnowLeopardSuspendsLockTimeoutsUnderAHypervisor(t *testing.T) {
	patches := kernelPatches(t, releasePlist(t, SnowLeopard))
	if len(patches) != 1 {
		t.Fatalf("10.6's Kernel>Patch has %d entries; want exactly the machine_timeout_suspended one", len(patches))
	}
	p, _ := patches[0].(map[string]any)
	find := []byte{
		0x55, 0x48, 0x89, 0xe5, // push rbp; mov rbp,rsp
		0xe8, 0x0d, 0x35, 0x00, 0x00, 0x85, 0xc0, 0x75, 0x2b, // mp_recent_debugger_activity()
		0xe8, 0x47, 0x8c, 0xfd, 0xff, 0x85, 0xc0, 0x75, 0x22, // panic_active()
		0x48, 0x8d, 0x05, 0x47, 0x91, 0x61, 0x00, 0x8b, 0x00, 0x85, 0xc0, 0x75, 0x15, // pmap_tlb_flush_timeout
		0x48, 0x8d, 0x05, 0xb6, 0x43, 0x65, 0x00, 0x48, 0x8b, 0x00, 0x48, 0x85, 0xc0, // spinlock_timed_out
		0x0f, 0x95, 0xc0, 0x0f, 0xb6, 0xc0, 0xeb, 0x05,
		0xb8, 0x01, 0x00, 0x00, 0x00, 0xc9, 0xc3, // return TRUE; leave; ret
	}
	replace := []byte{
		0x55, 0x48, 0x89, 0xe5,
		0xf6, 0x05, 0x0c, 0xf1, 0x68, 0x00, 0x80, 0x75, 0x2b, // virtualized: test byte [cpuid_features+7],0x80
		0xe8, 0x04, 0x35, 0x00, 0x00, 0x85, 0xc0, 0x75, 0x22, // mp_recent_debugger_activity()
		0xe8, 0x3e, 0x8c, 0xfd, 0xff, 0x85, 0xc0, 0x75, 0x19, // panic_active()
		0x8b, 0x05, 0x3f, 0x91, 0x61, 0x00, 0x85, 0xc0, 0x75, 0x0f, // pmap_tlb_flush_timeout
		0x31, 0xc0, 0x48, 0x83, 0x3d, 0xad, 0x43, 0x65, 0x00, 0x00, 0x0f, 0x95, 0xc0, 0xeb, 0x05, // spinlock_timed_out != 0
		0xb8, 0x01, 0x00, 0x00, 0x00, 0xc9, 0xc3,
	}
	want := map[string]any{
		"Enabled":     true,
		"Identifier":  "kernel",
		"Arch":        "x86_64",
		"Base":        "_machine_timeout_suspended",
		"MinKernel":   "10.8.0",
		"MaxKernel":   "10.8.0",
		"Find":        find,
		"Replace":     replace,
		"Count":       int64(1),
		"Skip":        int64(0),
		"Limit":       int64(0),
		"Mask":        []byte{},
		"ReplaceMask": []byte{},
	}
	for k, v := range want {
		if !reflect.DeepEqual(p[k], v) {
			t.Errorf("10.6's Kernel>Patch[0]>%s = %v; want %v", k, p[k], v)
		}
	}
	if len(find) != 63 || len(replace) != len(find) {
		t.Errorf("Find is %d bytes and Replace %d; want the function's 63, both", len(find), len(replace))
	}
	if c, _ := p["Comment"].(string); !strings.Contains(c, "docs/decisions/0046") {
		t.Errorf("Comment %q does not name docs/decisions/0046", c)
	}
}

func TestSnowLeopardDefaultModel(t *testing.T) {
	if SnowLeopard.DefaultSMBIOS != "iMac9,1" {
		t.Fatalf("default %q; want iMac9,1, the model 10.6.0 installed with (MEASURED 2026-10-04)", SnowLeopard.DefaultSMBIOS)
	}
	if s, _ := SnowLeopard.SMBIOSVerdict("iMac9,1"); s == "UNLISTED" {
		t.Fatal("the default model is not in 10.6's table")
	}
	if s, _ := SnowLeopard.SMBIOSVerdict("iMac14,2"); s != "UNLISTED" {
		t.Fatalf("iMac14,2 is %s for 10.6; it postdates 10.6.0 and is not in its table", s)
	}
}

func TestMavericksIsTodaysTable(t *testing.T) {
	if Mavericks.ConfigPlist != "assets/firmware/config.plist" || Mavericks.DefaultSMBIOS != DefaultSMBIOS || len(Mavericks.Models) != len(SMBIOSModels) {
		t.Fatalf("Mavericks = %+v; want today's plist, default and table", Mavericks)
	}
}

func TestEFIImageUsesTheReleasesPlist(t *testing.T) {
	f := newFixture(t)
	shipped(f)
	img, err := f.b.EFIImage(context.Background(), SnowLeopard, "", false)
	if err != nil {
		t.Fatal(err)
	}
	got := readEFI(t, img).files["/EFI/OC/config.plist"]
	if string(got) != string(releasePlist(t, SnowLeopard)) {
		t.Fatal("a default 10.6 image does not carry assets/firmware/snowleopard/config.plist verbatim")
	}
}
