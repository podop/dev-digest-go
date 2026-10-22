package httpapi

import (
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/projectcontext/app"
)

// docResponse is a ContextDoc (contract: project-context.ts); version only on store documents.
type docResponse struct {
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	DocType   string     `json:"doc_type"`
	SizeBytes int64      `json:"size_bytes"`
	Tokens    int        `json:"tokens"`
	UpdatedAt httpx.Time `json:"updated_at"`
	UsedBy    int        `json:"used_by"`
	Source    string     `json:"source"`
	Editable  bool       `json:"editable"`
	Version   *int       `json:"version,omitempty"`
}

// listResponse is a ContextList; truncated is present only when true.
type listResponse struct {
	CloneStatus string        `json:"clone_status"`
	Globs       []string      `json:"globs"`
	Docs        []docResponse `json:"docs"`
	TokensTotal int           `json:"tokens_total"`
	Truncated   bool          `json:"truncated,omitempty"`
}

type usedByResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Via       string `json:"via"`
	SkillName string `json:"skill_name,omitempty"`
}

// previewResponse is a ContextDocPreview.
type previewResponse struct {
	Path         string           `json:"path"`
	Name         string           `json:"name"`
	DocType      string           `json:"doc_type"`
	Content      string           `json:"content"`
	Tokens       int              `json:"tokens"`
	SizeBytes    int64            `json:"size_bytes"`
	UsedBy       int              `json:"used_by"`
	UsedByAgents []usedByResponse `json:"used_by_agents"`
	Source       string           `json:"source"`
	Editable     bool             `json:"editable"`
	Version      *int             `json:"version,omitempty"`
}

// attachmentsResponse is a ContextAttachments.
type attachmentsResponse struct {
	RepoID string   `json:"repo_id"`
	Paths  []string `json:"paths"`
}

func toListResponse(l app.List) listResponse {
	docs := make([]docResponse, len(l.Docs))
	for i, d := range l.Docs {
		docs[i] = docResponse{
			Path: d.Path, Name: d.Name, DocType: d.DocType, SizeBytes: d.SizeBytes, Tokens: d.Tokens,
			UpdatedAt: httpx.NewTime(d.UpdatedAt), UsedBy: d.UsedBy, Source: d.Source, Editable: d.Editable,
			Version: d.Version,
		}
	}
	globs := l.Globs
	if globs == nil {
		globs = []string{}
	}
	return listResponse{
		CloneStatus: l.CloneStatus, Globs: globs, Docs: docs, TokensTotal: l.TokensTotal, Truncated: l.Truncated,
	}
}

func toPreviewResponse(p app.Preview) previewResponse {
	agents := make([]usedByResponse, len(p.UsedByAgents))
	for i, a := range p.UsedByAgents {
		agents[i] = usedByResponse{ID: a.ID, Name: a.Name, Via: a.Via, SkillName: a.SkillName}
	}
	return previewResponse{
		Path: p.Path, Name: p.Name, DocType: p.DocType, Content: p.Content, Tokens: p.Tokens,
		SizeBytes: p.SizeBytes, UsedBy: p.UsedBy, UsedByAgents: agents, Source: p.Source,
		Editable: p.Editable, Version: p.Version,
	}
}

func toAttachmentsResponse(a app.Attachments) attachmentsResponse {
	paths := a.Paths
	if paths == nil {
		paths = []string{}
	}
	return attachmentsResponse{RepoID: a.RepoID, Paths: paths}
}
