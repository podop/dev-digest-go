package domain

// JavaScript-parity text helpers shared by the Phase 5 contexts (intent, brief, onboarding,
// smart diff, blast). Node counts, cuts, sorts and serialises strings in UTF-16 code units; Go
// counts bytes and runes. Everything here reproduces the JavaScript result on a Go string. The
// existing Len16, EstimateTokens, JSTrim and prefix16 (split.go) are the building blocks.
//
// A Go string cannot hold a lone surrogate. Where JavaScript would keep one (a cut inside a
// surrogate pair), the helpers store U+FFFD in its place: that is what Node's UTF-8 encoding
// writes for it, and it is one unit long like the surrogate, so lengths agree too.

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// ErrURIMalformed is the failure of DecodeURIComponent; its text is the message of the
// JavaScript URIError.
var ErrURIMalformed = errors.New("URI malformed")

// PrefixUnits is JavaScript's s.slice(0, end) for a Go string. A negative end counts from the
// end of the string (slice semantics); an end past the length returns s. A cut inside a
// surrogate pair keeps the high half as U+FFFD (see the file comment).
func PrefixUnits(s string, end int) string {
	if end < 0 {
		end += Len16(s)
		if end < 0 {
			end = 0
		}
	}
	u := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if u+w > end {
			if u < end { // end falls between the two halves of a pair
				return s[:i] + "\uFFFD"
			}
			return s[:i]
		}
		u += w
	}
	return s
}

// CutTruncated is the clamp of Node's truncateText: trim with JavaScript trim semantics, return
// the text when it has at most max UTF-16 units, else its first max-1 units followed by "…"
// (U+2026). The flag reports whether the text was shortened.
func CutTruncated(text string, max int) (string, bool) {
	t := JSTrim(text)
	if Len16(t) <= max {
		return t, false
	}
	return PrefixUnits(t, max-1) + "…", true
}

// Cut is CutTruncated without the flag (spec 7.1 "Cut(text, max)").
func Cut(text string, max int) string {
	t, _ := CutTruncated(text, max)
	return t
}

// CompareUnits orders two strings by UTF-16 code units, as JavaScript's < and the default
// Array.prototype.sort do: a supplementary character (a surrogate pair) sorts before U+E000..
// U+FFFF, unlike in UTF-8 byte order. It returns -1, 0 or +1 and does not allocate.
func CompareUnits(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if ra != rb {
			if ua, ub := firstUnit(ra), firstUnit(rb); ua != ub {
				if ua < ub {
					return -1
				}
				return 1
			}
			// same high surrogate: both supplementary, the low surrogate decides
			if ra < rb {
				return -1
			}
			return 1
		}
		a, b = a[sa:], b[sb:]
	}
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return -1
	}
	return 1
}

func firstUnit(r rune) rune {
	if r >= 0x10000 {
		return 0xD800 + (r-0x10000)>>10
	}
	return r
}

// LessUnits reports a < b in UTF-16 code unit order (JavaScript's `<` on strings).
func LessUnits(a, b string) bool { return CompareUnits(a, b) < 0 }

// EnglishComparer returns a comparison equal to JavaScript's a.localeCompare(b, 'en'): -1, 0 or
// +1. The collator behind it is not safe for concurrent use, so take one per sort, not one per
// process.
func EnglishComparer() func(a, b string) int {
	coll := collate.New(language.English)
	return coll.CompareString
}

// SortEnglish sorts s in place by English collation (localeCompare). The sort is stable, like
// Array.prototype.sort.
func SortEnglish(s []string) {
	cmp := EnglishComparer()
	sort.SliceStable(s, func(i, j int) bool { return cmp(s[i], s[j]) < 0 })
}

// SortEnglishBy sorts s in place, stably, by the English collation of key(item).
func SortEnglishBy[T any](s []T, key func(T) string) {
	cmp := EnglishComparer()
	sort.SliceStable(s, func(i, j int) bool { return cmp(key(s[i]), key(s[j])) < 0 })
}

// DecodeURIComponent is JavaScript's decodeURIComponent: every %XX escape is decoded as UTF-8;
// an escape that is not two hex digits, or whose bytes are not one well-formed UTF-8 sequence
// (overlong, surrogate, above U+10FFFF, truncated), fails with ErrURIMalformed. Other
// characters pass through unchanged.
func DecodeURIComponent(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '%' {
			out.WriteByte(s[i])
			i++
			continue
		}
		b0, ok := hexByte(s, i)
		if !ok {
			return "", ErrURIMalformed
		}
		i += 3
		if b0 < 0x80 {
			out.WriteByte(b0)
			continue
		}
		var n int
		switch {
		case b0&0xE0 == 0xC0:
			n = 2
		case b0&0xF0 == 0xE0:
			n = 3
		case b0&0xF8 == 0xF0:
			n = 4
		default: // a continuation byte or 0xF8..0xFF cannot start a sequence
			return "", ErrURIMalformed
		}
		seq := [4]byte{b0}
		for k := 1; k < n; k++ {
			if i >= len(s) || s[i] != '%' {
				return "", ErrURIMalformed
			}
			bk, ok := hexByte(s, i)
			if !ok || bk&0xC0 != 0x80 {
				return "", ErrURIMalformed
			}
			seq[k] = bk
			i += 3
		}
		r, size := utf8.DecodeRune(seq[:n])
		if r == utf8.RuneError || size != n { // overlong, surrogate or out of range
			return "", ErrURIMalformed
		}
		out.Write(seq[:n])
	}
	return out.String(), nil
}

