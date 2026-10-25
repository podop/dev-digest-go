package app

import (
	"context"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/spend/domain"
)

// CodeCapReached is the wire code of the 402 answer.
const CodeCapReached = "spend_cap_reached"

// The operations that run the cap check, as passed to Admit and logged with a 402.
const (
	OpReviewStart        = "review_start"
	OpEvalAgentStart     = "eval_agent_start"
	OpEvalSkillStart     = "eval_skill_start"
	OpEvalRunAll         = "eval_run_all"
	OpConventionsExtract = "conventions_extract"
	OpIntentRefresh      = "intent_refresh"
	OpBriefGenerate      = "brief_generate"
	OpOnboardingGenerate = "onboarding_generate"
	OpAutoReviewStart    = "auto_review_start"
)

// API is the spend use cases as transport and the entry-point contexts see them; *Service
// implements it. Failures are apperr errors that httpx renders.
type API interface {
	// Status answers GET /settings/spend: the current UTC month's spend and the cap. A read
	// error is returned wrapped (500 internal_error at the edge).
	Status(ctx context.Context, workspaceID string) (domain.Status, error)
	// Admit is the cap check of one LLM entry point (op is one of the Op constants): nil when
	// the cap is unset or not reached; the CapReached error (402) when it is reached, after one
	// INFO log line; any read error wrapped, so the request fails closed (500) and no work starts.
	Admit(ctx context.Context, workspaceID, op string) error
}

// CapReached is the 402 spend_cap_reached error for a status whose cap is reached; its details
// are {cap_usd, spent_usd, month_start} (spec section 7). httpx renders it; Admit returns it.
func CapReached(s domain.Status) *apperr.Error {
	var capUSD float64
	if s.Cap != nil {
		capUSD = s.Cap.USD()
	}
	return apperr.PaymentRequired(s.Message()).WithCode(CodeCapReached).WithDetails(map[string]any{
		"cap_usd":     capUSD,
		"spent_usd":   s.Spent.USD(),
		"month_start": s.MonthStart.UTC().Format(monthStartLayout),
	})
}

// monthStartLayout is the wire format of month_start: ISO 8601 UTC with milliseconds, like
// JavaScript's Date.toISOString.
const monthStartLayout = "2006-01-02T15:04:05.000Z"
