package main

// The runner process in GitLab mode (spec GitLab C FR16-FR23, FR29, FR30; AC17-AC23, AC29): the real
// runProcess over a real git fixture repository (CI_PROJECT_DIR), a TLS stub of the GitLab API, a
// stub model and nothing but the environment. The helpers are prefixed gn.

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	runnerdomain "dev-digest-go/backend/internal/ci/runner/domain"
)

const (
	gnToken      = "glpat-Tq9xZ4kLm2VbN8wRc5Hd"
	gnProviderKy = "sk-or-v1-Pj3sYe6UuA1oF7gKt0Wi"
	gnGitHubTok  = "ghp_Zk8mQw2Rt6Yn4Vb1Xc9L"
	gnJobTok     = "gljob-Hf5dSa3Gp7Jk0Lz2Xq8W"
	gnOtherVal   = "other-Bn6vCx1Zm4Lk9Qw3Er7T"

	gnSlug        = "security-reviewer"
	gnTitle       = `Add "feature" X`
	gnDescription = "A cool feature.\n\nSee ticket 12."
	gnProjectPath = "acme/platform/widgets"
	gnIID         = "7"
	gnProjectID   = "42"
	gnJobID       = "9001"

	gnTokenMissingMsg = "DEVDIGEST_GITLAB_TOKEN is not set: add it as a masked, not protected CI/CD variable (scope api), or set post_as to none"
)

// gnOrigPath is the PATH of the test process before any test put a git wrapper in front of it.
var gnOrigPath = sync.OnceValue(func() string { return os.Getenv("PATH") })

// gnRealGit is the git program the wrapper and the fixture builder run.
var gnRealGit = sync.OnceValue(func() string {
	_ = gnOrigPath()
	p, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	return p
})

func gnGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	git := gnRealGit()
	if git == "" {
		t.Skip("git is not installed")
	}
	cmd := exec.Command(git, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + gnOrigPath(), "HOME=" + dir, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_AUTHOR_DATE=2026-01-02T03:04:05Z",
		"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func gnWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gnRepo is a fixture checkout: base, head (the merge request's last commit) and merged (a child
// of head, the commit of a merged-results pipeline).
type gnRepo struct {
	dir                string
	base, head, merged string
	evilMarker         string // exists only when git ran a program the repository configures
	evilScript         string
}

const gnConfigBase = "export const config = {\n  name: 'app',\n  host: 'localhost',\n  port: 3000,\n  timeout: 30,\n" +
	"  retries: 3,\n  debug: false,\n  region: 'eu',\n  version: 2,\n  verbose: true,\n}\n"

const gnConfigHead = "export const config = {\n  name: 'app',\n  host: 'localhost',\n  port: 3000,\n  timeout: 30,\n" +
	"  retries: 3,\n  debug: false,\n  region: 'eu',\n  version: 2,\n  apiKey: 'sk_live_abcdef123456',\n  verbose: true,\n}\n"

const gnGuide = "the guide, line 1\nthe guide, line 2\nthe guide, line 3\nthe guide, line 4\nthe guide, line 5\n" +
	"the guide, line 6\nthe guide, line 7\nthe guide, line 8\nthe guide, line 9\nthe guide, line 10\n"

// gnNewRepo builds main = base -> head -> merged. The head commit adds a source file and the
// agent manifest, changes a file, renames one, adds a binary file and touches the DevDigest paths
// (.devdigest/ and .gitlab/devdigest/, which are stripped) next to look-alikes (which are kept).
// The repository's own configuration names a diff driver, a text conversion, an external diff and
// an fsmonitor: every one of them writes the marker file if git ever runs it.
func gnNewRepo(t *testing.T) gnRepo {
	t.Helper()
	dir := t.TempDir()
	r := gnRepo{dir: dir, evilMarker: filepath.Join(t.TempDir(), "evil-ran"), evilScript: filepath.Join(t.TempDir(), "evil.sh")}
	gnGit(t, dir, "init", "-q", "--object-format=sha1", "-b", "main")

	gnWrite(t, dir, ".gitattributes", "* diff=evil\n")
	gnWrite(t, dir, "README.md", "# widgets\n")
	gnWrite(t, dir, "src/config.ts", gnConfigBase)
	gnWrite(t, dir, "docs/old.md", gnGuide)
	gnWrite(t, dir, ".devdigest/skills/note.md", "base skill note\n")
	gnWrite(t, dir, ".gitlab/devdigest/other.yml", "base: 1\n")
	gnGit(t, dir, "add", "-A")
	gnGit(t, dir, "commit", "-q", "-m", "base")
	r.base = gnGit(t, dir, "rev-parse", "HEAD")

	gnWrite(t, dir, "src/added.go", "package src\n\nfunc Added() int { return 1 }\n")
	gnWrite(t, dir, "src/config.ts", gnConfigHead)
	gnGit(t, dir, "mv", "docs/old.md", "docs/new.md")
	gnWrite(t, dir, "docs/new.md", strings.Replace(gnGuide, "line 5", "line five", 1))
	if err := os.WriteFile(filepath.Join(dir, "assets-logo.bin"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00binary\x00\xff"), 0o644); err != nil {
		t.Fatal(err)
	}
	gnWrite(t, dir, ".devdigest/agents/"+gnSlug+".yaml", testManifest)
	gnWrite(t, dir, ".devdigest/skills/note.md", "head skill note\n")
	gnWrite(t, dir, ".devdigest/runner/devdigest-runner-linux-amd64.sha256", "00  devdigest-runner\n")
	gnWrite(t, dir, ".gitlab/devdigest/other.yml", "base: 2\n")
	gnWrite(t, dir, ".gitlab/devdigest/review-"+gnSlug+".yml", "job: 1\n")
	// look-alikes: not DevDigest's directories, so they stay in the review
	gnWrite(t, dir, ".devdigest-notes.md", "notes\n")
	gnWrite(t, dir, "src/.devdigest/inner.txt", "inner\n")
	gnWrite(t, dir, ".gitlab/ci/other.yml", "ci: 1\n")
	gnGit(t, dir, "add", "-A")
	gnGit(t, dir, "commit", "-q", "-m", "head")
	r.head = gnGit(t, dir, "rev-parse", "HEAD")

	gnWrite(t, dir, "merged-only.txt", "only in the merge result\n")
	gnGit(t, dir, "add", "-A")
	gnGit(t, dir, "commit", "-q", "-m", "merged")
	r.merged = gnGit(t, dir, "rev-parse", "HEAD")

	// a hostile repository configuration, set after the commits so the fixture builder runs none of it
	if err := os.WriteFile(r.evilScript, []byte("#!/bin/sh\ntouch '"+r.evilMarker+"'\ncat \"$1\" 2>/dev/null\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"diff.evil.command": r.evilScript, "diff.evil.textconv": r.evilScript, "diff.external": r.evilScript,
		"core.fsmonitor": r.evilScript, "core.pager": r.evilScript, "core.hooksPath": filepath.Dir(r.evilScript),
	} {
		gnGit(t, dir, "config", k, v)
	}
	return r
}

// gnShallowClone is a depth-limited clone of the fixture; the commits beyond the depth are absent.
func gnShallowClone(t *testing.T, r gnRepo, depth int) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clone")
	gnGit(t, r.dir, "clone", "-q", "--no-local", "--depth", strconv.Itoa(depth), "file://"+r.dir, dst)
	return dst
}

// gnModelStub answers every chat completion with a review (or, with status, an error whose
// message is errBody) and keeps what it was asked.
type gnModelStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []gnReq
}

// gnReq is one request a stub saw: the method, path, every header line and the body.
type gnReq struct {
	Method, Path, Headers, Body string
	Token                       string // PRIVATE-TOKEN of a GitLab request
}

type gnModelOpts struct {
	findings []map[string]any
	status   int
	errBody  string
}

func newGNModel(t *testing.T, o gnModelOpts) *gnModelStub {
	t.Helper()
	m := &gnModelStub{}
	findings := o.findings
	if findings == nil {
		findings = []map[string]any{}
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.reqs = append(m.reqs, gnReq{Method: r.Method, Path: r.URL.Path, Headers: gnHeaderText(r.Header), Body: string(body)})
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if o.status != 0 {
			w.WriteHeader(o.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": o.errBody, "code": o.status}})
			return
		}
		review, _ := json.Marshal(map[string]any{"verdict": "approve", "summary": "ok", "score": 90, "findings": findings})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "deepseek/deepseek-v4-flash",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(review)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "cost": 0.0025},
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *gnModelStub) requests() []gnReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

func gnHeaderText(h http.Header) string {
	var lines []string
	for k, vs := range h {
		lines = append(lines, k+": "+strings.Join(vs, ","))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// gnFinding is a model finding on the added line of src/config.ts (new line 10).
func gnFinding(severity, rationale string) map[string]any {
	return map[string]any{
		"id": "f1", "severity": severity, "category": "security", "title": "Hardcoded key", "file": "src/config.ts",
		"start_line": 10, "end_line": 10, "rationale": rationale, "confidence": 0.9, "kind": "finding",
	}
}

// gnGitLabStub is a TLS stub of the GitLab API of one merge request. failOn answers a request with
// the given status and body instead of the normal answer.
type gnGitLabStub struct {
	srv    *httptest.Server
	caFile string
	mu     sync.Mutex
	reqs   []gnReq
	draft  int
	head   string
	failOn func(method, path string) (status int, body string, fail bool)
}

func newGNGitLab(t *testing.T, head string) *gnGitLabStub {
	t.Helper()
	g := &gnGitLabStub{head: head}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.reqs = append(g.reqs, gnReq{Method: r.Method, Path: r.URL.Path, Headers: gnHeaderText(r.Header), Body: string(body), Token: r.Header.Get("PRIVATE-TOKEN")})
		g.draft++
		id := g.draft
		failOn := g.failOn
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if failOn != nil {
			if status, text, fail := failOn(r.Method, r.URL.Path); fail {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, text)
				return
			}
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/draft_notes"):
			_, _ = io.WriteString(w, "[]")
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"iid":`+gnIID+`,"diff_refs":{"base_sha":"`+strings.Repeat("a", 40)+`","start_sha":"`+strings.Repeat("a", 40)+`","head_sha":"`+g.head+`"}}`)
		case strings.HasSuffix(r.URL.Path, "/draft_notes"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":`+strconv.Itoa(id)+`}`)
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "{}")
		}
	}))
	t.Cleanup(g.srv.Close)
	g.caFile = filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw})
	if err := os.WriteFile(g.caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *gnGitLabStub) requests() []gnReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

