package main

// Differential tests against the Node agent-runner (dev-digest at d1c9093): the goldens of
// runner.json (pure functions and the three GitHub calls) and runner_runci.json (runCi end to
// end: 59 scripted scenarios and the 16 runs over the real OpenRouter client against a local
// stub). Spec: AC6050, AC6051, AC6054; plan S31. The goldens are Node truth; where the Go runner
// deliberately differs (spec 7.8.4, 7.8.5, D6018) the test says so and asserts the Go rule.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/ci/runner/adapters/ghapi"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

const (
	goldenWorkspace = "/ws"
	goldenDir       = "/ws/.devdigest"
	goldenResult    = "/ws/devdigest-result.json"
	nodeAPI         = "https://api.github.com"
	testToken       = "ghp_test_token"
)

// ---------------------------------------------------------------------------------------------
// golden loading

func parityPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "internal", "parity", "testdata"}, parts...)...)
}

func readJSONFile(t *testing.T, p string, v any) {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %s: %v", p, err)
	}
}

type goldError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// goldFinding is a finding as the goldens write it.
type goldFinding struct {
	ID         string  `json:"id"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Title      string  `json:"title"`
	File       string  `json:"file"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Rationale  string  `json:"rationale"`
	Suggestion *string `json:"suggestion"`
	Confidence float64 `json:"confidence"`
	Kind       string  `json:"kind"`
}

func (g goldFinding) finding() reviewer.Finding {
	return reviewer.Finding{
		ID: g.ID, Severity: reviewer.Severity(g.Severity), Category: reviewer.Category(g.Category),
		Title: g.Title, File: g.File, StartLine: g.StartLine, EndLine: g.EndLine, Rationale: g.Rationale,
		Suggestion: g.Suggestion, Confidence: g.Confidence, Kind: reviewer.Kind(g.Kind),
	}
}

func findingsOf(gs []goldFinding) []reviewer.Finding {
	out := make([]reviewer.Finding, len(gs))
	for i, g := range gs {
		out[i] = g.finding()
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// fakes of the app ports

// nodeNotFound is the text of Node's ENOENT.
func nodeNotFound(op, p string) error {
	return errors.New("ENOENT: no such file or directory, " + op + " '" + p + "'")
}

// memFiles is an in-memory runnerapp.Files that records what the runner touches and words its
// errors as Node does.
type memFiles struct {
	mu          sync.Mutex
	files       map[string]string
	readErrs    map[string]string // path -> error text
	listing     map[string][]string
	listThrow   string
	hasListing  bool
	reads       []string
	writes      []string
	writeErr    error
	writtenData map[string]string
}

func newMemFiles() *memFiles {
	return &memFiles{files: map[string]string{}, readErrs: map[string]string{}, writtenData: map[string]string{}}
}

func (m *memFiles) ReadFile(p string, max int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, p)
	if msg, ok := m.readErrs[p]; ok {
		return nil, errors.New(msg)
	}
	text, ok := m.files[p]
	if !ok {
		return nil, nodeNotFound("open", p)
	}
	if int64(len(text)) > max {
		return nil, runnerapp.ErrFileTooLarge
	}
	return []byte(text), nil
}

func (m *memFiles) ReadDir(p string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, p+"/")
	if m.listThrow != "" {
		return nil, errors.New(m.listThrow)
	}
	if m.hasListing {
		names, ok := m.listing[p]
		if !ok {
			return nil, nodeNotFound("scandir", p)
		}
		return names, nil
	}
	// the files directly under p, sorted
	var names []string
	for f := range m.files {
		if strings.HasPrefix(f, p+"/") && !strings.Contains(f[len(p)+1:], "/") {
			names = append(names, f[len(p)+1:])
		}
	}
	if len(names) == 0 && p != goldenDir+"/agents" {
		return nil, nodeNotFound("scandir", p)
	}
	sort.Strings(names)
	return names, nil
}

func (m *memFiles) WriteFile(p string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writeErr != nil {
		return m.writeErr
	}
	m.writes = append(m.writes, p)
	m.writtenData[p] = string(data)
	return nil
}

// mapEnv is a runnerapp.Env over a map.
type mapEnv map[string]string

func (e mapEnv) Lookup(key string) (string, bool) { v, ok := e[key]; return v, ok }
func (mapEnv) Cwd() string                        { return goldenWorkspace }

// lineOut records the job log.
type lineOut struct {
	mu             sync.Mutex
	stdout, stderr []string
}

func (o *lineOut) Stdout(line string) { o.mu.Lock(); o.stdout = append(o.stdout, line); o.mu.Unlock() }
func (o *lineOut) Stderr(line string) { o.mu.Lock(); o.stderr = append(o.stderr, line); o.mu.Unlock() }

// ---------------------------------------------------------------------------------------------
// stub GitHub

type stubResp struct {
	Status int    `json:"status"`
	Text   string `json:"text"`
	Throw  string `json:"throw"`
}

type gotRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    any
}

type wantRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

var recordedHeaders = []string{"Authorization", "Accept", "X-GitHub-Api-Version", "User-Agent", "Content-Type"}

// githubStub answers by a function of the request; a Throw closes the connection.
type githubStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []gotRequest
}

func newGitHubStub(t *testing.T, answer func(method, uri string, n int) stubResp) *githubStub {
	t.Helper()
	s := &githubStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := gotRequest{Method: r.Method, URL: r.URL.RequestURI(), Headers: map[string]string{}}
		for _, h := range recordedHeaders {
			if v := r.Header.Get(h); v != "" {
				rec.Headers[h] = v
			}
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &rec.Body); err != nil {
				rec.Body = string(body)
			}
		}
		s.mu.Lock()
		n := 0
		for _, prev := range s.reqs {
			if prev.Method == rec.Method && routeOf(prev.URL) == routeOf(rec.URL) {
				n++
			}
		}
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()

		resp := answer(r.Method, r.URL.RequestURI(), n)
		if resp.Throw != "" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(resp.Status)
		_, _ = io.WriteString(w, resp.Text)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func routeOf(uri string) string {
	switch {
	case strings.HasSuffix(uri, "/reviews"):
		return "reviews"
	case strings.HasSuffix(uri, "/comments"):
		return "comments"
	}
	return "diff"
}

func (s *githubStub) requests() []gotRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gotRequest(nil), s.reqs...)
}

