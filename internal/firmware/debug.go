package firmware

import (
	"bytes"
	"fmt"
	"strings"
)

// DebugSetting is one of the three config.plist values the debug switch
// turns on: the line after Key holds Off in assets/firmware/config.plist
// and On in a debug build. Nothing else in the plist moves.
type DebugSetting struct{ Key, Off, On string }

// DebugSettings are the switch's three settings, in plist order. Off is
// what assets/firmware/config.plist ships; On is what firmware bring-up
// ran with (docs/configuration-register.md §7, assets/firmware/README.md).
//
//   - Misc > Debug > AppleDebug: OpenCore 1.0.7's Sample.plist value is
//     false; true writes boot.efi's own debug log into OpenCore's.
//   - Misc > Debug > DisplayLevel: the sample's 2147483650 (0x80000002,
//     DEBUG_ERROR|DEBUG_WARN) off; 2147483714 (0x80000042, adding
//     DEBUG_INFO) on.
//   - NVRAM boot-args: -v keepsyms=1 off (verbose boot and symbolicated
//     backtraces are how every boot is read, debug or not); debug=0x100
//     added on.
var DebugSettings = []DebugSetting{
	{"<key>AppleDebug</key>", "<false/>", "<true/>"},
	{"<key>DisplayLevel</key>", "<integer>2147483650</integer>", "<integer>2147483714</integer>"},
	{"<key>boot-args</key>", "<string>-v keepsyms=1</string>", "<string>-v keepsyms=1 debug=0x100</string>"},
}

// SetDebug is plist with each of DebugSettings at its On value (on) or
// its Off value, on a fresh slice: the input is not modified. A surgical
// edit, as SetProductName's: each key must appear exactly once, on a line
// of its own, with its value alone on the next line and already one of
// the two the switch knows -- anything else is refused rather than
// overwritten, so an edit to the repository's config.plist that moves
// one of these values cannot be silently undone by a debug build.
func SetDebug(plist []byte, on bool) ([]byte, error) {
	lines := strings.Split(strings.TrimSuffix(string(plist), "\n"), "\n")
	for _, s := range DebugSettings {
		at := -1
		for i, line := range lines {
			if strings.Contains(line, s.Key) {
				if at >= 0 {
					return nil, fmt.Errorf("debug: more than one %s; refusing to guess which one OpenCore reads", s.Key)
				}
				at = i
			}
		}
		if at < 0 {
			return nil, fmt.Errorf("debug: no %s", s.Key)
		}
		if strings.TrimSpace(lines[at]) != s.Key {
			return nil, fmt.Errorf("debug: %s shares its line with something else; refusing to edit it", s.Key)
		}
		if at+1 >= len(lines) {
			return nil, fmt.Errorf("debug: nothing after %s", s.Key)
		}
		value := lines[at+1]
		have := strings.TrimSpace(value)
		if have != s.Off && have != s.On {
			return nil, fmt.Errorf("debug: %s is %s, which is neither %s (off) nor %s (on); refusing to overwrite it", s.Key, have, s.Off, s.On)
		}
		want := s.Off
		if on {
			want = s.On
		}
		lines[at+1] = value[:strings.Index(value, have)] + want
	}
	var out bytes.Buffer
	for _, line := range lines {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
