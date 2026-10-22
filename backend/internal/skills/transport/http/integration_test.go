package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/httpx"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/skills/adapters/catalog"
	skillspg "dev-digest-go/backend/internal/skills/adapters/postgres"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
	httpapi "dev-digest-go/backend/internal/skills/transport/http"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// dbAgents stands in for the agents context: it appends the skill to the agent's links and bumps
// the agent's version through the transaction in ctx, like the real AppendSkill does.
type dbAgents struct {
	db   *gorm.DB
	fail error
}

func (a *dbAgents) AppendSkill(ctx context.Context, _, agentID, skillID string) error {
	if a.fail != nil {
		return a.fail
	}
	conn := pg.Conn(ctx, a.db)
	if err := conn.Exec(`INSERT INTO agent_skills (agent_id, skill_id, "order")
		SELECT ?, ?, count(*) FROM agent_skills WHERE agent_id = ?`, agentID, skillID, agentID).Error; err != nil {
		return err
	}
	return conn.Exec(`UPDATE agents SET version = version + 1 WHERE id = ?`, agentID).Error
}

// dbConventions stands in for the conventions context.
type dbConventions struct {
	db   *gorm.DB
	fail error
}

func (c *dbConventions) NotAccepted(ctx context.Context, _, repoID string, ids []string) ([]string, error) {
	var ok []string
	if err := pg.Conn(ctx, c.db).Raw(`SELECT id FROM conventions WHERE repo_id = ? AND status = 'accepted' AND id IN ?`, repoID, ids).
		Scan(&ok).Error; err != nil {
		return nil, err
	}
	var bad []string
	for _, id := range ids {
		found := false
		for _, o := range ok {
			found = found || o == id
		}
		if !found {
			bad = append(bad, id)
		}
	}
	return bad, nil
}

func (c *dbConventions) AttachSkill(ctx context.Context, _, repoID string, ids []string, skillID string) error {
	if c.fail != nil {
		return c.fail
	}
	return pg.Conn(ctx, c.db).Exec(`UPDATE conventions SET skill_id = ?, updated_at = now() WHERE repo_id = ? AND id IN ?`, skillID, repoID, ids).Error
}

// stubFetcher answers URL imports with a canned document.
type stubFetcher struct {
	doc  domain.FetchedDocument
	urls []string
}

func (s *stubFetcher) Fetch(_ context.Context, u *url.URL) (domain.FetchedDocument, error) {
	s.urls = append(s.urls, u.String())
	return s.doc, nil
}

type stack struct {
	e       *echo.Echo
	db      *gorm.DB
	ws      string
	other   string
	repo    string
	pr      string
	fetcher *stubFetcher
	agents  *dbAgents
	conv    *dbConventions
}

func newStack(t *testing.T) *stack {
	t.Helper()
	d := pgtest.Migrated(t)
	s := &stack{db: d.DB, fetcher: &stubFetcher{}, agents: &dbAgents{db: d.DB}, conv: &dbConventions{db: d.DB}}
	s.ws = s.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	s.other = s.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	s.exec(t, `INSERT INTO users (email, name) VALUES ('you@local', 'You')`)
	s.repo = s.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id`, s.ws)
	s.pr = s.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 1, 't', 'a', 'b', 'main', 'sha') RETURNING id`, s.ws, s.repo)

	cat, err := catalog.New()
	if err != nil {
		t.Fatal(err)
	}
	tx := pg.NewTx(d.DB)
	store := skillspg.NewStore(d.DB)
	svc := app.NewService(store, tx, time.Now)
	merger := app.NewMerger(app.MergerDeps{Tx: tx, Skills: svc, Lookup: store, Agents: s.agents, Conventions: s.conv})
	s.e = httpx.New(httpx.Options{BodyLimits: httpapi.BodyLimits()})
	httpapi.AddRoutes(s.e, svc, app.NewImporter(s.fetcher, cat), merger, tenancy.NewResolver(tenancypg.NewStore(d.DB)))
	return s
}

func (s *stack) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := s.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (s *stack) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := s.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (s *stack) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func (s *stack) agent(t *testing.T, name string, enabled bool) string {
	t.Helper()
	return s.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled)
		VALUES (?, ?, 'openai', 'm', 'p', ?) RETURNING id`, s.ws, name, enabled)
}

func (s *stack) link(t *testing.T, agent, skill string, order int) {
	t.Helper()
	s.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, ?)`, agent, skill, order)
}

func (s *stack) do(t *testing.T, method, path, body string) response {
	t.Helper()
	return do(t, s.e, method, path, body)
}

// post creates a skill through the API and returns it decoded.
func (s *stack) post(t *testing.T, body string) map[string]any {
	t.Helper()
	r := s.do(t, http.MethodPost, "/skills", body)
	if r.status != http.StatusCreated {
		t.Fatalf("POST /skills %s = %d %s, want 201", body, r.status, r.body)
	}
	return decodeMap(t, r)
}

