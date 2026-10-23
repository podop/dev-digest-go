// Package cihttp serves the four ci routes (spec 7.2): export an agent to GitHub Actions, list an
// agent's installations, list CI runs and pull new results from GitHub. It is the only place of
// the context that knows Echo and status codes; it validates every request before any use case
// runs, depends on the use cases only through app.Exporter and app.Ingest and owns the wire
// shapes (dto.go). Failures of the use cases are apperr errors that httpx renders.
package cihttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	exporter app.Exporter
	ingest   app.Ingest
	ids      identities
}

// AddRoutes registers the ci routes:
//
//	POST /agents/:id/export-ci          CiExport: 200 for action "files", 201 for "open_pr"
//	GET  /agents/:id/ci-installations   {installations}
//	GET  /ci/runs?agent_id=&limit=      {runs}
//	POST /ci/refresh                    CiRefreshResult; no body is {}
func AddRoutes(e *echo.Echo, exporter app.Exporter, ingest app.Ingest, ids identities) {
	h := &handler{exporter: exporter, ingest: ingest, ids: ids}
	e.POST("/agents/:id/export-ci", h.export)
	e.GET("/agents/:id/ci-installations", h.installations)
	e.GET("/ci/runs", h.runs)
	e.POST("/ci/refresh", h.refresh)
}

func (h *handler) export(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id") // params are validated before the body
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	req, issues := parseExportBody(raw)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	res, err := h.exporter.Export(c.Request().Context(), ws, id, req)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if req.Action == domain.ActionOpenPR {
		status = http.StatusCreated
	}
	return c.JSON(status, toExport(res))
}

func (h *handler) installations(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	list, err := h.exporter.ListInstallations(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toInstallations(list))
}

func (h *handler) runs(c *echo.Context) error {
	filter, issues := parseRunsQuery(c)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	list, err := h.ingest.ListRuns(c.Request().Context(), ws, filter)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toRuns(list))
}

func (h *handler) refresh(c *echo.Context) error {
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	agentID, issues := parseRefreshBody(raw)
	if len(issues) > 0 {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	res, err := h.ingest.Refresh(c.Request().Context(), ws, agentID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toRefresh(res))
}

func (h *handler) workspace(c *echo.Context) (string, error) {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return ident.WorkspaceID, nil
}
