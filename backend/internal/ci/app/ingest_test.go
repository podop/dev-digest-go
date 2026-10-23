package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/adapters/zipentry"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// testClock is a clock the fake GitHub advances by a latency per call.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// ingestRig is one Ingest service over fakes.
type ingestRig struct {
	svc   *app.IngestService
	store *fakeStore
	gh    *fakeGitHub
	logs  *logCapture
	clock *testClock
}

var ingestStart = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

func newIngestRig(t testing.TB, agents ...string) *ingestRig {
	t.Helper()
	clock := &testClock{now: ingestStart}
	store := &fakeStore{calls: &calls{}}
	gh := &fakeGitHub{calls: &calls{}, configured: true}
	gh.listRuns = func(app.RepoRef, string, int) ([]domain.WorkflowRun, error) { return nil, nil }
	gh.listArtifact = func(app.RepoRef, int64) ([]domain.RunArtifact, error) { return nil, nil }
	gh.download = func(app.RepoRef, int64, int64) ([]byte, error) { return nil, errors.New("no download scripted") }
	dir := fakeAgents{agents: map[string]domain.ExportAgent{}}
	for _, a := range agents {
		dir.agents[a] = domain.ExportAgent{ID: a}
	}
	logs := newLogCapture()
	svc := app.NewIngestService(app.IngestDeps{
		Agents: dir, Store: store, GitHub: gh, Zip: zipentry.New(), Clock: clock.Now, Log: logs.logger(),
	})
	return &ingestRig{svc: svc, store: store, gh: gh, logs: logs, clock: clock}
}

// TestIngestGolden runs the 26 refresh scenarios of the Node golden.
func TestIngestGolden(t *testing.T) {
	spec, corpus, golden := scenarios(t, "ingest")
	for i := range golden {
		t.Run(asStr(golden[i]["name"]), func(t *testing.T) {
			runIngestScenario(t, spec, expand(t, corpus[i]).(obj), golden[i])
		})
	}
}

