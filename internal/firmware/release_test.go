package firmware

import (
	"context"
	"io/fs"
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
// mig_table_max_displ under KVM, its own default model, and the
// description naming the OS -- and nothing else. DevirtualiseMmio stays
// off: on, the installer's bless panicked writing NVRAM (both MEASURED
// 2026-10-04).
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
		return s
	}
	if normalize(mav) != normalize(sl) {
		t.Fatal("10.6's config.plist differs from 10.9's beyond the documented changes")
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
