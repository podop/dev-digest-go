package app_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

const nodeRunnerPath = ".devdigest/runner/index.js"

// exportRig is one Export service over fakes.
type exportRig struct {
	svc   *app.ExportService
	store *fakeStore
	gh    *fakeGitHub
	logs  *logCapture
	rec   *calls
	// agents is the map behind the AgentDirectory: a test changes an agent between exports.
	agents map[string]domain.ExportAgent
}

type rigOpts struct {
	agent      domain.ExportAgent
	noAgent    bool
	runner     bool
	configured bool
	openPR     string // URL of an open pull request; "" = none
	custom     app.CustomProviders
	prices     app.ModelPrices
	gitlab     []app.ExportRepo // GitLab repositories of the workspace
}

func defaultAgent() domain.ExportAgent {
	return domain.ExportAgent{
		ID: "agent-1", Name: "Security Reviewer", Provider: "openrouter", Model: "deepseek/deepseek-v4-flash",
		SystemPrompt: "Review this PR for security issues.", Strategy: "auto", CIFailOn: "critical", Version: 3,
		Skills: []domain.ExportSkill{{Name: "API Safety", Body: "Check auth.\nCheck input.\n"}, {Name: "No Secrets", Body: "Never commit keys."}},
	}
}

func newExportRig(t *testing.T, o rigOpts) *exportRig {
	t.Helper()
	rec := &calls{}
	store := &fakeStore{calls: rec}
	gh := &fakeGitHub{calls: rec, configured: o.configured}
	gh.commitFiles = func(app.RepoRef, app.CommitFilesInput) error { return nil }
	gh.findOpenPR = func(app.RepoRef, string) (string, bool, error) { return o.openPR, o.openPR != "", nil }
	gh.openPR = func(app.RepoRef, app.OpenPullRequestInput) (string, error) {
		return "https://github.com/acme/widgets/pull/12", nil
	}
	agents := fakeAgents{agents: map[string]domain.ExportAgent{}}
	if !o.noAgent {
		agents.agents[o.agent.ID] = o.agent
	}
	logs := newLogCapture()
	svc := app.NewExportService(app.ExportDeps{
		Agents: agents,
		Repos: fakeRepos{repos: map[string]domain.ImportedRepo{
			"acme/widgets": {Owner: "acme", Name: "widgets", FullName: "acme/widgets", DefaultBranch: "main"},
		}, gitlab: o.gitlab},
		Store:  store,
		Runner: fakeRunner{artifact: testRunner, ok: o.runner},
		GitHub: gh,
		Custom: o.custom,
		Prices: o.prices,
		Log:    logs.logger(),
	})
	return &exportRig{svc: svc, store: store, gh: gh, logs: logs, rec: rec, agents: agents.agents}
}

func okRig(t *testing.T) *exportRig {
	t.Helper()
	return newExportRig(t, rigOpts{agent: defaultAgent(), runner: true, configured: true})
}

func request(repo string) app.ExportRequest {
	return app.ExportRequest{
		Repo: repo, Target: "gha", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview,
		Triggers: []string{"opened", "synchronize", "reopened"},
	}
}

func wantError(t *testing.T, err error, kind apperr.Kind, code, message string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v (%T), want an apperr %s %s", err, err, kind, code)
	}
	if ae.Kind != kind || ae.Code != code || (message != "" && ae.Message != message) {
		t.Errorf("error = %s %s %q, want %s %s %q", ae.Kind, ae.Code, ae.Message, kind, code, message)
	}
	return ae
}

const deviationsPath = "../../parity/testdata/handwritten/ci_deviations.json"

// exportDeviations are the listed differences between the Node answer of a golden export step and
// the Go one (ci_deviations.json, keys "export/<scenario>/<node error code>"). A listed step must
// still show the listed Node answer in the golden and the listed Go answer here, and must be hit.
type exportDeviations struct {
	listed map[string]obj
	hit    map[string]bool
}

func loadExportDeviations(t testing.TB) *exportDeviations {
	t.Helper()
	d := &exportDeviations{listed: map[string]obj{}, hit: map[string]bool{}}
	for _, x := range asList(readJSONFile(t, deviationsPath)["deviations"]) {
		if key := asStr(asObj(x)["key"]); strings.HasPrefix(key, "export/") {
			d.listed[key] = asObj(x)
		}
	}
	return d
}

