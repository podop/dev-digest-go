package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	ciapp "dev-digest-go/backend/internal/ci/app"
)

// Phase 6 component tests, lane X2: export to CI, the installation and run lists, and the refresh
// that ingests workflow results (AC6028-AC6035, AC6040-AC6044) and the contract goldens ci_*.
// GitHub is a double behind the existing stub's override seam (githubBaseURL), the runner is a fake
// bundle (spec field runner) and the storage leg of an artifact download is a TLS stub
// (spec field artifactClient). Everything here is prefixed ci to stay clear of the other lanes.

const (
	ciToken      = "ghp_CIMARKER0123456789abcdefghijklmnopqrstuvwx"
	ciGHMarker   = "GH-MESSAGE-MARKER-4f7a"
	ciRepoFull   = "acme/payments-api"
	ciRepoAPI    = "/repos/acme/payments-api"
	ciMainSHA    = "9f1c0de5a3b27d4e6f80a1b2c3d4e5f607182930"
	ciModel      = "deepseek/deepseek-v4-flash"
	ciRunnerBody = "fake-runner-executable"
)

// ---- fake runner ----

type ciRunnerBundle struct {
	art ciapp.RunnerArtifact
	ok  bool
}

func (r ciRunnerBundle) Runner() (ciapp.RunnerArtifact, bool) { return r.art, r.ok }

func ciRunnerSHA() string {
	sum := sha256.Sum256([]byte(ciRunnerBody))
	return hex.EncodeToString(sum[:])
}

// ciFakeRunner is a bundle with a fixed, recognisable encoding: the export ships its text as is.
func ciFakeRunner() ciapp.RunnerBundle {
	return ciRunnerBundle{ok: true, art: ciapp.RunnerArtifact{
		Encoded: base64.StdEncoding.EncodeToString([]byte(ciRunnerBody)) + "\n",
		SHA256:  ciRunnerSHA(),
		BuildID: "abc123def456",
	}}
}

// ---- stack ----

// ciSpec is a spec with the GitHub token and the fake runner.
func ciSpec() spec {
	return spec{env: map[string]string{"GITHUB_TOKEN": ciToken}, runner: ciFakeRunner()}
}

// ciStack starts a server whose GitHub is the double d.
func ciStack(t *testing.T, sp spec) (*stack, *ciGH) {
	t.Helper()
	d := newCiGH()
	s := startStack(t, sp)
	s.gh.setOverride(d.handle)
	return s, d
}

func ciGoldens(t *testing.T, s *stack) *goldens {
	return &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
}

// ---- GitHub double ----

type ciPR struct {
	Number int
	URL    string
}

// ciWFRun is one workflow run as GitHub lists it.
type ciWFRun struct {
	ID         int64
	Attempt    int
	HeadSHA    string
	Path       string
	Conclusion string // "" = null
	URL        string
	RepoID     int64
	HeadRepoID int64 // 0 = GitHub names no head repository
	PRs        []int
	CreatedAt  string
}

type ciArt struct {
	ID        int64
	Name      string
	Size      int64
	Expired   bool
	CreatedAt string
}

type ciRule struct {
	method, contains string
	respond          http.HandlerFunc
}

// ciGH plays the GitHub endpoints of Git Data, pull requests and Actions that the export and the
// refresh call. State lives in maps, scripted rules (first match wins) override single endpoints.
type ciGH struct {
	mu        sync.Mutex
	seq       int
	branches  map[string]map[string]string // repo -> branch -> commit sha
	prs       map[string][]ciPR            // repo -> open pull requests of devdigest/ci
	workflows map[string][]ciWFRun         // repo/file -> runs; absent = 404
	artifacts map[int64][]ciArt            // run id -> artifacts
	zips      map[int64]http.HandlerFunc   // artifact id -> download answer
	rules     []ciRule
	hook      func(*http.Request)
}

func newCiGH() *ciGH {
	return &ciGH{
		branches:  map[string]map[string]string{ciRepoFull: {"main": ciMainSHA}},
		prs:       map[string][]ciPR{},
		workflows: map[string][]ciWFRun{},
		artifacts: map[int64][]ciArt{},
		zips:      map[int64]http.HandlerFunc{},
	}
}

var (
	ciRefRE      = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/ref/heads/(.+)$`)
	ciCommitRE   = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/commits/([^/]+)$`)
	ciCreateRE   = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/(blobs|trees|commits|refs)$`)
	ciPatchRefRE = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/refs/heads/(.+)$`)
	ciPullsRE    = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/pulls$`)
	ciRunsRE     = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/workflows/([^/]+)/runs$`)
	ciArtsRE     = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/runs/(\d+)/artifacts$`)
	ciZipRE      = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/artifacts/(\d+)/zip$`)
)

func ciJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// on scripts one endpoint: requests with that method and a path containing contains are answered by fn.
func (d *ciGH) on(method, contains string, fn http.HandlerFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rules = append(d.rules, ciRule{method: method, contains: contains, respond: fn})
}

// fail scripts an error answer whose message carries the markers no output may repeat.
func (d *ciGH) fail(method, contains string, status int, headers ...string) {
	d.on(method, contains, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		ciJSON(w, status, map[string]string{"message": ciGHMarker + " " + ciToken})
	})
}

func (d *ciGH) clearRules() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rules = nil
}

func (d *ciGH) setHook(fn func(*http.Request)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hook = fn
}

// addWorkflow registers the runs of workflow devdigest-review-<slug>.yml of repo.
func (d *ciGH) addWorkflow(repo, slug string, runs ...ciWFRun) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.workflows[repo+"/devdigest-review-"+slug+".yml"] = runs
}

// addArtifact registers an artifact of a run and the answer of its download.
func (d *ciGH) addArtifact(runID int64, a ciArt, download http.HandlerFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.artifacts[runID] = append(d.artifacts[runID], a)
	if download != nil {
		d.zips[a.ID] = download
	}
}

func (d *ciGH) openPRs(repo string) []ciPR {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.prs[repo])
}

func (d *ciGH) branch(repo, name string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sha, ok := d.branches[repo][name]
	return sha, ok
}

// handle is the override of the shared stub: every /repos/ request is answered here.
func (d *ciGH) handle(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/repos/") {
		return false
	}
	d.mu.Lock()
	hook, rules := d.hook, slices.Clone(d.rules)
	d.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	for _, rule := range rules {
		if rule.method == r.Method && strings.Contains(r.URL.Path, rule.contains) {
			rule.respond(w, r)
			return true
		}
	}
	d.route(w, r)
	return true
}

func (d *ciGH) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	notFound := func() { ciJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"}) }
	var body struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && ciRefRE.MatchString(p):
		m := ciRefRE.FindStringSubmatch(p)
		sha, ok := d.branches[m[1]][m[2]]
		if !ok {
			notFound()
			return
		}
		ciJSON(w, http.StatusOK, map[string]any{"object": map[string]string{"sha": sha}})
	case r.Method == http.MethodGet && ciCommitRE.MatchString(p):
		m := ciCommitRE.FindStringSubmatch(p)
		ciJSON(w, http.StatusOK, map[string]any{"sha": m[2], "tree": map[string]string{"sha": "tree-of-" + m[2]}})
	case r.Method == http.MethodPatch && ciPatchRefRE.MatchString(p):
		m := ciPatchRefRE.FindStringSubmatch(p)
		d.branches[m[1]][m[2]] = body.SHA
		ciJSON(w, http.StatusOK, map[string]string{"ref": "refs/heads/" + m[2]})
	case r.Method == http.MethodPost && ciCreateRE.MatchString(p):
		m := ciCreateRE.FindStringSubmatch(p)
		if m[2] == "refs" {
			if d.branches[m[1]] == nil {
				d.branches[m[1]] = map[string]string{}
			}
			d.branches[m[1]][strings.TrimPrefix(body.Ref, "refs/heads/")] = body.SHA
			ciJSON(w, http.StatusCreated, map[string]string{"ref": body.Ref})
			return
		}
		d.seq++
		ciJSON(w, http.StatusCreated, map[string]string{"sha": fmt.Sprintf("%s-%03d", strings.TrimSuffix(m[2], "s"), d.seq)})
	case ciPullsRE.MatchString(p) && r.Method == http.MethodGet:
		repo := ciPullsRE.FindStringSubmatch(p)[1]
		out := []map[string]any{}
		for _, pr := range d.prs[repo] {
			out = append(out, map[string]any{"number": pr.Number, "html_url": pr.URL})
		}
		ciJSON(w, http.StatusOK, out)
	case ciPullsRE.MatchString(p) && r.Method == http.MethodPost:
		repo := ciPullsRE.FindStringSubmatch(p)[1]
		pr := ciPR{Number: 40 + len(d.prs[repo]) + 1}
		pr.URL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, pr.Number)
		d.prs[repo] = append(d.prs[repo], pr)
		ciJSON(w, http.StatusCreated, map[string]any{"number": pr.Number, "html_url": pr.URL})
	case r.Method == http.MethodGet && ciRunsRE.MatchString(p):
		m := ciRunsRE.FindStringSubmatch(p)
		runs, ok := d.workflows[m[1]+"/"+m[2]]
		if !ok {
			notFound()
			return
		}
		list := []map[string]any{}
		for _, run := range runs {
			var conclusion any
			if run.Conclusion != "" {
				conclusion = run.Conclusion
			}
			prs := []map[string]int{}
			for _, n := range run.PRs {
				prs = append(prs, map[string]int{"number": n})
			}
			item := map[string]any{
				"id": run.ID, "run_attempt": run.Attempt, "head_sha": run.HeadSHA, "path": run.Path, "event": "pull_request",
				"conclusion": conclusion, "html_url": run.URL, "created_at": run.CreatedAt,
				"repository":      map[string]int64{"id": run.RepoID},
				"head_repository": nil,
				"pull_requests":   prs,
			}
			if run.HeadRepoID != 0 {
				item["head_repository"] = map[string]int64{"id": run.HeadRepoID}
			}
			list = append(list, item)
		}
		ciJSON(w, http.StatusOK, map[string]any{"total_count": len(list), "workflow_runs": list})
	case r.Method == http.MethodGet && ciArtsRE.MatchString(p):
		var id int64
		_, _ = fmt.Sscan(ciArtsRE.FindStringSubmatch(p)[2], &id)
		list := []map[string]any{}
		for _, a := range d.artifacts[id] {
			list = append(list, map[string]any{
				"id": a.ID, "name": a.Name, "size_in_bytes": a.Size, "expired": a.Expired, "created_at": a.CreatedAt,
			})
		}
		ciJSON(w, http.StatusOK, map[string]any{"total_count": len(list), "artifacts": list})
	case r.Method == http.MethodGet && ciZipRE.MatchString(p):
		var id int64
		_, _ = fmt.Sscan(ciZipRE.FindStringSubmatch(p)[2], &id)
		fn, ok := d.zips[id]
		if !ok {
			notFound()
			return
		}
		d.mu.Unlock()
		fn(w, r)
		d.mu.Lock()
	default:
		notFound()
	}
}

// ---- requests ----

func ciSeq(reqs []ghRequest) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Method + " " + r.Path
	}
	return out
}

