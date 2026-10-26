package llm_test

import (
	"testing"

	"dev-digest-go/backend/internal/llm"
)

func TestStaticPerM(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model         string
		wantIn, wantO float64
		wantOK        bool
	}{
		{model: "gpt-5.4", wantIn: 2.5, wantO: 15, wantOK: true},
		{model: "claude-haiku-4-5", wantIn: 1, wantO: 5, wantOK: true},
		{model: "unknown-model", wantOK: false},
	}
	for _, tc := range tests {
		in, out, ok := llm.StaticPerM(tc.model)
		if in != tc.wantIn || out != tc.wantO || ok != tc.wantOK {
			t.Errorf("StaticPerM(%q) = %v, %v, %v, want %v, %v, %v", tc.model, in, out, ok, tc.wantIn, tc.wantO, tc.wantOK)
		}
	}
}

func TestClient_PerM(t *testing.T) {
	t.Parallel()
	c := llm.New(llm.Options{Keys: noKeys{}})
	defer c.Close()
	if in, out, ok := c.PerM("claude-sonnet-4-6"); !ok || in != 3 || out != 15 {
		t.Errorf("Client.PerM(claude-sonnet-4-6) = %v, %v, %v, want 3, 15, true", in, out, ok)
	}
	if _, _, ok := c.PerM("unknown-model"); ok {
		t.Error("Client.PerM(unknown-model) ok = true, want false")
	}
}

type noKeys struct{}

func (noKeys) Get(string) string { return "" }
