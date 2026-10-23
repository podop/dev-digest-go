// Package leaderboardhttp serves GET /leaderboard (spec 7). It is the only place of the context
// that knows Echo and status codes; it validates the query before any use case runs, depends on
// the use case only through app.API and owns the wire shapes (dto.go). The route only reads
// (NFR2) and has no body, so it takes no body rules; the Host and Origin guards, the CORS
// policy and the error envelope are those of httpx.New (NFR5).
package leaderboardhttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/leaderboard/app"
	"dev-digest-go/backend/internal/leaderboard/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	api app.API
	ids identities
}

// AddRoutes registers the leaderboard route:
//
//	GET /leaderboard?period=&tz=&repo=   Leaderboard
//
// Echo answers HEAD for the GET route (httpx.New); every other method is not served.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/leaderboard", h.leaderboard)
}

func (h *handler) leaderboard(c *echo.Context) error {
	q, err := query(c)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	lb, err := h.api.Leaderboard(c.Request().Context(), ident.WorkspaceID, q)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toLeaderboard(lb))
}

// query reads and validates ?period=&tz=&repo=: 422 validation_error with one issue per invalid
// or unknown key (domain.ParseQuery).
func query(c *echo.Context) (domain.Query, error) {
	q, issues := domain.ParseQuery(c.QueryParams())
	if len(issues) == 0 {
		return q, nil
	}
	out := make([]httpx.Issue, len(issues))
	for i, is := range issues {
		out[i] = httpx.Issue{Code: issueCode(is.Param), Path: []any{is.Param}, Message: is.Message}
	}
	return domain.Query{}, httpx.NewValidationError(out...)
}

// issueCode is the Zod-style kind of an issue by the query key it is about.
func issueCode(param string) string {
	switch param {
	case domain.ParamPeriod:
		return "invalid_enum_value"
	case domain.ParamTZ, domain.ParamRepo:
		return "invalid_string"
	default:
		return "unrecognized_keys"
	}
}
