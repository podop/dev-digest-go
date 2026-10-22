package importgraph_test

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// plain converts parsed values to maps for comparison; key order is checked separately.
func plain(v any) any {
	switch t := v.(type) {
	case *importgraph.Object:
		m := map[string]any{}
		for _, k := range t.Keys {
			x, _ := t.Get(k)
			m[k] = plain(x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = plain(x)
		}
		return out
	}
	return v
}

func TestParseJSON5_Valid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want any
	}{
		{name: "plain json", in: `{"a": [1, 2.5, -3e2, true, false, null, "x"]}`, want: map[string]any{"a": []any{1.0, 2.5, -300.0, true, false, nil, "x"}}},
		{name: "line and block comments", in: "// head\n{ /* c */ \"a\": 1, // tail\n \"b\": /* mid */ 2 }", want: map[string]any{"a": 1.0, "b": 2.0}},
		{name: "trailing commas", in: `{"a": [1, 2,], "b": {"c": 1,},}`, want: map[string]any{"a": []any{1.0, 2.0}, "b": map[string]any{"c": 1.0}}},
		{name: "single quotes", in: `{'a': 'it\'s "ok"'}`, want: map[string]any{"a": `it's "ok"`}},
		{name: "bare keys", in: `{a: 1, $b: 2, _c1: 3, ünï: 4}`, want: map[string]any{"a": 1.0, "$b": 2.0, "_c1": 3.0, "ünï": 4.0}},
		{name: "string escapes", in: `"a\n\tA\x42\/\\\"\
z"`, want: "a\n\tAB/\\\"z"},
		{name: "surrogate pair", in: `"\ud83d\ude00"`, want: "\U0001F600"},
		{name: "json5 numbers", in: `[+1, .5, 5., 0x1F, -0xA, Infinity, -Infinity]`, want: []any{1.0, 0.5, 5.0, 31.0, -10.0, math.Inf(1), math.Inf(-1)}},
		{name: "byte order mark", in: "\uFEFF{\"a\":1}", want: map[string]any{"a": 1.0}},
		{name: "unicode white space", in: "{\u00a0\"a\"\u2003:\u00a01}", want: map[string]any{"a": 1.0}},
		{name: "empty containers", in: `{"a": {}, "b": []}`, want: map[string]any{"a": map[string]any{}, "b": []any{}}},
		{name: "slashes in strings are not comments", in: `{"p": "a//b/*c*/"}`, want: map[string]any{"p": "a//b/*c*/"}},
		{name: "tsconfig shape", in: "{\n  // base\n  'extends': './base',\n  compilerOptions: {\n    baseUrl: '.',\n    paths: { '@/*': ['src/*'], },\n  },\n}\n", want: map[string]any{
			"extends": "./base",
			"compilerOptions": map[string]any{
				"baseUrl": ".",
				"paths":   map[string]any{"@/*": []any{"src/*"}},
			},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := importgraph.ParseJSON5([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseJSON5(%q) error: %v", tc.in, err)
			}
			if diff := cmp.Diff(tc.want, plain(got), cmpopts.EquateNaNs()); diff != "" {
				t.Errorf("ParseJSON5(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestParseJSON5_NaN(t *testing.T) {
	got, err := importgraph.ParseJSON5([]byte("NaN"))
	if f, ok := got.(float64); err != nil || !ok || !math.IsNaN(f) {
		t.Errorf("ParseJSON5(NaN) = %v, %v, want NaN", got, err)
	}
}

func TestParseJSON5_KeyOrderAndRepeatedKeys(t *testing.T) {
	got, err := importgraph.ParseJSON5([]byte(`{"b": 1, "a": 2, "b": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	obj := got.(*importgraph.Object)
	if diff := cmp.Diff([]string{"b", "a"}, obj.Keys); diff != "" {
		t.Errorf("Keys mismatch (-want +got):\n%s", diff)
	}
	if v, _ := obj.Get("b"); v != 3.0 {
		t.Errorf("Get(b) = %v, want the last value 3", v)
	}
	if _, ok := obj.Get("zzz"); ok {
		t.Error("Get(zzz) ok, want absent")
	}
}

func TestParseJSON5_Invalid(t *testing.T) {
	deep := ""
	for range 100 {
		deep += "["
	}
	tests := map[string]string{
		"empty":                  ``,
		"only a comment":         `// nothing`,
		"unterminated object":    `{"a": 1`,
		"unterminated array":     `[1, 2`,
		"unterminated string":    `{"a": "x`,
		"line break in string":   "{\"a\": \"x\ny\"}",
		"unterminated comment":   `{"a": 1 /* oops`,
		"unterminated comment 2": `/* oops`,
		"missing colon":          `{"a" 1}`,
		"missing comma":          `{"a": 1 "b": 2}`,
		"bad value":              `{"a": nope}`,
		"bad number":             `{"a": 1.2.3}`,
		"underscored number":     `[1_000]`,
		"inf spelling":           `[inf]`,
		"trailing content":       `{} {}`,
		"bad escape":             `"\u12"`,
		"bad hex number":         `[0xZZ]`,
		"no key":                 `{: 1}`,
		"too deep":               deep,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := importgraph.ParseJSON5([]byte(in)); err == nil {
				t.Errorf("ParseJSON5(%q) = %v, want an error", in, got)
			}
		})
	}
}

// FuzzParseJSON5 checks that hostile tsconfig content never panics or hangs; the seeds replay as
// part of a plain `go test`.
func FuzzParseJSON5(f *testing.F) {
	for _, seed := range []string{
		`{"compilerOptions":{"paths":{"@/*":["src/*"]}}}`,
		"{a:1,/*c*/'b':[1,2,],}",
		`"😀"`,
		"[0x1F, +.5, -Infinity]",
		"/* unterminated",
		"{\"a\": \"x\\",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = importgraph.ParseJSON5(data)
	})
}