// compareRequests checks the requests the stub saw against Node's, URLs relative to the API root.
func compareRequests(t *testing.T, want []wantRequest, got []gotRequest, rewrite func(string) string) {
	t.Helper()
	type view struct {
		Method  string
		URL     string
		Headers map[string]string
		Body    any
	}
	// An HTTP server drops trailing white space of a header value, so the empty bearer of a
	// run without a token ("Bearer ") arrives as "Bearer".
	trim := func(h map[string]string) map[string]string {
		out := make(map[string]string, len(h))
		for k, v := range h {
			out[k] = strings.TrimRight(v, " ")
		}
		return out
	}
	w := make([]view, len(want))
	for i, r := range want {
		w[i] = view{r.Method, strings.TrimPrefix(rewrite(r.URL), nodeAPI), trim(r.Headers), r.Body}
	}
	g := make([]view, len(got))
	for i, r := range got {
		g[i] = view{r.Method, r.URL, r.Headers, r.Body}
	}
	if diff := cmp.Diff(w, g, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("GitHub requests mismatch (-node +go):\n%s", diff)
	}
}

// goErrorMessage is Node's GitHub API error text with the Go runner's two differences (spec
// 7.8.5): the token is redacted and the body is cut to 1,000 bytes.
func goErrorMessage(msg, token string) string {
	if !strings.HasPrefix(msg, "GitHub API error ") {
		return msg
	}
	i := strings.Index(msg, "): ")
	if i < 0 {
		return msg
	}
	head, rest := msg[:i+3], msg[i+3:]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return msg
	}
	status, body := rest[:sp], rest[sp+1:]
	if len(token) >= 8 { // shorter values are not scrubbed: no real token is that short
		body = strings.ReplaceAll(body, token, "[REDACTED]")
	}
	if len(body) > 1000 {
		body = body[:1000]
	}
	return head + status + " " + body
}

// escapedURL is the one place the Go client words a URL differently: it escapes the owner and
// repository path segments, which Node pasted in as they were.
func escapedURL(u string) string {
	return strings.NewReplacer("ac me/wid/gets", "ac%20me/wid%2Fgets").Replace(u)
}

// ---------------------------------------------------------------------------------------------
// runner.json

type runnerGolden struct {
	ManifestPaths []struct {
		Name    string                     `json:"name"`
		Agent   string                     `json:"agent"`
		Listing map[string]json.RawMessage `json:"listing"`
		Result  string                     `json:"result"`
		Error   *goldError                 `json:"error"`
	} `json:"manifestPaths"`
	Manifests []struct {
		Name      string          `json:"name"`
		Text      json.RawMessage `json:"text"`
		Throw     string          `json:"throw"`
		YAMLError bool            `json:"yamlError"`
		Path      string          `json:"path"`
		Result    *struct {
			Name         string          `json:"name"`
			Provider     string          `json:"provider"`
			Model        string          `json:"model"`
			SystemPrompt json.RawMessage `json:"system_prompt"`
			Skills       []string        `json:"skills"`
			Strategy     string          `json:"strategy"`
			CIFailOn     string          `json:"ci_fail_on"`
			Version      *float64        `json:"version"`
		} `json:"result"`
		Error *goldError `json:"error"`
	} `json:"manifests"`
	Skills []struct {
		Name   string            `json:"name"`
		Slugs  []string          `json:"slugs"`
		Files  map[string]string `json:"files"`
		Errors map[string]string `json:"errors"`
		Reads  []string          `json:"reads"`
		Result []string          `json:"result"`
		Error  *goldError        `json:"error"`
	} `json:"skills"`
	Contexts []struct {
		Name       string            `json:"name"`
		Env        map[string]string `json:"env"`
		Event      json.RawMessage   `json:"event"`
		EventText  *string           `json:"eventText"`
		EventThrow string            `json:"eventThrow"`
		Result     *struct {
			Owner    string  `json:"owner"`
			Repo     string  `json:"repo"`
			PRNumber float64 `json:"prNumber"`
			Title    any     `json:"title"`
			Body     any     `json:"body"`
			IsFork   bool    `json:"isFork"`
		} `json:"result"`
		Error *goldError `json:"error"`
	} `json:"contexts"`
	IntOrNull struct {
		Cases []struct {
			Value  string `json:"value"`
			Result *int64 `json:"result"`
		} `json:"cases"`
		Undefined *int64 `json:"undefined"`
	} `json:"intOrNull"`
	Strips []struct {
		Diff   string `json:"diff"`
		Raw    string `json:"raw"`
		Result string `json:"result"`
	} `json:"strips"`
	Parses []struct {
		Diff   string          `json:"diff"`
		Raw    json.RawMessage `json:"raw"`
		Result struct {
			Files []goldFile `json:"files"`
		} `json:"result"`
	} `json:"parses"`
	Gates []struct {
		Name     string        `json:"name"`
		Findings []goldFinding `json:"findings"`
		ByPolicy map[string]struct {
			Blockers  int  `json:"blockers"`
			Triggered bool `json:"triggered"`
		} `json:"byPolicy"`
	} `json:"gates"`
	Payloads []struct {
		Name   string `json:"name"`
		Diff   string `json:"diff"`
		Review struct {
			Findings []goldFinding `json:"findings"`
		} `json:"review"`
		Opts struct {
			FailOn string  `json:"failOn"`
			Title  *string `json:"title"`
			Inline *bool   `json:"inline"`
		} `json:"opts"`
		Result *goldPayload `json:"result"`
		Error  *goldError   `json:"error"`
	} `json:"payloads"`
	Artifacts []struct {
		Name  string `json:"name"`
		Input struct {
			Findings        []goldFinding `json:"findings"`
			CostUSD         *float64      `json:"costUsd"`
			DurationMS      float64       `json:"durationMs"`
			Agent           string        `json:"agent"`
			PRNumber        float64       `json:"prNumber"`
			CommitSHA       *string       `json:"commitSha"`
			Repository      string        `json:"repository"`
			RepositoryID    *int64        `json:"repositoryId"`
			RunID           *int64        `json:"runId"`
			RunAttempt      *int64        `json:"runAttempt"`
			Model           string        `json:"model"`
			ManifestVersion *int64        `json:"manifestVersion"`
			Skills          []string      `json:"skills"`
			Blockers        int           `json:"blockers"`
		} `json:"input"`
		Result *struct {
			JSON string `json:"json"`
		} `json:"result"`
		Error *goldError `json:"error"`
	} `json:"artifacts"`
	Fetches []struct {
		Name  string `json:"name"`
		Op    string `json:"op"`
		Token string `json:"token"`
		Ctx   struct {
			Owner    string `json:"owner"`
			Repo     string `json:"repo"`
			PRNumber int    `json:"prNumber"`
		} `json:"ctx"`
		Response  *stubResp       `json:"response"`
		Responses []stubResp      `json:"responses"`
		Payload   *goldPayload    `json:"payload"`
		Body      string          `json:"body"`
		Result    json.RawMessage `json:"result"`
		Error     *goldError      `json:"error"`
		Requests  []wantRequest   `json:"requests"`
	} `json:"fetches"`
}

type goldFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Hunks     []struct {
		File           string `json:"file"`
		OldStart       int    `json:"oldStart"`
		OldLines       int    `json:"oldLines"`
		NewStart       int    `json:"newStart"`
		NewLines       int    `json:"newLines"`
		NewLineNumbers []int  `json:"newLineNumbers"`
	} `json:"hunks"`
}