// posted is the body text of every request that sends one.
func (g *gnGitLabStub) posted() string {
	var b strings.Builder
	for _, r := range g.requests() {
		b.WriteString(r.Body + "\n")
	}
	return b.String()
}

// gnJob is one run of the runner process.
type gnJob struct {
	t          *testing.T
	repo       gnRepo
	dir        string // CI_PROJECT_DIR
	env        map[string]string
	model      *gnModelStub
	gl         *gnGitLabStub
	resultPath string
	ep         endpoints
	github     *httptest.Server // fails the test on any request: GitLab mode never talks to GitHub
}

func gnNewJob(t *testing.T, model gnModelOpts) *gnJob {
	t.Helper()
	r := gnNewRepo(t)
	j := &gnJob{t: t, repo: r, dir: r.dir, model: newGNModel(t, model), gl: newGNGitLab(t, r.head)}
	j.resultPath = filepath.Join(t.TempDir(), "devdigest-result.json")
	j.github = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("GitHub request %s %s in GitLab mode", r.Method, r.URL.Path)
		http.Error(w, "no", http.StatusTeapot)
	}))
	t.Cleanup(j.github.Close)
	j.ep = endpoints{OpenRouter: j.model.srv.URL + "/v1", GitHub: j.github.URL}
	j.env = map[string]string{
		"DEVDIGEST_PLATFORM": runnerdomain.GitLabPlatform, "GITLAB_CI": "true", "CI_PIPELINE_SOURCE": "merge_request_event",
		"CI_MERGE_REQUEST_IID": gnIID, "CI_PROJECT_ID": gnProjectID, "CI_MERGE_REQUEST_PROJECT_ID": gnProjectID,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": gnProjectID, "CI_PROJECT_PATH": gnProjectPath, "CI_JOB_ID": gnJobID,
		"CI_MERGE_REQUEST_DIFF_BASE_SHA": r.base, "CI_COMMIT_SHA": r.head, "CI_PROJECT_DIR": r.dir,
		"CI_API_V4_URL": j.gl.srv.URL + "/api/v4", "CI_SERVER_TLS_CA_FILE": j.gl.caFile,
		"CI_MERGE_REQUEST_TITLE": gnTitle, "CI_MERGE_REQUEST_DESCRIPTION": gnDescription,
		"DEVDIGEST_DIR": filepath.Join(r.dir, ".devdigest"), "DEVDIGEST_RESULT_PATH": j.resultPath,
		"DEVDIGEST_AGENT": gnSlug, "DEVDIGEST_POST_AS": runnerdomain.PostAsReview,
		"DEVDIGEST_GITLAB_TOKEN": gnToken, "OPENROUTER_API_KEY": gnProviderKy,
	}
	return j
}

// gnResult is what one run left behind.
type gnResult struct {
	code           int
	stdout, stderr string
	artifact       string
	hasArtifact    bool
	gitLog         string // one line per git invocation of the runner, its arguments
	gl, model      []gnReq
}

// gitSubcommands are the git subcommands the runner ran, in order.
func (r gnResult) gitSubcommands() []string {
	var out []string
	for line := range strings.SplitSeq(strings.TrimSpace(r.gitLog), "\n") {
		args := strings.Fields(line)
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" {
				i++
				continue
			}
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			out = append(out, args[i])
			break
		}
	}
	return out
}

