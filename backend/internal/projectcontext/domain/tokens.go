package domain

import "unicode/utf8"

// Tokens estimates tokens as ceil(units / 4).
func Tokens(units int) int { return (units + 3) / 4 }

// StoreTextTokens is the token estimate of a store document: ceil(Unicode code points / 4).
func StoreTextTokens(text string) int { return Tokens(utf8.RuneCountInString(text)) }

// UTF8Bytes is the size of a store text as stored: its UTF-8 byte length.
func UTF8Bytes(text string) int { return len(text) }

// CloneTextTokens is the token estimate of a clone document that was read:
// ceil(UTF-16 code units of the decoded text / 4) (D218). The text is decoded
// like JavaScript's UTF-8 decoder: each maximal invalid byte sequence counts
// as one U+FFFD.
func CloneTextTokens(content []byte) int { return Tokens(UTF16Units(content)) }

// CloneSizeTokens is the token estimate of a clone document that was not read
// (larger than MaxDocBytes): ceil(size / 4).
func CloneSizeTokens(size int64) int { return int((size + 3) / 4) }

// UTF16Units is the number of UTF-16 code units of content decoded as UTF-8
// with replacement (WHATWG): a maximal invalid subpart becomes one U+FFFD.
func UTF16Units(content []byte) int {
	n := 0
	for i := 0; i < len(content); {
		r, size := utf8.DecodeRune(content[i:])
		if r != utf8.RuneError || size != 1 {
			if r >= 0x10000 {
				n += 2
			} else {
				n++
			}
			i += size
			continue
		}
		n++ // U+FFFD
		i += invalidPrefixLen(content[i:])
	}
	return n
}

// invalidPrefixLen is the length of the maximal subpart of an ill-formed
// sequence at the start of b: the lead byte and the continuation bytes that
// still form a valid prefix of a UTF-8 sequence.
func invalidPrefixLen(b []byte) int {
	lead := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead >= 0xE0 && lead <= 0xEF:
		need = 2
		switch lead {
		case 0xE0:
			lo = 0xA0
		case 0xED:
			hi = 0x9F
		}
	case lead >= 0xF0 && lead <= 0xF4:
		need = 3
		switch lead {
		case 0xF0:
			lo = 0x90
		case 0xF4:
			hi = 0x8F
		}
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(b); n++ {
		if b[n] < lo || b[n] > hi {
			break
		}
		lo, hi = 0x80, 0xBF
	}
	return n
}

// DecodeUTF8 decodes content like JavaScript's UTF-8 decoder: each maximal
// invalid byte sequence becomes one U+FFFD. Valid input is returned as is.
func DecodeUTF8(content []byte) string {
	if utf8.Valid(content) {
		return string(content)
	}
	out := make([]byte, 0, len(content))
	for i := 0; i < len(content); {
		r, size := utf8.DecodeRune(content[i:])
		if r != utf8.RuneError || size != 1 {
			out = append(out, content[i:i+size]...)
			i += size
			continue
		}
		out = utf8.AppendRune(out, utf8.RuneError)
		i += invalidPrefixLen(content[i:])
	}
	return string(out)
}
