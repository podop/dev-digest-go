package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/httpx"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/projectcontext/adapters/clonedocs"
	pcpg "dev-digest-go/backend/internal/projectcontext/adapters/postgres"
	"dev-digest-go/backend/internal/projectcontext/app"
	httpapi "dev-digest-go/backend/internal/projectcontext/transport/http"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const storeRoot = ".devdigest/specs/"

type env struct {
	e    *echo.Echo
	db   *gorm.DB
	ws   string
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	var ws, user string
	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&ws).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Raw(`INSERT INTO users (email, name) VALUES ('you@local', 'You') RETURNING id`).Scan(&user).Error; err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	svc, err := app.NewService(pcpg.NewStore(d.DB), clonedocs.Docs{}, pg.NewTx(d.DB),
		[]string{config.DefaultProjectContextGlobs}, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), BodyLimits: httpapi.BodyLimits()})
	httpapi.AddRoutes(e, svc, tenancy.NewResolver(tenancypg.NewStore(d.DB)))
	return &env{e: e, db: d.DB, ws: ws, logs: logs}
}

// repo inserts a repo; a non-empty clone is its clone directory.
func (v *env) repo(t *testing.T, name, clone string) string {
	t.Helper()
	var cp *string
	if clone != "" {
		cp = &clone
	}
	var id string
	err := v.db.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name, clone_path) VALUES (?, 'o', ?, ?, ?) RETURNING id`,
		v.ws, name, "o/"+name, cp).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (v *env) agent(t *testing.T, name string) string {
	t.Helper()
	var id string
	err := v.db.Raw(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, ?, 'openai', 'm', 'p') RETURNING id`,
		v.ws, name).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (v *env) skill(t *testing.T, name string) string {
	t.Helper()
	var id string
	err := v.db.Raw(`INSERT INTO skills (workspace_id, name, description, type, source, body, enabled)
		VALUES (?, ?, 'd', 'custom', 'manual', 'b', false) RETURNING id`, v.ws, name).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (v *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if err := v.db.Exec(sql, args...).Error; err != nil {
		t.Fatal(err)
	}
}

type response struct {
	status int
	body   string
}

func (v *env) do(method, path, body string, headers ...string) response {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	v.e.ServeHTTP(rec, req)
	return response{rec.Code, rec.Body.String()}
}

func decode(t *testing.T, r response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", r.body, err)
	}
	return m
}

// wantError checks the status and the error envelope code and message.
func wantError(t *testing.T, r response, status int, code, message string) map[string]any {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d (%s), want %d", r.status, r.body, status)
	}
	errObj, _ := decode(t, r)["error"].(map[string]any)
	if errObj["code"] != code || (message != "" && errObj["message"] != message) {
		t.Errorf("error = %v, want code %q message %q", errObj, code, message)
	}
	return errObj
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func createFile(t *testing.T, v *env, repo, body string) map[string]any {
	t.Helper()
	r := v.do(http.MethodPost, "/repos/"+repo+"/context/files", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create %s = %d (%s), want 201", body, r.status, r.body)
	}
	return decode(t, r)
}

func paths(docs []any) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i], _ = d.(map[string]any)["path"].(string)
	}
	return out
}