// run executes the process once with a git wrapper in front of PATH that records every git call.
func (j *gnJob) run() gnResult {
	t := j.t
	t.Helper()
	wrapper := t.TempDir()
	gitLog := filepath.Join(t.TempDir(), "git.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + gitLog + "'\nexec '" + gnRealGit() + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+gnOrigPath())
	t.Setenv("GIT_EXTERNAL_DIFF", j.repo.evilScript)

	var stdout, stderr bytes.Buffer
	code := runProcess(t.Context(), envOf(j.env), &stdout, &stderr, j.ep)
	res := gnResult{code: code, stdout: stdout.String(), stderr: stderr.String(), gl: j.gl.requests(), model: j.model.requests()}
	if b, err := os.ReadFile(j.resultPath); err == nil {
		res.artifact, res.hasArtifact = string(b), true
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(gitLog); err == nil {
		res.gitLog = string(b)
	}
	if _, err := os.Stat(j.repo.evilMarker); err == nil {
		t.Errorf("git ran a program the repository configures (%s exists)", j.repo.evilMarker)
	}
	return res
}

// gnAssertNoMarker fails when text holds marker or any 20-character part of it.
func gnAssertNoMarker(t *testing.T, where, text, marker string) {
	t.Helper()
	if strings.Contains(text, marker) {
		t.Errorf("%s contains %q:\n%s", where, marker, text)
		return
	}
	for i := 0; i+20 <= len(marker); i++ {
		if part := marker[i : i+20]; strings.Contains(text, part) {
			t.Errorf("%s contains the 20-character part %q of %q:\n%s", where, part, marker, text)
			return
		}
	}
}

// gnFailsEarly checks a run that must fail before the diff, the model or GitLab are touched.
func gnFailsEarly(t *testing.T, res gnResult, wantInStderr string) {
	t.Helper()
	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nstderr: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, wantInStderr) {
		t.Errorf("stderr = %q, want it to contain %q", res.stderr, wantInStderr)
	}
	if res.hasArtifact {
		t.Errorf("artifact written, want none:\n%s", res.artifact)
	}
	if len(res.model) != 0 {
		t.Errorf("model requests = %d, want none", len(res.model))
	}
	if len(res.gl) != 0 {
		t.Errorf("GitLab requests = %+v, want none", res.gl)
	}
	if strings.TrimSpace(res.gitLog) != "" {
		t.Errorf("git was run, want no git command: %q", res.gitLog)
	}
}

// AC17, first half: without DEVDIGEST_PLATFORM=gitlab_ci the runner is the GitHub runner, whatever
// else GitLab-like is in the environment (the byte-identical goldens are TestParity_*).
func TestGitLabEnv_OtherPlatformValuesStayGitHub(t *testing.T) {
	for _, platform := range []map[string]string{
		nil,
		{"DEVDIGEST_PLATFORM": ""},
		{"DEVDIGEST_PLATFORM": "gitlab"},
		{"DEVDIGEST_PLATFORM": "GITLAB_CI"},
		{"DEVDIGEST_PLATFORM": "github"},
	} {
		name := "unset"
		if platform != nil {
			name = "DEVDIGEST_PLATFORM=" + platform["DEVDIGEST_PLATFORM"]
		}
		t.Run(name, func(t *testing.T) {
			env := map[string]string{
				"GITLAB_CI": "true", "CI_PIPELINE_SOURCE": "merge_request_event", "CI_MERGE_REQUEST_IID": "7",
				"DEVDIGEST_GITLAB_TOKEN": gnToken,
			}
			for k, v := range platform {
				env[k] = v
			}
			w := newWorkspace(t, testManifest, env)
			gh := gitHubOK(t)
			or := openRouterReply(t, nil)
			var stdout, stderr bytes.Buffer
			code := runProcess(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})
			if code != 0 {
				t.Fatalf("runProcess() = %d, want 0\nstderr: %s", code, stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), "::stop-commands::") {
				t.Errorf("stdout = %q, want the GitHub stop-commands bracket", stdout.String())
			}
			reqs := gh.requests()
			if len(reqs) != 2 || reqs[0].Method != http.MethodGet || reqs[1].Method != http.MethodPost {
				t.Errorf("GitHub requests = %+v, want the diff GET and the review POST", reqs)
			}
			if !strings.Contains(stdout.String(), `"repository": "acme/widgets"`) && !strings.Contains(stdout.String(), "posted=github_review") {
				t.Errorf("stdout = %q, want the GitHub result line", stdout.String())
			}
		})
	}
}

// AC17, second half (EC16): gitlab_ci outside a merge request pipeline is a hard failure.
func TestGitLabEnv_NotAMergeRequestPipeline(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]string
		del  []string
	}{
		{name: "GITLAB_CI unset", del: []string{"GITLAB_CI"}},
		{name: "GITLAB_CI false", set: map[string]string{"GITLAB_CI": "false"}},
		{name: "branch pipeline", set: map[string]string{"CI_PIPELINE_SOURCE": "push"}},
		{name: "scheduled pipeline", set: map[string]string{"CI_PIPELINE_SOURCE": "schedule"}},
		{name: "pipeline source unset", del: []string{"CI_PIPELINE_SOURCE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			for k, v := range tc.set {
				j.env[k] = v
			}
			for _, k := range tc.del {
				delete(j.env, k)
			}
			res := j.run()
			gnFailsEarly(t, res, "This job must run in a GitLab merge request pipeline")
		})
	}
}

// AC18, first half: an invalid required variable fails naming it, before any model call.
func TestGitLabEnv_InvalidVariablesNameTheVariable(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"iid zero", "CI_MERGE_REQUEST_IID", "0"},
		{"iid text", "CI_MERGE_REQUEST_IID", "x"},
		{"iid negative", "CI_MERGE_REQUEST_IID", "-3"},
		{"iid empty", "CI_MERGE_REQUEST_IID", ""},
		{"project id text", "CI_PROJECT_ID", "forty-two"},
		{"base sha short", "CI_MERGE_REQUEST_DIFF_BASE_SHA", "abc"},
		{"base sha upper case", "CI_MERGE_REQUEST_DIFF_BASE_SHA", strings.Repeat("A", 40)},
		{"base sha an option", "CI_MERGE_REQUEST_DIFF_BASE_SHA", "--output=/tmp/x" + strings.Repeat("a", 30)},
		{"commit sha invalid", "CI_COMMIT_SHA", "HEAD"},
		{"source branch sha invalid", "CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "main"},
		{"job id zero", "CI_JOB_ID", "0"},
		{"project path empty", "CI_PROJECT_PATH", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			j.env[tc.key] = tc.value
			res := j.run()
			gnFailsEarly(t, res, tc.key+" is missing or invalid")
			if tc.value != "" && strings.Contains(res.stderr, tc.value) {
				t.Errorf("stderr = %q, must not echo the invalid value %q", res.stderr, tc.value)
			}
		})
	}
}

