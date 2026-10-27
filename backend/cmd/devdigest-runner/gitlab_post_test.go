package main

// GitLab C S25: the runner's posting in GitLab mode, end to end through runProcess (spec FR24-FR28,
// FR38, FR48; AC24-AC27, AC46, AC47). The runner runs against a real git checkout (the image's git
// program), a TLS GitLab API stub (CI_API_V4_URL, CI_SERVER_TLS_CA_FILE = the stub's certificate)
// and an OpenRouter stub that plays the mock model. Helpers carry the prefix gr.

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/adapters/gitdiff"
	"dev-digest-go/backend/internal/platform/gitlab/notebody"
)

const (
	grToken  = "glpat-gr0123456789abcdefXYZ"
	grORKey  = "sk-or-test-key-value"
	grPrefix = "/api/v4"
	grMRPath = "/projects/42/merge_requests/7"
	grStart  = "5555555555555555555555555555555555555555"
)

// ---------------------------------------------------------------- checkout fixture

type grFixture struct{ dir, base, head string }

func grGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func grWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func grLines(format string, from, to int, edit func(int) (string, bool)) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		line := fmt.Sprintf(format, i)
		if edit != nil {
			if repl, ok := edit(i); ok {
				line = repl
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// grBulkFiles are the new files of the head commit that hold 20 added lines each; their names
// exercise the byte order of the cap (an upper-case name sorts before a lower-case one, "@" first).
var grBulkFiles = []string{"pkg/@scope.go", "pkg/B.go", "pkg/a.go", "pkg/c.go", "pkg/d.go", "pkg/e.go"}

// grNewFixture builds a checkout whose head commit
//   - inserts two lines after line 5 of svc/handler.go and edits its line 30: new line 8 is a
//     context line (old line 6), new line 32 an added line;
//   - renames old/name.go to new/name.go and edits its line 5: new line 3 is a context line of a
//     renamed file (old path old/name.go, old line 3);
//   - adds the six pkg files of 20 lines.
func grNewFixture(t *testing.T) grFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git program not available")
	}
	dir := t.TempDir()
	grGit(t, dir, "init", "-q")
	grWrite(t, dir, "svc/handler.go", grLines("func handler line %02d", 1, 40, nil))
	grWrite(t, dir, "old/name.go", grLines("rename source line %02d", 1, 10, nil))
	grGit(t, dir, "add", "-A")
	grGit(t, dir, "commit", "-q", "-m", "base")
	base := grGit(t, dir, "rev-parse", "HEAD")

	var h strings.Builder
	for i := 1; i <= 40; i++ {
		switch i {
		case 6:
			h.WriteString("inserted A\ninserted B\n")
		case 30:
			h.WriteString("changed handler line 30\n")
			continue
		}
		h.WriteString(fmt.Sprintf("func handler line %02d\n", i))
	}
	grWrite(t, dir, "svc/handler.go", h.String())
	if err := os.MkdirAll(filepath.Join(dir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	grGit(t, dir, "mv", "old/name.go", "new/name.go")
	grWrite(t, dir, "new/name.go", grLines("rename source line %02d", 1, 10, func(i int) (string, bool) {
		return "rename source line 05 EDITED", i == 5
	}))
	for _, f := range grBulkFiles {
		grWrite(t, dir, f, grLines("// "+f+" line %02d", 1, 20, nil))
	}
	grGit(t, dir, "add", "-A")
	grGit(t, dir, "commit", "-q", "-m", "head")
	return grFixture{dir: dir, base: base, head: grGit(t, dir, "rev-parse", "HEAD")}
}

// ---------------------------------------------------------------- GitLab API stub

type grReq struct {
	Method, Path, Query, Token, Auth, Host, Body string
}

// label is the request below the merge request, with the method: "GET mr", "POST /draft_notes",
// "PUT /draft_notes/1001/publish".
func (r grReq) label() string {
	i := strings.Index(r.Path, grMRPath)
	if i < 0 {
		return r.Method + " " + r.Path
	}
	rest := r.Path[i+len(grMRPath):]
	if rest == "" {
		rest = "mr"
	}
	return r.Method + " " + rest
}

var grDigits = regexp.MustCompile(`\d+`)

// kind is the label without ids, to count the requests of one kind.
func (r grReq) kind() string { return grDigits.ReplaceAllString(r.label(), "N") }

type grAnswer struct {
	Status   int
	Body     string
	Location string
}

type grGitLab struct {
	srv    *httptest.Server
	caFile string
	prefix string

	mu     sync.Mutex
	reqs   []grReq
	nextID int
	// existing are the ids of the token user's pending drafts before the run.
	existing []int
	// mr is the answer of the merge request read.
	mr string
	// answer overrides the default answer; n counts the earlier requests of the same kind.
	answer func(r grReq, n int) (grAnswer, bool)
}

func grNewGitLab(t *testing.T, fx grFixture) *grGitLab {
	t.Helper()
	gl := &grGitLab{prefix: grPrefix, nextID: 1000}
	gl.mr = `{"iid":7,"diff_refs":{"base_sha":"` + fx.base + `","start_sha":"` + grStart + `","head_sha":"` + fx.head + `"}}`
	gl.srv = httptest.NewTLSServer(http.HandlerFunc(gl.serve))
	t.Cleanup(gl.srv.Close)
	gl.caFile = filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gl.srv.Certificate().Raw})
	if err := os.WriteFile(gl.caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return gl
}

func (gl *grGitLab) apiURL() string { return gl.srv.URL + gl.prefix }

func (gl *grGitLab) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := grReq{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Token: r.Header.Get("PRIVATE-TOKEN"),
		Auth: r.Header.Get("Authorization"), Host: r.Host, Body: string(body),
	}
	gl.mu.Lock()
	n := 0
	for _, prev := range gl.reqs {
		if prev.kind() == req.kind() {
			n++
		}
	}
	gl.reqs = append(gl.reqs, req)
	ans, ok := grAnswer{}, false
	if gl.answer != nil {
		ans, ok = gl.answer(req, n)
	}
	if !ok {
		ans = gl.defaultAnswer(req)
	}
	gl.mu.Unlock()

	if ans.Location != "" {
		w.Header().Set("Location", ans.Location)
	}
	w.WriteHeader(ans.Status)
	_, _ = io.WriteString(w, ans.Body)
}

// defaultAnswer is a healthy GitLab (called with the lock held).
func (gl *grGitLab) defaultAnswer(r grReq) grAnswer {
	switch r.kind() {
	case "GET mr":
		return grAnswer{Status: 200, Body: gl.mr}
	case "GET /draft_notes":
		var parts []string
		for _, id := range gl.existing {
			parts = append(parts, `{"id":`+strconv.Itoa(id)+`}`)
		}
		return grAnswer{Status: 200, Body: "[" + strings.Join(parts, ",") + "]"}
	case "POST /draft_notes":
		gl.nextID++
		return grAnswer{Status: 201, Body: `{"id":` + strconv.Itoa(gl.nextID-1) + `}`}
	case "POST /draft_notes/bulk_publish", "DELETE /draft_notes/N":
		return grAnswer{Status: 204}
	case "PUT /draft_notes/N/publish":
		return grAnswer{Status: 200, Body: "{}"}
	case "POST /notes":
		return grAnswer{Status: 201, Body: `{"id":1}`}
	}
	return grAnswer{Status: 404, Body: `{"message":"404 Not Found"}`}
}

func (gl *grGitLab) requests() []grReq {
	gl.mu.Lock()
	defer gl.mu.Unlock()
	return slices.Clone(gl.reqs)
}

func (gl *grGitLab) labels() []string {
	var out []string
	for _, r := range gl.requests() {
		out = append(out, r.label())
	}
	return out
}

// ofKind returns the requests of one kind, in order.
func (gl *grGitLab) ofKind(kind string) []grReq {
	var out []grReq
	for _, r := range gl.requests() {
		if r.kind() == kind {
			out = append(out, r)
		}
	}
	return out
}

