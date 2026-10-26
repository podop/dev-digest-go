// Package httpapi exposes the skills routes and the conventions-to-skill merge. It is the only
// place of the skills context that knows Echo, JSON field names and status codes.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// importBodyLimit is the request body limit of the import preview: the base64 text plus the JSON
// envelope (the Node server allowed 64 KiB for it). It is 2,162,688 bytes.
const importBodyLimit = int64(domain.ImportMaxBase64Chars) + 64*1024

// BodyLimits returns the routes whose request body may exceed the default limit, keyed
// "METHOD /path" as the routes are registered. Pass it to httpx.Options.BodyLimits.
func BodyLimits() map[string]int64 {
	return map[string]int64{"POST /skills/import/preview": importBodyLimit}
}

// skillService is what the handlers need from the skill use cases.
type skillService interface {
	List(ctx context.Context, workspaceID string) ([]domain.Skill, error)
	Get(ctx context.Context, workspaceID, id string) (domain.Skill, error)
	Create(ctx context.Context, workspaceID string, in app.CreateInput) (domain.Skill, error)
	Update(ctx context.Context, workspaceID, id string, patch domain.Patch) (domain.Skill, error)
	Restore(ctx context.Context, workspaceID, id string, version int) (domain.Skill, error)
	Delete(ctx context.Context, workspaceID, id string) error
	ListVersions(ctx context.Context, workspaceID, id string) ([]domain.Version, error)
	GetVersion(ctx context.Context, workspaceID, id string, version int) (domain.Version, error)
	Agents(ctx context.Context, workspaceID, id string) ([]domain.AgentRef, error)
	Stats(ctx context.Context, workspaceID, id string, days int) (domain.Stats, error)
	StatsSummary(ctx context.Context, workspaceID string, days int) ([]domain.StatsSummary, error)
}

// importer is what the handlers need from the import use cases.
type importer interface {
	Community(f domain.CatalogFilter) []domain.CatalogEntry
	Preview(ctx context.Context, req app.ImportRequest) (app.Preview, error)
}

// merger is what the handlers need from the conventions-to-skill merge.
type merger interface {
	Merge(ctx context.Context, workspaceID, repoID string, in app.MergeInput) (app.MergeResult, error)
}

// resolver finds the workspace and user a request acts as.
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the skills routes.
type Handler struct {
	skills  skillService
	imports importer
	merge   merger
	tenants resolver
}

// AddRoutes registers the 13 skills routes and POST /repos/:id/conventions/skill. The static
// segments /skills/stats, /skills/community and /skills/import/preview win over /skills/:id.
func AddRoutes(e *echo.Echo, skills skillService, imports importer, merge merger, tenants resolver) {
	h := &Handler{skills: skills, imports: imports, merge: merge, tenants: tenants}
	e.GET("/skills", h.list)
	e.GET("/skills/stats", h.statsSummary)
	e.GET("/skills/community", h.community)
	e.POST("/skills/import/preview", h.preview)
	e.GET("/skills/:id", h.get)
	e.POST("/skills", h.create)
	e.PUT("/skills/:id", h.update)
	e.DELETE("/skills/:id", h.remove)
	e.GET("/skills/:id/versions", h.versions)
	e.GET("/skills/:id/versions/:version", h.version)
	e.POST("/skills/:id/versions/:version/restore", h.restore)
	e.GET("/skills/:id/agents", h.agents)
	e.GET("/skills/:id/stats", h.stats)
	e.POST("/repos/:id/conventions/skill", h.mergeConventions)
}

// ---- wire shapes --------------------------------------------------------------------------------

// skillResponse is the wire shape of a skill (contract: Skill in knowledge.ts). Every key is
// always present; evidence_files and plugin (the owner, FR14) are null when there is none.
type skillResponse struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Type          string     `json:"type"`
	Source        string     `json:"source"`
	SourceRef     *string    `json:"source_ref"`
	Body          string     `json:"body"`
	Enabled       bool       `json:"enabled"`
	Version       int        `json:"version"`
	EvidenceFiles []string   `json:"evidence_files"`
	CreatedAt     httpx.Time `json:"created_at"`
	UpdatedAt     httpx.Time `json:"updated_at"`
	UsedBy        int        `json:"used_by"`
	// Plugin is the owner plugin; null for a skill the user made.
	Plugin *pluginRefResponse `json:"plugin"`
}

