package payload

import (
	"fmt"
	"strings"
)

// specialChars are the bytes bashQuote backslash-escapes unconditionally,
// wherever they appear in the string. It mirrors bash 5.2.21's own
// `printf '%q'` (checked against the host's bash by
// TestBashQuoteMatchesBash): every value written to firstboot.conf goes
// through this, since postinstall and firstboot.sh source that file as
// shell.
const specialChars = " '\"\\|&;()<>!{}*[?]^$`,"

// bashQuote is bash's `printf '%q'` for printable ASCII: an empty string
// becomes a pair of single quotes; the characters in specialChars are backslash-escaped
// wherever they occur; '#' is escaped only at position 0 (a comment
// there, not elsewhere); '~' is escaped at position 0 or right after '='
// or ':' (a tilde expansion there, not elsewhere). Anything outside
// printable ASCII -- a control character or a non-ASCII byte -- is an
// error, since bash's own %q would instead fall back to $'...'
// ANSI-C quoting, which this project's values are never expected to need.
func bashQuote(s string) (string, error) {
	if s == "" {
		return "''", nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return "", fmt.Errorf("bashQuote: byte 0x%02x is not printable ASCII", c)
		}
		escape := strings.IndexByte(specialChars, c) >= 0
		if c == '#' && i == 0 {
			escape = true
		}
		if c == '~' && (i == 0 || s[i-1] == '=' || s[i-1] == ':') {
			escape = true
		}
		if escape {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}