// ---------------------------------------------------------------- model stub

type grModel struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func grNewModel(t *testing.T, findings []map[string]any) *grModel {
	t.Helper()
	if findings == nil {
		findings = []map[string]any{}
	}
	m := &grModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(b))
		m.mu.Unlock()
		review, _ := json.Marshal(map[string]any{"verdict": "approve", "summary": "ok", "score": 90, "findings": findings})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "deepseek/deepseek-v4-flash",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(review)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "cost": 0.0025},
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *grModel) requestBodies() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.bodies)
}

// ---------------------------------------------------------------- findings

type grFinding struct {
	sev, cat, title, file string
	line                  int
	rationale             string
	suggestion            *string
	// unanchored findings carry a full-file kind and a line outside every hunk: grounding keeps
	// them, no inline comment can be placed on them.
	unanchored bool
}

func grStr(s string) *string { return &s }

func (f grFinding) json(i int) map[string]any {
	m := map[string]any{
		"id": "f" + strconv.Itoa(i), "severity": f.sev, "category": f.cat, "title": f.title, "file": f.file,
		"start_line": f.line, "end_line": f.line, "rationale": f.rationale, "confidence": 0.9, "kind": "finding",
	}
	if f.suggestion != nil {
		m["suggestion"] = *f.suggestion
	}
	if f.unanchored {
		m["kind"] = "phantom"
		m["start_line"], m["end_line"] = 999, 999
	}
	return m
}

func grJSONs(fs []grFinding) []map[string]any {
	out := make([]map[string]any, len(fs))
	for i, f := range fs {
		out[i] = f.json(i)
	}
	return out
}

func (f grFinding) startLine() int {
	if f.unanchored {
		return 999
	}
	return f.line
}

func grEmoji(sev string) string {
	switch sev {
	case "CRITICAL":
		return "🔴"
	case "WARNING":
		return "🟡"
	}
	return "🔵"
}

// grSummary is the summary body of Phase 6 7.8.7 for the findings in engine order, with an
// optional cap section between the last bullet and the closing line (spec 7.7), before the note
// body rules.
func grSummary(title string, fs []grFinding, capSection string) string {
	header := "## " + title
	if len(fs) == 0 {
		return header + " — Approved ✅\n\n_No findings. Looks good._"
	}
	var crit, warn, sugg int
	for _, f := range fs {
		switch f.sev {
		case "CRITICAL":
			crit++
		case "WARNING":
			warn++
		default:
			sugg++
		}
	}
	if crit > 0 { // ci_fail_on is critical in every test manifest
		header += " — Changes requested"
	}
	lines := make([]string, len(fs))
	for i, f := range fs {
		loc := f.file + ":" + strconv.Itoa(f.startLine())
		lines[i] = "- " + grEmoji(f.sev) + " **" + f.title + "** (" + strings.ToLower(f.sev) + ", " + f.cat + ") — `" + loc + "`\n  - " + f.rationale
		if f.suggestion != nil && *f.suggestion != "" {
			lines[i] += "\n  - _Suggestion:_ " + *f.suggestion
		}
	}
	noun := " findings"
	if len(fs) == 1 {
		noun = " finding"
	}
	out := header + "\n\n**" + strconv.Itoa(len(fs)) + noun + "** · " +
		strconv.Itoa(crit) + " critical · " + strconv.Itoa(warn) + " warning · " + strconv.Itoa(sugg) + " suggestion\n\n" +
		strings.Join(lines, "\n") + "\n\n"
	if capSection != "" {
		out += capSection + "\n\n"
	}
	return out + "_Posted via DevDigest._"
}

// grInline is the body of an inline comment (Phase 6 7.8.7).
func grInline(f grFinding) string {
	body := "**" + f.title + "** (" + strings.ToLower(f.sev) + ")\n\n" + f.rationale
	if f.suggestion != nil && *f.suggestion != "" {
		body += "\n\n_Suggestion:_ " + *f.suggestion
	}
	return body
}

func grRank(sev string) int {
	switch sev {
	case "CRITICAL":
		return 3
	case "WARNING":
		return 2
	case "SUGGESTION":
		return 1
	}
	return 0
}

// grOrder is the FR48 order of the anchored findings: severity descending, file path in byte order,
// start line ascending, then the engine's order.
func grOrder(fs []grFinding) []grFinding {
	var anchored []grFinding
	for _, f := range fs {
		if !f.unanchored {
			anchored = append(anchored, f)
		}
	}
	slices.SortStableFunc(anchored, func(a, b grFinding) int {
		return cmp.Or(cmp.Compare(grRank(b.sev), grRank(a.sev)), strings.Compare(a.file, b.file), cmp.Compare(a.line, b.line))
	})
	return anchored
}

// grCapSection is the section of spec 7.7 for the findings that are not posted inline.
func grCapSection(rest []grFinding) string {
	noun := " more findings"
	if len(rest) == 1 {
		noun = " more finding"
	}
	lines := []string{"_" + strconv.Itoa(len(rest)) + noun + " not posted inline (limit: 50 inline threads per run):_"}
	for _, f := range rest {
		lines = append(lines, "- "+grEmoji(f.sev)+" **"+f.title+"** ("+strings.ToLower(f.sev)+") — `"+f.file+":"+strconv.Itoa(f.line)+"`")
	}
	return strings.Join(lines, "\n")
}

// grBulk makes n anchored findings on the new files of the fixture: finding j (engine order) has
// the severity class and the anchor of a permutation of 0..n-1, so the engine order mixes severities,
// files and lines. The classes are 20 critical, 25 warning, the rest suggestions.
func grBulk(n int) []grFinding {
	out := make([]grFinding, n)
	for j := range n {
		p := (j * 37) % 73 // a bijection on 0..72
		a := (j * 11) % 73 // another one: the anchor
		sev := "SUGGESTION"
		switch {
		case p < 20:
			sev = "CRITICAL"
		case p < 45:
			sev = "WARNING"
		}
		out[j] = grFinding{
			sev: sev, cat: "bug", title: fmt.Sprintf("Finding %02d", j), file: grBulkFiles[a%len(grBulkFiles)], line: 1 + a/len(grBulkFiles),
			rationale: fmt.Sprintf("Reason %02d.", j), suggestion: grStr(fmt.Sprintf("Fix %02d.", j)),
		}
	}
	return out
}

// grAllSuggestions makes n anchored SUGGESTION findings on distinct lines (no critical: the gate
// stays closed).
func grAllSuggestions(n int) []grFinding {
	out := make([]grFinding, n)
	for j := range n {
		out[j] = grFinding{
			sev: "SUGGESTION", cat: "style", title: fmt.Sprintf("Nit %02d", j), file: grBulkFiles[j%len(grBulkFiles)], line: 1 + j/len(grBulkFiles),
			rationale: fmt.Sprintf("Nit reason %02d.", j),
		}
	}
	return out
}

// grThree are the findings of AC24 in engine order: a context line of a modified file, a context
// line of a renamed file and an added line.
func grThree() []grFinding {
	return []grFinding{
		{sev: "WARNING", cat: "bug", title: "Context line", file: "svc/handler.go", line: 8, rationale: "On an unchanged line.", suggestion: grStr("Use a constant.")},
		{sev: "CRITICAL", cat: "security", title: "Renamed file", file: "new/name.go", line: 3, rationale: "In a renamed file."},
		{sev: "SUGGESTION", cat: "style", title: "Added line", file: "svc/handler.go", line: 32, rationale: "On an added line.", suggestion: grStr("Rename it.")},
	}
}

// ---------------------------------------------------------------- running the runner

type grCase struct {
	findings []grFinding
	postAs   string // "" = github_review
	name     string // manifest name; "" = Security Reviewer
	// env overrides or adds environment variables.
	env map[string]string
}

type grOutcome struct {
	code           int
	stdout, stderr string
	artifact       string
	hasArtifact    bool
	model          *grModel
	title          string
}