// ciBodyOf decodes the JSON body of a recorded request.
func ciBodyOf(t testing.TB, r ghRequest) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("%s %s: body %q is not a JSON object: %v", r.Method, r.Path, r.Body, err)
	}
	return m
}

// ciWith returns the recorded requests with that method and a path ending in suffix.
func ciWith(reqs []ghRequest, method, suffix string) []ghRequest {
	var out []ghRequest
	for _, r := range reqs {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

// ---- API fixtures ----

func ciPtr[T any](v T) *T { return &v }

const ciPrompt = "Review the diff.\n  Indented line: with a colon # and \"quotes\"\n\ttabbed line  \n"

func ciCreateAgent(t *testing.T, s *stack, name, provider string) string {
	t.Helper()
	model := ciModel
	if provider == "openai" {
		model = "gpt-5.4"
	}
	resp := s.post("/agents", map[string]any{"name": name, "provider": provider, "model": model, "system_prompt": ciPrompt})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /agents %q = %d %s, want 201", name, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

func ciCreateSkill(t *testing.T, s *stack, name, body string, enabled bool) string {
	t.Helper()
	resp := s.post("/skills", map[string]any{"name": name, "type": "custom", "body": body, "enabled": enabled})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /skills %q = %d %s, want 201", name, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

func ciLinkSkills(t *testing.T, s *stack, agent string, skills ...string) {
	t.Helper()
	if resp := s.post("/agents/"+agent+"/skills", map[string]any{"skill_ids": skills}); resp.Status != http.StatusOK {
		t.Fatalf("POST /agents/%s/skills = %d %s, want 200", agent, resp.Status, resp.Body)
	}
}

// ciAgentField reads one field of GET /agents/:id.
func ciAgentField(t *testing.T, s *stack, agent, field string) any {
	t.Helper()
	return s.get("/agents/" + agent).Obj(t)[field]
}

func ciExportBody(repo, action string, extra map[string]any) map[string]any {
	body := map[string]any{"repo": repo, "action": action}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

type ciError struct {
	Code, Message string
	Details       any
}

func ciErr(t testing.TB, r response) ciError {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details any    `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &env); err != nil {
		t.Fatalf("body is not an error envelope (status %d): %q: %v", r.Status, r.Body, err)
	}
	return ciError{Code: env.Error.Code, Message: env.Error.Message, Details: env.Error.Details}
}

type ciFile struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
	Editable bool   `json:"editable"`
}

type ciInstallation struct {
	ID              string  `json:"id"`
	AgentID         string  `json:"agent_id"`
	Repo            string  `json:"repo"`
	TargetType      string  `json:"target_type"`
	InstalledAt     string  `json:"installed_at"`
	Slug            *string `json:"slug"`
	WorkflowPath    *string `json:"workflow_path"`
	ManifestVersion *int    `json:"manifest_version"`
	LastRun         any     `json:"last_run"`
}

type ciExportResult struct {
	Installation *ciInstallation `json:"installation"`
	Files        []ciFile        `json:"files"`
	PRURL        *string         `json:"pr_url"`
}

func ciExport(t *testing.T, s *stack, agent string, body any, want int) ciExportResult {
	t.Helper()
	resp := s.post("/agents/"+agent+"/export-ci", body)
	if resp.Status != want {
		t.Fatalf("POST /agents/%s/export-ci = %d %s, want %d", agent, resp.Status, resp.Body, want)
	}
	var out ciExportResult
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("export body: %v: %s", err, resp.Body)
	}
	return out
}

// ciKeyOrder returns the keys of a JSON object in document order.
func ciKeyOrder(t testing.TB, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("body is not an object: %q", raw)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// ---- expected texts (spec 7.6.3-7.6.7), written out independently of the production builders ----

func ciWantWorkflow(slug string, triggers []string, postAs string) string {
	return fmt.Sprintf(`name: DevDigest Review · %[1]s

on:
  pull_request:
    types: [%[2]s]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    name: DevDigest Review · %[1]s
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-24.04
    steps:
      - name: Check out the pull request
        uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262
        with:
          ref: ${{ github.event.pull_request.head.sha }}
          persist-credentials: false

      - name: Run the DevDigest review
        shell: bash
        run: |
          base64 -d .devdigest/runner/devdigest-runner-linux-amd64.gz.b64 | gzip -dc > "$RUNNER_TEMP/devdigest-runner"
          cd "$RUNNER_TEMP" && sha256sum --check --strict "$GITHUB_WORKSPACE/.devdigest/runner/devdigest-runner-linux-amd64.sha256"
          chmod +x "$RUNNER_TEMP/devdigest-runner"
          cd "$GITHUB_WORKSPACE" && "$RUNNER_TEMP/devdigest-runner"
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          DEVDIGEST_AGENT: %[1]s
          DEVDIGEST_POST_AS: %[3]s
          PR_HEAD_SHA: ${{ github.event.pull_request.head.sha }}

      - name: Upload the review result
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: devdigest-result-%[1]s
          path: devdigest-result.json
`, slug, strings.Join(triggers, ", "), postAs)
}

func ciWantPRBody(slug string, paths []string) string {
	var files []string
	for _, p := range paths {
		files = append(files, "- `"+p+"`")
	}
	return "Adds the DevDigest review agent `" + slug + "` to this repository's GitHub Actions.\n\n" +
		"### Files\n" + strings.Join(files, "\n") + "\n\n" +
		"### Before merging\n" +
		"1. Add the repository Actions secret `OPENROUTER_API_KEY` (Settings > Secrets and variables > Actions).\n" +
		"   This PR triggers the review workflow on itself, so its check stays red until the secret exists.\n" +
		"2. Merge this PR.\n" +
		"3. To block merges on CRITICAL findings, add the status check `DevDigest Review · " + slug + "` as required in the branch protection of the default branch.\n\n" +
		"### Runner\n" +
		"`.devdigest/runner/devdigest-runner-linux-amd64.gz.b64` is the DevDigest CI runner (linux/amd64), built from DevDigest source `abc123def456`. " +
		"SHA-256 of the decoded binary: `" + ciRunnerSHA() + "`. The workflow checks it before running.\n\n" +
		"Branch: `devdigest/ci`. Generated by DevDigest."
}

func ciWantPaths(slug string, skills ...string) []string {
	paths := []string{".devdigest/agents/" + slug + ".yaml"}
	for _, sk := range skills {
		paths = append(paths, ".devdigest/skills/"+sk+".md")
	}
	return append(paths,
		".devdigest/memory.jsonl",
		".devdigest/runner/devdigest-runner-linux-amd64.gz.b64",
		".devdigest/runner/devdigest-runner-linux-amd64.sha256",
		".github/workflows/devdigest-review-"+slug+".yml")
}

// ciCheckFiles compares the files of an export with spec 7.6.3.
func ciCheckFiles(t *testing.T, got []ciFile, slug string, skillBodies map[string]string, skillOrder []string, workflow string) {
	t.Helper()
	wantPaths := ciWantPaths(slug, skillOrder...)
	var gotPaths []string
	for _, f := range got {
		gotPaths = append(gotPaths, f.Path)
	}
	if diff := cmp.Diff(wantPaths, gotPaths); diff != "" {
		t.Fatalf("export file paths mismatch (-want +got):\n%s", diff)
	}
	for _, f := range got {
		var want string
		switch {
		case strings.HasPrefix(f.Path, ".devdigest/skills/"):
			want = skillBodies[strings.TrimSuffix(strings.TrimPrefix(f.Path, ".devdigest/skills/"), ".md")]
		case f.Path == ".devdigest/memory.jsonl":
			want = ""
		case strings.HasSuffix(f.Path, ".gz.b64"):
			want = base64.StdEncoding.EncodeToString([]byte(ciRunnerBody)) + "\n"
		case strings.HasSuffix(f.Path, ".sha256"):
			want = ciRunnerSHA() + "  devdigest-runner\n"
		case strings.HasSuffix(f.Path, ".yml"):
			want = workflow
		default:
			continue // the manifest is checked separately
		}
		if f.Contents != want {
			t.Errorf("file %s contents = %q, want %q", f.Path, f.Contents, want)
		}
		if f.Editable != strings.HasSuffix(f.Path, ".yml") {
			t.Errorf("file %s editable = %v, want only the workflow editable", f.Path, f.Editable)
		}
	}
	if got[0].Editable {
		t.Errorf("manifest %s is editable, want false", got[0].Path)
	}
}

// ciCheckManifest parses the manifest and compares it with the agent as the API shows it.
func ciCheckManifest(t *testing.T, s *stack, agent, text string, skills []string) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(text), &node); err != nil {
		t.Fatalf("manifest is not YAML: %v\n%s", err, text)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("manifest is not a mapping:\n%s", text)
	}
	var keys []string
	for i := 0; i < len(node.Content[0].Content); i += 2 {
		keys = append(keys, node.Content[0].Content[i].Value)
	}
	wantKeys := []string{"name", "provider", "model", "system_prompt", "skills", "strategy", "ci_fail_on", "version"}
	if diff := cmp.Diff(wantKeys, keys); diff != "" {
		t.Errorf("manifest key order mismatch (-want +got):\n%s", diff)
	}
	var m map[string]any
	if err := node.Decode(&m); err != nil {
		t.Fatal(err)
	}
	api := s.get("/agents/" + agent).Obj(t)
	wantSkills := []any{}
	for _, sk := range skills {
		wantSkills = append(wantSkills, sk)
	}
	want := map[string]any{
		"name": api["name"], "provider": "openrouter", "model": ciModel, "system_prompt": ciPrompt, "skills": wantSkills,
		"strategy": api["strategy"], "ci_fail_on": api["ci_fail_on"], "version": int(api["version"].(float64)),
	}
	if diff := cmp.Diff(want, m); diff != "" {
		t.Errorf("manifest values mismatch (-want +got):\n%s", diff)
	}
}

// ---- AC6028: export files ----

func TestCiExport_FilesActionReturnsTheSevenFiles(t *testing.T) { // AC6028, EC6030 (files without a token), EC6024
	s, _ := ciStack(t, ciSpec())
	g := ciGoldens(t, s)
	agent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	errorsSkill := ciCreateSkill(t, s, "error-handling", "Check every error.\nWrap with %w.\n", true)
	apiSkill := ciCreateSkill(t, s, "api-design", "Keep the wire stable.", true)
	off := ciCreateSkill(t, s, "disabled-skill", "Never shipped.", false)
	ciLinkSkills(t, s, agent, errorsSkill, off, apiSkill)

	resp := s.post("/agents/"+agent+"/export-ci", ciExportBody(ciRepoFull, "files",
		map[string]any{"post_as": "pr_comment", "triggers": []string{"reopened", "opened"}}))
	if resp.Status != http.StatusOK {
		t.Fatalf("export files = %d %s, want 200", resp.Status, resp.Body)
	}
	if diff := cmp.Diff([]string{"installation", "files", "pr_url", "required_secrets", "required_variables", "gitlab_ci"}, ciKeyOrder(t, resp.Body)); diff != "" {
		t.Errorf("CiExport key order mismatch (-want +got):\n%s", diff)
	}
	var out ciExportResult
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Installation != nil || out.PRURL != nil {
		t.Errorf("files export installation = %+v, pr_url = %v, want both null", out.Installation, out.PRURL)
	}
	workflow := ciWantWorkflow("ci-reviewer", []string{"opened", "reopened"}, "pr_comment")
	ciCheckFiles(t, out.Files, "ci-reviewer",
		map[string]string{"error-handling": "Check every error.\nWrap with %w.\n", "api-design": "Keep the wire stable."},
		[]string{"error-handling", "api-design"}, workflow)
	ciCheckManifest(t, s, agent, out.Files[0].Contents, []string{"error-handling", "api-design"})
	g.check("ci_export_files", resp, http.StatusOK)

	if n := len(s.gh.requests()); n != 0 {
		t.Errorf("GitHub requests after a files export = %v, want none", ciSeq(s.gh.requests()))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
		t.Errorf("ci_installations rows = %d, want 0 (the files action writes nothing)", n)
	}

	t.Run("no token still answers files", func(t *testing.T) {
		sp := ciSpec()
		sp.env = nil
		s, _ := ciStack(t, sp)
		agent := ciCreateAgent(t, s, "No Token Reviewer", "openrouter")
		out := ciExport(t, s, agent, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		if len(out.Files) != 5 {
			t.Errorf("files without skills = %d, want 5", len(out.Files))
		}
		if n := len(s.gh.requests()); n != 0 {
			t.Errorf("GitHub requests = %v, want none", ciSeq(s.gh.requests()))
		}
	})
}

// ---- AC6029: refusals ----

func TestCiExport_RefusalsAnswerBeforeAnyGitHubCall(t *testing.T) { // AC6029, EC6023-EC6026
	s, _ := ciStack(t, ciSpec())
	orAgent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	openAI := ciCreateAgent(t, s, "OpenAI Reviewer", "openai")
	other := ciCreateAgent(t, s, "Other Reviewer", "openrouter")
	// The API refuses an undefined custom provider on an agent, so the row is set directly: a
	// provider deleted behind the agent's back, or a bundle installed before the provider existed.
	ghost := ciCreateAgent(t, s, "Ghost Reviewer", "openrouter")
	if err := s.db.DB.Exec(`UPDATE agents SET provider = 'custom:ghost' WHERE id = ?`, ghost).Error; err != nil {
		t.Fatal(err)
	}
	skillAgent := ciCreateAgent(t, s, "Skill Clash Reviewer", "openrouter")
	a, b := ciCreateSkill(t, s, "first-skill", "one", true), ciCreateSkill(t, s, "second-skill", "two", true)
	ciLinkSkills(t, s, skillAgent, a, b)
	// Names that the slug rule maps to one file name (the API only creates kebab-case names).
	if err := s.db.DB.Exec(`UPDATE skills SET name = 'Error handling' WHERE id = ?`, a).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.DB.Exec(`UPDATE skills SET name = 'error-handling' WHERE id = ?`, b).Error; err != nil {
		t.Fatal(err)
	}

	insecure := "name: x\non:\n  pull_request_target:\n    types: [opened]\njobs:\n  review:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: echo hi\n"
	clash := ciInstall(t, s, other, ciRepoFull, "ci-reviewer", 1, time.Now().Add(-time.Hour))

	cases := []struct {
		name       string
		agent      string
		body       map[string]any
		status     int
		code, msg  string
		wantDetail any
		before     func()
	}{
		{"agent on an undefined custom provider", ghost, ciExportBody(ciRepoFull, "files", nil), 422, "provider_not_configured",
			"Custom provider custom:ghost is not configured", nil, nil},
		{"openai agent and a circle target", openAI, ciExportBody("nowhere/none", "files", map[string]any{"target": "circle"}), 422, "unsupported_target",
			"Only GitHub Actions can be exported", nil, nil},
		{"empty triggers", orAgent, ciExportBody(ciRepoFull, "files", map[string]any{"triggers": []string{}}), 422, "invalid_triggers",
			"Select at least one valid pull_request trigger", nil, nil},
		{"unknown trigger", orAgent, ciExportBody(ciRepoFull, "files", map[string]any{"triggers": []string{"closed"}}), 422, "invalid_triggers",
			"Select at least one valid pull_request trigger", nil, nil},
		{"repository not imported", orAgent, ciExportBody("acme/not-imported", "files", nil), 404, "repo_not_found",
			"Repository is not imported in this workspace", nil, nil},
		{"slug owned by another agent", orAgent, ciExportBody(ciRepoFull, "files", nil), 409, "ci_slug_conflict",
			`Another agent already uses the CI name "ci-reviewer" in this repository`, nil, nil},
		{"colliding skill file names", skillAgent, ciExportBody(ciRepoFull, "files", nil), 422, "skill_slug_conflict",
			"Two attached skills have the same file name", nil, func() {
				if err := s.db.DB.Exec(`DELETE FROM ci_installations WHERE id = ?`, clash).Error; err != nil {
					t.Fatal(err)
				}
			}},
		{"workflow with pull_request_target", orAgent, ciExportBody(ciRepoFull, "files", map[string]any{"workflow": insecure}), 422, "workflow_insecure",
			"The workflow breaks a security rule", map[string]any{"rule": "forbidden_trigger"}, nil},
		{"empty edited workflow is text", orAgent, ciExportBody(ciRepoFull, "files", map[string]any{"workflow": ""}), 422, "workflow_insecure",
			"The workflow breaks a security rule", map[string]any{"rule": "invalid_yaml"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.before != nil {
				tc.before()
			}
			resp := s.post("/agents/"+tc.agent+"/export-ci", tc.body)
			if resp.Status != tc.status {
				t.Fatalf("export = %d %s, want %d", resp.Status, resp.Body, tc.status)
			}
			e := ciErr(t, resp)
			if e.Code != tc.code || e.Message != tc.msg {
				t.Errorf("error = %s %q, want %s %q", e.Code, e.Message, tc.code, tc.msg)
			}
			if diff := cmp.Diff(tc.wantDetail, e.Details); diff != "" {
				t.Errorf("error details mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if got := s.gh.requests(); len(got) != 0 {
		t.Errorf("GitHub requests during the refusals = %v, want none", ciSeq(got))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
		t.Errorf("ci_installations rows = %d, want 0", n)
	}

	t.Run("request errors", func(t *testing.T) {
		if resp := s.post("/agents/00000000-0000-4000-8000-000000000999/export-ci", ciExportBody(ciRepoFull, "files", nil)); resp.Status != 404 || ciErr(t, resp).Code != "agent_not_found" {
			t.Errorf("unknown agent = %d %s, want 404 agent_not_found", resp.Status, resp.Body)
		}
		if resp := s.post("/agents/"+orAgent+"/export-ci", map[string]any{"action": "files"}); resp.Status != 422 || ciErr(t, resp).Code != "validation_error" {
			t.Errorf("missing repo = %d %s, want 422 validation_error", resp.Status, resp.Body)
		}
		if resp := s.post("/agents/"+orAgent+"/export-ci", ciExportBody("", "files", nil)); resp.Status != 422 {
			t.Errorf("empty repo = %d %s, want 422", resp.Status, resp.Body)
		}
		resp := s.do(http.MethodPost, "/agents/"+orAgent+"/export-ci", ciExportBody(ciRepoFull, "files", nil), "Origin", "http://evil.example")
		if resp.Status != http.StatusForbidden {
			t.Errorf("foreign Origin = %d %s, want 403", resp.Status, resp.Body)
		}
		if got := s.gh.requests(); len(got) != 0 {
			t.Errorf("GitHub requests = %v, want none", ciSeq(got))
		}
	})
}

// A server without the runner answers after every validation error would have been reported (EC6033).
func TestCiExport_WithoutTheRunnerBundle(t *testing.T) { // AC6029 (last case), EC6033
	sp := ciSpec()
	sp.runner = nil
	s, _ := ciStack(t, sp)
	agent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	ghost := ciCreateAgent(t, s, "Ghost Reviewer", "openrouter")
	if err := s.db.DB.Exec(`UPDATE agents SET provider = 'custom:ghost' WHERE id = ?`, ghost).Error; err != nil {
		t.Fatal(err)
	}

	const msg = "The runner binary is missing: rebuild the backend image (docker compose build backend)"
	for _, action := range []string{"files", "open_pr"} {
		resp := s.post("/agents/"+agent+"/export-ci", ciExportBody(ciRepoFull, action, nil))
		if e := ciErr(t, resp); resp.Status != 500 || e.Code != "runner_bundle_missing" || e.Message != msg {
			t.Errorf("%s without a runner = %d %s %q, want 500 runner_bundle_missing %q", action, resp.Status, e.Code, e.Message, msg)
		}
	}
	if resp := s.post("/agents/"+ghost+"/export-ci", ciExportBody(ciRepoFull, "files", nil)); resp.Status != 422 || ciErr(t, resp).Code != "provider_not_configured" {
		t.Errorf("undefined custom provider without a runner = %d %s, want the validation error first", resp.Status, resp.Body)
	}
	if got := s.gh.requests(); len(got) != 0 {
		t.Errorf("GitHub requests = %v, want none", ciSeq(got))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
		t.Errorf("ci_installations rows = %d, want 0", n)
	}
}

// ---- AC6030: open_pr ----

func TestCiExport_OpenPRPublishesOneCommitAndOpensThePR(t *testing.T) { // AC6030
	s, gh := ciStack(t, ciSpec())
	g := ciGoldens(t, s)
	agent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	skill := ciCreateSkill(t, s, "error-handling", "Check every error.", true)
	ciLinkSkills(t, s, agent, skill)

	resp := s.post("/agents/"+agent+"/export-ci", ciExportBody(ciRepoFull, "open_pr", nil))
	if resp.Status != http.StatusCreated {
		t.Fatalf("export open_pr = %d %s, want 201", resp.Status, resp.Body)
	}
	var out ciExportResult
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	g.check("ci_export_open_pr", resp, http.StatusCreated)

	version := int(ciAgentField(t, s, agent, "version").(float64))
	in := out.Installation
	if in == nil || in.AgentID != agent || in.Repo != ciRepoFull || in.TargetType != "gha" || in.LastRun != nil ||
		in.Slug == nil || *in.Slug != "ci-reviewer" || in.ManifestVersion == nil || *in.ManifestVersion != version ||
		in.WorkflowPath == nil || *in.WorkflowPath != ".github/workflows/devdigest-review-ci-reviewer.yml" {
		t.Errorf("installation = %+v, want agent %s, slug ci-reviewer, manifest_version %d, no last run", in, agent, version)
	}
	if out.PRURL == nil || *out.PRURL != "https://github.com/acme/payments-api/pull/41" {
		t.Errorf("pr_url = %v, want the PR the double created", out.PRURL)
	}
	// Default triggers are all three, in order; post_as defaults to github_review.
	workflow := ciWantWorkflow("ci-reviewer", []string{"opened", "synchronize", "reopened"}, "github_review")
	ciCheckFiles(t, out.Files, "ci-reviewer", map[string]string{"error-handling": "Check every error."}, []string{"error-handling"}, workflow)

	reqs := s.gh.requests()
	wantSeq := []string{
		"GET " + ciRepoAPI + "/git/ref/heads/devdigest/ci",
		"GET " + ciRepoAPI + "/git/ref/heads/main",
		"GET " + ciRepoAPI + "/git/commits/" + ciMainSHA,
		"POST " + ciRepoAPI + "/git/blobs",
		"POST " + ciRepoAPI + "/git/trees",
		"POST " + ciRepoAPI + "/git/commits",
		"POST " + ciRepoAPI + "/git/refs",
		"GET " + ciRepoAPI + "/pulls",
		"POST " + ciRepoAPI + "/pulls",
	}
	if diff := cmp.Diff(wantSeq, ciSeq(reqs)); diff != "" {
		t.Fatalf("GitHub request sequence mismatch (-want +got):\n%s", diff)
	}
	for _, r := range reqs {
		if r.Auth != "Bearer "+ciToken {
			t.Errorf("%s %s Authorization = %q, want the bearer token", r.Method, r.Path, r.Auth)
		}
	}

	blob := ciBodyOf(t, reqs[3])
	wantBlob := base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString([]byte(ciRunnerBody)) + "\n"))
	if diff := cmp.Diff(map[string]any{"content": wantBlob, "encoding": "base64"}, blob); diff != "" {
		t.Errorf("blob body mismatch (-want +got):\n%s", diff)
	}
	tree := ciBodyOf(t, reqs[4])
	if tree["base_tree"] != "tree-of-"+ciMainSHA {
		t.Errorf("tree base_tree = %v, want the default branch's tree", tree["base_tree"])
	}
	entries, _ := tree["tree"].([]any)
	if len(entries) != len(out.Files) {
		t.Fatalf("tree has %d entries, want %d", len(entries), len(out.Files))
	}
	for i, raw := range entries {
		e := raw.(map[string]any)
		f := out.Files[i]
		if e["path"] != f.Path || e["mode"] != "100644" || e["type"] != "blob" {
			t.Errorf("tree entry %d = %v, want %s mode 100644 blob", i, e, f.Path)
		}
		if f.Path == ".devdigest/runner/devdigest-runner-linux-amd64.gz.b64" {
			if e["sha"] != "blob-001" || e["content"] != nil {
				t.Errorf("runner tree entry = %v, want the blob sha and no inline content", e)
			}
		} else if e["content"] != f.Contents || e["sha"] != nil {
			t.Errorf("tree entry %s = %v, want inline content equal to the file", f.Path, e)
		}
	}
	commit := ciBodyOf(t, reqs[5])
	if diff := cmp.Diff(map[string]any{"message": "Add DevDigest CI review", "tree": "tree-002", "parents": []any{ciMainSHA}}, commit); diff != "" {
		t.Errorf("commit body mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]any{"ref": "refs/heads/devdigest/ci", "sha": "commit-003"}, ciBodyOf(t, reqs[6])); diff != "" {
		t.Errorf("create ref body mismatch (-want +got):\n%s", diff)
	}
	q, _ := url.ParseQuery(reqs[7].Query)
	if q.Get("state") != "open" || q.Get("head") != "acme:devdigest/ci" || q.Get("per_page") != "1" {
		t.Errorf("find-PR query = %q, want state=open head=acme:devdigest/ci per_page=1", reqs[7].Query)
	}
	wantPR := map[string]any{
		"title": "Add DevDigest CI review", "head": "devdigest/ci", "base": "main",
		"body": ciWantPRBody("ci-reviewer", ciWantPaths("ci-reviewer", "error-handling")),
	}
	if diff := cmp.Diff(wantPR, ciBodyOf(t, reqs[8])); diff != "" {
		t.Errorf("create PR body mismatch (-want +got):\n%s", diff)
	}

	if sha, ok := gh.branch(ciRepoFull, "devdigest/ci"); !ok || sha != "commit-003" {
		t.Errorf("devdigest/ci after the export = %q (exists %v), want the new commit", sha, ok)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations WHERE agent_id = ? AND slug = 'ci-reviewer' AND manifest_version = ?`, agent, version); n != 1 {
		t.Errorf("installation rows for the agent = %d, want 1", n)
	}
}

// ---- AC6031: second agent, rename ----

func TestCiExport_SecondExportReusesTheBranchAndThePR(t *testing.T) { // AC6031, EC6025, EC6032
	s, gh := ciStack(t, ciSpec())
	first := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	second := ciCreateAgent(t, s, "Second Reviewer", "openrouter")

	one := ciExport(t, s, first, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)
	before := len(s.gh.requests())
	two := ciExport(t, s, second, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)

	reqs := s.gh.requests()[before:]
	wantSeq := []string{
		"GET " + ciRepoAPI + "/git/ref/heads/devdigest/ci",
		"GET " + ciRepoAPI + "/git/commits/commit-003",
		"POST " + ciRepoAPI + "/git/blobs",
		"POST " + ciRepoAPI + "/git/trees",
		"POST " + ciRepoAPI + "/git/commits",
		"PATCH " + ciRepoAPI + "/git/refs/heads/devdigest/ci",
		"GET " + ciRepoAPI + "/pulls",
	}
	if diff := cmp.Diff(wantSeq, ciSeq(reqs)); diff != "" {
		t.Fatalf("second export request sequence mismatch (-want +got):\n%s", diff)
	}
	if got := ciBodyOf(t, reqs[2]); got["encoding"] != "base64" {
		t.Errorf("blob body = %v, want base64", got)
	}
	if diff := cmp.Diff(map[string]any{"message": "Add DevDigest CI review", "tree": "tree-005", "parents": []any{"commit-003"}}, ciBodyOf(t, reqs[4])); diff != "" {
		t.Errorf("second commit body mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]any{"sha": "commit-006", "force": true}, ciBodyOf(t, reqs[5])); diff != "" {
		t.Errorf("ref update body mismatch (-want +got):\n%s", diff)
	}
	if one.PRURL == nil || two.PRURL == nil || *one.PRURL != *two.PRURL {
		t.Errorf("pr_url of the second export = %v, want the first one's %v", two.PRURL, one.PRURL)
	}
	if prs := gh.openPRs(ciRepoFull); len(prs) != 1 {
		t.Errorf("open PRs after two exports = %v, want one", prs)
	}
	if two.Installation == nil || two.Installation.Slug == nil || *two.Installation.Slug != "second-reviewer" {
		t.Errorf("second installation = %+v, want slug second-reviewer", two.Installation)
	}

	// Rename the first agent (a new version): its slug stays, the manifest version moves.
	if resp := s.do(http.MethodPut, "/agents/"+first, map[string]any{"name": "Renamed Reviewer", "system_prompt": "A changed prompt."}); resp.Status != http.StatusOK {
		t.Fatalf("PUT /agents/%s = %d %s, want 200", first, resp.Status, resp.Body)
	}
	version := int(ciAgentField(t, s, first, "version").(float64))
	if version < 2 {
		t.Fatalf("agent version after the edit = %d, want at least 2", version)
	}
	again := ciExport(t, s, first, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)
	if again.Installation == nil || again.Installation.ID != one.Installation.ID {
		t.Errorf("re-export installation = %+v, want the first export's row %s", again.Installation, one.Installation.ID)
	}
	if again.Installation.Slug == nil || *again.Installation.Slug != "ci-reviewer" || again.Installation.ManifestVersion == nil || *again.Installation.ManifestVersion != version {
		t.Errorf("re-export installation = %+v, want slug ci-reviewer and manifest_version %d", again.Installation, version)
	}
	if again.Files[0].Path != ".devdigest/agents/ci-reviewer.yaml" {
		t.Errorf("re-export manifest path = %q, want the kept slug", again.Files[0].Path)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 2 {
		t.Errorf("ci_installations rows = %d, want 2 (the re-export updates in place)", n)
	}
}

// ---- AC6032: GitHub failures ----

func TestCiExport_GitHubFailuresMapToStatusesAndLeaveNoInstallation(t *testing.T) { // AC6032, EC6030, EC6031
	s, gh := ciStack(t, ciSpec())
	agent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	const scope = "GitHub refused the write. The studio token needs the classic scopes `repo` + `workflow`, or the fine-grained permissions Contents:write, Workflows:write and Pull requests:write."
	open := ciExportBody(ciRepoFull, "open_pr", nil)

	steps := []struct {
		name, method, contains string
		githubStatus           int
		status                 int
		code, msg              string
	}{
		{"tree 404", "POST", "/git/trees", 404, 422, "github_permission_denied", scope},
		{"pull request 403", "POST", "/pulls", 403, 422, "github_permission_denied", scope},
		{"commit 500", "POST", "/git/commits", 500, 502, "github_unavailable", "GitHub is unavailable, try again later"},
	}
	commitsBefore := 0
	for _, st := range steps {
		if st.githubStatus == 500 {
			commitsBefore = len(ciWith(s.gh.requests(), "POST", "/git/commits"))
		}
		gh.clearRules()
		gh.fail(st.method, st.contains, st.githubStatus)
		resp := s.post("/agents/"+agent+"/export-ci", open)
		if e := ciErr(t, resp); resp.Status != st.status || e.Code != st.code || e.Message != st.msg {
			t.Errorf("%s: export = %d %s %q, want %d %s %q", st.name, resp.Status, e.Code, e.Message, st.status, st.code, st.msg)
		}
		if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
			t.Errorf("%s: ci_installations rows = %d, want 0", st.name, n)
		}
	}
	// A 5xx on the commit re-runs the whole publish once (spec 7.9); POSTs themselves are not retried.
	if got := len(ciWith(s.gh.requests(), "POST", "/git/commits")) - commitsBefore; got != 2 {
		t.Errorf("POST git/commits attempts after one 500 = %d, want 2 (one re-run of the sequence)", got)
	}

	lines := logLines(t, s.logs, "ci export: GitHub write failed")
	wantStatus := []float64{404, 403, 500}
	if len(lines) != len(wantStatus) {
		t.Fatalf("publish failure log lines = %d, want %d: %v", len(lines), len(wantStatus), lines)
	}
	for i, line := range lines {
		keys := []string{}
		for k := range line {
			if k != "time" && k != "level" && k != "msg" {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		if diff := cmp.Diff([]string{"repo", "status"}, keys); diff != "" {
			t.Errorf("log line %d fields mismatch (-want +got):\n%s", i, diff)
		}
		if line["repo"] != ciRepoFull || line["status"] != wantStatus[i] || line["level"] != "ERROR" {
			t.Errorf("log line %d = %v, want ERROR repo %s status %v", i, line, ciRepoFull, wantStatus[i])
		}
	}
	for _, marker := range []string{ciToken, ciGHMarker} {
		if strings.Contains(s.logs.String(), marker) {
			t.Errorf("logs hold %q, want GitHub's text and the token kept out", marker)
		}
	}

	t.Run("no token", func(t *testing.T) {
		sp := ciSpec()
		sp.env = nil
		s, _ := ciStack(t, sp)
		agent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
		resp := s.post("/agents/"+agent+"/export-ci", open)
		if e := ciErr(t, resp); resp.Status != 500 || e.Code != "config_error" || e.Message != "GITHUB_TOKEN is not configured" {
			t.Errorf("open_pr without a token = %d %s %q, want 500 config_error", resp.Status, e.Code, e.Message)
		}
		if got := s.gh.requests(); len(got) != 0 {
			t.Errorf("GitHub requests without a token = %v, want none", ciSeq(got))
		}
		if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
			t.Errorf("ci_installations rows = %d, want 0", n)
		}
	})
}

// ---- AC6040: lost slug race ----

func TestCiExport_LosingTheSlugRaceAfterPublishIsAConflict(t *testing.T) { // AC6040
	s, gh := ciStack(t, ciSpec())
	loser := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	winner := ciCreateAgent(t, s, "Winner Reviewer", "openrouter")

	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var unblock sync.Once
	gh.on("POST", "/pulls", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(reached) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
		ciJSON(w, http.StatusCreated, map[string]any{"number": 41, "html_url": "https://github.com/acme/payments-api/pull/41"})
	})
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })

	done := ciAsyncPost(s, "/agents/"+loser+"/export-ci", ciExportBody(ciRepoFull, "open_pr", nil))
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the export did not reach the pull request step")
	}
	// The pre-check passed (nobody owned the slug); another agent takes it while the publish is held.
	ciInstall(t, s, winner, ciRepoFull, "ci-reviewer", 1, time.Now())
	unblock.Do(func() { close(release) })

	var resp response
	select {
	case resp = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the export did not finish after the release")
	}
	if e := ciErr(t, resp); resp.Status != http.StatusConflict || e.Code != "ci_slug_conflict" ||
		e.Message != `Another agent already uses the CI name "ci-reviewer" in this repository` {
		t.Errorf("export that lost the race = %d %s %q, want 409 ci_slug_conflict", resp.Status, e.Code, e.Message)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations WHERE repo = ? AND slug = 'ci-reviewer'`, ciRepoFull); n != 1 {
		t.Errorf("installation rows for (repo, slug) = %d, want 1 (the winner's)", n)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations WHERE agent_id = ?`, loser); n != 0 {
		t.Errorf("installation rows of the losing agent = %d, want 0", n)
	}
}

// ciAsyncPost sends a POST from its own goroutine; failures are test errors, not fatals.
func ciAsyncPost(s *stack, path string, body any) <-chan response {
	ch := make(chan response, 1)
	go func() {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Error(err)
			ch <- response{}
			return
		}
		req, err := http.NewRequestWithContext(s.t.Context(), http.MethodPost, s.base+path, bytes.NewReader(raw))
		if err != nil {
			s.t.Error(err)
			ch <- response{}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			s.t.Errorf("POST %s: %v", path, err)
			ch <- response{}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			s.t.Errorf("POST %s: read body: %v", path, err)
		}
		ch <- response{Status: resp.StatusCode, Header: resp.Header, Body: b}
	}()
	return ch
}

// ---- database fixtures ----

func ciInstall(t testing.TB, s *stack, agent, repo, slug string, version int, at time.Time) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `INSERT INTO ci_installations (agent_id, repo, target_type, installed_at, slug, manifest_version, provider)
		VALUES (?, ?, 'gha', ?, ?, ?, 'openrouter') RETURNING id::text`, agent, repo, at, slug, version)
}