// AC18, second half (NFR2): the runner reads only the variables of its list; a GITHUB_TOKEN, a
// CI_JOB_TOKEN and an unrelated secret in the environment reach no request, no line, no artifact.
func TestGitLabEnv_ForeignVariablesAreNeverRead(t *testing.T) {
	for _, postAs := range []string{runnerdomain.PostAsReview, runnerdomain.PostAsComment, runnerdomain.PostAsNone} {
		t.Run(postAs, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{findings: []map[string]any{gnFinding("WARNING", "A live key is committed.")}})
			j.env["DEVDIGEST_POST_AS"] = postAs
			j.env["GITHUB_TOKEN"] = gnGitHubTok
			j.env["CI_JOB_TOKEN"] = gnJobTok
			j.env["MY_SECRET"] = gnOtherVal
			j.env["GITHUB_REPOSITORY"] = "evil/repo"
			res := j.run()
			if res.code != 0 {
				t.Fatalf("exit code = %d, want 0 (a warning does not trigger the gate)\nstderr: %s", res.code, res.stderr)
			}
			var all strings.Builder
			all.WriteString(res.stdout + "\n" + res.stderr + "\n" + res.artifact + "\n")
			for _, r := range append(slices.Clone(res.gl), res.model...) {
				all.WriteString(r.Method + " " + r.Path + "\n" + r.Headers + "\n" + r.Body + "\n")
			}
			for _, marker := range []string{gnGitHubTok, gnJobTok, gnOtherVal, "evil/repo"} {
				if strings.Contains(all.String(), marker) {
					t.Errorf("%q reached a request, a line or the artifact:\n%s", marker, all.String())
				}
			}
			for _, r := range res.gl {
				if r.Token != gnToken {
					t.Errorf("GitLab %s %s PRIVATE-TOKEN = %q, want DEVDIGEST_GITLAB_TOKEN", r.Method, r.Path, r.Token)
				}
			}
		})
	}
}

// AC19 (FR18, EC17): a merge request from a fork is not reviewed: no git command, no model call,
// no artifact, no GitLab request.
func TestGitLabEnv_ForkMergeRequestsAreNotReviewed(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]string
	}{
		{"source project differs", map[string]string{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "43"}},
		{"pipeline project differs", map[string]string{"CI_PROJECT_ID": "43"}},
		{"merge request project differs", map[string]string{"CI_MERGE_REQUEST_PROJECT_ID": "43"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			for k, v := range tc.set {
				j.env[k] = v
			}
			res := j.run()
			gnFailsEarly(t, res, "Merge requests from forks are not reviewed")
		})
	}
}

// AC20, first part (FR19, EC18): posting needs the token, and the message tells how to give it.
func TestGitLabEnv_MissingTokenFailsBeforeTheDiff(t *testing.T) {
	for _, postAs := range []string{runnerdomain.PostAsReview, runnerdomain.PostAsComment, "", "anything"} {
		for _, tc := range []struct {
			name string
			set  func(env map[string]string)
		}{
			{"unset", func(env map[string]string) { delete(env, "DEVDIGEST_GITLAB_TOKEN") }},
			{"empty", func(env map[string]string) { env["DEVDIGEST_GITLAB_TOKEN"] = "" }},
			{"blank", func(env map[string]string) { env["DEVDIGEST_GITLAB_TOKEN"] = "  \t" }},
		} {
			t.Run("post_as="+postAs+"/"+tc.name, func(t *testing.T) {
				j := gnNewJob(t, gnModelOpts{})
				j.env["DEVDIGEST_POST_AS"] = postAs
				tc.set(j.env)
				res := j.run()
				gnFailsEarly(t, res, gnTokenMissingMsg)
			})
		}
	}
}

// AC20, second part (FR19): CI_API_V4_URL must be an https URL without credentials, query or
// fragment; the failure names the variable and does not echo credentials.
func TestGitLabEnv_BadAPIURLFailsNamingTheVariable(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"http", "http://gitlab.local/api/v4"},
		{"userinfo", "https://user:s3cretpw@gitlab.local/api/v4"},
		{"query", "https://gitlab.local/api/v4?private_token=s3cretpw"},
		{"fragment", "https://gitlab.local/api/v4#s3cretpw"},
		{"no host", "https:///api/v4"},
		{"unset", ""},
		{"not a URL", "gitlab.local/api/v4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			if tc.url == "" {
				delete(j.env, "CI_API_V4_URL")
			} else {
				j.env["CI_API_V4_URL"] = tc.url
			}
			res := j.run()
			gnFailsEarly(t, res, "CI_API_V4_URL")
			if strings.Contains(res.stdout+res.stderr, "s3cretpw") {
				t.Errorf("the job log echoes the URL credentials: %q %q", res.stdout, res.stderr)
			}
		})
	}
}

// AC20, third part (FR19): post_as none reads no token and no API URL and makes no GitLab request;
// the review runs and the artifact is written.
func TestGitLabEnv_PostAsNoneReadsNoTokenAndCallsNoGitLab(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]string
		del  []string
	}{
		{name: "no token", del: []string{"DEVDIGEST_GITLAB_TOKEN"}},
		{name: "token and a bad API URL are ignored", set: map[string]string{"CI_API_V4_URL": "http://not-https/api/v4"}},
		{name: "no API URL", del: []string{"CI_API_V4_URL", "CI_SERVER_TLS_CA_FILE"}},
		{name: "a CA file that does not exist", set: map[string]string{"CI_SERVER_TLS_CA_FILE": "/nonexistent/ca.pem"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			j.env["DEVDIGEST_POST_AS"] = runnerdomain.PostAsNone
			for k, v := range tc.set {
				j.env[k] = v
			}
			for _, k := range tc.del {
				delete(j.env, k)
			}
			res := j.run()
			if res.code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", res.code, res.stderr)
			}
			if !res.hasArtifact {
				t.Error("no artifact written")
			}
			if len(res.gl) != 0 {
				t.Errorf("GitLab requests = %+v, want none", res.gl)
			}
			if len(res.model) != 1 {
				t.Errorf("model requests = %d, want 1", len(res.model))
			}
			if want := "[agent-runner] findings=0 blockers=0 gateTriggered=false posted=none"; !strings.HasSuffix(strings.TrimSpace(res.stdout), want) {
				t.Errorf("stdout = %q, want it to end with %q", res.stdout, want)
			}
			gnAssertNoMarker(t, "the job log", res.stdout+res.stderr, gnToken)
		})
	}
}

// AC21 (FR20, NFR10): the reviewed diff is the checkout's own base-to-head diff without
// DevDigest's sections, computed with git and no network request, and a repository's diff driver,
// text conversion, external diff or fsmonitor runs no program.
func TestGitLabDiff_IsTheCheckoutDiffWithoutDevDigestSections(t *testing.T) {
	j := gnNewJob(t, gnModelOpts{})
	j.env["DEVDIGEST_POST_AS"] = runnerdomain.PostAsNone
	res := j.run()
	if res.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", res.code, res.stderr)
	}
	if len(res.model) != 1 {
		t.Fatalf("model requests = %d, want 1", len(res.model))
	}
	prompt := res.model[0].Body

	// the reviewed paths: the added, modified, renamed and binary file and the look-alikes
	for _, want := range []string{
		"src/added.go", "src/config.ts", "docs/new.md", "assets-logo.bin",
		".devdigest-notes.md", "src/.devdigest/inner.txt", ".gitlab/ci/other.yml",
		"+  apiKey: 'sk_live_abcdef123456',",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the review prompt lacks %q", want)
		}
	}
	// DevDigest's own sections are not reviewed
	for _, banned := range []string{
		".devdigest/agents", ".devdigest/skills/note.md", ".devdigest/runner", ".gitlab/devdigest/",
		"head skill note", "job: 1", "merged-only.txt",
	} {
		if strings.Contains(prompt, banned) {
			t.Errorf("the review prompt holds %q, want the DevDigest sections stripped", banned)
		}
	}

	// git only read the checkout: no fetch, no network, no write
	for _, sub := range res.gitSubcommands() {
		if sub != "rev-parse" && sub != "diff" {
			t.Errorf("git subcommands = %v, want only rev-parse and diff", res.gitSubcommands())
			break
		}
	}
	if !slices.Contains(res.gitSubcommands(), "diff") {
		t.Errorf("git subcommands = %v, want a diff", res.gitSubcommands())
	}
	if len(res.gl) != 0 {
		t.Errorf("GitLab requests = %+v, want none for the diff", res.gl)
	}
	for _, line := range strings.Split(strings.TrimSpace(res.gitLog), "\n") {
		if !strings.Contains(line, "--no-ext-diff") && strings.Contains(line, " diff ") {
			t.Errorf("git diff ran without --no-ext-diff: %q", line)
		}
	}
}

