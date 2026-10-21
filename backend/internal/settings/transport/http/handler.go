// Package settingshttp serves the settings routes: GET and PUT /settings,
// GET /settings/secrets-status and POST /settings/test-connection.
package settingshttp

import (
	"context"
	"io"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/settings/app"
	"dev-digest-go/backend/internal/tenancy"
)

// TestConnectionLimit is the per-IP budget of POST /settings/test-connection per window.
const TestConnectionLimit = 20

const testConnectionPath = "/settings/test-connection"

// RateLimitBuckets are the per-route budgets this package needs; pass them as
// httpx.RateLimit.Buckets when building the router.
func RateLimitBuckets() []httpx.Bucket {
	return []httpx.Bucket{{Method: http.MethodPost, Path: testConnectionPath, Limit: TestConnectionLimit}}
}

// service is what the handlers need from the settings use cases (*app.Service).
type service interface {
	Get(ctx context.Context, workspaceID string) (app.Bag, error)
	Update(ctx context.Context, workspaceID, userID string, entries app.Bag) (app.Bag, error)
	SecretsStatus() app.SecretsStatus
	TestConnection(ctx context.Context, provider app.Provider, key string) app.ConnTestResult
}

// identities resolves the single-tenant context (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the settings routes.
type Handler struct {
	svc service
	ids identities
}

// NewHandler builds a Handler.
func NewHandler(svc service, ids identities) *Handler {
	return &Handler{svc: svc, ids: ids}
}

// AddRoutes registers the four settings routes on e.
func AddRoutes(e *echo.Echo, h *Handler) {
	e.GET("/settings", h.get)
	e.PUT("/settings", h.put)
	e.GET("/settings/secrets-status", h.secretsStatus)
	e.POST(testConnectionPath, h.testConnection)
}

func (h *Handler) get(c *echo.Context) error {
	id, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	bag, err := h.svc.Get(c.Request().Context(), id.WorkspaceID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bag)
}

func (h *Handler) put(c *echo.Context) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	entries, issues := parseSettingsUpdate(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	id, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	bag, err := h.svc.Update(c.Request().Context(), id.WorkspaceID, id.UserID, entries)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bag)
}

func (h *Handler) secretsStatus(c *echo.Context) error {
	if _, err := h.ids.Resolve(c.Request().Context()); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.svc.SecretsStatus())
}

// testConnection needs no workspace: it works on a database that was never seeded.
func (h *Handler) testConnection(c *echo.Context) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	req, issues := parseConnTest(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	return c.JSON(http.StatusOK, h.svc.TestConnection(c.Request().Context(), req.Provider, req.Key))
}

// readBody returns the request body; the body rules of httpx already limited its size.
func readBody(c *echo.Context) ([]byte, error) {
	return io.ReadAll(c.Request().Body)
}
