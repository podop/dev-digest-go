package gitlabconnhttp

import (
	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/platform/httpx"
)

// connectionResponse is the wire shape of a connection (contract: GitLabConnection), in the field
// order of the contract. The token is not part of it: it is reported as a flag only (spec FR4).
type connectionResponse struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	BaseURL         string     `json:"base_url"`
	Host            string     `json:"host"`
	TokenConfigured bool       `json:"token_configured"`
	RepoCount       int        `json:"repo_count"`
	CreatedAt       httpx.Time `json:"created_at"`
	UpdatedAt       httpx.Time `json:"updated_at"`
}

// deletedResponse is the answer of DELETE (contract: GitLabConnectionDeleted).
type deletedResponse struct {
	Deleted           string `json:"deleted"`
	DisconnectedRepos int    `json:"disconnected_repos"`
}

func toResponse(v app.View) connectionResponse {
	c := v.Connection
	return connectionResponse{
		ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Host: c.Host,
		TokenConfigured: v.TokenConfigured, RepoCount: v.RepoCount,
		CreatedAt: httpx.NewTime(c.CreatedAt), UpdatedAt: httpx.NewTime(c.UpdatedAt),
	}
}

func toResponses(views []app.View) []connectionResponse {
	out := make([]connectionResponse, len(views))
	for i, v := range views {
		out[i] = toResponse(v)
	}
	return out
}
