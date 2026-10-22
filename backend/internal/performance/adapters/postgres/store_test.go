package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/performance/adapters/postgres"
	"dev-digest-go/backend/internal/performance/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

var (
	t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(7 * 24 * time.Hour)
)

// fixture is a migrated database with two workspaces, two agents and a pull request.
type fixture struct {
	db     *gorm.DB
	store  *postgres.Store
	ws     string
	other  string
	repo   string
	pr     string
	agentA string
	agentB string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'api', 'acme/api') RETURNING id`, f.ws)
	f.pr = f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 482, 't', 'dev', 'feat', 'main', 'abc') RETURNING id`, f.ws, f.repo)
	f.agentA = f.agent(t, f.ws, "alpha", true)
	f.agentB = f.agent(t, f.ws, "beta", false)
	return f
}

func (f fixture) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (f fixture) agent(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return f.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled)
		VALUES (?, ?, 'openai', 'gpt', 'p', ?) RETURNING id`, ws, name, enabled)
}

// runSpec describes an agent_runs row; zero values are the common case.
type runSpec struct {
	ws, agent, pr string // "" = NULL for agent and pr; ws defaults to the fixture's
	at            time.Time
	status        string // default done; "NULL" = SQL NULL
	source        string // default local
	model         *string
	duration      *int
	tokensIn      *int
	tokensOut     *int
	cost          *string // numeric text, so that no float64 is involved on the way in
	findings      *int
}

func (f fixture) run(t *testing.T, s runSpec) string {
	t.Helper()
	if s.ws == "" {
		s.ws = f.ws
	}
	var status *string
	switch s.status {
	case "":
		status = ptr("done")
	case "NULL":
	default:
		status = &s.status
	}
	if s.source == "" {
		s.source = "local"
	}
	nullable := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	return f.insert(t, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, ran_at, model, duration_ms, tokens_in, tokens_out,
			status, source, findings_count, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::numeric) RETURNING id`,
		s.ws, nullable(s.agent), nullable(s.pr), s.at, s.model, s.duration, s.tokensIn, s.tokensOut, status, s.source, s.findings, s.cost)
}

// finding adds a review of the run (kind review) with the findings given as
// severity/category/state ("pending", "accepted", "dismissed", "both").
func (f fixture) findings(t *testing.T, ws, runID string, specs ...[3]string) {
	t.Helper()
	review := f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?) RETURNING id`, ws, f.pr, runID)
	for _, s := range specs {
		var accepted, dismissed *time.Time
		switch s[2] {
		case "accepted":
			accepted = &t0
		case "dismissed":
			dismissed = &t0
		case "both":
			accepted, dismissed = &t0, &t0
		}
		f.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence,
				accepted_at, dismissed_at)
			VALUES (?, 'a.go', 1, 2, ?, ?, 't', 'r', 0.5, ?, ?)`, review, s[0], s[1], accepted, dismissed)
	}
}

func (f fixture) skill(t *testing.T, name string) string {
	t.Helper()
	return f.insert(t, `INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES (?, ?, 'd', 'custom', 'manual', 'b') RETURNING id`, f.ws, name)
}

func (f fixture) useSkill(t *testing.T, runID, skillID string, order int) {
	t.Helper()
	f.exec(t, `INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, ?)`, runID, skillID, order)
}

func (f fixture) trace(t *testing.T, runID, doc string) {
	t.Helper()
	f.exec(t, `INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb)`, runID, doc)
}

func custom() domain.PeriodWindow {
	return domain.PeriodWindow{Key: domain.PeriodCustom, From: t0, To: t1}
}

func preset() domain.PeriodWindow {
	return domain.PeriodWindow{Key: domain.Period30d, From: t0, To: t1, ToInclusive: true}
}

// countedFixture holds runs on every side of the filter; the returned map names the ids.
func countedFixture(t *testing.T) (fixture, map[string]string) {
	t.Helper()
	f := newFixture(t)
	id := map[string]string{
		"from":      f.run(t, runSpec{agent: f.agentA, at: t0, model: ptr("gpt-4o"), duration: ptr(1200), cost: ptr("0.123457")}),
		"mid":       f.run(t, runSpec{agent: f.agentA, at: t0.Add(time.Hour), status: "failed", source: "ci"}),
		"to":        f.run(t, runSpec{agent: f.agentA, at: t1, status: "cancelled", cost: ptr("0.1")}),
		"before":    f.run(t, runSpec{agent: f.agentA, at: t0.Add(-time.Millisecond)}),
		"running":   f.run(t, runSpec{agent: f.agentA, at: t0.Add(time.Hour), status: "running"}),
		"nullstate": f.run(t, runSpec{agent: f.agentA, at: t0.Add(time.Hour), status: "NULL"}),
		"otherws":   f.run(t, runSpec{ws: f.other, at: t0.Add(time.Hour)}),
		"agentB":    f.run(t, runSpec{agent: f.agentB, at: t0.Add(2 * time.Hour), cost: ptr("2")}),
		"noagent":   f.run(t, runSpec{at: t0.Add(3 * time.Hour), model: ptr("gpt-4o-mini")}),
	}
	return f, id
}

