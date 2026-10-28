package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/repointel/domain"
)

// Phase 3 component tests: the two repo-intel routes, the three triggers (clone, refresh,
// startup backfill) and the ranked conventions sample, all through serve with real adapters.

const (
	// synthesised is the state of a repo without an index row (spec 7.2), byte for byte.
	synthesised = `{"repoId":"%s","status":"degraded","filesIndexed":0,"filesSkipped":0,"durationMs":0,` +
		`"reason":"no_data","lastIndexedSha":"","indexerVersion":3,"updatedAt":"1970-01-01T00:00:00.000Z",` +
		`"degraded":true,"degradedReason":"no_data"}`
	upperRepoID = "5F0C3A9E-1B2D-4C3E-8F4A-0123456789AB"
)

// intelFixture is a small module graph: src/util.ts and src/core.ts are imported by the three
// leaf files, so they outrank them; the leaves tie and sort by path.
var intelFixture = map[string]string{
	"src/core.ts": "import { util } from \"./util\";\n\nexport function core(): string {\n  return util();\n}\n",
	"src/util.ts": "export function util(): string {\n  return \"util\";\n}\n",
	"src/a.ts":    "import { core } from \"./core\";\nimport { util } from \"./util\";\n\nexport const a = core() + util();\n",
	"src/b.ts":    "import { core } from \"./core\";\nimport { util } from \"./util\";\n\nexport const b = core() + util();\n",
	"src/c.ts":    "import { core } from \"./core\";\nimport { util } from \"./util\";\n\nexport const c = core() + util();\n",
}

// intelOn switches REPO_INTEL_ENABLED on (the test config leaves it at its zero value).
func intelOn(c *config.Config) { c.RepoIntelEnabled = true }

// putIndexState writes (or replaces) the repo_index_state row of repoID.
func putIndexState(t testing.TB, db *gorm.DB, repoID, status string, indexed, skipped int, sha string, stats string) {
	t.Helper()
	err := db.Exec(`INSERT INTO repo_index_state (repo_id, last_indexed_sha, indexer_version, status, files_indexed, files_skipped, stats, updated_at)
		VALUES (?, ?, 3, ?, ?, ?, ?::jsonb, '2026-10-22T10:00:00.123Z')
		ON CONFLICT (repo_id) DO UPDATE SET last_indexed_sha = EXCLUDED.last_indexed_sha, indexer_version = EXCLUDED.indexer_version,
			status = EXCLUDED.status, files_indexed = EXCLUDED.files_indexed, files_skipped = EXCLUDED.files_skipped,
			stats = EXCLUDED.stats, updated_at = EXCLUDED.updated_at`,
		repoID, sha, status, indexed, skipped, stats).Error
	if err != nil {
		t.Fatalf("write repo_index_state of %s: %v", repoID, err)
	}
}

// topLevelKeys returns the keys of a JSON object in the order they appear in body.
func topLevelKeys(t testing.TB, body []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("body is not a JSON object: %q (%v)", body, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
	}
	return keys
}

// payloadOf returns the payload of the job row id decoded as a JSON object.
func payloadOf(t testing.TB, db *gorm.DB, id string) map[string]any {
	t.Helper()
	raw := scalar[string](t, db, `SELECT payload::text FROM jobs WHERE id = ?`, id)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("payload of job %s is not an object: %q: %v", id, raw, err)
	}
	return out
}

// pushCommit adds a commit that rewrites rel in the bare remote <remoteDir>/<owner>/<name>.git
// and returns the new head.
func pushCommit(t testing.TB, remoteDir, owner, name, rel, content string) string {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", filepath.Join(remoteDir, owner, name+".git"), ".")
	if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(rel)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "change "+rel)
	gitRun(t, work, "push", "-q", "origin", "main")
	return gitRun(t, work, "rev-parse", "HEAD")
}

