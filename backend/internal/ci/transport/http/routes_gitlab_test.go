package cihttp_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

const (
	repoUUID   = "7e1d0000-0000-4000-8000-0000000000b1"
	gitlabHost = "gitlab.example.com"
)

var defaultTriggers = []string{"opened", "synchronize", "reopened"}

// Spec C FR1, FR2, SCR10: repo stays required, repo_id is an optional UUID, target is optional with
// no default (a GitHub-only client that omits it reaches the use case with "").
func TestExport_RequestMapping_GitLab(t *testing.T) {
	t.Parallel()
	base := app.ExportRequest{Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted, Triggers: defaultTriggers}
	with := func(mutate func(*app.ExportRequest)) app.ExportRequest {
		r := base
		mutate(&r)
		return r
	}
	tests := []struct {
		name string
		body string
		want app.ExportRequest
	}{
		{"target omitted stays empty", `{"repo":"acme/api"}`, with(func(r *app.ExportRequest) { r.Repo = "acme/api" })},
		{"gitlab_ci", `{"repo":"grp/proj","target":"gitlab_ci"}`, with(func(r *app.ExportRequest) { r.Repo, r.Target = "grp/proj", "gitlab_ci" })},
		{"repo_id beside repo", `{"repo":"grp/proj","repo_id":"` + repoUUID + `","target":"gitlab_ci"}`, with(func(r *app.ExportRequest) {
			r.Repo, r.RepoID, r.Target = "grp/proj", repoUUID, "gitlab_ci"
		})},
		{"repo_id uppercase uuid", `{"repo":"r","repo_id":"` + strings.ToUpper(repoUUID) + `"}`, with(func(r *app.ExportRequest) {
			r.Repo, r.RepoID = "r", strings.ToUpper(repoUUID)
		})},
		{"gitlab options", `{"repo":"g/p","action":"files","post_as":"none","runner":"self_hosted","target":"gitlab_ci"}`, app.ExportRequest{
			Repo: "g/p", Target: "gitlab_ci", Action: domain.ActionFiles, PostAs: domain.PostAsNone, Runner: domain.RunnerSelfHosted, Triggers: defaultTriggers}},
		{"gha stays gha", `{"repo":"a/b","target":"gha"}`, with(func(r *app.ExportRequest) { r.Repo, r.Target = "a/b", "gha" })},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{}
			r := postJSON(t, newServer(t, f), "/agents/"+agentID+"/export-ci", tc.body)
			if r.Status != http.StatusOK && r.Status != http.StatusCreated {
				t.Fatalf("status = %d (%s), want 200 or 201", r.Status, r.Body)
			}
			if diff := cmp.Diff(tc.want, f.exportReq); diff != "" {
				t.Errorf("ExportRequest mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExport_ValidationIssues_GitLab(t *testing.T) {
	t.Parallel()
	targetEnum := "'gha' | 'circle' | 'jenkins' | 'cli' | 'gitlab_ci'"
	tests := []struct {
		name string
		body string
		want []issueDetail
	}{
		{"repo_id not a uuid", `{"repo":"a/b","repo_id":"nope"}`, []issueDetail{{"invalid_string", "/repo_id", "Invalid uuid"}}},
		{"repo_id number", `{"repo":"a/b","repo_id":5}`, []issueDetail{{"invalid_type", "/repo_id", "Expected string, received number"}}},
		{"repo_id null", `{"repo":"a/b","repo_id":null}`, []issueDetail{{"invalid_type", "/repo_id", "Expected string, received null"}}},
		{"repo is still required with repo_id", `{"repo_id":"` + repoUUID + `"}`, []issueDetail{{"invalid_type", "/repo", "Required"}}},
		{"unknown target", `{"repo":"a/b","target":"gitlab"}`, []issueDetail{{"invalid_enum_value", "/target", "Invalid enum value. Expected " + targetEnum + ", received 'gitlab'"}}},
		{"empty target", `{"repo":"a/b","target":""}`, []issueDetail{{"invalid_enum_value", "/target", "Invalid enum value. Expected " + targetEnum + ", received ''"}}},
		{"target number", `{"repo":"a/b","target":1}`, []issueDetail{{"invalid_type", "/target", "Expected " + targetEnum + ", received number"}}},
		{"issues in schema order", `{"repo":1,"repo_id":"x","target":"y"}`, []issueDetail{
			{"invalid_type", "/repo", "Expected string, received number"},
			{"invalid_string", "/repo_id", "Invalid uuid"},
			{"invalid_enum_value", "/target", "Invalid enum value. Expected " + targetEnum + ", received 'y'"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{}
			r := postJSON(t, newServer(t, f), "/agents/"+agentID+"/export-ci", tc.body)
			if diff := cmp.Diff(tc.want, validationIssues(t, r)); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
			if calls := f.callList(); len(calls) != 0 {
				t.Errorf("use case called on invalid input: %v", calls)
			}
		})
	}
}

func gitlabInstallation() domain.Installation {
	return domain.Installation{
		ID: instID, AgentID: agentID, Repo: "grp/sub/proj", TargetType: "gitlab_ci", InstalledAt: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC),
		Slug: ptr("reviewer"), ManifestVersion: ptr(3), Provider: "openrouter", Host: gitlabHost, ProjectID: 42,
	}
}

func wantGitLabInstallationJSON(lastRun any) map[string]any {
	return map[string]any{
		"id": instID, "agent_id": agentID, "repo": "grp/sub/proj", "target_type": "gitlab_ci", "installed_at": "2026-10-19T08:00:00.000Z",
		"slug": "reviewer", "workflow_path": ".gitlab/devdigest/review-reviewer.yml", "manifest_version": 3.0, "last_run": lastRun,
		"provider": "gitlab", "host": gitlabHost,
	}
}

func TestExport_GitLabShape(t *testing.T) {
	t.Parallel()
	inst := gitlabInstallation()
	f := &fakeCI{export: domain.Export{
		Installation: &inst,
		Files:        []domain.File{{Path: ".gitlab/devdigest/review-reviewer.yml", Contents: "job:\n", Editable: true}},
		PRURL:        ptr("https://gitlab.example.com/grp/sub/proj/-/merge_requests/5"),
		GitLab: &domain.GitLabExport{
			JobFile: ".gitlab/devdigest/review-reviewer.yml", RootConfig: domain.RootConfigCreate, ConfigPath: ".gitlab-ci.yml",
			IncludeSnippet: "include:\n  - local: .gitlab/devdigest/review-reviewer.yml\n", TriggersNote: "note text",
		},
		RequiredSecrets: []string{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"},
	}}
	srv := newServer(t, f)
	r := postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"grp/sub/proj","repo_id":"`+repoUUID+`","target":"gitlab_ci","action":"open_pr"}`)
	if r.Status != http.StatusCreated {
		t.Fatalf("open_pr status = %d (%s), want 201", r.Status, r.Body)
	}
	want := map[string]any{
		"installation": wantGitLabInstallationJSON(nil),
		"files":        []any{map[string]any{"path": ".gitlab/devdigest/review-reviewer.yml", "contents": "job:\n", "editable": true}},
		"pr_url":       "https://gitlab.example.com/grp/sub/proj/-/merge_requests/5",
		"gitlab_ci": map[string]any{
			"job_file": ".gitlab/devdigest/review-reviewer.yml", "root_config": "create", "config_path": ".gitlab-ci.yml",
			"include_snippet": "include:\n  - local: .gitlab/devdigest/review-reviewer.yml\n", "triggers_note": "note text",
		},
		"required_secrets":   []any{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"},
		"required_variables": []any{},
	}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("open_pr body mismatch (-want +got):\n%s", diff)
	}

	// files action: 200, no installation, no PR; an empty note and a custom config path.
	f.export = domain.Export{
		Files: []domain.File{{Path: "j", Contents: "c", Editable: true}},
		GitLab: &domain.GitLabExport{
			JobFile: "j", RootConfig: domain.RootConfigCustomPath, ConfigPath: "ci/main.yml", IncludeSnippet: "include:\n",
		},
	}
	r = postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"grp/sub/proj","target":"gitlab_ci","action":"files"}`)
	if r.Status != http.StatusOK {
		t.Fatalf("files status = %d (%s), want 200", r.Status, r.Body)
	}
	got, ok := decode(t, r).(map[string]any)
	if !ok {
		t.Fatalf("body %s is not an object", r.Body)
	}
	wantGL := map[string]any{"job_file": "j", "root_config": "custom_path", "config_path": "ci/main.yml", "include_snippet": "include:\n", "triggers_note": nil}
	if diff := cmp.Diff(wantGL, got["gitlab_ci"]); diff != "" {
		t.Errorf("gitlab_ci mismatch (-want +got):\n%s", diff)
	}
	if got["installation"] != nil || got["pr_url"] != nil {
		t.Errorf("files answer has installation %v / pr_url %v, want null", got["installation"], got["pr_url"])
	}
}

func TestInstallations_GitLabShape(t *testing.T) {
	t.Parallel()
	gl := gitlabInstallation()
	gl.LastRun = &domain.Run{
		ID: "run-gl", InstallationID: ptr(instID), PRNumber: ptr(5), RanAt: ptr(ranAt), Status: ptr("succeeded"), Source: ptr("gitlab_ci"),
		GithubURL: ptr("https://gitlab.example.com/grp/sub/proj/-/jobs/77"), Repo: ptr("grp/sub/proj"), Host: gitlabHost,
		PRURL: ptr("https://gitlab.example.com/grp/sub/proj/-/merge_requests/5"),
	}
	noSlug := gitlabInstallation()
	noSlug.Slug = nil
	f := &fakeCI{insts: []domain.Installation{gl, noSlug}}
	r := send(t, newServer(t, f), http.MethodGet, "/agents/"+agentID+"/ci-installations", "", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
	}
	wantRun := map[string]any{
		"id": "run-gl", "ci_installation_id": instID, "pr_number": 5.0, "ran_at": "2026-10-20T12:30:15.123Z", "status": "succeeded",
		"findings_count": nil, "cost_usd": nil, "github_url": "https://gitlab.example.com/grp/sub/proj/-/jobs/77", "source": "gitlab_ci",
		"agent": nil, "duration_s": nil, "agent_id": nil, "repo": "grp/sub/proj", "verdict": "passed", "blockers": nil, "head_sha": nil,
		"manifest_version": nil, "provider": "gitlab", "host": gitlabHost, "pr_url": "https://gitlab.example.com/grp/sub/proj/-/merge_requests/5",
	}
	wantNoSlug := wantGitLabInstallationJSON(nil)
	wantNoSlug["slug"], wantNoSlug["workflow_path"] = nil, nil
	want := map[string]any{"installations": []any{wantGitLabInstallationJSON(wantRun), wantNoSlug}}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestRuns_GitLabShape(t *testing.T) {
	t.Parallel()
	gl := domain.Run{
		ID: "run-gl", Source: ptr("gitlab_ci"), Host: gitlabHost, Repo: ptr("grp/proj"), PRNumber: ptr(9),
		GithubURL: ptr("https://gitlab.example.com/grp/proj/-/jobs/3"),
	}
	f := &fakeCI{runs: []domain.Run{gl}}
	r := send(t, newServer(t, f), http.MethodGet, "/ci/runs", "", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
	}
	runs, _ := decode(t, r).(map[string]any)["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %s, want one", r.Body)
	}
	run, _ := runs[0].(map[string]any)
	want := map[string]any{"provider": "gitlab", "host": gitlabHost, "pr_url": nil, "source": "gitlab_ci", "repo": "grp/proj", "pr_number": 9.0}
	for k, w := range want {
		if run[k] != w {
			t.Errorf("run[%q] = %v, want %v", k, run[k], w)
		}
	}
}

func TestRepoInstallations_GitLabShape(t *testing.T) {
	t.Parallel()
	installed := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	f := &fakeCI{groups: []domain.RepoGroup{
		{Repo: "grp/proj", TargetType: "gitlab_ci", Host: gitlabHost, ProjectID: 42, Installations: []domain.RepoInstallation{
			{ID: instID, AgentID: agentID, AgentName: "alpha", Repo: "grp/proj", Slug: ptr("alpha"), InstalledAt: installed, TargetType: "gitlab_ci", Host: gitlabHost, ProjectID: 42},
		}},
		{Repo: "grp/proj", TargetType: "gha", Installations: []domain.RepoInstallation{
			{ID: "i2", AgentID: agentID, AgentName: "alpha", Repo: "grp/proj", InstalledAt: installed},
		}},
	}}
	r := send(t, newServer(t, f), http.MethodGet, "/ci/installations", "", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
	}
	want := map[string]any{"repos": []any{
		map[string]any{
			"repo": "grp/proj", "provider": "gitlab", "host": gitlabHost, "target_type": "gitlab_ci", "last_run": nil,
			"installations": []any{map[string]any{"id": instID, "agent_id": agentID, "agent_name": "alpha", "slug": "alpha", "installed_at": "2026-10-19T08:00:00.000Z"}},
		},
		map[string]any{
			"repo": "grp/proj", "provider": "github", "host": "github.com", "target_type": "gha", "last_run": nil,
			"installations": []any{map[string]any{"id": "i2", "agent_id": agentID, "agent_name": "alpha", "slug": nil, "installed_at": "2026-10-19T08:00:00.000Z"}},
		},
	}}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestRefresh_GitLabResult(t *testing.T) {
	t.Parallel()
	codes := []domain.ErrorCode{
		domain.CodeGitLabPermissionDenied, domain.CodeGitLabRateLimited, domain.CodeGitLabUnavailable,
		domain.CodeRepoHostNotConnected, domain.CodeGitLabTokenMissing,
	}
	f := &fakeCI{}
	var want []any
	for i, code := range codes {
		f.refresh.Installations = append(f.refresh.Installations, domain.InstallationResult{
			InstallationID: "g" + string(rune('1'+i)), Repo: "grp/p", Host: gitlabHost, State: domain.StateError, ErrorCode: code,
		})
		want = append(want, map[string]any{
			"installation_id": "g" + string(rune('1'+i)), "repo": "grp/p", "host": gitlabHost, "state": "error",
			"error_code": string(code), "ingested": 0.0, "skipped": []any{},
		})
	}
	// GitHub results keep today's shape: no host key, whether the store says github.com or nothing.
	f.refresh.Installations = append(f.refresh.Installations,
		domain.InstallationResult{InstallationID: "h1", Repo: "acme/a", Host: "github.com", State: domain.StateOK},
		domain.InstallationResult{InstallationID: "h2", Repo: "acme/b", State: domain.StateOK},
	)
	want = append(want,
		map[string]any{"installation_id": "h1", "repo": "acme/a", "state": "ok", "ingested": 0.0, "skipped": []any{}},
		map[string]any{"installation_id": "h2", "repo": "acme/b", "state": "ok", "ingested": 0.0, "skipped": []any{}},
	)
	f.refresh.Degraded = true
	r := send(t, newServer(t, f), http.MethodPost, "/ci/refresh", "", "")
	if diff := cmp.Diff(map[string]any{"ingested": 0.0, "degraded": true, "installations": want}, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}
