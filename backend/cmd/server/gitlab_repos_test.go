package main

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/migrations"

	"dev-digest-go/backend/internal/platform/postgres/migrate"
)

// Component tests of adding GitLab repositories (plan S34: AC9-AC12, AC39, AC40) through the real
// server and the GitLab stub of gitlab_clone_test.go. Host keys are "127.0.0.1:<port>" (see there).

const grInvalidURL = "Must be a GitHub or GitLab repository URL, e.g. https://github.com/owner/repo or https://gitlab.com/group/project"

// grProjectGET is the path of the project read by path.
func grProjectGET(g *grGitLab, fullPath string) string {
	return g.prefix + "/api/v4/projects/" + strings.ReplaceAll(fullPath, "/", "%2F")
}

// grIssue returns the first validation issue (instance path, message) of a 422.
func grIssue(t testing.TB, resp response) (path, message string) {
	t.Helper()
	e, _ := resp.Obj(t)["error"].(map[string]any)
	details, _ := e["details"].([]any)
	if len(details) == 0 {
		t.Fatalf("answer %s has no details", resp.Body)
	}
	d, _ := details[0].(map[string]any)
	path, _ = d["instancePath"].(string)
	message, _ = d["message"].(string)
	return path, message
}

// grErrDetails returns error.details of an envelope as an object.
func grErrDetails(t testing.TB, resp response) map[string]any {
	t.Helper()
	e, _ := resp.Obj(t)["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	return d
}

// grList returns GET /repos as objects.
func grList(t testing.TB, s *cpStack) []map[string]any {
	t.Helper()
	resp := s.get("/repos")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /repos = %d %s, want 200", resp.Status, resp.Body)
	}
	var out []map[string]any
	for _, v := range resp.JSON(t).([]any) {
		out = append(out, v.(map[string]any))
	}
	return out
}

// grRow is a repository row.
type grRow struct {
	FullName      string
	Owner         string
	Name          string
	WebURL        string
	DefaultBranch string
	ClonePath     *string
	ProjectID     int64
}

func grRowOf(t testing.TB, s *cpStack, id string) grRow {
	t.Helper()
	var row grRow
	if err := s.db.DB.Raw(`SELECT full_name, owner, name, coalesce(web_url, '') AS web_url, default_branch, clone_path, coalesce(project_id, 0) AS project_id
		FROM repos WHERE id = ?`, id).Scan(&row).Error; err != nil || row.FullName == "" {
		t.Fatalf("read repo %s: %v (%+v)", id, err, row)
	}
	return row
}

// AC9 (FR13-FR16, EC13): adding a project by its HTTPS URL reads the project once, stores it under
// the canonical path with the stub's default branch and enqueues a clone job; the SSH form of the
// same project (other case) answers 200 with the same repository and no job.
func TestGitLabRepos_AC9_AddByHTTPSAndSSH(t *testing.T) { // AC9
	g := grNewGitLab(t, "")
	g.addProject(42, "acme/platform/payments", "develop")
	s := grBoot(t, g, grOpts{})
	connID := grConnect(t, s, g.base, "glpat-grAC9tokenTokenToken01")
	s.leakCheck("glpat-grAC9tokenTokenToken01")

	resp := grAdd(s, g.base+"/acme/platform/payments")
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	got := resp.Obj(t)
	want := map[string]any{
		"provider": "gitlab", "host": g.host, "owner": "acme/platform", "name": "payments",
		"full_name": "acme/platform/payments", "default_branch": "develop",
		"web_url": g.base + "/acme/platform/payments", "connection_id": connID, "clone_path": nil,
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("created repo %s = %v, want %v", key, got[key], w)
		}
	}
	id, _ := got["id"].(string)
	if reqs := g.projectGETs(); len(reqs) != 1 || reqs[0].Path != grProjectGET(g, "acme/platform/payments") {
		t.Errorf("project reads of the add = %+v, want one GET %s", reqs, grProjectGET(g, "acme/platform/payments"))
	}
	if jobsNow := jobRows(t, s.db.DB, "clone"); len(jobsNow) != 1 {
		t.Errorf("clone jobs after the add = %d, want 1", len(jobsNow))
	}
	s.waitJobs("clone", 1)

	// The SSH form resolves to project 42: the stored repository, no job.
	resp = grAdd(s, "git@127.0.0.1:Acme/Platform/Payments.git")
	if resp.Status != http.StatusOK || resp.Obj(t)["id"] != id || resp.Obj(t)["full_name"] != "acme/platform/payments" {
		t.Errorf("POST /repos by the SSH form = %d %s, want 200 with repo %s", resp.Status, resp.Body, id)
	}
	if n := len(jobRows(t, s.db.DB, "clone")); n != 1 {
		t.Errorf("clone jobs after the SSH add = %d, want 1 (no new job)", n)
	}
	if n := s.rows(t, "repos"); n != 1 {
		t.Errorf("repos after the second add = %d, want 1", n)
	}
}

