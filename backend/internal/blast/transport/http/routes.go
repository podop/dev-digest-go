// Package blasthttp serves GET /pulls/:id/blast and GET /pulls/:id/history (spec 7.2). It is the
// only place of the context that knows Echo and the wire shapes; it depends on the use cases only
// through app.API.
package blasthttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// identities resolves the workspace a request runs in (*tenancy.Resolver).
type identities interface {
	Resolve(ctx context.Context) (tenancy.Identity, error)
}

type handler struct {
	api app.API
	ids identities
}

// AddRoutes registers the blast routes:
//
//	GET /pulls/:id/blast     BlastRadius
//	GET /pulls/:id/history   PrHistory
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/pulls/:id/blast", h.blast)
	e.GET("/pulls/:id/history", h.history)
}

// workspaceAndPull answers 422 for an id that is not a UUID.
func (h *handler) workspaceAndPull(c *echo.Context) (workspaceID, prID string, err error) {
	prID, err = httpx.PathUUID(c, "id")
	if err != nil {
		return "", "", err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return "", "", err
	}
	return ident.WorkspaceID, prID, nil
}

func (h *handler) blast(c *echo.Context) error {
	ws, id, err := h.workspaceAndPull(c)
	if err != nil {
		return err
	}
	b, err := h.api.Radius(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toBlastResponse(b))
}

func (h *handler) history(c *echo.Context) error {
	ws, id, err := h.workspaceAndPull(c)
	if err != nil {
		return err
	}
	prior, err := h.api.History(c.Request().Context(), ws, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toHistoryResponse(prior))
}

// The wire shapes of spec 7.7 and 7.8. Struct field order is the key order of the JSON; slices
// are never nil so that an empty list is [] and not null.

type blastResponse struct {
	ChangedSymbols []changedSymbolDTO `json:"changed_symbols"`
	Downstream     []downstreamDTO    `json:"downstream"`
	Summary        string             `json:"summary"`
	Degraded       bool               `json:"degraded"`
	Reason         string             `json:"reason,omitempty"`
}

type changedSymbolDTO struct {
	Name string `json:"name"`
	File string `json:"file"`
	Kind string `json:"kind"`
}

type downstreamDTO struct {
	Symbol            string      `json:"symbol"`
	Callers           []callerDTO `json:"callers"`
	EndpointsAffected []string    `json:"endpoints_affected"`
	CronsAffected     []string    `json:"crons_affected"`
}

type callerDTO struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type historyResponse struct {
	History []priorPRDTO `json:"history"`
}

type priorPRDTO struct {
	PRNumber     int      `json:"pr_number"`
	Title        string   `json:"title"`
	MergedAt     string   `json:"merged_at"`
	Author       string   `json:"author"`
	FilesOverlap []string `json:"files_overlap"`
	Notes        string   `json:"notes"`
}

// toBlastResponse emits reason only when the answer is degraded (Node's serializer drops it
// otherwise) and degraded always.
func toBlastResponse(b domain.BlastRadius) blastResponse {
	symbols := make([]changedSymbolDTO, len(b.ChangedSymbols))
	for i, s := range b.ChangedSymbols {
		symbols[i] = changedSymbolDTO{Name: s.Name, File: s.File, Kind: s.Kind}
	}
	downstream := make([]downstreamDTO, len(b.Downstream))
	for i, d := range b.Downstream {
		callers := make([]callerDTO, len(d.Callers))
		for j, c := range d.Callers {
			callers[j] = callerDTO{Name: c.Name, File: c.File, Line: c.Line}
		}
		downstream[i] = downstreamDTO{
			Symbol:            d.Symbol,
			Callers:           callers,
			EndpointsAffected: nonNil(d.EndpointsAffected),
			CronsAffected:     nonNil(d.CronsAffected),
		}
	}
	resp := blastResponse{ChangedSymbols: symbols, Downstream: downstream, Summary: b.Summary, Degraded: b.Degraded}
	if b.Degraded {
		resp.Reason = string(b.Reason)
	}
	return resp
}

func toHistoryResponse(prior []domain.PriorPR) historyResponse {
	items := make([]priorPRDTO, len(prior))
	for i, p := range prior {
		items[i] = priorPRDTO{
			PRNumber:     p.PRNumber,
			Title:        p.Title,
			MergedAt:     p.MergedAt,
			Author:       p.Author,
			FilesOverlap: nonNil(p.FilesOverlap),
			Notes:        "",
		}
	}
	return historyResponse{History: items}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
