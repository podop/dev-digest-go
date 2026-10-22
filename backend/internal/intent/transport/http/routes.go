// Package intenthttp serves the two intent routes (spec 7.2): the stored PR intent with its
// staleness, and the forced re-derivation. It is the only place of the context that knows Echo
// and status codes; it depends on the use cases only through app.API and owns the wire shapes.
package intenthttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/intent/app"
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

// RefreshPath is the registered path of the refresh route: the composition root gives it a
// 10-requests-per-minute bucket (httpx.Bucket) under exactly this path.
const RefreshPath = "/pulls/:id/intent/refresh"

// AddRoutes registers the intent routes:
//
//	GET  /pulls/:id/intent          PrIntentResponse
//	POST /pulls/:id/intent/refresh  PrIntentResponse (10 requests/min bucket, set up in the composition root)
//
// The POST never reads the request body: whatever a caller sends is ignored.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/pulls/:id/intent", h.get)
	e.POST(RefreshPath, h.refresh)
}

// scope validates the :id path param (422 unless a UUID) and resolves the workspace.
func (h *handler) scope(c *echo.Context) (prID, workspaceID string, err error) {
	prID, err = httpx.PathUUID(c, "id")
	if err != nil {
		return "", "", err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", "", err
	}
	return prID, ident.WorkspaceID, nil
}

func (h *handler) get(c *echo.Context) error {
	prID, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	res, err := h.api.Get(c.Request().Context(), ws, prID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponse(res))
}

func (h *handler) refresh(c *echo.Context) error {
	prID, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	res, err := h.api.Refresh(c.Request().Context(), ws, prID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponse(res))
}