type goldPayload struct {
	Body     string `json:"body"`
	Event    string `json:"event"`
	Comments []struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Body string `json:"body"`
	} `json:"comments"`
}

func loadRunnerGolden(t *testing.T) *runnerGolden {
	t.Helper()
	var g runnerGolden
	readJSONFile(t, parityPath("goldens", "runner.json"), &g)
	return &g
}

func rawString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func TestParity_ManifestPaths(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.ManifestPaths {
		t.Run(c.Name, func(t *testing.T) {
			files := newMemFiles()
			files.hasListing = true
			files.listing = map[string][]string{}
			for dir, raw := range c.Listing {
				if dir == "throw" {
					var msg string
					_ = json.Unmarshal(raw, &msg)
					files.listThrow = msg
					continue
				}
				var names []string
				_ = json.Unmarshal(raw, &names)
				files.listing[dir] = names
			}
			got, err := runnerapp.FindManifestPath(files, goldenDir, c.Agent)
			if c.Error != nil {
				if err == nil || err.Error() != c.Error.Message {
					t.Errorf("FindManifestPath(%q) error = %v, want %q", c.Agent, err, c.Error.Message)
				}
				return
			}
			if err != nil || got != c.Result {
				t.Errorf("FindManifestPath(%q) = %q, %v, want %q", c.Agent, got, err, c.Result)
			}
		})
	}
}

func TestParity_Manifests(t *testing.T) {
	g := loadRunnerGolden(t)
	const yamlPrefix = " is not valid YAML: "
	for _, c := range g.Manifests {
		t.Run(c.Name, func(t *testing.T) {
			text, ok := rawString(c.Text)
			if !ok && c.Throw == "" {
				t.Skip("the corpus text is a digest, not the text")
			}
			files := newMemFiles()
			if c.Throw != "" {
				files.readErrs[c.Path] = c.Throw
			} else {
				files.files[c.Path] = text
			}
			got, err := runnerapp.LoadAgentManifest(files, c.Path)
			if c.Name == "yaml: document with %YAML directive" {
				// Known difference of the YAML parser: Node accepts a %YAML 1.2 directive, yaml.v3
				// refuses it. The runner fails closed; the export never writes one.
				if err == nil {
					t.Errorf("LoadAgentManifest(%q) accepted the directive; the documented difference is a refusal", text)
				}
				return
			}
			switch {
			case c.Error != nil && c.YAMLError:
				want := "Agent manifest at " + c.Path + yamlPrefix
				if err == nil || !strings.HasPrefix(err.Error(), want) {
					t.Errorf("LoadAgentManifest(%q) error = %v, want prefix %q", text, err, want)
				}
			case c.Error != nil:
				if err == nil || err.Error() != c.Error.Message {
					t.Errorf("LoadAgentManifest(%q) error = %v, want %q", text, err, c.Error.Message)
				}
			default:
				if err != nil {
					t.Fatalf("LoadAgentManifest(%q) error = %v", text, err)
				}
				w := c.Result
				prompt, _ := rawString(w.SystemPrompt)
				want := runnerapp.Manifest{
					Name: w.Name, Provider: w.Provider, Model: w.Model, SystemPrompt: prompt,
					Skills: w.Skills, Strategy: reviewer.Strategy(w.Strategy), CIFailOn: reviewer.FailOn(w.CIFailOn),
					Version: w.Version,
				}
				if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("LoadAgentManifest(%q) mismatch (-node +go):\n%s", text, diff)
				}
			}
		})
	}
}

func TestParity_Skills(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Skills {
		t.Run(c.Name, func(t *testing.T) {
			files := newMemFiles()
			for slug, text := range c.Files {
				files.files[path.Join(goldenDir, "skills", slug+".md")] = text
			}
			for slug, msg := range c.Errors {
				files.readErrs[path.Join(goldenDir, "skills", slug+".md")] = msg
			}
			got, err := runnerapp.LoadSkillBodies(files, goldenDir, c.Slugs)

			// Hardening (D6018, spec 7.8.4): a slug that is not a file name fails before any
			// file is read; Node joined it into a path.
			for _, slug := range c.Slugs {
				if !domain.ValidSlug(slug) {
					want := "Skill slug '" + slug + "' is not a valid file name"
					if err == nil || err.Error() != want || len(files.reads) != 0 {
						t.Errorf("LoadSkillBodies(%q) = %v, reads %v; want error %q and no read", c.Slugs, err, files.reads, want)
					}
					return
				}
			}
			if c.Error != nil {
				if err == nil || err.Error() != c.Error.Message {
					t.Errorf("LoadSkillBodies(%q) error = %v, want %q", c.Slugs, err, c.Error.Message)
				}
			} else if err != nil {
				t.Fatalf("LoadSkillBodies(%q) error = %v", c.Slugs, err)
			} else if diff := cmp.Diff(c.Result, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("LoadSkillBodies(%q) mismatch (-node +go):\n%s", c.Slugs, diff)
			}
			if diff := cmp.Diff(c.Reads, files.reads, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("LoadSkillBodies(%q) reads mismatch (-node +go):\n%s", c.Slugs, diff)
			}
		})
	}
}

// jsString is JavaScript's String(v) of a decoded JSON value.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return reviewer.JSNumber(x)
	case []any:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = jsString(it)
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