func TestCountedRuns_FiltersByWorkspaceStatusSourceAndWindow(t *testing.T) {
	f, _ := countedFixture(t)

	tests := []struct {
		name  string
		win   domain.PeriodWindow
		agent string
		want  []string // by name, order-independent
	}{
		{"custom: from in, to out", custom(), "", []string{"from", "mid", "agentB", "noagent"}},
		{"preset: to in", preset(), "", []string{"from", "mid", "to", "agentB", "noagent"}},
		{"one agent", preset(), f.agentA, []string{"from", "mid", "to"}},
		{"disabled agent", custom(), f.agentB, []string{"agentB"}},
		{"nothing before from", domain.PeriodWindow{From: t0.Add(-time.Hour), To: t0, ToInclusive: false}, "", []string{"before"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs, err := f.store.CountedRuns(t.Context(), f.ws, tc.win, tc.agent)
			if err != nil {
				t.Fatalf("CountedRuns() error = %v", err)
			}
			var got []string
			for _, r := range runs {
				got = append(got, nameOf(t, r))
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("CountedRuns() runs mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("columns", func(t *testing.T) {
		runs, err := f.store.CountedRuns(t.Context(), f.ws, custom(), f.agentA)
		if err != nil {
			t.Fatal(err)
		}
		var from domain.RunFact
		for _, r := range runs {
			if r.RanAt.Equal(t0) {
				from = r
			}
		}
		want := domain.RunFact{
			AgentID: &f.agentA, Status: domain.RunDone, Source: domain.SourceLocal, Model: ptr("gpt-4o"),
			DurationMs: ptr(1200), CostUSD: ptr(0.123457), RanAt: t0,
		}
		if diff := cmp.Diff(want, from, cmpopts.EquateApproxTime(0)); diff != "" {
			t.Errorf("CountedRuns() run mismatch (-want +got):\n%s", diff)
		}
	})
}

// nameOf finds the fixture name of a run by its distinguishing facts.
func nameOf(t *testing.T, r domain.RunFact) string {
	t.Helper()
	switch {
	case r.RanAt.Equal(t0):
		return "from"
	case r.RanAt.Equal(t1):
		return "to"
	case r.RanAt.Equal(t0.Add(-time.Millisecond)):
		return "before"
	case r.RanAt.Equal(t0.Add(time.Hour)):
		return "mid"
	case r.RanAt.Equal(t0.Add(2 * time.Hour)):
		return "agentB"
	case r.RanAt.Equal(t0.Add(3 * time.Hour)):
		return "noagent"
	}
	t.Fatalf("unexpected run %+v", r)
	return ""
}

func TestCountedRuns_DeletedAgentBecomesNoAgent(t *testing.T) {
	f, _ := countedFixture(t)
	f.exec(t, `DELETE FROM agents WHERE id = ?`, f.agentB)
	runs, err := f.store.CountedRuns(t.Context(), f.ws, custom(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.RanAt.Equal(t0.Add(2*time.Hour)) && r.AgentID != nil {
			t.Errorf("run of the deleted agent has AgentID %q, want nil", *r.AgentID)
		}
	}
	if len(runs) != 4 {
		t.Errorf("runs = %d, want 4 (deleting an agent keeps its runs)", len(runs))
	}
}

func TestFindingCounts_AcceptedWinsAndOnlyCountedRunsCount(t *testing.T) {
	f, ids := countedFixture(t)
	f.findings(t, f.ws, ids["from"], [3]string{"CRITICAL", "bug", "accepted"}, [3]string{"WARNING", "bug", "dismissed"},
		[3]string{"WARNING", "style", "both"}, [3]string{"SUGGESTION", "perf", "pending"})
	f.findings(t, f.ws, ids["mid"], [3]string{"WARNING", "bug", "dismissed"})
	f.findings(t, f.ws, ids["to"], [3]string{"WARNING", "bug", "accepted"})         // out of a custom window
	f.findings(t, f.ws, ids["running"], [3]string{"WARNING", "bug", "accepted"})    // not a counted status
	f.findings(t, f.ws, ids["before"], [3]string{"WARNING", "bug", "accepted"})     // before the window
	f.findings(t, f.ws, ids["agentB"], [3]string{"WARNING", "bug", "pending"})      // another agent
	f.findings(t, f.other, ids["otherws"], [3]string{"WARNING", "bug", "accepted"}) // another workspace

	t.Run("one agent", func(t *testing.T) {
		got, err := f.store.FindingCounts(t.Context(), f.ws, custom(), f.agentA)
		if err != nil {
			t.Fatal(err)
		}
		want := []domain.FindingCounts{{AgentID: &f.agentA, Total: 5, Accepted: 2, Dismissed: 2}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("FindingCounts() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("all agents, per agent", func(t *testing.T) {
		got, err := f.store.FindingCounts(t.Context(), f.ws, custom(), "")
		if err != nil {
			t.Fatal(err)
		}
		byAgent := map[string]domain.FindingCounts{}
		for _, c := range got {
			byAgent[*c.AgentID] = c
		}
		if len(got) != 2 || byAgent[f.agentA].Total != 5 || byAgent[f.agentB].Total != 1 {
			t.Errorf("FindingCounts() = %+v, want agent A 5 and agent B 1 findings", got)
		}
	})
	t.Run("preset window adds the run at to", func(t *testing.T) {
		got, err := f.store.FindingCounts(t.Context(), f.ws, preset(), f.agentA)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Total != 6 || got[0].Accepted != 3 {
			t.Errorf("FindingCounts() = %+v, want 6 findings of which 3 accepted", got)
		}
	})
}

func TestFindingFacts_GroupedPerRunSeverityAndCategory(t *testing.T) {
	f, ids := countedFixture(t)
	f.findings(t, f.ws, ids["from"], [3]string{"CRITICAL", "bug", "pending"}, [3]string{"CRITICAL", "bug", "accepted"}, [3]string{"WARNING", "style", "pending"})
	f.findings(t, f.ws, ids["mid"], [3]string{"CRITICAL", "bug", "pending"})
	f.findings(t, f.ws, ids["agentB"], [3]string{"CRITICAL", "bug", "pending"})

	got, err := f.store.FindingFacts(t.Context(), f.ws, custom(), f.agentA)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool {
		if !got[i].RanAt.Equal(got[j].RanAt) {
			return got[i].RanAt.Before(got[j].RanAt)
		}
		return got[i].Severity < got[j].Severity
	})
	want := []domain.FindingFact{
		{RanAt: t0, Severity: "CRITICAL", Category: "bug", Count: 2},
		{RanAt: t0, Severity: "WARNING", Category: "style", Count: 1},
		{RanAt: t0.Add(time.Hour), Severity: "CRITICAL", Category: "bug", Count: 1},
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateApproxTime(0)); diff != "" {
		t.Errorf("FindingFacts() mismatch (-want +got):\n%s", diff)
	}
}

func TestSkillUsage_CountsDistinctCountedRuns(t *testing.T) {
	f, ids := countedFixture(t)
	s1, s2 := f.skill(t, "security"), f.skill(t, "style")
	f.useSkill(t, ids["from"], s1, 0)
	f.useSkill(t, ids["mid"], s1, 0)
	f.useSkill(t, ids["from"], s2, 1)
	f.useSkill(t, ids["running"], s2, 0) // not counted
	f.useSkill(t, ids["to"], s1, 0)      // outside a custom window
	f.useSkill(t, ids["agentB"], s2, 0)  // another agent

	got, err := f.store.SkillUsage(t.Context(), f.ws, custom(), f.agentA)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	want := []domain.SkillFact{{SkillID: s1, Name: "security", Runs: 2}, {SkillID: s2, Name: "style", Runs: 1}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SkillUsage() mismatch (-want +got):\n%s", diff)
	}
}

func TestMemoryPulled_ReturnsTheArrayAndNullWhenAbsent(t *testing.T) {
	f, ids := countedFixture(t)
	f.trace(t, ids["from"], `{"memory_pulled":[{"text":"use const"},{"text":7}],"other":1}`)
	f.trace(t, ids["mid"], `{"steps":[]}`)
	f.trace(t, ids["to"], `{"memory_pulled":[{"text":"outside"}]}`)
	f.trace(t, ids["running"], `{"memory_pulled":[{"text":"not counted"}]}`)
	f.trace(t, ids["agentB"], `{"memory_pulled":[{"text":"other agent"}]}`)
	// ids["noagent"] has no trace: no row.

	got, err := f.store.MemoryPulled(t.Context(), f.ws, custom(), f.agentA)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, raw := range got {
		if len(raw) == 0 {
			texts = append(texts, "<absent>")
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			t.Fatalf("invalid JSON %q: %v", raw, err)
		}
		texts = append(texts, compact.String())
	}
	sort.Strings(texts)
	want := []string{"<absent>", `[{"text":"use const"},{"text":7}]`}
	if diff := cmp.Diff(want, texts); diff != "" {
		t.Errorf("MemoryPulled() mismatch (-want +got):\n%s", diff)
	}
	// The domain turns them into the top list.
	if top := domain.TopMemories(got); len(top) != 1 || top[0].Label != "use const" {
		t.Errorf("TopMemories(MemoryPulled()) = %+v, want use const", top)
	}
}

func TestRunHistory_NewestFirstWithPullRequestTokensAndTrace(t *testing.T) {
	f := newFixture(t)
	two := f.run(t, runSpec{agent: f.agentA, at: t0.Add(2 * time.Hour), pr: f.pr, tokensIn: ptr(100), tokensOut: ptr(20), cost: ptr("0.5"), findings: ptr(3)})
	f.trace(t, two, `{}`)
	oneOnlyOut := f.run(t, runSpec{agent: f.agentA, at: t0.Add(time.Hour), status: "failed", source: "ci", tokensOut: ptr(7)})
	oneNoUsage := f.run(t, runSpec{agent: f.agentA, at: t0.Add(time.Hour), source: "ci"})
	deletedPR := f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 483, 't', 'dev', 'feat', 'main', 'abc') RETURNING id`, f.ws, f.repo)
	gone := f.run(t, runSpec{agent: f.agentA, at: t0, pr: deletedPR, tokensIn: ptr(5)})
	f.exec(t, `DELETE FROM pull_requests WHERE id = ?`, deletedPR)
	f.run(t, runSpec{agent: f.agentA, at: t1})                    // custom window: not counted
	f.run(t, runSpec{agent: f.agentA, at: t0, status: "running"}) // not counted
	f.run(t, runSpec{agent: f.agentB, at: t0.Add(time.Minute)})   // another agent

	got, err := f.store.RunHistory(t.Context(), f.ws, custom(), f.agentA, domain.RunHistoryLimit)
	if err != nil {
		t.Fatal(err)
	}
	// Same ran_at: id descending.
	tied := []string{oneOnlyOut, oneNoUsage}
	sort.Sort(sort.Reverse(sort.StringSlice(tied)))
	var order []string
	for _, h := range got {
		order = append(order, h.RunID)
	}
	if diff := cmp.Diff([]string{two, tied[0], tied[1], gone}, order); diff != "" {
		t.Fatalf("RunHistory() order mismatch (-want +got):\n%s", diff)
	}
	byID := map[string]domain.RunHistoryItem{}
	for _, h := range got {
		byID[h.RunID] = h
	}
	checks := map[string]domain.RunHistoryItem{
		two: {RunID: two, RanAt: t0.Add(2 * time.Hour), Status: domain.RunDone, Source: domain.SourceLocal,
			PR: &domain.RunPR{ID: f.pr, RepoID: f.repo, Number: 482}, Tokens: ptr(120), CostUSD: ptr(0.5), FindingsCount: ptr(3), HasTrace: true},
		oneOnlyOut: {RunID: oneOnlyOut, RanAt: t0.Add(time.Hour), Status: domain.RunFailed, Source: domain.SourceCI, Tokens: ptr(7)},
		oneNoUsage: {RunID: oneNoUsage, RanAt: t0.Add(time.Hour), Status: domain.RunDone, Source: domain.SourceCI},
		gone:       {RunID: gone, RanAt: t0, Status: domain.RunDone, Source: domain.SourceLocal, Tokens: ptr(5)},
	}
	for id, want := range checks {
		if diff := cmp.Diff(want, byID[id], cmpopts.EquateApproxTime(0)); diff != "" {
			t.Errorf("RunHistory() item %s mismatch (-want +got):\n%s", id, diff)
		}
	}

	t.Run("limit", func(t *testing.T) {
		limited, err := f.store.RunHistory(t.Context(), f.ws, custom(), f.agentA, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(limited) != 2 || limited[0].RunID != two {
			t.Errorf("RunHistory(limit 2) = %d items starting with %v, want the 2 newest", len(limited), limited)
		}
	})
}

func TestAgents_ListIncludesDisabledAndFindIsWorkspaceScoped(t *testing.T) {
	f := newFixture(t)
	f.agent(t, f.other, "foreign", true)

	agents, err := f.store.ListAgents(t.Context(), f.ws)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	want := []domain.AgentRef{{ID: f.agentA, Name: "alpha"}, {ID: f.agentB, Name: "beta"}}
	if diff := cmp.Diff(want, agents); diff != "" {
		t.Errorf("ListAgents() mismatch (-want +got):\n%s", diff)
	}

	got, err := f.store.FindAgent(t.Context(), f.ws, f.agentB)
	if err != nil || got != want[1] {
		t.Errorf("FindAgent(disabled) = %+v, %v; want %+v", got, err, want[1])
	}
	foreign := f.insert(t, `SELECT id FROM agents WHERE name = 'foreign'`)
	if _, err := f.store.FindAgent(t.Context(), f.ws, foreign); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindAgent(other workspace) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.FindAgent(t.Context(), f.ws, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindAgent(unknown) error = %v, want ErrNotFound", err)
	}
}
