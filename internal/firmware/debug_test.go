package firmware

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDebugSettingsAreTheThreeValues pins the switch itself: exactly
// these three keys, off at OpenCore 1.0.7's Sample.plist values (-v and
// keepsyms=1 stay in boot-args either way), on at what bring-up ran with.
func TestDebugSettingsAreTheThreeValues(t *testing.T) {
	want := []DebugSetting{
		{"<key>AppleDebug</key>", "<false/>", "<true/>"},
		{"<key>DisplayLevel</key>", "<integer>2147483650</integer>", "<integer>2147483714</integer>"},
		{"<key>boot-args</key>", "<string>-v keepsyms=1</string>", "<string>-v keepsyms=1 debug=0x100</string>"},
	}
	if !reflect.DeepEqual(DebugSettings, want) {
		t.Fatalf("DebugSettings = %q, want %q", DebugSettings, want)
	}
}

// valueAfter is the trimmed line after key's line in plist.
func valueAfter(t *testing.T, plist []byte, key string) string {
	t.Helper()
	lines := strings.Split(string(plist), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == key && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	t.Fatalf("no %s", key)
	return ""
}

// TestTheShippedConfigIsDebugOff: a default build ships the repository's
// config.plist verbatim, so debug is off by default exactly when that
// file carries every Off value.
func TestTheShippedConfigIsDebugOff(t *testing.T) {
	for _, s := range DebugSettings {
		if got := valueAfter(t, embeddedConfig(t), s.Key); got != s.Off {
			t.Errorf("assets/firmware/config.plist: %s is %s, want %s (off)", s.Key, got, s.Off)
		}
	}
	if off, err := SetDebug(embeddedConfig(t), false); err != nil || string(off) != string(embeddedConfig(t)) {
		t.Fatalf("turning debug off in the shipped config must change no byte (err %v)", err)
	}
}

// TestSetDebugMatchesItsGoldenByteForByte: each model's config with the
// switch on, byte for byte, and back off again to the model's own golden.
func TestSetDebugMatchesItsGoldenByteForByte(t *testing.T) {
	for m, suffix := range map[string]string{"MacPro5,1": "MacPro5-1", "iMac14,2": "iMac14-2"} {
		base, err := SetProductName(embeddedConfig(t), m)
		if err != nil {
			t.Fatal(err)
		}
		on, err := SetDebug(base, true)
		if err != nil {
			t.Fatal(err)
		}
		if string(on) != golden(t, "debug-plist-set-"+suffix+".plist") {
			t.Errorf("%s: debug on differs from debug-plist-set-%s.plist", m, suffix)
		}
		for _, s := range DebugSettings {
			if got := valueAfter(t, on, s.Key); got != s.On {
				t.Errorf("%s: debug on: %s is %s, want %s", m, s.Key, got, s.On)
			}
		}
		if again, _ := SetDebug(on, true); string(again) != string(on) {
			t.Errorf("%s: turning debug on twice changed bytes", m)
		}
		off, err := SetDebug(on, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(off) != string(base) {
			t.Errorf("%s: debug on then off is not the model's config", m)
		}
	}
}

// TestSetDebugChangesOnlyItsThreeLines: the on and off configs differ in
// exactly the three value lines.
func TestSetDebugChangesOnlyItsThreeLines(t *testing.T) {
	off := strings.Split(string(embeddedConfig(t)), "\n")
	b, err := SetDebug(embeddedConfig(t), true)
	if err != nil {
		t.Fatal(err)
	}
	on := strings.Split(string(b), "\n")
	if len(on) != len(off) {
		t.Fatalf("debug on has %d lines, off %d", len(on), len(off))
	}
	var changed []string
	for i := range off {
		if off[i] != on[i] {
			changed = append(changed, strings.TrimSpace(off[i-1]))
		}
	}
	if want := []string{"<key>AppleDebug</key>", "<key>DisplayLevel</key>", "<key>boot-args</key>"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("debug on changed the values after %q, want %q", changed, want)
	}
}

func TestSetDebugRefusesWhatItCannotDoSafely(t *testing.T) {
	base := string(embeddedConfig(t))
	for name, c := range map[string]struct{ plist, want string }{
		"a value it does not know": {strings.Replace(base, "<integer>2147483650</integer>", "<integer>7</integer>", 1), "neither"},
		"a key twice":              {strings.Replace(base, "<key>AppleDebug</key>", "<key>AppleDebug</key>\n\t\t\t<false/>\n\t\t\t<key>AppleDebug</key>", 1), "more than one <key>AppleDebug</key>"},
		"no key":                   {strings.Replace(base, "<key>boot-args</key>", "<key>boot-arguments</key>", 1), "no <key>boot-args</key>"},
		"key and value on a line":  {strings.Replace(base, "<key>AppleDebug</key>\n\t\t\t<false/>", "<key>AppleDebug</key><false/>", 1), "shares its line"},
	} {
		if _, err := SetDebug([]byte(c.plist), true); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one saying %q", name, err, c.want)
		}
	}
}

// TestEFIImageWithDebugOn: debug on ships SetDebug's config, derived
// under build/config/ and validated, at the default SMBIOS and another.
func TestEFIImageWithDebugOn(t *testing.T) {
	for m, file := range map[string]string{"iMac14,2": "debug-plist-set-iMac14-2.plist", "MacPro5,1": "debug-plist-set-MacPro5-1.plist"} {
		f := newFixture(t)
		shipped(f)
		if err := os.MkdirAll(filepath.Dir(f.b.ocvalidate()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.b.ocvalidate(), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		img, err := f.b.EFIImage(t.Context(), m, true)
		if err != nil {
			t.Fatalf("%s: EFIImage: %v\n%s", m, err, f.log.String())
		}
		want := golden(t, file)
		derived := filepath.Join(f.home, "build", "config", "config-"+m+"-debug.plist")
		if b, _ := os.ReadFile(derived); string(b) != want {
			t.Errorf("%s: %s is not %s", m, derived, file)
		}
		if c := readEFI(t, img); string(c.files["/EFI/OC/config.plist"]) != want {
			t.Errorf("%s: the image's config.plist is not %s", m, file)
		}
		if cs := f.calls(f.b.ocvalidate()); len(cs) != 1 || !reflect.DeepEqual(cs[0].Args, []string{derived}) {
			t.Errorf("%s: ocvalidate calls: %v", m, cs)
		}
		if !strings.Contains(f.log.String(), "debug: on") {
			t.Errorf("%s: the log does not say debug is on:\n%s", m, f.log.String())
		}
	}
}