// want is the error the Go service must answer for a golden error step: the golden's, unless the
// step is listed, then the listed Go answer (after checking the listed Node answer is the golden's).
func (d *exportDeviations) want(t testing.TB, scenario string, golden obj) obj {
	t.Helper()
	key := "export/" + scenario + "/" + asStr(golden["code"])
	dev, listed := d.listed[key]
	if !listed {
		return golden
	}
	d.hit[key] = true
	if diff := cmp.Diff(dev["node"], any(golden)); diff != "" {
		t.Errorf("%s: the listed Node answer is out of date (-listed +golden):\n%s", key, diff)
	}
	return asObj(dev["go"])
}

func (d *exportDeviations) finish(t testing.TB) {
	t.Helper()
	for key := range d.listed {
		if !d.hit[key] {
			t.Errorf("deviation %q was never hit: it names no golden step", key)
		}
	}
}

// TestExportGolden runs the 20 export scenarios of the Node golden.
func TestExportGolden(t *testing.T) {
	spec, corpus, golden := scenarios(t, "export")
	dev := loadExportDeviations(t)
	for i := range golden {
		name := asStr(golden[i]["name"])
		t.Run(name, func(t *testing.T) {
			world := asObj(corpus[i]["world"])
			goldSteps, corpusSteps := asList(golden[i]["steps"]), asList(corpus[i]["steps"])
			for j := range goldSteps {
				runExportStep(t, spec, world, asObj(goldSteps[j]), asObj(corpusSteps[j]), func(g obj) obj { return dev.want(t, name, g) })
			}
		})
	}
	dev.finish(t)
}

func exportWorld(t *testing.T, spec, world obj) *exportRig {
	t.Helper()
	agentMap := mergeObj(asObj(spec["agent"]), asObj(world["agent"]))
	agent := domain.ExportAgent{
		ID: asStr(agentMap["id"]), Name: asStr(agentMap["name"]), Provider: asStr(agentMap["provider"]),
		Model: asStr(agentMap["model"]), SystemPrompt: asStr(agentMap["system_prompt"]),
		Strategy: asStr(agentMap["strategy"]), CIFailOn: asStr(agentMap["ci_fail_on"]), Version: asInt(agentMap["version"]),
	}
	for _, s := range asList(agentMap["skills"]) {
		agent.Skills = append(agent.Skills, domain.ExportSkill{Name: asStr(asObj(s)["name"]), Body: asStr(asObj(s)["body"])})
	}
	rig := newExportRig(t, rigOpts{
		agent: agent, runner: world["runner"] != nil || !hasKey(world, "runner"), configured: true,
		openPR: asStr(asObj(world["openPr"])["url"]),
	})
	rig.store.slugOwner = asStr(world["slugOwner"])
	if world["upsert"] == "slug_taken" {
		rig.store.upsertErr = app.ErrSlugTaken
	}
	rig.store.vanish = world["vanishAfterUpsert"] == true
	if w, ok := world["installation"].(obj); ok {
		inst := mergeObj(obj{
			"id": "inst-1", "agent_id": agent.ID, "repo": "acme/widgets", "slug": "security-reviewer", "manifest_version": float64(2),
		}, w)
		slug, version := asStr(inst["slug"]), asInt(inst["manifest_version"])
		rig.store.existing = &domain.Installation{
			ID: asStr(inst["id"]), AgentID: asStr(inst["agent_id"]), Repo: asStr(inst["repo"]), TargetType: "gha",
			InstalledAt: installedAt, Slug: &slug, ManifestVersion: &version,
		}
	}
	for _, w := range asList(world["installations"]) {
		inst := mergeObj(obj{
			"id": "inst-1", "agent_id": "agent-1", "repo": "acme/widgets", "slug": "security-reviewer", "manifest_version": float64(3),
		}, asObj(w))
		slug, version := asStr(inst["slug"]), asInt(inst["manifest_version"])
		rig.store.listResults = append(rig.store.listResults, domain.Installation{
			ID: asStr(inst["id"]), AgentID: asStr(inst["agent_id"]), Repo: asStr(inst["repo"]), TargetType: "gha",
			InstalledAt: installedAt, Slug: &slug, ManifestVersion: &version,
		})
	}
	return rig
}

func hasKey(m obj, k string) bool { _, ok := m[k]; return ok }