func grRun(t *testing.T, fx grFixture, gl *grGitLab, c grCase) grOutcome {
	t.Helper()
	root := t.TempDir()
	title := cmp.Or(c.name, "Security Reviewer")
	manifest := "name: " + strconv.Quote(title) + "\nprovider: \"openrouter\"\nmodel: \"deepseek/deepseek-v4-flash\"\n" +
		"system_prompt: \"Review this PR.\"\nskills: []\nstrategy: \"single-pass\"\nci_fail_on: \"critical\"\n"
	grWrite(t, root, ".devdigest/agents/reviewer.yaml", manifest)
	model := grNewModel(t, grJSONs(c.findings))

	env := map[string]string{
		"DEVDIGEST_PLATFORM": "gitlab_ci", "GITLAB_CI": "true", "CI_PIPELINE_SOURCE": "merge_request_event",
		"CI_MERGE_REQUEST_IID": "7", "CI_PROJECT_ID": "42", "CI_MERGE_REQUEST_PROJECT_ID": "42", "CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "42",
		"CI_PROJECT_PATH": "acme/widgets", "CI_JOB_ID": "9001", "CI_PROJECT_DIR": fx.dir,
		"CI_MERGE_REQUEST_DIFF_BASE_SHA": fx.base, "CI_COMMIT_SHA": fx.head,
		"CI_MERGE_REQUEST_TITLE": "Add feature", "CI_MERGE_REQUEST_DESCRIPTION": "A cool feature.",
		"CI_API_V4_URL": gl.apiURL(), "CI_SERVER_TLS_CA_FILE": gl.caFile,
		"DEVDIGEST_GITLAB_TOKEN": grToken, "OPENROUTER_API_KEY": grORKey,
		"DEVDIGEST_DIR": filepath.Join(root, ".devdigest"), "DEVDIGEST_RESULT_PATH": filepath.Join(root, "devdigest-result.json"),
	}
	if c.postAs != "" {
		env["DEVDIGEST_POST_AS"] = c.postAs
	}
	for k, v := range c.env {
		env[k] = v
	}

	var stdout, stderr bytes.Buffer
	code := runProcess(t.Context(), envOf(env), &stdout, &stderr, endpoints{OpenRouter: model.srv.URL + "/v1"})
	out := grOutcome{code: code, stdout: stdout.String(), stderr: stderr.String(), model: model, title: title}
	data, err := os.ReadFile(env["DEVDIGEST_RESULT_PATH"])
	switch {
	case err == nil:
		out.artifact, out.hasArtifact = string(data), true
	case !os.IsNotExist(err):
		t.Fatal(err)
	}
	return out
}

// ---------------------------------------------------------------- body rules

// grLiveSlash reports whether a line starts, after the FR15 prefix, with a "/" (a quick action).
func grLiveSlash(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		s := line
		for {
			switch {
			case s == "":
			case s[0] == ' ' || s[0] == '\t' || s[0] == '>':
				s = s[1:]
				continue
			case strings.HasPrefix(s, "\ufeff"):
				s = strings.TrimPrefix(s, "\ufeff")
				continue
			case strings.HasPrefix(s, "\u00a0"):
				s = strings.TrimPrefix(s, "\u00a0")
				continue
			case (s[0] == '-' || s[0] == '*' || s[0] == '+') && len(s) > 1 && (s[1] == ' ' || s[1] == '\t'):
				s = s[2:]
				continue
			}
			if j := strings.IndexAny(s, ".)"); j >= 1 && j <= 9 && len(s) > j+1 && (s[j+1] == ' ' || s[j+1] == '\t') {
				if _, err := strconv.Atoi(s[:j]); err == nil {
					s = s[j+2:]
					continue
				}
			}
			break
		}
		if strings.HasPrefix(s, "/") {
			return true
		}
	}
	return false
}

var grEntity = regexp.MustCompile(`&#[0-9]+;|&#[xX][0-9a-fA-F]+;|&[A-Za-z][A-Za-z0-9]*;`)

// grAllowedLine reports whether line is one of the two raw HTML lines slice B leaves alone:
// "<sub>DevDigest · N% confidence</sub>" (N 1-3 digits) anywhere, or, as the last non-empty line,
// "<!-- devdigest-finding:ID -->" (ID 1-64 of [A-Za-z0-9_-]).
func grAllowedLine(line string, last bool) bool {
	const subPre, subSuf = "<sub>DevDigest \u00b7 ", "% confidence</sub>"
	if rest, ok := strings.CutPrefix(line, subPre); ok {
		if digits, ok := strings.CutSuffix(rest, subSuf); ok && len(digits) >= 1 && len(digits) <= 3 {
			if _, err := strconv.Atoi(digits); err == nil && !strings.ContainsAny(digits, "+-") {
				return true
			}
		}
	}
	const markPre, markSuf = "<!-- devdigest-finding:", " -->"
	if rest, ok := strings.CutPrefix(line, markPre); ok && last {
		if id, ok := strings.CutSuffix(rest, markSuf); ok && len(id) >= 1 && len(id) <= 64 && strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == "" {
			return true
		}
	}
	return false
}

// grResidue lists what the note body rules must have removed from a sent body (slice B's residue
// rule): an "@" without the U+200B after it, a "<" without it (outside the two allowed lines),
// "![", a character reference that decodes to "@", "<", "!" or "[" (the "&lt;" Apply writes for
// "<" excepted), and a live quick action.
func grResidue(body string) []string {
	var found []string
	lines := strings.Split(body, "\n")
	lastNonEmpty := -1
	for i, l := range lines {
		if strings.Trim(l, " \t") != "" {
			lastNonEmpty = i
		}
	}
	for n, line := range lines {
		allowed := grAllowedLine(line, n == lastNonEmpty)
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '@':
				if !strings.HasPrefix(line[i+1:], notebody.ZWSP) {
					found = append(found, "@ in line "+strconv.Itoa(n))
				}
			case '<':
				if !allowed && !strings.HasPrefix(line[i+1:], notebody.ZWSP) {
					found = append(found, "< in line "+strconv.Itoa(n))
				}
			case '!':
				if strings.HasPrefix(line[i+1:], "[") {
					found = append(found, "![ in line "+strconv.Itoa(n))
				}
			}
		}
	}
	for _, m := range grEntity.FindAllString(body, -1) {
		if m != "&lt;" && strings.ContainsAny(html.UnescapeString(m), "@<![") {
			found = append(found, "entity "+m)
		}
	}
	if grLiveSlash(body) {
		found = append(found, "live quick action")
	}
	return found
}

func grScrub(s string) string {
	return strings.NewReplacer(grToken, "[REDACTED]", grORKey, "[REDACTED]").Replace(s)
}

// grWantBody is the body the runner must send for an assembled body: secrets redacted, then
// slice B's rules once.
func grWantBody(assembled string) string { return notebody.Apply(grScrub(assembled)) }

// grCheckBody asserts the sent text is exactly want, stable under the rules and free of residue.
func grCheckBody(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s body mismatch (-want +got):\n%s", what, gocmp.Diff(want, got))
	}
	if again := notebody.Apply(got); again != got {
		t.Errorf("%s: Apply(sent) != sent (the rules are not idempotent on it)", what)
	}
	if res := grResidue(got); len(res) > 0 {
		t.Errorf("%s: residue in the sent body: %v", what, res)
	}
	if strings.Contains(got, grToken) || strings.Contains(got, grORKey) {
		t.Errorf("%s: a secret reached the body", what)
	}
}

func grJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("request body %q is not JSON: %v", raw, err)
	}
	return m
}

