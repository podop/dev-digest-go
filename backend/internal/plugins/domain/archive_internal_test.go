package domain

import (
	"errors"
	"io"
	"testing"
)

// countingReader yields zeros forever and counts the bytes pulled from it.
type countingReader struct{ n int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += int64(len(p))
	clear(p)
	return len(p), nil
}

// finiteReader yields n zero bytes, then EOF.
type finiteReader struct{ left int64 }

func (f *finiteReader) Read(p []byte) (int, error) {
	if f.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), f.left)
	clear(p[:n])
	f.left -= n
	return int(n), nil
}

func TestReadCapped_NeverPullsMoreThanTheCapPlusOne(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name string
		hint uint64
	}{
		{"no hint", 0},
		{"lying small hint", 10},
		{"honest hint", limit},
		{"lying huge hint", 1 << 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &countingReader{}
			_, err := readCapped(src, limit, tc.hint)
			if !errors.Is(err, errTooLarge) {
				t.Fatalf("readCapped(infinite) error = %v, want errTooLarge", err)
			}
			if src.n > limit+1 {
				t.Errorf("readCapped pulled %d bytes from the source, want at most %d", src.n, limit+1)
			}
		})
	}
}

func TestReadCapped_Boundary(t *testing.T) {
	const limit = 100_000
	tests := []struct {
		name    string
		size    int64
		hint    uint64
		wantErr bool
	}{
		{"empty", 0, 0, false},
		{"one byte under the cap", limit - 1, limit - 1, false},
		{"exactly the cap", limit, limit, false},
		{"exactly the cap, lying hint", limit, 5, false},
		{"one byte over the cap", limit + 1, limit + 1, true},
		{"one byte over the cap, lying hint", limit + 1, 5, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readCapped(&finiteReader{left: tc.size}, limit, tc.hint)
			if tc.wantErr {
				if !errors.Is(err, errTooLarge) {
					t.Fatalf("readCapped(%d bytes) error = %v, want errTooLarge", tc.size, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readCapped(%d bytes) error = %v", tc.size, err)
			}
			if int64(len(got)) != tc.size {
				t.Errorf("readCapped(%d bytes) returned %d bytes", tc.size, len(got))
			}
		})
	}
}

func TestJSONHasNUL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"escape in a value", `{"a":"x\u0000y"}`, true},
		{"escape in a key", `{"a\u0000":1}`, true},
		{"escape in an array", `["a", "\u0000"]`, true},
		{"escaped backslash then text", `{"a":"\\u0000"}`, false},
		{"escaped backslash then escape", `{"a":"\\\u0000"}`, true},
		{"other escape", `{"a":"\u0001\n"}`, false},
		{"upper case digits are not NUL", `{"a":"\u00A0"}`, false},
		{"outside a string", `{"a":1}`, false},
		{"truncated escape", `"\u00`, false},
		{"escape at the very end of a string", `"\u0000"`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonHasNUL([]byte(tc.raw)); got != tc.want {
				t.Errorf("jsonHasNUL(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestJSONHasLoneSurrogate(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"lone high", `{"x":"\ud800"}`, true},
		{"lone high upper case", `{"x":"\uDBFF"}`, true},
		{"lone low", `{"x":"\udc00"}`, true},
		{"low before high", `{"x":"\udc00\ud800"}`, true},
		{"high then text", `{"x":"\ud800abc"}`, true},
		{"high then other escape", `{"x":"\ud800\u0041"}`, true},
		{"high at the end of the text", `"\ud800`, true},
		{"high in a key", `{"\ud800":1}`, true},
		{"high in an array", `["a","\ud83d"]`, true},
		{"valid pair", `{"x":"\ud83d\ude00"}`, false},
		{"valid pair upper case", `{"x":"\uD83D\uDE00"}`, false},
		{"two pairs", `{"x":"\ud83d\ude00\ud83d\ude00"}`, false},
		{"high, pair", `{"x":"\ud83d\ud83d\ude00"}`, true},
		{"escaped backslash then text", `{"x":"\\ud800"}`, false},
		{"escaped backslash then escape", `{"x":"\\\ud800"}`, true},
		{"ordinary escapes", `{"x":"\u00e9\u20ac\n\"\ud7ff\ue000"}`, false},
		{"outside a string", `{"a":1}`, false},
		{"truncated escape", `"\ud80`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonHasLoneSurrogate([]byte(tc.raw)); got != tc.want {
				t.Errorf("jsonHasLoneSurrogate(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCheckEntryName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"devdigest-plugin.json", false},
		{"plugin/devdigest-plugin.json", false},
		{"plugin/", false},
		{"a..b/c", false},
		{"..", true},
		{"../x", true},
		{"a/..", true},
		{"a/../b", true},
		{"/x", true},
		{`a\b`, true},
		{"C:/x", true},
		{"z:", true},
		{"", true},
		{"/", true},
		{"a\tb", true},
		{"a\u0085b", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkEntryName(tc.name)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkEntryName(%q) error = %v, wantErr %v", tc.name, err, tc.wantErr)
			}
		})
	}
}