// ciRunRow is a ci_runs row with its agent_runs row.
type ciRunRow struct {
	Installation string // "" = installation deleted (NULL)
	PR           *int
	RanAt        time.Time
	Status       string
	Findings     int
	Blockers     *int
	Cost         *float64
	DurationMS   *int
	URL          string
	GithubRunID  int64 // 0 = NULL
	Attempt      int
	HeadSHA      string
	Manifest     *int
	Error        *string
}

func ciInsertRun(t testing.TB, s *stack, agent string, r ciRunRow) string {
	t.Helper()
	runStatus := "done"
	if r.Status == "failed" {
		runStatus = "failed"
	}
	agentRun := scalar[string](t, s.db.DB, `INSERT INTO agent_runs
		(workspace_id, agent_id, ran_at, provider, model, duration_ms, status, source, findings_count, error, blockers, cost_usd)
		SELECT workspace_id, id, ?::timestamptz, 'openrouter', ?::text, ?::int, ?::text, 'ci', ?::int, ?::text, ?::int, ?::numeric
		FROM agents WHERE id = ? RETURNING id::text`,
		r.RanAt, ciModel, r.DurationMS, runStatus, r.Findings, r.Error, r.Blockers, r.Cost, agent)
	var inst, ghID, attempt any
	if r.Installation != "" {
		inst = r.Installation
	}
	if r.GithubRunID != 0 {
		ghID, attempt = r.GithubRunID, max(r.Attempt, 1)
	}
	return scalar[string](t, s.db.DB, `INSERT INTO ci_runs
		(ci_installation_id, pr_number, ran_at, status, findings_count, cost_usd, github_url, source, agent_run_id, github_run_id, run_attempt, head_sha, manifest_version)
		VALUES (?::uuid, ?::int, ?::timestamptz, ?::text, ?::int, ?::numeric, ?::text, 'gha', ?::uuid, ?::bigint, ?::int, ?::text, ?::int)
		RETURNING id::text`,
		inst, r.PR, r.RanAt, r.Status, r.Findings, r.Cost, r.URL, agentRun, ghID, attempt, r.HeadSHA, r.Manifest)
}

