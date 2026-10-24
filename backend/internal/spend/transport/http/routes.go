// Package spendhttp serves GET /settings/spend (spec FR12): the month's spend against the cap.
// It is the only place of the context that knows Echo; it depends on the use case only through
// app.API and owns the wire shape (dto.go).
package spendhttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/spend/app"
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

// AddRoutes registers GET /settings/spend. Registering it on the Echo built by httpx.New puts it
// behind the same Host, Origin and rate-limit guards as every other route.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/settings/spend", h.status)
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