// AC21: the runner's parse of the stripped diff is the Phase 6 rule: the expected diff, computed
// with the same git on the fixture, minus the two DevDigest directories, parses to the files the
// review sees.
func TestGitLabDiff_ParseEqualsThePhase6Rule(t *testing.T) {
	r := gnNewRepo(t)
	raw := gnGit(t, r.dir, "-c", "diff.evil.command=", "-c", "diff.external=", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "-M",
		"--src-prefix=a/", "--dst-prefix=b/", "-U3", "--inter-hunk-context=0", r.base, r.head, "--") + "\n"
	stripped := runnerdomain.StripGitLabIgnored(raw)
	parsed := runnerdomain.ParseDiff(stripped)
	var paths []string
	for _, f := range parsed.Files {
		paths = append(paths, f.Path)
	}
	// a binary file has no hunk, so the Phase 6 parse has no entry for it; the section itself stays
	want := []string{".devdigest-notes.md", ".gitlab/ci/other.yml", "docs/new.md", "src/.devdigest/inner.txt", "src/added.go", "src/config.ts"}
	slices.Sort(paths)
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("ParseDiff(StripGitLabIgnored(git diff)) paths mismatch (-want +got):\n%s", diff)
	}
	for _, banned := range []string{"diff --git a/.devdigest/", "diff --git a/.gitlab/devdigest/"} {
		if strings.Contains(stripped, banned) {
			t.Errorf("stripped diff still has a %q section", banned)
		}
	}
	if !strings.Contains(stripped, "diff --git a/assets-logo.bin b/assets-logo.bin") || !strings.Contains(stripped, "Binary files") {
		t.Errorf("the stripped diff lost the binary file section:\n%s", stripped)
	}
	if !strings.Contains(raw, "rename from docs/old.md") || !strings.Contains(raw, "Binary files") {
		t.Errorf("the fixture diff lacks the rename or the binary file:\n%s", raw)
	}
}

// AC21 (EC20): with CI_MERGE_REQUEST_SOURCE_BRANCH_SHA set, that commit is the head of the diff
// and the artifact's commit_sha stays CI_COMMIT_SHA; without it the head is CI_COMMIT_SHA.
func TestGitLabDiff_MergedResultsPipelineUsesTheSourceBranchHead(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sourceBranch bool
		wantMerged   bool
	}{
		{"merged results pipeline", true, false},
		{"plain merge request pipeline", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			j.env["DEVDIGEST_POST_AS"] = runnerdomain.PostAsNone
			j.env["CI_COMMIT_SHA"] = j.repo.merged
			if tc.sourceBranch {
				j.env["CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"] = j.repo.head
			}
			res := j.run()
			if res.code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", res.code, res.stderr)
			}
			if got := strings.Contains(res.model[0].Body, "merged-only.txt"); got != tc.wantMerged {
				t.Errorf("the prompt holds merged-only.txt = %t, want %t", got, tc.wantMerged)
			}
			var art map[string]any
			if err := json.Unmarshal([]byte(res.artifact), &art); err != nil {
				t.Fatalf("artifact is not JSON: %v\n%s", err, res.artifact)
			}
			if art["commit_sha"] != j.repo.merged {
				t.Errorf("artifact commit_sha = %v, want CI_COMMIT_SHA %s", art["commit_sha"], j.repo.merged)
			}
			if want := "commit=" + j.repo.merged; !strings.Contains(res.stdout, want) {
				t.Errorf("stdout = %q, want the trace line with %s", res.stdout, want)
			}
		})
	}
}

// AC21 (FR20, EC19): a commit missing from the checkout is a hard failure with the documented
// message, no artifact, no model call.
func TestGitLabDiff_CommitMissingFromTheClone(t *testing.T) {
	t.Run("base commit missing from a shallow clone", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{})
		j.dir = gnShallowClone(t, j.repo, 1)
		j.env["CI_PROJECT_DIR"] = j.dir
		j.env["DEVDIGEST_DIR"] = filepath.Join(j.dir, ".devdigest")
		j.env["CI_COMMIT_SHA"] = j.repo.merged
		res := j.run()
		want := "Base commit " + j.repo.base + " is not in the job's clone; keep GIT_DEPTH: \"0\" in the job"
		if res.code != 1 || !strings.Contains(res.stderr, "[agent-runner] FAILED: "+want) {
			t.Errorf("exit %d, stderr %q; want exit 1 and the FAILED line %q", res.code, res.stderr, want)
		}
		if res.hasArtifact || len(res.model) != 0 || len(res.gl) != 0 {
			t.Errorf("artifact %t, model requests %d, GitLab requests %d; want none of them", res.hasArtifact, len(res.model), len(res.gl))
		}
	})
	t.Run("head commit missing", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{})
		missing := strings.Repeat("1", 40)
		j.env["CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"] = missing
		res := j.run()
		want := "Head commit " + missing + " is not in the job's clone; keep GIT_DEPTH: \"0\" in the job"
		if res.code != 1 || !strings.Contains(res.stderr, want) {
			t.Errorf("exit %d, stderr %q; want exit 1 and %q", res.code, res.stderr, want)
		}
		if res.hasArtifact || len(res.model) != 0 || len(res.gl) != 0 {
			t.Errorf("artifact %t, model requests %d, GitLab requests %d; want none of them", res.hasArtifact, len(res.model), len(res.gl))
		}
	})
}

// artifactKeys is the top-level keys of a JSON object in document order.
func gnArtifactKeys(t *testing.T, artifact string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(artifact))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("artifact does not start with an object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// gnGitHubEquivalent runs the GitHub runner on the same diff, manifest, task and description and
// returns the model requests and the artifact it produced.
func gnGitHubEquivalent(t *testing.T, manifest, diffText string, env map[string]string, ep endpoints) (artifact string) {
	t.Helper()
	w := newWorkspace(t, manifest, env)
	delete(w.env, "OPENROUTER_API_KEY")
	for k, v := range env {
		w.env[k] = v
	}
	event, _ := json.Marshal(map[string]any{"pull_request": map[string]any{"number": 7, "title": gnTitle, "body": gnDescription}})
	mustWrite(t, filepath.Join(w.root, "event.json"), string(event))
	gh := newGitHubStub(t, func(method, _ string, _ int) stubResp {
		if method == http.MethodGet {
			return stubResp{Status: 200, Text: diffText}
		}
		return stubResp{Status: 200, Text: "{}"}
	})
	ep.GitHub = gh.srv.URL
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), w.lookup, &stdout, &stderr, ep); code != 0 {
		t.Fatalf("GitHub runner exit code = %d\nstderr: %s", code, stderr.String())
	}
	art, ok := w.artifact(t)
	if !ok {
		t.Fatal("GitHub runner wrote no artifact")
	}
	return art
}

