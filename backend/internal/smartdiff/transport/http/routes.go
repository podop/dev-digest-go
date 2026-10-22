// Package smartdiffhttp serves GET /pulls/:id/smart-diff (spec 7.2). It is the only place of the
// context that knows Echo and the wire shape; it depends on the use case only through app.API.
package smartdiffhttp

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/smartdiff/app"
	"dev-digest-go/backend/internal/smartdiff/domain"
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

// AddRoutes registers the smart-diff route:
//
//	GET /pulls/:id/smart-diff   SmartDiff
func AddRoutes(e *echo.Echo, api app.API, ids identities) {
	h := &handler{api: api, ids: ids}
	e.GET("/pulls/:id/smart-diff", h.get)
}

// get answers 422 for an id that is not a UUID, 404 "Pull request not found" for one that is not
// in the workspace.
func (h *handler) get(c *echo.Context) error {
	id, err := httpx.PathUUID(c, "id")
	if err != nil {
		return err
	}
	ident, err := h.ids.Resolve(c.Request().Context())
	if err != nil {
		return err
	}
	sd, err := h.api.Get(c.Request().Context(), ident.WorkspaceID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toResponse(sd))
}

// The wire shape of spec 7.3. Struct field order is the key order of the JSON; slices are never
// nil so that an empty list is [] and not null. There is no pseudocode_summary: the context
// never makes a model call.

type smartDiffResponse struct {
	Groups          []groupDTO         `json:"groups"`
	SplitSuggestion splitSuggestionDTO `json:"split_suggestion"`
}

type groupDTO struct {
	Role  string    `json:"role"`
	Files []fileDTO `json:"files"`
}

type fileDTO struct {
	Path         string `json:"path"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	FindingLines []int  `json:"finding_lines"`
}

// splitSuggestionDTO is constant but for total_lines: the context never proposes splits.
type splitSuggestionDTO struct {
	TooBig         bool       `json:"too_big"`
	TotalLines     int        `json:"total_lines"`
	ProposedSplits []struct{} `json:"proposed_splits"`
}

func toResponse(sd domain.SmartDiff) smartDiffResponse {
	groups := make([]groupDTO, len(sd.Groups))
	for i, g := range sd.Groups {
		files := make([]fileDTO, len(g.Files))
		for j, f := range g.Files {
			lines := f.FindingLines
			if lines == nil {
				lines = []int{}
			}
			files[j] = fileDTO{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, FindingLines: lines}
		}
		groups[i] = groupDTO{Role: string(g.Role), Files: files}
	}
	return smartDiffResponse{
		Groups:          groups,
		SplitSuggestion: splitSuggestionDTO{TotalLines: sd.TotalLines, ProposedSplits: []struct{}{}},
	}
}