// pluginRefResponse is the wire shape of PluginRef (knowledge.ts).
type pluginRefResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func toSkillResponse(s domain.Skill) skillResponse {
	out := skillResponse{
		ID: s.ID, Name: s.Name, Description: s.Description, Type: string(s.Type), Source: string(s.Source),
		SourceRef: s.SourceRef, Body: s.Body, Enabled: s.Enabled, Version: s.Version,
		CreatedAt: httpx.NewTime(s.CreatedAt), UpdatedAt: httpx.NewTime(s.UpdatedAt), UsedBy: s.UsedBy,
	}
	if s.Plugin != nil {
		out.Plugin = &pluginRefResponse{ID: s.Plugin.ID, Name: s.Plugin.Name, Enabled: s.Plugin.Enabled}
	}
	return out
}

type versionResponse struct {
	SkillID     string     `json:"skill_id"`
	Version     int        `json:"version"`
	Body        string     `json:"body"`
	Description *string    `json:"description"`
	Message     *string    `json:"message"`
	CreatedAt   httpx.Time `json:"created_at"`
}

func toVersionResponse(v domain.Version) versionResponse {
	return versionResponse{
		SkillID: v.SkillID, Version: v.Version, Body: v.Body, Description: v.Description, Message: v.Message,
		CreatedAt: httpx.NewTime(v.CreatedAt),
	}
}

type agentRefResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func toAgentRefs(in []domain.AgentRef) []agentRefResponse {
	out := make([]agentRefResponse, len(in))
	for i, a := range in {
		out[i] = agentRefResponse{ID: a.ID, Name: a.Name, Enabled: a.Enabled}
	}
	return out
}

type ignoredFileResponse struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type previewResponse struct {
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	Type          string                `json:"type"`
	Body          string                `json:"body"`
	Source        string                `json:"source"`
	SourceRef     string                `json:"source_ref"`
	IncludedFiles []string              `json:"included_files"`
	IgnoredFiles  []ignoredFileResponse `json:"ignored_files"`
	Warnings      []string              `json:"warnings"`
}

func toPreviewResponse(p app.Preview) previewResponse {
	ignored := make([]ignoredFileResponse, len(p.IgnoredFiles))
	for i, f := range p.IgnoredFiles {
		ignored[i] = ignoredFileResponse{Path: f.Path, Reason: f.Reason}
	}
	return previewResponse{
		Name: p.Name, Description: p.Description, Type: string(p.Type), Body: p.Body, Source: string(p.Source),
		SourceRef: p.SourceRef, IncludedFiles: p.IncludedFiles, IgnoredFiles: ignored, Warnings: p.Warnings,
	}
}

type communityResponse struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Repo  string   `json:"repo"`
	Stars int      `json:"stars"`
	Lang  string   `json:"lang"`
	Desc  string   `json:"desc"`
	Type  string   `json:"type"`
	Tags  []string `json:"tags"`
}

type countByResponse struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func toCountBy(in []domain.CountBy) []countByResponse {
	out := make([]countByResponse, len(in))
	for i, c := range in {
		out[i] = countByResponse{Key: c.Key, Count: c.Count}
	}
	return out
}

type statsResponse struct {
	SkillID      string             `json:"skill_id"`
	WindowDays   int                `json:"window_days"`
	RunsAttached int                `json:"runs_attached"`
	RunsCited    int                `json:"runs_cited"`
	PullRate     *float64           `json:"pull_rate"`
	Findings     int                `json:"findings"`
	Accepted     int                `json:"accepted"`
	Dismissed    int                `json:"dismissed"`
	AcceptRate   *float64           `json:"accept_rate"`
	ByCategory   []countByResponse  `json:"by_category"`
	BySeverity   []countByResponse  `json:"by_severity"`
	UsedBy       []agentRefResponse `json:"used_by"`
}

type summaryResponse struct {
	SkillID    string   `json:"skill_id"`
	PullRate   *float64 `json:"pull_rate"`
	AcceptRate *float64 `json:"accept_rate"`
	Findings   int      `json:"findings"`
}

type mergeResponse struct {
	Skill        skillResponse      `json:"skill"`
	LinkedAgents []agentRefResponse `json:"linked_agents"`
}

// ---- handlers -----------------------------------------------------------------------------------

// workspace resolves the workspace of the request.
func (h *Handler) workspace(c *echo.Context) (string, error) {
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return "", err
	}
	return id.WorkspaceID, nil
}

