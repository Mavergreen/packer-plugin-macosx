// Package diskimg writes and reads the disk images this project builds --
// a GUID partition table and a FAT32 filesystem -- as plain files: no loop
// devices, no root, no sgdisk, no mtools. It knows file formats and
// nothing about what goes in them.
//
// Everything it writes is deterministic: the same inputs give the same
// bytes, which sgdisk's and mtools' images (random GUIDs, a random
// serial, current timestamps) never are.
package diskimg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// SectorSize is the only sector size these images use.
const SectorSize = 512

// A GUID in its on-disk byte order: the first three fields
// little-endian, the last two as written.
type GUID [16]byte

// The partition types this project writes.
var (
	TypeEFISystem = MustGUID("C12A7328-F81F-11D2-BA4B-00A0C93EC93B")
	TypeAppleHFS  = MustGUID("48465300-0000-11AA-AA11-00306543ECAC")
)

// ParseGUID reads the textual form, 8-4-4-4-12 hex digits.
func ParseGUID(s string) (GUID, error) {
	var g GUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return g, fmt.Errorf("not a GUID: %q", s)
	}
	var b [16]byte
	if _, err := hex.Decode(b[:], []byte(s[0:8]+s[9:13]+s[14:18]+s[19:23]+s[24:36])); err != nil {
		return g, fmt.Errorf("not a GUID: %q", s)
	}
	return fromText(b), nil
}

// MustGUID is ParseGUID for constants.
func MustGUID(s string) GUID {
	g, err := ParseGUID(s)
	if err != nil {
		panic(err)
	}
	return g
}

// fromText swaps the first three fields between textual (big-endian)
// and on-disk (little-endian) order. It is its own inverse.
func fromText(b [16]byte) GUID {
	return GUID{b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15]}
}

func (g GUID) String() string {
	t := fromText(g)
	h := fmt.Sprintf("%X", t[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// DerivedGUID is a GUID taken from a hash of seed, shaped as a version-4
// (random) GUID: stable across builds, which random GUIDs are not.
func DerivedGUID(seed string) GUID {
	sum := sha256.Sum256([]byte("mavericks diskimg " + seed))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fromText(b)
}
