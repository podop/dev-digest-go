package wire_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm/adapters/internal/wire"
	"dev-digest-go/backend/internal/llm/custom"
)

func TestJSONNumber(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw    string
		want   float64
		wantOK bool
	}{
		{"0", 0, true},
		{"0.25", 0.25, true},
		{"-1", -1, true},
		{"1e3", 1000, true},
		{"1e999", 0, false},
		{`"0.5"`, 0, false},
		{"null", 0, false},
		{"true", 0, false},
		{"{}", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		got, ok := wire.JSONNumber(json.RawMessage(tc.raw))
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("JSONNumber(%q) = %v, %v; want %v, %v", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestScrubText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		secrets []string
		want    string
	}{
		{"exact", "a k1 b k1", []string{"k1"}, "a [REDACTED] b [REDACTED]"},
		{"empty secret is ignored", "abc", []string{""}, "abc"},
		{"json escaped", `bad "a<b"`, []string{"a<b"}, `bad "[REDACTED]"`},
		{"masked key", "Incorrect API key: sk-proj-****abcd.", nil, "Incorrect API key: [REDACTED]."},
		{"plain text stays", "model not found", []string{"k1"}, "model not found"},
	}
	for _, tc := range tests {
		if got := wire.ScrubText(tc.in, tc.secrets); got != tc.want {
			t.Errorf("ScrubText(%q, %q) = %q, want %q", tc.in, tc.secrets, got, tc.want)
		}
	}
}

func TestCustomHeaders(t *testing.T) {
	t.Parallel()
	got, err := wire.CustomHeaders(custom.AuthHeader, "", "k", map[string]string{"anthropic-version": "9", "x-a": "1"}, map[string]string{"anthropic-version": "2023-06-01", "x-d": "d"})
	if err != nil {
		t.Fatalf("CustomHeaders() error = %v", err)
	}
	want := map[string]string{"X-Api-Key": "k", "Anthropic-Version": "9", "X-A": "1", "X-D": "d"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CustomHeaders() mismatch (-want +got):\n%s", diff)
	}
}
