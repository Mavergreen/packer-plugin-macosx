package firmware

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file check the embedded assets/firmware/config.plist,
// read with a small property-list decoder (parsePlist below). The
// SMBIOS default is not repeated here: TestTheDefaultIsWhatConfigPlistShips
// (smbios_test.go) already holds it.

// parsePlist decodes an Apple XML property list's root <dict> into plain
// Go values: string, int64, bool, map[string]any (dict) and []any
// (array). Only the shapes config.plist actually uses are handled; any
// other element (<real>, <date>, <data>, ...) is skipped, not decoded.
func parsePlist(t *testing.T, b []byte) map[string]any {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("config.plist does not parse as XML: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "plist" {
			break
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("config.plist: %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		v := decodePlistValue(t, dec, se)
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("config.plist's root value is not a dict")
		}
		return m
	}
}

func decodePlistValue(t *testing.T, dec *xml.Decoder, start xml.StartElement) any {
	t.Helper()
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		var key string
		haveKey := false
		for {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("config.plist: %v", err)
			}
			switch tt := tok.(type) {
			case xml.StartElement:
				if tt.Name.Local == "key" {
					var k string
					if err := dec.DecodeElement(&k, &tt); err != nil {
						t.Fatalf("config.plist: %v", err)
					}
					key, haveKey = k, true
					continue
				}
				if !haveKey {
					t.Fatalf("config.plist: a dict value with no preceding key")
				}
				m[key] = decodePlistValue(t, dec, tt)
				haveKey = false
			case xml.EndElement:
				if tt.Name.Local == "dict" {
					return m
				}
			}
		}
	case "array":
		var arr []any
		for {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("config.plist: %v", err)
			}
			switch tt := tok.(type) {
			case xml.StartElement:
				arr = append(arr, decodePlistValue(t, dec, tt))
			case xml.EndElement:
				if tt.Name.Local == "array" {
					return arr
				}
			}
		}
	case "string":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			t.Fatalf("config.plist: %v", err)
		}
		return s
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			t.Fatalf("config.plist: %v", err)
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("config.plist: integer %q: %v", s, err)
		}
		return n
	case "true", "false":
		if err := dec.Skip(); err != nil {
			t.Fatalf("config.plist: %v", err)
		}
		return start.Name.Local == "true"
	default:
		if err := dec.Skip(); err != nil {
			t.Fatalf("config.plist: %v", err)
		}
		return nil
	}
}

// plistPath walks nested dicts, key by key, failing the test (not
// panicking) the moment a step is missing or is not itself a dict.
func plistPath(t *testing.T, d map[string]any, keys ...string) any {
	t.Helper()
	var v any = d
	for i, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("config.plist: %s is not a dict", strings.Join(keys[:i], ">"))
		}
		v, ok = m[k]
		if !ok {
			t.Fatalf("config.plist: no key %q under %s", k, strings.Join(keys[:i], ">"))
		}
	}
	return v
}

// "our config.plist exists and is a readable plist"
func TestConfigPlistIsAReadablePropertyList(t *testing.T) {
	if d := parsePlist(t, embeddedConfig(t)); len(d) == 0 {
		t.Fatal("config.plist parsed as an empty dict")
	}
}

// "the HFS+ driver is OpenHfsPlus, never Apple's HfsPlus" (docs/decisions/0002:
// whole names, not substrings -- "OpenHfsPlus.efi" contains "HfsPlus.efi").
func TestConfigPlistUsesOpenHfsPlusNeverApplesHfsPlus(t *testing.T) {
	d := parsePlist(t, embeddedConfig(t))
	drivers, ok := plistPath(t, d, "UEFI", "Drivers").([]any)
	if !ok {
		t.Fatal("UEFI>Drivers is not an array")
	}
	seenOurs := false
	for _, e := range drivers {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		path, _ := entry["Path"].(string)
		switch path {
		case "":
			continue
		case "HfsPlus.efi", "HfsPlusLegacy.efi":
			t.Errorf("config.plist loads Apple's %s driver", path)
		case "OpenHfsPlus.efi":
			seenOurs = true
		}
	}
	if !seenOurs {
		t.Error("config.plist never loads OpenHfsPlus.efi")
	}
}

