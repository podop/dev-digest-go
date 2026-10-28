package wire_test

import (
	"encoding/json"
	"net/http"
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

func TestGatewayCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cost   string // usage.cost as raw JSON, "" = absent
		header string // X-Litellm-Response-Cost, "" = absent
		want   *float64
	}{
		{name: "usage cost", cost: "0.25", want: new(0.25)},
		{name: "usage cost wins over the header", cost: "0.25", header: "9.4e-06", want: new(0.25)},
		{name: "header when usage has no cost", header: "9.4e-06", want: new(9.4e-06)},
		{name: "header with spaces", header: " 0.5 ", want: new(0.5)},
		{name: "zero header", header: "0.0", want: new(0.0)},
		{name: "header when usage cost is unusable", cost: `"0.25"`, header: "0.5", want: new(0.5)},
		{name: "negative usage cost falls back to the header", cost: "-1", header: "0.5", want: new(0.5)},
		{name: "negative header", header: "-0.5"},
		{name: "non-number header", header: "None"},
		{name: "NaN header", header: "NaN"},
		{name: "infinite header", header: "1e999"},
		{name: "nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tc.header != "" {
				h.Set(wire.LiteLLMCostHeader, tc.header)
			}
			got := wire.GatewayCost(json.RawMessage(tc.cost), h)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GatewayCost(%q, %q) mismatch (-want +got):\n%s", tc.cost, tc.header, diff)
			}
		})
	}
}
