package domain

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

// DecodeText decodes source bytes the way the Node indexer reads a file: as UTF-8 with the WHATWG
// decoder's error handling, so every maximal invalid subpart becomes one U+FFFD (P4). A byte
// loop over runes would replace each stray byte instead and give another text, another hash.
// The byte order mark is kept; a NUL byte is kept.
func DecodeText(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	var (
		cp       rune
		needed   int
		seen     int
		lower    byte = 0x80
		upper    byte = 0xBF
		resetDec      = func() { cp, needed, seen, lower, upper = 0, 0, 0, 0x80, 0xBF }
	)
	for i := 0; i < len(b); {
		c := b[i]
		if needed == 0 {
			i++
			switch {
			case c <= 0x7F:
				sb.WriteByte(c)
			case c >= 0xC2 && c <= 0xDF:
				needed, cp = 1, rune(c&0x1F)
			case c >= 0xE0 && c <= 0xEF:
				switch c {
				case 0xE0:
					lower = 0xA0
				case 0xED:
					upper = 0x9F
				}
				needed, cp = 2, rune(c&0x0F)
			case c >= 0xF0 && c <= 0xF4:
				switch c {
				case 0xF0:
					lower = 0x90
				case 0xF4:
					upper = 0x8F
				}
				needed, cp = 3, rune(c&0x07)
			default:
				sb.WriteRune('\uFFFD')
			}
			continue
		}
		if c < lower || c > upper {
			// The sequence ends here: emit one replacement and read c again from the start state.
			resetDec()
			sb.WriteRune('\uFFFD')
			continue
		}
		lower, upper = 0x80, 0xBF
		cp = cp<<6 | rune(c&0x3F)
		seen++
		i++
		if seen == needed {
			sb.WriteRune(cp)
			resetDec()
		}
	}
	if needed != 0 {
		sb.WriteRune('\uFFFD')
	}
	return sb.String()
}

// ContentHash returns the lowercase hex SHA-1 of text encoded as UTF-8 (FR3017).
func ContentHash(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

// SanitizeText makes s storable in a Postgres text or jsonb column: U+0000, which neither accepts,
// becomes U+FFFD, and so does every byte run that is not valid UTF-8. Text decoded by DecodeText
// is valid UTF-8 already but may hold U+0000 (a NUL inside a string literal).
func SanitizeText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "\uFFFD")
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// ClampName cuts a symbol name or a reference's target to MaxNameUnits UTF-16 code units without
// splitting a surrogate pair (FR3031).
func ClampName(s string) string {
	return ClampUTF16(s, MaxNameUnits)
}