// "SecureBootModel is disabled, since 10.9 predates it"
func TestConfigPlistDisablesSecureBoot(t *testing.T) {
	d := parsePlist(t, embeddedConfig(t))
	if got := plistPath(t, d, "Misc", "Security", "SecureBootModel"); got != "Disabled" {
		t.Errorf("SecureBootModel = %v, want Disabled -- 10.9 predates Secure Boot", got)
	}
}

// "ScanPolicy admits only HFS+ on SATA, so the picker has exactly one
// entry": without the right bits, OpenCore lists its own image as a boot
// entry, defaults to it, times out into it and hangs -- the bug that made
// the guest need a keypress to boot.
func TestConfigPlistScanPolicyAdmitsOnlyHFSOnSATA(t *testing.T) {
	d := parsePlist(t, embeddedConfig(t))
	policy, ok := plistPath(t, d, "Misc", "Security", "ScanPolicy").(int64)
	if !ok {
		t.Fatal("ScanPolicy is not an integer")
	}
	if policy == 0 {
		t.Fatal("ScanPolicy 0 scans everything")
	}
	// OC_SCAN_FILE_SYSTEM_LOCK and OC_SCAN_DEVICE_LOCK: without the lock
	// bits the allow bits mean nothing, and OpenCore rejects an allow bit
	// whose lock is missing ("Invalid ScanPolicy").
	const (
		fsLock    = 0x1
		devLock   = 0x2
		allowHFS  = 0x200   // OC_SCAN_ALLOW_FS_HFS -- the macOS volume.
		allowSATA = 0x10000 // OC_SCAN_ALLOW_DEVICE_SATA -- q35's ide-hd presents as SATA.
		allowUSB  = 0x80000 // the OpenCore image itself lives on usb-storage.
	)
	for name, bit := range map[string]int64{
		"OC_SCAN_FILE_SYSTEM_LOCK":  fsLock,
		"OC_SCAN_DEVICE_LOCK":       devLock,
		"OC_SCAN_ALLOW_FS_HFS":      allowHFS,
		"OC_SCAN_ALLOW_DEVICE_SATA": allowSATA,
	} {
		if policy&bit == 0 {
			t.Errorf("ScanPolicy %#x lacks %s (%#x)", policy, name, bit)
		}
	}
	if policy&allowUSB != 0 {
		t.Errorf("ScanPolicy %#x allows usb-storage, where the OpenCore image itself lives -- admitting it hangs the guest", policy)
	}
}

// "every enabled kext in Kernel>Add is one we have pinned": Kexts is the
// table already checked against the embedded pin
// registry (TestEveryPinIsInTheEmbeddedRegistryWithItsCommit), so
// checking against it answers the same question -- is every kext
// config.plist loads one this repository actually pins.
func TestConfigPlistEnabledKextsArePinned(t *testing.T) {
	d := parsePlist(t, embeddedConfig(t))
	add, ok := plistPath(t, d, "Kernel", "Add").([]any)
	if !ok {
		t.Fatal("Kernel>Add is not an array")
	}
	pinned := map[string]bool{}
	for _, k := range Kexts {
		pinned[k.Name] = true
	}
	seen := 0
	for _, e := range add {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if enabled, _ := entry["Enabled"].(bool); !enabled {
			continue
		}
		bundle, _ := entry["BundlePath"].(string)
		name := strings.TrimSuffix(bundle, ".kext")
		if !pinned[name] {
			t.Errorf("kext not pinned: %s", bundle)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("config.plist enables no kext in Kernel>Add")
	}
}