func (h *Handler) list(c *echo.Context) error {
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	skills, err := h.skills.List(c.Request().Context(), ws)
	if err != nil {
		return err
	}
	out := make([]skillResponse, len(skills))
	for i, s := range skills {
		out[i] = toSkillResponse(s)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) statsSummary(c *echo.Context) error {
	days, issues := parseDays(c)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	rows, err := h.skills.StatsSummary(c.Request().Context(), ws, days)
	if err != nil {
		return err
	}
	out := make([]summaryResponse, len(rows))
	for i, r := range rows {
		out[i] = summaryResponse{SkillID: r.SkillID, PullRate: r.PullRate, AcceptRate: r.AcceptRate, Findings: r.Findings}
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) community(c *echo.Context) error {
	filter, issues := parseCommunityFilter(c)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	if _, err := h.workspace(c); err != nil {
		return err
	}
	entries := h.imports.Community(filter)
	out := make([]communityResponse, len(entries))
	for i, e := range entries {
		tags := e.Tags
		if tags == nil {
			tags = []string{}
		}
		out[i] = communityResponse{
			ID: e.ID, Name: e.Name, Repo: e.Repo, Stars: e.Stars, Lang: e.Lang, Desc: e.Desc, Type: string(e.Type), Tags: tags,
		}
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) preview(c *echo.Context) error {
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	req, issues := parseImport(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	if _, err := h.workspace(c); err != nil {
		return err
	}
	p, err := h.imports.Preview(c.Request().Context(), req)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toPreviewResponse(p))
}

func (h *Handler) get(c *echo.Context) error {
	skillID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	s, err := h.skills.Get(c.Request().Context(), ws, skillID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toSkillResponse(s))
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
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	s, err := h.skills.Create(c.Request().Context(), ws, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toSkillResponse(s))
}

func (h *Handler) update(c *echo.Context) error {
	skillID, err := httpx.PathUUID(c, "id")
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
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	s, err := h.skills.Update(c.Request().Context(), ws, skillID, patch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toSkillResponse(s))
}

func (h *Handler) remove(c *echo.Context) error {
	skillID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	if err := h.skills.Delete(c.Request().Context(), ws, skillID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) versions(c *echo.Context) error {
	skillID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	vs, err := h.skills.ListVersions(c.Request().Context(), ws, skillID)
	if err != nil {
		return err
	}
	out := make([]versionResponse, len(vs))
	for i, v := range vs {
		out[i] = toVersionResponse(v)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) version(c *echo.Context) error {
	skillID, version, err := versionParams(c)
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	v, err := h.skills.GetVersion(c.Request().Context(), ws, skillID, version)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toVersionResponse(v))
}

func (h *Handler) restore(c *echo.Context) error {
	skillID, version, err := versionParams(c)
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	s, err := h.skills.Restore(c.Request().Context(), ws, skillID, version)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toSkillResponse(s))
}

func (h *Handler) agents(c *echo.Context) error {
	skillID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	agents, err := h.skills.Agents(c.Request().Context(), ws, skillID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toAgentRefs(agents))
}

func (h *Handler) stats(c *echo.Context) error {
	skillID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	days, issues := parseDays(c)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	s, err := h.skills.Stats(c.Request().Context(), ws, skillID, days)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statsResponse{
		SkillID: s.SkillID, WindowDays: s.WindowDays, RunsAttached: s.RunsAttached, RunsCited: s.RunsCited,
		PullRate: s.PullRate, Findings: s.Findings, Accepted: s.Accepted, Dismissed: s.Dismissed, AcceptRate: s.AcceptRate,
		ByCategory: toCountBy(s.ByCategory), BySeverity: toCountBy(s.BySeverity), UsedBy: toAgentRefs(s.UsedBy),
	})
}

func (h *Handler) mergeConventions(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	in, issues := parseMerge(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	ws, err := h.workspace(c)
	if err != nil {
		return err
	}
	res, err := h.merge.Merge(c.Request().Context(), ws, repoID, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, mergeResponse{Skill: toSkillResponse(res.Skill), LinkedAgents: toAgentRefs(res.LinkedAgents)})
}

// versionParams validates the :id and :version params together, so that both failures are
// reported in one 422 like a single Zod params object does.
func versionParams(c *echo.Context) (string, int, error) {
	id, idErr := httpx.PathUUID(c, "id")
	version, vErr := httpx.PathVersion(c, "version")
	if idErr == nil && vErr == nil {
		return id, version, nil
	}
	var issues []httpx.Issue
	for _, err := range []error{idErr, vErr} {
		issues = append(issues, issuesOf(err)...)
	}
	return "", 0, httpx.NewValidationError(issues...)
}

// issuesOf extracts the issues of a validation error built by httpx.NewValidationError.
func issuesOf(err error) []httpx.Issue {
	var ae *apperr.Error
	if err == nil || !errors.As(err, &ae) {
		return nil
	}
	details, _ := ae.Details.([]httpx.IssueDetail)
	out := make([]httpx.Issue, len(details))
	for i, d := range details {
		out[i] = httpx.Issue{Code: d.Params.Issue.Code, Path: d.Params.Issue.Path, Message: d.Params.Issue.Message}
	}
	return out
}