// githubFail makes the named GitHub write fail as the step says: a status, or a transport error.
func (r *exportRig) githubFail(t *testing.T, fail obj) {
	t.Helper()
	failure := func(name string) error {
		f, ok := fail[name].(obj)
		if !ok {
			return nil
		}
		if status := asInt(f["status"]); status != 0 {
			return &app.GitHubError{Status: status}
		}
		return &app.GitHubError{Transport: true, Cause: errors.New(asStr(f["message"]))}
	}
	if err := failure("commitFiles"); err != nil {
		r.gh.commitFiles = func(app.RepoRef, app.CommitFilesInput) error { return err }
	}
	if err := failure("findOpenPr"); err != nil {
		r.gh.findOpenPR = func(app.RepoRef, string) (string, bool, error) { return "", false, err }
	}
	if err := failure("openPullRequest"); err != nil {
		r.gh.openPR = func(app.RepoRef, app.OpenPullRequestInput) (string, error) { return "", err }
	}
}

var kinds = map[string]apperr.Kind{
	"validation": apperr.KindValidation, "not_found": apperr.KindNotFound, "conflict": apperr.KindConflict,
	"internal": apperr.KindInternal, "external_service": apperr.KindExternalService, "config": apperr.KindConfig,
}

func runExportStep(t *testing.T, spec, world, gold, corpusStep obj, expectedError func(golden obj) obj) {
	t.Helper()
	if _, schema := gold["parse"]; schema { // request schema: transport's job
		return
	}
	rig := exportWorld(t, spec, world)
	rig.githubFail(t, asObj(gold["githubFail"]))
	agentID := asStr(gold["agentId"])
	ctx := context.Background()

	var err error
	var got domain.Export
	var gotList []domain.Installation
	body := asObj(gold["body"])
	switch gold["op"] {
	case "export":
		req := request(asStr(body["repo"]))
		if v, ok := body["target"]; ok {
			req.Target = asStr(v)
		}
		if v, ok := body["action"]; ok {
			req.Action = domain.ExportAction(asStr(v))
		}
		if v, ok := body["post_as"]; ok {
			req.PostAs = domain.PostAs(asStr(v))
		}
		if v, ok := body["triggers"]; ok {
			req.Triggers = nil
			for _, x := range asList(v) {
				req.Triggers = append(req.Triggers, asStr(x))
			}
			if req.Triggers == nil {
				req.Triggers = []string{}
			}
		}
		if v, ok := body["workflow"]; ok && v != nil {
			text := asStr(expand(t, workflowSource(v, asObj(corpusStep["body"])["workflow"])))
			req.Workflow = &text
		}
		got, err = rig.svc.Export(ctx, "ws-1", agentID, req)
	case "listInstallations":
		gotList, err = rig.svc.ListInstallations(ctx, "ws-1", agentID)
	default:
		t.Fatalf("unknown op %v", gold["op"])
	}

	label := fmt.Sprintf("%s %v", gold["op"], body)
	if len(label) > 120 {
		label = label[:120]
	}
	if gerr, failed := gold["error"].(obj); failed {
		checkGoldenError(t, label, err, expectedError(gerr))
	} else if err != nil {
		t.Errorf("%s: error %v, want success", label, err)
	} else if gold["op"] == "export" {
		checkExportResult(t, label, got, asObj(gold["result"]), body)
	} else {
		checkListedInstallations(t, label, gotList, asList(gold["result"]))
	}

	wantCalls := exportCalls(asList(gold["calls"]))
	if diff := cmp.Diff(wantCalls, rig.rec.snapshot(t)); diff != "" {
		t.Errorf("%s: calls mismatch (-want +got):\n%s", label, diff)
	}
	if diff := cmp.Diff(asList(gold["logs"]), rig.logs.snapshot(t)); diff != "" {
		t.Errorf("%s: logs mismatch (-want +got):\n%s", label, diff)
	}
}

// workflowSource is the workflow text of a step: the golden's, or, for a blob digest, the
// corpus's placeholder.
func workflowSource(golden, corpus any) any {
	if m, ok := golden.(obj); ok && m["$blob"] == true {
		return corpus
	}
	return golden
}

func checkGoldenError(t *testing.T, label string, err error, want obj) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want %v %v", label, want["kind"], want["code"])
		return
	}
	message := asStr(want["message"])
	if want["code"] == "runner_bundle_missing" { // new text on purpose (D6009); TestExportRunnerMissingMessage
		message = ""
	}
	ae := wantError(t, err, kinds[asStr(want["kind"])], asStr(want["code"]), message)
	var gotDetails any
	if ae.Details != nil {
		gotDetails = normalize(t, ae.Details)
	}
	if diff := cmp.Diff(want["details"], gotDetails); diff != "" {
		t.Errorf("%s: details mismatch (-want +got):\n%s", label, diff)
	}
}