func TestParity_Contexts(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Contexts {
		t.Run(c.Name, func(t *testing.T) {
			ev := func(key string) domain.EnvValue {
				v, ok := c.Env[key]
				return domain.EnvValue{Value: v, Set: ok}
			}
			var event []byte
			switch {
			case c.EventThrow != "":
			case c.EventText != nil:
				event = []byte(*c.EventText)
			case len(c.Event) > 0 && string(c.Event) != "null":
				event = c.Event
			}
			if c.Env["GITHUB_EVENT_PATH"] == "" {
				event = nil // the runner reads no file without a path
			}
			got, err := domain.ResolvePRContext(ev("GITHUB_REPOSITORY"), ev("PR_NUMBER"), event)
			if c.Result != nil && c.Result.PRNumber > 1<<53-1 {
				// Node accepted any positive integer; the number goes into the artifact and the
				// URLs, which only carry a safe one (the Go domain refuses the rest).
				if err == nil || !strings.HasPrefix(err.Error(), "PR_NUMBER must resolve to a positive integer") {
					t.Errorf("ResolvePRContext(%v) = %+v, %v; want the PR_NUMBER error", c.Env, got, err)
				}
				return
			}
			if c.Error != nil {
				if err == nil || err.Error() != c.Error.Message {
					t.Errorf("ResolvePRContext(%v) error = %v, want %q", c.Env, err, c.Error.Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePRContext(%v) error = %v", c.Env, err)
			}
			w := c.Result
			want := domain.PRContext{Owner: w.Owner, Repo: w.Repo, Number: int(w.PRNumber), Title: jsString(w.Title), Body: jsString(w.Body), IsFork: w.IsFork}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ResolvePRContext(%v) mismatch (-node +go):\n%s", c.Env, diff)
			}
		})
	}
}

func TestParity_IntOrNull(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.IntOrNull.Cases {
		got := domain.IntOrNull(c.Value)
		if diff := cmp.Diff(c.Result, got); diff != "" {
			t.Errorf("IntOrNull(%q) mismatch (-node +go):\n%s", c.Value, diff)
		}
	}
	if got := domain.IntOrNull(""); got != g.IntOrNull.Undefined {
		t.Errorf("IntOrNull(\"\") = %v, want %v", got, g.IntOrNull.Undefined)
	}
}

func TestParity_Strips(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Strips {
		t.Run(c.Diff, func(t *testing.T) {
			if got := domain.StripIgnoredFiles(c.Raw); got != c.Result {
				t.Errorf("StripIgnoredFiles(%q) = %q, want %q", c.Raw, got, c.Result)
			}
		})
	}
}

// diffTexts maps the diff names of the goldens to their text.
func diffTexts(g *runnerGolden) map[string]string {
	m := map[string]string{}
	for _, c := range g.Strips {
		m[c.Diff] = c.Raw
	}
	for _, c := range g.Parses {
		if s, ok := rawString(c.Raw); ok {
			m[c.Diff] = s
		}
	}
	return m
}

func TestParity_Parses(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Parses {
		t.Run(c.Diff, func(t *testing.T) {
			raw, ok := rawString(c.Raw)
			if !ok {
				t.Skip("the corpus text is a digest, not the text")
			}
			d := domain.ParseDiff(raw)
			got := make([]goldFile, len(d.Files))
			for i, f := range d.Files {
				got[i] = goldFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
				for _, h := range f.Hunks {
					got[i].Hunks = append(got[i].Hunks, struct {
						File           string `json:"file"`
						OldStart       int    `json:"oldStart"`
						OldLines       int    `json:"oldLines"`
						NewStart       int    `json:"newStart"`
						NewLines       int    `json:"newLines"`
						NewLineNumbers []int  `json:"newLineNumbers"`
					}{h.File, h.OldStart, h.OldLines, h.NewStart, h.NewLines, h.NewLineNumbers})
				}
			}
			if diff := cmp.Diff(c.Result.Files, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ParseDiff(%s) mismatch (-node +go):\n%s", c.Diff, diff)
			}
			if d.Raw != raw {
				t.Errorf("ParseDiff(%s).Raw differs from the input", c.Diff)
			}
		})
	}
}

func TestParity_Gates(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Gates {
		t.Run(c.Name, func(t *testing.T) {
			findings := findingsOf(c.Findings)
			for policy, want := range c.ByPolicy {
				if got := domain.CountBlockers(findings, reviewer.FailOn(policy)); got != want.Blockers {
					t.Errorf("CountBlockers(%s) = %d, want %d", policy, got, want.Blockers)
				}
				if got := domain.GateTriggered(findings, reviewer.FailOn(policy)); got != want.Triggered {
					t.Errorf("GateTriggered(%s) = %v, want %v", policy, got, want.Triggered)
				}
			}
		})
	}
}

func TestParity_Payloads(t *testing.T) {
	g := loadRunnerGolden(t)
	texts := diffTexts(g)
	for _, c := range g.Payloads {
		t.Run(c.Name, func(t *testing.T) {
			if c.Opts.Inline != nil || c.Diff == "" {
				t.Skip("an option of the shared Node function that the runner never sets: it always anchors on the diff")
			}
			var diff reviewer.Diff
			if c.Diff != "" {
				raw, ok := texts[c.Diff]
				if !ok {
					t.Fatalf("diff %q is not in the goldens", c.Diff)
				}
				diff = domain.ParseDiff(raw)
			}
			failOn, title := reviewer.FailOn(c.Opts.FailOn), "DevDigest Review" // Node's defaults
			if failOn == "" {
				failOn = reviewer.FailOnCritical
			}
			if c.Opts.Title != nil {
				title = *c.Opts.Title
			}
			got := domain.ToReviewPayload(findingsOf(c.Review.Findings), failOn, diff, title)
			w := c.Result
			if w == nil {
				t.Fatalf("golden has an error: %v", c.Error)
			}
			if got.Body != w.Body || got.Event != w.Event {
				t.Errorf("ToReviewPayload() = {%q %s}, want {%q %s}", got.Body, got.Event, w.Body, w.Event)
			}
			if len(got.Comments) != len(w.Comments) {
				t.Fatalf("ToReviewPayload() has %d comments, want %d: %+v", len(got.Comments), len(w.Comments), got.Comments)
			}
			for i, cm := range w.Comments {
				if got.Comments[i].Path != cm.Path || got.Comments[i].Line != cm.Line || got.Comments[i].Body != cm.Body {
					t.Errorf("comment %d = %+v, want %+v", i, got.Comments[i], cm)
				}
			}
		})
	}
}

func TestParity_Artifacts(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Artifacts {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Input
			if c.Name == "lone surrogate in a string" {
				t.Skip("a Go string is UTF-8: a lone surrogate is U+FFFD where Node wrote the escape")
			}
			if in.PRNumber != float64(int(in.PRNumber)) || in.DurationMS != float64(int64(in.DurationMS)) {
				t.Skip("the Go artifact input is typed: a fractional number cannot be expressed")
			}
			a, err := domain.BuildArtifact(domain.ArtifactInput{
				Findings: findingsOf(in.Findings), CostUSD: in.CostUSD, DurationMS: int64(in.DurationMS), Agent: in.Agent,
				PRNumber: int(in.PRNumber), CommitSHA: in.CommitSHA, Repository: in.Repository,
				RepositoryID: in.RepositoryID, RunID: in.RunID, RunAttempt: in.RunAttempt, Model: in.Model,
				ManifestVersion: in.ManifestVersion, Skills: in.Skills, Blockers: in.Blockers,
			})
			if err != nil {
				t.Fatalf("BuildArtifact() error = %v (golden: %+v)", err, c.Error)
			}
			if c.Result == nil {
				t.Fatalf("golden has an error %v, Go built an artifact", c.Error)
			}
			if got := a.JSON(); got != c.Result.JSON {
				t.Errorf("Artifact.JSON() mismatch\n got: %q\nwant: %q", got, c.Result.JSON)
			}
		})
	}
}

