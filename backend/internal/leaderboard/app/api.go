package app

import (
	"context"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

// API is the leaderboard use cases as transport sees them; *Service implements it. The query is
// validated by transport (domain.ParseQuery, 422 validation_error with the issues as details)
// before any use case runs; the workspace id and q.Repo (when not domain.RepoAll) are UUIDs by
// the time they get here. Failures are apperr errors that httpx renders.
type API interface {
	// Leaderboard answers GET /leaderboard: the window of q.Period in q.Location (resolved on
	// the server clock), the repositories of q.Repo, KPIs of the window and of the previous
	// window (none for 30d or when the previous window has no rows), the ranked rows, awards,
	// needs-attention items, hot files, health by size and the activity chart. A repository id that is not in the
	// workspace is 404 "Repo not found"; a workspace without repositories (q.Repo all) answers empty sections and zeroed KPIs
	// without further reads; a database error is 500 internal_error.
	Leaderboard(ctx context.Context, workspaceID string, q domain.Query) (domain.Leaderboard, error)
}