// AC10 (FR12, FR13, EC11, EC12, NFR7): the refused inputs answer their 422 and create nothing; the
// GitHub cases keep the FR12 message.
func TestGitLabRepos_AC10_RefusedInputs(t *testing.T) { // AC10
	g := grNewGitLab(t, "/gitlab")
	g.addProject(42, "acme/platform/payments", "main")
	s := grBoot(t, g, grOpts{})
	grConnect(t, s, g.base, "")
	otherPort := grFreePort(t)
	grConnect(t, s, "https://127.0.0.1:"+strconv.Itoa(otherPort), "")

	segments := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = "g" + strconv.Itoa(i)
		}
		return strings.Join(parts, "/")
	}
	tests := []struct {
		name, url, code, message string
		host                     string // details.host for repo_host_not_connected
	}{
		{name: "outside the connection's path prefix (EC11)", url: "https://" + g.host + "/other/group/proj",
			code: "validation_error", message: "This URL is outside " + g.base},
		{name: "an SSH host shared by two connections (EC11)", url: "git@127.0.0.1:group/proj.git",
			code: "validation_error", message: "Several GitLab connections use 127.0.0.1; paste the HTTPS URL"},
		{name: "no connection for the host (EC12)", url: "https://gitlab.com/acme/web",
			code: "repo_host_not_connected", host: "gitlab.com"},
		{name: "a merge request deep link (EC12)", url: "https://gitlab.com/acme/web/-/merge_requests/1",
			code: "validation_error", message: grInvalidURL},
		{name: "22 segments (EC12)", url: "https://gitlab.com/" + segments(22), code: "validation_error", message: grInvalidURL},
		{name: "22 segments below the prefix (EC12)", url: g.base + "/" + segments(22), code: "validation_error", message: grInvalidURL},
		{name: "GitHub: plain http", url: "http://github.com/o/r", code: "validation_error", message: grInvalidURL},
		{name: "GitHub: owner only", url: "https://github.com/o", code: "validation_error", message: grInvalidURL},
		{name: "GitHub: a pull request link", url: "https://github.com/o/r/pull/1", code: "validation_error", message: grInvalidURL},
		{name: "GitHub: SSH without a repository", url: "git@github.com:o", code: "validation_error", message: grInvalidURL},
		{name: "GitHub: www host", url: "https://www.github.com/o/r", code: "validation_error", message: grInvalidURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := grAdd(s, tc.url)
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != tc.code {
				t.Fatalf("POST /repos %s = %d %s, want 422 %s", tc.url, resp.Status, resp.Body, tc.code)
			}
			switch tc.code {
			case "repo_host_not_connected":
				if h := grErrDetails(t, resp)["host"]; h != tc.host {
					t.Errorf("details.host = %v, want %s", h, tc.host)
				}
				if _, msg := cpErr(t, resp); msg != "No GitLab connection for "+tc.host+". Add one in Settings → GitLab Integration." {
					t.Errorf("message = %q, want the FR13 text for %s", msg, tc.host)
				}
			default:
				if path, msg := grIssue(t, resp); path != "/url" || msg != tc.message {
					t.Errorf("issue = %q %q, want /url %q", path, msg, tc.message)
				}
			}
		})
	}
	if n := s.rows(t, "repos"); n != 0 {
		t.Errorf("repos after the refused adds = %d, want 0", n)
	}
	if n := s.rows(t, "jobs"); n != 0 {
		t.Errorf("jobs after the refused adds = %d, want 0", n)
	}
	if reqs := g.requests(); len(reqs) != 0 {
		t.Errorf("the stub received %d request(s) for refused inputs, want 0: %+v", len(reqs), reqs)
	}
}

