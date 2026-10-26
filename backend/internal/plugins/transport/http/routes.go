// Package pluginshttp serves the six plugin routes of Settings > Integrations (spec section 7):
// list, export (a .zip download), import (preview or install of an uploaded .zip or .json),
// enable/disable and remove. It is the only place of the plugins context that knows Echo, status
// codes and the wire shapes (dto.go); it depends on the use cases only through app.API.
//
// JSON bodies arrive only through httpx.ReadJSONBody and are checked as strict objects. The
// import body is the one raw body of the API: the composition root lists the route in
// httpx.Options.RawBodies (RawBodies), and the handler reads it with httpx.ReadRawBody, so the
// media type (zip or json only) and the size limits are enforced before the handler runs.
// Origin, Host and rate-limit guards come from httpx.New and RateLimitBuckets.
package pluginshttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// Registered paths of the routes that need a rate bucket or a raw body: the composition root
// keys RateLimitBuckets and RawBodies by exactly these.
const (
	ExportPath = "/plugins/export"
	ImportPath = "/plugins/import"
)

// Rate limits (spec NFR3): import and export have a budget of 10 requests a minute each.
const (
	ImportLimit = 10
	ExportLimit = 10
)

// zipMediaType is the media type of the export answer and of an uploaded archive.
const zipMediaType = "application/zip"

// RateLimitBuckets are the budgets these routes need; pass them in httpx.RateLimit.Buckets when
// building the router. Both count per client (IP), each route on its own counter.
func RateLimitBuckets() []httpx.Bucket {
	return []httpx.Bucket{
		{Method: http.MethodPost, Path: ImportPath, Limit: ImportLimit},
		{Method: http.MethodPost, Path: ExportPath, Limit: ExportLimit},
	}
}

// RawBodies is the httpx.Options.RawBodies entry of the import route: an archive up to
// domain.ArchiveMaxZipBytes (5 MiB) or a bundle up to domain.MaxInflatedBytes (20 MiB); any other
// media type is a 415.
func RawBodies() map[string]map[string]int64 {
	return map[string]map[string]int64{
		http.MethodPost + " " + ImportPath: {
			zipMediaType:             domain.ArchiveMaxZipBytes,
			echo.MIMEApplicationJSON: domain.MaxInflatedBytes,
		},
	}
}

// identities resolves the workspace and user a request acts as (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	svc app.API
	ids identities
}

// AddRoutes registers the plugin routes:
//
//	GET    /plugins              {plugins, last_export}
//	POST   /plugins/export       200 application/zip download, body PluginExportRequest (own bucket)
//	POST   /plugins/import       ?dry_run=true|false&filename=; zip or json raw body (own bucket)
//	                             dry run 200 preview, install 201 {installed, created}
//	PATCH  /plugins/:id          200 plugin row, body {enabled}
//	DELETE /plugins/:id          200 {ok, removed}, body {confirm}
//
// Failures are the standard envelope; app.API returns application errors, rendered by httpx.
func AddRoutes(e *echo.Echo, svc app.API, ids identities) {
	h := &handler{svc: svc, ids: ids}
	e.GET("/plugins", h.list)
	e.POST(ExportPath, h.export)
	e.POST(ImportPath, h.importPlugin)
	e.PATCH("/plugins/:id", h.setEnabled)
	e.DELETE("/plugins/:id", h.remove)
}

func (h *handler) list(c *echo.Context) error {
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	listing, err := h.svc.List(c.Request().Context(), ident.WorkspaceID)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toList(listing))
}

// export answers the archive as a download. Content-Disposition is exposed to cross-origin
// scripts (the UI on :3100 reads the file name from an API on :3101); no-store keeps the
// content out of every cache.
func (h *handler) export(c *echo.Context) error {
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	in, err := parseExportBody(raw)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	res, err := h.svc.Export(c.Request().Context(), ident.WorkspaceID, ident.UserID, in)
	if err != nil {
		return mapError(err)
	}
	hd := c.Response().Header()
	hd.Set("Content-Disposition", contentDisposition(res.FileName))
	hd.Set("Access-Control-Expose-Headers", "Content-Disposition")
	hd.Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, zipMediaType, res.Archive)
}

// importPlugin previews (dry_run=true: 200) or installs (201) the uploaded plugin. The query is
// checked before the body is read: a malformed dry_run is a 400, a bad filename a 422.
func (h *handler) importPlugin(c *echo.Context) error {
	dryRun, err := dryRunQuery(c)
	if err != nil {
		return err
	}
	fileName, err := fileNameQuery(c)
	if err != nil {
		return err
	}
	media, body, err := httpx.ReadRawBody(c)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	in := app.ImportInput{Media: media, Body: body, FileName: fileName}
	if dryRun {
		doc, err := h.svc.Preview(c.Request().Context(), ident.WorkspaceID, in)
		if err != nil {
			return importError(c, err)
		}
		return c.JSON(http.StatusOK, toPreview(doc))
	}
	installed, err := h.svc.Install(c.Request().Context(), ident.WorkspaceID, in)
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusCreated, toInstall(installed))
}

func (h *handler) setEnabled(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	enabled, err := parseEnabledBody(raw)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	p, err := h.svc.SetEnabled(c.Request().Context(), ident.WorkspaceID, id, enabled)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toPlugin(p))
}

func (h *handler) remove(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	confirm, err := parseConfirmBody(raw)
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	removed, err := h.svc.Remove(c.Request().Context(), ident.WorkspaceID, id, confirm)
	if err != nil {
		return mapError(err)
	}
	return c.JSON(http.StatusOK, toRemove(removed))
}

// dryRunQuery reads ?dry_run=: "true" or "false" exactly, absent = false (an install). Any other
// or repeated value is a 400 (a malformed query), not a silent install.
func dryRunQuery(c *echo.Context) (bool, error) {
	values, present := c.QueryParams()["dry_run"]
	if !present {
		return false, nil
	}
	if len(values) == 1 {
		switch values[0] {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, apperr.InvalidInput("dry_run must be true or false")
}

// fileNameQuery reads ?filename=, the uploaded file's name: absent = "" (the plugin has no
// source), present = 1 to 255 characters without control characters, else a 422. Repeated
// values are refused too.
func fileNameQuery(c *echo.Context) (string, error) {
	values, present := c.QueryParams()["filename"]
	if !present {
		return "", nil
	}
	if len(values) != 1 {
		return "", httpx.NewValidationError(httpx.Issue{Code: httpx.CodeCustom, Path: []any{"filename"},
			Message: "filename must be given once"})
	}
	if issues := fileNameIssue(values[0]); len(issues) > 0 {
		return "", httpx.NewValidationError(issues...)
	}
	return values[0], nil
}

// importError renders a failed preview or install. An unsupported media type cannot normally get
// here (the body rules refuse it first) but a use case may report it: it is the 415 envelope of
// the raw-body rule. Everything else goes through mapError.
func importError(c *echo.Context, err error) error {
	if errors.Is(err, domain.ErrUnsupportedMedia) {
		return c.JSON(http.StatusUnsupportedMediaType, httpx.ErrorBody{Error: httpx.ErrorDetail{
			Code:    httpx.CodeUnsupportedMediaType,
			Message: "Unsupported Media Type",
		}})
	}
	return mapError(err)
}

// contentDisposition is `attachment; filename="<name>"`. The service builds the name with
// domain.ExportFileName (letters, digits and . _ -), but a header value is never built from text
// without a check: any other character becomes an underscore, so no quote, CR/LF or path
// separator can get through.
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