// grCheckRequests asserts what holds for every request of every scenario: it went to the API
// origin under the CI_API_V4_URL prefix, addressed the numeric project and the merge request, carried
// the token only in PRIVATE-TOKEN, and sent no reviewer_state, approval or foreign endpoint.
func grCheckRequests(t *testing.T, gl *grGitLab) {
	t.Helper()
	wantHost := strings.TrimPrefix(gl.srv.URL, "https://")
	for _, r := range gl.requests() {
		if !strings.HasPrefix(r.Path, gl.prefix+"/projects/42/merge_requests/7") {
			t.Errorf("%s %s: outside %s/projects/42/merge_requests/7", r.Method, r.Path, gl.prefix)
		}
		if r.Token != grToken {
			t.Errorf("%s: PRIVATE-TOKEN = %q, want the job token", r.label(), r.Token)
		}
		if r.Auth != "" {
			t.Errorf("%s: Authorization header %q sent", r.label(), r.Auth)
		}
		if r.Host != wantHost {
			t.Errorf("%s: Host = %q, want %q", r.label(), r.Host, wantHost)
		}
		if strings.Contains(r.Path+r.Query+r.Body, grToken) {
			t.Errorf("%s: the token is in the URL or the body", r.label())
		}
		for _, bad := range []string{`"reviewer_state"`, `"approve"`, `"approved"`, `"state"`} {
			if strings.Contains(r.Body, bad) {
				t.Errorf("%s: body holds %s: %s", r.label(), bad, r.Body)
			}
		}
		if r.label() == "POST /draft_notes/bulk_publish" || strings.HasSuffix(r.label(), "/publish") || r.Method == http.MethodDelete || r.Method == http.MethodGet {
			if r.Body != "" {
				t.Errorf("%s: body %q, want none", r.label(), r.Body)
			}
		}
	}
}

// grCheckQuiet asserts a secret appears nowhere the runner writes or sends besides the GitLab header.
func grCheckQuiet(t *testing.T, o grOutcome) {
	t.Helper()
	for name, text := range map[string]string{"stdout": o.stdout, "stderr": o.stderr, "artifact": o.artifact, "model request": strings.Join(o.model.requestBodies(), "\n")} {
		if strings.Contains(text, grToken) {
			t.Errorf("the GitLab token is in the %s", name)
		}
	}
}

// grDrafts returns the notes of the draft creates in order, decoded.
func grDrafts(t *testing.T, gl *grGitLab) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range gl.ofKind("POST /draft_notes") {
		out = append(out, grJSON(t, r.Body))
	}
	return out
}

func grLabels(parts ...[]string) []string { return slices.Concat(parts...) }

func grRepeat(label string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = label
	}
	return out
}

func grIDs(from, n int, format string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(format, from+i)
	}
	return out
}

// grPos is the position object of a positioned draft.
func grPos(fx grFixture, newPath, oldPath string, newLine, oldLine int) map[string]any {
	m := map[string]any{
		"position_type": "text", "base_sha": fx.base, "start_sha": grStart, "head_sha": fx.head,
		"new_path": newPath, "old_path": oldPath, "new_line": float64(newLine),
	}
	if oldLine > 0 {
		m["old_line"] = float64(oldLine)
	}
	return m
}

func grWantPositioned(t *testing.T, fx grFixture, gl *grGitLab, ordered []grFinding) {
	t.Helper()
	drafts := grDrafts(t, gl)
	if len(drafts) != len(ordered)+1 {
		t.Fatalf("%d drafts created, want %d (the summary and %d positioned)", len(drafts), len(ordered)+1, len(ordered))
	}
	for i, f := range ordered {
		d := drafts[i+1]
		grCheckBody(t, "positioned draft "+f.title, fmt.Sprint(d["note"]), grWantBody(grInline(f)))
		pos, _ := d["position"].(map[string]any)
		want := grPos(fx, f.file, f.file, f.line, 0)
		switch f.title {
		case "Context line":
			want = grPos(fx, f.file, f.file, 8, 6)
		case "Renamed file":
			want = grPos(fx, "new/name.go", "old/name.go", 3, 3)
		}
		if diff := gocmp.Diff(want, pos); diff != "" {
			t.Errorf("draft %q position mismatch (-want +got):\n%s", f.title, diff)
		}
		if len(d) != 2 {
			t.Errorf("draft %q has keys %v, want note and position only", f.title, d)
		}
	}
}

func grTitles(fs []grFinding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.title
	}
	return out
}

// ================================================================ AC24

// The MR review: MR read, drafts list, the summary draft, the positioned drafts in the FR48 order
// with the position rules (context line: old_line and new_line; renamed file: old_path != new_path;
// added line: no old_line), one bulk publish without a body; the gate triggers, so the job fails
// with the artifact written and the posting done.
func TestGRPost_AC24_MRReview(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	fs := grThree()
	o := grRun(t, fx, gl, grCase{findings: fs})

	wantLabels := []string{
		"GET mr", "GET /draft_notes",
		"POST /draft_notes", "POST /draft_notes", "POST /draft_notes", "POST /draft_notes",
		"POST /draft_notes/bulk_publish",
	}
	if diff := gocmp.Diff(wantLabels, gl.labels()); diff != "" {
		t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
	reqs := gl.requests()
	if reqs[1].Query != "per_page=100" {
		t.Errorf("drafts list query = %q, want per_page=100", reqs[1].Query)
	}
	if reqs[6].Body != "" {
		t.Errorf("bulk_publish body = %q, want none", reqs[6].Body)
	}

	drafts := grDrafts(t, gl)
	if diff := gocmp.Diff([]string{"note"}, grKeys(drafts[0])); diff != "" {
		t.Errorf("summary draft keys mismatch (-want +got):\n%s", diff)
	}
	grCheckBody(t, "summary draft", fmt.Sprint(drafts[0]["note"]), grWantBody(grSummary(o.title, fs, "")))
	// FR48 order: critical, then warning, then suggestion
	grWantPositioned(t, fx, gl, grOrder(fs))
	if got := grTitlesOfDrafts(drafts[1:]); !slices.Equal(got, []string{"Renamed file", "Context line", "Added line"}) {
		t.Errorf("positioned draft order = %v, want [Renamed file Context line Added line]", got)
	}

	if o.code != 1 {
		t.Errorf("exit code = %d, want 1 (a critical finding triggers the gate)\nstderr: %s", o.code, o.stderr)
	}
	if !strings.Contains(o.stdout, "findings=3 blockers=1 gateTriggered=true posted=github_review") {
		t.Errorf("stdout = %q, want the result line", o.stdout)
	}
	if strings.Contains(o.stdout, "::") {
		t.Errorf("stdout = %q: GitLab mode prints no workflow command line", o.stdout)
	}
	if !o.hasArtifact || !strings.Contains(o.artifact, `"findings_count": 3`) {
		t.Errorf("artifact = %q, %v; want it written with 3 findings", o.artifact, o.hasArtifact)
	}
	grCheckRequests(t, gl)
	grCheckQuiet(t, o)
}

func grKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// grTitlesOfDrafts reads the finding titles from the notes of inline drafts ("**<title>** (...").
func grTitlesOfDrafts(drafts []map[string]any) []string {
	var out []string
	for _, d := range drafts {
		note := fmt.Sprint(d["note"])
		title, _, _ := strings.Cut(strings.TrimPrefix(note, "**"), "** (")
		out = append(out, title)
	}
	return out
}

// No blocker: the job succeeds and still posts.
func TestGRPost_AC23_NoBlockerExitsZero(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	fs := grAllSuggestions(2)
	o := grRun(t, fx, gl, grCase{findings: fs})
	if o.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", o.code, o.stderr)
	}
	if !strings.Contains(o.stdout, "findings=2 blockers=0 gateTriggered=false posted=github_review") {
		t.Errorf("stdout = %q, want the result line", o.stdout)
	}
	want := grLabels([]string{"GET mr", "GET /draft_notes"}, grRepeat("POST /draft_notes", 3), []string{"POST /draft_notes/bulk_publish"})
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Errorf("GitLab requests mismatch (-want +got):\n%s", diff)
	}
	if !o.hasArtifact {
		t.Error("no artifact")
	}
}