func decodeMap(t *testing.T, r response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", r.body, err)
	}
	return m
}

func decodeList(t *testing.T, r response) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal([]byte(r.body), &l); err != nil {
		t.Fatalf("body %q is not a JSON array: %v", r.body, err)
	}
	return l
}

func field[T any](t *testing.T, m map[string]any, key string) T {
	t.Helper()
	v, ok := m[key].(T)
	if !ok {
		t.Fatalf("field %q = %#v, want %T", key, m[key], v)
	}
	return v
}

func names(list []map[string]any) []string {
	var out []string
	for _, m := range list {
		out = append(out, m["name"].(string))
	}
	return out
}

func wantError(t *testing.T, r response, status int, code, message string) {
	t.Helper()
	if r.status != status {
		t.Errorf("status = %d %s, want %d", r.status, r.body, status)
		return
	}
	c, m, _, _ := errorOf(t, r)
	if c != code || m != message {
		t.Errorf("error = %q %q, want %q %q", c, m, code, message)
	}
}

func TestAC237_ListIsOrderedByNameWithUsedBy(t *testing.T) {
	s := newStack(t)
	for _, n := range []string{"b", "a-2", "a"} {
		s.post(t, `{"name":"`+n+`","type":"custom","body":"x"}`)
	}
	a := s.insert(t, `SELECT id FROM skills WHERE name = 'a'`)
	x, y := s.agent(t, "x", true), s.agent(t, "y", true)
	s.link(t, x, a, 0)
	s.link(t, y, a, 0)

	list := decodeList(t, s.do(t, http.MethodGet, "/skills", ""))
	if diff := cmp.Diff([]string{"a", "a-2", "b"}, names(list)); diff != "" {
		t.Errorf("GET /skills order mismatch (-want +got):\n%s", diff)
	}
	if list[0]["used_by"] != float64(2) {
		t.Errorf("a.used_by = %v, want 2", list[0]["used_by"])
	}
}

func TestAC238_AC239_Create(t *testing.T) {
	s := newStack(t)
	got := s.post(t, `{"name":"x","type":"custom","body":"b","source":"imported_url","source_ref":"https://h/x.md","enabled":true}`)
	if got["enabled"] != false || got["source_ref"] != "https://h/x.md" || got["version"] != float64(1) || got["used_by"] != float64(0) || got["source"] != "imported_url" {
		t.Errorf("POST /skills (imported) = %v, want disabled, source_ref kept, version 1, used_by 0", got)
	}
	id := got["id"].(string)
	versions := decodeList(t, s.do(t, http.MethodGet, "/skills/"+id+"/versions", ""))
	if len(versions) != 1 || versions[0]["message"] != "Imported from https://h/x.md" || versions[0]["description"] != "" {
		t.Errorf("versions = %v, want one snapshot 'Imported from https://h/x.md'", versions)
	}

	manual := s.post(t, `{"name":"m","type":"rubric","body":"b","source_ref":"ignored"}`)
	if manual["source_ref"] != nil || manual["enabled"] != true || manual["source"] != "manual" || manual["description"] != "" {
		t.Errorf("POST /skills (manual) = %v, want source_ref null, enabled true, source manual", manual)
	}
	mv := decodeList(t, s.do(t, http.MethodGet, "/skills/"+manual["id"].(string)+"/versions", ""))
	if mv[0]["message"] != "Created" {
		t.Errorf("manual snapshot message = %v, want Created", mv[0]["message"])
	}

	r := s.do(t, http.MethodPost, "/skills", `{"name":"x","type":"custom","body":"b"}`)
	if r.status != 409 || r.body != `{"error":{"code":"conflict","message":"A skill named \"x\" already exists in this workspace"}}` {
		t.Errorf("duplicate POST /skills = %d %s, want the 409 conflict envelope", r.status, r.body)
	}
}