// hexByte reads the escape "%XX" at s[i:].
func hexByte(s string, i int) (byte, bool) {
	if i+2 >= len(s) {
		return 0, false
	}
	hi, ok1 := hexVal(s[i+1])
	lo, ok2 := hexVal(s[i+2])
	return hi<<4 | lo, ok1 && ok2
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// JSNumber formats f as JavaScript's Number.prototype.toString (also what JSON.stringify writes
// for a finite number): shortest round-trip digits, plain notation for exponents -7 < e < 21,
// "1e+21" style beyond. NaN and the infinities give "NaN", "Infinity", "-Infinity"; -0 gives "0".
func JSNumber(f float64) string {
	switch {
	case f != f:
		return "NaN"
	case f > 1.7976931348623157e308:
		return "Infinity"
	case f < -1.7976931348623157e308:
		return "-Infinity"
	case f == 0:
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d[.ddd]e±XX
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	k, n := len(digits), exp+1 // value = 0.digits × 10^n
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	ex := strconv.Itoa(abs(n - 1))
	if k == 1 {
		return sign + digits + "e" + es + ex
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + es + ex
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ToFixed is JavaScript's Number.prototype.toFixed: f rounded to the given number of decimals
// by its exact binary value, ties away from zero (strconv rounds ties to even: 0.125 with two
// decimals is "0.13" in JavaScript, "0.12" in Go). digits is clamped to 0..100 (JavaScript
// throws outside it); |f| >= 1e21 and NaN give JSNumber(f).
func ToFixed(f float64, digits int) string {
	if digits < 0 {
		digits = 0
	}
	if digits > 100 {
		digits = 100
	}
	if f != f || f >= 1e21 || f <= -1e21 {
		return JSNumber(f)
	}
	sign := ""
	if f < 0 { // not for -0, which prints as "0"
		sign, f = "-", -f
	}
	r := new(big.Rat).SetFloat64(f)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	r.Mul(r, new(big.Rat).SetInt(scale))
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Quo(r.Num(), r.Denom()) // floor: r is not negative
	s := n.String()
	if digits > 0 {
		if len(s) <= digits {
			s = strings.Repeat("0", digits-len(s)+1) + s
		}
		s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	return sign + s
}

// Canonical returns v as JavaScript's JSON.stringify(sortKeysDeep(v)) would: no whitespace,
// object keys sorted in UTF-16 code unit order (array-index-like keys first, ascending, as a
// JavaScript object enumerates them), no HTML escaping, U+2028/U+2029 written as they are, and
// numbers formatted by JSNumber. v is first encoded by encoding/json, so struct tags, maps and
// json.Marshaler behave as usual; a value encoding/json rejects is returned as its error.
func Canonical(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", err
	}
	var sb strings.Builder
	writeCanonical(&sb, tree)
	return sb.String(), nil
}

func writeCanonical(sb *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(x))
	case json.Number:
		f, _ := strconv.ParseFloat(string(x), 64)
		if f != f || f > 1.7976931348623157e308 || f < -1.7976931348623157e308 {
			sb.WriteString("null") // JSON.stringify(Infinity)
			return
		}
		sb.WriteString(JSNumber(f))
	case string:
		writeJSString(sb, x)
	case []any:
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanonical(sb, e)
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return jsKeyLess(keys[i], keys[j]) })
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeJSString(sb, k)
			sb.WriteByte(':')
			writeCanonical(sb, x[k])
		}
		sb.WriteByte('}')
	}
}

// jsKeyLess is the enumeration order of a JavaScript object after its keys were inserted in
// sorted order: array indices (canonical decimal below 2^32-1) ascending, then the rest in
// code unit order.
func jsKeyLess(a, b string) bool {
	ia, aIdx := arrayIndex(a)
	ib, bIdx := arrayIndex(b)
	switch {
	case aIdx && bIdx:
		return ia < ib
	case aIdx != bIdx:
		return aIdx
	}
	return LessUnits(a, b)
}

func arrayIndex(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return 0, false
		}
	}
	n, _ := strconv.ParseUint(k, 10, 64)
	return n, n < 1<<32-1
}

// writeJSString writes s as JSON.stringify does: only the quote, the backslash and the control
// characters are escaped (\b \t \n \f \r by letter, the rest as lower-case \u00xx).
func writeJSString(sb *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '\b':
			sb.WriteString(`\b`)
		case r == '\f':
			sb.WriteString(`\f`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20:
			sb.WriteString(`\u00`)
			sb.WriteByte(hex[r>>4])
			sb.WriteByte(hex[r&0xF])
		default:
			sb.WriteRune(r) // invalid bytes arrive as U+FFFD
		}
	}
	sb.WriteByte('"')
}