func checkInstallation(t *testing.T, label string, got domain.Installation, want obj) {
	t.Helper()
	gotShape := obj{
		"id": got.ID, "agent_id": got.AgentID, "repo": got.Repo, "target_type": got.TargetType,
		"installed_at": got.InstalledAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		"slug":         got.Slug, "manifest_version": got.ManifestVersion,
	}
	wantShape := obj{}
	for _, k := range []string{"id", "agent_id", "repo", "target_type", "installed_at", "slug", "manifest_version"} {
		wantShape[k] = want[k]
	}
	if diff := cmp.Diff(wantShape, normalize(t, gotShape)); diff != "" {
		t.Errorf("%s: installation mismatch (-want +got):\n%s", label, diff)
	}
	if got.LastRun != nil {
		t.Errorf("%s: last_run = %+v, want none", label, got.LastRun)
	}
}

func checkListedInstallations(t *testing.T, label string, got []domain.Installation, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d installations, want %d", label, len(got), len(want))
	}
	for i := range want {
		checkInstallation(t, label, got[i], asObj(want[i]))
	}
}

func checkExportResult(t *testing.T, label string, got domain.Export, want, body obj) {
	t.Helper()
	if want["installation"] == nil {
		if got.Installation != nil {
			t.Errorf("%s: installation = %+v, want none", label, got.Installation)
		}
	} else if got.Installation == nil {
		t.Errorf("%s: no installation, want %v", label, want["installation"])
	} else {
		checkInstallation(t, label, *got.Installation, asObj(want["installation"]))
	}
	switch wantURL := want["pr_url"].(type) {
	case nil:
		if got.PRURL != nil {
			t.Errorf("%s: pr_url = %q, want none", label, *got.PRURL)
		}
	case string:
		if got.PRURL == nil || *got.PRURL != wantURL {
			t.Errorf("%s: pr_url = %v, want %q", label, got.PRURL, wantURL)
		}
	}

	var wantFiles []obj
	for _, f := range asList(want["files"]) {
		file := asObj(f)
		if file["path"] == nodeRunnerPath {
			wantFiles = append(wantFiles,
				obj{"path": domain.RunnerPath, "contents": testRunner.Encoded, "editable": false},
				obj{"path": domain.RunnerSumPath, "contents": testRunner.SHA256 + "  devdigest-runner\n", "editable": false})
			continue
		}
		wantFiles = append(wantFiles, file)
	}
	if len(got.Files) != len(wantFiles) {
		t.Fatalf("%s: %d files, want %d", label, len(got.Files), len(wantFiles))
	}
	for i, w := range wantFiles {
		g := got.Files[i]
		path := asStr(w["path"])
		if g.Path != path || g.Editable != (w["editable"] == true) {
			t.Errorf("%s: file %d = %s editable=%v, want %s editable=%v", label, i, g.Path, g.Editable, path, w["editable"])
		}
		switch {
		case strings.HasPrefix(path, domain.AgentsDir+"/"):
			if diff := cmp.Diff(parseYAML(t, asStr(w["contents"])), parseYAML(t, g.Contents)); diff != "" {
				t.Errorf("%s: manifest %s mismatch (-want +got):\n%s", label, path, diff)
			}
		case strings.HasPrefix(path, domain.WorkflowsDir+"/"):
			checkWorkflowFile(t, label, g.Contents, asStr(w["contents"]), body)
		default:
			if g.Contents != w["contents"] {
				t.Errorf("%s: file %s contents = %q, want %q", label, path, g.Contents, w["contents"])
			}
		}
	}
}

func parseYAML(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("parse manifest %q: %v", text, err)
	}
	return m
}

var workflowLines = regexp.MustCompile(`(?m)^( {4}types: \[.*\]| {10}DEVDIGEST_(AGENT|POST_AS): .*)$`)

// checkWorkflowFile: an edited workflow is kept verbatim; a generated one differs from Node's on
// purpose (the Go runner steps), so only the lines the request controls are compared.
func checkWorkflowFile(t *testing.T, label, got, nodeText string, body obj) {
	t.Helper()
	if w, ok := body["workflow"]; ok && w != nil {
		if got != nodeText {
			t.Errorf("%s: edited workflow changed", label)
		}
		return
	}
	if diff := cmp.Diff(workflowLines.FindAllString(nodeText, -1), workflowLines.FindAllString(got, -1)); diff != "" {
		t.Errorf("%s: workflow lines mismatch (-node +go):\n%s", label, diff)
	}
	if strings.Contains(got, "setup-node") || !strings.Contains(got, domain.RunnerPath) {
		t.Errorf("%s: generated workflow is not the Go runner workflow", label)
	}
}