// grFreePort is a port nothing listens on.
func grFreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// AC11 (FR14, EC15): the project read answers 404, a redirect, 401, 429 and 500; each maps to its
// code, nothing is stored and the redirect target is not contacted. A connection without a token
// adds a public project anonymously.
func TestGitLabRepos_AC11_ProjectErrors(t *testing.T) { // AC11
	const token = "glpat-grAC11tokenTokenToken1"
	g := grNewGitLab(t, "")
	g.addProject(42, "acme/platform/payments", "main")
	s := grBoot(t, g, grOpts{})
	s.leakCheck(token)
	connID := grConnect(t, s, g.base, token)
	const project = "acme/platform/payments"
	projectPath := grProjectGET(g, project)
	url := g.base + "/" + project

	answer := func(status int, header ...string) {
		g.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.EscapedPath() != projectPath {
				return false
			}
			for i := 0; i+1 < len(header); i += 2 {
				w.Header().Set(header[i], header[i+1])
			}
			grJSON(w, status, `{"message":"x"}`)
			return true
		})
	}
	tests := []struct {
		name     string
		status   int
		header   []string
		wantHTTP int
		wantCode string
		wantMsg  string
	}{
		{"404", 404, nil, 422, "gitlab_project_not_found", "Project " + project + " was not found on " + g.host + ", or the token cannot see it"},
		{"301 to the project id", 301, []string{"Location", g.base + "/api/v4/projects/42"}, 422, "gitlab_project_not_found",
			"GitLab reports that " + project + " has moved; paste the project's current URL"},
		{"401", 401, nil, 422, "gitlab_permission_denied", "GitLab rejected the token of connection " + g.host},
		{"429", 429, nil, 502, "gitlab_rate_limited", "GitLab rate limit reached, try again later"},
		{"500", 500, nil, 502, "gitlab_unavailable", "GitLab is unavailable, try again later"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answer(tc.status, tc.header...)
			resp := grAdd(s, url)
			if resp.Status != tc.wantHTTP || resp.errorCode(t) != tc.wantCode {
				t.Fatalf("POST /repos with the project answering %d = %d %s, want %d %s", tc.status, resp.Status, resp.Body, tc.wantHTTP, tc.wantCode)
			}
			if _, msg := cpErr(t, resp); msg != tc.wantMsg {
				t.Errorf("message = %q, want %q", msg, tc.wantMsg)
			}
			if n := s.rows(t, "repos"); n != 0 {
				t.Errorf("repos after the failed add = %d, want 0", n)
			}
			if n := s.rows(t, "jobs"); n != 0 {
				t.Errorf("jobs after the failed add = %d, want 0", n)
			}
		})
	}
	for _, r := range g.requests() {
		if r.Path == g.prefix+"/api/v4/projects/42" {
			t.Errorf("the redirect target %s was contacted", r.Path)
		}
	}

	// A connection without a token: the public project is added anonymously.
	g.setOverride(nil)
	g.forget()
	if resp := s.del("/settings/gitlab-connections/" + connID); resp.Status != http.StatusOK {
		t.Fatalf("DELETE the connection = %d %s, want 200", resp.Status, resp.Body)
	}
	grConnect(t, s, g.base, "")
	resp := grAdd(s, url)
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos through a connection without a token = %d %s, want 201", resp.Status, resp.Body)
	}
	reqs := g.projectGETs()
	if len(reqs) != 1 || reqs[0].PrivateToken != "" || reqs[0].Authorization != "" {
		t.Errorf("anonymous project read = %+v, want one request without a token header", reqs)
	}
}

