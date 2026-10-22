package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func intentStr(s string) *string { return &s }

func TestRenderIntent(t *testing.T) {
	tests := []struct {
		name string
		in   domain.Intent
		want string
	}{
		{
			name: "text only",
			in:   domain.Intent{Text: "Add a limiter."},
			want: "Add a limiter.",
		},
		{
			name: "everything, explicit",
			in: domain.Intent{
				Text: "Add a limiter.", ChangeType: intentStr("feature"), Confidence: intentStr("high"), DerivedFrom: "explicit",
				InScope: []string{"limiter", "config"}, OutOfScope: []string{"auth"},
			},
			want: "Add a limiter.\nChange type: feature\nConfidence: high\nIn scope:\n- limiter\n- config\nOut of scope:\n- auth",
		},
		{
			name: "inferred adds the note after the confidence",
			in:   domain.Intent{Text: "Guess.", Confidence: intentStr("low"), DerivedFrom: domain.DerivedInferred, InScope: []string{"a"}},
			want: "Guess.\nConfidence: low (inferred — no substantive PR description or linked ticket/doc; guessed from commits/branch/changed paths)\nIn scope:\n- a",
		},
		{
			name: "inferred without confidence has no note",
			in:   domain.Intent{Text: "Guess.", DerivedFrom: domain.DerivedInferred},
			want: "Guess.",
		},
		{
			name: "empty change type and confidence count as unset",
			in:   domain.Intent{Text: "x", ChangeType: intentStr(""), Confidence: intentStr("")},
			want: "x",
		},
		{
			name: "only out of scope",
			in:   domain.Intent{Text: "x", OutOfScope: []string{"y"}},
			want: "x\nOut of scope:\n- y",
		},
		{
			name: "blank text keeps its line",
			in:   domain.Intent{ChangeType: intentStr("docs")},
			want: "\nChange type: docs",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.RenderIntent(tc.in); got != tc.want {
				t.Errorf("RenderIntent(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The rule is the verbatim reviewer-core text; a drift here changes every review prompt. The
// digest is the SHA-256 of Node's INTENT_SCOPE_RULE (reviewer-core src/prompt.ts at d1c9093),
// 782 UTF-16 units.
func TestIntentScopeRule(t *testing.T) {
	const wantSHA256 = "1940af4c5626a379784f510ad60fe6ffb773f1c29ec4ed70951590f63082e86c"
	sum := sha256.Sum256([]byte(domain.IntentScopeRule))
	if got := hex.EncodeToString(sum[:]); got != wantSHA256 {
		t.Errorf("sha256(IntentScopeRule) = %s, want %s", got, wantSHA256)
	}
	if got := domain.Len16(domain.IntentScopeRule); got != 782 {
		t.Errorf("Len16(IntentScopeRule) = %d, want 782", got)
	}
}