func TestList_CloneAndStore(t *testing.T) { // AC281
	v := newEnv(t)
	clone := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "secret.md", "secret")
	write(t, clone, "docs/b.md", "# b\n")
	write(t, clone, "docs/a.md", "# a\n")
	write(t, clone, "specs/big.md", strings.Repeat("x", 300*1024))
	write(t, clone, "node_modules/docs/x.md", "x")
	write(t, clone, "readme.md", "not under a glob")
	symlink(t, filepath.Join(outside, "secret.md"), clone, "docs/link.md")
	symlink(t, outside, clone, "docs/ext")
	repo := v.repo(t, "r", clone)
	createFile(t, v, repo, `{"path":"`+storeRoot+`s.md","content":"héllo😀"}`)

	r := v.do(http.MethodGet, "/repos/"+repo+"/context", "")
	if r.status != http.StatusOK {
		t.Fatalf("GET context = %d (%s)", r.status, r.body)
	}
	got := decode(t, r)
	docs, _ := got["docs"].([]any)
	wantPaths := []string{storeRoot + "s.md", "docs/a.md", "docs/b.md", "specs/big.md"}
	if diff := cmp.Diff(wantPaths, paths(docs)); diff != "" {
		t.Fatalf("docs paths mismatch (-want +got):\n%s", diff)
	}
	if got["clone_status"] != "ready" || got["truncated"] != nil {
		t.Errorf("clone_status = %v truncated = %v, want ready and no key", got["clone_status"], got["truncated"])
	}
	if diff := cmp.Diff([]any{config.DefaultProjectContextGlobs}, got["globs"]); diff != "" {
		t.Errorf("globs mismatch (-want +got):\n%s", diff)
	}
	store := docs[0].(map[string]any)
	if store["source"] != "store" || store["editable"] != true || store["version"] != float64(1) ||
		store["doc_type"] != "specs" || store["tokens"] != float64(2) || store["size_bytes"] != float64(len("héllo😀")) {
		t.Errorf("store doc = %v", store)
	}
	sum := 0.0
	for i, d := range docs {
		doc := d.(map[string]any)
		sum += doc["tokens"].(float64)
		if i == 0 {
			continue
		}
		if doc["source"] != "repo" || doc["editable"] != false {
			t.Errorf("clone doc %v = %v", doc["path"], doc)
		}
		if _, has := doc["version"]; has {
			t.Errorf("clone doc %v has a version key", doc["path"])
		}
	}
	big := docs[3].(map[string]any)
	if big["tokens"] != float64(300*1024/4) || big["size_bytes"] != float64(300*1024) || big["doc_type"] != "specs" {
		t.Errorf("big doc = %v, want tokens %d", big, 300*1024/4)
	}
	if got["tokens_total"] != sum {
		t.Errorf("tokens_total = %v, want %v", got["tokens_total"], sum)
	}
	if !strings.HasSuffix(docs[1].(map[string]any)["updated_at"].(string), "Z") {
		t.Errorf("updated_at = %v, want an ISO UTC time", docs[1].(map[string]any)["updated_at"])
	}
}

func TestList_NotClonedAndTruncated(t *testing.T) { // AC282
	v := newEnv(t)
	bare := v.repo(t, "bare", "")
	createFile(t, v, bare, `{"path":"`+storeRoot+`only.md"}`)
	got := decode(t, v.do(http.MethodGet, "/repos/"+bare+"/context", ""))
	if got["clone_status"] != "not_cloned" || len(got["docs"].([]any)) != 1 {
		t.Errorf("not cloned list = %v", got)
	}

	clone := t.TempDir()
	for i := 0; i < 501; i++ {
		write(t, clone, fmt.Sprintf("docs/d%03d.md", i), "x")
	}
	big := v.repo(t, "big", clone)
	got = decode(t, v.do(http.MethodGet, "/repos/"+big+"/context", ""))
	if n := len(got["docs"].([]any)); n != 500 || got["truncated"] != true {
		t.Errorf("docs = %d truncated = %v, want 500 and true", n, got["truncated"])
	}

	gone := t.TempDir()
	missing := filepath.Join(gone, "missing")
	nodir := v.repo(t, "nodir", missing)
	got = decode(t, v.do(http.MethodGet, "/repos/"+nodir+"/context", ""))
	if got["clone_status"] != "not_cloned" {
		t.Errorf("clone_status of a missing clone dir = %v, want not_cloned", got["clone_status"])
	}
}

