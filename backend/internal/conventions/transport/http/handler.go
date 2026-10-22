// Package conventionshttp exposes the conventions routes. It is the only place of the
// conventions context that knows Echo, JSON field names and status codes.
package conventionshttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// service is what the handlers need from the conventions use cases.
type service interface {
	State(ctx context.Context, workspaceID, repoID string) (app.State, error)
	Extract(ctx context.Context, workspaceID, repoID string) (domain.Scan, error)
	Update(ctx context.Context, workspaceID, id string, p app.Patch) (domain.Convention, error)
}

// resolver finds the workspace and user a request acts as.
type resolver interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

// Handler serves the conventions routes.
type Handler struct {
	svc     service
	tenants resolver
}

// AddRoutes registers GET /repos/:id/conventions, POST /repos/:id/conventions/extract and
// PATCH /conventions/:id. The merge route POST /repos/:id/conventions/skill belongs to the
// skills context.
func AddRoutes(e *echo.Echo, svc service, tenants resolver) {
	h := &Handler{svc: svc, tenants: tenants}
	e.GET("/repos/:id/conventions", h.state)
	e.POST("/repos/:id/conventions/extract", h.extract)
	e.PATCH("/conventions/:id", h.update)
}

// droppedResponse is the wire shape of a dropped candidate.
type droppedResponse struct {
	Rule   string `json:"rule"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// scanResponse is the wire shape of a scan (contract: ConventionScan); tokens are not sent.
type scanResponse struct {
	ID           string            `json:"id"`
	RepoID       string            `json:"repo_id"`
	Status       string            `json:"status"`
	SampledFiles []string          `json:"sampled_files"`
	Proposed     int               `json:"proposed"`
	Kept         int               `json:"kept"`
	Dropped      []droppedResponse `json:"dropped"`
	Model        *string           `json:"model"`
	CostUSD      *float64          `json:"cost_usd"`
	Error        *string           `json:"error"`
	StartedAt    httpx.Time        `json:"started_at"`
	FinishedAt   *httpx.Time       `json:"finished_at"`
}

func toScanResponse(s domain.Scan) scanResponse {
	sampled := s.SampledFiles
	if sampled == nil {
		sampled = []string{}
	}
	dropped := make([]droppedResponse, len(s.Dropped))
	for i, d := range s.Dropped {
		dropped[i] = droppedResponse{Rule: d.Rule, Path: d.Path, Reason: string(d.Reason)}
	}
	var finished *httpx.Time
	if s.FinishedAt != nil {
		t := httpx.NewTime(*s.FinishedAt)
		finished = &t
	}
	return scanResponse{
		ID: s.ID, RepoID: s.RepoID, Status: string(s.Status), SampledFiles: sampled, Proposed: s.Proposed,
		Kept: s.Kept, Dropped: dropped, Model: s.Model, CostUSD: s.CostUSD, Error: s.Error,
		StartedAt: httpx.NewTime(s.StartedAt), FinishedAt: finished,
	}
}

// evidenceResponse is the wire shape of one verified place (contract: ConventionEvidence).
type evidenceResponse struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Snippet   string `json:"snippet"`
}

// conventionResponse is the wire shape of a convention (contract: Convention).
type conventionResponse struct {
	ID         string             `json:"id"`
	RepoID     string             `json:"repo_id"`
	ScanID     *string            `json:"scan_id"`
	Category   string             `json:"category"`
	Rule       string             `json:"rule"`
	Evidence   []evidenceResponse `json:"evidence"`
	Confidence float64            `json:"confidence"`
	Status     string             `json:"status"`
	Edited     bool               `json:"edited"`
	SkillID    *string            `json:"skill_id"`
	CreatedAt  httpx.Time         `json:"created_at"`
	UpdatedAt  httpx.Time         `json:"updated_at"`
}

func toConventionResponse(c domain.Convention) conventionResponse {
	evidence := make([]evidenceResponse, len(c.Evidence))
	for i, e := range c.Evidence {
		evidence[i] = evidenceResponse{Path: e.Path, StartLine: e.StartLine, EndLine: e.EndLine, Snippet: e.Snippet}
	}
	return conventionResponse{
		ID: c.ID, RepoID: c.RepoID, ScanID: c.ScanID, Category: c.Category, Rule: c.Rule, Evidence: evidence,
		Confidence: c.Confidence, Status: string(c.Status), Edited: c.Edited, SkillID: c.SkillID,
		CreatedAt: httpx.NewTime(c.CreatedAt), UpdatedAt: httpx.NewTime(c.UpdatedAt),
	}
}

// stateResponse is the wire shape of GET /repos/:id/conventions (contract: ConventionsState).
type stateResponse struct {
	Scan        *scanResponse        `json:"scan"`
	Conventions []conventionResponse `json:"conventions"`
}

func (h *Handler) state(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	st, err := h.svc.State(c.Request().Context(), id.WorkspaceID, repoID)
	if err != nil {
		return err
	}
	out := stateResponse{Conventions: make([]conventionResponse, len(st.Conventions))}
	if st.Scan != nil {
		s := toScanResponse(*st.Scan)
		out.Scan = &s
	}
	for i, cv := range st.Conventions {
		out.Conventions[i] = toConventionResponse(cv)
	}
	return c.JSON(http.StatusOK, out)
}

// extract starts a scan; any request body is ignored.
func (h *Handler) extract(c *echo.Context) error {
	repoID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	scan, err := h.svc.Extract(c.Request().Context(), id.WorkspaceID, repoID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, toScanResponse(scan))
}

func (h *Handler) update(c *echo.Context) error {
	conventionID, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	body, err := httpx.ReadJSONBody(c)
	if err != nil {
		return err
	}
	patch, issues := parseUpdate(body)
	if issues != nil {
		return httpx.NewValidationError(issues...)
	}
	id, err := h.tenants.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	updated, err := h.svc.Update(c.Request().Context(), id.WorkspaceID, conventionID, patch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toConventionResponse(updated))
}