// ---- AC6033: lists ----

func TestCiLists_InstallationsAndRunsWithVerdicts(t *testing.T) { // AC6033, EC6043
	s := startStack(t, spec{})
	g := ciGoldens(t, s)
	alpha := ciCreateAgent(t, s, "Alpha Reviewer", "openrouter")
	beta := ciCreateAgent(t, s, "Beta Reviewer", "openrouter")
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	iA1 := ciInstall(t, s, alpha, ciRepoFull, "alpha-reviewer", 2, base.Add(-time.Hour))
	iA2 := ciInstall(t, s, alpha, "acme/other-repo", "alpha-reviewer", 1, base.Add(-2*time.Hour))
	iB := ciInstall(t, s, beta, ciRepoFull, "beta-reviewer", 1, base.Add(-30*time.Minute))
	url := func(repo string, n int) string { return fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, n) }

	ciInsertRun(t, s, alpha, ciRunRow{Installation: iA1, PR: ciPtr(7), RanAt: base.Add(-50 * time.Minute), Status: "succeeded", Findings: 3,
		Blockers: ciPtr(0), Cost: ciPtr(0.0123), DurationMS: ciPtr(4321), URL: url(ciRepoFull, 1001), GithubRunID: 1001, HeadSHA: "aaa1", Manifest: ciPtr(2)})
	ciInsertRun(t, s, alpha, ciRunRow{Installation: iA1, PR: ciPtr(8), RanAt: base.Add(-10 * time.Minute), Status: "succeeded", Findings: 5,
		Blockers: ciPtr(2), Cost: ciPtr(0.5), DurationMS: ciPtr(90500), URL: url(ciRepoFull, 1002), GithubRunID: 1002, HeadSHA: "aaa2", Manifest: ciPtr(2)})
	ciInsertRun(t, s, alpha, ciRunRow{Installation: iA2, RanAt: base.Add(-100 * time.Minute), Status: "no_findings", Findings: 0,
		URL: url("acme/other-repo", 1003), GithubRunID: 1003, HeadSHA: "aaa3", Manifest: ciPtr(1)})
	ciInsertRun(t, s, beta, ciRunRow{Installation: iB, PR: ciPtr(9), RanAt: base.Add(-5 * time.Minute), Status: "failed",
		Error: ciPtr("The CI run produced no result artifact"), URL: url(ciRepoFull, 1004), GithubRunID: 1004, HeadSHA: "bbb1", Manifest: ciPtr(1)})
	ciInsertRun(t, s, alpha, ciRunRow{Installation: "", PR: ciPtr(3), RanAt: base.Add(-70 * time.Minute), Status: "succeeded", Findings: 1,
		Blockers: ciPtr(0), Cost: ciPtr(0.01), DurationMS: ciPtr(1000), URL: url("acme/gone-repo", 2001), GithubRunID: 2001, HeadSHA: "ggg1", Manifest: ciPtr(1)})

	insts := s.get("/agents/" + alpha + "/ci-installations")
	g.check("ci_installations", insts, http.StatusOK)
	var parsed struct {
		Installations []struct {
			ID           string  `json:"id"`
			WorkflowPath *string `json:"workflow_path"`
			LastRun      *struct {
				PRNumber int     `json:"pr_number"`
				Verdict  *string `json:"verdict"`
			} `json:"last_run"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(insts.Body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Installations) != 2 || parsed.Installations[0].ID != iA1 || parsed.Installations[1].ID != iA2 {
		t.Fatalf("alpha installations = %s, want %s then %s (newest first)", insts.Body, iA1, iA2)
	}
	if lr := parsed.Installations[0].LastRun; lr == nil || lr.PRNumber != 8 || lr.Verdict == nil || *lr.Verdict != "blocked" {
		t.Errorf("last_run of the first installation = %+v, want the newest run (PR 8, blocked)", lr)
	}
	if w := parsed.Installations[0].WorkflowPath; w == nil || *w != ".github/workflows/devdigest-review-alpha-reviewer.yml" {
		t.Errorf("workflow_path = %v, want the slug's workflow file", w)
	}

	betaInsts := s.get("/agents/" + beta + "/ci-installations").Obj(t)["installations"].([]any)
	if len(betaInsts) != 1 {
		t.Fatalf("beta installations = %v, want one", betaInsts)
	}
	if lr := betaInsts[0].(map[string]any)["last_run"].(map[string]any); lr["status"] != "failed" || lr["verdict"] != nil || lr["duration_s"] != nil {
		t.Errorf("beta last_run = %v, want a failed run with no verdict and no duration", lr)
	}

	g.check("ci_runs_agent", s.get("/ci/runs?agent_id="+alpha+"&limit=20"), http.StatusOK)
	all := s.get("/ci/runs")
	g.check("ci_runs_all", all, http.StatusOK)
	runs := all.Obj(t)["runs"].([]any)
	var order []string
	for _, r := range runs {
		order = append(order, fmt.Sprint(r.(map[string]any)["github_url"]))
	}
	wantOrder := []string{url(ciRepoFull, 1004), url(ciRepoFull, 1002), url(ciRepoFull, 1001), url("acme/gone-repo", 2001), url("acme/other-repo", 1003)}
	if diff := cmp.Diff(wantOrder, order); diff != "" {
		t.Errorf("GET /ci/runs order (-want +got):\n%s", diff)
	}
	if n := len(s.get("/ci/runs?limit=2").Obj(t)["runs"].([]any)); n != 2 {
		t.Errorf("GET /ci/runs?limit=2 returned %d runs, want 2", n)
	}
	for _, r := range runs {
		run := r.(map[string]any)
		wantRepo := map[string]any{url("acme/gone-repo", 2001): "acme/gone-repo"}[run["github_url"].(string)]
		if wantRepo != nil && run["repo"] != wantRepo {
			t.Errorf("run %v repo = %v, want %v parsed from the URL", run["github_url"], run["repo"], wantRepo)
		}
	}

	if resp := s.get("/agents/00000000-0000-4000-8000-000000000999/ci-installations"); resp.Status != 404 || ciErr(t, resp).Code != "agent_not_found" {
		t.Errorf("installations of an unknown agent = %d %s, want 404 agent_not_found", resp.Status, resp.Body)
	}
	for _, q := range []string{"?limit=201", "?limit=0", "?limit=x", "?agent_id=nope"} {
		if resp := s.get("/ci/runs" + q); resp.Status != http.StatusUnprocessableEntity {
			t.Errorf("GET /ci/runs%s = %d %s, want 422", q, resp.Status, resp.Body)
		}
	}
	if resp := s.get("/agents/nope/ci-installations"); resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("installations of a non-UUID id = %d, want 422", resp.Status)
	}

	// EC6043: a deleted agent takes its installations; its runs stay, without agent and with the repo of their URL.
	if resp := s.do(http.MethodDelete, "/agents/"+beta, nil); resp.Status >= 300 {
		t.Fatalf("DELETE /agents/%s = %d %s, want success", beta, resp.Status, resp.Body)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations WHERE id = ?`, iB); n != 0 {
		t.Errorf("installation of the deleted agent still exists")
	}
	after := s.get("/ci/runs")
	g.check("ci_runs_after_agent_delete", after, http.StatusOK)
	for _, r := range after.Obj(t)["runs"].([]any) {
		run := r.(map[string]any)
		if run["github_url"] != url(ciRepoFull, 1004) {
			continue
		}
		if run["agent"] != nil || run["agent_id"] != nil || run["repo"] != ciRepoFull || run["ci_installation_id"] != nil {
			t.Errorf("run of the deleted agent = %v, want agent null, installation null and repo %s from the URL", run, ciRepoFull)
		}
	}
}

