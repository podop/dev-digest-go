// Package gitlabconnhttp serves the GitLab connection routes (spec §7, FR3, FR4, FR7). It is the
// only place of the context that knows Echo, JSON field names and status codes: it decodes the
// body by type and key (parse.go), leaves every rule on the values to the service, and answers
// with the wire shape of dto.go, which never holds a token. The Host and Origin guards, the
// JSON-only body rule, the global rate limit and the error envelope are those of httpx.New
// (NFR6); the service's application errors pass through unchanged.
package gitlabconnhttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/platform/httpx"
)

// service is what the handlers need from the GitLab connection use cases (*app.Service). The
// token of a request travels as an app.TokenInput; the service validates every value and answers
// an id that is no UUID, or unknown, with 404 gitlab_connection_not_found.
type service interface {
	List(ctx context.Context) ([]app.View, error)
	Create(ctx context.Context, in app.CreateInput) (app.View, error)
	Update(ctx context.Context, id string, in app.UpdateInput) (app.View, error)
	Delete(ctx context.Context, id string) (disconnected int, err error)
}

var _ service = (*app.Service)(nil)

type handler struct {
	svc service
}

// AddRoutes registers:
//
//	GET    /settings/gitlab-connections        200 GitLabConnection[]
//	POST   /settings/gitlab-connections        201 GitLabConnection
//	PUT    /settings/gitlab-connections/:id    200 GitLabConnection
//	DELETE /settings/gitlab-connections/:id    200 {deleted, disconnected_repos}
//
// :id is read with httpx.PathParam. The routes use the global rate limit.
func AddRoutes(e *echo.Echo, svc service) {
	h := &handler{svc: svc}
	e.GET("/settings/gitlab-connections", h.list)
	e.POST("/settings/gitlab-connections", h.create)
	e.PUT("/settings/gitlab-connections/:id", h.update)
	e.DELETE("/settings/gitlab-connections/:id", h.remove)
}

func (h *handler) list(c *echo.Context) error {
	views, err := h.svc.List(c.Request().Context())
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
	view, err := h.svc.Create(c.Request().Context(), in)
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
	view, err := h.svc.Update(c.Request().Context(), id, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponse(view))
}

func (h *handler) remove(c *echo.Context) error {
	id := httpx.PathParam(c, "id")
	disconnected, err := h.svc.Delete(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, deletedResponse{Deleted: id, DisconnectedRepos: disconnected})
}