// stateStack starts a server on the seeded acme/payments-api repo (never cloned, so no index job
// ever runs for it) and returns the stack and that repo's id.
func stateStack(t *testing.T, sp spec) (*stack, string) {
	t.Helper()
	sp.config = chain(intelOn, sp.config)
	s := startStack(t, sp)
	return s, repoByName(t, s, "acme/payments-api")
}

// chain applies several config adjustments in order; nil entries are skipped.
func chain(fs ...func(*config.Config)) func(*config.Config) {
	return func(c *config.Config) {
		for _, f := range fs {
			if f != nil {
				f(c)
			}
		}
	}
}

// AC3001, AC3002, AC3003, AC3011: GET /repos/:id/index-state for a persisted full state, a
// persisted degraded state, a failed state without degradedReason and a repo without a row; the
// full, degraded and synthesised bodies are the contract goldens.
func TestIndexState_PersistedAndSynthesised(t *testing.T) {
	s, repo := stateStack(t, spec{})
	g := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
	const sha = "0123456789abcdef0123456789abcdef01234567"

	t.Run("full: exact keys in order, no internal stats", func(t *testing.T) {
		putIndexState(t, s.db.DB, repo, "full", 42, 3, sha, `{"durationMs":1234,"bounded":7,"softBudgetReached":false}`)
		resp := s.get("/repos/" + repo + "/index-state")
		g.t = t
		g.check("repo_index_state_full", resp, http.StatusOK)
		wantKeys := []string{"repoId", "status", "filesIndexed", "filesSkipped", "durationMs", "lastIndexedSha", "indexerVersion", "updatedAt"}
		if diff := cmp.Diff(wantKeys, topLevelKeys(t, resp.Body)); diff != "" {
			t.Errorf("GET index-state keys mismatch (-want +got):\n%s", diff)
		}
		got := resp.Obj(t)
		if got["durationMs"] != float64(1234) || got["repoId"] != repo || got["lastIndexedSha"] != sha ||
			got["filesIndexed"] != float64(42) || got["filesSkipped"] != float64(3) || got["indexerVersion"] != float64(domain.IndexerVersion) {
			t.Errorf("GET index-state = %s, want the persisted row", resp.Body)
		}
		if at, _ := got["updatedAt"].(string); at != "2026-10-22T10:00:00.123Z" {
			t.Errorf("updatedAt = %q, want 2026-10-22T10:00:00.123Z", at)
		}
	})

	t.Run("degraded: reason and degradedReason from stats", func(t *testing.T) {
		putIndexState(t, s.db.DB, repo, "degraded", 0, 0, "", `{"reason":"no_clone","degradedReason":"no_data"}`)
		resp := s.get("/repos/" + repo + "/index-state")
		g.t = t
		g.check("repo_index_state_degraded", resp, http.StatusOK)
		got := resp.Obj(t)
		if got["reason"] != "no_clone" || got["degraded"] != true || got["degradedReason"] != "no_data" || got["status"] != "degraded" {
			t.Errorf("GET index-state = %s, want reason no_clone, degraded true, degradedReason no_data", resp.Body)
		}
	})

	t.Run("failed without degradedReason is index_failed", func(t *testing.T) {
		putIndexState(t, s.db.DB, repo, "failed", 0, 0, "", `{"reason":"index_failed"}`)
		got := s.get("/repos/" + repo + "/index-state").Obj(t)
		if got["status"] != "failed" || got["degraded"] != true || got["degradedReason"] != "index_failed" {
			t.Errorf("GET index-state of a failed state = %v, want degraded true, degradedReason index_failed", got)
		}
	})

	t.Run("synthesised for an upper-case uuid without a row", func(t *testing.T) {
		resp := s.get("/repos/" + upperRepoID + "/index-state")
		g.t = t
		g.check("repo_index_state_synth", resp, http.StatusOK)
		want := strings.Replace(synthesised, "%s", upperRepoID, 1)
		if got := string(bytes.TrimSpace(resp.Body)); got != want {
			t.Errorf("GET index-state of an unknown repo =\n%s\nwant\n%s", got, want)
		}
	})
}