func runIngestScenario(t *testing.T, spec, scenario, gold obj) {
	t.Helper()
	world := asObj(scenario["world"])
	var agents []string
	for _, a := range asList(world["agents"]) {
		agents = append(agents, asStr(a))
	}
	rig := newIngestRig(t, agents...)
	rig.gh.configured = world["githubFail"] != "config"
	latency := time.Duration(asInt(world["latency"])) * time.Millisecond
	rig.gh.onCall = func() { rig.clock.now = rig.clock.now.Add(latency) }

	var installs []obj
	for _, w := range asList(world["installations"]) {
		inst := mergeObj(asObj(spec["installation"]), asObj(w))
		installs = append(installs, inst)
		rig.store.installs = append(rig.store.installs, installationOf(inst))
	}
	rig.store.stored = map[domain.RunKey]bool{}
	for _, k := range asList(world["stored"]) {
		rig.store.stored[parseKey(t, asStr(k))] = true
	}
	rig.store.recordFalse = map[domain.RunKey]bool{}
	for _, k := range asList(world["recordFalse"]) {
		rig.store.recordFalse[parseKey(t, asStr(k))] = true
	}
	if msg := asStr(asObj(world["dbFail"])["storedRunKeys"]); msg != "" {
		rig.store.keysErr = errors.New(msg)
	}
	if msg := asStr(asObj(world["dbFail"])["recordRun"]); msg != "" {
		rig.store.recordErr = errors.New(msg)
	}

	rig.gh.listRuns = func(repo app.RepoRef, _ string, _ int) ([]domain.WorkflowRun, error) {
		var instID string
		for _, i := range installs {
			if asStr(i["repo"]) == repo.Owner+"/"+repo.Name {
				instID = asStr(i["id"])
				break
			}
		}
		entry := asObj(world["runs"])[instID]
		if th, ok := entry.(obj); ok {
			return nil, githubFailure(th["throw"])
		}
		var out []domain.WorkflowRun
		for _, r := range asList(entry) {
			out = append(out, workflowRunOf(mergeObj(asObj(spec["run"]), asObj(r))))
		}
		return out, nil
	}
	rig.gh.listArtifact = func(_ app.RepoRef, runID int64) ([]domain.RunArtifact, error) {
		entry := asObj(world["artifacts"])[itoa(runID)]
		if th, ok := entry.(obj); ok {
			return nil, githubFailure(th["throw"])
		}
		var out []domain.RunArtifact
		for _, a := range asList(entry) {
			m := mergeObj(asObj(spec["artifact"]), asObj(a))
			out = append(out, domain.RunArtifact{
				ID: asInt64(m["id"]), Name: asStr(m["name"]), SizeInBytes: asInt64(m["size_in_bytes"]),
				Expired: m["expired"] == true, CreatedAt: asStr(m["created_at"]),
			})
		}
		return out, nil
	}
	rig.gh.download = func(_ app.RepoRef, id, maxBytes int64) ([]byte, error) {
		d := asObj(asObj(world["downloads"])[itoa(id)])
		switch {
		case d == nil:
			return nil, errors.New("no download scripted")
		case d["throw"] != nil:
			return nil, githubFailure(d["throw"])
		case d["null"] == true:
			return nil, app.ErrArtifactTooLarge
		}
		var data []byte
		switch {
		case d["raw"] != nil:
			data = bytesOf(t, asObj(d["raw"]))
		case d["zip"] != nil:
			data = buildZip(t, asObj(d["zip"]))
		default:
			result := mergeAbsent(asObj(spec["result"]), asObj(d["result"]))
			data = buildZip(t, obj{"entries": []any{obj{"name": domain.ResultFileName, "json": result}}})
		}
		if int64(len(data)) > maxBytes {
			return nil, app.ErrArtifactTooLarge
		}
		return data, nil
	}

	got, err := rig.svc.Refresh(context.Background(), "ws-1", asStr(scenario["agentId"]))
	if gerr, failed := gold["error"].(obj); failed {
		checkRefreshError(t, err, gerr)
	} else if err != nil {
		t.Fatalf("Refresh() error = %v, want a result", err)
	} else if diff := cmp.Diff(gold["result"], normalize(t, refreshShape(got))); diff != "" {
		t.Errorf("Refresh() result mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(asList(gold["recorded"]), normalize(t, recordedShapes(rig.store.recorded)).([]any)); diff != "" {
		t.Errorf("recorded runs mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(asList(gold["githubCalls"]), rig.gh.calls.snapshot(t)); diff != "" {
		t.Errorf("github calls mismatch (-want +got):\n%s", diff)
	}
	// Go does not ask the store about an empty run list.
	wantStore := []any{}
	for _, c := range asList(gold["storeCalls"]) {
		if asObj(c)["fn"] == "storedRunKeys" && len(asList(asObj(c)["keys"])) == 0 {
			continue
		}
		wantStore = append(wantStore, c)
	}
	if diff := cmp.Diff(wantStore, rig.store.calls.snapshot(t)); diff != "" {
		t.Errorf("store calls mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(asList(gold["logs"]), rig.logs.snapshot(t)); diff != "" {
		t.Errorf("logs mismatch (-want +got):\n%s", diff)
	}
}

func checkRefreshError(t *testing.T, err error, want obj) {
	t.Helper()
	if err == nil {
		t.Fatalf("Refresh() = no error, want %v", want)
	}
	if want["kind"] == nil { // a plain Error of the store: the database failure surfaces as is
		if _, ok := apperr.As(err); ok || !strings.Contains(err.Error(), asStr(want["message"])) {
			t.Errorf("Refresh() error = %v, want a plain error carrying %q", err, want["message"])
		}
		return
	}
	message := asStr(want["message"])
	if want["code"] == "config_error" { // Node says "GitHub token is not configured"; the spec (7.2) says GITHUB_TOKEN
		message = "GITHUB_TOKEN is not configured"
	}
	wantError(t, err, kinds[asStr(want["kind"])], asStr(want["code"]), message)
}

func installationOf(m obj) domain.Installation {
	inst := domain.Installation{
		ID: asStr(m["id"]), AgentID: asStr(m["agent_id"]), Repo: asStr(m["repo"]), TargetType: "gha",
		InstalledAt: installedAt, Slug: optStr(m["slug"]), ManifestVersion: optInt(m["manifest_version"]),
	}
	return inst
}

func workflowRunOf(m obj) domain.WorkflowRun {
	run := domain.WorkflowRun{
		ID: asInt64(m["id"]), RunAttempt: asInt(m["run_attempt"]), HeadSHA: asStr(m["head_sha"]), Path: asStr(m["path"]),
		Event: asStr(m["event"]), Conclusion: optStr(m["conclusion"]), HTMLURL: asStr(m["html_url"]),
		RepositoryID: asInt64(m["repository_id"]), CreatedAt: asStr(m["created_at"]),
	}
	for _, n := range asList(m["pr_numbers"]) {
		run.PRNumbers = append(run.PRNumbers, asInt(n))
	}
	return run
}

// githubFailure is the error of a scripted failure: a status (with the rate limit header) or a
// transport error.
func githubFailure(spec any) error {
	m := asObj(spec)
	if status := asInt(m["status"]); status != 0 {
		remaining := asObj(m["headers"])["x-ratelimit-remaining"]
		return &app.GitHubError{Status: status, RateLimited: remaining == "0"}
	}
	return &app.GitHubError{Transport: true, Cause: errors.New(asStr(m["message"]))}
}

func parseKey(t testing.TB, s string) domain.RunKey {
	t.Helper()
	id, attempt, ok := strings.Cut(s, ":")
	if !ok {
		t.Fatalf("bad run key %q", s)
	}
	var k domain.RunKey
	if err := json.Unmarshal([]byte(id), &k.GithubRunID); err != nil {
		t.Fatalf("bad run key %q: %v", s, err)
	}
	if err := json.Unmarshal([]byte(attempt), &k.Attempt); err != nil {
		t.Fatalf("bad run key %q: %v", s, err)
	}
	return k
}

// bytesOf is the generator's bytesOf: parts, hex, json or text.
func bytesOf(t testing.TB, spec obj) []byte {
	t.Helper()
	if parts, ok := spec["parts"]; ok {
		var b []byte
		for _, p := range asList(parts) {
			b = append(b, bytesOf(t, asObj(p))...)
		}
		return b
	}
	if h, ok := spec["hex"]; ok {
		b, err := hex.DecodeString(asStr(h))
		if err != nil {
			t.Fatalf("bad hex: %v", err)
		}
		return b
	}
	if j, ok := spec["json"]; ok {
		b, err := json.Marshal(j)
		if err != nil {
			t.Fatalf("marshal %v: %v", j, err)
		}
		return b
	}
	return []byte(asStr(spec["text"]))
}

// buildZip writes the ZIP of a spec: entries of bytesOf, deflated.
func buildZip(t testing.TB, spec obj) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range asList(spec["entries"]) {
		entry := asObj(e)
		w, err := zw.Create(asStr(entry["name"]))
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(bytesOf(t, entry)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func refreshShape(r domain.RefreshResult) obj {
	installations := []any{}
	for _, i := range r.Installations {
		skipped := []any{}
		for _, s := range i.Skipped {
			skipped = append(skipped, obj{"github_run_id": s.GithubRunID, "reason": string(s.Reason)})
		}
		m := obj{
			"installation_id": i.InstallationID, "repo": i.Repo, "state": string(i.State),
			"ingested": i.Ingested, "skipped": skipped,
		}
		if i.ErrorCode != "" {
			m["error_code"] = string(i.ErrorCode)
		}
		installations = append(installations, m)
	}
	return obj{"ingested": r.Ingested, "degraded": r.Degraded, "installations": installations}
}

func recordedShapes(runs []domain.NewRun) []any {
	out := []any{}
	for _, r := range runs {
		out = append(out, obj{
			"workspaceId": r.WorkspaceID, "installationId": r.InstallationID, "agentId": r.AgentID,
			"githubRunId": r.GithubRunID, "runAttempt": r.RunAttempt, "headSha": r.HeadSHA, "githubUrl": r.GithubURL,
			"ranAt": r.RanAt.UTC().Format("2006-01-02T15:04:05.000Z"), "prNumber": r.PRNumber,
			"manifestVersion": r.ManifestVersion, "status": r.Status, "model": r.Model, "costUsd": r.CostUSD,
			"durationMs": r.DurationMS, "findingsCount": r.FindingsCount, "blockers": r.Blockers, "error": r.Error,
		})
	}
	return out
}
