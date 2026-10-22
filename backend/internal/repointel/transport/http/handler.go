// Package httpapi exposes the two repo-intel routes (GET /repos/:id/index-state and
// POST /repos/:id/resync). It is the only place of the context that knows Echo and status codes;
// the JSON of the index state is the wire contract carried by domain.StateView.
package httpapi

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// service is what the handlers need from the repo-intel use cases.
type service interface {
	// IndexState returns the state of the repo; a repo without a row, or a failed read, yields
	// the synthesised state. It never fails.
	IndexState(ctx context.Context, repoID string) domain.StateView
	// Resync enqueues a resync job of the repo in the workspace and returns the job id.
	Resync(ctx context.Context, workspaceID, repoID string) (jobID string, err error)
}

// resolver finds the workspace a request acts as.
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the repo-intel routes.
type Handler struct {
	svc     service
	tenants resolver
}

// AddRoutes registers GET /repos/:id/index-state and POST /repos/:id/resync.
func AddRoutes(e *echo.Echo, svc service, tenants resolver) {
	h := &Handler{svc: svc, tenants: tenants}
	e.GET("/repos/:id/index-state", h.indexState)
	e.POST("/repos/:id/resync", h.resync)
}

// resyncAccepted is the 202 body of an enqueued resync.
type resyncAccepted struct {
	Status string `json:"status"`
	JobID  string `json:"jobId"`
}

// resyncDegraded is the 202 body when the job could not be enqueued; the UI keeps polling the
// index state.
type resyncDegraded struct {
	Status   string `json:"status"`
	Degraded bool   `json:"degraded"`
	Reason   string `json:"reason"`
}

func (h *Handler) indexState(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.svc.IndexState(c.Request().Context(), repoID))
}

// resync never reads the request body: whatever a caller sends is ignored.
func (h *Handler) resync(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	id, err := h.tenants.Resolve(ctx)
	if err != nil {
		return err
	}
	jobID, err := h.svc.Resync(ctx, id.WorkspaceID, repoID)
	if err != nil {
		return c.JSON(http.StatusAccepted, resyncDegraded{Status: "accepted", Degraded: true, Reason: "no_handler"})
	}
	return c.JSON(http.StatusAccepted, resyncAccepted{Status: "accepted", JobID: jobID})
}
