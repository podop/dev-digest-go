package domain

import (
	"bytes"
	"unicode/utf8"
)

// glSectionMarkers are the collapsible-section markers GitLab's log renderer acts on
// ("section_start:<unix time>:<name>", "section_end:..."); a marker in text from outside would
// fold or hide the log around it (spec FR29).
var glSectionMarkers = [...]string{"section_start:", "section_end:"}

// GitLabLogText makes text from outside safe to print into a GitLab job log (spec FR29): ESC
// sequences (ESC and a following CSI sequence) are removed, as is every C0 control character,
// DEL and every C1 control, and each line break (CR, LF, CR LF, NEL, U+2028, U+2029) becomes one
// "↵". Invalid UTF-8 becomes U+FFFD. Last, every "section_start:" and "section_end:" is removed
// together with any that the removal brings together, so none survives in the result. One pass,
// linear in the input. The result holds no line break and no control character; it is not cut
// (the caller applies the 1,200-byte FAILED cap after redacting secrets).
func GitLabLogText(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			out = appendLogRune(out, utf8.RuneError)
			i++
			continue
		}
		switch {
		case r == 0x1b:
			i += 1 + csiLength(s[i+1:])
			continue
		case r == '\r':
			i += size
			if i < len(s) && s[i] == '\n' {
				i++
			}
			out = appendLogRune(out, '↵')
			continue
		case r == '\n', r == 0x85, r == 0x2028, r == 0x2029:
			out = appendLogRune(out, '↵')
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// dropped
		default:
			out = appendLogRune(out, r)
		}
		i += size
	}
	return string(out)
}

// appendLogRune appends r and then cuts a section marker the append completed. Markers are
// ASCII, so a byte-wise suffix test cannot cut inside a multi-byte character.
func appendLogRune(out []byte, r rune) []byte {
	out = utf8.AppendRune(out, r)
	if r != ':' {
		return out
	}
	for _, m := range glSectionMarkers {
		if bytes.HasSuffix(out, []byte(m)) {
			return out[:len(out)-len(m)]
		}
	}
	return out
}

// csiLength is the length of the CSI sequence that starts s, which follows an ESC: "[", parameter
// bytes 0x30-0x3F, intermediate bytes 0x20-0x2F and one final byte 0x40-0x7E. It is 0 when s does
// not start one (the ESC alone is dropped), and the whole of s when the sequence is cut short.
func csiLength(s string) int {
	if s == "" || s[0] != '[' {
		return 0
	}
	i := 1
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}
