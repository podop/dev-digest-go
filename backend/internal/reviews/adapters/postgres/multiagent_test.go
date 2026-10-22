package postgres_test

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviews/domain"
)

func TestFindMultiAgentRun(t *testing.T) {
	f := newFixture(t)
	multi, err := f.store.CreateMultiAgentRun(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.FindMultiAgentRun(t.Context(), f.ws, multi)
	if err != nil {
		t.Fatalf("FindMultiAgentRun() error = %v", err)
	}
	if got.ID != multi || got.PRID != f.pr || got.RepoID != f.repo || got.PRNumber == nil || *got.PRNumber != 482 ||
		got.PRTitle == nil || *got.PRTitle != "Add rate limiting" || got.RanAt.IsZero() {
		t.Errorf("FindMultiAgentRun() = %+v", got)
	}
	if _, err := f.store.FindMultiAgentRun(t.Context(), f.other, multi); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindMultiAgentRun(other workspace) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.FindMultiAgentRun(t.Context(), f.ws, missingID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindMultiAgentRun(unknown) error = %v, want ErrNotFound", err)
	}
}

// TestMultiAgentColumns follows AC4062: beta-x done, Alpha done, gamma failed with its agent
// deleted; a CI run with no parent never appears.
func TestMultiAgentColumns(t *testing.T) {
	f := newFixture(t)
	multi, err := f.store.CreateMultiAgentRun(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatal(err)
	}
	alpha, beta, gamma := f.agent(t, f.ws, "Alpha", true), f.agent(t, f.ws, "beta-x", true), f.agent(t, f.ws, "gamma", true)
	alphaRun, alphaReview := f.doneReview(t, alpha, &multi,
		finding("b.go", 9, 9, "late"), finding("a.go", 20, 21, "second"), finding("a.go", 3, 4, "first"))
	betaRun, _ := f.doneReview(t, beta, &multi, finding("a.go", 3, 4, "same place"))
	gammaRun := f.run(t, gamma, &multi)
	f.exec(t, `UPDATE agent_runs SET status = 'failed', error = 'LLM down', duration_ms = 77, cost_usd = 0.25, score = 12 WHERE id = ?`, gammaRun)
	f.exec(t, `DELETE FROM agents WHERE id = ?`, gamma)
	f.exec(t, `UPDATE agent_runs SET duration_ms = 2000, cost_usd = 0.5, provider = 'anthropic', model = 'claude' WHERE id = ?`, alphaRun)
	f.run(t, alpha, nil) // a run with no parent: it never appears

	// A Learn entry for one of Alpha's findings and an eval case for another.
	var firstID, secondID string
	if err := f.db.Raw(`SELECT id FROM findings WHERE title = 'first'`).Scan(&firstID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw(`SELECT id FROM findings WHERE title = 'second'`).Scan(&secondID).Error; err != nil {
		t.Fatal(err)
	}
	memory, err := f.store.InsertLearnedMemory(t.Context(), domain.NewMemory{WorkspaceID: f.ws, RepoID: f.repo, FindingID: firstID, PRNumber: 482, Content: "c", Confidence: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	evalCase := f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, source_finding_id)
		VALUES (?, 'agent', ?, 'c', ?) RETURNING id`, f.ws, alpha, secondID)

	cols, err := f.store.MultiAgentColumns(t.Context(), f.ws, multi)
	if err != nil {
		t.Fatalf("MultiAgentColumns() error = %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("MultiAgentColumns() = %d columns, want 3 (the CI run has no parent)", len(cols))
	}
	byRun := map[string]domain.AgentColumn{}
	for _, c := range cols {
		byRun[c.RunID] = c
	}

	a := byRun[alphaRun]
	if a.AgentID != alpha || a.AgentName != "Alpha" || a.Status != domain.StatusDone || a.Verdict == nil || *a.Verdict != "comment" ||
		a.Score == nil || *a.Score != 70 || a.Summary == nil || *a.Summary != "summary" ||
		a.DurationMS == nil || *a.DurationMS != 2000 || a.CostUSD == nil || *a.CostUSD != 0.5 ||
		a.Provider == nil || *a.Provider != "anthropic" {
		t.Errorf("Alpha column = %+v", a)
	}
	var order []string
	for _, fr := range a.Findings {
		order = append(order, fr.Title)
	}
	// File, then start line: the column order is not insertion order.
	if diff := cmp.Diff([]string{"first", "second", "late"}, order); diff != "" {
		t.Errorf("Alpha findings order mismatch (-want +got):\n%s", diff)
	}
	if a.Findings[0].MemoryID == nil || *a.Findings[0].MemoryID != memory || a.Findings[1].MemoryID != nil {
		t.Errorf("memory ids = %v / %v, want %s / nil", a.Findings[0].MemoryID, a.Findings[1].MemoryID, memory)
	}
	if a.Findings[1].EvalCaseID == nil || *a.Findings[1].EvalCaseID != evalCase || a.Findings[0].EvalCaseID != nil {
		t.Errorf("eval case ids = %v / %v", a.Findings[0].EvalCaseID, a.Findings[1].EvalCaseID)
	}
	if a.Findings[0].ReviewID != alphaReview {
		t.Errorf("finding review id = %s, want %s", a.Findings[0].ReviewID, alphaReview)
	}

	g := byRun[gammaRun]
	if g.AgentID != "" || g.AgentName != "" || g.Status != domain.StatusFailed || g.Error == nil || *g.Error != "LLM down" ||
		g.Verdict != nil || g.Summary != nil || g.Score == nil || *g.Score != 12 || len(g.Findings) != 0 || g.Findings == nil {
		t.Errorf("gamma column = %+v, want no agent, failed, run score 12, empty non-nil findings", g)
	}
	if b := byRun[betaRun]; b.AgentName != "beta-x" || len(b.Findings) != 1 {
		t.Errorf("beta column = %+v", b)
	}

	if got, err := f.store.MultiAgentColumns(t.Context(), f.other, multi); err != nil || len(got) != 0 {
		t.Errorf("MultiAgentColumns(other workspace) = %d, %v, want 0, nil", len(got), err)
	}
}

func TestMultiAgentColumns_NullStatusIsFailedAndNewestReviewWins(t *testing.T) {
	f := newFixture(t)
	multi, _ := f.store.CreateMultiAgentRun(t.Context(), f.ws, f.pr)
	agent := f.agent(t, f.ws, "Alpha", true)
	run, oldReview := f.doneReview(t, agent, &multi, finding("a.go", 1, 1, "old"))
	f.exec(t, `UPDATE reviews SET created_at = '2026-10-22 08:00:00+00' WHERE id = ?`, oldReview)
	newReview := f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, agent_id, run_id, kind, verdict, summary, score, model)
		VALUES (?, ?, ?, ?, 'review', 'approve', 'newer', NULL, 'm') RETURNING id`, f.ws, f.pr, agent, run)
	f.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
		VALUES (?, 'n.go', 1, 1, 'WARNING', 'bug', 'newer-finding', 'r', 0.5)`, newReview)
	f.exec(t, `ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_status_chk`)
	f.exec(t, `UPDATE agent_runs SET status = NULL, score = 33 WHERE id = ?`, run)

	cols, err := f.store.MultiAgentColumns(t.Context(), f.ws, multi)
	if err != nil || len(cols) != 1 {
		t.Fatalf("MultiAgentColumns() = %d, %v", len(cols), err)
	}
	c := cols[0]
	if c.Status != domain.StatusFailed {
		t.Errorf("status = %q, want failed for a NULL status", c.Status)
	}
	if c.Verdict == nil || *c.Verdict != "approve" || c.Summary == nil || *c.Summary != "newer" ||
		len(c.Findings) != 1 || c.Findings[0].Title != "newer-finding" {
		t.Errorf("column = %+v, want the newest review", c)
	}
	if c.Score == nil || *c.Score != 33 {
		t.Errorf("score = %v, want the run's 33 when the review has none", c.Score)
	}
}

func TestRepoInWorkspace(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		ws, repo string
		want     bool
	}{{f.ws, f.repo, true}, {f.other, f.repo, false}, {f.ws, missingID, false}} {
		if got, err := f.store.RepoInWorkspace(t.Context(), tc.ws, tc.repo); err != nil || got != tc.want {
			t.Errorf("RepoInWorkspace(%s, %s) = %v, %v, want %v", tc.ws, tc.repo, got, err, tc.want)
		}
	}
}

// TestListMultiAgentRuns is AC4064 at the store: the newest first, capped by limit, with the
// aggregates of the linked runs.
func TestListMultiAgentRuns(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	var ids []string
	for i := 0; i < 25; i++ {
		id, err := f.store.CreateMultiAgentRun(t.Context(), f.ws, f.pr)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(t, `UPDATE multi_agent_runs SET ran_at = ? WHERE id = ?`, time.Date(2026, 10, 22, 0, i, 0, 0, time.UTC), id)
		ids = append(ids, id)
	}
	newest := ids[24]
	r1, r2 := f.run(t, agent, &newest), f.run(t, agent, &newest)
	f.exec(t, `UPDATE agent_runs SET cost_usd = 0.25 WHERE id IN (?, ?)`, r1, r2)
	f.exec(t, `UPDATE agent_runs SET status = 'done' WHERE id = ?`, r1)
	f.run(t, agent, &ids[23]) // running, no cost
	// Other repo, other workspace.
	repo2 := f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r2', 'o/r2') RETURNING id`, f.ws)
	pr2 := f.pull(t, f.ws, repo2, 1)
	if _, err := f.store.CreateMultiAgentRun(t.Context(), f.ws, pr2); err != nil {
		t.Fatal(err)
	}

	got, err := f.store.ListMultiAgentRuns(t.Context(), f.ws, f.repo, 20)
	if err != nil || len(got) != 20 {
		t.Fatalf("ListMultiAgentRuns(20) = %d, %v", len(got), err)
	}
	if got[0].ID != newest || got[1].ID != ids[23] || got[19].ID != ids[5] {
		t.Errorf("order = %s, %s ... %s", got[0].ID, got[1].ID, got[19].ID)
	}
	first := got[0]
	if first.AgentCount != 2 || first.RunningCount != 1 || first.TotalCostUSD == nil || *first.TotalCostUSD != 0.5 ||
		first.PRNumber == nil || *first.PRNumber != 482 || first.PRTitle == nil || first.PRID != f.pr {
		t.Errorf("newest summary = %+v", first)
	}
	if second := got[1]; second.AgentCount != 1 || second.RunningCount != 1 || second.TotalCostUSD != nil {
		t.Errorf("second summary = %+v, want a nil total for runs without cost", second)
	}
	if third := got[2]; third.AgentCount != 0 || third.RunningCount != 0 || third.TotalCostUSD != nil {
		t.Errorf("a multi-agent run without runs = %+v", third)
	}
	if five, err := f.store.ListMultiAgentRuns(t.Context(), f.ws, f.repo, 5); err != nil || len(five) != 5 || five[4].ID != ids[20] {
		t.Errorf("ListMultiAgentRuns(5) = %d, %v", len(five), err)
	}
	if none, err := f.store.ListMultiAgentRuns(t.Context(), f.other, f.repo, 20); err != nil || len(none) != 0 {
		t.Errorf("ListMultiAgentRuns(other workspace) = %d, %v, want 0", len(none), err)
	}
}

// TestAgentEstimates is AC4065: 12 done runs with durations 1..12 s (the first without cost), a
// failed run, a run of a deleted agent.
func TestAgentEstimates(t *testing.T) {
	f := newFixture(t)
	agent, other := f.agent(t, f.ws, "Alpha", true), f.agent(t, f.ws, "Beta", true)
	gone := f.agent(t, f.ws, "Gone", true)
	var rows []domain.EstimateRun
	for i := 1; i <= 12; i++ {
		id := f.run(t, agent, nil)
		ranAt := time.Date(2026, 10, 22, 0, i, 0, 0, time.UTC)
		cost := "0.100001"
		if i == 12 { // the newest has no cost; the oldest two are outside the sample
			cost = ""
		}
		if cost == "" {
			f.exec(t, `UPDATE agent_runs SET status = 'done', ran_at = ?, duration_ms = ?, cost_usd = NULL WHERE id = ?`, ranAt, i*1000, id)
		} else {
			f.exec(t, `UPDATE agent_runs SET status = 'done', ran_at = ?, duration_ms = ?, cost_usd = ? WHERE id = ?`, ranAt, i*1000, cost, id)
		}
		var c *float64
		if cost != "" {
			c = floatp(0.100001)
		}
		rows = append(rows, domain.EstimateRun{ID: id, AgentID: agent, RanAt: ranAt, DurationMS: intp(i * 1000), CostUSD: c})
	}
	failed := f.run(t, agent, nil)
	f.exec(t, `UPDATE agent_runs SET status = 'failed', duration_ms = 99999, cost_usd = 9, ran_at = '2026-10-23 00:00:00+00' WHERE id = ?`, failed)
	deleted := f.run(t, gone, nil)
	f.exec(t, `UPDATE agent_runs SET status = 'done', duration_ms = 5, cost_usd = 1 WHERE id = ?`, deleted)
	f.exec(t, `DELETE FROM agents WHERE id = ?`, gone)
	// An agent whose done run has neither a duration nor a cost has no row; one with only a cost does.
	empty := f.run(t, other, nil)
	f.exec(t, `UPDATE agent_runs SET status = 'done' WHERE id = ?`, empty)
	costOnly := f.run(t, f.agent(t, f.ws, "Cost", true), nil)
	f.exec(t, `UPDATE agent_runs SET status = 'done', cost_usd = 2 WHERE id = ?`, costOnly)
	// Another workspace's runs are not counted.
	foreign := f.agent(t, f.other, "Foreign", true)
	f.exec(t, `INSERT INTO agent_runs (workspace_id, agent_id, status, duration_ms) VALUES (?, ?, 'done', 1)`, f.other, foreign)

	got, err := f.store.AgentEstimates(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("AgentEstimates() error = %v", err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.AgentID)
	}
	if len(got) != 2 {
		t.Fatalf("AgentEstimates() agents = %v, want the sampled agent and the cost-only one", ids)
	}
	if got[0].AgentID > got[1].AgentID {
		t.Errorf("rows are not ordered by agent id: %v", ids)
	}
	var e domain.AgentRunEstimate
	for _, r := range got {
		if r.AgentID == agent {
			e = r
		}
	}
	// Newest 10 of 12: durations 3..12 s, mean 7.5 s; costs of 3..11 (nine runs, 12 has none).
	if e.SampleSize != 10 || e.AvgDurationMS == nil || *e.AvgDurationMS != 7500 || e.AvgCostUSD == nil || math.Abs(*e.AvgCostUSD-0.100001) > 1e-12 {
		t.Errorf("estimate = %+v (duration %v cost %v), want 10 / 7500 / 0.100001", e, deref(e.AvgDurationMS), deref(e.AvgCostUSD))
	}

	// The pure domain function is the reference for the same rows.
	want := domain.Estimates(rows)
	if len(want) != 1 || want[0].AgentID != agent || want[0].SampleSize != e.SampleSize ||
		*want[0].AvgDurationMS != *e.AvgDurationMS || math.Abs(*want[0].AvgCostUSD-*e.AvgCostUSD) > 1e-12 {
		t.Errorf("domain.Estimates() = %+v disagrees with the SQL %+v", want, e)
	}
	for _, r := range got {
		if r.AgentID != agent && (r.SampleSize != 1 || r.AvgDurationMS != nil || r.AvgCostUSD == nil || *r.AvgCostUSD != 2) {
			t.Errorf("cost-only agent estimate = %+v", r)
		}
	}
	if none, err := f.store.AgentEstimates(t.Context(), missingID); err != nil || none == nil || len(none) != 0 {
		t.Errorf("AgentEstimates(unknown workspace) = %#v, %v, want empty non-nil", none, err)
	}
}

func deref(p *float64) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}