// AC22 (FR21, FR22): per provider the model request of the GitLab runner equals the GitHub
// runner's for the same diff, with the task "Review PR #<iid>: <title>" and the description; the
// artifact has the v2 keys in the GitHub order, the GitLab identity and parses with the ingest parser.
func TestGitLabRun_ModelRequestsAndArtifactMatchTheGitHubRunner(t *testing.T) {
	type provider struct {
		name     string
		manifest string
		env      map[string]string
		gateway  *cpGateway
		ep       func(g *cpGateway) endpoints
	}
	providers := []provider{
		{name: "openrouter", manifest: testManifest},
		{
			name: "openai", manifest: cpManifest("openai", "gpt-4.1", "pricing:\n  promptPerM: 2\n  completionPerM: 8\n"),
			env: map[string]string{"OPENAI_API_KEY": cpOpenAIKey}, gateway: newCPGateway(t, cpGatewayOpts{protocol: "openai", in: 100, out: 50}),
			ep: func(g *cpGateway) endpoints { return endpoints{OpenAI: g.srv.URL + "/v1"} },
		},
		{
			name: "anthropic", manifest: cpManifest("anthropic", "claude-sonnet", "pricing:\n  promptPerM: 3\n  completionPerM: 15\n"),
			env: map[string]string{"ANTHROPIC_API_KEY": cpAnthropic}, gateway: newCPGateway(t, cpGatewayOpts{protocol: "anthropic", in: 100, out: 50}),
			ep: func(g *cpGateway) endpoints { return endpoints{Anthropic: g.srv.URL + "/v1"} },
		},
		{
			name: "custom", manifest: cpManifest("custom:my-gw", "m1", cpCustomBlock("openai", "bearer", "", "strict")),
			gateway: newCPGateway(t, cpGatewayOpts{protocol: "openai", in: 100, out: 50}),
		},
	}
	providers[3].env = cpCustomEnv(providers[3].gateway.srv.URL + "/v1")
	providers[3].ep = func(*cpGateway) endpoints { return defaultEndpoints() }

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			j.env["DEVDIGEST_POST_AS"] = runnerdomain.PostAsNone
			gnWrite(t, j.dir, ".devdigest/agents/"+gnSlug+".yaml", p.manifest)
			delete(j.env, "OPENROUTER_API_KEY")
			for k, v := range p.env {
				j.env[k] = v
			}
			if p.name == "openrouter" {
				j.env["OPENROUTER_API_KEY"] = gnProviderKy
			}
			if p.ep != nil {
				ep := p.ep(p.gateway)
				ep.GitHub = j.github.URL
				j.ep = ep
			}
			res := j.run()
			if res.code != 0 {
				t.Fatalf("GitLab runner exit code = %d\nstderr: %s", res.code, res.stderr)
			}
			var glRaw []string
			if p.gateway != nil {
				for _, r := range p.gateway.requests() {
					glRaw = append(glRaw, r.Raw)
				}
			} else {
				for _, r := range res.model {
					glRaw = append(glRaw, r.Body)
				}
			}
			if len(glRaw) != 1 {
				t.Fatalf("model requests = %d, want 1", len(glRaw))
			}

			// the GitHub runner reviews the same diff, fetched from its stub; the DevDigest sections
			// are cut the way each platform's rule cuts them, which leaves the same text
			diffText := runnerdomain.StripGitLabIgnored(gnGit(t, j.dir, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "-M",
				"--src-prefix=a/", "--dst-prefix=b/", "-U3", "--inter-hunk-context=0", j.repo.base, j.repo.head, "--") + "\n")
			ghEnv := maps(p.env)
			var ghEp endpoints
			if p.ep != nil {
				ghEp = p.ep(p.gateway)
			} else {
				ghEp = endpoints{OpenRouter: j.model.srv.URL + "/v1"}
				ghEnv["OPENROUTER_API_KEY"] = gnProviderKy
			}
			before := 0
			if p.gateway != nil {
				before = len(p.gateway.requests())
			} else {
				before = len(j.model.requests())
			}
			ghArt := gnGitHubEquivalent(t, p.manifest, diffText, ghEnv, ghEp)
			var ghRaw []string
			if p.gateway != nil {
				for _, r := range p.gateway.requests()[before:] {
					ghRaw = append(ghRaw, r.Raw)
				}
			} else {
				for _, r := range j.model.requests()[before:] {
					ghRaw = append(ghRaw, r.Body)
				}
			}
			if len(ghRaw) != 1 {
				t.Fatalf("GitHub runner model requests = %d, want 1", len(ghRaw))
			}
			var glBody, ghBody any
			if err := json.Unmarshal([]byte(glRaw[0]), &glBody); err != nil {
				t.Fatalf("GitLab model request is not JSON: %v", err)
			}
			if err := json.Unmarshal([]byte(ghRaw[0]), &ghBody); err != nil {
				t.Fatalf("GitHub model request is not JSON: %v", err)
			}
			if diff := cmp.Diff(ghBody, glBody); diff != "" {
				t.Errorf("model request of the GitLab runner differs from the GitHub runner's (-github +gitlab):\n%s", diff)
			}
			if want := strings.Trim(gnJSON(t, "Review PR #7: "+gnTitle), `"`); !strings.Contains(glRaw[0], want) {
				t.Errorf("model request lacks the task %q", want)
			}
			if want := "A cool feature."; !strings.Contains(glRaw[0], want) {
				t.Errorf("model request lacks the description section %q", want)
			}

			// the artifact: v2 keys in the GitHub order, the GitLab identity
			if diff := cmp.Diff(gnArtifactKeys(t, ghArt), gnArtifactKeys(t, res.artifact)); diff != "" {
				t.Errorf("artifact keys differ from the GitHub runner's (-github +gitlab):\n%s", diff)
			}
			parsed, err := cidomain.ParseResultArtifact([]byte(res.artifact))
			if err != nil {
				t.Fatalf("ParseResultArtifact() error = %v\n%s", err, res.artifact)
			}
			if parsed.PRNumber == nil || *parsed.PRNumber != 7 || parsed.Repository == nil || *parsed.Repository != gnProjectPath ||
				parsed.Version == nil || *parsed.Version != runnerdomain.RunnerVersion {
				t.Errorf("artifact = %+v, want pr_number 7, repository %s, version %s", parsed, gnProjectPath, runnerdomain.RunnerVersion)
			}
			if parsed.RepositoryID == nil || *parsed.RepositoryID != 42 || parsed.RunID == nil || *parsed.RunID != 9001 ||
				parsed.RunAttempt == nil || *parsed.RunAttempt != 1 || parsed.CommitSHA == nil || *parsed.CommitSHA != j.repo.head {
				t.Errorf("artifact identity = repository_id %v run_id %v run_attempt %v commit_sha %v, want 42, 9001, 1, %s",
					parsed.RepositoryID, parsed.RunID, parsed.RunAttempt, parsed.CommitSHA, j.repo.head)
			}
		})
	}
}

func gnJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// gnPostPaths are the GitLab requests of a run as "METHOD suffix" lines, relative to the merge
// request.
func gnPostPaths(reqs []gnReq) []string {
	var out []string
	for _, r := range reqs {
		out = append(out, r.Method+" "+strings.TrimPrefix(r.Path, "/api/v4/projects/"+gnProjectID))
	}
	return out
}

// AC23 (FR23): the gate fails the job after the artifact is written and the review is posted; a
// model failure writes no artifact and sends nothing to GitLab; a posting failure keeps the artifact.
func TestGitLabRun_ExitCodes(t *testing.T) {
	t.Run("gate triggers: artifact, posting, exit 1", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{findings: []map[string]any{gnFinding("CRITICAL", "A live key is committed.")}})
		res := j.run()
		if res.code != 1 {
			t.Errorf("exit code = %d, want 1\nstderr: %s", res.code, res.stderr)
		}
		if !res.hasArtifact || !strings.Contains(res.artifact, `"blockers": 1`) {
			t.Errorf("artifact = %q, %t; want it written with one blocker", res.artifact, res.hasArtifact)
		}
		want := []string{
			"GET /merge_requests/" + gnIID, "GET /merge_requests/" + gnIID + "/draft_notes",
			"POST /merge_requests/" + gnIID + "/draft_notes", "POST /merge_requests/" + gnIID + "/draft_notes",
			"POST /merge_requests/" + gnIID + "/draft_notes/bulk_publish",
		}
		if diff := cmp.Diff(want, gnPostPaths(res.gl)); diff != "" {
			t.Errorf("GitLab requests mismatch (-want +got):\n%s", diff)
		}
		if want := "[agent-runner] findings=1 blockers=1 gateTriggered=true posted=github_review"; !strings.HasSuffix(strings.TrimSpace(res.stdout), want) {
			t.Errorf("stdout = %q, want it to end with %q", res.stdout, want)
		}
	})
	t.Run("gate triggers with post_as none", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{findings: []map[string]any{gnFinding("CRITICAL", "A live key is committed.")}})
		j.env["DEVDIGEST_POST_AS"] = runnerdomain.PostAsNone
		res := j.run()
		if res.code != 1 || !res.hasArtifact || len(res.gl) != 0 {
			t.Errorf("exit %d, artifact %t, GitLab requests %d; want 1, true, 0", res.code, res.hasArtifact, len(res.gl))
		}
	})
	t.Run("model failure: no artifact, no GitLab request", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{status: http.StatusUnauthorized, errBody: "invalid api key"})
		res := j.run()
		if res.code != 1 {
			t.Errorf("exit code = %d, want 1", res.code)
		}
		if res.hasArtifact {
			t.Errorf("artifact written, want none:\n%s", res.artifact)
		}
		if len(res.gl) != 0 {
			t.Errorf("GitLab requests = %+v, want none", res.gl)
		}
		if len(res.model) == 0 || !strings.Contains(res.stderr, "[agent-runner] FAILED:") {
			t.Errorf("model requests %d, stderr %q; want the model asked and a FAILED line", len(res.model), res.stderr)
		}
	})
	for _, tc := range []struct{ name, postAs, suffix string }{
		{"MR review draft", runnerdomain.PostAsReview, "/draft_notes"},
		{"MR comment", runnerdomain.PostAsComment, "/notes"},
	} {
		t.Run("posting failure keeps the artifact: "+tc.name, func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			j.env["DEVDIGEST_POST_AS"] = tc.postAs
			j.gl.failOn = func(method, path string) (int, string, bool) {
				return http.StatusInternalServerError, "boom", method == http.MethodPost && strings.HasSuffix(path, tc.suffix)
			}
			res := j.run()
			if res.code != 1 {
				t.Errorf("exit code = %d, want 1\nstderr: %s", res.code, res.stderr)
			}
			if !res.hasArtifact {
				t.Error("no artifact, want it kept after a posting failure")
			}
			if !strings.Contains(res.stderr, "GitLab API error posting MR") || !strings.Contains(res.stderr, "500 boom") {
				t.Errorf("stderr = %q, want the GitLab API error line with the status and body", res.stderr)
			}
		})
	}
}

// gnAssertSafeLog fails when text holds a character GitLab's log would act on: ESC, a C0 control
// other than the newline between lines, DEL, a C1 control, or a section marker.
func gnAssertSafeLog(t *testing.T, where, text string) {
	t.Helper()
	for i, r := range text {
		switch {
		case r == '\n':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Errorf("%s holds the control character %U at byte %d: %q", where, r, i, text)
			return
		}
	}
	for _, marker := range []string{"section_start:", "section_end:"} {
		if strings.Contains(text, marker) {
			t.Errorf("%s holds %q: %q", where, marker, text)
		}
	}
}

const gnHostile = "\x1b[31mred\x1b[0m\rsection_start:1:x\r\x1b[0Ksection_end:1:x bad\nsecond line \u0085   \x00 \x7f tab\there ##[error]x ::stop-commands::abc"

