package postgres_test

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/workspace/adapters/postgres"
	"dev-digest-go/backend/internal/workspace/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// readsDB is a migrated database with the store under test and SQL seeding helpers.
type readsDB struct {
	t     *testing.T
	db    *gorm.DB
	store *pgstore.Store
}

func newReadsDB(t *testing.T) *readsDB {
	t.Helper()
	d := pgtest.Migrated(t)
	return &readsDB{t: t, db: d.DB, store: pgstore.NewStore(d.DB)}
}

// scalar runs an INSERT ... RETURNING id (or any single-value statement).
func (r *readsDB) scalar(query string, args ...any) string {
	r.t.Helper()
	var out string
	if err := r.db.Raw(query, args...).Scan(&out).Error; err != nil {
		r.t.Fatalf("%.50s: %v", query, err)
	}
	return out
}

func (r *readsDB) exec(query string, args ...any) {
	r.t.Helper()
	if err := r.db.Exec(query, args...).Error; err != nil {
		r.t.Fatalf("exec %.50s: %v", query, err)
	}
}

func (r *readsDB) workspace(name string) string {
	r.t.Helper()
	return r.scalar(`INSERT INTO workspaces (name) VALUES (?) RETURNING id::text`, name)
}

func (r *readsDB) repo(ws, fullName string) string {
	r.t.Helper()
	owner, name, _ := strings.Cut(fullName, "/")
	return r.scalar(`INSERT INTO repos (workspace_id, owner, name, full_name, clone_path)
		VALUES (?, ?, ?, ?, '/data/clones/secret-path') RETURNING id::text`, ws, owner, name, fullName)
}

