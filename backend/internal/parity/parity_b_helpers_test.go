package parity_test

// Helpers of the second parity group (S31: brief, onboarding, performance): loading a golden of
// the Node implementation of dev-digest (commit d1c9093, testdata/gen/README.md), comparing decoded
// JSON, and the `$blob` digest of a long string. Every name starts with "pb" so that it cannot
// collide with the helpers of golden_test.go (group A).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/google/go-cmp/cmp"
)

// pbLoad decodes testdata/goldens/<name>.json into v.
func pbLoad(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "goldens", name+".json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
}

// pbLoadHandwritten decodes testdata/handwritten/<name>.json into v.
func pbLoadHandwritten(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "handwritten", name+".json"))
	if err != nil {
		t.Fatalf("read handwritten %s: %v", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode handwritten %s: %v", name, err)
	}
}

// pbGeneric turns any value into its decoded JSON form (maps, slices, float64, string, bool, nil),
// so that a Go projection and a golden compare by value and not by serialisation.
func pbGeneric(t testing.TB, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return pbDecode(t, b)
}

// pbDecode decodes JSON text into its generic form.
func pbDecode(t testing.TB, b []byte) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %.200s: %v", b, err)
	}
	return out
}

// pbDiff reports a mismatch of want (the Node value) and got (the Go value).
func pbDiff(t testing.TB, what string, want, got any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("%s differs from Node (-node +go):\n%s", what, pbClip(diff, 4000))
	}
}

// pbClip shortens a long failure text.
func pbClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (clipped)"
}

// pbISO renders t as JSON.stringify of a Date does.
func pbISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// pbTime parses an ISO-8601 instant of a golden.
func pbTime(t testing.TB, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse instant %q: %v", s, err)
	}
	return at.UTC()
}

// pbUnits is the length of s in UTF-16 code units, as JavaScript counts it.
func pbUnits(s string) int {
	n := 0
	for _, r := range s {
		if l := utf16.RuneLen(r); l > 0 {
			n += l
		} else {
			n++ // an invalid rune is read as U+FFFD
		}
	}
	return n
}

// pbBlob is the digest a golden writes for a string above 16,384 UTF-16 units.
type pbBlob struct {
	Len    int    `json:"len"`
	SHA256 string `json:"sha256"`
	Head   string `json:"head"`
	Tail   string `json:"tail"`
}

// pbText is a golden string: the text itself or its blob digest.
type pbText struct {
	s string
	b *pbBlob
}

// UnmarshalJSON implements json.Unmarshaler.
func (x *pbText) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '{' {
		var d struct {
			Blob bool `json:"$blob"`
			pbBlob
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		x.b = &d.pbBlob
		return nil
	}
	return json.Unmarshal(raw, &x.s)
}

// pbCheckText compares a Go string with a golden string (plain or blob digest).
func pbCheckText(t testing.TB, what string, want pbText, got string) {
	t.Helper()
	if want.b == nil {
		if diff := cmp.Diff(want.s, got); diff != "" {
			t.Errorf("%s differs from Node (-node +go):\n%s", what, pbClip(diff, 3000))
		}
		return
	}
	if n := pbUnits(got); n != want.b.Len {
		t.Errorf("%s has %d UTF-16 units, Node %d", what, n, want.b.Len)
		return
	}
	if sum := sha256.Sum256([]byte(got)); hex.EncodeToString(sum[:]) != want.b.SHA256 {
		t.Errorf("%s differs from Node: SHA-256 %x, want %s (head %q)", what, sum, want.b.SHA256, pbClip(got, 60))
	}
}

