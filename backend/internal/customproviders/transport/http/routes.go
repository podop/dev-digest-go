// Package customprovidershttp serves the custom model provider routes (spec §7, FR2). It is the
// only place of the context that knows Echo, JSON field names and status codes: it decodes the
// body by type (validate.go), leaves every rule on the values to the service (domain.Validate),
// and answers with the wire shape of dto.go, which never holds a key or a header value. The Host
// and Origin guards, the JSON-only body rule, the global rate limit and the error envelope are
// those of httpx.New (NFR3); the service's application errors pass through unchanged.
package customprovidershttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// service is what the handlers need from the custom providers use cases (*app.Service).
type service interface {
	List(ctx context.Context, workspaceID string) ([]app.View, error)
	Create(ctx context.Context, workspaceID string, in domain.Input) (app.View, error)
	Update(ctx context.Context, workspaceID, id string, in domain.Input) (app.View, error)
	Delete(ctx context.Context, workspaceID, id string) (string, error)
}

var _ service = (*app.Service)(nil)

// resolver finds the workspace a request acts as (*tenancy.Resolver).
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	svc     service
	tenants resolver
}

// AddRoutes registers:
//
//	GET    /settings/custom-providers        200 CustomProvider[]
//	POST   /settings/custom-providers        201 CustomProvider
//	PUT    /settings/custom-providers/:id    200 CustomProvider
//	DELETE /settings/custom-providers/:id    200 {deleted}
//
// :id is read with httpx.PathParam, so `custom:synapse` and `custom%3Asynapse` address the same
// provider. The routes use the global rate limit; only the connection test has its own bucket
// (spec §7), and it belongs to the settings routes.
func AddRoutes(e *echo.Echo, svc service, tenants resolver) {
	h := &handler{svc: svc, tenants: tenants}
	e.GET("/settings/custom-providers", h.list)
	e.POST("/settings/custom-providers", h.create)
	e.PUT("/settings/custom-providers/:id", h.update)
	e.DELETE("/settings/custom-providers/:id", h.remove)
}

func (h *handler) list(c *echo.Context) error {
	ident, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	views, err := h.svc.List(c.Request().Context(), ident.WorkspaceID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponses(views))
}

func (h *handler) create(c *echo.Context) error {
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	in, issues := parseCreate(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ident, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	view, err := h.svc.Create(c.Request().Context(), ident.WorkspaceID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toResponse(view))
}

func (h *handler) update(c *echo.Context) error {
	id := httpx.PathParam(c, "id")
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	in, issues := parseUpdate(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ident, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	view, err := h.svc.Update(c.Request().Context(), ident.WorkspaceID, id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponse(view))
}

func (h *handler) remove(c *echo.Context) error {
	id := httpx.PathParam(c, "id")
	ident, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	deleted, err := h.svc.Delete(c.Request().Context(), ident.WorkspaceID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, deletedResponse{Deleted: deleted})
}
