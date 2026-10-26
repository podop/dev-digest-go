package llm_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
)

// Options.PriceReportedUsageOnly (SCR7): a built-in attempt whose response carried no usage is
// unpriced; off, the price book prices the estimated tokens as the studio's ledger always did.
func TestBuiltIn_PriceReportedUsageOnly(t *testing.T) {
	t.Parallel()
	prices := fixedPricer{cost: map[string]float64{"gpt-4.1": 7}}
	reported := &llm.Usage{TokensIn: 100, TokensOut: 50}
	tests := []struct {
		name         string
		provider     llm.Provider
		reportedOnly bool
		result       llm.AttemptResult
		want         *float64
	}{
		{"on: estimated tokens are not priced", llm.OpenAI, true, llm.AttemptResult{Raw: validAnswer}, nil},
		{"on: estimated tokens are not priced (anthropic)", llm.Anthropic, true, llm.AttemptResult{Raw: validAnswer}, nil},
		{"on: reported tokens are priced", llm.OpenAI, true, llm.AttemptResult{Raw: validAnswer, Usage: reported}, costPtr(7)},
		{"on: reported tokens are priced (anthropic)", llm.Anthropic, true, llm.AttemptResult{Raw: validAnswer, Usage: reported}, costPtr(7)},
		{"on: the provider's own cost wins even without usage", llm.OpenAI, true, llm.AttemptResult{Raw: validAnswer, CostUSD: costPtr(0.5)}, costPtr(0.5)},
		{"off: estimated tokens are priced", llm.OpenAI, false, llm.AttemptResult{Raw: validAnswer}, costPtr(7)},
		{"off: reported tokens are priced", llm.OpenAI, false, llm.AttemptResult{Raw: validAnswer, Usage: reported}, costPtr(7)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) { return tc.result, nil }}
			c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey, "ANTHROPIC_API_KEY", "ak-test-key"),
				func(o *llm.Options) {
					o.Recorder = rec
					o.Prices = prices
					o.PriceReportedUsageOnly = tc.reportedOnly
				})
			req := chargedRequest()
			req.Provider = tc.provider
			req.Model = "gpt-4.1"
			var seen *float64
			req.OnUsage = func(u llm.AttemptUsage) { seen = u.CostUSD }
			res, err := c.Structured(t.Context(), req)
			if err != nil {
				t.Fatalf("Structured() error = %v", err)
			}
			recs := rec.records()
			if len(recs) != 1 {
				t.Fatalf("RecordUsage calls = %d, want 1", len(recs))
			}
			for name, got := range map[string]*float64{"RecordUsage": recs[0].CostUSD, "OnUsage": seen, "Result": res.CostUSD} {
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("%s cost mismatch (-want +got):\n%s", name, diff)
				}
			}
		})
	}
}
