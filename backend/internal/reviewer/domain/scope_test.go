package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func boolPtr(b bool) *bool { return &b }

// AC4046, EC4012
func TestApplyScopePolicy_WithoutIntentEveryFlagIsNil(t *testing.T) {
	in := []domain.Finding{
		{ID: "t", OutOfScope: boolPtr(true)},
		{ID: "f", OutOfScope: boolPtr(false)},
		{ID: "n"},
	}
	got := domain.ApplyScopePolicy(in, false)
	for _, f := range got.Findings {
		if f.OutOfScope != nil {
			t.Errorf("finding %s: OutOfScope = %v, want nil", f.ID, *f.OutOfScope)
		}
	}
	if got.OutOfScope != 0 || got.OutOfScopeCritical != 0 {
		t.Errorf("counts = %d/%d, want 0/0 without intent", got.OutOfScope, got.OutOfScopeCritical)
	}
	if in[0].OutOfScope == nil || !*in[0].OutOfScope {
		t.Errorf("ApplyScopePolicy modified its input")
	}
}

func TestApplyScopePolicy_WithIntentFlagsBecomeBooleans(t *testing.T) {
	in := []domain.Finding{
		{ID: "t", Severity: domain.SeverityCritical, OutOfScope: boolPtr(true)},
		{ID: "w", Severity: domain.SeverityWarning, OutOfScope: boolPtr(true)},
		{ID: "f", OutOfScope: boolPtr(false)},
		{ID: "n"},
	}
	got := domain.ApplyScopePolicy(in, true)
	var flags []bool
	for _, f := range got.Findings {
		if f.OutOfScope == nil {
			t.Fatalf("finding %s: OutOfScope = nil, want a boolean with intent", f.ID)
		}
		flags = append(flags, *f.OutOfScope)
	}
	if diff := cmp.Diff([]bool{true, true, false, false}, flags); diff != "" {
		t.Errorf("flags mismatch (-want +got):\n%s", diff)
	}
	if got.OutOfScope != 2 || got.OutOfScopeCritical != 1 {
		t.Errorf("counts = %d/%d, want 2/1", got.OutOfScope, got.OutOfScopeCritical)
	}
}