// pr_comment: one note with the summary body, nothing else, no cap section whatever the count.
func TestGRPost_AC24_AC47_PRComment(t *testing.T) {
	for _, n := range []int{3, 73} {
		t.Run(fmt.Sprintf("%d findings", n), func(t *testing.T) {
			fx := grNewFixture(t)
			gl := grNewGitLab(t, fx)
			fs := grThree()
			if n == 73 {
				fs = grBulk(73)
			}
			o := grRun(t, fx, gl, grCase{findings: fs, postAs: "pr_comment"})
			if diff := gocmp.Diff([]string{"POST /notes"}, gl.labels()); diff != "" {
				t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
			}
			body := grJSON(t, gl.requests()[0].Body)
			if diff := gocmp.Diff([]string{"body"}, grKeys(body)); diff != "" {
				t.Errorf("note keys mismatch (-want +got):\n%s", diff)
			}
			note := fmt.Sprint(body["body"])
			grCheckBody(t, "note", note, grWantBody(grSummary(o.title, fs, "")))
			if strings.Contains(note, "not posted inline") || strings.Contains(o.stdout, "capped") {
				t.Errorf("pr_comment shows a cap (note %q, stdout %q)", note, o.stdout)
			}
			if got := strings.Count(note, "\n- "); got != n {
				t.Errorf("note lists %d finding bullets, want %d", got, n)
			}
			if o.code != 1 || !o.hasArtifact {
				t.Errorf("exit %d, artifact %v; want 1 (critical) with the artifact", o.code, o.hasArtifact)
			}
			grCheckRequests(t, gl)
		})
	}
}

// ================================================================ AC46, AC47

// 73 anchored findings in mixed order: 1 summary draft and exactly 50 positioned drafts (the 20
// critical, the 25 warning, the first 5 suggestions, each group by file path in byte order then
// start line), one bulk publish; the summary lists all 73 in engine order, then the cap section
// of the 23 others in the FR48 order, then the closing line; the cap line is in the job log.
func TestGRPost_AC46_CapOf73(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	fs := grBulk(73)
	o := grRun(t, fx, gl, grCase{findings: fs})

	ordered := grOrder(fs)
	var classes [3]int
	for _, f := range ordered[:50] {
		classes[3-grRank(f.sev)]++
	}
	if classes != [3]int{20, 25, 5} {
		t.Fatalf("test data: first 50 hold %v critical/warning/suggestion, want [20 25 5]", classes)
	}
	want := grLabels([]string{"GET mr", "GET /draft_notes"}, grRepeat("POST /draft_notes", 51), []string{"POST /draft_notes/bulk_publish"})
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
	if n := len(gl.requests()) - 2; n > 53 {
		t.Errorf("draft creates plus publishes = %d, want <= 53", n)
	}
	drafts := grDrafts(t, gl)
	capSection := grCapSection(ordered[50:])
	if first, _, _ := strings.Cut(capSection, "\n"); first != "_23 more findings not posted inline (limit: 50 inline threads per run):_" {
		t.Fatalf("test data: cap first line %q", first)
	}
	grCheckBody(t, "summary draft", fmt.Sprint(drafts[0]["note"]), grWantBody(grSummary(o.title, fs, capSection)))
	grWantPositioned(t, fx, gl, ordered[:50])

	summary := fmt.Sprint(drafts[0]["note"])
	if got := strings.Count(summary, "\n- "); got != 73+23 {
		t.Errorf("summary holds %d bullets, want 73 findings + 23 cap bullets", got)
	}
	if !strings.HasSuffix(summary, "\n\n_Posted via DevDigest._") {
		t.Errorf("summary does not end with the closing line: %q", summary[max(0, len(summary)-80):])
	}
	if !strings.Contains(o.stdout, "[agent-runner] inline comments capped: posted=50 not_posted=23\n") {
		t.Errorf("stdout = %q, want the cap line", o.stdout)
	}
	if o.code != 1 || !o.hasArtifact {
		t.Errorf("exit %d, artifact %v; want 1 (critical) with the artifact", o.code, o.hasArtifact)
	}
	grCheckRequests(t, gl)
}

// The boundaries of the cap (EC36): 50 -> no cap; 51 -> one bullet; 15 unanchored of 60 -> 45
// drafts and no cap section; GitHub mode (DEVDIGEST_PLATFORM unset) -> every inline comment.
func TestGRPost_AC47_CapBoundaries(t *testing.T) {
	unanchored := func(n int) []grFinding {
		out := make([]grFinding, n)
		for i := range out {
			out[i] = grFinding{sev: "WARNING", cat: "bug", title: fmt.Sprintf("Loose %02d", i), file: "pkg/a.go", rationale: "No anchor line.", unanchored: true}
		}
		return out
	}
	tests := []struct {
		name         string
		findings     []grFinding
		wantDrafts   int // positioned
		wantCapped   int // findings in the cap section
		wantBullets  int // finding bullets of the summary
		wantCapLines string
	}{
		{"exactly 50", grAllSuggestions(50), 50, 0, 50, ""},
		{"51", grAllSuggestions(51), 50, 1, 51 + 1, "[agent-runner] inline comments capped: posted=50 not_posted=1"},
		{"60 with 15 unanchored", slices.Concat(grAllSuggestions(45), unanchored(15)), 45, 0, 60, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := grNewFixture(t)
			gl := grNewGitLab(t, fx)
			o := grRun(t, fx, gl, grCase{findings: tc.findings})
			if o.code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", o.code, o.stderr)
			}
			ordered := grOrder(tc.findings)
			if len(ordered[:min(len(ordered), 50)]) != tc.wantDrafts {
				t.Fatalf("test data: %d anchored", len(ordered))
			}
			wantLabels := grLabels([]string{"GET mr", "GET /draft_notes"}, grRepeat("POST /draft_notes", tc.wantDrafts+1), []string{"POST /draft_notes/bulk_publish"})
			if diff := gocmp.Diff(wantLabels, gl.labels()); diff != "" {
				t.Fatalf("GitLab requests mismatch (-want +got):\n%s", diff)
			}
			capSection := ""
			if tc.wantCapped > 0 {
				capSection = grCapSection(ordered[50:])
			}
			summary := fmt.Sprint(grDrafts(t, gl)[0]["note"])
			grCheckBody(t, "summary draft", summary, grWantBody(grSummary(o.title, tc.findings, capSection)))
			if got := strings.Count(summary, "\n- "); got != tc.wantBullets {
				t.Errorf("summary has %d bullets, want %d", got, tc.wantBullets)
			}
			hasCapText := strings.Contains(summary, "not posted inline")
			if hasCapText != (tc.wantCapped > 0) {
				t.Errorf("cap section present = %v, want %v", hasCapText, tc.wantCapped > 0)
			}
			if tc.wantCapped == 1 && !strings.Contains(summary, "_1 more finding not posted inline (limit: 50 inline threads per run):_") {
				t.Errorf("summary lacks the singular cap line")
			}
			if tc.wantCapLines == "" && strings.Contains(o.stdout, "capped") {
				t.Errorf("stdout = %q, want no cap line", o.stdout)
			}
			if tc.wantCapLines != "" && !strings.Contains(o.stdout, tc.wantCapLines+"\n") {
				t.Errorf("stdout = %q, want %q", o.stdout, tc.wantCapLines)
			}
		})
	}
}

