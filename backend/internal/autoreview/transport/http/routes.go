// Package autoreviewhttp serves GET /settings/automatic-reviews-status (spec FR12): the status line
// of automatic reviews. It is the only place of the context that knows Echo; it depends on the use
// case through the small statusReader port and owns the wire shape (dto.go).
package autoreviewhttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/autoreview/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// statusReader is what the route needs of the use cases (satisfied by autoreview/app.API).
type statusReader interface {
	Status(ctx context.Context, workspaceID string) (domain.Status, error)
}

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	api statusReader
	ids identities
}

// AddRoutes registers GET /settings/automatic-reviews-status. Registering it on the Echo built by
// httpx.New puts it behind the same Host, Origin and rate-limit guards as every other route.
func AddRoutes(e *echo.Echo, api statusReader, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/settings/automatic-reviews-status", h.status)
}

func (h *handler) status(c *echo.Context) error {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	st, err := h.api.Status(c.Request().Context(), ident.WorkspaceID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toStatusResponse(st))
}