func TestParity_GitHubCalls(t *testing.T) {
	g := loadRunnerGolden(t)
	for _, c := range g.Fetches {
		t.Run(c.Name, func(t *testing.T) {
			responses := c.Responses
			if responses == nil && c.Response != nil {
				responses = []stubResp{*c.Response}
			}
			stub := newGitHubStub(t, func(_, _ string, n int) stubResp {
				return responses[min(n, len(responses)-1)]
			})
			client := ghapi.New(ghapi.Options{BaseURL: stub.srv.URL})
			pr := runnerapp.PullRef{Owner: c.Ctx.Owner, Repo: c.Ctx.Repo, Number: c.Ctx.PRNumber}

			var gotText string
			var err error
			switch c.Op {
			case "diff":
				gotText, err = client.FetchDiff(t.Context(), c.Token, pr)
			case "review":
				p := domain.ReviewPayload{Body: c.Payload.Body, Event: c.Payload.Event}
				for _, cm := range c.Payload.Comments {
					p.Comments = append(p.Comments, domain.InlineComment{Path: cm.Path, Line: cm.Line, Body: cm.Body})
				}
				err = runnerapp.PostReview(t.Context(), client, c.Token, pr, p)
			case "comment":
				err = client.PostComment(t.Context(), c.Token, pr, c.Body)
			default:
				t.Fatalf("unknown op %q", c.Op)
			}

			rewrite := func(s string) string { return s }
			if c.Name == "diff url with unusual names" {
				rewrite = escapedURL // the Go client escapes the path segments
			}
			compareRequests(t, c.Requests, stub.requests(), rewrite)

			transport := responses[0].Throw != ""
			switch {
			case c.Error != nil && transport:
				if err == nil {
					t.Errorf("%s: error = nil, want a transport error", c.Op)
				}
			case c.Error != nil:
				want := goErrorMessage(rewrite(c.Error.Message), c.Token)
				if err == nil || strings.Replace(err.Error(), stub.srv.URL, nodeAPI, 1) != want {
					t.Errorf("%s: error = %v, want %q", c.Op, err, want)
				}
			case err != nil:
				t.Errorf("%s: error = %v, want success", c.Op, err)
			case c.Op == "diff":
				var want string
				_ = json.Unmarshal(c.Result, &want)
				if gotText != want {
					t.Errorf("FetchDiff() = %q, want %q", gotText, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// runner_runci.json

type runCiGolden struct {
	Scenarios  []runCiScenario `json:"scenarios"`
	OpenRouter struct {
		Scenarios []orScenario `json:"scenarios"`
	} `json:"openrouter"`
}

type runCiScenario struct {
	Name  string `json:"name"`
	Input struct {
		Manifest       *string           `json:"manifest"`
		AgentFile      string            `json:"agentFile"`
		ExtraManifests map[string]string `json:"extraManifests"`
		Skills         map[string]string `json:"skills"`
		ExtraFiles     map[string]string `json:"extraFiles"`
		NoAgents       bool              `json:"noAgentsDirectory"`
		Event          json.RawMessage   `json:"event"`
		Env            map[string]string `json:"env"`
		PostAs         string            `json:"postAs"`
		Diff           *string           `json:"diff"`
		DiffResponse   *stubResp         `json:"diffResponse"`
		GitHub         *struct {
			Reviews []stubResp `json:"reviews"`
			Comment *stubResp  `json:"comment"`
		} `json:"github"`
		LLM []llmEntry `json:"llm"`
	} `json:"input"`
	Result struct {
		ExitCode int    `json:"exitCode"`
		Error    string `json:"error"`
		Artifact *struct {
			FindingsCount int `json:"findings_count"`
			Blockers      int `json:"blockers"`
		} `json:"artifact"`
		Posted *struct {
			Kind string `json:"kind"`
		} `json:"posted"`
		Blockers      int  `json:"blockers"`
		GateTriggered bool `json:"gateTriggered"`
	} `json:"result"`
	ArtifactFile *string          `json:"artifactFile"`
	Writes       []string         `json:"writes"`
	Reads        []string         `json:"reads"`
	Requests     []wantRequest    `json:"requests"`
	LLMRequests  []goldLLMRequest `json:"llmRequests"`
	Logs         []string         `json:"logs"`
}

type llmEntry struct {
	Cost     *float64        `json:"cost"`
	Findings json.RawMessage `json:"findings"`
	Error    *string         `json:"error"`
}

type goldLLMRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type runCiCorpus struct {
	Event       json.RawMessage   `json:"event"`
	Env         map[string]string `json:"env"`
	FindingBase map[string]any    `json:"findingBase"`
	Grounded    []map[string]any  `json:"grounded"`
}

func loadRunCiCorpus(t *testing.T) *runCiCorpus {
	t.Helper()
	var c runCiCorpus
	readJSONFile(t, parityPath("corpus", "runner", "runci.json"), &c)
	return &c
}

// findingsFor expands a scripted findings value ("$grounded" or a list) over the finding base.
func (c *runCiCorpus) findingsFor(t *testing.T, raw json.RawMessage) []reviewer.Finding {
	t.Helper()
	var list []map[string]any
	if s, ok := rawString(raw); ok {
		if s != "$grounded" {
			t.Fatalf("unknown findings placeholder %q", s)
		}
		list = c.Grounded
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatalf("decode findings %s: %v", raw, err)
		}
	}
	out := make([]reviewer.Finding, len(list))
	for i, over := range list {
		merged := map[string]any{}
		for k, v := range c.FindingBase {
			merged[k] = v
		}
		merged["id"] = "f" + strconv.Itoa(i+1)
		for k, v := range over {
			merged[k] = v
		}
		data, _ := json.Marshal(merged)
		var gf goldFinding
		if err := json.Unmarshal(data, &gf); err != nil {
			t.Fatalf("decode finding %s: %v", data, err)
		}
		out[i] = gf.finding()
	}
	return out
}

// scriptedLLM is the stub model of the runCi scenarios: the entry of a call is chosen by its
// chunk (Node chose by arrival order, which is chunk order), the last entry repeats.
type scriptedLLM struct {
	t      *testing.T
	corpus *runCiCorpus
	script []llmEntry
	mu     sync.Mutex
	calls  []scriptedCall
}

type scriptedCall struct {
	Chunk    int
	Model    string
	Messages []reviewer.Message
}

func (s *scriptedLLM) For(model string) reviewerapp.LLM { return scriptedCaller{s: s, model: model} }

type scriptedCaller struct {
	s     *scriptedLLM
	model string
}

func (c scriptedCaller) Review(_ context.Context, call reviewerapp.Call) (reviewerapp.CallResult, error) {
	s := c.s
	s.mu.Lock()
	s.calls = append(s.calls, scriptedCall{Chunk: call.Chunk.Index, Model: c.model, Messages: call.Messages})
	s.mu.Unlock()
	if len(s.script) == 0 {
		return reviewerapp.CallResult{}, errors.New("no scripted model reply")
	}
	entry := s.script[min(call.Chunk.Index, len(s.script)-1)]
	if entry.Error != nil {
		return reviewerapp.CallResult{}, errors.New(*entry.Error)
	}
	review := reviewer.Review{Verdict: reviewer.VerdictApprove, Summary: "looks fine", Score: 95, Findings: s.corpus.findingsFor(s.t, entry.Findings)}
	return reviewerapp.CallResult{
		Review: review,
		Raw:    "{}",
		Usage:  reviewer.Usage{TokensIn: 100, TokensOut: 50, CostUSD: entry.Cost},
	}, nil
}

// fakeClock returns 0 on the first call and 4321 ms from then on, the clock of the generator.
func fakeClock() func() time.Time {
	var mu sync.Mutex
	n := 0
	base := time.Unix(1_700_000_000, 0)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return base
		}
		return base.Add(4321 * time.Millisecond)
	}
}

var buildSuffix = regexp.MustCompile(` build=\S+$`)

func TestParity_RunCI(t *testing.T) {
	var g runCiGolden
	readJSONFile(t, parityPath("goldens", "runner_runci.json"), &g)
	corpus := loadRunCiCorpus(t)
	if len(g.Scenarios) != 59 {
		t.Fatalf("golden has %d scenarios, want 59", len(g.Scenarios))
	}
	for _, sc := range g.Scenarios {
		t.Run(sc.Name, func(t *testing.T) { runCiScenarioTest(t, corpus, sc) })
	}
}

func runCiScenarioTest(t *testing.T, corpus *runCiCorpus, sc runCiScenario) {
	in := sc.Input
	files := newMemFiles()
	agentFile := in.AgentFile
	if in.Manifest != nil {
		files.files[goldenDir+"/agents/"+agentFile] = *in.Manifest
	}
	for name, text := range in.ExtraManifests {
		files.files[goldenDir+"/agents/"+name] = text
	}
	for slug, text := range in.Skills {
		files.files[goldenDir+"/skills/"+slug+".md"] = text
	}
	for p, text := range in.ExtraFiles {
		files.files[p] = text
	}
	if len(in.Event) > 0 && string(in.Event) != "null" {
		files.files["/ws/event.json"] = string(in.Event)
	}
	if in.NoAgents {
		files.hasListing = true
		files.listing = map[string][]string{}
	}

	diffResp := stubResp{Status: 200}
	if in.DiffResponse != nil {
		diffResp = *in.DiffResponse
	} else if in.Diff != nil {
		diffResp.Text = *in.Diff
	}
	stub := newGitHubStub(t, func(method, uri string, n int) stubResp {
		switch {
		case method == http.MethodGet && !strings.Contains(uri, "/reviews") && !strings.Contains(uri, "/comments"):
			return diffResp
		case method == http.MethodPost && strings.HasSuffix(uri, "/reviews"):
			if in.GitHub == nil || len(in.GitHub.Reviews) == 0 {
				return stubResp{Status: 200, Text: "{}"}
			}
			return in.GitHub.Reviews[min(n, len(in.GitHub.Reviews)-1)]
		case method == http.MethodPost && strings.HasSuffix(uri, "/comments"):
			if in.GitHub != nil && in.GitHub.Comment != nil {
				return *in.GitHub.Comment
			}
			return stubResp{Status: 201, Text: "{}"}
		}
		return stubResp{Status: 599, Text: "unexpected request"}
	})

	env := mapEnv{}
	for k, v := range in.Env {
		env[k] = v
	}
	env["DEVDIGEST_POST_AS"] = in.PostAs // the postAs parameter of Node's runCi is this variable here
	script := &scriptedLLM{t: t, corpus: corpus, script: in.LLM}
	out := &lineOut{}
	code := runnerapp.Run(t.Context(), runnerapp.Config{BuildID: "abc123def456"}, runnerapp.Deps{
		Env: env, Files: files, GitHub: ghapi.New(ghapi.Options{BaseURL: stub.srv.URL}),
		LLM: script, Out: out, Now: fakeClock(),
	})

	// The Go runner differs from Node in three documented places; the expectation follows them.
	wantCode, wantError := sc.Result.ExitCode, sc.Result.Error
	wantReads, wantWrites, wantRequests, wantLLM := sc.Reads, sc.Writes, sc.Requests, sc.LLMRequests
	wantArtifact := sc.ArtifactFile
	wantLogs := sc.Logs
	switch {
	case strings.HasPrefix(sc.Name, "a skill slug that escapes the directory"):
		// D6018: the slug is refused before any file, the diff or the model is touched.
		wantCode, wantError = 1, "Skill slug '../../outside' is not a valid file name"
		wantReads = []string{goldenDir + "/agents/", goldenDir + "/agents/" + agentFile}
		wantWrites, wantRequests, wantLLM, wantArtifact, wantLogs = nil, nil, nil, nil, nil
	case wantError != "":
		wantError = goErrorMessage(wantError, env["GITHUB_TOKEN"])
	}

	if code != wantCode {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, wantCode, out.stderr)
	}

	// stderr: the failure line, or nothing
	var failed []string
	for _, l := range out.stderr {
		if strings.HasPrefix(l, "[agent-runner] FAILED: ") {
			failed = append(failed, strings.ReplaceAll(strings.TrimPrefix(l, "[agent-runner] FAILED: "), stub.srv.URL, nodeAPI))
		}
	}
	switch {
	case wantError == "":
		if len(failed) != 0 {
			t.Errorf("failure line = %q, want none", failed)
		}
	case sc.Name == "the diff fetch fails on the transport":
		if len(failed) != 1 {
			t.Errorf("failure lines = %q, want one", failed)
		}
	default:
		if diff := cmp.Diff([]string{wantError}, failed); diff != "" {
			t.Errorf("failure line mismatch (-node +go):\n%s", diff)
		}
	}

	// stdout: the trace line (Node's plus " build=<id>") and, on success, the result line
	var traces []string
	var results []string
	for _, l := range out.stdout {
		if strings.HasPrefix(l, "[agent-runner] findings=") {
			results = append(results, l)
			continue
		}
		if !strings.HasSuffix(l, " build=abc123def456") && strings.HasPrefix(l, "[agent-runner] trace ") {
			t.Errorf("trace line %q lacks the build id", l)
		}
		traces = append(traces, buildSuffix.ReplaceAllString(l, ""))
	}
	if diff := cmp.Diff(wantLogs, traces, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("trace lines mismatch (-node +go):\n%s", diff)
	}
	if wantError == "" {
		a := sc.Result.Artifact
		want := fmt.Sprintf("[agent-runner] findings=%d blockers=%d gateTriggered=%t posted=%s",
			a.FindingsCount, sc.Result.Blockers, sc.Result.GateTriggered, sc.Result.Posted.Kind)
		if diff := cmp.Diff([]string{want}, results); diff != "" {
			t.Errorf("result line mismatch (-node +go):\n%s", diff)
		}
	} else if len(results) != 0 {
		t.Errorf("result line %q printed on a failure", results)
	}

	if diff := cmp.Diff(wantReads, files.reads, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("files read mismatch (-node +go):\n%s", diff)
	}
	if diff := cmp.Diff(wantWrites, files.writes, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("files written mismatch (-node +go):\n%s", diff)
	}
	if wantArtifact == nil {
		if len(files.writtenData) != 0 {
			t.Errorf("artifact written %v, want none", files.writtenData)
		}
	} else if got, ok := files.writtenData[goldenResult]; !ok || got != *wantArtifact {
		t.Errorf("artifact file mismatch\n got: %q\nwant: %q", got, *wantArtifact)
	}

	compareRequests(t, wantRequests, stub.requests(), func(s string) string { return s })

	sort.SliceStable(script.calls, func(i, j int) bool { return script.calls[i].Chunk < script.calls[j].Chunk })
	if sc.Name == "a model error after a partial first call in map-reduce" && len(script.calls) == len(wantLLM)-1 {
		// After the first failed call the engine starts no further chunk (reviewer/app.Run); the
		// third call races with the failure of the second, Node started all three.
		wantLLM = wantLLM[:len(script.calls)]
	}
	if len(script.calls) != len(wantLLM) {
		t.Fatalf("model calls = %d, want %d", len(script.calls), len(wantLLM))
	}
	for i, w := range wantLLM {
		got := script.calls[i]
		if got.Model != w.Model {
			t.Errorf("call %d model = %q, want %q", i, got.Model, w.Model)
		}
		if len(got.Messages) != len(w.Messages) {
			t.Fatalf("call %d has %d messages, want %d", i, len(got.Messages), len(w.Messages))
		}
		for j, m := range w.Messages {
			if string(got.Messages[j].Role) != m.Role || got.Messages[j].Content != m.Content {
				t.Errorf("call %d message %d = {%s %q}, want {%s %q}", i, j, got.Messages[j].Role, got.Messages[j].Content, m.Role, m.Content)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------
// runCi over the real OpenRouter client and a local stub

type orScenario struct {
	Name  string `json:"name"`
	Input struct {
		Manifest  string            `json:"manifest"`
		Diff      string            `json:"diff"`
		Env       map[string]string `json:"env"`
		Responses []orResponse      `json:"responses"`
	} `json:"input"`
	Result struct {
		ExitCode int    `json:"exitCode"`
		Error    string `json:"error"`
	} `json:"result"`
	ArtifactFile *string         `json:"artifactFile"`
	Requests     []wantRequest   `json:"requests"`
	HTTPRequests []orHTTPRequest `json:"httpRequests"`
}

type orResponse struct {
	Review *struct {
		Findings json.RawMessage `json:"findings"`
	} `json:"review"`
	Raw    *string        `json:"raw"`
	Fenced bool           `json:"fenced"`
	Usage  map[string]any `json:"usage"`
	HTTP   *struct {
		Status int `json:"status"`
		JSON   any `json:"json"`
	} `json:"http"`
}

type orHTTPRequest struct {
	Method        string  `json:"method"`
	Path          string  `json:"path"`
	Authorization *string `json:"authorization"`
	ContentType   *string `json:"contentType"`
	Body          struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Temperature    *float64 `json:"temperature"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string `json:"name"`
				Strict bool   `json:"strict"`
			} `json:"json_schema"`
		} `json:"response_format"`
		Usage struct {
			Include bool `json:"include"`
		} `json:"usage"`
	} `json:"body"`
	Chunk int `json:"chunk"`
}

// openRouterStub plays OpenRouter: responses are picked by arrival, or by the chunk the request
// carries (map-reduce sends its chunks concurrently).
type openRouterStub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []map[string]any
	idx      int
	chunkOf  func(body map[string]any) int
	scenario *orScenario
	corpus   *runCiCorpus
	t        *testing.T
}

func newOpenRouterStub(t *testing.T, corpus *runCiCorpus, sc *orScenario, byChunk bool, chunkPaths []string) *openRouterStub {
	s := &openRouterStub{t: t, scenario: sc, corpus: corpus}
	pathRe := regexp.MustCompile(`diff --git a/(\S+)`)
	s.chunkOf = func(body map[string]any) int {
		msgs, _ := body["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] != "user" {
				continue
			}
			text, _ := mm["content"].(string)
			_, after, _ := strings.Cut(text, "## Diff to review")
			if loc := pathRe.FindStringSubmatch(after); loc != nil {
				for i, p := range chunkPaths {
					if p == loc[1] {
						return i
					}
				}
			}
		}
		return 0
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		body["_method"], body["_path"] = r.Method, r.URL.Path
		body["_authorization"], body["_contentType"] = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		chunk := s.chunkOf(body)
		body["_chunk"] = chunk
		s.mu.Lock()
		s.reqs = append(s.reqs, body)
		pick := s.idx
		s.idx++
		s.mu.Unlock()
		if byChunk {
			pick = chunk
		}
		responses := sc.Input.Responses
		var resp orResponse
		if len(responses) > 0 {
			resp = responses[min(pick, len(responses)-1)]
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.HTTP != nil {
			w.WriteHeader(resp.HTTP.Status)
			_ = json.NewEncoder(w).Encode(resp.HTTP.JSON)
			return
		}
		var findings []reviewer.Finding
		if resp.Review != nil {
			findings = corpus.findingsFor(t, resp.Review.Findings)
		}
		jsonFindings := make([]map[string]any, len(findings))
		for i, f := range findings {
			jsonFindings[i] = map[string]any{
				"id": f.ID, "severity": string(f.Severity), "category": string(f.Category), "title": f.Title, "file": f.File,
				"start_line": f.StartLine, "end_line": f.EndLine, "rationale": f.Rationale, "confidence": f.Confidence,
				"kind": string(f.Kind),
			}
		}
		review, _ := json.Marshal(map[string]any{"verdict": "approve", "summary": "looks fine", "score": 95, "findings": jsonFindings})
		content := string(review)
		if resp.Raw != nil {
			content = *resp.Raw
		}
		if resp.Fenced {
			content = "```json\n" + string(review) + "\n```"
		}
		reply := map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "deepseek/deepseek-v4-flash",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		}
		if resp.Usage != nil {
			reply["usage"] = resp.Usage
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *openRouterStub) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	reqs := append([]map[string]any(nil), s.reqs...)
	sort.SliceStable(reqs, func(i, j int) bool { return reqs[i]["_chunk"].(int) < reqs[j]["_chunk"].(int) })
	return reqs
}

var durationField = regexp.MustCompile(`"duration_ms": \d+`)

func TestParity_OpenRouter(t *testing.T) {
	var g runCiGolden
	readJSONFile(t, parityPath("goldens", "runner_runci.json"), &g)
	corpus := loadRunCiCorpus(t)
	if len(g.OpenRouter.Scenarios) != 16 {
		t.Fatalf("golden has %d OpenRouter scenarios, want 16", len(g.OpenRouter.Scenarios))
	}
	byChunk := map[string]bool{"three calls sum with float noise": true, "one of three calls without a cost": true}
	for i := range g.OpenRouter.Scenarios {
		sc := g.OpenRouter.Scenarios[i]
		t.Run(sc.Name, func(t *testing.T) {
			root := t.TempDir()
			mustWrite(t, filepath.Join(root, ".devdigest", "agents", "security-reviewer.yaml"), sc.Input.Manifest)
			mustWrite(t, filepath.Join(root, "event.json"), string(corpus.Event))

			var chunkPaths []string
			for _, m := range regexp.MustCompile(`(?m)^diff --git a/(\S+)`).FindAllStringSubmatch(sc.Input.Diff, -1) {
				chunkPaths = append(chunkPaths, m[1])
			}
			or := newOpenRouterStub(t, corpus, &sc, byChunk[sc.Name], chunkPaths)
			gh := newGitHubStub(t, func(method, uri string, _ int) stubResp {
				if method == http.MethodGet {
					return stubResp{Status: 200, Text: sc.Input.Diff}
				}
				return stubResp{Status: 200, Text: "{}"}
			})

			env := map[string]string{}
			for k, v := range corpus.Env {
				env[k] = v
			}
			env["GITHUB_EVENT_PATH"] = filepath.Join(root, "event.json")
			env["DEVDIGEST_DIR"] = filepath.Join(root, ".devdigest")
			env["DEVDIGEST_RESULT_PATH"] = filepath.Join(root, "devdigest-result.json")
			for k, v := range sc.Input.Env {
				env[k] = v
			}
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }, &stdout, &stderr,
				endpoints{OpenRouter: or.srv.URL + "/api/v1", GitHub: gh.srv.URL})

			if code != sc.Result.ExitCode {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, sc.Result.ExitCode, &stdout, &stderr)
			}

			// the artifact
			written, werr := os.ReadFile(env["DEVDIGEST_RESULT_PATH"])
			if sc.ArtifactFile == nil {
				if werr == nil {
					t.Errorf("artifact written, want none: %s", written)
				}
			} else if werr != nil {
				t.Errorf("artifact not written: %v", werr)
			} else {
				got := durationField.ReplaceAllString(string(written), `"duration_ms": 4321`)
				if got != *sc.ArtifactFile {
					t.Errorf("artifact mismatch\n got: %s\nwant: %s", got, *sc.ArtifactFile)
				}
			}

			// the failure text: the engine's own texts are Go's where Node printed the OpenAI SDK's
			if sc.Result.Error != "" {
				stderrText := strings.ReplaceAll(stderr.String(), root, goldenWorkspace)
				if strings.HasPrefix(sc.Result.Error, "OpenRouter ") {
					if !strings.Contains(stderrText, "[agent-runner] FAILED: "+oneLineText(sc.Result.Error)) {
						t.Errorf("stderr lacks the Node failure text\n got: %s\nwant: %s", stderrText, oneLineText(sc.Result.Error))
					}
				} else if !strings.Contains(stderrText, "[agent-runner] FAILED: ") {
					t.Errorf("stderr lacks a failure line: %s", stderrText)
				}
			}

			// the HTTP requests the model layer sent
			got := or.requests()
			if sc.Name == "an empty key is sent as an empty bearer and the stub refuses it" {
				// Node sent the call with an empty bearer; the Go client refuses an empty key
				// before any request.
				if len(got) != 0 {
					t.Errorf("requests with an empty key = %d, want 0", len(got))
				}
				return
			}
			if len(got) != len(sc.HTTPRequests) {
				t.Fatalf("OpenRouter requests = %d, want %d", len(got), len(sc.HTTPRequests))
			}
			for i, w := range sc.HTTPRequests {
				r := got[i]
				if r["_method"] != w.Method || r["_path"] != w.Path {
					t.Errorf("request %d = %v %v, want %s %s", i, r["_method"], r["_path"], w.Method, w.Path)
				}
				if w.Authorization != nil && r["_authorization"] != *w.Authorization {
					t.Errorf("request %d authorization = %v, want %s", i, r["_authorization"], *w.Authorization)
				}
				if w.ContentType != nil && !strings.HasPrefix(r["_contentType"].(string), *w.ContentType) {
					t.Errorf("request %d content type = %v, want %s", i, r["_contentType"], *w.ContentType)
				}
				compareLLMBody(t, i, w, r)
			}
		})
	}
}

// parseErrorReprompt is the re-prompt the engine sends after a reply that is not JSON. Its middle
// is the parser's own message (JSON.parse in Node, encoding/json here), which differs by nature.
var parseErrorReprompt = regexp.MustCompile(`(?s)^(Output was not valid JSON: ).*?(\nReturn ONLY a single valid JSON object)`)

func maskParseError(s string) string {
	return parseErrorReprompt.ReplaceAllString(s, "${1}<parser message>${2}")
}

func oneLineText(s string) string { return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(s) }

func compareLLMBody(t *testing.T, i int, w orHTTPRequest, r map[string]any) {
	t.Helper()
	if r["model"] != w.Body.Model {
		t.Errorf("request %d model = %v, want %s", i, r["model"], w.Body.Model)
	}
	msgs, _ := r["messages"].([]any)
	if len(msgs) != len(w.Body.Messages) {
		t.Errorf("request %d has %d messages, want %d", i, len(msgs), len(w.Body.Messages))
	} else {
		for j, m := range w.Body.Messages {
			mm, _ := msgs[j].(map[string]any)
			got, _ := mm["content"].(string)
			if mm["role"] == "user" {
				got, m.Content = maskParseError(got), maskParseError(m.Content)
			}
			if mm["role"] != m.Role || got != m.Content {
				t.Errorf("request %d message %d = {%v %q}, want {%s %q}", i, j, mm["role"], mm["content"], m.Role, m.Content)
			}
		}
	}
	if w.Body.Temperature != nil && r["temperature"] != *w.Body.Temperature {
		t.Errorf("request %d temperature = %v, want %v", i, r["temperature"], *w.Body.Temperature)
	}
	rf, _ := r["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != w.Body.ResponseFormat.Type || js["name"] != w.Body.ResponseFormat.JSONSchema.Name || js["strict"] != w.Body.ResponseFormat.JSONSchema.Strict {
		t.Errorf("request %d response_format = %v, want %+v", i, rf, w.Body.ResponseFormat)
	}
	usage, _ := r["usage"].(map[string]any)
	if usage["include"] != w.Body.Usage.Include {
		t.Errorf("request %d usage = %v, want include %v", i, usage, w.Body.Usage.Include)
	}
}

func mustWrite(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
