// Package httpapi exposes the agent routes. It is the only place of the agents context that
// knows Echo, JSON field names and status codes.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// service is what the handlers need from the agent use cases.
type service interface {
	List(ctx context.Context, workspaceID string) ([]domain.Agent, error)
	Get(ctx context.Context, workspaceID, id string) (domain.Agent, error)
	Create(ctx context.Context, workspaceID, userID string, in domain.CreateInput) (domain.Agent, error)
	Update(ctx context.Context, workspaceID, id string, p domain.Patch) (domain.Agent, error)
	Delete(ctx context.Context, workspaceID, id string) error
	Versions(ctx context.Context, workspaceID, id string) ([]domain.Version, error)
	Version(ctx context.Context, workspaceID, id string, version int) (domain.Version, error)
	SkillLinks(ctx context.Context, workspaceID, id string) ([]domain.SkillLink, error)
	SetSkills(ctx context.Context, workspaceID, id string, skillIDs []string) ([]domain.SkillLink, error)
	LinkSkill(ctx context.Context, workspaceID, id, skillID string, order *int) ([]domain.SkillLink, error)
	AgentModels(ctx context.Context, workspaceID, id string) ([]llm.ModelInfo, error)
	ProviderModels(ctx context.Context, provider string) []llm.ModelInfo
}

// resolver finds the workspace and user a request acts as.
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the agent routes.
type Handler struct {
	svc     service
	tenants resolver
}

// AddRoutes registers the 11 agent routes: GET/POST /agents, GET/PUT/DELETE /agents/:id,
// GET /agents/:id/versions, GET /agents/:id/versions/:version, GET/POST /agents/:id/skills,
// GET /agents/:id/models and GET /providers/:id/models.
func AddRoutes(e *echo.Echo, svc service, tenants resolver) {
	h := &Handler{svc: svc, tenants: tenants}
	e.GET("/agents", h.list)
	e.POST("/agents", h.create)
	e.GET("/agents/:id", h.get)
	e.PUT("/agents/:id", h.update)
	e.DELETE("/agents/:id", h.remove)
	e.GET("/agents/:id/versions", h.versions)
	e.GET("/agents/:id/versions/:version", h.version)
	e.GET("/agents/:id/skills", h.skills)
	e.POST("/agents/:id/skills", h.setSkills)
	e.GET("/agents/:id/models", h.agentModels)
	e.GET("/providers/:id/models", h.providerModels)
}

// agentResponse is the wire shape of an agent (contract: Agent in knowledge.ts); every key is
// always present, output_schema and plugin are null when unset.
type agentResponse struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Enabled      bool            `json:"enabled"`
	Version      int             `json:"version"`
	Strategy     string          `json:"strategy"`
	CIFailOn     string          `json:"ci_fail_on"`
	RepoIntel    bool            `json:"repo_intel"`
	Plugin       *pluginRef      `json:"plugin"`
}

// pluginRef is PluginRef of the contract: the plugin that owns the agent.
type pluginRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func toAgentResponse(a domain.Agent) agentResponse {
	var plugin *pluginRef
	if a.Plugin != nil {
		plugin = &pluginRef{ID: a.Plugin.ID, Name: a.Plugin.Name, Enabled: a.Plugin.Enabled}
	}
	return agentResponse{
		ID: a.ID, Name: a.Name, Description: a.Description, Provider: a.Provider, Model: a.Model,
		SystemPrompt: a.SystemPrompt, OutputSchema: a.OutputSchema, Enabled: a.Enabled, Version: a.Version,
		Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel, Plugin: plugin,
	}
}

// versionConfigResponse is AgentVersionConfig of the contract.
type versionConfigResponse struct {
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Strategy     string          `json:"strategy"`
	CIFailOn     string          `json:"ci_fail_on"`
	RepoIntel    bool            `json:"repo_intel"`
	Skills       []string        `json:"skills"`
}

// versionResponse is AgentVersion of the contract.
type versionResponse struct {
	AgentID   string                `json:"agent_id"`
	Version   int                   `json:"version"`
	Config    versionConfigResponse `json:"config"`
	CreatedAt httpx.Time            `json:"created_at"`
}

func toVersionResponse(v domain.Version) versionResponse {
	c := v.Config
	return versionResponse{
		AgentID: v.AgentID,
		Version: v.Version,
		Config: versionConfigResponse{
			Provider: c.Provider, Model: c.Model, SystemPrompt: c.SystemPrompt, OutputSchema: c.OutputSchema,
			Strategy: c.Strategy, CIFailOn: c.CIFailOn, RepoIntel: c.RepoIntel, Skills: append([]string{}, c.Skills...),
		},
		CreatedAt: httpx.NewTime(v.CreatedAt),
	}
}

// linkResponse is AgentSkillLink of the contract.
type linkResponse struct {
	AgentID string `json:"agent_id"`
	SkillID string `json:"skill_id"`
	Order   int    `json:"order"`
}

func toLinkResponses(links []domain.SkillLink) []linkResponse {
	out := make([]linkResponse, len(links))
	for i, l := range links {
		out[i] = linkResponse{AgentID: l.AgentID, SkillID: l.SkillID, Order: l.Order}
	}
	return out
}

func (h *Handler) list(c *echo.Context) error {
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	agents, err := h.svc.List(c.Request().Context(), id.WorkspaceID)
	if err != nil {
		return err
	}
	out := make([]agentResponse, len(agents))
	for i, a := range agents {
		out[i] = toAgentResponse(a)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) get(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	a, err := h.svc.Get(c.Request().Context(), id.WorkspaceID, agentID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toAgentResponse(a))
}

func (h *Handler) create(c *echo.Context) error {
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	in, issues := parseCreate(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	a, err := h.svc.Create(c.Request().Context(), id.WorkspaceID, id.UserID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toAgentResponse(a))
}

func (h *Handler) update(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	patch, issues := parsePatch(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	a, err := h.svc.Update(c.Request().Context(), id.WorkspaceID, agentID, patch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toAgentResponse(a))
}

func (h *Handler) remove(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), id.WorkspaceID, agentID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) versions(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	versions, err := h.svc.Versions(c.Request().Context(), id.WorkspaceID, agentID)
	if err != nil {
		return err
	}
	out := make([]versionResponse, len(versions))
	for i, v := range versions {
		out[i] = toVersionResponse(v)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) version(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	number, err := httpx.PathVersion(c, "version")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	v, err := h.svc.Version(c.Request().Context(), id.WorkspaceID, agentID, number)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toVersionResponse(v))
}

func (h *Handler) skills(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	links, err := h.svc.SkillLinks(c.Request().Context(), id.WorkspaceID, agentID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toLinkResponses(links))
}

func (h *Handler) setSkills(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	req, issues := parseSkills(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	var links []domain.SkillLink
	if req.SkillIDs != nil {
		links, err = h.svc.SetSkills(c.Request().Context(), id.WorkspaceID, agentID, req.SkillIDs)
	} else {
		links, err = h.svc.LinkSkill(c.Request().Context(), id.WorkspaceID, agentID, req.SkillID, req.Order)
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toLinkResponses(links))
}

func (h *Handler) agentModels(c *echo.Context) error {
	agentID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	models, err := h.svc.AgentModels(c.Request().Context(), id.WorkspaceID, agentID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, models)
}

func (h *Handler) providerModels(c *echo.Context) error {
	provider, err := httpx.PathEnum(c, "id", domain.Providers())
	if err != nil {
		return err
	}
	if _, err := h.tenants.Resolve(c.Request().Context()); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.svc.ProviderModels(c.Request().Context(), provider))
}
