// Package performancehttp serves the two performance routes (spec 7.2, 7.11): the cross-agent
// dashboard and one agent's stats. It is the only place of the context that knows Echo and status
// codes; it validates the period query before any use case runs, depends on the use cases only
// through app.API and owns the wire shapes (dto.go).
package performancehttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/performance/app"
	"dev-digest-go/backend/internal/performance/domain"
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

// AddRoutes registers the performance routes:
//
//	GET /agents/performance?period=&from=&to=   AgentPerf
//	GET /agents/:id/stats?period=&from=&to=     AgentStats
//
// /agents/performance is a static route: the router prefers it over /agents/:id and
// /agents/:id/*, so it is never read as an agent id.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/agents/performance", h.dashboard)
	e.GET("/agents/:id/stats", h.stats)
}

func (h *handler) dashboard(c *echo.Context) error {
	q, err := periodQuery(c)
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	perf, err := h.api.Dashboard(c.Request().Context(), ws, q)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toAgentPerf(perf))
}

func (h *handler) stats(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id") // the path param is validated before the query
	if err != nil {
		return err
	}
	q, err := periodQuery(c)
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	stats, err := h.api.Stats(c.Request().Context(), ws, id, q)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toAgentStats(stats))
}

func (h *handler) workspace(c *echo.Context) (string, error) {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return ident.WorkspaceID, nil
}

// periodQuery reads and validates ?period=&from=&to=: 422 validation_error with the issues of
// spec 7.11.1, a plain error (500) for the one instant JavaScript cannot represent. Unknown
// query keys are ignored; a repeated key is a type error.
func periodQuery(c *echo.Context) (domain.PeriodQuery, error) {
	params := c.QueryParams()
	q, issues, err := domain.ParsePeriod(domain.RawPeriod{
		Period: params["period"], From: params["from"], To: params["to"],
	})
	if err != nil {
		return domain.PeriodQuery{}, err
	}
	if len(issues) > 0 {
		out := make([]httpx.Issue, len(issues))
		for i, is := range issues {
			out[i] = httpx.Issue{Code: is.Code, Path: []any{is.Field}, Message: is.Message}
		}
		return domain.PeriodQuery{}, httpx.NewValidationError(out...)
	}
	return q, nil
}
