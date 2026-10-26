package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

func TestFindPull(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `UPDATE pull_requests SET body = 'the body' WHERE id = ?`, f.pr)
	got, err := f.store.FindPull(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatalf("FindPull() error = %v", err)
	}
	want := domain.ReviewPull{
		ID: f.pr, WorkspaceID: f.ws, RepoID: f.repo, Number: 482, Title: "Add rate limiting", Author: "dev",
		Base: "main", Branch: "feat", HeadSHA: "a1b2c3d4e5f6", Body: strp("the body"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindPull() mismatch (-want +got):\n%s", diff)
	}
	if _, err := f.store.FindPull(t.Context(), f.other, f.pr); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindPull(other workspace) error = %v, want ErrNotFound", err)
	}
}

func TestFindRepo(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.FindRepo(t.Context(), f.repo)
	if want := (domain.ReviewRepo{ID: f.repo, Owner: "acme", Name: "payments-api", Provider: "github", Host: "github.com"}); err != nil || got != want {
		t.Errorf("FindRepo() = %+v, %v, want %+v", got, err, want)
	}
	gl := f.insertRepo(t, `INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, 'grp/sub', 'web', 'grp/sub/web', 'gitlab', 'git.corp.example', 4242, 'https://git.corp.example/grp/sub/web') RETURNING id`, f.ws)
	got, err = f.store.FindRepo(t.Context(), gl)
	if want := (domain.ReviewRepo{ID: gl, Owner: "grp/sub", Name: "web", Provider: "gitlab", Host: "git.corp.example", ProjectID: 4242}); err != nil || got != want {
		t.Errorf("FindRepo(gitlab) = %+v, %v, want %+v", got, err, want)
	}
	if _, err := f.store.FindRepo(t.Context(), missingID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindRepo(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestFindAgent(t *testing.T) {
	f := newFixture(t)
	off := f.agent(t, f.ws, "Disabled", false)
	f.exec(t, `UPDATE agents SET strategy = 'map-reduce', ci_fail_on = 'any', repo_intel = false, version = 4 WHERE id = ?`, off)
	got, err := f.store.FindAgent(t.Context(), f.ws, off)
	if err != nil {
		t.Fatalf("FindAgent(disabled) error = %v", err)
	}
	want := domain.ReviewAgent{
		ID: off, Name: "Disabled", Provider: "openai", Model: "gpt", SystemPrompt: "be careful",
		Strategy: reviewer.StrategyMapReduce, CIFailOn: reviewer.FailOnAny, RepoIntel: false, Version: 4,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindAgent() mismatch (-want +got):\n%s", diff)
	}

	for name, id := range map[string]string{"other workspace": off, "unknown": missingID, "not a uuid": "nope", "empty": "", "sql": "x' OR '1'='1"} {
		ws := f.ws
		if name == "other workspace" {
			ws = f.other
		}
		if _, err := f.store.FindAgent(t.Context(), ws, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("FindAgent(%s) error = %v, want ErrNotFound", name, err)
		}
	}
}

// TestFindAgent_NotAUUIDLeavesTheTransactionUsable: a non-UUID id must not reach the database,
// where it would fail the statement and abort the transaction.
func TestFindAgent_NotAUUIDLeavesTheTransactionUsable(t *testing.T) {
	f := newFixture(t)
	f.agent(t, f.ws, "Alpha", true)
	err := f.tx.InTx(t.Context(), func(ctx context.Context) error {
		if _, err := f.store.FindAgent(ctx, f.ws, "not-a-uuid"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("FindAgent(not a uuid) error = %v, want ErrNotFound", err)
		}
		agents, err := f.store.EnabledAgents(ctx, f.ws)
		if err != nil || len(agents) != 1 {
			t.Errorf("EnabledAgents() after the lookup = %d, %v, want 1, nil", len(agents), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnabledAgents_ByteOrderThenID(t *testing.T) {
	f := newFixture(t)
	if got, err := f.store.EnabledAgents(t.Context(), f.ws); err != nil || len(got) != 0 {
		t.Fatalf("EnabledAgents(none) = %v, %v, want empty, nil", got, err)
	}
	for _, name := range []string{"beta", "alpha", "Zed", "Alpha"} {
		f.agent(t, f.ws, name, true)
	}
	f.agent(t, f.ws, "off", false)
	f.agent(t, f.other, "elsewhere", true)
	dupA := f.agent(t, f.ws, "dup", true)
	dupB := f.agent(t, f.ws, "dup", true)

	got, err := f.store.EnabledAgents(t.Context(), f.ws)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	// Byte order puts every capital before every lower-case letter (the default collation would
	// not: Alpha, alpha, beta, dup, Zed).
	want := []string{"Alpha", "Zed", "alpha", "beta", "dup", "dup"}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("EnabledAgents() names mismatch (-want +got):\n%s", diff)
	}
	lo, hi := dupA, dupB
	if lo > hi {
		lo, hi = hi, lo
	}
	if got[4].ID != lo || got[5].ID != hi {
		t.Errorf("equal names are ordered %s, %s, want by id %s, %s", got[4].ID, got[5].ID, lo, hi)
	}
}

func TestEnabledSkills_LinkOrderSkippingDisabled(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	s1, s2, s3 := f.skill(t, f.ws, "one", true), f.skill(t, f.ws, "two", false), f.skill(t, f.ws, "three", true)
	f.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 2), (?, ?, 0), (?, ?, 1)`, agent, s1, agent, s2, agent, s3)

	got, err := f.store.EnabledSkills(t.Context(), agent)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ReviewSkill{
		{ID: s3, Name: "three", Description: "d", Body: "body", Version: 3},
		{ID: s1, Name: "one", Description: "d", Body: "body", Version: 3},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("EnabledSkills() mismatch (-want +got):\n%s", diff)
	}
	if got, err := f.store.EnabledSkills(t.Context(), missingID); err != nil || len(got) != 0 {
		t.Errorf("EnabledSkills(no links) = %v, %v, want empty, nil", got, err)
	}
}

func TestPullFiles_PatchedFilesInByteOrder(t *testing.T) {
	f := newFixture(t)
	if got, err := f.store.PullFiles(t.Context(), f.pr); err != nil || len(got) != 0 {
		t.Fatalf("PullFiles(none) = %v, %v, want empty, nil", got, err)
	}
	f.exec(t, `INSERT INTO pr_files (pr_id, path, patch) VALUES
		(?, 'b.go', '@@ b'), (?, 'B.go', '@@ B'), (?, 'a/z.go', '@@ az'), (?, 'binary.png', NULL), (?, 'empty.txt', '')`,
		f.pr, f.pr, f.pr, f.pr, f.pr)
	got, err := f.store.PullFiles(t.Context(), f.pr)
	if err != nil {
		t.Fatal(err)
	}
	want := []reviewer.PatchFile{{Path: "B.go", Patch: "@@ B"}, {Path: "a/z.go", Patch: "@@ az"}, {Path: "b.go", Patch: "@@ b"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PullFiles() mismatch (-want +got):\n%s", diff)
	}
}

func TestFindRun(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	f.exec(t, `UPDATE agent_runs SET status = 'failed', error = 'boom' WHERE id = ?`, run)
	got, err := f.store.FindRun(t.Context(), f.ws, run)
	if want := (domain.RunState{ID: run, Status: domain.StatusFailed, Error: strp("boom")}); err != nil || !cmp.Equal(got, want) {
		t.Errorf("FindRun() = %+v, %v, want %+v", got, err, want)
	}
	f.exec(t, `ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_status_chk`)
	f.exec(t, `UPDATE agent_runs SET status = NULL WHERE id = ?`, run)
	if got, err := f.store.FindRun(t.Context(), f.ws, run); err != nil || got.Status != "" {
		t.Errorf("FindRun(NULL status) = %+v, %v, want the empty status", got, err)
	}
	if _, err := f.store.FindRun(t.Context(), f.other, run); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindRun(other workspace) error = %v, want ErrNotFound", err)
	}
}

// TestRunsOfAPull is AC4010: history newest first, active oldest first.
func TestRunsOfAPull(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	r1, r2, r3 := f.run(t, agent, nil), f.run(t, agent, nil), f.run(t, agent, nil)
	t1, t2, t3 := "2026-10-22 10:00:00+00", "2026-10-22 11:00:00+00", "2026-10-22 12:00:00+00"
	f.exec(t, `UPDATE agent_runs SET ran_at = ?, status = 'done', duration_ms = 5, cost_usd = 0.5, score = 70, blockers = 0,
		findings_count = 1, grounding = '1/1 passed', tokens_in = 1, tokens_out = 2 WHERE id = ?`, t1, r1)
	f.exec(t, `UPDATE agent_runs SET ran_at = ? WHERE id = ?`, t2, r2)
	f.exec(t, `UPDATE agent_runs SET ran_at = ?, status = 'failed', error = 'x' WHERE id = ?`, t3, r3)
	other := f.pull(t, f.ws, f.repo, 7)
	if _, err := f.store.CreateRun(t.Context(), domain.NewRun{WorkspaceID: f.ws, AgentID: agent, PRID: other}); err != nil {
		t.Fatal(err)
	}

	runs, err := f.store.ListRuns(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.RunID)
	}
	if diff := cmp.Diff([]string{r3, r2, r1}, ids); diff != "" {
		t.Errorf("ListRuns() order mismatch (-want +got):\n%s", diff)
	}
	last := runs[2]
	wantLast := domain.RunSummary{
		RunID: r1, AgentID: &agent, AgentName: strp("Alpha"), Provider: strp("openai"), Model: strp("gpt"),
		Status: domain.StatusDone, DurationMS: intp(5), TokensIn: intp(1), TokensOut: intp(2), CostUSD: floatp(0.5),
		FindingsCount: intp(1), Grounding: strp("1/1 passed"), RanAt: timep(mustTime(t, "2026-10-22T10:00:00Z")),
		Score: intp(70), Blockers: intp(0),
	}
	if diff := cmp.Diff(wantLast, last); diff != "" {
		t.Errorf("ListRuns()[2] mismatch (-want +got):\n%s", diff)
	}
	if runs[1].Error != nil || runs[0].Error == nil || *runs[0].Error != "x" {
		t.Errorf("run errors = %v / %v", runs[0].Error, runs[1].Error)
	}

	active, err := f.store.ActiveRuns(t.Context(), f.ws, f.pr)
	wantActive := []domain.ActiveRun{{RunID: r2, AgentID: &agent, AgentName: strp("Alpha"), RanAt: timep(mustTime(t, "2026-10-22T11:00:00Z"))}}
	if diff := cmp.Diff(wantActive, active); err != nil || diff != "" {
		t.Errorf("ActiveRuns() = %v mismatch (-want +got):\n%s", err, diff)
	}

	for _, unknown := range []string{missingID} {
		if got, err := f.store.ListRuns(t.Context(), f.ws, unknown); err != nil || got == nil || len(got) != 0 {
			t.Errorf("ListRuns(unknown pr) = %#v, %v, want empty non-nil", got, err)
		}
		if got, err := f.store.ActiveRuns(t.Context(), f.ws, unknown); err != nil || got == nil || len(got) != 0 {
			t.Errorf("ActiveRuns(unknown pr) = %#v, %v, want empty non-nil", got, err)
		}
	}
	if got, _ := f.store.ListRuns(t.Context(), f.other, f.pr); len(got) != 0 {
		t.Errorf("ListRuns(other workspace) = %d runs, want 0", len(got))
	}
}

func TestRunsOfAPull_TiesAndDeletedAgent(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	a, b := f.run(t, agent, nil), f.run(t, agent, nil)
	f.exec(t, `UPDATE agent_runs SET ran_at = '2026-10-22 10:00:00+00'`)
	f.exec(t, `DELETE FROM agents WHERE id = ?`, agent)
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	active, err := f.store.ActiveRuns(t.Context(), f.ws, f.pr)
	if err != nil || len(active) != 2 || active[0].RunID != lo || active[1].RunID != hi {
		t.Fatalf("ActiveRuns() = %+v, %v, want ties by id ascending %s, %s", active, err, lo, hi)
	}
	if active[0].AgentID != nil || active[0].AgentName != nil {
		t.Errorf("deleted agent: id %v name %v, want nil, nil", active[0].AgentID, active[0].AgentName)
	}
	runs, err := f.store.ListRuns(t.Context(), f.ws, f.pr)
	if err != nil || len(runs) != 2 || runs[0].RunID != lo || runs[1].RunID != hi {
		t.Errorf("ListRuns() ties = %+v, %v, want by id ascending", runs, err)
	}
}

// TestReviewsForPull is AC4013: newest first, usage only with a run, findings in insertion order
// with their eval case.
func TestReviewsForPull(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	// The older review has no run (as a seeded one without its run link would).
	old := f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind, verdict, summary, score, model, created_at)
		VALUES (?, ?, 'review', 'comment', 'old', 50, 'seed', '2026-10-22 09:00:00+00') RETURNING id`, f.ws, f.pr)
	f.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
		VALUES (?, 'z.go', 9, 9, 'CRITICAL', 'bug', 'old-first', 'r', 0.5), (?, 'a.go', 1, 1, 'WARNING', 'perf', 'old-second', 'r', 0.6)`, old, old)
	run, fresh := f.doneReview(t, agent, nil, finding("m.go", 5, 6, "new-1"), finding("c.go", 1, 1, "new-2"), finding("k.go", 3, 3, "new-3"))
	f.exec(t, `UPDATE reviews SET created_at = '2026-10-22 12:00:00+00' WHERE id = ?`, fresh)
	var oldFirst string
	if err := f.db.Raw(`SELECT id FROM findings WHERE title = 'old-first'`).Scan(&oldFirst).Error; err != nil {
		t.Fatal(err)
	}
	evalCase := f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, source_finding_id)
		VALUES (?, 'agent', ?, 'c', ?) RETURNING id`, f.ws, agent, oldFirst)

	got, err := f.store.ReviewsForPull(t.Context(), f.pr)
	if err != nil {
		t.Fatalf("ReviewsForPull() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != fresh || got[1].ID != old {
		t.Fatalf("ReviewsForPull() = %d reviews, want newest (%s) first then %s", len(got), fresh, old)
	}
	n, o := got[0], got[1]
	if n.RunID == nil || *n.RunID != run || n.Usage == nil || n.Usage.CostUSD == nil || *n.Usage.CostUSD != 0.002135 ||
		n.Usage.TokensIn == nil || *n.Usage.TokensIn != 100 || *n.Usage.TokensOut != 20 {
		t.Errorf("new review run/usage = %v / %+v", n.RunID, n.Usage)
	}
	if n.AgentName == nil || *n.AgentName != "Alpha" || n.Kind != "review" || n.Verdict == nil || *n.Verdict != "comment" ||
		n.Score == nil || *n.Score != 70 || !n.CreatedAt.Equal(mustTime(t, "2026-10-22T12:00:00Z")) {
		t.Errorf("new review = %+v", n)
	}
	if o.RunID != nil || o.Usage != nil || o.AgentName != nil || o.AgentID != nil {
		t.Errorf("old review run/usage/agent = %v/%v/%v/%v, want nil", o.RunID, o.Usage, o.AgentName, o.AgentID)
	}
	var titles []string
	for _, fr := range n.Findings {
		titles = append(titles, fr.Title)
	}
	if diff := cmp.Diff([]string{"new-1", "new-2", "new-3"}, titles); diff != "" {
		t.Errorf("findings are not in insertion order (-want +got):\n%s", diff)
	}
	if o.Findings[0].Title != "old-first" || o.Findings[0].EvalCaseID == nil || *o.Findings[0].EvalCaseID != evalCase {
		t.Errorf("old first finding = %+v, want eval case %s", o.Findings[0], evalCase)
	}
	if o.Findings[1].EvalCaseID != nil || n.Findings[0].EvalCaseID != nil || n.Findings[0].MemoryID != nil {
		t.Errorf("eval_case_id/memory_id set where there is none")
	}
	if got, err := f.store.ReviewsForPull(t.Context(), missingID); err != nil || got == nil || len(got) != 0 {
		t.Errorf("ReviewsForPull(unknown) = %#v, %v, want empty non-nil", got, err)
	}
}

func TestReviewsForPull_DeletedAgentKeepsReview(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	f.doneReview(t, agent, nil, finding("a.go", 1, 1, "x"))
	f.exec(t, `DELETE FROM agents WHERE id = ?`, agent)
	got, err := f.store.ReviewsForPull(t.Context(), f.pr)
	if err != nil || len(got) != 1 || got[0].AgentName != nil || got[0].AgentID != nil || got[0].Usage == nil {
		t.Errorf("ReviewsForPull() = %+v, %v, want the review with a nil agent and its usage", got, err)
	}
}

func timep(v time.Time) *time.Time { return &v }

// plugin inserts an installed plugin of the workspace.
func (f fixture) plugin(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return f.insert(t, `INSERT INTO installed_plugins (workspace_id, name, enabled) VALUES (?, ?, ?) RETURNING id`, ws, name, enabled)
}

func TestFindAgent_CarriesThePluginRef(t *testing.T) {
	f := newFixture(t)
	on, off := f.plugin(t, f.ws, "On Pack", true), f.plugin(t, f.ws, "Off Pack", false)
	mine := f.agent(t, f.ws, "Mine", true)
	fromOn, fromOff := f.agent(t, f.ws, "FromOn", true), f.agent(t, f.ws, "FromOff", false)
	f.exec(t, `UPDATE agents SET plugin_id = ? WHERE id = ?`, on, fromOn)
	f.exec(t, `UPDATE agents SET plugin_id = ? WHERE id = ?`, off, fromOff)

	tests := []struct {
		name string
		id   string
		want *domain.PluginRef
	}{
		{"user agent", mine, nil},
		{"enabled plugin", fromOn, &domain.PluginRef{ID: on, Name: "On Pack", Enabled: true}},
		{"disabled plugin (named read still returns it)", fromOff, &domain.PluginRef{ID: off, Name: "Off Pack", Enabled: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.store.FindAgent(t.Context(), f.ws, tc.id)
			if err != nil {
				t.Fatalf("FindAgent(%s) error = %v", tc.name, err)
			}
			if diff := cmp.Diff(tc.want, got.Plugin); diff != "" {
				t.Errorf("FindAgent(%s).Plugin mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestEnabledAgents_SkipsAgentsOfADisabledPlugin(t *testing.T) {
	f := newFixture(t)
	on, off := f.plugin(t, f.ws, "On Pack", true), f.plugin(t, f.ws, "Off Pack", false)
	f.agent(t, f.ws, "mine", true)
	fromOn, fromOff, ownOff := f.agent(t, f.ws, "from-on", true), f.agent(t, f.ws, "from-off", true), f.agent(t, f.ws, "own-off", false)
	f.exec(t, `UPDATE agents SET plugin_id = ? WHERE id = ?`, on, fromOn)
	f.exec(t, `UPDATE agents SET plugin_id = ? WHERE id IN (?, ?)`, off, fromOff, ownOff)

	got, err := f.store.EnabledAgents(t.Context(), f.ws)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if diff := cmp.Diff([]string{"from-on", "mine"}, names); diff != "" {
		t.Errorf("EnabledAgents() names mismatch (-want +got):\n%s", diff)
	}
}

func TestEnabledSkills_SkipsSkillsOfADisabledPlugin(t *testing.T) {
	f := newFixture(t)
	on, off := f.plugin(t, f.ws, "On Pack", true), f.plugin(t, f.ws, "Off Pack", false)
	agent := f.agent(t, f.ws, "Alpha", true)
	mine, fromOn, fromOff := f.skill(t, f.ws, "mine", true), f.skill(t, f.ws, "from-on", true), f.skill(t, f.ws, "from-off", true)
	f.exec(t, `UPDATE skills SET plugin_id = ? WHERE id = ?`, on, fromOn)
	f.exec(t, `UPDATE skills SET plugin_id = ? WHERE id = ?`, off, fromOff)
	f.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 0), (?, ?, 1), (?, ?, 2)`,
		agent, mine, agent, fromOff, agent, fromOn)

	got, err := f.store.EnabledSkills(t.Context(), agent)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if diff := cmp.Diff([]string{mine, fromOn}, ids); diff != "" {
		t.Errorf("EnabledSkills() ids mismatch (-want +got):\n%s", diff)
	}
}
