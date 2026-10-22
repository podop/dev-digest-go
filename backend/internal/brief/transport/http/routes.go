// Package briefhttp serves the PR brief routes (spec 7.2). It is the only place of the context
// that knows Echo and the wire shape; it depends on the use cases only through app.API. The POST
// takes no body and no content type, as the UI sends neither: the handler never reads one.
package briefhttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
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

// AddRoutes registers the brief routes:
//
//	GET  /pulls/:id/brief   PrBriefResponse, never calls a model
//	POST /pulls/:id/brief   PrBriefResponse with stale false: generates, stores and returns
//
// Both answer 422 for an id that is not a UUID and 404 "Pull request not found" for one that is
// not in the workspace. GET handlers stay read-only: Echo runs them for HEAD too.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/pulls/:id/brief", h.get)
	e.POST("/pulls/:id/brief", h.generate)
}

func (h *handler) get(c *echo.Context) error {
	return h.answer(c, h.api.Get)
}

func (h *handler) generate(c *echo.Context) error {
	return h.answer(c, h.api.Generate)
}

func (h *handler) answer(c *echo.Context, call func(ctx context.Context, workspaceID, prID string) (domain.State, error)) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	state, err := call(c.Request().Context(), ident.WorkspaceID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponse(state))
}
