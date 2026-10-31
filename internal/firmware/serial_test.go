package firmware

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

// The EFI image's FAT volume serial is derived, not random, so the image is
// the same every build. This pins both the answer for the seed EFIImage uses
// and the rule behind it: the first four bytes, little-endian, of
// SHA-256("guest-macosx fat serial " + seed). Until 2026-10-17 the prefix
// was "mavericks fat serial ".
func TestSerialKnownAnswer(t *testing.T) {
	const want = 0x22C4C28E // serial("opencore"), what EFIImage writes
	if got := serial("opencore"); got != want {
		t.Errorf(`serial("opencore") = %#08x, the pinned answer is %#08x`, got, want)
	}
	for _, seed := range []string{"opencore", "x"} {
		s := sha256.Sum256([]byte("guest-macosx fat serial " + seed))
		if got, rule := serial(seed), binary.LittleEndian.Uint32(s[:4]); got != rule {
			t.Errorf("serial(%q) = %#08x, its rule gives %#08x", seed, got, rule)
		}
	}
}
