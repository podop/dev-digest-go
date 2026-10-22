package parity_test

// Phase 5 parity of the Go implementation with the Node implementation of dev-digest (commit
// d1c9093): the goldens in testdata/goldens were produced by running Node (testdata/gen/README.md).
// This file holds what every test of the package shares: loading a golden or a corpus file, the
// `$blob` digest of a long string, and a few JSON helpers. The package has no production code.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// loadGolden decodes testdata/goldens/<name>.json into v.
func loadGolden(t testing.TB, name string, v any) {
	t.Helper()
	readJSON(t, filepath.Join("testdata", "goldens", name+".json"), v)
}

// loadCorpus decodes testdata/corpus/<dir>/<name>.json into v.
func loadCorpus(t testing.TB, dir, name string, v any) {
	t.Helper()
	readJSON(t, filepath.Join("testdata", "corpus", dir, name+".json"), v)
}

func readJSON(t testing.TB, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// utf16Len is the length of s in UTF-16 code units, as JavaScript counts it.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if l := utf16.RuneLen(r); l > 0 {
			n += l
		} else { // an invalid rune counts as U+FFFD
			n++
		}
	}
	return n
}

// blob is the digest a golden writes for a string above 16,384 UTF-16 units.
type blob struct {
	Len    int    `json:"len"`
	SHA256 string `json:"sha256"`
	Head   string `json:"head"`
	Tail   string `json:"tail"`
}

// text is a golden string: the text itself or its blob digest.
type text struct {
	s string
	b *blob
}

func (x *text) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '{' {
		var d struct {
			Blob bool `json:"$blob"`
			blob
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if !d.Blob {
			return fmt.Errorf("object %s is not a $blob digest", raw)
		}
		b := d.blob
		x.b = &b
		return nil
	}
	return json.Unmarshal(raw, &x.s)
}

// matches reports whether got equals the golden string (by length and SHA-256 for a blob).
func (x text) matches(got string) bool {
	if x.b == nil {
		return got == x.s
	}
	sum := sha256.Sum256([]byte(got))
	return utf16Len(got) == x.b.Len && hex.EncodeToString(sum[:]) == x.b.SHA256
}

// describe is what an error message shows for the golden string.
func (x text) describe() string {
	if x.b == nil {
		return fmt.Sprintf("%q", x.s)
	}
	return fmt.Sprintf("blob{len=%d sha256=%s head=%q}", x.b.Len, x.b.SHA256, x.b.Head)
}

// describeGot is the same description for a string a Go function returned.
func describeGot(got string) string {
	if utf16Len(got) <= 16_384 {
		return fmt.Sprintf("%q", got)
	}
	sum := sha256.Sum256([]byte(got))
	r := []rune(got)
	return fmt.Sprintf("blob{len=%d sha256=%s head=%q}", utf16Len(got), hex.EncodeToString(sum[:]), string(r[:min(80, len(r))]))
}

// checkText fails t when got differs from the golden string.
func checkText(t testing.TB, what string, want text, got string) {
	t.Helper()
	if !want.matches(got) {
		t.Errorf("%s = %s, want %s", what, describeGot(got), want.describe())
	}
}

// expandStrings resolves the corpus placeholders `{"$repeat": [text, n]}` and `{"$concat": [a, b…]}`
// (testdata/gen/README.md) of a decoded corpus value; other values are returned unchanged.
func expandStrings(t testing.TB, v any) string {
	t.Helper()
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if r, ok := x["$repeat"].([]any); ok && len(r) == 2 {
			return strings.Repeat(expandStrings(t, r[0]), int(r[1].(float64)))
		}
		if c, ok := x["$concat"].([]any); ok {
			var sb strings.Builder
			for _, p := range c {
				sb.WriteString(expandStrings(t, p))
			}
			return sb.String()
		}
	}
	t.Fatalf("cannot expand corpus string %v", v)
	return ""
}

// jsonValue decodes raw JSON into a generic value, so that two documents can be compared as
// values (object key order is not significant).
func jsonValue(t testing.TB, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// toValue turns a Go value into its generic JSON value.
func toValue(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return jsonValue(t, raw)
}