// AC3005: REPO_INTEL_ENABLED=false changes nothing for the index-state route.
func TestIndexState_FlagOffAnswersLikeFlagOn(t *testing.T) {
	db := pgtest.Migrated(t)
	on, repo := stateStack(t, spec{db: db})
	putIndexState(t, db.DB, repo, "full", 42, 3, "0123456789abcdef0123456789abcdef01234567", `{"durationMs":1234}`)
	want := on.get("/repos/" + repo + "/index-state")
	if want.Status != http.StatusOK {
		t.Fatalf("GET index-state with the flag on = %d %s, want 200", want.Status, want.Body)
	}
	if err := on.stop(); err != nil {
		t.Fatal(err)
	}

	off := startStack(t, spec{db: db, config: func(c *config.Config) { c.RepoIntelEnabled = false }})
	got := off.get("/repos/" + repo + "/index-state")
	if got.Status != want.Status || !bytes.Equal(got.Body, want.Body) {
		t.Errorf("GET index-state with the flag off = %d %s, want %d %s", got.Status, got.Body, want.Status, want.Body)
	}
}

// AC3006: a path id that is not a UUID is a 422 on /id for both routes.
func TestIntelRoutes_RejectNonUUID(t *testing.T) {
	s := startStack(t, spec{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/repos/abc/index-state"},
		{http.MethodPost, "/repos/abc/resync"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := s.do(tc.method, tc.path, nil)
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
				t.Fatalf("%s %s = %d %s, want 422 validation_error", tc.method, tc.path, resp.Status, resp.Body)
			}
			details, _ := resp.Obj(t)["error"].(map[string]any)["details"].([]any)
			if len(details) == 0 {
				t.Fatalf("%s %s: no details in %s", tc.method, tc.path, resp.Body)
			}
			if got := details[0].(map[string]any)["instancePath"]; got != "/id" {
				t.Errorf("%s %s: details[0].instancePath = %v, want /id", tc.method, tc.path, got)
			}
		})
	}
}

// AC3007, AC3011: a resync is accepted without a body, with a text/plain body and with a JSON
// body (the body is never read); each gets its own job id and a repo-intel-resync row.
func TestResync_AcceptsAnyBodyAndEnqueuesOneJobEach(t *testing.T) {
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", intelFixture)
	s := startStack(t, spec{remoteDir: remote, config: intelOn})
	g := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)

	requests := []struct {
		name string
		body any
		hdr  []string
	}{
		{name: "no body"},
		{name: "text/plain", body: "ignored text", hdr: []string{"Content-Type", "text/plain"}},
		{name: "json body", body: map[string]any{"anything": []int{1, 2, 3}}},
	}
	ids := map[string]bool{}
	for i, rq := range requests {
		resp := s.do(http.MethodPost, "/repos/"+repo+"/resync", rq.body, rq.hdr...)
		if resp.Status != http.StatusAccepted {
			t.Fatalf("%s: POST resync = %d %s, want 202", rq.name, resp.Status, resp.Body)
		}
		if i == 0 {
			g.check("repo_resync_accepted", resp, http.StatusAccepted)
		}
		body := resp.Obj(t)
		id, _ := body["jobId"].(string)
		if len(body) != 2 || body["status"] != "accepted" || !uuidPattern.MatchString(id) {
			t.Fatalf("%s: POST resync body = %s, want {status:accepted, jobId:<uuid>}", rq.name, resp.Body)
		}
		if ids[id] {
			t.Errorf("%s: job id %s was already handed out", rq.name, id)
		}
		ids[id] = true
		if kind := scalar[string](t, s.db.DB, `SELECT kind FROM jobs WHERE id = ?`, id); kind != "repo-intel-resync" {
			t.Errorf("%s: job %s has kind %q, want repo-intel-resync", rq.name, id, kind)
		}
		if diff := cmp.Diff(map[string]any{"repoId": repo}, payloadOf(t, s.db.DB, id)); diff != "" {
			t.Errorf("%s: payload of job %s mismatch (-want +got):\n%s", rq.name, id, diff)
		}
		// One at a time: concurrent syncs of one clone are not what this test is about.
		s.waitJobs("repo-intel-resync", i+1)
	}
}

