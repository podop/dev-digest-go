package postgres_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/skills/domain"
)

// statsFixture inserts the AC246 scene: skill S attached to 2 done runs and 1 failed run within
// 30 days and 1 done run 40 days old; one review of a done run with 3 findings for S.
type statsFixture struct {
	fixture
	skill domain.Skill
	now   time.Time
}

func newStatsFixture(t *testing.T) statsFixture {
	t.Helper()
	f := newFixture(t)
	s := statsFixture{fixture: f, skill: f.skill(t, "s"), now: time.Now()}
	agent := f.agent(t, f.ws, "a", true)

	run := func(status string, age time.Duration) string {
		id := f.insert(t, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, ran_at, status) VALUES (?, ?, ?, ?, ?) RETURNING id`,
			f.ws, agent, f.pr, s.now.Add(-age), status)
		f.exec(t, `INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 0)`, id, s.skill.ID)
		return id
	}
	done1 := run("done", time.Hour)
	run("done", 2*time.Hour)
	run("failed", 3*time.Hour)
	run("done", 40*24*time.Hour)

	review := f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind, run_id, created_at) VALUES (?, ?, 'review', ?, ?) RETURNING id`,
		f.ws, f.pr, done1, s.now.Add(-time.Hour))
	finding := func(category, severity string, accepted, dismissed bool) {
		var at, dt *time.Time
		if accepted {
			at = &s.now
		}
		if dismissed {
			dt = &s.now
		}
		f.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, skill_id, accepted_at, dismissed_at)
			VALUES (?, 'f', 1, 1, ?, ?, 't', 'r', 0.5, ?, ?, ?)`, review, severity, category, s.skill.ID, at, dt)
	}
	finding("bug", "CRITICAL", true, false)
	finding("bug", "WARNING", false, true)
	finding("security", "WARNING", false, false)
	return s
}

func TestStore_CountersAndBreakdown(t *testing.T) {
	s := newStatsFixture(t)
	since := domain.WindowStart(s.now, 30)

	want := domain.Counters{RunsAttached: 2, RunsCited: 1, Findings: 3, Accepted: 1, Dismissed: 1}
	for _, only := range []string{s.skill.ID, ""} {
		got, err := s.store.Counters(t.Context(), s.ws, since, only)
		if err != nil {
			t.Fatalf("Counters(skill=%q) error = %v", only, err)
		}
		if diff := cmp.Diff(map[string]domain.Counters{s.skill.ID: want}, got); diff != "" {
			t.Errorf("Counters(skill=%q) mismatch (-want +got):\n%s", only, diff)
		}
	}
	if got := want.PullRate(); got == nil || *got != 0.5 {
		t.Errorf("PullRate() = %v, want 0.5", got)
	}
	if got := want.AcceptRate(); got == nil || *got != 0.5 {
		t.Errorf("AcceptRate() = %v, want 0.5", got)
	}

	br, err := s.store.Breakdown(t.Context(), s.ws, s.skill.ID, since)
	if err != nil {
		t.Fatalf("Breakdown error = %v", err)
	}
	wantBr := domain.Breakdown{
		ByCategory: []domain.CountBy{{Key: "bug", Count: 2}, {Key: "security", Count: 1}},
		BySeverity: []domain.CountBy{{Key: "WARNING", Count: 2}, {Key: "CRITICAL", Count: 1}},
	}
	if diff := cmp.Diff(wantBr, br); diff != "" {
		t.Errorf("Breakdown mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_CountersWideWindowIncludesOldRun(t *testing.T) {
	s := newStatsFixture(t)
	got, err := s.store.Counters(t.Context(), s.ws, domain.WindowStart(s.now, 60), s.skill.ID)
	if err != nil {
		t.Fatalf("Counters error = %v", err)
	}
	if got[s.skill.ID].RunsAttached != 3 {
		t.Errorf("RunsAttached over 60 days = %d, want 3 (the 40-day-old done run counts)", got[s.skill.ID].RunsAttached)
	}
}

func TestStore_CountersIgnoreOtherWorkspaces(t *testing.T) {
	s := newStatsFixture(t)
	got, err := s.store.Counters(t.Context(), s.other, domain.WindowStart(s.now, 30), "")
	if err != nil {
		t.Fatalf("Counters error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Counters(other workspace) = %v, want none", got)
	}
}

func TestStore_CountersEmptyDatabase(t *testing.T) {
	f := newFixture(t)
	sk := f.skill(t, "s")
	got, err := f.store.Counters(t.Context(), f.ws, domain.WindowStart(time.Now(), 30), sk.ID)
	if err != nil || len(got) != 0 {
		t.Errorf("Counters(no runs) = %v, %v, want empty", got, err)
	}
	br, err := f.store.Breakdown(t.Context(), f.ws, sk.ID, domain.WindowStart(time.Now(), 30))
	if err != nil || len(br.ByCategory) != 0 || len(br.BySeverity) != 0 {
		t.Errorf("Breakdown(no findings) = %+v, %v, want empty", br, err)
	}
}