// agent inserts an agent created at the given offset (seconds) after a fixed instant, so the
// creation order is under the test's control.
func (r *readsDB) agent(ws, name string, enabled bool, offset int) string {
	r.t.Helper()
	return r.scalar(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled, created_at)
		VALUES (?, ?, 'openai', 'gpt-x', 'prompt of ' || ?, ?, ?::timestamptz + make_interval(secs => ?))
		RETURNING id::text`, ws, name, name, enabled, epoch, offset)
}

func (r *readsDB) skill(ws, name, source string) string {
	r.t.Helper()
	return r.scalar(`INSERT INTO skills (workspace_id, name, description, type, source, body)
		VALUES (?, ?, 'd', 'rubric', ?, 'body') RETURNING id::text`, ws, name, source)
}

func (r *readsDB) memory(ws string, repoID *string, scope, kind string) {
	r.t.Helper()
	r.exec(`INSERT INTO memory (workspace_id, repo_id, scope, kind, content) VALUES (?, ?, ?, ?, 'c')`,
		ws, repoID, scope, kind)
}

const epoch = "2026-01-01T00:00:00Z"

// jsonEqual compares raw JSON by value (the database normalises spacing), nil only equals nil.
var jsonEqual = cmp.Comparer(func(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
})

var timeEqual = cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })

func ptr[T any](v T) *T { return &v }

func TestStats_Fixture(t *testing.T) { // AC11, FR11
	h := newReadsDB(t)
	ws := h.workspace("a")
	other := h.workspace("b")

	h.agent(ws, "one", true, 0)
	h.agent(ws, "two", true, 1)
	h.agent(ws, "three", false, 2)
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		h.skill(ws, n, "manual")
	}
	for i := range 12 {
		h.exec(`INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name)
			VALUES (?, 'agent', gen_random_uuid(), ?)`, ws, "case"+string(rune('a'+i)))
	}
	repo1 := h.repo(ws, "o/r1")
	h.repo(ws, "o/r2")
	kinds := []string{"decision", "convention", "preference", "fact", "learning", "fact"}
	for i := range 24 {
		var repoID *string
		scope := "global"
		if i%3 == 0 {
			repoID, scope = &repo1, "repo"
		}
		h.memory(ws, repoID, scope, kinds[i%len(kinds)])
	}
	statuses := []struct{ source, status string }{
		{"local", "done"}, {"local", "failed"}, {"local", "cancelled"}, {"local", "running"},
		{"ci", "done"}, {"ci", "failed"}, {"local", "done"},
	}
	for _, s := range statuses {
		h.exec(`INSERT INTO agent_runs (workspace_id, source, status) VALUES (?, ?, ?)`, ws, s.source, s.status)
	}

	// Another workspace's rows must not count.
	h.agent(other, "x", true, 0)
	h.skill(other, "x", "manual")
	h.repo(other, "o/r1")
	h.memory(other, nil, "global", "fact")
	h.exec(`INSERT INTO agent_runs (workspace_id, status) VALUES (?, 'done')`, other)

	got, err := h.store.Stats(t.Context(), ws)
	if err != nil {
		t.Fatalf("Stats(%s): %v", ws, err)
	}
	want := domain.Stats{Agents: 3, Skills: 6, EvalCases: 12, Learnings: 24, Repos: 2, Runs: 7}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Stats(%s) mismatch (-want +got):\n%s", ws, diff)
	}

	gotOther, err := h.store.Stats(t.Context(), other)
	if err != nil {
		t.Fatalf("Stats(%s): %v", other, err)
	}
	wantOther := domain.Stats{Agents: 1, Skills: 1, Repos: 1, Learnings: 1, Runs: 1}
	if diff := cmp.Diff(wantOther, gotOther); diff != "" {
		t.Errorf("Stats(%s) mismatch (-want +got):\n%s", other, diff)
	}
}

func TestStats_EmptyWorkspaceIsZero(t *testing.T) {
	h := newReadsDB(t)
	ws := h.workspace("empty")
	got, err := h.store.Stats(t.Context(), ws)
	if err != nil {
		t.Fatalf("Stats(%s): %v", ws, err)
	}
	if diff := cmp.Diff(domain.Stats{}, got); diff != "" {
		t.Errorf("Stats(%s) mismatch (-want +got):\n%s", ws, diff)
	}
}

func TestWorkspaceIDs(t *testing.T) {
	h := newReadsDB(t)
	a := h.workspace("a")
	b := h.workspace("b")
	got, err := h.store.WorkspaceIDs(t.Context())
	if err != nil {
		t.Fatalf("WorkspaceIDs() error: %v", err)
	}
	if diff := cmp.Diff([]string{a, b}, got); diff != "" {
		t.Errorf("WorkspaceIDs() mismatch (-want +got):\n%s", diff)
	}
}

func TestExportData_EmptyWorkspace(t *testing.T) { // EC12
	h := newReadsDB(t)
	ws := h.workspace("empty")
	got, err := h.store.ExportData(t.Context(), ws)
	if err != nil {
		t.Fatalf("ExportData(%s): %v", ws, err)
	}
	if diff := cmp.Diff(domain.ExportData{
		Agents: []domain.AgentRecord{}, Skills: []domain.SkillRecord{}, EvalCases: []domain.EvalCaseRecord{},
		Conventions: []domain.ConventionRecord{}, Learnings: []domain.LearningRecord{},
		ContextFiles: []domain.ContextFileRecord{},
	}, got); diff != "" {
		t.Errorf("ExportData(%s) mismatch (-want +got):\n%s", ws, diff)
	}
	b := domain.BuildBundle(got, domain.Meta{Name: "empty", Now: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)})
	if diff := cmp.Diff(domain.ManifestCounts{}, b.Manifest.Counts); diff != "" {
		t.Errorf("BuildBundle(empty).Manifest.Counts mismatch (-want +got):\n%s", diff)
	}
	if len(b.Settings) != 0 {
		t.Errorf("BuildBundle(empty).Settings = %v, want empty", b.Settings)
	}
}

// exportFixture seeds one workspace with every kind of export record and returns the ids the
// expectations need.
type exportFixture struct {
	ws                                 string
	agentA1, agentA2, agentB           string
	skillAlpha, skillBeta, skillImport string
	repoX, repoY                       string
}

func seedExport(h *readsDB) exportFixture {
	f := exportFixture{ws: h.workspace("main")}
	ws := f.ws

	// Two agents named "Reviewer" (created 1 s apart, the second inserted first) and one other.
	f.agentA2 = h.agent(ws, "Reviewer", true, 10)
	f.agentA1 = h.agent(ws, "Reviewer", false, 5)
	f.agentB = h.agent(ws, "Auditor", true, 20)
	h.exec(`UPDATE agents SET output_schema = '{"type":"object"}', strategy = 'map-reduce',
		ci_fail_on = 'any', repo_intel = false, description = 'checks things' WHERE id = ?`, f.agentA1)

	// Skills named so that a bytewise sort differs from a locale sort ("Zeta" < "alpha").
	f.skillAlpha = h.skill(ws, "alpha", "manual")
	f.skillBeta = h.skill(ws, "Zeta", "extracted")
	f.skillImport = h.skill(ws, "imported", "imported_file")
	h.exec(`UPDATE skills SET evidence_files = '["a.go"]' WHERE id = ?`, f.skillImport)

	// Link order is the "order" column, not the skill name: Zeta first, then alpha.
	h.exec(`INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 0), (?, ?, 1)`,
		f.agentA1, f.skillBeta, f.agentA1, f.skillAlpha)
	h.exec(`INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 0)`, f.agentB, f.skillImport)

	// Three versions of "imported", inserted out of order.
	for _, v := range []int{3, 1, 2} {
		h.exec(`INSERT INTO skill_versions (skill_id, version, body, created_at, description, message)
			VALUES (?, ?, ?, ?::timestamptz + make_interval(mins => ?), ?, ?)`,
			f.skillImport, v, "body v"+string(rune('0'+v)), epoch, v,
			map[bool]*string{true: ptr("desc"), false: nil}[v == 2],
			map[bool]*string{true: ptr("msg"), false: nil}[v == 3])
	}

	// Eval cases: one per owner kind, inserted so that name order differs from creation order.
	h.exec(`INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, input_files,
		input_meta, expected_output, notes, created_at) VALUES
		(?, 'skill', ?, 'b-skill-case', NULL, NULL, NULL, NULL, NULL, ?::timestamptz),
		(?, 'agent', ?, 'a-agent-case', 'diff', '[{"path":"x"}]', '{"k":1}', '{"findings":[]}', 'note', ?::timestamptz + interval '1 s')`,
		ws, f.skillImport, epoch, ws, f.agentA1, epoch)

	f.repoX, f.repoY = h.repo(ws, "org/x"), h.repo(ws, "Org/y")

	// Conventions: evidence with two entries (the first counts), an empty array, an entry
	// without a snippet.
	h.exec(`INSERT INTO conventions (workspace_id, repo_id, rule, confidence, category, evidence, status, created_at) VALUES
		(?, ?, 'rule-two-entries', 0.9, 'naming',
			'[{"path":"a.go","start_line":1,"end_line":2,"snippet":"first"},{"path":"b.go","snippet":"second"}]',
			'accepted', ?::timestamptz),
		(?, ?, 'rule-empty', NULL, 'other', '[]', 'pending', ?::timestamptz + interval '1 s'),
		(?, ?, 'rule-no-snippet', 0.5, 'style', '[{"path":"c.go"}]', 'rejected', ?::timestamptz + interval '2 s')`,
		f.ws, f.repoX, epoch, f.ws, f.repoX, epoch, f.ws, f.repoY, epoch)

	// Learnings with an embedding and sources that must never come out.
	h.exec(`INSERT INTO memory (workspace_id, repo_id, scope, kind, content, embedding, confidence, sources, created_at) VALUES
		(?, ?, 'repo', 'decision', 'repo learning', array_fill(0.5::real, ARRAY[1536])::vector, 0.7,
			'[{"secret":"source-text"}]', ?::timestamptz),
		(?, NULL, 'global', 'fact', 'global learning', NULL, NULL, NULL, ?::timestamptz + interval '1 s')`,
		f.ws, f.repoX, epoch, f.ws, epoch)

	// Context files: paths where bytewise order differs from locale order.
	h.exec(`INSERT INTO context_files (repo_id, path, content, size_bytes) VALUES
		(?, 'docs/b.md', 'B', 1), (?, 'Docs/a.md', 'A', 1), (?, 'z.md', 'Z', 1)`, f.repoX, f.repoX, f.repoY)
	return f
}

func TestExportData_Fixture(t *testing.T) { // FR13, EC13 input, EC14
	h := newReadsDB(t)
	f := seedExport(h)

	got, err := h.store.ExportData(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("ExportData(%s): %v", f.ws, err)
	}

	want := domain.ExportData{
		Agents: []domain.AgentRecord{
			{ID: f.agentA1, Name: "Reviewer", Description: "checks things", Provider: "openai", Model: "gpt-x",
				SystemPrompt: "prompt of Reviewer", OutputSchema: json.RawMessage(`{"type":"object"}`),
				Enabled: false, Strategy: "map-reduce", CIFailOn: "any", RepoIntel: false,
				SkillNames: []string{"Zeta", "alpha"}},
			{ID: f.agentA2, Name: "Reviewer", Provider: "openai", Model: "gpt-x",
				SystemPrompt: "prompt of Reviewer", Enabled: true, Strategy: "single-pass", CIFailOn: "critical",
				RepoIntel: true},
			{ID: f.agentB, Name: "Auditor", Provider: "openai", Model: "gpt-x", SystemPrompt: "prompt of Auditor",
				Enabled: true, Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true,
				SkillNames: []string{"imported"}},
		},
		Skills: []domain.SkillRecord{
			{ID: f.skillBeta, Name: "Zeta", Description: "d", Type: "rubric", Source: "extracted", Body: "body",
				Enabled: true},
			{ID: f.skillAlpha, Name: "alpha", Description: "d", Type: "rubric", Source: "manual", Body: "body",
				Enabled: true},
			{ID: f.skillImport, Name: "imported", Description: "d", Type: "rubric", Source: "imported_file",
				Body: "body", Enabled: true, EvidenceFiles: json.RawMessage(`["a.go"]`),
				Versions: []domain.SkillVersionRecord{
					{Version: 1, Body: "body v1", CreatedAt: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)},
					{Version: 2, Body: "body v2", Description: ptr("desc"), CreatedAt: time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC)},
					{Version: 3, Body: "body v3", Message: ptr("msg"), CreatedAt: time.Date(2026, 1, 1, 0, 3, 0, 0, time.UTC)},
				}},
		},
		EvalCases: []domain.EvalCaseRecord{
			{Name: "a-agent-case", OwnerKind: "agent", OwnerID: f.agentA1, InputDiff: ptr("diff"),
				InputFiles: json.RawMessage(`[{"path":"x"}]`), InputMeta: json.RawMessage(`{"k":1}`),
				ExpectedOutput: json.RawMessage(`{"findings":[]}`), Notes: ptr("note")},
			{Name: "b-skill-case", OwnerKind: "skill", OwnerID: f.skillImport},
		},
		Conventions: []domain.ConventionRecord{
			{Repo: "Org/y", Rule: "rule-no-snippet", Category: "style", Status: "rejected",
				Confidence: ptr(0.5), EvidencePath: ptr("c.go")},
			{Repo: "org/x", Rule: "rule-two-entries", Category: "naming", Status: "accepted",
				Confidence: ptr(0.9), EvidencePath: ptr("a.go"), EvidenceSnippet: ptr("first")},
			{Repo: "org/x", Rule: "rule-empty", Category: "other", Status: "pending"},
		},
		Learnings: []domain.LearningRecord{
			{Scope: "repo", Kind: "decision", Content: "repo learning", Confidence: ptr(0.7), Repo: ptr("org/x")},
			{Scope: "global", Kind: "fact", Content: "global learning"},
		},
		ContextFiles: []domain.ContextFileRecord{
			{Repo: "Org/y", Path: "z.md", Content: "Z"},
			{Repo: "org/x", Path: "Docs/a.md", Content: "A"},
			{Repo: "org/x", Path: "docs/b.md", Content: "B"},
		},
	}
	if diff := cmp.Diff(want, got, jsonEqual, timeEqual); diff != "" {
		t.Errorf("ExportData(%s) mismatch (-want +got):\n%s", f.ws, diff)
	}

	// The same call twice returns the same order.
	again, err := h.store.ExportData(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("ExportData(%s) second call: %v", f.ws, err)
	}
	if diff := cmp.Diff(got, again, jsonEqual, timeEqual); diff != "" {
		t.Errorf("ExportData(%s) is not stable (-first +second):\n%s", f.ws, diff)
	}
}

var uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestExportData_BundleLeaksNothing(t *testing.T) { // FR13, EC14, FR14 (store part)
	h := newReadsDB(t)
	f := seedExport(h)

	data, err := h.store.ExportData(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("ExportData(%s): %v", f.ws, err)
	}
	if data.Settings != nil {
		t.Errorf("ExportData(%s).Settings = %v, want nil (the service fills them)", f.ws, data.Settings)
	}
	bundle := domain.BuildBundle(data, domain.Meta{Name: "main", Now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)})
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	text := string(body)

	for _, forbidden := range []string{"source-text", "secret", "clone", "/data/", `"embedding"`, `"sources"`, `"id"`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("bundle contains %q", forbidden)
		}
	}
	if ids := uuidRe.FindAllString(text, -1); len(ids) != 0 {
		t.Errorf("bundle contains database ids: %v", ids)
	}

	// EC14: the imported skill keeps its source; EC13: duplicates are suffixed in creation order.
	var sources []string
	for _, s := range bundle.Skills {
		sources = append(sources, s.Name+":"+s.Source)
	}
	if diff := cmp.Diff([]string{"Zeta:extracted", "alpha:manual", "imported:imported_file"}, sources); diff != "" {
		t.Errorf("bundle skill sources mismatch (-want +got):\n%s", diff)
	}
	var agents []string
	for _, a := range bundle.Agents {
		agents = append(agents, a.Name)
	}
	if diff := cmp.Diff([]string{"Reviewer", "Reviewer (2)", "Auditor"}, agents); diff != "" {
		t.Errorf("bundle agent names mismatch (-want +got):\n%s", diff)
	}
	var owners []string
	for _, c := range bundle.EvalCases {
		owners = append(owners, c.OwnerKind+":"+c.OwnerRef)
	}
	if diff := cmp.Diff([]string{"agent:Reviewer", "skill:imported"}, owners); diff != "" {
		t.Errorf("bundle eval case owners mismatch (-want +got):\n%s", diff)
	}
}

func TestExportData_WorkspaceIsolation(t *testing.T) {
	h := newReadsDB(t)
	f := seedExport(h)

	// A second workspace with records of its own, a repo with the same full name, and a skill
	// linked to nothing of the first.
	other := h.workspace("other")
	otherAgent := h.agent(other, "Foreign", true, 0)
	otherSkill := h.skill(other, "foreign-skill", "manual")
	h.exec(`INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, otherAgent, otherSkill)
	h.exec(`INSERT INTO skill_versions (skill_id, version, body) VALUES (?, 1, 'foreign body')`, otherSkill)
	otherRepo := h.repo(other, "org/x")
	h.exec(`INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', ?, 'foreign-case')`,
		other, otherAgent)
	h.exec(`INSERT INTO conventions (workspace_id, repo_id, rule) VALUES (?, ?, 'foreign-rule')`, other, otherRepo)
	h.exec(`INSERT INTO memory (workspace_id, repo_id, scope, kind, content) VALUES (?, ?, 'repo', 'fact', 'foreign learning')`,
		other, otherRepo)
	h.exec(`INSERT INTO context_files (repo_id, path, content, size_bytes) VALUES (?, 'foreign.md', 'F', 1)`, otherRepo)

	mine, err := h.store.ExportData(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("ExportData(%s): %v", f.ws, err)
	}
	body, err := json.Marshal(mine)
	if err != nil {
		t.Fatalf("marshal export data: %v", err)
	}
	if strings.Contains(string(body), "oreign") {
		t.Errorf("ExportData(%s) holds records of another workspace: %s", f.ws, body)
	}
	if got := len(mine.Agents) + len(mine.Skills) + len(mine.EvalCases) + len(mine.Conventions) +
		len(mine.Learnings) + len(mine.ContextFiles); got != 3+3+2+3+2+3 {
		t.Errorf("ExportData(%s) holds %d records, want 16", f.ws, got)
	}

	theirs, err := h.store.ExportData(t.Context(), other)
	if err != nil {
		t.Fatalf("ExportData(%s): %v", other, err)
	}
	want := domain.ExportData{
		Agents: []domain.AgentRecord{{ID: otherAgent, Name: "Foreign", Provider: "openai", Model: "gpt-x",
			SystemPrompt: "prompt of Foreign", Enabled: true, Strategy: "single-pass", CIFailOn: "critical",
			RepoIntel: true, SkillNames: []string{"foreign-skill"}}},
		Skills: []domain.SkillRecord{{ID: otherSkill, Name: "foreign-skill", Description: "d", Type: "rubric",
			Source: "manual", Body: "body", Enabled: true,
			Versions: []domain.SkillVersionRecord{{Version: 1, Body: "foreign body"}}}},
		EvalCases: []domain.EvalCaseRecord{{Name: "foreign-case", OwnerKind: "agent", OwnerID: otherAgent}},
		Conventions: []domain.ConventionRecord{{Repo: "org/x", Rule: "foreign-rule", Category: "other",
			Status: "pending"}},
		Learnings:    []domain.LearningRecord{{Scope: "repo", Kind: "fact", Content: "foreign learning", Repo: ptr("org/x")}},
		ContextFiles: []domain.ContextFileRecord{{Repo: "org/x", Path: "foreign.md", Content: "F"}},
	}
	if diff := cmp.Diff(want, theirs, jsonEqual, cmpopts.IgnoreFields(domain.SkillVersionRecord{}, "CreatedAt")); diff != "" {
		t.Errorf("ExportData(%s) mismatch (-want +got):\n%s", other, diff)
	}
}
