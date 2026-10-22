// Package reviewshttp serves the 15 review routes (spec 7.2): starting runs, the live log over
// Server-Sent Events, cancel and delete, traces, reviews, finding actions, the multi-agent read
// model and Learn. It is the only place of the context that knows Echo and status codes; it
// depends on the use cases only through app.API and owns the wire shapes (dto.go, trace.go).
package reviewshttp

import (
	"context"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/tenancy"
)

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	api     app.API
	ids     identities
	streams *streamSlots
}

// AddRoutes registers the review routes:
//
//	POST   /pulls/:id/review          ReviewRunResponse (10 requests/min bucket, set up in httpx)
//	GET    /runs/:id/events           text/event-stream (exempt from rate limiting, set up in httpx)
//	GET    /pulls/:id/runs/active     ActiveRun[]
//	GET    /pulls/:id/runs            RunSummary[]
//	DELETE /runs/:id                  {ok}
//	POST   /runs/:id/cancel           {ok}
//	GET    /runs/:id/trace            RunTrace
//	GET    /pulls/:id/reviews         ReviewRecord[]
//	DELETE /reviews/:id               {ok}
//	POST   /findings/:id/accept       {finding}
//	POST   /findings/:id/dismiss      {finding}
//	GET    /multi-agent-runs/:id      MultiAgentRun
//	GET    /repos/:id/multi-agent-runs?limit=  MultiAgentRunSummary[]
//	GET    /runs/estimates            AgentRunEstimate[]
//	POST   /findings/:id/learn        {finding, memoryId}
//
// The paths are the keys of the rate-limit configuration in the composition root: keep them.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids, streams: newStreamSlots()}
	e.POST("/pulls/:id/review", h.start)
	e.GET("/runs/:id/events", h.events)
	e.GET("/pulls/:id/runs/active", h.activeRuns)
	e.GET("/pulls/:id/runs", h.listRuns)
	e.DELETE("/runs/:id", h.deleteRun)
	e.POST("/runs/:id/cancel", h.cancelRun)
	e.GET("/runs/:id/trace", h.trace)
	e.GET("/pulls/:id/reviews", h.reviews)
	e.DELETE("/reviews/:id", h.deleteReview)
	e.POST("/findings/:id/accept", h.acceptFinding)
	e.POST("/findings/:id/dismiss", h.dismissFinding)
	e.GET("/multi-agent-runs/:id", h.multiAgentRun)
	e.GET("/repos/:id/multi-agent-runs", h.listMultiAgentRuns)
	e.GET("/runs/estimates", h.estimates)
	e.POST("/findings/:id/learn", h.learn)
}

// scope validates the :id path param (422 unless a UUID) and resolves the workspace.
func (h *handler) scope(c *echo.Context) (id, workspaceID string, err error) {
	id, err = httpx.PathUUID(c, "id")
	if err != nil {
		return "", "", err
	}
	workspaceID, err = h.workspace(c)
	if err != nil {
		return "", "", err
	}
	return id, workspaceID, nil
}

func (h *handler) workspace(c *echo.Context) (string, error) {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return ident.WorkspaceID, nil
}
