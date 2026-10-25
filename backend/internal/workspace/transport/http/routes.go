// Package workspacehttp serves the /workspace routes (spec section 7): the workspace info and
// rename, the stats, the export download and the reset of the run history. It is the only place
// of the workspace context that knows Echo; it depends on the use cases through the service
// interface below and owns the wire shapes (dto.go). Registered on the Echo built by httpx.New
// the routes sit behind the same Host, Origin, rate-limit and body guards as every other route.
package workspacehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
)

// service is what the handlers need from the workspace use cases (*app.Service).
type service interface {
	Info(ctx context.Context, workspaceID string) (app.Info, error)
	Rename(ctx context.Context, workspaceID, userID, name string) (app.Info, error)
	Stats(ctx context.Context, workspaceID string) (domain.Stats, error)
	Export(ctx context.Context, workspaceID string) (app.ExportFile, error)
	Reset(ctx context.Context, workspaceID, confirm string) (domain.Deleted, error)
}

// identities resolves the workspace and user a request acts as (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	svc service
	ids identities
}

// AddRoutes registers GET /workspace, PUT /workspace, GET /workspace/stats,
// GET /workspace/export and POST /workspace/reset-history.
func AddRoutes(e *echo.Echo, svc service, ids identities) {
	h := &handler{svc: svc, ids: ids}
	e.GET("/workspace", h.info)
	e.PUT("/workspace", h.rename)
	e.GET("/workspace/stats", h.stats)
	e.GET("/workspace/export", h.export)
	e.POST("/workspace/reset-history", h.resetHistory)
}

func (h *handler) info(c *echo.Context) error {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	info, err := h.svc.Info(c.Request().Context(), ident.WorkspaceID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toInfoResponse(info))
}

func (h *handler) rename(c *echo.Context) error {
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	name, err := parseRenameBody(body)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	info, err := h.svc.Rename(c.Request().Context(), ident.WorkspaceID, ident.UserID, name)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toInfoResponse(info))
}

func (h *handler) stats(c *echo.Context) error {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	st, err := h.svc.Stats(c.Request().Context(), ident.WorkspaceID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toStatsResponse(st))
}

// export answers the bundle as a download. Content-Disposition is exposed to cross-origin
// scripts (the UI on :3100 reads the file name from an API on :3101); no-store keeps the
// workspace content out of every cache.
func (h *handler) export(c *echo.Context) error {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	file, err := h.svc.Export(c.Request().Context(), ident.WorkspaceID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(file.Bundle)
	if err != nil {
		return fmt.Errorf("encode workspace export: %w", err)
	}
	hd := c.Response().Header()
	hd.Set("Content-Disposition", contentDisposition(file.FileName))
	hd.Set("Access-Control-Expose-Headers", "Content-Disposition")
	hd.Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSON, body)
}

func (h *handler) resetHistory(c *echo.Context) error {
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	confirm, err := parseResetBody(body)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	deleted, err := h.svc.Reset(c.Request().Context(), ident.WorkspaceID, confirm)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResetResponse(deleted))
}

// contentDisposition is `attachment; filename="<name>"`. The service builds the name from a
// slug of [a-z0-9-], but a header value is never built from text without a check: any other
// character becomes an underscore, so no quote, CR/LF or path separator can get through.
func contentDisposition(fileName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, fileName)
	return `attachment; filename="` + safe + `"`
}
