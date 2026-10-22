package app

import (
	"context"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

// genStats is what the generation line reports: ids, counts and enum labels, never PR text. A nil
// field was not reached and is logged as null.
type genStats struct {
	provider, model     *string
	modelRequests       *int
	tokensIn, tokensOut *int
	costUSD             *float64
	estTokensIn         *int
	dropped             *domain.DroppedCounts
	missingInputs       []string
	outcome             string
}

// outcomeOf is the outcome label of a failed generation: the error's code, else internal_error.
func outcomeOf(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Code
	}
	return "internal_error"
}

// logGenerated writes the one generation line: info "pr brief generated" or warn "pr brief
// generation failed".
func (s *Service) logGenerated(ctx context.Context, prID string, st *genStats, took time.Duration) {
	dropped := slog.Any("dropped", nil)
	if st.dropped != nil {
		dropped = slog.Group("dropped",
			"risks", st.dropped.Risks, "review_focus", st.dropped.ReviewFocus, "risk_refs", st.dropped.RiskRefs)
	}
	var missing any
	if st.missingInputs != nil {
		missing = st.missingInputs
	}
	attrs := []any{
		"evt", "brief_generated",
		"prId", prID,
		"provider", orNil(st.provider),
		"model", orNil(st.model),
		"modelRequests", orNil(st.modelRequests),
		"tokensIn", orNil(st.tokensIn),
		"tokensOut", orNil(st.tokensOut),
		"costUsd", orNil(st.costUSD),
		"estTokensIn", orNil(st.estTokensIn),
		dropped,
		"missingInputs", missing,
		"outcome", st.outcome,
		"durationMs", took.Milliseconds(),
	}
	if st.outcome == "ok" {
		s.log.InfoContext(ctx, "pr brief generated", attrs...)
		return
	}
	s.log.WarnContext(ctx, "pr brief generation failed", attrs...)
}

// logModelRequests writes one info line per model response: every response but the last was
// invalid output, the last one is ok or failed.
func (s *Service) logModelRequests(ctx context.Context, prID, model string, estTokensIn int, usages []llm.AttemptUsage, ok bool) {
	for i, u := range usages {
		outcome := "invalid_output"
		if i == len(usages)-1 {
			outcome = "failed"
			if ok {
				outcome = "ok"
			}
		}
		s.log.InfoContext(ctx, "pr brief model request",
			"evt", "brief_model_request",
			"feature", domain.Feature,
			"prId", prID,
			"attempt", i+1,
			"model", model,
			"estTokensIn", estTokensIn,
			"tokensIn", u.TokensIn,
			"tokensOut", u.TokensOut,
			"outcome", outcome,
		)
	}
}

// orNil gives slog a nil for an unset pointer (a JSON null) and the value otherwise.
func orNil[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