// FR29 (EC25): in GitLab mode no line is wrapped in ::stop-commands::, and text from outside is
// neutralised: a GitLab error body, a model error body and a manifest value with escape sequences
// and section markers reach the log without them, on one line of at most 1,200 bytes.
func TestGitLabLog_NoStopCommandsAndHostileTextIsNeutralised(t *testing.T) {
	t.Run("success prints no stop-commands", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{findings: []map[string]any{gnFinding("WARNING", "x")}})
		res := j.run()
		if res.code != 0 {
			t.Fatalf("exit code = %d\nstderr: %s", res.code, res.stderr)
		}
		for _, line := range strings.Split(res.stdout+res.stderr, "\n") {
			if strings.HasPrefix(line, "::") {
				t.Errorf("GitLab log line %q starts like a GitHub workflow command", line)
			}
		}
		if strings.Contains(res.stdout+res.stderr, "stop-commands") {
			t.Errorf("the log mentions stop-commands: %q %q", res.stdout, res.stderr)
		}
	})
	t.Run("GitLab error body", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{})
		j.gl.failOn = func(method, path string) (int, string, bool) {
			return http.StatusUnprocessableEntity, gnHostile + strings.Repeat("y", 3000), method == http.MethodPost
		}
		res := j.run()
		line := strings.TrimSuffix(res.stderr, "\n")
		if res.code != 1 || strings.Contains(line, "\n") || !strings.HasPrefix(line, "[agent-runner] FAILED: GitLab API error posting MR review") {
			t.Fatalf("exit %d, stderr %q; want exit 1 and one GitLab API error FAILED line", res.code, res.stderr)
		}
		if len(line) > 1200 {
			t.Errorf("failure line = %d bytes, want at most 1,200", len(line))
		}
		gnAssertSafeLog(t, "stderr", res.stderr)
		gnAssertSafeLog(t, "stdout", res.stdout)
		if strings.Contains(line, "\n") || !strings.Contains(line, "↵") {
			t.Errorf("failure line = %q, want the line break shown as ↵", line)
		}
	})
	t.Run("model error body", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{status: http.StatusBadRequest, errBody: gnHostile + strings.Repeat("y", 3000)})
		res := j.run()
		if res.code != 1 || res.hasArtifact {
			t.Fatalf("exit %d, artifact %t; want exit 1 and none", res.code, res.hasArtifact)
		}
		gnAssertSafeLog(t, "stderr", res.stderr)
		gnAssertSafeLog(t, "stdout", res.stdout)
		for _, line := range strings.Split(strings.TrimSpace(res.stderr), "\n") {
			if strings.HasPrefix(line, "[agent-runner] FAILED:") && len(line) > 1200 {
				t.Errorf("failure line = %d bytes, want at most 1,200", len(line))
			}
		}
	})
	t.Run("manifest value", func(t *testing.T) {
		j := gnNewJob(t, gnModelOpts{})
		j.env["DEVDIGEST_POST_AS"] = runnerdomain.PostAsNone
		model := `"deepseek/m\e[31m\rsection_start:1:x\r\e[0Ksection_end:1:x x"`
		gnWrite(t, j.dir, ".devdigest/agents/"+gnSlug+".yaml", strings.Replace(testManifest, `model: "deepseek/deepseek-v4-flash"`, "model: "+model, 1))
		res := j.run()
		if res.code != 0 {
			t.Fatalf("exit code = %d\nstderr: %s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "[agent-runner] trace manifest_version=") {
			t.Fatalf("stdout = %q, want the trace line", res.stdout)
		}
		gnAssertSafeLog(t, "stdout", res.stdout)
		gnAssertSafeLog(t, "stderr", res.stderr)
	})
}

// gnEcho is an error body longer than 1,200 bytes that echoes both secrets, the first marker at
// off bytes, so that one of them straddles each cut the runner makes.
func gnEcho(off int) string {
	pad := func(n int) string { return strings.Repeat("e", n) }
	return pad(off) + "token=" + gnToken + " key=" + gnProviderKy + pad(200) + gnProviderKy + pad(150) + gnToken + pad(1500)
}

// AC29 (FR30, EC26, NFR2): secrets echoed back in a long error body reach neither the printed line,
// the artifact nor any posted body, not even a 20-character part of them.
func TestGitLabSecrets_EchoedInAnErrorBodyAreRedactedBeforeTheCut(t *testing.T) {
	offsets := []int{0, 100, 600, 900, 940, 945, 960, 970, 980, 990, 1000, 1100, 1150, 1170, 1180, 1190}
	for _, off := range offsets {
		t.Run("GitLab body, marker at "+strconv.Itoa(off), func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{findings: []map[string]any{
				gnFinding("WARNING", "leaked "+gnToken+" and "+gnProviderKy+" in the rationale"),
			}})
			j.gl.failOn = func(method, path string) (int, string, bool) {
				return http.StatusInternalServerError, gnEcho(off), method == http.MethodPost && strings.HasSuffix(path, "/draft_notes")
			}
			res := j.run()
			if res.code != 1 || !strings.Contains(res.stderr, "[agent-runner] FAILED: GitLab API error posting MR review") {
				t.Fatalf("exit %d, stderr %q; want the posting failure", res.code, res.stderr)
			}
			if line := strings.TrimSuffix(res.stderr, "\n"); len(line) > 1200 {
				t.Errorf("failure line = %d bytes, want at most 1,200", len(line))
			}
			for _, marker := range []string{gnToken, gnProviderKy} {
				gnAssertNoMarker(t, "the printed output", res.stdout+res.stderr, marker)
				gnAssertNoMarker(t, "the artifact", res.artifact, marker)
				gnAssertNoMarker(t, "a posted body", j.gl.posted(), marker)
			}
		})
	}
	t.Run("posted bodies hold neither secret (review and comment)", func(t *testing.T) {
		for _, postAs := range []string{runnerdomain.PostAsReview, runnerdomain.PostAsComment} {
			j := gnNewJob(t, gnModelOpts{findings: []map[string]any{
				gnFinding("WARNING", "leaked "+gnToken+" and "+gnProviderKy+" in the rationale"),
			}})
			j.env["DEVDIGEST_POST_AS"] = postAs
			res := j.run()
			if res.code != 0 {
				t.Fatalf("%s: exit code = %d\nstderr: %s", postAs, res.code, res.stderr)
			}
			if !strings.Contains(j.gl.posted(), "[REDACTED]") {
				t.Errorf("%s: posted bodies = %q, want the secrets replaced by [REDACTED]", postAs, j.gl.posted())
			}
			for _, marker := range []string{gnToken, gnProviderKy} {
				gnAssertNoMarker(t, postAs+": a posted body", j.gl.posted(), marker)
				gnAssertNoMarker(t, postAs+": the artifact", res.artifact, marker)
				gnAssertNoMarker(t, postAs+": the job log", res.stdout+res.stderr, marker)
			}
		}
	})
	for _, off := range []int{0, 900, 1000, 1190} {
		t.Run("model body, marker at "+strconv.Itoa(off), func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{status: http.StatusBadRequest, errBody: gnEcho(off)})
			res := j.run()
			if res.code != 1 || res.hasArtifact {
				t.Fatalf("exit %d, artifact %t; want exit 1 and none", res.code, res.hasArtifact)
			}
			for _, marker := range []string{gnToken, gnProviderKy} {
				gnAssertNoMarker(t, "the printed output", res.stdout+res.stderr, marker)
			}
		})
	}
}

// AC29, the hard case: the GitLab client cuts an error body to 1,000 bytes and knows only the
// GitLab token, so a provider key that straddles that cut survives as a fragment unless the secrets
// of the runner are redacted before the cut (FR30: "before any truncation"). The key starts at
// byte 979-off of the cut text (the token before it shrinks to [REDACTED]), so offsets 951 to 959
// leave 20 to 28 of its 29 characters in front of the cut.
func TestGitLabSecrets_ProviderKeyStraddlingTheGitLabErrorCut(t *testing.T) {
	for _, off := range []int{951, 953, 955, 957, 959} {
		t.Run("marker at "+strconv.Itoa(off), func(t *testing.T) {
			j := gnNewJob(t, gnModelOpts{})
			j.gl.failOn = func(method, path string) (int, string, bool) {
				return http.StatusInternalServerError, gnEcho(off), method == http.MethodPost && strings.HasSuffix(path, "/draft_notes")
			}
			res := j.run()
			if res.code != 1 || !strings.Contains(res.stderr, "[agent-runner] FAILED: GitLab API error posting MR review") {
				t.Fatalf("exit %d, stderr %q; want the posting failure", res.code, res.stderr)
			}
			gnAssertNoMarker(t, "the printed output", res.stdout+res.stderr, gnProviderKy)
		})
	}
}
