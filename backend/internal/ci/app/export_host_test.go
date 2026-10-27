package app_test

import (
	"testing"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

// TestExport_GitHubRepositoryStillExports pins that a GitHub repository exports as before while the
// workspace also holds a GitLab repository (GitLab C: unsupported_host is no longer produced).
func TestExport_GitHubRepositoryStillExports(t *testing.T) {
	rig := newExportRig(t, rigOpts{
		agent: defaultAgent(), runner: true, configured: true,
		gitlab: []app.ExportRepo{{
			ID: "gl-1", Provider: app.ProviderGitLab, Host: "gitlab.com", Owner: "acme", Name: "web",
			FullName: "acme/web", DefaultBranch: "main", ProjectID: 42,
		}},
	})
	req := request("acme/widgets")
	req.Action = domain.ActionFiles
	got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
	if err != nil {
		t.Fatalf("Export(acme/widgets) error = %v, want none", err)
	}
	if got.GitLab != nil {
		t.Errorf("Export(acme/widgets).GitLab = %+v, want nil for a gha export", got.GitLab)
	}
}
