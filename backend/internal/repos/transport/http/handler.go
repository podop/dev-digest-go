// Package httpapi exposes the repos and workspace routes. It is the only place of the repos
// context that knows Echo, JSON field names and status codes.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// service is what the handlers need from the repos use cases.
type service interface {
	Add(ctx context.Context, workspaceID, userID string, ref domain.Ref) (repo domain.Repo, created bool, err error)
	List(ctx context.Context, workspaceID string) ([]domain.Repo, error)
	Refresh(ctx context.Context, workspaceID, id string) error
	Delete(ctx context.Context, workspaceID, id string) error
	Overview(ctx context.Context, workspaceID string) (app.Overview, error)
}

// resolver finds the workspace and user a request acts as.
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the repos routes.
type Handler struct {
	svc     service
	tenants resolver
}

// AddRoutes registers POST /repos, GET /repos, POST /repos/:id/refresh, DELETE /repos/:id and
// GET /workspace.
func AddRoutes(e *echo.Echo, svc service, tenants resolver) {
	h := &Handler{svc: svc, tenants: tenants}
	e.POST("/repos", h.add)
	e.GET("/repos", h.list)
	e.POST("/repos/:id/refresh", h.refresh)
	e.DELETE("/repos/:id", h.remove)
	e.GET("/workspace", h.workspace)
}

// repoResponse is the wire shape of a repo (contract: Repo in platform.ts); all keys are
// always present.
type repoResponse struct {
	ID            string      `json:"id"`
	WorkspaceID   string      `json:"workspace_id"`
	Owner         string      `json:"owner"`
	Name          string      `json:"name"`
	FullName      string      `json:"full_name"`
	DefaultBranch string      `json:"default_branch"`
	ClonePath     *string     `json:"clone_path"`
	LastPolledAt  *httpx.Time `json:"last_polled_at"`
	CreatedBy     *string     `json:"created_by"`
}

func toRepoResponse(r domain.Repo) repoResponse {
	return repoResponse{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Owner: r.Owner, Name: r.Name, FullName: r.FullName,
		DefaultBranch: r.DefaultBranch, ClonePath: r.ClonePath, LastPolledAt: wireTime(r.LastPolledAt),
		CreatedBy: r.CreatedBy,
	}
}

func wireTime(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	w := httpx.NewTime(*t)
	return &w
}

type workspaceRepo struct {
	ID           string      `json:"id"`
	FullName     string      `json:"full_name"`
	ClonePath    *string     `json:"clone_path"`
	LastPolledAt *httpx.Time `json:"last_polled_at"`
	Cloned       bool        `json:"cloned"`
}

// workspaceResponse has camelCase workspaceId and cloneDir, as the contract demands.
type workspaceResponse struct {
	WorkspaceID string          `json:"workspaceId"`
	CloneDir    string          `json:"cloneDir"`
	Repos       []workspaceRepo `json:"repos"`
}

func (h *Handler) add(c *echo.Context) error {
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	rawURL, err := parseAddBody(bytes.NewReader(body))
	if err != nil {
		return err
	}
	ref, err := domain.ParseURL(rawURL)
	if err != nil {
		return httpx.NewValidationError(httpx.Issue{Code: "custom", Path: []any{"url"}, Message: domain.InvalidURLMessage})
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	repo, created, err := h.svc.Add(c.Request().Context(), id.WorkspaceID, id.UserID, ref)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, toRepoResponse(repo))
}

func (h *Handler) list(c *echo.Context) error {
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	repos, err := h.svc.List(c.Request().Context(), id.WorkspaceID)
	if err != nil {
		return err
	}
	out := make([]repoResponse, len(repos))
	for i, r := range repos {
		out[i] = toRepoResponse(r)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) refresh(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	if err := h.svc.Refresh(c.Request().Context(), id.WorkspaceID, repoID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "refreshing"})
}

func (h *Handler) remove(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), id.WorkspaceID, repoID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"deleted": repoID})
}

func (h *Handler) workspace(c *echo.Context) error {
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	ov, err := h.svc.Overview(c.Request().Context(), id.WorkspaceID)
	if err != nil {
		return err
	}
	repos := make([]workspaceRepo, len(ov.Repos))
	for i, r := range ov.Repos {
		repos[i] = workspaceRepo{
			ID: r.ID, FullName: r.FullName, ClonePath: r.ClonePath,
			LastPolledAt: wireTime(r.LastPolledAt), Cloned: r.Cloned(),
		}
	}
	return c.JSON(http.StatusOK, workspaceResponse{WorkspaceID: ov.WorkspaceID, CloneDir: ov.CloneDir, Repos: repos})
}

// parseAddBody validates the POST /repos body like the Node schema {url: string}: errors are
// 422 issues in Zod's wording.
func parseAddBody(body io.Reader) (string, error) {
	var doc any
	dec := json.NewDecoder(body)
	if err := dec.Decode(&doc); err != nil {
		// No body at all is a missing object; any other unparsable text is a string.
		if errors.Is(err, io.EOF) {
			return "", issue("invalid_type", nil, "Required")
		}
		return "", issue("invalid_type", nil, "Expected object, received string")
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "", issue("invalid_type", nil, fmt.Sprintf("Expected object, received %s", zodType(doc)))
	}
	v, present := obj["url"]
	if !present {
		return "", issue("invalid_type", []any{"url"}, "Required")
	}
	s, ok := v.(string)
	if !ok {
		return "", issue("invalid_type", []any{"url"}, fmt.Sprintf("Expected string, received %s", zodType(v)))
	}
	return s, nil
}

func issue(code string, path []any, msg string) error {
	return httpx.NewValidationError(httpx.Issue{Code: code, Path: path, Message: msg})
}

// zodType names a decoded JSON value the way Zod's "received" does.
func zodType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "object"
	}
}