// exportCalls adapts the golden's recorded calls to the Go files: the Node bundle path becomes the
// two runner paths and the pull request body gains the runner paragraph (D6003, D6008).
func exportCalls(in []any) []any {
	out := make([]any, 0, len(in))
	for _, c := range in {
		m := obj{}
		for k, v := range asObj(c) {
			m[k] = v
		}
		if paths, ok := m["paths"].([]any); ok {
			var np []any
			for _, p := range paths {
				if p == nodeRunnerPath {
					np = append(np, domain.RunnerPath, domain.RunnerSumPath)
				} else {
					np = append(np, p)
				}
			}
			m["paths"] = np
		}
		if body, ok := m["body"].(string); ok {
			body = strings.Replace(body, "- `"+nodeRunnerPath+"`",
				"- `.devdigest/runner/devdigest-runner-linux-amd64.gz.b64`\n- `.devdigest/runner/devdigest-runner-linux-amd64.sha256`", 1)
			body = strings.Replace(body, "\n\nBranch: `devdigest/ci`",
				"\n\n### Runner\n`.devdigest/runner/devdigest-runner-linux-amd64.gz.b64` is the DevDigest CI runner (linux/amd64), built from DevDigest source `"+
					testRunner.BuildID+"`. SHA-256 of the decoded binary: `"+testRunner.SHA256+"`. The workflow checks it before running.\n\nBranch: `devdigest/ci`", 1)
			m["body"] = body
		}
		out = append(out, m)
	}
	return out
}

func TestExportRunnerFiles(t *testing.T) {
	rig := okRig(t)
	req := request("acme/widgets")
	req.Action = domain.ActionFiles
	got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	byPath := map[string]domain.File{}
	for _, f := range got.Files {
		byPath[f.Path] = f
	}
	if f := byPath[domain.RunnerPath]; f.Contents != testRunner.Encoded || f.Editable {
		t.Errorf("runner file = %+v, want the encoded runner, not editable", f)
	}
	if f := byPath[domain.RunnerSumPath]; f.Contents != testRunner.SHA256+"  devdigest-runner\n" {
		t.Errorf("sum file = %q, want sha256sum text format", f.Contents)
	}
	if f := byPath[domain.WorkflowPath("security-reviewer")]; !f.Editable {
		t.Errorf("workflow file = %+v, want editable", f)
	}
	if len(rig.rec.snapshot(t)) != 1 { // only slugOwner
		t.Errorf("files action made calls beyond slugOwner: %v", rig.rec.snapshot(t))
	}
}

func TestExportPublishesRunnerAsBlob(t *testing.T) {
	rig := okRig(t)
	var got app.CommitFilesInput
	rig.gh.commitFiles = func(_ app.RepoRef, in app.CommitFilesInput) error { got = in; return nil }
	if _, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets")); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	for _, f := range got.Files {
		if want := f.Path == domain.RunnerPath; f.AsBlob != want {
			t.Errorf("file %s AsBlob = %v, want %v", f.Path, f.AsBlob, want)
		}
	}
	if got.Branch != "devdigest/ci" || got.Base != "main" || got.Message != "Add DevDigest CI review" {
		t.Errorf("commit = %s from %s %q, want devdigest/ci from main", got.Branch, got.Base, got.Message)
	}
}

func TestExportNeedsTokenOnlyForOpenPR(t *testing.T) {
	rig := newExportRig(t, rigOpts{agent: defaultAgent(), runner: true, configured: false})
	_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets"))
	wantError(t, err, apperr.KindConfig, "config_error", "GITHUB_TOKEN is not configured")
	for _, c := range rig.rec.snapshot(t) {
		if fn := asObj(c)["fn"]; fn != "slugOwner" {
			t.Errorf("call %v before the token check", fn)
		}
	}
	req := request("acme/widgets")
	req.Action = domain.ActionFiles
	if _, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req); err != nil {
		t.Errorf("files without a token: error = %v, want none", err)
	}
}

func TestExportRunnerMissingMessage(t *testing.T) {
	rig := newExportRig(t, rigOpts{agent: defaultAgent(), runner: false, configured: true})
	_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets"))
	wantError(t, err, apperr.KindInternal, "runner_bundle_missing",
		"The runner binary is missing: rebuild the backend image (docker compose build backend)")
}

