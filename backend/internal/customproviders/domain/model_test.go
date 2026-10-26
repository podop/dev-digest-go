package domain_test

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/customproviders/domain"
)

func TestPlainHTTP(t *testing.T) {
	tests := []struct {
		baseURL string
		want    bool
	}{
		{"http://gateway.corp/v1", true}, // EC24
		{"http://127.0.0.1:8080/v1", false},
		{"http://[::1]/v1", false},
		{"http://localhost/v1", false},
		{"https://gateway.corp/v1", false},
		{"http://10.0.0.5/v1", true},
	}
	for _, tc := range tests {
		p := domain.Provider{BaseURL: tc.baseURL}
		if got := p.PlainHTTP(); got != tc.want {
			t.Errorf("Provider{%q}.PlainHTTP() = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestHost(t *testing.T) {
	for in, want := range map[string]string{"https://synapse.test/v1": "synapse.test", "http://127.0.0.1:8080/v1": "127.0.0.1:8080", "http://[::1]/v1": "[::1]"} {
		if got := (domain.Provider{BaseURL: in}).Host(); got != want {
			t.Errorf("Provider{%q}.Host() = %q, want %q", in, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	if got, want := (domain.Provider{ID: "custom:my-gw"}).Slug(), "my-gw"; got != want {
		t.Errorf("Slug() = %q, want %q", got, want)
	}
}

func TestEstimate(t *testing.T) {
	p := domain.Provider{Models: []domain.Model{
		{ID: "priced", Pricing: &domain.Pricing{PromptPerM: 2, CompletionPerM: 8}},
		{ID: "free", Pricing: &domain.Pricing{}},
		{ID: "unpriced"},
		{ID: "huge", Pricing: &domain.Pricing{PromptPerM: math.MaxFloat64, CompletionPerM: math.MaxFloat64}},
	}}
	ptr := func(f float64) *float64 { return &f }
	tests := []struct {
		name    string
		model   string
		in, out int
		want    *float64
	}{
		{"AC12 manual price", "priced", 1_000_000, 500_000, ptr(6.0)},
		{"zero price is zero cost", "free", 100, 100, ptr(0)},
		{"model without price", "unpriced", 100, 100, nil},
		{"model without entry (gpt-4.1)", "gpt-4.1", 100, 100, nil},
		{"negative tokens", "priced", -1, 1, nil},
		{"overflow is not finite", "huge", 1 << 30, 1 << 30, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.Estimate(tc.model, tc.in, tc.out)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Estimate(%q, %d, %d) mismatch (-want +got):\n%s", tc.model, tc.in, tc.out, diff)
			}
		})
	}
}

func TestMergeHeaders(t *testing.T) {
	v := func(s string) *string { return &s }
	stored := []domain.HeaderValue{{Name: "X-Tenant", Value: "TENANT-SECRET-9"}, {Name: "X-Old", Value: "OLD-SECRET-1"}}
	tests := []struct {
		name       string
		listed     []domain.HeaderInput
		want       []domain.HeaderValue
		wantIssues []string
	}{
		{name: "kept in another case keeps the listed spelling",
			listed: []domain.HeaderInput{{Name: "x-tenant"}},
			want:   []domain.HeaderValue{{Name: "x-tenant", Value: "TENANT-SECRET-9"}}},
		{name: "new value replaces",
			listed: []domain.HeaderInput{{Name: "X-Tenant", Value: v("NEW-SECRET-2")}},
			want:   []domain.HeaderValue{{Name: "X-Tenant", Value: "NEW-SECRET-2"}}},
		{name: "unlisted headers are removed, order is the listed one",
			listed: []domain.HeaderInput{{Name: "X-New", Value: v("N")}, {Name: "X-Tenant"}},
			want:   []domain.HeaderValue{{Name: "X-New", Value: "N"}, {Name: "X-Tenant", Value: "TENANT-SECRET-9"}}},
		{name: "new name without value is refused",
			listed:     []domain.HeaderInput{{Name: "X-Tenant"}, {Name: "X-Fresh"}},
			wantIssues: []string{"extra_headers.1.value"}},
		{name: "none listed", listed: nil, want: []domain.HeaderValue{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, issues := domain.MergeHeaders(tc.listed, stored)
			if diff := cmp.Diff(tc.wantIssues, paths(issues)); diff != "" {
				t.Fatalf("MergeHeaders() issue paths mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("MergeHeaders() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUsageEmpty(t *testing.T) {
	tests := []struct {
		name string
		u    domain.Usage
		want bool
	}{
		{"zero", domain.Usage{}, true},
		{"agent", domain.Usage{Agents: []domain.AgentRef{{ID: "1", Name: "A"}}}, false},
		{"feature", domain.Usage{FeatureModels: []string{"conventions"}}, false},
		{"default model", domain.Usage{DefaultModel: true}, false},
	}
	for _, tc := range tests {
		if got := tc.u.Empty(); got != tc.want {
			t.Errorf("%s: Empty() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