// With DEVDIGEST_PLATFORM unset the runner is the GitHub runner: 73 findings give a review with
// every inline comment, no cap and no cap line.
func TestGRPost_AC47_GitHubModeKeepsEveryInlineComment(t *testing.T) {
	fx := grNewFixture(t)
	raw, err := gitdiff.New(fx.dir).Diff(t.Context(), fx.base, fx.head)
	if err != nil {
		t.Fatal(err)
	}
	gh := newGitHubStub(t, func(method, _ string, _ int) stubResp {
		if method == http.MethodGet {
			return stubResp{Status: 200, Text: raw}
		}
		return stubResp{Status: 200, Text: "{}"}
	})
	fs := grBulk(73)
	w := newWorkspace(t, testManifest, nil)
	model := grNewModel(t, grJSONs(fs))
	var stdout, stderr bytes.Buffer
	code := runProcess(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: model.srv.URL + "/v1", GitHub: gh.srv.URL})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, &stderr)
	}
	reqs := gh.requests()
	if len(reqs) != 2 {
		t.Fatalf("GitHub requests = %d, want the diff and one review", len(reqs))
	}
	review := reqs[1].Body.(map[string]any)
	if comments, _ := review["comments"].([]any); len(comments) != 73 {
		t.Errorf("review comments = %d, want 73", len(comments))
	}
	if body := fmt.Sprint(review["body"]); strings.Contains(body, "not posted inline") {
		t.Errorf("GitHub review body has a cap section: %q", body)
	}
	if strings.Contains(stdout.String(), "capped") {
		t.Errorf("stdout = %q, want no cap line", stdout.String())
	}
}

// ================================================================ AC25

// Foreign pending drafts: the runner publishes only its own, one by one, summary first and then in
// the FR48 order; it neither deletes nor publishes the foreign ones.
func TestGRPost_AC25_ForeignDraftsPublishOneByOne(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.existing = []int{71, 72}
	fs := grThree()
	o := grRun(t, fx, gl, grCase{findings: fs})

	want := grLabels(
		[]string{"GET mr", "GET /draft_notes"}, grRepeat("POST /draft_notes", 4),
		grIDs(1000, 4, "PUT /draft_notes/%d/publish"),
	)
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
	for _, r := range gl.requests() {
		if strings.Contains(r.Path, "/71") || strings.Contains(r.Path, "/72") || strings.HasSuffix(r.Path, "bulk_publish") {
			t.Errorf("%s touches a foreign draft or bulk-publishes", r.label())
		}
	}
	// the publishes follow the creates: summary first, then the FR48 order
	grWantPositioned(t, fx, gl, grOrder(fs))
	if o.code != 1 || !o.hasArtifact {
		t.Errorf("exit %d, artifact %v; want 1 (critical) with the artifact", o.code, o.hasArtifact)
	}
	grCheckRequests(t, gl)
}

