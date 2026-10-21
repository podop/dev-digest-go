// Package pullshttp serves the pull request routes: the PR list, PR detail, review comments and
// the manual poll. It maps requests to the pulls use cases and results to the PrMeta, PrDetail
// and PrReviewComment wire contracts.
package pullshttp

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// service is what the routes need from the pulls use cases (*app.Service).
type service interface {
	ListForRepo(ctx context.Context, workspaceID, repoID string) ([]app.ListItem, error)
	Detail(ctx context.Context, workspaceID, prID string) (domain.Detail, error)
	Comments(ctx context.Context, workspaceID, prID string) ([]domain.ReviewComment, error)
	PostComment(ctx context.Context, workspaceID, prID string, req app.CommentRequest) (domain.ReviewComment, error)
	Poll(ctx context.Context, workspaceID, repoID string) (app.PollResult, error)
}

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	svc service
	ids identities
}

// AddRoutes registers:
//
//	GET  /repos/:id/pulls     PrMeta[]
//	GET  /pulls/:id           PrDetail
//	GET  /pulls/:id/comments  PrReviewComment[]
//	POST /pulls/:id/comments  PrReviewComment (200)
//	POST /repos/:id/poll      {synced, reviewTriggered}
func AddRoutes(e *echo.Echo, svc service, ids identities) {
	h := &handler{svc: svc, ids: ids}
	e.GET("/repos/:id/pulls", h.list)
	e.GET("/pulls/:id", h.detail)
	e.GET("/pulls/:id/comments", h.comments)
	e.POST("/pulls/:id/comments", h.postComment)
	e.POST("/repos/:id/poll", h.poll)
}

// scope validates the :id path param and resolves the workspace.
func (h *handler) scope(c *echo.Context) (id, workspaceID string, err error) {
	id, err = httpx.PathUUID(c, "id")
	if err != nil {
		return "", "", err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", "", err
	}
	return id, ident.WorkspaceID, nil
}

func (h *handler) list(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	items, err := h.svc.ListForRepo(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toMetas(items))
}

func (h *handler) detail(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Detail(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	if d.FromGitHub {
		return c.JSON(http.StatusOK, toDetail(d, freshFile))
	}
	return c.JSON(http.StatusOK, toDetail(d, storedFile))
}

func (h *handler) comments(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	cs, err := h.svc.Comments(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toComments(cs))
}

func (h *handler) postComment(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return fmt.Errorf("read comment body: %w", err)
	}
	req, issues := parseCommentInput(raw)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	created, err := h.svc.PostComment(c.Request().Context(), ident.WorkspaceID, id, req)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toComment(created))
}

func (h *handler) poll(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	res, err := h.svc.Poll(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, pollDTO{Synced: res.Synced, ReviewTriggered: false})
}