// ---- artifacts ----

func ciZip(t testing.TB, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ciBombZip is a ZIP whose entry declares declared bytes and inflates to inflated zero bytes.
func ciBombZip(t testing.TB, name string, declared uint64, inflated int) []byte {
	t.Helper()
	var packed bytes.Buffer
	fw, err := flate.NewWriter(&packed, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	var crc uint32
	for left := inflated; left > 0; left -= len(chunk) {
		n := min(left, len(chunk))
		crc = crc32.Update(crc, crc32.IEEETable, chunk[:n])
		if _, err := fw.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: name, Method: zip.Deflate, CRC32: crc,
		CompressedSize64: uint64(packed.Len()), UncompressedSize64: declared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(packed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func ciServe(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(body)
	}
}

const ciRepoID = 4242

// ciRun is a completed, successful run of the workflow of slug in repo.
func ciRun(slug, repo string, id int64, created time.Time) ciWFRun {
	return ciWFRun{
		ID: id, Attempt: 1, HeadSHA: fmt.Sprintf("sha%d", id), Path: ".github/workflows/devdigest-review-" + slug + ".yml",
		Conclusion: "success", URL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, id), RepoID: ciRepoID, HeadRepoID: ciRepoID,
		CreatedAt: created.UTC().Format(time.RFC3339),
	}
}

// ciResultJSON is a verified-looking devdigest-result.json of run (version 2); over replaces keys.
func ciResultJSON(run ciWFRun, slug string, over map[string]any) []byte {
	m := map[string]any{
		"findings_count": 3, "critical": 1, "warning": 1, "suggestion": 1, "cost_usd": 0.0123, "duration_ms": 4321,
		"agent": slug, "version": "2", "pr_number": 7, "commit_sha": run.HeadSHA, "repository": "acme/ci-one",
		"repository_id": run.RepoID, "run_id": run.ID, "run_attempt": run.Attempt, "model": ciModel,
		"manifest_version": 2, "skills": []string{}, "blockers": 1,
	}
	for k, v := range over {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return raw
}

// addGoodRun registers run with a result artifact that verifies against it; over replaces result keys.
func (d *ciGH) addGoodRun(t testing.TB, repo, slug string, run ciWFRun, artID int64, over map[string]any) ciWFRun {
	d.mu.Lock()
	d.workflows[repo+"/devdigest-review-"+slug+".yml"] = append(d.workflows[repo+"/devdigest-review-"+slug+".yml"], run)
	d.mu.Unlock()
	d.addArtifact(run.ID, ciArt{ID: artID, Name: "devdigest-result-" + slug, Size: 900, CreatedAt: run.CreatedAt},
		ciServe(ciZip(t, "devdigest-result.json", ciResultJSON(run, slug, over))))
	return run
}

// ---- AC6034: refresh stores ----

func TestCiRefresh_StoresVerifiedAndFailedRuns(t *testing.T) { // AC6034, EC6035, EC6042
	s, gh := ciStack(t, ciSpec())
	g := ciGoldens(t, s)
	agent := ciCreateAgent(t, s, "Alpha Reviewer", "openrouter")
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	two := ciInstall(t, s, agent, "acme/ci-two", "alpha-reviewer", 1, base.Add(-time.Hour))
	one := ciInstall(t, s, agent, "acme/ci-one", "alpha-reviewer", 3, base)

	run101 := ciRun("alpha-reviewer", "acme/ci-one", 101, base.Add(-3*time.Hour))
	run102 := ciRun("alpha-reviewer", "acme/ci-one", 102, base.Add(-4*time.Hour))
	run103 := ciRun("alpha-reviewer", "acme/ci-one", 103, base.Add(-5*time.Hour))
	run103.Conclusion = "cancelled"
	run104 := ciRun("alpha-reviewer", "acme/ci-one", 104, base.Add(-2*time.Hour))
	run104.PRs = []int{9}
	run105 := ciRun("alpha-reviewer", "acme/ci-one", 105, base.Add(-6*time.Hour))
	run105.Conclusion = ""
	run106 := ciRun("alpha-reviewer", "acme/ci-one", 106, base.Add(-7*time.Hour))
	run106.Conclusion = "skipped"
	gh.addWorkflow("acme/ci-one", "alpha-reviewer", run101, run102, run103, run104, run105, run106)
	gh.addArtifact(101, ciArt{ID: 5001, Name: "devdigest-result-alpha-reviewer", Size: 800, CreatedAt: run101.CreatedAt},
		ciServe(ciZip(t, "devdigest-result.json", ciResultJSON(run101, "alpha-reviewer", nil))))
	// Run 104 has only an unrelated artifact: the runner died before it wrote its result.
	gh.addArtifact(104, ciArt{ID: 5004, Name: "something-else", Size: 10, CreatedAt: run104.CreatedAt}, nil)
	// Run 102 is stored already.
	ciInsertRun(t, s, agent, ciRunRow{Installation: one, RanAt: base.Add(-4 * time.Hour), Status: "succeeded", Findings: 1,
		Blockers: ciPtr(0), URL: run102.URL, GithubRunID: 102, HeadSHA: run102.HeadSHA, Manifest: ciPtr(3)})
	// acme/ci-two has no workflow on its default branch: its runs endpoint is a 404.

	resp := s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	want := map[string]any{
		"ingested": float64(2), "degraded": false,
		"installations": []any{
			map[string]any{"installation_id": one, "repo": "acme/ci-one", "state": "ok", "ingested": float64(2), "skipped": []any{}},
			map[string]any{"installation_id": two, "repo": "acme/ci-two", "state": "not_installed", "ingested": float64(0), "skipped": []any{}},
		},
	}
	if diff := cmp.Diff(want, resp.JSON(t)); diff != "" {
		t.Errorf("refresh body mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(string(resp.Body), "error_code") {
		t.Errorf("refresh body %s has an error_code key, want none without an error", resp.Body)
	}
	if diff := cmp.Diff([]string{"ingested", "degraded", "installations"}, ciKeyOrder(t, resp.Body)); diff != "" {
		t.Errorf("CiRefreshResult key order mismatch (-want +got):\n%s", diff)
	}
	g.check("ci_refresh", resp, http.StatusOK)

	type stored struct {
		Status        string
		PR            *int     `gorm:"column:pr_number"`
		FindingsCount int      `gorm:"column:findings_count"`
		Cost          *float64 `gorm:"column:cost_usd"`
		GithubURL     string   `gorm:"column:github_url"`
		Source        string
		HeadSHA       string `gorm:"column:head_sha"`
		Manifest      *int   `gorm:"column:manifest_version"`
		RunProvider   string `gorm:"column:provider"`
		RunSource     string `gorm:"column:run_source"`
		RunStatus     string `gorm:"column:run_status"`
		Model         *string
		DurationMS    *int    `gorm:"column:duration_ms"`
		Blockers      *int    `gorm:"column:blockers"`
		Error         *string `gorm:"column:error"`
		PRID          *string `gorm:"column:pr_id"`
		Attempt       int     `gorm:"column:run_attempt"`
	}
	read := func(id int64) stored {
		var row stored
		err := s.db.DB.Raw(`SELECT c.status, c.pr_number, c.findings_count, c.cost_usd, c.github_url, c.source, c.head_sha, c.manifest_version,
			c.run_attempt, a.provider, a.source AS run_source, a.status AS run_status, a.model, a.duration_ms, a.blockers, a.error, a.pr_id::text AS pr_id
			FROM ci_runs c JOIN agent_runs a ON a.id = c.agent_run_id WHERE c.github_run_id = ?`, id).Scan(&row).Error
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	ok := read(101)
	wantOK := stored{Status: "succeeded", PR: ciPtr(7), FindingsCount: 3, Cost: ciPtr(0.0123), GithubURL: run101.URL, Source: "gha", HeadSHA: "sha101",
		Manifest: ciPtr(2), RunProvider: "openrouter", RunSource: "ci", RunStatus: "done", Model: ciPtr(ciModel), DurationMS: ciPtr(4321), Blockers: ciPtr(1), Attempt: 1}
	if diff := cmp.Diff(wantOK, ok); diff != "" {
		t.Errorf("stored verified run mismatch (-want +got):\n%s", diff)
	}
	failed := read(104)
	wantFailed := stored{Status: "failed", PR: ciPtr(9), FindingsCount: 0, GithubURL: run104.URL, Source: "gha", HeadSHA: "sha104", Manifest: ciPtr(3),
		RunProvider: "openrouter", RunSource: "ci", RunStatus: "failed", Blockers: ciPtr(0), Error: ciPtr("The CI run produced no result artifact"), Attempt: 1}
	if diff := cmp.Diff(wantFailed, failed); diff != "" {
		t.Errorf("stored failed run mismatch (-want +got):\n%s", diff)
	}
	for _, id := range []int64{103, 105, 106} {
		if n := count(t, s.db.DB, `SELECT count(*) FROM ci_runs WHERE github_run_id = ?`, id); n != 0 {
			t.Errorf("run %d is stored, want a cancelled, null-conclusion or skipped run left out", id)
		}
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_runs WHERE agent_run_id IS NULL`); n != 0 {
		t.Errorf("ci_runs without an agent run = %d, want 0", n)
	}
	g.check("ci_refresh_runs", s.get("/ci/runs?agent_id="+agent), http.StatusOK)

	// A second refresh finds everything stored; the body {"agent_id"} limits it to one agent.
	again := s.do(http.MethodPost, "/ci/refresh", map[string]any{"agent_id": agent})
	if got := again.Obj(t)["ingested"]; again.Status != http.StatusOK || got != float64(0) {
		t.Errorf("second refresh = %d %s, want ingested 0", again.Status, again.Body)
	}
}

// A fork pull request runs the fork's copy of the workflow: it can drop the fork guard and upload
// a result that matches the run's commit, repository id and workflow path. Such a run is not
// inspected, downloaded or stored, however perfect its artifact; same-repository runs still are.
func TestCiRefresh_ForkRunWithAForgedArtifactIsNotStored(t *testing.T) { // AC6062, EC6042
	s, gh := ciStack(t, ciSpec())
	agent := ciCreateAgent(t, s, "Alpha Reviewer", "openrouter")
	const slug, repo = "alpha-reviewer", "acme/ci-one"
	inst := ciInstall(t, s, agent, repo, slug, 2, time.Now())
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	fork := ciRun(slug, repo, 401, base.Add(-time.Hour))
	fork.HeadRepoID = ciRepoID + 1
	gone := ciRun(slug, repo, 402, base.Add(-2*time.Hour)) // the fork was deleted: no head repository
	gone.HeadRepoID = 0
	forkNoArtifact := ciRun(slug, repo, 403, base.Add(-3*time.Hour))
	forkNoArtifact.HeadRepoID = ciRepoID + 1
	forkNoArtifact.Conclusion = "failure"
	own := ciRun(slug, repo, 404, base.Add(-4*time.Hour))
	forged := map[string]any{"blockers": 0, "cost_usd": 999999.99, "pr_number": 1}
	gh.addWorkflow(repo, slug, forkNoArtifact) // replaces the list: add the others after it
	gh.addGoodRun(t, repo, slug, fork, 9401, forged)
	gh.addGoodRun(t, repo, slug, gone, 9402, forged)
	gh.addGoodRun(t, repo, slug, own, 9404, nil)

	resp := s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	want := map[string]any{
		"ingested": float64(1), "degraded": false,
		"installations": []any{map[string]any{"installation_id": inst, "repo": repo, "state": "ok", "ingested": float64(1), "skipped": []any{}}},
	}
	if diff := cmp.Diff(want, resp.JSON(t)); diff != "" {
		t.Errorf("refresh body mismatch (-want +got):\n%s", diff)
	}
	if got := count(t, s.db.DB, `SELECT count(*) FROM ci_runs WHERE github_run_id IN (401, 402, 403)`); got != 0 {
		t.Errorf("fork runs stored = %d, want 0", got)
	}
	if got := count(t, s.db.DB, `SELECT count(*) FROM ci_runs WHERE github_run_id = 404`); got != 1 {
		t.Errorf("same-repository run stored = %d, want 1", got)
	}
	for _, id := range []string{"401", "402", "403"} {
		if reqs := ciWith(s.gh.requests(), "GET", "/runs/"+id+"/artifacts"); len(reqs) != 0 {
			t.Errorf("run %s: %d artifact listing(s), want none for a run from a fork", id, len(reqs))
		}
	}
	for _, art := range []string{"9401", "9402"} {
		if reqs := ciWith(s.gh.requests(), "GET", "/artifacts/"+art+"/zip"); len(reqs) != 0 {
			t.Errorf("artifact %s was downloaded %d time(s), want none", art, len(reqs))
		}
	}
	lines := logLines(t, s.logs, "ci refresh: run not inspected")
	if len(lines) != 3 {
		t.Fatalf("not-inspected log lines = %d, want 3 (runs 401-403)", len(lines))
	}
	for _, line := range lines {
		if line["level"] != "WARN" || line["installation_id"] != inst || line["repo"] != repo || line["reason"] != "head_repository_differs" {
			t.Errorf("log line = %v, want WARN with installation_id %s, repo %s and the fixed reason", line, inst, repo)
		}
	}
}

// ---- AC6035: refresh refusals ----

func TestCiRefresh_Refusals(t *testing.T) { // AC6035, EC6030
	sp := ciSpec()
	sp.env = nil // no GitHub token
	s, _ := ciStack(t, sp)

	unknown := s.post("/ci/refresh", map[string]any{"agent_id": "00000000-0000-4000-8000-000000000999"})
	if e := ciErr(t, unknown); unknown.Status != 404 || e.Code != "agent_not_found" || e.Message != "Agent not found" {
		t.Errorf("refresh of an unknown agent = %d %s %q, want 404 agent_not_found (checked before the token)", unknown.Status, e.Code, e.Message)
	}
	noToken := s.post("/ci/refresh", nil)
	if e := ciErr(t, noToken); noToken.Status != 500 || e.Code != "config_error" || e.Message != "GITHUB_TOKEN is not configured" {
		t.Errorf("refresh without a token = %d %s %q, want 500 config_error", noToken.Status, e.Code, e.Message)
	}
	bad := s.post("/ci/refresh", map[string]any{"agent_id": "x"})
	if bad.Status != 422 || ciErr(t, bad).Code != "validation_error" {
		t.Errorf("refresh with agent_id x = %d %s, want 422 validation_error", bad.Status, bad.Body)
	}
	if resp := s.do(http.MethodPost, "/ci/refresh", map[string]any{}, "Origin", "http://evil.example"); resp.Status != http.StatusForbidden {
		t.Errorf("refresh with a foreign Origin = %d, want 403", resp.Status)
	}
	if got := s.gh.requests(); len(got) != 0 {
		t.Errorf("GitHub requests = %v, want none", ciSeq(got))
	}
}

// ---- AC6041: skipped artifacts ----

func TestCiRefresh_SkipsArtifactsThatCannotBeTrusted(t *testing.T) { // AC6041, EC6038
	s, gh := ciStack(t, ciSpec())
	g := ciGoldens(t, s)
	agent := ciCreateAgent(t, s, "Alpha Reviewer", "openrouter")
	const slug, repo = "alpha-reviewer", "acme/ci-one"
	inst := ciInstall(t, s, agent, repo, slug, 1, time.Now())
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	artName := "devdigest-result-" + slug
	mk := func(id int64) ciWFRun { return ciRun(slug, repo, id, base.Add(-time.Duration(id)*time.Minute)) }

	runs := make([]ciWFRun, 0, 9)
	add := func(run ciWFRun, art ciArt, download http.HandlerFunc) {
		runs = append(runs, run)
		art.CreatedAt = run.CreatedAt
		gh.addArtifact(run.ID, art, download)
	}
	padded := append(ciResultJSON(mk(305), slug, nil), bytes.Repeat([]byte(" "), 70*1024)...)
	badUTF8 := append([]byte(`{"findings_count":0,"cost_usd":null,"agent":"`), 0xff, '"', '}')
	other := mk(309)
	other.Path = ".github/workflows/other-workflow.yml"

	add(mk(301), ciArt{ID: 9301, Name: artName, Size: 500, Expired: true}, nil)                                                                                                       // artifact_expired
	add(mk(302), ciArt{ID: 9302, Name: artName, Size: 2 << 20}, ciServe([]byte("never downloaded")))                                                                                  // reported size
	add(mk(303), ciArt{ID: 9303, Name: artName, Size: 900}, ciServe(bytes.Repeat([]byte{'x'}, (1<<20)+1)))                                                                            // body over the cap
	add(mk(304), ciArt{ID: 9304, Name: artName, Size: 900}, ciServe(ciBombZip(t, "devdigest-result.json", 1024, 100<<20)))                                                            // inflates to 100 MiB
	add(mk(305), ciArt{ID: 9305, Name: artName, Size: 900}, ciServe(ciZip(t, "devdigest-result.json", padded)))                                                                       // declared 70 KiB
	add(mk(306), ciArt{ID: 9306, Name: artName, Size: 900}, ciServe(ciZip(t, "devdigest-result.json", badUTF8)))                                                                      // invalid UTF-8
	add(mk(307), ciArt{ID: 9307, Name: artName, Size: 900}, ciServe(ciZip(t, "devdigest-result.json", []byte(`[1,2,3]`))))                                                            // JSON array
	add(mk(308), ciArt{ID: 9308, Name: artName, Size: 900}, ciServe(ciZip(t, "devdigest-result.json", ciResultJSON(mk(308), slug, map[string]any{"commit_sha": "someotherc0mmit"})))) // other commit
	add(other, ciArt{ID: 9309, Name: artName, Size: 900}, ciServe(ciZip(t, "devdigest-result.json", ciResultJSON(other, slug, nil))))                                                 // other workflow file
	gh.addWorkflow(repo, slug, runs...)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	resp := s.post("/ci/refresh", nil)
	runtime.ReadMemStats(&after)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	g.check("ci_refresh_skips", resp, http.StatusOK)

	var got struct {
		Ingested      int  `json:"ingested"`
		Degraded      bool `json:"degraded"`
		Installations []struct {
			InstallationID string `json:"installation_id"`
			State          string `json:"state"`
			Ingested       int    `json:"ingested"`
			Skipped        []struct {
				GithubRunID int64  `json:"github_run_id"`
				Reason      string `json:"reason"`
			} `json:"skipped"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	wantSkips := map[int64]string{
		301: "artifact_expired", 302: "artifact_too_large", 303: "artifact_too_large", 304: "artifact_too_large", 305: "artifact_too_large",
		306: "artifact_invalid", 307: "artifact_invalid", 308: "artifact_mismatch", 309: "artifact_mismatch",
	}
	if got.Ingested != 0 || got.Degraded || len(got.Installations) != 1 || got.Installations[0].State != "ok" || got.Installations[0].InstallationID != inst {
		t.Fatalf("refresh = %s, want one ok installation and nothing ingested", resp.Body)
	}
	gotSkips := map[int64]string{}
	var order []int64
	for _, sk := range got.Installations[0].Skipped {
		gotSkips[sk.GithubRunID] = sk.Reason
		order = append(order, sk.GithubRunID)
	}
	if diff := cmp.Diff(wantSkips, gotSkips); diff != "" {
		t.Errorf("skipped reasons mismatch (-want +got):\n%s", diff)
	}
	if want := []int64{301, 302, 303, 304, 305, 306, 307, 308, 309}; !slices.Equal(order, want) {
		t.Errorf("skipped order = %v, want GitHub's order %v", order, want)
	}

	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_runs`); n != 0 {
		t.Errorf("ci_runs rows = %d, want nothing stored for skipped runs", n)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM agent_runs WHERE source = 'ci'`); n != 0 {
		t.Errorf("ci agent_runs rows = %d, want 0", n)
	}
	if downloads := ciWith(s.gh.requests(), "GET", "/artifacts/9302/zip"); len(downloads) != 0 {
		t.Errorf("a 2 MiB artifact was downloaded %d time(s), want no download", len(downloads))
	}
	if downloads := ciWith(s.gh.requests(), "GET", "/artifacts/9301/zip"); len(downloads) != 0 {
		t.Errorf("an expired artifact was downloaded %d time(s), want no download", len(downloads))
	}
	// The bomb inflates to 100 MiB; the reader stops at 65,537 bytes, so the refresh allocates a tiny part of that.
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 40<<20 {
		t.Errorf("the refresh allocated %d bytes, want far less than the 100 MiB the ZIP bomb inflates to", grown)
	}

	lines := logLines(t, s.logs, "ci refresh: run skipped")
	if len(lines) != len(wantSkips) {
		t.Fatalf("skip log lines = %d, want %d", len(lines), len(wantSkips))
	}
	for _, line := range lines {
		id, _ := line["github_run_id"].(float64)
		if line["level"] != "WARN" || line["installation_id"] != inst || line["repo"] != repo || line["reason"] != wantSkips[int64(id)] {
			t.Errorf("skip log line = %v, want WARN with installation_id %s, repo %s and reason %s", line, inst, repo, wantSkips[int64(id)])
		}
	}
}

// ---- AC6042: redirects ----

func TestCiRefresh_ArtifactRedirectsLeaveTheTokenAtGitHub(t *testing.T) { // AC6042, EC6039
	type seen struct{ path, auth, cookie string }
	var mu sync.Mutex
	var storageSeen []seen
	var storage *httptest.Server
	good := ciZip(t, "devdigest-result.json", nil) // replaced below once the run is known
	storage = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		storageSeen = append(storageSeen, seen{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie")})
		body := good
		mu.Unlock()
		switch r.URL.Path {
		case "/artifact/again":
			http.Redirect(w, r, storage.URL+"/artifact/ok?sig=SIGNED-TWO", http.StatusFound)
		default:
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(storage.Close)
	var plainHits int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		plainHits++
		mu.Unlock()
	}))
	t.Cleanup(plain.Close)

	sp := ciSpec()
	sp.artifactClient = storage.Client()
	s, gh := ciStack(t, sp)
	agent := ciCreateAgent(t, s, "Redirect Reviewer", "openrouter")
	const slug = "redirect-reviewer"
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	redirect := func(location string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if location != "" {
				w.Header().Set("Location", location)
			}
			w.WriteHeader(http.StatusFound)
		}
	}
	targets := []string{
		storage.URL + "/artifact/ok?sig=SIGNED-ONE",
		plain.URL + "/artifact?sig=SIGNED-PLAIN",
		storage.URL + "/artifact/again?sig=SIGNED-THREE",
		"",
	}
	var insts []string
	for i, loc := range targets {
		repo := fmt.Sprintf("acme/redir-%d", i+1)
		insts = append(insts, ciInstall(t, s, agent, repo, slug, 1, base.Add(-time.Duration(i)*time.Minute)))
		run := ciRun(slug, repo, int64(400+i), base.Add(-time.Hour))
		gh.addWorkflow(repo, slug, run)
		gh.addArtifact(run.ID, ciArt{ID: int64(8000 + i), Name: "devdigest-result-" + slug, Size: 900, CreatedAt: run.CreatedAt}, redirect(loc))
		if i == 0 {
			mu.Lock()
			good = ciZip(t, "devdigest-result.json", ciResultJSON(run, slug, nil))
			mu.Unlock()
		}
	}

	resp := s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	var out struct {
		Ingested      int  `json:"ingested"`
		Degraded      bool `json:"degraded"`
		Installations []struct {
			InstallationID string `json:"installation_id"`
			State          string `json:"state"`
			ErrorCode      string `json:"error_code"`
			Ingested       int    `json:"ingested"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Installations) != 4 || out.Ingested != 1 || !out.Degraded {
		t.Fatalf("refresh = %s, want one run ingested, four installations, degraded", resp.Body)
	}
	for i, in := range out.Installations {
		if in.InstallationID != insts[i] {
			t.Fatalf("installation %d = %s, want %s", i, in.InstallationID, insts[i])
		}
		if i == 0 {
			if in.State != "ok" || in.Ingested != 1 || in.ErrorCode != "" {
				t.Errorf("first installation = %+v, want ok with the artifact ingested", in)
			}
			continue
		}
		if in.State != "error" || in.ErrorCode != "github_unavailable" || in.Ingested != 0 {
			t.Errorf("installation %d = %+v, want error github_unavailable", i+1, in)
		}
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_runs`); n != 1 {
		t.Errorf("ci_runs rows = %d, want only the artifact behind the https redirect", n)
	}

	mu.Lock()
	defer mu.Unlock()
	var paths []string
	for _, st := range storageSeen {
		paths = append(paths, st.path)
		if st.auth != "" || st.cookie != "" {
			t.Errorf("storage request %s carried Authorization %q / Cookie %q, want no credentials", st.path, st.auth, st.cookie)
		}
	}
	slices.Sort(paths)
	if diff := cmp.Diff([]string{"/artifact/again", "/artifact/ok"}, paths); diff != "" {
		t.Errorf("storage requests mismatch (-want +got):\n%s", diff)
	}
	if plainHits != 0 {
		t.Errorf("the plain http storage target was contacted %d time(s), want never", plainHits)
	}
	zips := 0
	for _, r := range s.gh.requests() {
		if strings.HasSuffix(r.Path, "/zip") {
			zips++
			if r.Auth != "Bearer "+ciToken {
				t.Errorf("GitHub download %s Authorization = %q, want the token on the first leg", r.Path, r.Auth)
			}
		}
	}
	if zips != 4 {
		t.Errorf("GitHub download requests = %d, want 4", zips)
	}
	for _, text := range []string{string(resp.Body), s.logs.String()} {
		for _, secret := range []string{ciToken, "SIGNED-", "127.0.0.1:" + strings.TrimPrefix(storage.URL, "https://127.0.0.1:")} {
			if strings.Contains(text, secret) {
				t.Errorf("an answer or log line holds %q, want tokens and storage URLs kept out", secret)
			}
		}
	}
}

// ---- AC6043: GitHub error codes ----

func TestCiRefresh_GitHubErrorsBecomeCodesWithoutGitHubText(t *testing.T) { // AC6043, EC6040
	s, gh := ciStack(t, ciSpec())
	g := ciGoldens(t, s)
	agent := ciCreateAgent(t, s, "Error Reviewer", "openrouter")
	const slug = "error-reviewer"
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	answers := []struct {
		status  int
		headers []string
		code    string
	}{
		{429, nil, "github_rate_limited"},
		{403, []string{"x-ratelimit-remaining", "0"}, "github_rate_limited"},
		{403, nil, "github_permission_denied"},
		{401, nil, "github_permission_denied"},
		{500, nil, "github_unavailable"},
	}
	var insts []string
	for i, a := range answers {
		repo := fmt.Sprintf("acme/err-%d", i+1)
		insts = append(insts, ciInstall(t, s, agent, repo, slug, 1, base.Add(-time.Duration(i)*time.Minute)))
		gh.fail("GET", "/repos/"+repo+"/actions/workflows/", a.status, a.headers...)
	}

	resp := s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	var out struct {
		Ingested      int  `json:"ingested"`
		Degraded      bool `json:"degraded"`
		Installations []struct {
			InstallationID string `json:"installation_id"`
			State          string `json:"state"`
			ErrorCode      string `json:"error_code"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Installations) != len(answers) || out.Ingested != 0 || !out.Degraded {
		t.Fatalf("refresh = %s, want five installations, degraded, nothing ingested", resp.Body)
	}
	for i, in := range out.Installations {
		if in.InstallationID != insts[i] || in.State != "error" || in.ErrorCode != answers[i].code {
			t.Errorf("installation %d = %+v, want %s error %s", i+1, in, insts[i], answers[i].code)
		}
	}
	g.check("ci_refresh_errors", resp, http.StatusOK)

	lines := logLines(t, s.logs, "ci refresh: installation failed")
	if len(lines) != len(answers) {
		t.Fatalf("installation failure log lines = %d, want %d", len(lines), len(answers))
	}
	for i, line := range lines {
		if line["installation_id"] != insts[i] || line["repo"] != fmt.Sprintf("acme/err-%d", i+1) ||
			line["code"] != answers[i].code || line["status"] != float64(answers[i].status) || line["level"] != "ERROR" {
			t.Errorf("failure log line %d = %v, want installation %s, code %s, status %d", i, line, insts[i], answers[i].code, answers[i].status)
		}
	}
	for name, text := range map[string]string{"response": string(resp.Body), "logs": s.logs.String()} {
		for _, secret := range []string{ciToken, ciGHMarker} {
			if strings.Contains(text, secret) {
				t.Errorf("%s hold %q, want GitHub's message text and the token kept out", name, secret)
			}
		}
	}
}

// ---- AC6044: budget ----

type ciClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *ciClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *ciClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestCiRefresh_BudgetEndsBeforeTheNextInstallation(t *testing.T) { // AC6044, EC6041
	clock := &ciClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	start := clock.Now()
	sp := ciSpec()
	sp.now = clock.Now
	s, gh := ciStack(t, sp)
	g := ciGoldens(t, s)
	agent := ciCreateAgent(t, s, "Slow Reviewer", "openrouter")
	const slug = "slow-reviewer"
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var insts []string
	for i := 1; i <= 3; i++ {
		insts = append(insts, ciInstall(t, s, agent, fmt.Sprintf("acme/slow-%d", i), slug, 1, base.Add(-time.Duration(i)*time.Minute)))
	}
	run := ciRun(slug, "acme/slow-1", 501, base)
	gh.addGoodRun(t, "acme/slow-1", slug, run, 9501, nil)
	for i := 2; i <= 3; i++ {
		gh.addWorkflow(fmt.Sprintf("acme/slow-%d", i), slug, ciRun(slug, fmt.Sprintf("acme/slow-%d", i), int64(500+i), base))
	}

	var mu sync.Mutex
	type call struct {
		path string
		at   time.Time
	}
	var calls []call
	gh.setHook(func(r *http.Request) {
		mu.Lock()
		calls = append(calls, call{r.URL.Path, clock.Now()})
		mu.Unlock()
		// The last call of the first installation takes 46 s of the fake clock.
		if strings.HasSuffix(r.URL.Path, "/artifacts/9501/zip") {
			clock.Advance(46 * time.Second)
		}
	})

	resp := s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	var out struct {
		Ingested      int  `json:"ingested"`
		Degraded      bool `json:"degraded"`
		Installations []struct {
			InstallationID string `json:"installation_id"`
			State          string `json:"state"`
			Ingested       int    `json:"ingested"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	wantStates := []string{"ok", "not_checked", "not_checked"}
	if len(out.Installations) != 3 || out.Ingested != 1 || !out.Degraded {
		t.Fatalf("refresh = %s, want three installations, one run ingested, degraded", resp.Body)
	}
	for i, in := range out.Installations {
		if in.InstallationID != insts[i] || in.State != wantStates[i] {
			t.Errorf("installation %d = %+v, want %s in state %s", i+1, in, insts[i], wantStates[i])
		}
	}
	if strings.Contains(string(resp.Body), "error_code") {
		t.Errorf("refresh body %s has an error_code key, want none for not_checked", resp.Body)
	}
	g.check("ci_refresh_not_checked", resp, http.StatusOK)

	mu.Lock()
	defer mu.Unlock()
	deadline := start.Add(45 * time.Second)
	for _, c := range calls {
		if !c.at.Before(deadline) {
			t.Errorf("GitHub call %s started at +%s, want it before the 45 s deadline", c.path, c.at.Sub(start))
		}
		if !strings.Contains(c.path, "/acme/slow-1/") {
			t.Errorf("GitHub call %s was made for an installation reached after the deadline", c.path)
		}
	}
	if len(calls) != 3 {
		t.Errorf("GitHub calls = %d, want 3 (runs, artifacts, download of the first installation)", len(calls))
	}
	if lines := logLines(t, s.logs, "ci refresh: installation failed"); len(lines) != 0 {
		t.Errorf("installation failure log lines = %v, want none for unchecked installations", lines)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_runs`); n != 1 {
		t.Errorf("ci_runs rows = %d, want the first installation's run only", n)
	}
}
