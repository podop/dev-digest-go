// Package httpapi exposes the project-context routes: the document list and preview, the agent
// and skill attachments, and the store files. It is the only place of the context that knows
// Echo, JSON field names and status codes.
package httpapi

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/projectcontext/app"
	"dev-digest-go/backend/internal/tenancy"
)

// fileBodyLimit is the body limit of the store-file writes: 256 KB of text escaped as \u00XX
// can reach about six times its size, and 2 MiB covers that with room.
const fileBodyLimit = 2 * 1024 * 1024

const filesPath = "/repos/:id/context/files"

// BodyLimits returns the per-route request body limits for httpx.Options.BodyLimits, keyed by
// "METHOD /route" as registered with Echo.
func BodyLimits() map[string]int64 {
	return map[string]int64{
		http.MethodPost + " " + filesPath: fileBodyLimit,
		http.MethodPut + " " + filesPath:  fileBodyLimit,
	}
}

// service is what the handlers need from the project-context use cases (*app.Service).
type service interface {
	ListDocs(ctx context.Context, workspaceID, repoID string) (app.List, error)
	PreviewDoc(ctx context.Context, workspaceID, repoID, path string) (app.Preview, error)
	AgentContext(ctx context.Context, workspaceID, agentID, repoID string) (app.Attachments, error)
	SkillContext(ctx context.Context, workspaceID, skillID, repoID string) (app.Attachments, error)
	PutAgentContext(ctx context.Context, workspaceID, agentID, repoID string, paths []string) (app.Attachments, error)
	PutSkillContext(ctx context.Context, workspaceID, skillID, repoID string, paths []string) (app.Attachments, error)
	CreateFile(ctx context.Context, workspaceID, repoID string, in app.CreateInput) (app.Preview, error)
	SaveFile(ctx context.Context, workspaceID, repoID, path, content string, baseVersion int64) (app.Preview, error)
	RenameFile(ctx context.Context, workspaceID, repoID, path, newPath string, baseVersion int64) (app.Preview, error)
	DeleteFile(ctx context.Context, workspaceID, repoID, path string) error
}

// resolver finds the workspace a request acts as.
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the project-context routes.
type Handler struct {
	svc     service
	tenants resolver
}

// AddRoutes registers the ten project-context routes.
func AddRoutes(e *echo.Echo, svc service, tenants resolver) {
	h := &Handler{svc: svc, tenants: tenants}
	e.GET("/repos/:id/context", h.list)
	e.GET("/repos/:id/context/doc", h.preview)
	e.GET("/agents/:id/context", h.getAttachments(svc.AgentContext))
	e.PUT("/agents/:id/context", h.putAttachments(svc.PutAgentContext))
	e.GET("/skills/:id/context", h.getAttachments(svc.SkillContext))
	e.PUT("/skills/:id/context", h.putAttachments(svc.PutSkillContext))
	e.POST(filesPath, h.create)
	e.PUT(filesPath, h.save)
	e.POST(filesPath+"/rename", h.rename)
	e.DELETE(filesPath, h.remove)
}

// requiredQuery reads a required query parameter with a rule; a missing one is "Required".
func requiredQuery(c *echo.Context, name string, rule httpx.StringRule) (string, error) {
	if _, ok := c.QueryParams()[name]; !ok {
		return "", httpx.NewValidationError(httpx.RequiredIssue(name))
	}
	v, issues := httpx.QueryString(c, name, rule)
	if issues != nil {
		return "", httpx.NewValidationError(issues...)
	}
	return v, nil
}

func pathQuery(c *echo.Context) (string, error) {
	return requiredQuery(c, "path", httpx.StringRule{Max: maxQueryPath})
}

func (h *Handler) workspaceID(c *echo.Context) (string, error) {
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return id.WorkspaceID, nil
}

func (h *Handler) list(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspaceID(c)
	if err != nil {
		return err
	}
	list, err := h.svc.ListDocs(c.Request().Context(), ws, repoID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toListResponse(list))
}

func (h *Handler) preview(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	path, err := pathQuery(c)
	if err != nil {
		return err
	}
	ws, err := h.workspaceID(c)
	if err != nil {
		return err
	}
	p, err := h.svc.PreviewDoc(c.Request().Context(), ws, repoID, path)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toPreviewResponse(p))
}

func (h *Handler) getAttachments(get func(ctx context.Context, ws, ownerID, repoID string) (app.Attachments, error)) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ownerID, err := httpx.PathUUID(c, "id")
		if err != nil {
			return err
		}
		repoID, err := requiredQuery(c, "repoId", httpx.StringRule{Pattern: uuidPattern, PatternMessage: "Invalid uuid"})
		if err != nil {
			return err
		}
		ws, err := h.workspaceID(c)
		if err != nil {
			return err
		}
		att, err := get(c.Request().Context(), ws, ownerID, repoID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, toAttachmentsResponse(att))
	}
}

func (h *Handler) putAttachments(put func(ctx context.Context, ws, ownerID, repoID string, paths []string) (app.Attachments, error)) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ownerID, err := httpx.PathUUID(c, "id")
		if err != nil {
			return err
		}
		raw, err := httpx.ReadJSONBody(c)
		if err != nil {
			return err
		}
		body, issues := parseAttachBody(raw)
		if issues != nil {
			return httpx.NewValidationError(issues...)
		}
		ws, err := h.workspaceID(c)
		if err != nil {
			return err
		}
		att, err := put(c.Request().Context(), ws, ownerID, body.RepoID, body.Paths)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, toAttachmentsResponse(att))
	}
}

func (h *Handler) create(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	in, issues := parseCreateBody(raw)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspaceID(c)
	if err != nil {
		return err
	}
	p, err := h.svc.CreateFile(c.Request().Context(), ws, repoID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toPreviewResponse(p))
}

func (h *Handler) save(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	body, issues := parseSaveBody(raw)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	path, err := pathQuery(c)
	if err != nil {
		return err
	}
	ws, err := h.workspaceID(c)
	if err != nil {
		return err
	}
	p, err := h.svc.SaveFile(c.Request().Context(), ws, repoID, path, body.Content, body.BaseVersion)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toPreviewResponse(p))
}

func (h *Handler) rename(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	raw, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	body, issues := parseRenameBody(raw)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspaceID(c)
	if err != nil {
		return err
	}
	p, err := h.svc.RenameFile(c.Request().Context(), ws, repoID, body.Path, body.NewPath, body.BaseVersion)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toPreviewResponse(p))
}

func (h *Handler) remove(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	path, err := pathQuery(c)
	if err != nil {
		return err
	}
	ws, err := h.workspaceID(c)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteFile(c.Request().Context(), ws, repoID, path); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