func TestAC240_AC241_Update(t *testing.T) {
	s := newStack(t)
	sk := s.post(t, `{"name":"x","type":"custom","body":"b","description":"d"}`)
	id := sk["id"].(string)
	put := func(body string) response { return s.do(t, http.MethodPut, "/skills/"+id, body) }

	if r := put(`{"body":"b2","description":"d2"}`); r.status != 200 || decodeMap(t, r)["version"] != float64(2) {
		t.Fatalf("PUT body+description = %d %s, want version 2", r.status, r.body)
	}
	s.exec(t, `UPDATE skills SET updated_at = now() - interval '1 hour' WHERE id = ?`, id)
	before := decodeMap(t, s.do(t, http.MethodGet, "/skills/"+id, ""))
	r := put(`{"name":"y","type":"rubric","enabled":false}`)
	after := decodeMap(t, r)
	if after["version"] != float64(2) || after["name"] != "y" || after["type"] != "rubric" || after["enabled"] != false || after["updated_at"] == before["updated_at"] {
		t.Errorf("PUT name/type/enabled = %v, want version 2 kept and a new updated_at", after)
	}
	if r := put(`{"body":"b2"}`); decodeMap(t, r)["version"] != float64(2) {
		t.Errorf("PUT same body = %s, want version 2", r.body)
	}
	put(`{"body":"b3"}`) // version 3

	for _, body := range []string{`{"body":"new","base_version":2}`, `{"base_version":2}`} {
		r := put(body)
		wantError(t, r, 409, "stale_version", "The skill changed since you opened it (you edited v2, current is v3)")
		if _, _, _, details := errorOf(t, r); string(details) != `{"current_version":3}` {
			t.Errorf("stale_version details = %s, want {\"current_version\":3}", details)
		}
	}
	if got := decodeMap(t, s.do(t, http.MethodGet, "/skills/"+id, "")); got["body"] != "b3" || got["version"] != float64(3) {
		t.Errorf("skill after stale updates = %v, want body b3 version 3", got)
	}

	s.post(t, `{"name":"taken","type":"custom","body":"b"}`)
	wantError(t, put(`{"name":"taken"}`), 409, "conflict", `A skill named "taken" already exists in this workspace`)
	wantError(t, s.do(t, http.MethodPut, "/skills/"+someUUID, `{}`), 404, "not_found", "Skill not found")
}

