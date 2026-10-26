package app_test

import (
	"testing"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// TestExport_GitLabRepositoryIsUnsupported covers FR29 / EC25: a name only a GitLab repository
// carries answers 422 unsupported_host before any GitHub call or store write.
func TestExport_GitLabRepositoryIsUnsupported(t *testing.T) {
	for _, action := range []domain.ExportAction{domain.ActionFiles, domain.ActionOpenPR} {
		t.Run(string(action), func(t *testing.T) {
			rig := newExportRig(t, rigOpts{
				agent: defaultAgent(), runner: true, configured: true,
				otherHost: map[string]bool{"acme/web": true},
			})
			req := request("acme/web")
			req.Action = action
			_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			wantError(t, err, apperr.KindValidation, "unsupported_host", "GitLab CI export is not available yet")
			if got := rig.rec.snapshot(t); len(got) != 0 {
				t.Errorf("Export(acme/web) made calls %v, want none", got)
			}
		})
	}
}

// TestExport_GitHubRepositoryStillExports pins that a GitHub repository is unaffected.
func TestExport_GitHubRepositoryStillExports(t *testing.T) {
	rig := newExportRig(t, rigOpts{
		agent: defaultAgent(), runner: true, configured: true,
		otherHost: map[string]bool{"acme/web": true},
	})
	req := request("acme/widgets")
	req.Action = domain.ActionFiles
	if _, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req); err != nil {
		t.Errorf("Export(acme/widgets) error = %v, want none", err)
	}
}