func TestExportEmptyWorkflowIsInvalidYAML(t *testing.T) {
	rig := okRig(t)
	req := request("acme/widgets")
	empty := ""
	req.Workflow = &empty
	_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
	ae := wantError(t, err, apperr.KindValidation, "workflow_insecure", "The workflow breaks a security rule")
	if diff := cmp.Diff(map[string]any{"rule": "invalid_yaml"}, normalize(t, ae.Details)); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
}

func TestExportGeneratedWorkflowPassesTheRules(t *testing.T) {
	for _, postAs := range []domain.PostAs{domain.PostAsGitHubReview, domain.PostAsPRComment, domain.PostAsNone} {
		rig := okRig(t)
		req := request("acme/widgets")
		req.Action, req.PostAs = domain.ActionFiles, postAs
		if _, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req); err != nil {
			t.Errorf("Export(post_as=%s) error = %v, want the generated workflow accepted", postAs, err)
		}
	}
}

func TestExportGitHubFailureNeverLeaksTheCause(t *testing.T) {
	rig := okRig(t)
	rig.gh.commitFiles = func(app.RepoRef, app.CommitFilesInput) error {
		return &app.GitHubError{Status: 500, Cause: errors.New("Bearer ghp_secret in url")}
	}
	_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets"))
	wantError(t, err, apperr.KindExternalService, "github_unavailable", "GitHub is unavailable, try again later")
	for _, l := range rig.logs.snapshot(t) {
		if strings.Contains(fmt.Sprint(l), "ghp_secret") {
			t.Errorf("log line %v carries the cause", l)
		}
	}
}

func TestExportListInstallationsUnknownAgent(t *testing.T) {
	rig := newExportRig(t, rigOpts{noAgent: true, agent: defaultAgent(), runner: true})
	_, err := rig.svc.ListInstallations(t.Context(), "ws-1", "nope")
	wantError(t, err, apperr.KindNotFound, "agent_not_found", "Agent not found")
}

func TestExportRefusesAnAgentOfADisabledPlugin(t *testing.T) {
	agent := defaultAgent()
	agent.Plugin = &domain.PluginRef{Name: `Sec "Pack"`, Enabled: false}
	rig := newExportRig(t, rigOpts{agent: agent, runner: true, configured: true})

	for _, action := range []domain.ExportAction{domain.ActionOpenPR, domain.ActionFiles} {
		req := request("acme/widgets")
		req.Action = action
		_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		wantError(t, err, apperr.KindConflict, "plugin_disabled",
			`Agent "Security Reviewer" belongs to the disabled plugin "Sec \"Pack\""`)
	}
	if got := rig.rec.snapshot(t); len(got) != 0 {
		t.Errorf("calls after the plugin_disabled refusal = %v, want none (no store read, no GitHub call)", got)
	}
	if rig.store.saved != nil {
		t.Errorf("installation saved = %+v, want nothing written", rig.store.saved)
	}
}

func TestExportPluginGateComesRightAfterTheAgentLookup(t *testing.T) {
	// Before the target, provider, trigger and repository checks.
	agent := defaultAgent()
	agent.Provider = "anthropic"
	agent.Plugin = &domain.PluginRef{Name: "Pack", Enabled: false}
	rig := newExportRig(t, rigOpts{agent: agent, runner: true, configured: true})
	req := request("nowhere/none")
	req.Target = "gitlab"
	_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
	wantError(t, err, apperr.KindConflict, "plugin_disabled", "")

	// After the 404: an unknown agent answers agent_not_found, not plugin_disabled.
	missing := newExportRig(t, rigOpts{noAgent: true, agent: agent, runner: true, configured: true})
	_, err = missing.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets"))
	wantError(t, err, apperr.KindNotFound, "agent_not_found", "Agent not found")
}

func TestExportOfAnEnabledPluginAgentOpensThePR(t *testing.T) {
	agent := defaultAgent()
	agent.Plugin = &domain.PluginRef{Name: "Pack", Enabled: true}
	rig := newExportRig(t, rigOpts{agent: agent, runner: true, configured: true})
	got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets"))
	if err != nil {
		t.Fatalf("Export() error = %v, want none for an enabled plugin", err)
	}
	if got.PRURL == nil || *got.PRURL == "" {
		t.Errorf("Export().PRURL = %v, want the opened pull request", got.PRURL)
	}
}