func TestAC242_Delete(t *testing.T) {
	s := newStack(t)
	sk := s.post(t, `{"name":"s","type":"custom","body":"b"}`)["id"].(string)
	tt := s.post(t, `{"name":"t","type":"custom","body":"b"}`)["id"].(string)
	x, y := s.agent(t, "x", true), s.agent(t, "y", true)
	s.link(t, x, sk, 0)
	s.link(t, x, tt, 1)
	s.link(t, y, sk, 0)
	conv := s.insert(t, `INSERT INTO conventions (workspace_id, repo_id, rule, skill_id) VALUES (?, ?, 'r', ?) RETURNING id`, s.ws, s.repo, sk)
	review := s.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind) VALUES (?, ?, 'review') RETURNING id`, s.ws, s.pr)
	finding := s.insert(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, skill_id)
		VALUES (?, 'f', 1, 1, 'WARNING', 'bug', 't', 'r', 0.5, ?) RETURNING id`, review, sk)
	eval := s.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'skill', ?, 'c') RETURNING id`, s.ws, sk)

	if r := s.do(t, http.MethodDelete, "/skills/"+sk, ""); r.status != 200 || r.body != `{"ok":true}` {
		t.Fatalf("DELETE /skills/:id = %d %s, want {\"ok\":true}", r.status, r.body)
	}
	if n := s.count(t, `SELECT count(*) FROM agent_skills WHERE agent_id = ? AND skill_id = ? AND "order" = 1`, x, tt); n != 1 {
		t.Errorf("X's link to T with order 1 = %d rows, want 1 (unchanged)", n)
	}
	if n := s.count(t, `SELECT count(*) FROM agent_skills WHERE skill_id = ?`, sk); n != 0 {
		t.Errorf("links to the deleted skill = %d, want 0", n)
	}
	if n := s.count(t, `SELECT count(*) FROM agents WHERE id IN (?, ?) AND version = 1`, x, y); n != 2 {
		t.Errorf("agents with unchanged version = %d, want 2", n)
	}
	if n := s.count(t, `SELECT count(*) FROM conventions WHERE id = ? AND skill_id IS NULL`, conv); n != 1 {
		t.Errorf("convention with skill_id null = %d, want 1", n)
	}
	if n := s.count(t, `SELECT count(*) FROM findings WHERE id = ? AND skill_id IS NULL`, finding); n != 1 {
		t.Errorf("finding with skill_id null = %d, want 1", n)
	}
	if n := s.count(t, `SELECT count(*) FROM eval_cases WHERE id = ?`, eval); n != 0 {
		t.Errorf("eval case rows = %d, want 0", n)
	}
	wantError(t, s.do(t, http.MethodDelete, "/skills/"+sk, ""), 404, "not_found", "Skill not found")
}

func TestAC243_Versions(t *testing.T) {
	s := newStack(t)
	id := s.post(t, `{"name":"x","type":"custom","body":"b1","description":"d1"}`)["id"].(string)
	s.do(t, http.MethodPut, "/skills/"+id, `{"body":"b2"}`)
	s.do(t, http.MethodPut, "/skills/"+id, `{"body":"b3","description":"d3"}`)

	r := s.do(t, http.MethodPost, "/skills/"+id+"/versions/1/restore", "")
	got := decodeMap(t, r)
	if r.status != 200 || got["version"] != float64(4) || got["body"] != "b1" || got["description"] != "d1" {
		t.Fatalf("restore v1 = %d %s, want version 4 with the body and description of v1", r.status, r.body)
	}
	var msgs []string
	for _, v := range decodeList(t, s.do(t, http.MethodGet, "/skills/"+id+"/versions", "")) {
		msgs = append(msgs, v["message"].(string))
	}
	if diff := cmp.Diff([]string{"Restored from v1", "Edited body and description", "Edited body", "Created"}, msgs); diff != "" {
		t.Errorf("version messages mismatch (-want +got):\n%s", diff)
	}
	one := decodeMap(t, s.do(t, http.MethodGet, "/skills/"+id+"/versions/1", ""))
	if one["skill_id"] != id || one["version"] != float64(1) || one["body"] != "b1" {
		t.Errorf("GET version 1 = %v", one)
	}
	wantError(t, s.do(t, http.MethodGet, "/skills/"+id+"/versions/9", ""), 404, "not_found", "Skill version not found")
	wantError(t, s.do(t, http.MethodPost, "/skills/"+id+"/versions/9/restore", ""), 404, "not_found", "Skill version not found")
	wantError(t, s.do(t, http.MethodPost, "/skills/"+someUUID+"/versions/1/restore", ""), 404, "not_found", "Skill not found")
	wantError(t, s.do(t, http.MethodGet, "/skills/"+someUUID+"/versions", ""), 404, "not_found", "Skill not found")
	wantError(t, s.do(t, http.MethodGet, "/skills/"+someUUID+"/versions/1", ""), 404, "not_found", "Skill version not found")
	if got := decodeMap(t, s.do(t, http.MethodGet, "/skills/"+id, "")); got["version"] != float64(4) {
		t.Errorf("version after failed restores = %v, want 4", got["version"])
	}
}

func TestAC244_Agents(t *testing.T) {
	s := newStack(t)
	id := s.post(t, `{"name":"x","type":"custom","body":"b"}`)["id"].(string)
	beta, alpha := s.agent(t, "beta", false), s.agent(t, "alpha", true)
	s.link(t, beta, id, 0)
	s.link(t, alpha, id, 0)

	want := []map[string]any{{"id": alpha, "name": "alpha", "enabled": true}, {"id": beta, "name": "beta", "enabled": false}}
	if diff := cmp.Diff(want, decodeList(t, s.do(t, http.MethodGet, "/skills/"+id+"/agents", ""))); diff != "" {
		t.Errorf("GET /skills/:id/agents mismatch (-want +got):\n%s", diff)
	}
	wantError(t, s.do(t, http.MethodGet, "/skills/"+someUUID+"/agents", ""), 404, "not_found", "Skill not found")
}

func TestAC245_StatsWithoutData(t *testing.T) {
	s := newStack(t)
	b := s.post(t, `{"name":"b","type":"custom","body":"x"}`)["id"].(string)
	a := s.post(t, `{"name":"a","type":"custom","body":"x"}`)["id"].(string)
	x := s.agent(t, "x", true)
	s.link(t, x, a, 0)

	want := []map[string]any{
		{"skill_id": a, "pull_rate": nil, "accept_rate": nil, "findings": float64(0)},
		{"skill_id": b, "pull_rate": nil, "accept_rate": nil, "findings": float64(0)},
	}
	if diff := cmp.Diff(want, decodeList(t, s.do(t, http.MethodGet, "/skills/stats", ""))); diff != "" {
		t.Errorf("GET /skills/stats mismatch (-want +got):\n%s", diff)
	}
	wantDetail := map[string]any{
		"skill_id": a, "window_days": float64(7), "runs_attached": float64(0), "runs_cited": float64(0), "pull_rate": nil,
		"findings": float64(0), "accepted": float64(0), "dismissed": float64(0), "accept_rate": nil,
		"by_category": []any{}, "by_severity": []any{}, "used_by": []any{map[string]any{"id": x, "name": "x", "enabled": true}},
	}
	if diff := cmp.Diff(wantDetail, decodeMap(t, s.do(t, http.MethodGet, "/skills/"+a+"/stats?days=7", ""))); diff != "" {
		t.Errorf("GET /skills/:id/stats mismatch (-want +got):\n%s", diff)
	}
	wantError(t, s.do(t, http.MethodGet, "/skills/"+someUUID+"/stats", ""), 404, "not_found", "Skill not found")
}

func TestAC246_StatsWithRuns(t *testing.T) {
	s := newStack(t)
	skill := s.post(t, `{"name":"s","type":"custom","body":"x"}`)["id"].(string)
	agent := s.agent(t, "a", true)
	now := time.Now()
	run := func(status string, age time.Duration) string {
		id := s.insert(t, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, ran_at, status) VALUES (?, ?, ?, ?, ?) RETURNING id`,
			s.ws, agent, s.pr, now.Add(-age), status)
		s.exec(t, `INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 0)`, id, skill)
		return id
	}
	done := run("done", time.Hour)
	run("done", 2*time.Hour)
	run("failed", 3*time.Hour)
	run("done", 40*24*time.Hour)
	review := s.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?) RETURNING id`, s.ws, s.pr, done)
	for _, f := range []struct {
		category            string
		accepted, dismissed *time.Time
	}{
		{"bug", &now, nil}, {"bug", nil, &now}, {"security", nil, nil},
	} {
		s.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, skill_id, accepted_at, dismissed_at)
			VALUES (?, 'f', 1, 1, 'WARNING', ?, 't', 'r', 0.5, ?, ?, ?)`, review, f.category, skill, f.accepted, f.dismissed)
	}

	got := decodeMap(t, s.do(t, http.MethodGet, "/skills/"+skill+"/stats", ""))
	for key, want := range map[string]any{
		"window_days": float64(30), "runs_attached": float64(2), "runs_cited": float64(1), "pull_rate": 0.5,
		"findings": float64(3), "accepted": float64(1), "dismissed": float64(1), "accept_rate": 0.5,
	} {
		if got[key] != want {
			t.Errorf("stats %s = %v, want %v", key, got[key], want)
		}
	}
	wantCat := []any{map[string]any{"key": "bug", "count": float64(2)}, map[string]any{"key": "security", "count": float64(1)}}
	if diff := cmp.Diff(wantCat, got["by_category"]); diff != "" {
		t.Errorf("by_category mismatch (-want +got):\n%s", diff)
	}
	summary := decodeList(t, s.do(t, http.MethodGet, "/skills/stats?days=30", ""))
	if len(summary) != 1 || summary[0]["pull_rate"] != 0.5 || summary[0]["findings"] != float64(3) {
		t.Errorf("GET /skills/stats = %v, want pull_rate 0.5 and 3 findings", summary)
	}
	// A 60-day window includes the old run: 3 attached, still 1 cited.
	wide := decodeMap(t, s.do(t, http.MethodGet, "/skills/"+skill+"/stats?days=60", ""))
	if wide["runs_attached"] != float64(3) || wide["runs_cited"] != float64(1) {
		t.Errorf("stats over 60 days = attached %v cited %v, want 3 and 1", wide["runs_attached"], wide["runs_cited"])
	}
	for _, days := range []string{"0", "366", "abc"} {
		if r := s.do(t, http.MethodGet, "/skills/"+skill+"/stats?days="+days, ""); r.status != 422 {
			t.Errorf("GET stats?days=%s = %d, want 422", days, r.status)
		}
	}
}