// A merge request that moved on (diff_refs null or another head, or incomplete): the summary alone,
// without a cap section, and the skip line.
func TestGRPost_AC25_AC47_ChangedMergeRequestPostsTheSummaryOnly(t *testing.T) {
	for name, mr := range map[string]func(fx grFixture) string{
		"diff_refs null":    func(grFixture) string { return `{"iid":7,"diff_refs":null}` },
		"diff_refs missing": func(grFixture) string { return `{"iid":7}` },
		"other head": func(fx grFixture) string {
			return `{"iid":7,"diff_refs":{"base_sha":"` + fx.base + `","start_sha":"` + grStart + `","head_sha":"` + strings.Repeat("c", 40) + `"}}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			fx := grNewFixture(t)
			gl := grNewGitLab(t, fx)
			gl.mr = mr(fx)
			fs := grBulk(73)
			o := grRun(t, fx, gl, grCase{findings: fs})

			want := []string{"GET mr", "GET /draft_notes", "POST /draft_notes", "POST /draft_notes/bulk_publish"}
			if diff := gocmp.Diff(want, gl.labels()); diff != "" {
				t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
			}
			draft := grJSON(t, gl.requests()[2].Body)
			if diff := gocmp.Diff([]string{"note"}, grKeys(draft)); diff != "" {
				t.Errorf("summary draft keys mismatch (-want +got):\n%s", diff)
			}
			note := fmt.Sprint(draft["note"])
			grCheckBody(t, "summary draft", note, grWantBody(grSummary(o.title, fs, "")))
			if strings.Contains(note, "not posted inline") {
				t.Error("the summary has a cap section although no thread was published")
			}
			skip := "[agent-runner] inline comments skipped: the merge request changed since this pipeline started\n"
			if !strings.Contains(o.stdout, skip) {
				t.Errorf("stdout = %q, want the skip line", o.stdout)
			}
			if strings.Contains(o.stdout, "capped") {
				t.Errorf("stdout = %q, want no cap line", o.stdout)
			}
			if o.code != 1 || !o.hasArtifact {
				t.Errorf("exit %d, artifact %v; want 1 (critical) with the artifact", o.code, o.hasArtifact)
			}
			grCheckRequests(t, gl)
		})
	}
}

// ================================================================ AC26, AC47

// A 400 on a positioned draft: this run's drafts are deleted, the summary is created again alone
// (no cap section) and published once; nothing is retried again.
func TestGRPost_AC26_AC47_PositionedDraft400(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.answer = func(_ grReq, _ int) (grAnswer, bool) { return grAnswer{}, false }
	gl.answer = func(r grReq, n int) (grAnswer, bool) {
		if r.kind() == "POST /draft_notes" && n == 3 { // the summary and two positioned drafts exist
			return grAnswer{Status: 400, Body: `{"message":"line_code is invalid"}`}, true
		}
		return grAnswer{}, false
	}
	fs := grBulk(73)
	o := grRun(t, fx, gl, grCase{findings: fs})

	want := grLabels(
		[]string{"GET mr", "GET /draft_notes"}, grRepeat("POST /draft_notes", 4),
		[]string{"DELETE /draft_notes/1000", "DELETE /draft_notes/1001", "DELETE /draft_notes/1002"},
		[]string{"POST /draft_notes", "POST /draft_notes/bulk_publish"},
	)
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
	creates := gl.ofKind("POST /draft_notes")
	again := grJSON(t, creates[4].Body)
	if diff := gocmp.Diff([]string{"note"}, grKeys(again)); diff != "" {
		t.Errorf("re-created summary keys mismatch (-want +got):\n%s", diff)
	}
	note := fmt.Sprint(again["note"])
	grCheckBody(t, "re-created summary", note, grWantBody(grSummary(o.title, fs, "")))
	if strings.Contains(note, "not posted inline") || strings.Contains(o.stdout, "capped") {
		t.Errorf("cap section or cap line after the 400 (stdout %q)", o.stdout)
	}
	if o.code != 1 || !o.hasArtifact {
		t.Errorf("exit %d, artifact %v; want 1 (critical) with the artifact", o.code, o.hasArtifact)
	}
	grCheckRequests(t, gl)
}

// If one draft of this run cannot be deleted after a 400, bulk publish would publish it: the runner
// publishes the re-created summary alone, by id.
func TestGRPost_AC26_UndeletableDraftIsNeverBulkPublished(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.answer = func(r grReq, n int) (grAnswer, bool) {
		switch {
		case r.kind() == "POST /draft_notes" && n == 1:
			return grAnswer{Status: 400, Body: `{"message":"bad position"}`}, true
		case r.kind() == "DELETE /draft_notes/N":
			return grAnswer{Status: 500, Body: `{"message":"boom"}`}, true
		}
		return grAnswer{}, false
	}
	o := grRun(t, fx, gl, grCase{findings: grThree()})
	want := []string{
		"GET mr", "GET /draft_notes", "POST /draft_notes", "POST /draft_notes",
		"DELETE /draft_notes/1000", "POST /draft_notes", "PUT /draft_notes/1001/publish",
	}
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Errorf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
}

// bulk_publish answers 500: the unpublished drafts of this run are deleted, the FR28 error line has
// the path and no host, the job fails and the artifact stays.
func TestGRPost_AC26_BulkPublishFailure(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.answer = func(r grReq, _ int) (grAnswer, bool) {
		if r.kind() == "POST /draft_notes/bulk_publish" {
			return grAnswer{Status: 500, Body: `{"message":"500 Internal Server Error"} ` + grToken}, true
		}
		return grAnswer{}, false
	}
	o := grRun(t, fx, gl, grCase{findings: grAllSuggestions(3)})

	want := grLabels(
		[]string{"GET mr", "GET /draft_notes"}, grRepeat("POST /draft_notes", 4),
		[]string{"POST /draft_notes/bulk_publish"}, grIDs(1000, 4, "DELETE /draft_notes/%d"),
	)
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Fatalf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
	line := "[agent-runner] FAILED: GitLab API error posting MR review (" + grPrefix + grMRPath + "/draft_notes/bulk_publish): 500 {\"message\":\"500 Internal Server Error\"} [REDACTED]"
	if !strings.Contains(o.stderr, line+"\n") {
		t.Errorf("stderr = %q, want the line %q", o.stderr, line)
	}
	if strings.Contains(o.stderr, strings.TrimPrefix(gl.srv.URL, "https://")) || strings.Contains(o.stderr, "127.0.0.1") {
		t.Errorf("stderr names the host: %q", o.stderr)
	}
	if o.code != 1 {
		t.Errorf("exit code = %d, want 1", o.code)
	}
	if !o.hasArtifact || !strings.Contains(o.artifact, `"findings_count": 3`) {
		t.Errorf("artifact = %q, %v; want it kept", o.artifact, o.hasArtifact)
	}
	grCheckRequests(t, gl)
	grCheckQuiet(t, o)
}

// 429 on a draft create: a write is never retried; this run's drafts are deleted, exit 1, the
// artifact stays.
func TestGRPost_AC47_RateLimitedDraftCreate(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.answer = func(r grReq, n int) (grAnswer, bool) {
		if r.kind() == "POST /draft_notes" && n == 1 {
			return grAnswer{Status: 429, Body: `{"message":"Retry later"}`}, true
		}
		return grAnswer{}, false
	}
	o := grRun(t, fx, gl, grCase{findings: grThree()})
	want := []string{"GET mr", "GET /draft_notes", "POST /draft_notes", "POST /draft_notes", "DELETE /draft_notes/1000"}
	if diff := gocmp.Diff(want, gl.labels()); diff != "" {
		t.Errorf("GitLab requests mismatch (-want +got):\n%s\nstderr: %s", diff, o.stderr)
	}
	if o.code != 1 || !o.hasArtifact {
		t.Errorf("exit %d, artifact %v; want 1 with the artifact", o.code, o.hasArtifact)
	}
	if !strings.Contains(o.stderr, "[agent-runner] FAILED: GitLab API error posting MR review ("+grPrefix+grMRPath+"/draft_notes): 429 ") {
		t.Errorf("stderr = %q, want the 429 line", o.stderr)
	}
}

// A failed pr_comment note fails the job after the artifact.
func TestGRPost_PRCommentFailure(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.answer = func(r grReq, _ int) (grAnswer, bool) {
		return grAnswer{Status: 403, Body: `{"message":"403 Forbidden"}`}, r.kind() == "POST /notes"
	}
	o := grRun(t, fx, gl, grCase{findings: grAllSuggestions(1), postAs: "pr_comment"})
	line := "[agent-runner] FAILED: GitLab API error posting MR comment (" + grPrefix + grMRPath + "/notes): 403 {\"message\":\"403 Forbidden\"}\n"
	if o.code != 1 || !strings.Contains(o.stderr, line) || !o.hasArtifact {
		t.Errorf("exit %d, artifact %v, stderr %q; want 1, the artifact and %q", o.code, o.hasArtifact, o.stderr, line)
	}
}

// ================================================================ redirects, origin, prefix

// The token goes only to the API origin and path prefix (here a prefix below the root), and a
// redirect is never followed: the other server, which the runner trusts as much as the stub, sees
// nothing.
func TestGRPost_RedirectsAreNeverFollowed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		redirect func(r grReq) bool
		postAs   string
		wantErr  string
	}{
		{"merge request read", func(r grReq) bool { return r.kind() == "GET mr" }, "", "GitLab API error posting MR review (/gl/api/v4" + grMRPath + "): 302 "},
		{"draft create", func(r grReq) bool { return r.kind() == "POST /draft_notes" }, "", "GitLab API error posting MR review (/gl/api/v4" + grMRPath + "/draft_notes): 302 "},
		{"note create", func(r grReq) bool { return r.kind() == "POST /notes" }, "pr_comment", "GitLab API error posting MR comment (/gl/api/v4" + grMRPath + "/notes): 302 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := grNewFixture(t)
			gl := grNewGitLab(t, fx)
			gl.prefix = "/gl/api/v4"
			var mu sync.Mutex
			var evilReqs []string
			evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				evilReqs = append(evilReqs, r.Method+" "+r.URL.Path+" token="+r.Header.Get("PRIVATE-TOKEN"))
				mu.Unlock()
			}))
			t.Cleanup(evil.Close)
			gl.answer = func(r grReq, _ int) (grAnswer, bool) {
				if tc.redirect(r) {
					return grAnswer{Status: 302, Location: evil.URL + "/stolen"}, true
				}
				return grAnswer{}, false
			}
			o := grRun(t, fx, gl, grCase{findings: grThree(), postAs: tc.postAs})
			if o.code != 1 {
				t.Errorf("exit code = %d, want 1", o.code)
			}
			if !strings.Contains(o.stderr, "[agent-runner] FAILED: "+tc.wantErr) {
				t.Errorf("stderr = %q, want a FAILED line with %q", o.stderr, tc.wantErr)
			}
			mu.Lock()
			if len(evilReqs) != 0 {
				t.Errorf("the redirect target received %v", evilReqs)
			}
			mu.Unlock()
			if !o.hasArtifact {
				t.Error("the artifact was not kept")
			}
			if strings.Contains(o.stderr, strings.TrimPrefix(evil.URL, "https://")) {
				t.Errorf("stderr echoes the Location: %q", o.stderr)
			}
			grCheckRequests(t, gl)
			grCheckQuiet(t, o)
		})
	}
}

// With a path prefix on CI_API_V4_URL every call stays under it (a request that leaves it is
// refused before it is sent: the stub sees none).
func TestGRPost_RequestsStayUnderTheAPIPrefix(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.prefix = "/gitlab/api/v4"
	o := grRun(t, fx, gl, grCase{findings: grThree()})
	if o.code != 1 || !o.hasArtifact {
		t.Fatalf("exit %d, artifact %v; stderr %s", o.code, o.hasArtifact, o.stderr)
	}
	reqs := gl.requests()
	if len(reqs) != 7 {
		t.Fatalf("%d requests, want 7", len(reqs))
	}
	for _, r := range reqs {
		if !strings.HasPrefix(r.Path, "/gitlab/api/v4/projects/42/merge_requests/7") {
			t.Errorf("%s %s: outside the CI_API_V4_URL prefix", r.Method, r.Path)
		}
	}
}

// ================================================================ AC27

// grHostile are the EC24 cases of the spec and the shapes of AC27 as text of a finding.
var grHostile = []string{
	"Fix it.\n/approve\n  /merge\n\t> - /close\n\ufeff/label ~x\n 1) /assign @me",
	"\u00a0/close\n> /merge\n- /approve\n* /close\n+ /assign",
	"ping @alice and @group/sub or `@user` or mail me@example.com",
	"<img src=x onerror=alert(1)> and <!-- hide",
	"![p](https://t/x.png) and ![q][ref]",
	"line one\r\n/close\r/merge\r\n\r\n/approve",
	"```\n/merge @bob\n<b>x</b>\n![i](u)\n```\n/close",
	"an `unclosed backtick run\n/approve @all\n<script>",
	"~~~\n/close\n~~~\n    /indented @x",
	"&#64;alice &#x40;bob &commat; &lt;b&gt; &#60;i&#62; &#33;&#91;x]",
	"```" + grToken + "``` and " + grORKey + " and /close " + grToken,
	"/close @bob",
}

// grCorpus reads slice B's EC15 corpus inputs.
func grCorpus(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "gitlab", "notebody", "testdata", "ec15.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ In string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 100 {
		t.Fatalf("EC15 corpus has %d cases", len(cases))
	}
	out := make([]string, len(cases))
	for i, c := range cases {
		out[i] = c.In
	}
	return out
}

func grHostileFindings(t *testing.T) []grFinding {
	t.Helper()
	corpus := grCorpus(t)
	const per = 12
	var out []grFinding
	for k := 0; k*per < len(corpus) && k < 12; k++ {
		in := corpus[k*per : min((k+1)*per, len(corpus))]
		anchor := grBulkFiles[k%len(grBulkFiles)]
		out = append(out, grFinding{
			sev: "SUGGESTION", cat: "bug", title: "T" + strconv.Itoa(k) + " " + grHostile[k%len(grHostile)],
			file: anchor, line: 1 + k/len(grBulkFiles), rationale: strings.Join(in, "\n\n"),
			suggestion: grStr(grHostile[(k+5)%len(grHostile)]),
		})
	}
	// the secrets of the run inside the text of findings
	out[1].rationale += "\nleak " + grToken + " and " + grORKey
	out[2].suggestion = grStr("use " + grToken)
	// the EC24 texts verbatim, as rationales on their own findings
	for i, h := range grHostile {
		out = append(out, grFinding{
			sev: "SUGGESTION", cat: "style", title: "H" + strconv.Itoa(i), file: "pkg/c.go", line: 1 + i, rationale: h,
		})
	}
	return out
}

// Every body the runner sends - the summary draft, every positioned draft and the pr_comment note
// - equals slice B's rules applied to the whole assembled body (secrets redacted first), applying
// the rules again changes nothing, and nothing live is left (no "@", "<", "![", quick action).
func TestGRPost_AC27_BodiesPassTheNoteBodyRules(t *testing.T) {
	fs := grHostileFindings(t)
	name := "/close @bob"
	t.Run("MR review", func(t *testing.T) {
		fx := grNewFixture(t)
		gl := grNewGitLab(t, fx)
		use := fs // all inline: no cap
		o := grRun(t, fx, gl, grCase{findings: use, name: name})
		if o.code != 0 || !o.hasArtifact {
			t.Fatalf("exit %d, artifact %v; stderr %s", o.code, o.hasArtifact, o.stderr)
		}
		drafts := grDrafts(t, gl)
		if len(drafts) != len(use)+1 {
			t.Fatalf("%d drafts, want %d\nstderr: %s", len(drafts), len(use)+1, o.stderr)
		}
		grCheckBody(t, "summary draft", fmt.Sprint(drafts[0]["note"]), grWantBody(grSummary(name, use, "")))
		// the corpus really reached the body: each kind of neutralising is visible in it
		for _, mark := range []string{"\u200b/", "@\u200b", "&lt;", "!\u200b["} {
			if !strings.Contains(fmt.Sprint(drafts[0]["note"]), mark) {
				t.Errorf("summary draft lacks %q: the hostile text did not reach it", mark)
			}
		}
		ordered := grOrder(use)
		for i, f := range ordered {
			grCheckBody(t, "positioned draft "+f.title, fmt.Sprint(drafts[i+1]["note"]), grWantBody(grInline(f)))
		}
		grCheckQuiet(t, o)
	})
	t.Run("the cap section carries the same rules", func(t *testing.T) {
		fx := grNewFixture(t)
		gl := grNewGitLab(t, fx)
		many := slices.Concat(fs, grAllSuggestions(40))
		o := grRun(t, fx, gl, grCase{findings: many, name: name})
		if o.code != 0 || !o.hasArtifact {
			t.Fatalf("exit %d, artifact %v; stderr %s", o.code, o.hasArtifact, o.stderr)
		}
		ordered := grOrder(many)
		drafts := grDrafts(t, gl)
		if len(drafts) != 51 {
			t.Fatalf("%d drafts, want 51\nstderr: %s", len(drafts), o.stderr)
		}
		grCheckBody(t, "summary draft", fmt.Sprint(drafts[0]["note"]), grWantBody(grSummary(name, many, grCapSection(ordered[50:]))))
		for i, f := range ordered[:50] {
			grCheckBody(t, "positioned draft "+f.title, fmt.Sprint(drafts[i+1]["note"]), grWantBody(grInline(f)))
		}
	})
	t.Run("pr_comment", func(t *testing.T) {
		fx := grNewFixture(t)
		gl := grNewGitLab(t, fx)
		o := grRun(t, fx, gl, grCase{findings: fs, name: name, postAs: "pr_comment"})
		if o.code != 0 || !o.hasArtifact {
			t.Fatalf("exit %d, artifact %v; stderr %s", o.code, o.hasArtifact, o.stderr)
		}
		reqs := gl.ofKind("POST /notes")
		if len(reqs) != 1 {
			t.Fatalf("%d notes, want 1", len(reqs))
		}
		grCheckBody(t, "note", fmt.Sprint(grJSON(t, reqs[0].Body)["body"]), grWantBody(grSummary(name, fs, "")))
		grCheckQuiet(t, o)
	})
}

// The manifest name is untrusted text of the summary header: a name that is a quick action is
// neutralised there too (its "/" does not start the line, "@" is neutralised).
func TestGRPost_AC27_ManifestNameIsNeutralised(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	o := grRun(t, fx, gl, grCase{findings: grAllSuggestions(1), name: "/close @bob"})
	if o.code != 0 {
		t.Fatalf("exit %d; stderr %s", o.code, o.stderr)
	}
	note := fmt.Sprint(grDrafts(t, gl)[0]["note"])
	if !strings.HasPrefix(note, "## /close @\u200bbob") {
		t.Errorf("summary starts %q, want the name with a neutralised mention", note[:min(len(note), 40)])
	}
	if res := grResidue(note); len(res) > 0 {
		t.Errorf("residue: %v", res)
	}
}

// The residue check itself must see what it is meant to see.
func TestGRResidue_FindsWhatItShould(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want bool
	}{
		"clean":                    {"ok &lt;b&gt; @\u200balice \u200b/close", false},
		"live mention":             {"hi @alice", true},
		"live angle":               {"a <b> c", true},
		"image":                    {"![x](y)", true},
		"entity for at":            {"&#64;", true},
		"quick action":             {"  - /close", true},
		"quick action after bom":   {"\ufeff/merge", true},
		"neutralised quick action": {"> \u200b/close", false},
		"slash inside a sentence":  {"see a/b", false},
	} {
		if got := len(grResidue(tc.in)) > 0; got != tc.want {
			t.Errorf("%s: grResidue(%q) found residue = %v, want %v", name, tc.in, got, tc.want)
		}
	}
}