func TestStoreFileHidesCloneDocument(t *testing.T) { // AC283
	v := newEnv(t)
	clone := t.TempDir()
	write(t, clone, storeRoot+"s.md", "from the clone")
	repo := v.repo(t, "r", clone)
	v.exec(t, `INSERT INTO context_files (repo_id, path, content, size_bytes) VALUES (?, ?, 'from the store', 14)`, repo, storeRoot+"s.md")

	docs := decode(t, v.do(http.MethodGet, "/repos/"+repo+"/context", ""))["docs"].([]any)
	if len(docs) != 1 || docs[0].(map[string]any)["source"] != "store" {
		t.Fatalf("docs = %v, want one store entry", docs)
	}
	p := decode(t, v.do(http.MethodGet, "/repos/"+repo+"/context/doc?path="+storeRoot+"s.md", ""))
	if p["source"] != "store" || p["content"] != "from the store" {
		t.Errorf("preview = %v, want the store content", p)
	}
}

func TestPreview(t *testing.T) { // AC284, AC285, AC286
	v := newEnv(t)
	clone := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "passwd.md", "root")
	write(t, clone, "docs/a.md", "# héllo\n")
	write(t, clone, "specs/big.md", strings.Repeat("x", 300*1024))
	symlink(t, filepath.Join(outside, "passwd.md"), clone, "docs/link.md")
	symlink(t, outside, clone, "docs/ext")
	repo := v.repo(t, "r", clone)

	x, y := v.agent(t, "X"), v.agent(t, "Y")
	s := v.skill(t, "S")
	v.exec(t, `INSERT INTO agent_context_docs (agent_id, repo_id, path, position) VALUES (?, ?, 'docs/a.md', 0)`, x, repo)
	v.exec(t, `INSERT INTO skill_context_docs (skill_id, repo_id, path, position) VALUES (?, ?, 'docs/a.md', 0)`, s, repo)
	v.exec(t, `INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, y, s)

	get := func(path string) response {
		return v.do(http.MethodGet, "/repos/"+repo+"/context/doc?path="+path, "")
	}
	r := get("docs/a.md")
	if r.status != http.StatusOK {
		t.Fatalf("preview = %d (%s)", r.status, r.body)
	}
	got := decode(t, r)
	wantAgents := []any{
		map[string]any{"id": x, "name": "X", "via": "direct"},
		map[string]any{"id": y, "name": "Y", "via": "skill", "skill_name": "S"},
	}
	if diff := cmp.Diff(wantAgents, got["used_by_agents"]); diff != "" {
		t.Errorf("used_by_agents mismatch (-want +got):\n%s", diff)
	}
	if got["source"] != "repo" || got["editable"] != false || got["used_by"] != float64(2) ||
		got["content"] != "# héllo\n" || got["tokens"] != float64(2) || got["size_bytes"] != float64(9) {
		t.Errorf("preview = %v", got)
	}

	wantError(t, get("specs/big.md"), http.StatusRequestEntityTooLarge, "doc_too_large", "Document is too large to preview")
	wantError(t, get("docs/missing.md"), http.StatusNotFound, "doc_not_found", "Document not found")
	wantError(t, get("../x.md"), http.StatusBadRequest, "invalid_path", "Not a listable document path")
	wantError(t, get(""), http.StatusBadRequest, "invalid_path", "")
	wantError(t, get("docs/link.md"), http.StatusNotFound, "doc_not_found", "")
	wantError(t, get("docs/ext/passwd.md"), http.StatusNotFound, "doc_not_found", "")
	// AC285 names the link docs/out, but "out" is an excluded directory (FR251): 400, never read.
	wantError(t, get("docs/out/passwd.md"), http.StatusBadRequest, "invalid_path", "")
	wantError(t, get("node_modules/docs/a.md"), http.StatusBadRequest, "invalid_path", "")
	wantError(t, v.do(http.MethodGet, "/repos/"+repo+"/context/doc", ""), http.StatusUnprocessableEntity, "validation_error", "Request validation failed")
	wantError(t, get(strings.Repeat("a", 4097)), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, get(storeRoot+"gone.md"), http.StatusNotFound, "doc_not_found", "")

	bare := v.repo(t, "bare", "")
	wantError(t, v.do(http.MethodGet, "/repos/"+bare+"/context/doc?path=docs/a.md", ""), http.StatusNotFound, "doc_not_found", "")
	wantError(t, v.do(http.MethodGet, "/repos/00000000-0000-4000-8000-000000000000/context/doc?path=docs/a.md", ""),
		http.StatusNotFound, "repo_not_found", "Repository not found")
}

func TestAttachments(t *testing.T) { // AC287, AC288
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	agent := v.agent(t, "A")
	skill := v.skill(t, "S")
	var version int
	v.db.Raw(`SELECT version FROM agents WHERE id = ?`, agent).Scan(&version)

	for _, owner := range []struct{ kind, id string }{{"agents", agent}, {"skills", skill}} {
		url := "/" + owner.kind + "/" + owner.id + "/context"
		put := func(body string) response { return v.do(http.MethodPut, url, body) }
		get := func() response { return v.do(http.MethodGet, url+"?repoId="+repo, "") }

		if got := decode(t, get()); fmt.Sprint(got["paths"]) != "[]" || got["repo_id"] != repo {
			t.Errorf("%s: empty GET = %v, want paths []", owner.kind, got)
		}
		r := put(`{"repo_id":"` + repo + `","paths":["docs/b.md","docs/a.md","docs/never.md"]}`)
		if r.status != http.StatusOK {
			t.Fatalf("%s: PUT = %d (%s)", owner.kind, r.status, r.body)
		}
		want := map[string]any{"repo_id": repo, "paths": []any{"docs/b.md", "docs/a.md", "docs/never.md"}}
		if diff := cmp.Diff(want, decode(t, r)); diff != "" {
			t.Errorf("%s: PUT echo mismatch (-want +got):\n%s", owner.kind, diff)
		}
		if diff := cmp.Diff(want, decode(t, get())); diff != "" {
			t.Errorf("%s: GET after PUT mismatch (-want +got):\n%s", owner.kind, diff)
		}

		many := make([]string, 51)
		for i := range many {
			many[i] = fmt.Sprintf("docs/%d.md", i)
		}
		manyJSON, _ := json.Marshal(many)
		wantError(t, put(`{"repo_id":"`+repo+`","paths":`+string(manyJSON)+`}`), http.StatusUnprocessableEntity, "too_many_paths", "Too many paths")
		wantError(t, put(`{"repo_id":"`+repo+`","paths":["docs/a.md","docs/a.md"]}`), http.StatusUnprocessableEntity, "duplicate_path", "A path is attached more than once")
		wantError(t, put(`{"repo_id":"`+repo+`","paths":["../a.md"]}`), http.StatusUnprocessableEntity, "invalid_path", "A path is not a listable document path")
		// A store-shaped path is accepted; existence is never checked.
		if r := put(`{"repo_id":"` + repo + `","paths":["` + storeRoot + `new.md"]}`); r.status != http.StatusOK {
			t.Errorf("%s: PUT of a store-shaped path = %d (%s), want 200", owner.kind, r.status, r.body)
		}
		if got := decode(t, get()); fmt.Sprint(got["paths"]) != "["+storeRoot+"new.md]" {
			t.Errorf("%s: paths after the failed PUTs = %v", owner.kind, got["paths"])
		}
		if r := put(`{"repo_id":"` + repo + `","paths":[]}`); r.status != http.StatusOK {
			t.Errorf("%s: PUT of no paths = %d, want 200", owner.kind, r.status)
		}
	}
	var after int
	v.db.Raw(`SELECT version FROM agents WHERE id = ?`, agent).Scan(&after)
	if after != version {
		t.Errorf("agent version = %d after attaching, want %d", after, version)
	}

	const none = "00000000-0000-4000-8000-000000000000"
	body := `{"repo_id":"` + repo + `","paths":[]}`
	wantError(t, v.do(http.MethodGet, "/agents/"+none+"/context?repoId="+repo, ""), http.StatusNotFound, "agent_not_found", "Agent not found")
	wantError(t, v.do(http.MethodGet, "/skills/"+none+"/context?repoId="+repo, ""), http.StatusNotFound, "skill_not_found", "Skill not found")
	wantError(t, v.do(http.MethodPut, "/skills/"+none+"/context", body), http.StatusNotFound, "skill_not_found", "")
	wantError(t, v.do(http.MethodGet, "/agents/"+agent+"/context?repoId="+none, ""), http.StatusNotFound, "repo_not_found", "Repository not found")
	wantError(t, v.do(http.MethodPut, "/agents/"+agent+"/context", `{"repo_id":"`+none+`","paths":[]}`), http.StatusNotFound, "repo_not_found", "")
	wantError(t, v.do(http.MethodGet, "/agents/"+none+"/context?repoId="+none, ""), http.StatusNotFound, "agent_not_found", "") // owner first
	wantError(t, v.do(http.MethodGet, "/agents/"+agent+"/context", ""), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodGet, "/agents/"+agent+"/context?repoId=nope", ""), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPut, "/agents/"+agent+"/context", `{"repo_id":"nope","paths":[1]}`), http.StatusUnprocessableEntity, "validation_error", "")
}

func TestCreateFile(t *testing.T) { // AC289, AC290
	v := newEnv(t)
	clone := t.TempDir()
	write(t, clone, storeRoot+"c.md", "in the clone")
	repo := v.repo(t, "r", clone)
	url := "/repos/" + repo + "/context/files"

	first := createFile(t, v, repo, "")
	second := createFile(t, v, repo, "null")
	if first["path"] != storeRoot+"untitled.md" || second["path"] != storeRoot+"untitled-2.md" ||
		first["version"] != float64(1) || first["source"] != "store" || first["editable"] != true ||
		fmt.Sprint(first["used_by_agents"]) != "[]" || first["content"] != "" {
		t.Errorf("created = %v and %v", first, second)
	}

	wantError(t, v.do(http.MethodPost, url, `{"path":"`+storeRoot+`untitled.md"}`), http.StatusConflict, "path_exists", "A file already exists at this path")
	wantError(t, v.do(http.MethodPost, url, `{"path":"`+storeRoot+`c.md"}`), http.StatusConflict, "path_exists", "")
	if got := createFile(t, v, repo, `{"path":"`+storeRoot+`c.md","on_conflict":"suffix"}`); got["path"] != storeRoot+"c-2.md" {
		t.Errorf("suffix path = %v, want c-2.md", got["path"])
	}
	wantError(t, v.do(http.MethodPost, url, `{"path":"docs/x.md"}`), http.StatusUnprocessableEntity, "invalid_path", "Not a valid store file path")
	wantError(t, v.do(http.MethodPost, url, `{"content":"a\u0000b"}`), http.StatusUnprocessableEntity, "invalid_content", "The content holds a NUL character")
	wantError(t, v.do(http.MethodPost, url, `{"content":"`+strings.Repeat("a", 262145)+`"}`), http.StatusRequestEntityTooLarge, "doc_too_large", "The file is larger than 256 KB")
	if got := createFile(t, v, repo, `{"path":"`+storeRoot+`max.md","content":"`+strings.Repeat("a", 262144)+`"}`); got["size_bytes"] != float64(262144) {
		t.Errorf("size_bytes of a 256 KB file = %v", got["size_bytes"])
	}
	wantError(t, v.do(http.MethodPost, url, `{"content":3}`), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPost, url, `{"on_conflict":"other"}`), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPost, url, `[]`), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPost, url, `{"path":"x"}`, "Content-Type", "text/plain"), http.StatusUnprocessableEntity, "validation_error", "")

	// One info line per successful create, with the five fields.
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(v.logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 4 { // untitled, untitled-2, c-2, max
		t.Fatalf("log lines = %d, want 4: %s", len(lines), v.logs.String())
	}
	l := lines[0]
	if l["level"] != "INFO" || l["repo_id"] != repo || l["operation"] != "create" || l["path"] != storeRoot+"untitled.md" ||
		l["size"] != float64(0) || l["version"] != float64(1) {
		t.Errorf("log line = %v", l)
	}

	missing := "00000000-0000-4000-8000-000000000000"
	wantError(t, v.do(http.MethodPost, "/repos/"+missing+"/context/files", ""), http.StatusNotFound, "repo_not_found", "Repository not found")
}

func TestCreateFile_TooManyFiles(t *testing.T) { // AC290
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	v.exec(t, `INSERT INTO context_files (repo_id, path, content, size_bytes)
		SELECT ?, '`+storeRoot+`f' || g || '.md', '', 0 FROM generate_series(1, 500) g`, repo)
	wantError(t, v.do(http.MethodPost, "/repos/"+repo+"/context/files", ""), http.StatusUnprocessableEntity, "too_many_files", "A repository holds at most 500 files")
}

func TestSaveFile_StaleVersion(t *testing.T) { // AC291
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	createFile(t, v, repo, `{"path":"`+storeRoot+`a.md","content":"one"}`)
	url := "/repos/" + repo + "/context/files?path=" + storeRoot + "a.md"

	r := v.do(http.MethodPut, url, `{"content":"two","base_version":1}`)
	got := decode(t, r)
	if r.status != http.StatusOK || got["version"] != float64(2) || got["content"] != "two" || got["size_bytes"] != float64(3) {
		t.Fatalf("save = %d %v", r.status, got)
	}
	r = v.do(http.MethodPut, url, `{"content":"three","base_version":1}`)
	if r.status != http.StatusConflict {
		t.Fatalf("stale save = %d (%s), want 409", r.status, r.body)
	}
	want := map[string]any{"error": map[string]any{
		"code": "stale_version", "message": "The file changed since you opened it (current is v2)",
		"details": map[string]any{"current_version": float64(2)},
	}}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("stale body mismatch (-want +got):\n%s", diff)
	}
	if got := decode(t, v.do(http.MethodPut, url, `{"content":"two","base_version":2}`)); got["version"] != float64(3) {
		t.Errorf("equal content saved version = %v, want 3", got["version"])
	}
	wantError(t, v.do(http.MethodPut, url, `{"content":"x","base_version":99999999999}`), http.StatusConflict, "stale_version", "")
	wantError(t, v.do(http.MethodPut, url, `{"content":"a\u0000","base_version":3}`), http.StatusUnprocessableEntity, "invalid_content", "")
	wantError(t, v.do(http.MethodPut, url, `{"content":"x","base_version":1.5}`), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPut, url, `{"content":"x"}`), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPut, "/repos/"+repo+"/context/files", `{"content":"x","base_version":3}`), http.StatusUnprocessableEntity, "validation_error", "")
	wantError(t, v.do(http.MethodPut, "/repos/"+repo+"/context/files?path="+storeRoot+"nope.md", `{"content":"x","base_version":1}`), http.StatusNotFound, "doc_not_found", "")

	// Two editors with the same base version: exactly one wins.
	var wins, stale int
	for i := 0; i < 2; i++ {
		switch r := v.do(http.MethodPut, url, `{"content":"race","base_version":3}`); r.status {
		case http.StatusOK:
			wins++
		case http.StatusConflict:
			stale++
		}
	}
	if wins != 1 || stale != 1 {
		t.Errorf("same-base saves: %d ok, %d stale, want 1 and 1", wins, stale)
	}
}

func TestReadOnlyCloneDocuments(t *testing.T) { // AC292
	v := newEnv(t)
	clone := t.TempDir()
	write(t, clone, "docs/a.md", "keep")
	repo := v.repo(t, "r", clone)
	bare := v.repo(t, "bare", "")
	for _, c := range []struct {
		repo string
		code string
		st   int
	}{{repo, "read_only", http.StatusForbidden}, {bare, "doc_not_found", http.StatusNotFound}} {
		q := "/repos/" + c.repo + "/context/files"
		wantError(t, v.do(http.MethodPut, q+"?path=docs/a.md", `{"content":"x","base_version":1}`), c.st, c.code, "")
		wantError(t, v.do(http.MethodPost, q+"/rename", `{"path":"docs/a.md","new_path":"`+storeRoot+`a.md","base_version":1}`), c.st, c.code, "")
		wantError(t, v.do(http.MethodDelete, q+"?path=docs/a.md", ""), c.st, c.code, "")
	}
	if b, err := os.ReadFile(filepath.Join(clone, "docs/a.md")); err != nil || string(b) != "keep" {
		t.Errorf("clone file = %q, %v, want it untouched", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(clone, ".devdigest")); len(entries) != 0 {
		t.Errorf("clone gained %v, want nothing written", entries)
	}
	wantError(t, v.do(http.MethodDelete, "/repos/"+repo+"/context/files?path=docs/none.md", ""), http.StatusNotFound, "doc_not_found", "")
}

func TestRenameFile(t *testing.T) { // AC293
	v := newEnv(t)
	clone := t.TempDir()
	write(t, clone, storeRoot+"taken.md", "clone")
	repo := v.repo(t, "r", clone)
	createFile(t, v, repo, `{"path":"`+storeRoot+`a.md","content":"A"}`)
	createFile(t, v, repo, `{"path":"`+storeRoot+`other.md"}`)
	x, y := v.agent(t, "X"), v.agent(t, "Y")
	s := v.skill(t, "S")
	a, b := storeRoot+"a.md", storeRoot+"b.md"
	v.exec(t, `INSERT INTO agent_context_docs (agent_id, repo_id, path, position) VALUES (?, ?, ?, 0), (?, ?, ?, 0), (?, ?, ?, 1)`,
		x, repo, a, y, repo, a, y, repo, b)
	v.exec(t, `INSERT INTO skill_context_docs (skill_id, repo_id, path, position) VALUES (?, ?, ?, 0)`, s, repo, a)

	url := "/repos/" + repo + "/context/files/rename"
	rename := func(from, to string, base int) response {
		return v.do(http.MethodPost, url, fmt.Sprintf(`{"path":%s,"new_path":%s,"base_version":%d}`, jsonStr(from), jsonStr(to), base))
	}
	wantError(t, rename(a, "docs/b.md", 1), http.StatusUnprocessableEntity, "invalid_path", "Not a valid store file path")
	wantError(t, rename(a, storeRoot+"taken.md", 1), http.StatusConflict, "path_exists", "")
	wantError(t, rename(a, storeRoot+"other.md", 1), http.StatusConflict, "path_exists", "")
	if got := decode(t, rename(a, a, 7)); got["version"] != float64(1) {
		t.Errorf("rename to the same path: version = %v, want 1 (no change)", got["version"])
	}
	wantError(t, rename(a, b, 5), http.StatusConflict, "stale_version", "")

	r := rename(a, b, 1)
	got := decode(t, r)
	if r.status != http.StatusOK || got["path"] != b || got["version"] != float64(2) || got["content"] != "A" {
		t.Fatalf("rename = %d %v", r.status, got)
	}
	var rows []struct {
		AgentID  string
		Path     string
		Position int
	}
	v.db.Raw(`SELECT agent_id, path, position FROM agent_context_docs WHERE repo_id = ? ORDER BY agent_id, path`, repo).Scan(&rows)
	if len(rows) != 2 {
		t.Fatalf("agent attachments = %v, want 2 (one per agent)", rows)
	}
	for _, row := range rows {
		want := map[string]int{x: 0, y: 0}[row.AgentID]
		if row.Path != b || row.Position != want {
			t.Errorf("attachment of %s = %v, want %s at position %d", row.AgentID, row, b, want)
		}
	}
	var skillPath string
	v.db.Raw(`SELECT path FROM skill_context_docs WHERE skill_id = ?`, s).Scan(&skillPath)
	if skillPath != b {
		t.Errorf("skill attachment path = %q, want %q", skillPath, b)
	}
	wantError(t, rename(a, storeRoot+"c.md", 1), http.StatusNotFound, "doc_not_found", "")
}

func TestDeleteFile(t *testing.T) { // AC294
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	createFile(t, v, repo, `{"path":"`+storeRoot+`a.md"}`)
	agent := v.agent(t, "A")
	v.exec(t, `INSERT INTO agent_context_docs (agent_id, repo_id, path, position) VALUES (?, ?, ?, 0)`, agent, repo, storeRoot+"a.md")

	url := "/repos/" + repo + "/context/files?path=" + storeRoot + "a.md"
	if r := v.do(http.MethodDelete, url, ""); r.status != http.StatusNoContent || r.body != "" {
		t.Fatalf("DELETE = %d %q, want 204 and no body", r.status, r.body)
	}
	var n int64
	v.db.Table("agent_context_docs").Count(&n)
	if n != 1 {
		t.Errorf("attachments after delete = %d, want 1", n)
	}
	wantError(t, v.do(http.MethodDelete, url, ""), http.StatusNotFound, "doc_not_found", "")
	wantError(t, v.do(http.MethodDelete, "/repos/"+repo+"/context/files", ""), http.StatusUnprocessableEntity, "validation_error", "")
}

func TestFileBodyLimits(t *testing.T) { // AC204
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	body := func(size int) string {
		const head = `{"content":"`
		const tail = `","base_version":1}`
		return head + strings.Repeat("a", size-len(head)-len(tail)) + tail
	}
	for _, c := range []struct {
		name, method, url string
		size, want        int
	}{
		{"PUT at the limit", http.MethodPut, "/repos/" + repo + "/context/files?path=" + storeRoot + "a.md", 2_097_152, http.StatusNotFound},
		{"PUT over the limit", http.MethodPut, "/repos/" + repo + "/context/files?path=" + storeRoot + "a.md", 2_097_153, http.StatusRequestEntityTooLarge},
		{"rename keeps 1 MiB", http.MethodPost, "/repos/" + repo + "/context/files/rename", 1_048_577, http.StatusRequestEntityTooLarge},
	} {
		if r := v.do(c.method, c.url, body(c.size)); r.status != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, r.status, c.want)
		}
	}
	want := map[string]int64{"POST /repos/:id/context/files": 2_097_152, "PUT /repos/:id/context/files": 2_097_152}
	if diff := cmp.Diff(want, httpapi.BodyLimits()); diff != "" {
		t.Errorf("BodyLimits() mismatch (-want +got):\n%s", diff)
	}
}

