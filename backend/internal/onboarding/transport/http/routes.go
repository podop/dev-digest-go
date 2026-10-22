// Package onboardinghttp serves the two onboarding routes (spec 7.2, 7.10): reading a
// repository's stored tour and generating one. It is the only place of the context that knows
// Echo and status codes; it depends on the use cases only through app.API and owns the wire
// shapes (dto.go).
package onboardinghttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/onboarding/app"
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

// AddRoutes registers the onboarding routes:
//
//	GET  /repos/:id/onboarding   OnboardingTourState
//	POST /repos/:id/onboarding   OnboardingTourReady (generates; the body is never read)
//
// The POST takes no body and no content type, as the UI sends neither, and ignores any body it
// receives (the shared body rules still apply). A foreign Origin is refused, and the Host
// checked, by httpx.New for every state-changing route.
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/repos/:id/onboarding", h.get)
	e.POST("/repos/:id/onboarding", h.generate)
}

func (h *handler) get(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	state, err := h.api.Get(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toState(state))
}

func (h *handler) generate(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	state, err := h.api.Generate(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toState(state))
}

func (h *handler) workspace(c *echo.Context) (string, error) {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return ident.WorkspaceID, nil
}