// AC3008, AC3011: when the runner refuses the enqueue the answer is the degraded 202.
func TestResync_DegradedWhenNoHandlerIsRegistered(t *testing.T) {
	s := startStack(t, spec{noIntelJobs: true})
	g := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
	const repo = "7b2f6c1e-3d2a-4c5b-9a41-0f6e2d8c9b10"
	resp := s.post("/repos/"+repo+"/resync", nil)
	g.check("repo_resync_degraded", resp, http.StatusAccepted)
	want := `{"status":"accepted","degraded":true,"reason":"no_handler"}`
	if got := string(bytes.TrimSpace(resp.Body)); got != want {
		t.Errorf("POST resync with no handler = %s, want %s", got, want)
	}
	if n := len(jobRows(t, s.db.DB, "")); n != 0 {
		t.Errorf("jobs after a refused enqueue = %d, want 0", n)
	}
}

// AC3009: an unknown repo is not an error: 202 with a job that ends done and writes nothing.
func TestResync_UnknownRepoEndsDoneWithoutState(t *testing.T) {
	s := startStack(t, spec{config: intelOn})
	const repo = "7b2f6c1e-3d2a-4c5b-9a41-0f6e2d8c9b10"
	resp := s.post("/repos/"+repo+"/resync", nil)
	if resp.Status != http.StatusAccepted {
		t.Fatalf("POST resync of an unknown repo = %d %s, want 202", resp.Status, resp.Body)
	}
	if id, _ := resp.Obj(t)["jobId"].(string); id == "" {
		t.Fatalf("POST resync body = %s, want a jobId", resp.Body)
	}
	rows := s.waitJobs("repo-intel-resync", 1)
	if len(rows) != 1 || rows[0].Status != "done" {
		t.Errorf("resync jobs = %+v, want one done", rows)
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM repo_index_state WHERE repo_id = ?`, repo); n != 0 {
		t.Errorf("repo_index_state rows for the unknown repo = %d, want 0", n)
	}
}

// AC3010: a resync with a foreign Origin is refused and enqueues nothing.
func TestResync_ForeignOriginIsForbidden(t *testing.T) {
	s := startStack(t, spec{config: intelOn})
	const repo = "7b2f6c1e-3d2a-4c5b-9a41-0f6e2d8c9b10"
	resp := s.do(http.MethodPost, "/repos/"+repo+"/resync", nil, "Origin", "http://evil.example")
	if resp.Status != http.StatusForbidden || resp.errorCode(t) != "forbidden" {
		t.Fatalf("POST resync with a foreign Origin = %d %s, want 403 forbidden", resp.Status, resp.Body)
	}
	if n := len(jobRows(t, s.db.DB, "")); n != 0 {
		t.Errorf("jobs after the refused request = %d, want 0", n)
	}
}

// AC3014: a finished clone enqueues repo-intel-index with {repoId, owner, name}; when that
// enqueue fails, the clone job still ends done.
func TestClone_EnqueuesIndex(t *testing.T) {
	t.Run("enqueued", func(t *testing.T) {
		remote := t.TempDir()
		makeFilesRemote(t, remote, "o", "r", intelFixture)
		s := startStack(t, spec{remoteDir: remote, config: intelOn})
		resp := s.post("/repos", map[string]string{"url": "https://github.com/o/r"})
		if resp.Status != http.StatusCreated {
			t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
		}
		repo := idOf(t, resp)
		if row := s.waitJobs("clone", 1)[0]; row.Status != "done" {
			t.Fatalf("clone job = %+v, want done", row)
		}
		rows := s.waitJobs("repo-intel-index", 1)
		if len(rows) != 1 {
			t.Fatalf("repo-intel-index jobs = %+v, want one", rows)
		}
		want := map[string]any{"repoId": repo, "owner": "o", "name": "r"}
		if diff := cmp.Diff(want, payloadOf(t, s.db.DB, rows[0].ID)); diff != "" {
			t.Errorf("payload of the index job mismatch (-want +got):\n%s", diff)
		}
		if rows[0].Status != "done" {
			t.Errorf("index job = %+v, want done", rows[0])
		}
	})

	t.Run("enqueue fails, clone job stays done", func(t *testing.T) {
		remote := t.TempDir()
		makeFilesRemote(t, remote, "o", "r", intelFixture)
		s := startStack(t, spec{remoteDir: remote, noIntelJobs: true})
		if resp := s.post("/repos", map[string]string{"url": "https://github.com/o/r"}); resp.Status != http.StatusCreated {
			t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
		}
		if row := s.waitJobs("clone", 1)[0]; row.Status != "done" {
			t.Fatalf("clone job = %+v, want done although the index job cannot be enqueued", row)
		}
		if n := len(jobRows(t, s.db.DB, "repo-intel-index")); n != 0 {
			t.Errorf("repo-intel-index jobs = %d, want 0", n)
		}
		if path := scalar[*string](t, s.db.DB, `SELECT clone_path FROM repos WHERE full_name = 'o/r'`); path == nil {
			t.Error("clone_path is null after the clone job, want it stored")
		}
		if lines := logLines(t, s.logs, "repo-intel index not enqueued"); len(lines) != 1 {
			t.Errorf("enqueue failure log lines = %v, want exactly one", lines)
		}
	})
}

// AC3015: a refresh answers 200 refreshing and, after its sync succeeded, enqueues
// repo-intel-refresh with {repoId}; a failing sync enqueues nothing.
func TestRefresh_EnqueuesIncrementalIndexAfterASuccessfulSync(t *testing.T) {
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", intelFixture)
	s := startStack(t, spec{remoteDir: remote, config: intelOn})
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)

	resp := s.post("/repos/"+repo+"/refresh", nil)
	if resp.Status != http.StatusOK || string(bytes.TrimSpace(resp.Body)) != `{"status":"refreshing"}` {
		t.Fatalf("POST refresh = %d %s, want 200 {\"status\":\"refreshing\"}", resp.Status, resp.Body)
	}
	if row := s.waitJobs("refresh", 1)[0]; row.Status != "done" {
		t.Fatalf("refresh job = %+v, want done", row)
	}
	rows := s.waitJobs("repo-intel-refresh", 1)
	if diff := cmp.Diff(map[string]any{"repoId": repo}, payloadOf(t, s.db.DB, rows[0].ID)); diff != "" {
		t.Errorf("payload of the refresh index job mismatch (-want +got):\n%s", diff)
	}

	// The remote disappears: the sync fails, and no further index job is enqueued.
	if err := os.Rename(filepath.Join(remote, "o", "r.git"), filepath.Join(remote, "o", "r.moved")); err != nil {
		t.Fatal(err)
	}
	if resp := s.post("/repos/"+repo+"/refresh", nil); resp.Status != http.StatusOK {
		t.Fatalf("second POST refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	if rows := s.waitJobs("refresh", 2); rows[1].Status != "failed" {
		t.Fatalf("refresh jobs = %+v, want the second failed", rows)
	}
	if n := len(jobRows(t, s.db.DB, "repo-intel-refresh")); n != 1 {
		t.Errorf("repo-intel-refresh jobs after a failed sync = %d, want still 1", n)
	}
}

// AC3016: at startup exactly the cloned repos without a state of the current version get an index
// job, and they are enqueued before the listener accepts requests.
func TestStartup_BackfillEnqueuesBeforeListening(t *testing.T) {
	db := pgtest.Migrated(t)
	if err := startStack(t, spec{db: db}).stop(); err != nil { // migrate + seed
		t.Fatal(err)
	}
	ws := scalar[string](t, db.DB, `SELECT id::text FROM workspaces LIMIT 1`)
	insert := func(name string, cloned bool) string {
		path := (*string)(nil)
		if cloned {
			p := filepath.Join(t.TempDir(), name)
			path = &p
		}
		return scalar[string](t, db.DB, `INSERT INTO repos (workspace_id, owner, name, full_name, clone_path)
			VALUES (?, 'o', ?, ?, ?) RETURNING id::text`, ws, name, "o/"+name, path)
	}
	fresh, stale, current, uncloned := insert("fresh", true), insert("stale", true), insert("current", true), insert("uncloned", false)
	putIndexState(t, db.DB, stale, "full", 1, 0, "abc", `{}`)
	if err := db.DB.Exec(`UPDATE repo_index_state SET indexer_version = 1 WHERE repo_id = ?`, stale).Error; err != nil {
		t.Fatal(err)
	}
	putIndexState(t, db.DB, current, "full", 1, 0, "abc", `{}`)

	var atListen []jobRow
	ctx, cancel := context.WithCancel(t.Context())
	logs := &syncBuffer{}
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- serve(ctx, newConfigWith(t, db, intelOn), withLogOutput(logs), func(o *options) {
			o.onListen = func(net.Addr) {
				atListen = jobRows(t, db.DB, "repo-intel-index")
				close(ready)
			}
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("serve returned before listening: %v\n%s", err, logs.String())
	case <-time.After(60 * time.Second):
		t.Fatalf("serve did not start listening\n%s", logs.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve() = %v after the stop signal, want nil", err)
	}

	var got []string
	for _, r := range atListen {
		got = append(got, payloadOf(t, db.DB, r.ID)["repoId"].(string))
	}
	slices.Sort(got)
	want := []string{fresh, stale}
	slices.Sort(want)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("repo-intel-index jobs at the moment the listener came up (-want +got; uncloned %s, current %s):\n%s", uncloned, current, diff)
	}
	if n := len(jobRows(t, db.DB, "repo-intel-index")); n != 2 {
		t.Errorf("repo-intel-index jobs after the run = %d, want 2", n)
	}
}

// newConfigWith is newConfig with config adjustments applied.
func newConfigWith(t testing.TB, db *pgtest.Database, fs ...func(*config.Config)) config.Config {
	t.Helper()
	cfg := newConfig(t, db, spec{})
	chain(fs...)(&cfg)
	return cfg
}

// AC3019: a resync after the remote moved on updates the clone and runs an incremental index of
// the one changed file.
func TestResync_IndexesTheChangedFileIncrementally(t *testing.T) {
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", intelFixture)
	s := startStack(t, spec{remoteDir: remote, config: intelOn})
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)

	clonePath := filepath.Join(s.cfg.CloneDir, "o", "r")
	shaA := gitRun(t, clonePath, "rev-parse", "HEAD")
	before := s.get("/repos/" + repo + "/index-state").Obj(t)
	if before["status"] != "full" || before["lastIndexedSha"] != shaA {
		t.Fatalf("state after the first index = %v, want full at %s", before, shaA)
	}

	shaB := pushCommit(t, remote, "o", "r", "src/util.ts", "export function util(): string {\n  return \"util v2\";\n}\n")
	if shaA == shaB {
		t.Fatal("the pushed commit has the same sha as the clone head")
	}
	if resp := s.post("/repos/"+repo+"/resync", nil); resp.Status != http.StatusAccepted {
		t.Fatalf("POST resync = %d %s, want 202", resp.Status, resp.Body)
	}
	if rows := s.waitJobs("repo-intel-resync", 1); rows[0].Status != "done" {
		t.Fatalf("resync job = %+v, want done", rows[0])
	}

	if head := gitRun(t, clonePath, "rev-parse", "HEAD"); head != shaB {
		t.Errorf("clone head after the resync = %s, want %s", head, shaB)
	}
	after := s.get("/repos/" + repo + "/index-state").Obj(t)
	if after["status"] != "full" || after["lastIndexedSha"] != shaB {
		t.Errorf("state after the resync = %v, want full at %s", after, shaB)
	}
	var stats struct {
		Incremental  bool `json:"incremental"`
		ChangedFiles int  `json:"changedFiles"`
	}
	raw := scalar[string](t, s.db.DB, `SELECT stats::text FROM repo_index_state WHERE repo_id = ?`, repo)
	if err := json.Unmarshal([]byte(raw), &stats); err != nil {
		t.Fatalf("stats %q: %v", raw, err)
	}
	if !stats.Incremental || stats.ChangedFiles != 1 {
		t.Errorf("stats = %s, want incremental true and changedFiles 1", raw)
	}
}

// sampledFiles returns the paths a conventions scan sampled, in sample order.
func sampledFiles(t testing.TB, scan map[string]any) []string {
	t.Helper()
	raw, _ := scan["sampled_files"].([]any)
	var out []string
	for _, p := range raw {
		out = append(out, p.(string))
	}
	return out
}

// AC3064: with an index, the conventions sample takes its sources from the ranker, in rank order;
// a failing ranker, or ranked files that are not in the clone, bring back the Phase 2 picks.
func TestConventions_SampleFollowsTheRank(t *testing.T) {
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", intelFixture)
	s := startStack(t, spec{remoteDir: remote, config: chain(intelOn, mockProvider), llm: fastLLM()})
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)
	if state := s.get("/repos/" + repo + "/index-state").Obj(t); state["status"] != "full" {
		t.Fatalf("index state = %v, want full", state)
	}

	var ranked []string
	if err := s.db.DB.Raw(`SELECT file_path FROM file_rank WHERE repo_id = ? ORDER BY rank DESC, file_path COLLATE "C"`, repo).Scan(&ranked).Error; err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 5 || !slices.Equal(ranked[:2], []string{"src/util.ts", "src/core.ts"}) {
		t.Fatalf("file_rank order = %v, want src/util.ts and src/core.ts on top of five files", ranked)
	}
	fallback := []string{"src/a.ts", "src/b.ts", "src/c.ts", "src/core.ts", "src/util.ts"}

	t.Run("ranked", func(t *testing.T) {
		scan := runScan(t, s, repo)
		if scan["status"] != "done" {
			t.Fatalf("scan = %v, want done", scan)
		}
		if diff := cmp.Diff(ranked, sampledFiles(t, scan)); diff != "" {
			t.Errorf("sampled files mismatch (-want rank order +got):\n%s", diff)
		}
	})

	t.Run("ranked files outside the clone use the fallback", func(t *testing.T) {
		if err := s.db.DB.Exec(`UPDATE file_rank SET file_path = 'gone/' || file_path WHERE repo_id = ?`, repo).Error; err != nil {
			t.Fatal(err)
		}
		scan := runScan(t, s, repo)
		if diff := cmp.Diff(fallback, sampledFiles(t, scan)); diff != "" {
			t.Errorf("sampled files mismatch (-want fallback +got):\n%s", diff)
		}
	})

	t.Run("a ranker error uses the fallback", func(t *testing.T) {
		if err := s.db.DB.Exec(`ALTER TABLE file_rank RENAME TO file_rank_gone`).Error; err != nil {
			t.Fatal(err)
		}
		scan := runScan(t, s, repo)
		if diff := cmp.Diff(fallback, sampledFiles(t, scan)); diff != "" {
			t.Errorf("sampled files mismatch (-want fallback +got):\n%s", diff)
		}
		if lines := logLines(t, s.logs, "ranked samples unavailable, using fallback walk"); len(lines) != 1 {
			t.Errorf("ranker failure log lines = %v, want exactly one", lines)
		}
	})
}
