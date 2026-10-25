package autoreviewhttp

import (
	"dev-digest-go/backend/internal/autoreview/domain"
	"dev-digest-go/backend/internal/platform/httpx"
)

// statusResponse is the wire shape of AutomaticReviewsStatus
// (frontend/src/vendor/shared/contracts/platform.ts). Struct field order is the key order of the
// JSON; last_poll_at is null before the first poll, so it has no omitempty.
type statusResponse struct {
	State           string  `json:"state"`
	LastPollAt      *string `json:"last_poll_at"`
	PRsTracked      int     `json:"prs_tracked"`
	PollIntervalMin int     `json:"poll_interval_min"`
}

func toStatusResponse(s domain.Status) statusResponse {
	out := statusResponse{
		State:           string(s.State),
		PRsTracked:      s.PRsTracked,
		PollIntervalMin: s.PollIntervalMin,
	}
	if s.LastPollAt != nil {
		v := s.LastPollAt.UTC().Format(httpx.TimeLayout)
		out.LastPollAt = &v
	}
	return out
}
