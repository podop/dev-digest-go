package spendhttp

import (
	"dev-digest-go/backend/internal/spend/domain"
)

// monthStartLayout is the wire format of month_start: ISO 8601 UTC with milliseconds, like
// every other API timestamp.
const monthStartLayout = "2006-01-02T15:04:05.000Z"

// statusResponse is the wire shape of SpendStatus (frontend/src/vendor/shared/contracts/platform.ts).
// Struct field order is the key order of the JSON; the nullable fields have no omitempty.
// Money is in dollars with at most the 6 decimals Micros holds.
type statusResponse struct {
	MonthStart   string   `json:"month_start"`
	SpentUSD     float64  `json:"spent_usd"`
	CapUSD       *float64 `json:"cap_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
	CapReached   bool     `json:"cap_reached"`
}

func toStatusResponse(s domain.Status) statusResponse {
	out := statusResponse{
		MonthStart: s.MonthStart.UTC().Format(monthStartLayout),
		SpentUSD:   s.Spent.USD(),
		CapReached: s.Reached(),
	}
	if s.Cap != nil {
		v := s.Cap.USD()
		out.CapUSD = &v
	}
	if rem, ok := s.Remaining(); ok {
		v := rem.USD()
		out.RemainingUSD = &v
	}
	return out
}