func TestAC248_Community(t *testing.T) {
	s := newStack(t)
	list := decodeList(t, s.do(t, http.MethodGet, "/skills/community", ""))
	if len(list) != 8 {
		t.Fatalf("GET /skills/community = %d cards, want 8", len(list))
	}
	var keys []string
	for k := range list[0] {
		keys = append(keys, k)
	}
	if len(keys) != 8 {
		t.Errorf("card keys = %v, want exactly id, name, repo, stars, lang, desc, type, tags", keys)
	}
	if list[0]["repo"] != "devdigest/community-skills" || list[0]["stars"] != float64(0) || list[0]["id"] != list[0]["name"] {
		t.Errorf("first card = %v, want repo devdigest/community-skills, stars 0, id = name", list[0])
	}
	if diff := cmp.Diff([]string{"react-hooks-rules", "a11y-jsx-audit"}, names(decodeList(t, s.do(t, http.MethodGet, "/skills/community?tag=REACT", "")))); diff != "" {
		t.Errorf("?tag=REACT mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"owasp-top-10-review"}, names(decodeList(t, s.do(t, http.MethodGet, "/skills/community?q=owasp", "")))); diff != "" {
		t.Errorf("?q=owasp mismatch (-want +got):\n%s", diff)
	}
	if r := s.do(t, http.MethodGet, "/skills/community?lang=sql&q=xyz", ""); r.body != "[]" {
		t.Errorf("?lang=sql&q=xyz = %s, want []", r.body)
	}
	if len(s.fetcher.urls) != 0 {
		t.Errorf("listing the catalog fetched %v, want no network access", s.fetcher.urls)
	}
}

func previewBody(kind string, fields map[string]string) string {
	m := map[string]string{"kind": kind}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestAC249_CommunityPreview(t *testing.T) {
	s := newStack(t)
	r := s.do(t, http.MethodPost, "/skills/import/preview", previewBody("community", map[string]string{"id": "sql-injection-gate"}))
	if r.status != 200 {
		t.Fatalf("preview = %d %s", r.status, r.body)
	}
	got := decodeMap(t, r)
	if got["name"] != "sql-injection-gate" || got["type"] != "security" || !strings.Contains(got["body"].(string), "${req.params.id}") ||
		got["source"] != "community" || got["source_ref"] != "community:sql-injection-gate" {
		t.Errorf("preview = %v, want the sql-injection-gate entry from the community source", got)
	}
	if diff := cmp.Diff([]any{"sql-injection-gate.md"}, got["included_files"]); diff != "" {
		t.Errorf("included_files mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{}, got["ignored_files"]); diff != "" {
		t.Errorf("ignored_files mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{}, got["warnings"]); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if n := s.count(t, `SELECT count(*) FROM skills`); n != 0 {
		t.Errorf("skills after a preview = %d, want 0", n)
	}
	wantError(t, s.do(t, http.MethodPost, "/skills/import/preview", previewBody("community", map[string]string{"id": "nope"})),
		422, "invalid_import", `Unknown community skill "nope"`)
}

func TestAC252_FileImportMessages(t *testing.T) {
	s := newStack(t)
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	tests := []struct{ name, file, content, want string }{
		{"extension", "a.pdf", b64([]byte("x")), "Upload a .md, .markdown, .txt or .zip file"},
		{"too many bytes", "a.md", b64([]byte(strings.Repeat("a", 262145))), "The file is 262145 bytes; the limit is 262144"},
		{"invalid utf-8", "a.md", b64([]byte("ok \xff")), "The file is not valid UTF-8 text"},
		{"too many chars", "a.md", strings.Repeat("A", 2097153), "The upload is larger than 2097152 base64 chars"},
		{"not a zip", "a.zip", b64([]byte("nope")), "The archive could not be read (not a valid .zip)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, s.do(t, http.MethodPost, "/skills/import/preview", previewBody("file", map[string]string{"filename": tc.file, "content_base64": tc.content})),
				422, "invalid_import", tc.want)
		})
	}
}

func TestAC258_URLPreview(t *testing.T) {
	s := newStack(t)
	final, _ := url.Parse("https://raw.githubusercontent.com/o/r/main/skills/x.md")
	s.fetcher.doc = domain.FetchedDocument{Bytes: []byte("Review the diff."), ContentType: "text/plain", FinalURL: final}
	typed := "https://github.com/o/r/blob/main/skills/x.md?plain=1"

	r := s.do(t, http.MethodPost, "/skills/import/preview", previewBody("url", map[string]string{"url": typed}))
	got := decodeMap(t, r)
	if r.status != 200 || got["source"] != "imported_url" || got["source_ref"] != typed || got["name"] != "x" {
		t.Fatalf("preview = %d %s, want imported_url from the typed URL named x", r.status, r.body)
	}
	if diff := cmp.Diff([]string{"https://raw.githubusercontent.com/o/r/main/skills/x.md"}, s.fetcher.urls); diff != "" {
		t.Errorf("fetched URLs mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{"/o/r/main/skills/x.md"}, got["included_files"]); diff != "" {
		t.Errorf("included_files mismatch (-want +got):\n%s", diff)
	}

	s.fetcher.urls = nil
	wantError(t, s.do(t, http.MethodPost, "/skills/import/preview", previewBody("url", map[string]string{"url": "https://127.0.0.1/a.md"})),
		422, "invalid_import", "127.0.0.1 is not a public address")
	if len(s.fetcher.urls) != 0 {
		t.Errorf("a non-public URL reached the fetcher: %v", s.fetcher.urls)
	}
}

func TestAC259_ConfirmingAPreviewCreatesADisabledSkill(t *testing.T) {
	s := newStack(t)
	final, _ := url.Parse("https://h.example/x.md")
	s.fetcher.doc = domain.FetchedDocument{Bytes: []byte("Body of x."), FinalURL: final}
	requests := []string{
		previewBody("community", map[string]string{"id": "sql-injection-gate"}),
		previewBody("url", map[string]string{"url": "https://h.example/x.md"}),
		previewBody("file", map[string]string{"filename": "f.md", "content_base64": base64.StdEncoding.EncodeToString([]byte("Body of f."))}),
	}
	for i, req := range requests {
		if n := s.count(t, `SELECT count(*) FROM skills`); n != i {
			t.Fatalf("skills before confirming preview %d = %d, want %d", i, n, i)
		}
		p := decodeMap(t, s.do(t, http.MethodPost, "/skills/import/preview", req))
		confirm, _ := json.Marshal(map[string]any{
			"name": p["name"], "description": p["description"], "type": p["type"], "body": p["body"],
			"source": p["source"], "source_ref": p["source_ref"], "enabled": true,
		})
		created := s.post(t, string(confirm))
		if created["enabled"] != false || created["source"] != p["source"] || created["source_ref"] != p["source_ref"] {
			t.Errorf("confirmed %v = %v, want a disabled skill with the preview's source and source_ref", p["source"], created)
		}
	}
}

func TestSkillsOfAnotherWorkspaceAre404(t *testing.T) {
	s := newStack(t)
	foreign := s.insert(t, `INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES (?, 'f', '', 'custom', 'manual', 'b') RETURNING id`, s.other)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/skills/" + foreign, ""}, {"PUT", "/skills/" + foreign, `{}`}, {"DELETE", "/skills/" + foreign, ""},
		{"GET", "/skills/" + foreign + "/versions", ""}, {"GET", "/skills/" + foreign + "/agents", ""},
		{"GET", "/skills/" + foreign + "/stats", ""}, {"POST", "/skills/" + foreign + "/versions/1/restore", ""},
	} {
		if r := s.do(t, tc.method, tc.path, tc.body); r.status != 404 {
			t.Errorf("%s %s = %d %s, want 404", tc.method, tc.path, r.status, r.body)
		}
	}
	if list := decodeList(t, s.do(t, http.MethodGet, "/skills", "")); len(list) != 0 {
		t.Errorf("GET /skills = %v, want the other workspace's skill hidden", list)
	}
}

// mergeScene is the AC278 setup: accepted conventions C1, C2 and a pending P of repo o/r, agents
// X (linking T) and Y.
type mergeScene struct {
	*stack
	c1, c2, pending string
	x, y, t         string
}

func newMergeScene(t *testing.T) mergeScene {
	t.Helper()
	s := newStack(t)
	m := mergeScene{stack: s}
	conv := func(status string) string {
		return s.insert(t, `INSERT INTO conventions (workspace_id, repo_id, rule, status) VALUES (?, ?, 'rule', ?) RETURNING id`, s.ws, s.repo, status)
	}
	m.c1, m.c2, m.pending = conv("accepted"), conv("accepted"), conv("pending")
	m.t = s.post(t, `{"name":"t","type":"custom","body":"b"}`)["id"].(string)
	m.x, m.y = s.agent(t, "X", true), s.agent(t, "Y", false)
	s.link(t, m.x, m.t, 0)
	return m
}

func (m mergeScene) post(t *testing.T, body string) response {
	t.Helper()
	return m.do(t, http.MethodPost, "/repos/"+m.repo+"/conventions/skill", body)
}

// assertUnchanged checks that a failed merge left no skill, link or convention change.
func (m mergeScene) assertUnchanged(t *testing.T) {
	t.Helper()
	if n := m.count(t, `SELECT count(*) FROM skills WHERE name <> 't'`); n != 0 {
		t.Errorf("skills created by a failed merge = %d, want 0", n)
	}
	if n := m.count(t, `SELECT count(*) FROM agent_skills`); n != 1 {
		t.Errorf("agent links after a failed merge = %d, want 1", n)
	}
	if n := m.count(t, `SELECT count(*) FROM agents WHERE version > 1`); n != 0 {
		t.Errorf("agents with a bumped version after a failed merge = %d, want 0", n)
	}
	if n := m.count(t, `SELECT count(*) FROM conventions WHERE skill_id IS NOT NULL`); n != 0 {
		t.Errorf("conventions marked by a failed merge = %d, want 0", n)
	}
}

func TestAC278_MergeConventions(t *testing.T) {
	m := newMergeScene(t)
	body := `{"convention_ids":["` + m.c1 + `","` + m.c2 + `","` + m.c1 + `"],"name":"house-rules","body":"rules","agent_ids":["` + m.x + `","` + m.y + `"]}`
	r := m.post(t, body)
	if r.status != 201 {
		t.Fatalf("merge = %d %s, want 201", r.status, r.body)
	}
	var got struct {
		Skill        map[string]any   `json:"skill"`
		LinkedAgents []map[string]any `json:"linked_agents"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatal(err)
	}
	skillID := got.Skill["id"].(string)
	for key, want := range map[string]any{
		"source": "extracted", "source_ref": "conventions:o/r", "type": "convention", "enabled": true, "used_by": float64(2), "version": float64(1),
	} {
		if got.Skill[key] != want {
			t.Errorf("skill.%s = %v, want %v", key, got.Skill[key], want)
		}
	}
	wantAgents := []map[string]any{{"id": m.x, "name": "X", "enabled": true}, {"id": m.y, "name": "Y", "enabled": false}}
	if diff := cmp.Diff(wantAgents, got.LinkedAgents); diff != "" {
		t.Errorf("linked_agents mismatch (-want +got):\n%s", diff)
	}
	if n := m.count(t, `SELECT count(*) FROM agent_skills WHERE agent_id = ? AND skill_id = ? AND "order" = 1`, m.x, skillID); n != 1 {
		t.Errorf("X's new link at order 1 = %d rows, want 1 (links [T, house-rules])", n)
	}
	if n := m.count(t, `SELECT count(*) FROM agents WHERE id IN (?, ?) AND version = 2`, m.x, m.y); n != 2 {
		t.Errorf("agents with the bumped version = %d, want 2", n)
	}
	if n := m.count(t, `SELECT count(*) FROM conventions WHERE skill_id = ?`, skillID); n != 2 {
		t.Errorf("conventions marked with the skill = %d, want 2", n)
	}
	if n := m.count(t, `SELECT count(*) FROM skill_versions WHERE skill_id = ? AND message = 'Extracted from conventions of o/r'`, skillID); n != 1 {
		t.Errorf("version-1 snapshot with the extracted message = %d, want 1", n)
	}
	if list := decodeList(t, m.do(t, http.MethodGet, "/skills", "")); list[0]["name"] != "house-rules" || list[0]["used_by"] != float64(2) {
		t.Errorf("GET /skills = %v, want house-rules first with used_by 2", names(list))
	}
}

func TestAC279_MergeFailuresChangeNothing(t *testing.T) {
	m := newMergeScene(t)
	unknownAgent := "00000000-0000-4000-8000-0000000000aa"
	m.insert(t, `INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES (?, 'taken', '', 'custom', 'manual', 'b') RETURNING id`, m.ws)

	r := m.post(t, `{"convention_ids":["`+m.pending+`"],"name":"x","body":"b"}`)
	wantError(t, r, 422, "validation_error", "Only accepted conventions of this repo can be merged into a skill")
	if _, _, _, details := errorOf(t, r); string(details) != `{"ids":["`+m.pending+`"]}` {
		t.Errorf("details = %s, want ids [pending]", details)
	}
	r = m.post(t, `{"convention_ids":["`+m.c1+`"],"name":"x","body":"b","agent_ids":["`+unknownAgent+`"]}`)
	wantError(t, r, 422, "unknown_agent", "Unknown agent")
	if _, _, _, details := errorOf(t, r); string(details) != `{"id":"`+unknownAgent+`"}` {
		t.Errorf("details = %s, want id of the unknown agent", details)
	}
	wantError(t, m.post(t, `{"convention_ids":["`+m.c1+`"],"name":"taken","body":"b","agent_ids":["`+m.x+`"]}`),
		409, "conflict", `A skill named "taken" already exists in this workspace`)
	wantError(t, m.do(t, http.MethodPost, "/repos/"+someUUID+"/conventions/skill", `{"convention_ids":["`+m.c1+`"],"name":"x","body":"b"}`),
		404, "not_found", "Repo not found")

	if n := m.count(t, `SELECT count(*) FROM skills WHERE name NOT IN ('t', 'taken')`); n != 0 {
		t.Errorf("skills created by failed merges = %d, want 0", n)
	}
	if n := m.count(t, `SELECT count(*) FROM agent_skills`); n != 1 {
		t.Errorf("agent links after failed merges = %d, want 1", n)
	}
	if n := m.count(t, `SELECT count(*) FROM conventions WHERE skill_id IS NOT NULL`); n != 0 {
		t.Errorf("conventions marked by failed merges = %d, want 0", n)
	}
}

func TestMergeRollsBackWhenALaterStepFails(t *testing.T) {
	boom := errors.New("boom")
	for name, arm := range map[string]func(*mergeScene){
		"linking the agent fails after the skill was inserted": func(m *mergeScene) { m.agents.fail = boom },
		"marking the conventions fails after the links":        func(m *mergeScene) { m.conv.fail = boom },
	} {
		t.Run(name, func(t *testing.T) {
			m := newMergeScene(t)
			arm(&m)
			r := m.post(t, `{"convention_ids":["`+m.c1+`"],"name":"house-rules","body":"b","agent_ids":["`+m.x+`"]}`)
			if r.status != 500 {
				t.Fatalf("merge = %d %s, want 500", r.status, r.body)
			}
			m.assertUnchanged(t)
		})
	}
}

func TestMergeDefaultsAndDisabledSkill(t *testing.T) {
	m := newMergeScene(t)
	r := m.post(t, `{"convention_ids":["`+m.c1+`"],"name":"quiet","body":"b","enabled":false,"type":"security","description":"when"}`)
	got := decodeMap(t, r)["skill"].(map[string]any)
	if r.status != 201 || got["enabled"] != false || got["type"] != "security" || got["description"] != "when" || got["used_by"] != float64(0) {
		t.Errorf("merge = %d %s, want a disabled security skill with no linked agents", r.status, r.body)
	}
}