// AC12 (FR30, EC14, EC29): a GitHub and a GitLab repository named acme/web live in one workspace
// with their own PRs; the down migration refuses while GitLab data exists and changes nothing,
// then, with GitHub data only, rolls back and up again.
func TestGitLabRepos_AC12_SameNameOnBothHosts(t *testing.T) { // AC12
	g := grNewGitLab(t, "")
	g.addProject(77, "acme/web", "main")
	remote := t.TempDir()
	makeRemote(t, remote, "acme", "web")
	s := grBoot(t, g, grOpts{remote: remote})
	grConnect(t, s, g.base, "")

	gh := s.post("/repos", map[string]string{"url": "https://github.com/acme/web"})
	gl := grAdd(s, g.base+"/acme/web")
	if gh.Status != http.StatusCreated || gl.Status != http.StatusCreated {
		t.Fatalf("POST /repos GitHub = %d %s, GitLab = %d %s, want 201 and 201", gh.Status, gh.Body, gl.Status, gl.Body)
	}
	ghID, glID := gh.Obj(t)["id"].(string), gl.Obj(t)["id"].(string)
	if ghID == glID {
		t.Fatalf("both adds answered repo %s", ghID)
	}
	s.env.pull(t, ghID, 7, nil)
	s.env.pull(t, glID, 7, nil)
	for id, want := range map[string][2]string{ghID: {"github", "github.com"}, glID: {"gitlab", g.host}} {
		var row struct{ Provider, Host string }
		if err := s.db.DB.Raw(`SELECT provider, host FROM repos WHERE id = ?`, id).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Provider != want[0] || row.Host != want[1] {
			t.Errorf("repo %s has provider %q host %q, want %q %q", id, row.Provider, row.Host, want[0], want[1])
		}
		if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, id); n != 1 {
			t.Errorf("pull requests of repo %s = %d, want 1", id, n)
		}
	}
	s.waitJobs("clone", 2)
	if err := s.stop(); err != nil {
		t.Fatalf("stop the server: %v", err)
	}

	sqlDB, err := s.db.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New(sqlDB, migrations.FS, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	before := dumpTable(t, s.db.DB, "repos") + dumpTable(t, s.db.DB, "gitlab_connections")
	_, _, err = m.Down(t.Context())
	if err == nil || !strings.Contains(err.Error(), "GitLab data exists") {
		t.Fatalf("Down() with a GitLab repository = %v, want an error naming the GitLab data", err)
	}
	if after := dumpTable(t, s.db.DB, "repos") + dumpTable(t, s.db.DB, "gitlab_connections"); after != before {
		t.Error("the refused Down() changed the repos or connections")
	}
	if v := scalar[int](t, s.db.DB, `SELECT max(version) FROM schema_migrations`); v != 10 {
		t.Errorf("schema version after the refused Down() = %d, want 10", v)
	}

	if err := s.db.DB.Exec(`DELETE FROM repos WHERE provider = 'gitlab'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.DB.Exec(`DELETE FROM gitlab_connections`).Error; err != nil {
		t.Fatal(err)
	}
	if v, ok, err := m.Down(t.Context()); err != nil || !ok || v != 10 {
		t.Fatalf("Down() with GitHub data only = %d, %v, %v, want 10, true, nil", v, ok, err)
	}
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after the Down() = %d, %v, want 1, nil", n, err)
	}
	if row := scalar[string](t, s.db.DB, `SELECT provider || ' ' || host FROM repos WHERE id = ?`, ghID); row != "github github.com" {
		t.Errorf("the GitHub repository after down and up = %q, want github github.com", row)
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, ghID); n != 1 {
		t.Errorf("pull requests of the GitHub repository after down and up = %d, want 1", n)
	}
}

// AC39 (FR48, EC33, D26): repositories whose connection was removed come back under a new
// connection for the same host key with no database change, no clone job and the new token on the
// next sync; a connection for the same host name on another port leaves them not connected.
func TestGitLabRepos_AC39_NewConnectionRebindsRepositories(t *testing.T) { // AC39
	const oldToken, newToken = "glpat-grAC39oldTokenToken001", "glpat-grAC39newTokenToken002"
	g := grNewGitLab(t, "")
	g.addProject(42, "acme/platform/payments", "main")
	g.setMRs(42, grMR(42, 1, "Add feature", "0123456789abcdef0123456789abcdef01234567"))
	s := grBoot(t, g, grOpts{})
	s.leakCheck(oldToken, newToken)
	first := grConnect(t, s, g.base, oldToken)

	resp := grAdd(s, g.base+"/acme/platform/payments")
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	id, _ := resp.Obj(t)["id"].(string)
	if job := s.waitJobs("clone", 1)[0]; job.Status != "done" {
		t.Fatalf("clone job = %+v, want done", job)
	}
	clonePath := grRepoPath(t, s, id)
	if clonePath == "" {
		t.Fatal("the repository has no clone_path after the clone job")
	}

	connectionID := func() any {
		t.Helper()
		for _, r := range grList(t, s) {
			if r["id"] == id {
				return r["connection_id"]
			}
		}
		t.Fatalf("repo %s is not listed", id)
		return nil
	}
	if connectionID() != first {
		t.Fatalf("connection_id before the removal = %v, want %s", connectionID(), first)
	}
	if resp := s.del("/settings/gitlab-connections/" + first); resp.Status != http.StatusOK || resp.Obj(t)["disconnected_repos"] != float64(1) {
		t.Fatalf("DELETE the connection = %d %s, want 200 with 1 disconnected repository", resp.Status, resp.Body)
	}
	if got := connectionID(); got != nil {
		t.Fatalf("connection_id after the removal = %v, want null", got)
	}

	before := dumpTable(t, s.db.DB, "repos")
	jobsBefore := len(jobRows(t, s.db.DB, ""))
	second := grConnect(t, s, g.base, newToken)
	if got := connectionID(); got != second {
		t.Errorf("connection_id under the new connection = %v, want %s", got, second)
	}
	if after := dumpTable(t, s.db.DB, "repos"); after != before {
		t.Errorf("the repos table changed by creating the connection:\n%s\n%s", before, after)
	}
	if n := len(jobRows(t, s.db.DB, "")); n != jobsBefore {
		t.Errorf("jobs after creating the connection = %d, want %d (no clone job)", n, jobsBefore)
	}

	g.forget()
	if resp := s.get("/repos/" + id + "/pulls"); resp.Status != http.StatusOK {
		t.Fatalf("GET /repos/:id/pulls = %d %s, want 200", resp.Status, resp.Body)
	}
	rest := g.restRequests()
	if len(rest) < 2 {
		t.Fatalf("the sync made %d REST request(s), want the project read and the merge request list: %+v", len(rest), rest)
	}
	for _, r := range rest {
		if r.PrivateToken != newToken {
			t.Errorf("sync request %s %s carried token %q, want the new token", r.Method, r.Path, r.PrivateToken)
		}
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, id); n != 1 {
		t.Errorf("pull requests after the sync = %d, want 1", n)
	}
	if got := grRepoPath(t, s, id); got != clonePath {
		t.Errorf("clone_path after the sync = %q, want %q", got, clonePath)
	}

	// Another port is another host key: the repository stays not connected.
	if resp := s.del("/settings/gitlab-connections/" + second); resp.Status != http.StatusOK {
		t.Fatalf("DELETE the new connection = %d %s, want 200", resp.Status, resp.Body)
	}
	grConnect(t, s, "https://127.0.0.1:"+strconv.Itoa(grFreePort(t)), newToken)
	if got := connectionID(); got != nil {
		t.Errorf("connection_id under a connection on another port = %v, want null", got)
	}
}

// grSetInterval writes the workspace polling interval (Settings) through the API.
func grSetInterval(t *testing.T, s *cpStack, minutes int) {
	t.Helper()
	resp := s.do(http.MethodPut, "/settings", map[string]any{"polling_interval_min": minutes})
	if resp.Status != http.StatusOK {
		t.Fatalf("PUT /settings polling_interval_min=%d = %d %s, want 200", minutes, resp.Status, resp.Body)
	}
}

// AC40 (FR47, FR15, EC20): a project renamed on GitLab is followed by id through each of the four
// paths: the add by the new URL, the list sync, the manual poll and the background cycle. The
// clone directory and the default branch stay, the MR is upserted by id, a web_url in the stub's
// answer is not used, and the next refresh fetches from the new remote URL.
func TestGitLabRepos_AC40_RenamedProjectFollowsTheId(t *testing.T) { // AC40
	const token = "glpat-grAC40tokenTokenToken1"
	const oldPath, newPath = "acme/platform/payments", "acme/core/payments"
	g := grNewGitLab(t, "")
	g.addProject(42, oldPath, "develop")
	clock := newPollClock()
	s := grBoot(t, g, grOpts{now: clock.Now, pollTick: pollTick, cfg: func(c *config.Config) { c.PRPollEnabled = true }})
	s.leakCheck(token)
	grConnect(t, s, g.base, token)
	grSetInterval(t, s, 1)

	resp := grAdd(s, g.base+"/"+oldPath)
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	id, _ := resp.Obj(t)["id"].(string)
	if job := s.waitJobs("clone", 1)[0]; job.Status != "done" {
		t.Fatalf("clone job = %+v, want done", job)
	}
	clonePath := grRepoPath(t, s, id)
	wantDir := s.cfg.CloneDir + "/@gitlab/" + g.hostDir() + "/42"
	if clonePath != wantDir {
		t.Fatalf("clone_path = %q, want %q", clonePath, wantDir)
	}

	// resetPath puts the stored repository back on the old path, as if GitLab had just renamed it.
	resetPath := func() {
		t.Helper()
		g.rename(42, oldPath)
		err := s.db.DB.Exec(`UPDATE repos SET full_name = ?, owner = 'acme/platform', name = 'payments', web_url = ? WHERE id = ?`,
			oldPath, g.base+"/"+oldPath, id).Error
		if err != nil {
			t.Fatal(err)
		}
		g.rename(42, newPath)
		g.setWebURL(42, "https://elsewhere.invalid/"+newPath) // never to be used
	}
	assertFollowed := func(title string) {
		t.Helper()
		row := grRowOf(t, s, id)
		want := grRow{
			FullName: newPath, Owner: "acme/core", Name: "payments", WebURL: g.base + "/" + newPath,
			DefaultBranch: "develop", ClonePath: &clonePath, ProjectID: 42,
		}
		if diff := cmp.Diff(want, row); diff != "" {
			t.Errorf("repository after following the rename mismatch (-want +got):\n%s", diff)
		}
		if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, id); n != 1 {
			t.Errorf("pull requests after the sync = %d, want 1 (upserted by id)", n)
		}
		if got := scalar[string](t, s.db.DB, `SELECT title FROM pull_requests WHERE repo_id = ? AND number = 1`, id); got != title {
			t.Errorf("title of !1 = %q, want %q", got, title)
		}
	}
	refreshes := 0
	assertRefreshFetchesNewURL := func() {
		t.Helper()
		g.forget()
		if resp := s.post("/repos/"+id+"/refresh", nil); resp.Status != http.StatusOK {
			t.Fatalf("POST /repos/:id/refresh = %d %s, want 200", resp.Status, resp.Body)
		}
		refreshes++
		if job := s.waitJobs("refresh", refreshes)[refreshes-1]; job.Status != "done" {
			t.Fatalf("refresh job = %+v, want done", job)
		}
		if got, want := grGit(t, clonePath, "remote", "get-url", "origin"), g.base+"/"+newPath+".git"; got != want {
			t.Errorf("origin of the clone after the refresh = %q, want %q", got, want)
		}
		for _, r := range g.gitRequests() {
			if !strings.HasPrefix(r.Path, "/"+newPath+".git/") {
				t.Errorf("the refresh fetched %s, want the new path %s", r.Path, newPath)
			}
		}
		if len(g.gitRequests()) == 0 {
			t.Error("the refresh made no git request")
		}
	}

	title := 0
	nextTitle := func() string {
		title++
		name := "Change " + strconv.Itoa(title)
		g.setMRs(42, grMR(42, 1, name, "0123456789abcdef0123456789abcdef0123456"+strconv.Itoa(title)))
		return name
	}

	t.Run("add by the new URL", func(t *testing.T) {
		resetPath()
		resp := grAdd(s, g.base+"/"+newPath)
		if resp.Status != http.StatusOK || resp.Obj(t)["id"] != id || resp.Obj(t)["full_name"] != newPath {
			t.Fatalf("POST /repos by the new URL = %d %s, want 200 with repo %s on %s", resp.Status, resp.Body, id, newPath)
		}
		row := grRowOf(t, s, id)
		if row.FullName != newPath || row.Owner != "acme/core" || row.WebURL != g.base+"/"+newPath || row.DefaultBranch != "develop" {
			t.Errorf("repository after the add by the new URL = %+v, want the new path, develop", row)
		}
		if n := s.rows(t, "repos"); n != 1 {
			t.Errorf("repos = %d, want 1", n)
		}
		assertRefreshFetchesNewURL()
	})
	t.Run("list sync", func(t *testing.T) {
		resetPath()
		want := nextTitle()
		if resp := s.get("/repos/" + id + "/pulls"); resp.Status != http.StatusOK {
			t.Fatalf("GET /repos/:id/pulls = %d %s, want 200", resp.Status, resp.Body)
		}
		assertFollowed(want)
		assertRefreshFetchesNewURL()
	})
	t.Run("manual poll", func(t *testing.T) {
		resetPath()
		want := nextTitle()
		if resp := s.post("/repos/"+id+"/poll", nil); resp.Status != http.StatusOK {
			t.Fatalf("POST /repos/:id/poll = %d %s, want 200", resp.Status, resp.Body)
		}
		assertFollowed(want)
		assertRefreshFetchesNewURL()
	})
	t.Run("background cycle", func(t *testing.T) {
		resetPath()
		want := nextTitle()
		// A cycle that fails (a stub hiccup under load) is retried one interval later, so the clock
		// moves on by a minute until the cycle has stored the new path.
		stored := func() bool { return grRowOf(t, s, id).FullName == newPath }
		for attempt := 0; attempt < 5 && !stored(); attempt++ {
			clock.Advance(time.Minute)
			deadline := time.Now().Add(8 * time.Second)
			for !stored() && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
		}
		if !stored() {
			t.Fatalf("the background cycle did not store the new path; log:\n%s", s.logs.String())
		}
		eventually(t, 30*time.Second, "the background cycle to upsert the merge request", func() bool {
			return scalar[string](t, s.db.DB, `SELECT coalesce(max(title), '') FROM pull_requests WHERE repo_id = ?`, id) == want
		})
		assertFollowed(want)
		assertRefreshFetchesNewURL()
	})
}