func TestCreateFile_Concurrent(t *testing.T) { // the repo row lock serialises the suffix pick
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	const n = 8
	got := make(chan response, n)
	for i := 0; i < n; i++ {
		go func() { got <- v.do(http.MethodPost, "/repos/"+repo+"/context/files", "") }()
	}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		r := <-got
		if r.status != http.StatusCreated {
			t.Errorf("concurrent create = %d (%s), want 201", r.status, r.body)
			continue
		}
		p, _ := decode(t, r)["path"].(string)
		if seen[p] {
			t.Errorf("path %q handed out twice", p)
		}
		seen[p] = true
	}
	if len(seen) != n {
		t.Errorf("distinct paths = %d, want %d", len(seen), n)
	}
}

func TestPutAttachments_Concurrent(t *testing.T) { // the owner row lock serialises replaces
	v := newEnv(t)
	repo := v.repo(t, "r", "")
	agent := v.agent(t, "A")
	const n = 8
	got := make(chan response, n)
	for i := 0; i < n; i++ {
		go func() {
			got <- v.do(http.MethodPut, "/agents/"+agent+"/context", `{"repo_id":"`+repo+`","paths":["docs/a.md","docs/b.md"]}`)
		}()
	}
	for i := 0; i < n; i++ {
		if r := <-got; r.status != http.StatusOK {
			t.Errorf("concurrent PUT = %d (%s), want 200", r.status, r.body)
		}
	}
	var count int64
	v.db.Table("agent_context_docs").Count(&count)
	if count != 2 {
		t.Errorf("attachment rows = %d, want 2", count)
	}
}