// ================================================================ safety

// post_as none posts nothing: no GitLab request, no token read, the artifact written.
func TestGRPost_PostAsNoneMakesNoRequest(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	o := grRun(t, fx, gl, grCase{findings: grThree(), postAs: "none", env: map[string]string{"DEVDIGEST_GITLAB_TOKEN": ""}})
	if n := len(gl.requests()); n != 0 {
		t.Errorf("GitLab requests = %d, want 0", n)
	}
	if o.code != 1 || !o.hasArtifact {
		t.Errorf("exit %d, artifact %v, stderr %s; want 1 (gate) with the artifact", o.code, o.hasArtifact, o.stderr)
	}
}

// The job log and the artifact never carry the token, even when GitLab echoes it.
func TestGRPost_TokenNeverInTheJobLog(t *testing.T) {
	fx := grNewFixture(t)
	gl := grNewGitLab(t, fx)
	gl.answer = func(r grReq, _ int) (grAnswer, bool) {
		if r.kind() == "POST /draft_notes" {
			return grAnswer{Status: 422, Body: `{"message":"bad token ` + grToken + `"}`}, true
		}
		return grAnswer{}, false
	}
	o := grRun(t, fx, gl, grCase{findings: grThree()})
	if o.code != 1 {
		t.Errorf("exit code = %d, want 1", o.code)
	}
	if !strings.Contains(o.stderr, "[REDACTED]") {
		t.Errorf("stderr = %q, want the redacted echo", o.stderr)
	}
	grCheckQuiet(t, o)
}