// plain returns the text of a golden string. For a blob digest it rebuilds the text, which the
// corpus writes as literal + $repeat(unit) + literal: a head literal, a run of one repeated unit and
// a tail literal, all read from the digest's head and tail. The result is accepted only when its
// length (UTF-16 units) and SHA-256 equal the digest's, so a wrong guess can never pass as input.
func (x pbText) plain(t testing.TB) string {
	t.Helper()
	if x.b == nil {
		return x.s
	}
	head := []rune(strings.TrimSuffix(x.b.Head, "\uFFFD")) // a cut inside a surrogate pair ends in U+FFFD
	tail := []rune(strings.TrimPrefix(x.b.Tail, "\uFFFD"))
	for ul := 1; ul <= 48 && ul <= len(head); ul++ {
		for i := 0; i+ul <= len(head); i++ {
			unit := head[i : i+ul]
			if !pbPeriodic(head[i:], unit) {
				continue
			}
			uUnits := pbUnits(string(unit))
			for j := len(tail); j >= 0; j-- {
				if !pbEndsRun(tail[:j], unit) {
					continue
				}
				prefix, suffix := string(head[:i]), string(tail[j:])
				rest := x.b.Len - pbUnits(prefix) - pbUnits(suffix)
				if rest < 0 || rest%uUnits != 0 {
					continue
				}
				text := prefix + strings.Repeat(string(unit), rest/uUnits) + suffix
				if sum := sha256.Sum256([]byte(text)); hex.EncodeToString(sum[:]) == x.b.SHA256 {
					return text
				}
			}
			break // the first periodic start for this unit length decides; a later i is a shifted unit
		}
	}
	if text, ok := x.twoRuns(head, tail); ok {
		return text
	}
	t.Fatalf("cannot rebuild the %d-unit golden blob %q ... %q as literal + repeated unit + literal", x.b.Len, pbClip(x.b.Head, 40), x.b.Tail)
	return ""
}

// pbPeriodic reports whether rs repeats unit from its start (the last repetition may be partial).
func pbPeriodic(rs, unit []rune) bool {
	for i, r := range rs {
		if r != unit[i%len(unit)] {
			return false
		}
	}
	return true
}

// pbEndsRun reports whether rs is the end of a run of unit repetitions (a suffix of unit^n).
func pbEndsRun(rs, unit []rune) bool {
	for i := 1; i <= len(rs); i++ {
		if rs[len(rs)-i] != unit[(len(unit)-i%len(unit))%len(unit)] {
			return false
		}
	}
	return true
}

// twoRuns rebuilds literal + run of one unit + a short separator + run of another unit + literal,
// the shape of "token: " + 10,000 x "s" + "\n" + 10,000 x "r" (the corpus' $concat of two $repeat).
// The separator is not part of the digest, so a few likely ones are tried; the SHA-256 decides.
func (x pbText) twoRuns(head, tail []rune) (string, bool) {
	for ul1 := 1; ul1 <= 8 && ul1 <= len(head); ul1++ {
		for i := 0; i+ul1 <= len(head); i++ {
			u1 := head[i : i+ul1]
			if !pbPeriodic(head[i:], u1) {
				continue
			}
			for ul2 := 1; ul2 <= 8 && ul2 <= len(tail); ul2++ {
				u2 := tail[len(tail)-ul2:]
				for j := len(tail); j >= 0; j-- {
					if !pbEndsRun(tail[:j], u2) {
						continue
					}
					for _, sep := range []string{"", "\n", " ", "\r\n", "\n\n"} {
						prefix, suffix := string(head[:i]), string(tail[j:])
						budget := x.b.Len - pbUnits(prefix) - pbUnits(sep) - pbUnits(suffix)
						a, b := pbUnits(string(u1)), pbUnits(string(u2))
						for k1 := 0; k1*a <= budget; k1++ {
							if (budget-k1*a)%b != 0 {
								continue
							}
							text := prefix + strings.Repeat(string(u1), k1) + sep + strings.Repeat(string(u2), (budget-k1*a)/b) + suffix
							if sum := sha256.Sum256([]byte(text)); hex.EncodeToString(sum[:]) == x.b.SHA256 {
								return text, true
							}
						}
					}
					break
				}
			}
			break
		}
	}
	return "", false
}
