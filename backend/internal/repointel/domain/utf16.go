package domain

import "unicode/utf8"

// UTF16Len returns the length of s in UTF-16 code units, the unit JavaScript's String.length and
// the Node implementation count in. A byte that is not valid UTF-8 counts as one unit (U+FFFD).
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// ClampUTF16 cuts s to at most max UTF-16 code units without splitting a surrogate pair: a
// character that would straddle the limit is dropped whole (FR3031). A negative max is zero.
func ClampUTF16(s string, max int) string {
	if max < 0 {
		max = 0
	}
	// ASCII-sized strings cannot exceed max units: a unit is at least one byte of input.
	if len(s) <= max {
		return s
	}
	units := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > max {
			return s[:i]
		}
		units += w
		i += size
	}
	return s
}
