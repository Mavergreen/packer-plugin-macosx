package diskimg

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// shortSpecial is what an 8.3 name may hold besides A-Z and 0-9.
const shortSpecial = "!#$%&'()-@^_`{}~"

func validShort(c rune) bool {
	return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(shortSpecial, c)
}

// checkLongName refuses a name FAT cannot hold.
func checkLongName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return fmt.Errorf("%q is not a file name", name)
	case strings.ContainsAny(name, `"*/:<>?\|`):
		return fmt.Errorf("%q: FAT names cannot hold any of \"*/:<>?\\|", name)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return fmt.Errorf("%q: FAT names cannot end in a dot or a space", name)
	case len(utf16.Encode([]rune(name))) > 255:
		return fmt.Errorf("%q: longer than 255 UTF-16 units", name)
	}
	for _, r := range name {
		if r < 0x20 {
			return fmt.Errorf("%q: FAT names cannot hold control characters", name)
		}
	}
	return nil
}

// pack83 is an 8.3 entry name: base and ext, space-padded.
func pack83(base, ext string) [11]byte {
	var s [11]byte
	copy(s[:], base+strings.Repeat(" ", 8-len(base))+ext+strings.Repeat(" ", 3-len(ext)))
	return s
}

func allShort(s string) bool {
	for _, c := range s {
		if !validShort(c) {
			return false
		}
	}
	return true
}

// shortName is name's 8.3 entry name in a directory whose short names so
// far are taken, and whether name needs long-name entries as well:
//
//   - a valid upper-case 8.3 name is its own short name (EFI, ACPI);
//   - a name that is valid 8.3 but for its case is upper-cased, and keeps
//     its case in a long name (BOOTx64.efi -> BOOTX64 EFI);
//   - anything else gets a basis name -- upper-cased, spaces and extra dots
//     dropped, other characters an 8.3 name cannot hold made "_" -- cut to
//     leave room for a numeric tail, ~1, ~2, ... (OpenRuntime.efi ->
//     OPENRU~1EFI), as Windows and mtools generate them.
func shortName(name string, taken map[[11]byte]bool) ([11]byte, bool, error) {
	if err := checkLongName(name); err != nil {
		return [11]byte{}, false, err
	}
	up := strings.ToUpper(name)
	base, ext := up, ""
	if i := strings.LastIndexByte(up, '.'); i > 0 {
		base, ext = up[:i], up[i+1:]
	}
	if base != "" && len(base) <= 8 && len(ext) <= 3 && allShort(base) && allShort(ext) {
		if s := pack83(base, ext); !taken[s] {
			return s, up != name, nil
		}
	}
	clean := func(s string) string {
		var b strings.Builder
		for _, c := range s {
			switch {
			case c == ' ' || c == '.':
			case validShort(c):
				b.WriteRune(c)
			default:
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	b, e := clean(base), clean(ext)
	if len(e) > 3 {
		e = e[:3]
	}
	if b == "" {
		b = "_"
	}
	for n := 1; n <= 999999; n++ {
		tail := "~" + strconv.Itoa(n)
		keep := b
		if len(keep) > 8-len(tail) {
			keep = keep[:8-len(tail)]
		}
		if s := pack83(keep+tail, e); !taken[s] {
			return s, true, nil
		}
	}
	return [11]byte{}, false, fmt.Errorf("%q: no free short name", name)
}

// lfnCount is how many long-name entries name takes: 13 UTF-16 units each.
func lfnCount(name string) uint32 {
	return uint32((len(utf16.Encode([]rune(name))) + 12) / 13)
}

// lfnChecksum ties long-name entries to their short entry.
func lfnChecksum(s [11]byte) byte {
	var sum byte
	for _, c := range s {
		sum = (sum>>1 | sum<<7) + c
	}
	return sum
}

// lfnEntries are name's long-name entries, in the order they sit on disk:
// the last part first, flagged 0x40.
func lfnEntries(name string, short [11]byte) [][]byte {
	u := utf16.Encode([]rune(name))
	n := int(lfnCount(name))
	padded := make([]uint16, n*13)
	copy(padded, u)
	if len(u) < len(padded) {
		for i := len(u) + 1; i < len(padded); i++ {
			padded[i] = 0xFFFF // after the 0x0000 terminator
		}
	}
	sum := lfnChecksum(short)
	var out [][]byte
	for i := n; i >= 1; i-- {
		e := make([]byte, dirEntrySize)
		e[0] = byte(i)
		if i == n {
			e[0] |= 0x40
		}
		chunk := padded[(i-1)*13 : i*13]
		for j, c := range chunk {
			var at int
			switch {
			case j < 5:
				at = 1 + 2*j
			case j < 11:
				at = 14 + 2*(j-5)
			default:
				at = 28 + 2*(j-11)
			}
			binary.LittleEndian.PutUint16(e[at:], c)
		}
		e[11] = attrLFN
		e[13] = sum
		out = append(out, e)
	}
	return out
}
