package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/evals/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

const noSuchID = "00000000-0000-4000-8000-000000000000"

func TestCreateCase_RoundTripsEveryField(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	finding, review, pr := e.finding(t, e.ws, agent, "a.go", "@@ -0,0 +1 @@\n+x")
	in := manualCase(e.ws, domain.OwnerAgent, agent, "name")
	in.Notes = sp("notes")
	in.Expectation.Severity, in.Expectation.Category, in.Expectation.Title = nil, nil, nil
	in.InputMeta = domain.InputMeta{PRTitle: "t", PRDescription: nil, Repo: sp("o/r"), PRNumber: ip(7), HeadSHA: sp("abc")}
	in.SourceFindingID, in.SourceReviewID, in.SourcePRID = &finding, &review, &pr

	got, err := e.store.CreateCase(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.CreatedAt.IsZero() {
		t.Fatalf("CreateCase() = %+v; want an id and a creation time", got)
	}
	want := domain.Case{
		ID: got.ID, WorkspaceID: e.ws, OwnerKind: domain.OwnerAgent, OwnerID: agent, Name: "name", Notes: sp("notes"),
		Expectation: in.Expectation, InputDiff: in.InputDiff, InputMeta: in.InputMeta,
		SourceFindingID: &finding, SourceReviewID: &review, SourcePRID: &pr, CreatedAt: got.CreatedAt,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CreateCase() mismatch (-want +got):\n%s", diff)
	}
	again, err := e.store.GetCase(t.Context(), e.ws, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(got, again, cmpopts.EquateApproxTime(time.Millisecond)); diff != "" {
		t.Errorf("GetCase() differs from the created case (-created +read):\n%s", diff)
	}
	// The jsonb columns carry the contract's snake_case shape; input_files stays NULL.
	if s := e.scalar(t, `SELECT expected_output::text FROM eval_cases WHERE id = ?`, got.ID); !strings.Contains(s, `"start_line": 1`) || !strings.Contains(s, `"severity": null`) {
		t.Errorf("expected_output = %s; want snake_case keys and explicit nulls", s)
	}
	if s := e.scalar(t, `SELECT (input_files IS NULL)::text FROM eval_cases WHERE id = ?`, got.ID); s != "true" {
		t.Errorf("input_files IS NULL = %s; want true", s)
	}
}

func TestCreateCase_DuplicateFindingIsCaseExistsWithTheWinner(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	finding, _, _ := e.finding(t, e.ws, agent, "a.go", "")
	in := manualCase(e.ws, domain.OwnerAgent, agent, "first")
	in.SourceFindingID = &finding
	first, err := e.store.CreateCase(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "second"
	_, err = e.store.CreateCase(t.Context(), in)
	var exists *domain.CaseExistsError
	if !errors.As(err, &exists) || exists.CaseID != first.ID || !errors.Is(err, domain.ErrCaseExists) {
		t.Fatalf("CreateCase(duplicate) error = %v; want *CaseExistsError{%s}", err, first.ID)
	}
	id, ok, err := e.store.CaseBySourceFinding(t.Context(), e.ws, finding)
	if err != nil || !ok || id != first.ID {
		t.Errorf("CaseBySourceFinding() = %q, %v, %v; want %q, true, nil", id, ok, err, first.ID)
	}
	if _, ok, _ := e.store.CaseBySourceFinding(t.Context(), e.workspace(t), finding); ok {
		t.Error("CaseBySourceFinding(other workspace) found the case")
	}
}

func TestCreateCase_LostRaceInsideATransactionDoesNotPoisonIt(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	finding, _, _ := e.finding(t, e.ws, agent, "a.go", "")
	in := manualCase(e.ws, domain.OwnerAgent, agent, "first")
	in.SourceFindingID = &finding
	if _, err := e.store.CreateCase(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	err := platformpg.NewTx(e.db).InTx(t.Context(), func(ctx context.Context) error {
		if _, err := e.store.CreateCase(ctx, in); !errors.Is(err, domain.ErrCaseExists) {
			t.Errorf("CreateCase(duplicate) error = %v; want ErrCaseExists", err)
		}
		n, err := e.store.CountCases(ctx, e.ws, domain.OwnerAgent, agent)
		if err != nil || n != 1 {
			t.Errorf("CountCases() after the lost race = %d, %v; want 1, nil", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateCase_HostileTextIsStorable(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	in := manualCase(e.ws, domain.OwnerAgent, agent, "na\x00me\xff")
	in.Notes = sp("no\x00tes")
	in.InputDiff = "d\x00iff"
	in.Expectation.Title = sp("ti\x00tle")
	in.InputMeta.PRTitle = "pr\x00"
	got, err := e.store.CreateCase(t.Context(), in)
	if err != nil {
		t.Fatalf("CreateCase(NUL) error = %v; want it stored", err)
	}
	if got.Name != "na�me�" || *got.Notes != "no�tes" || got.InputDiff != "d�iff" ||
		*got.Expectation.Title != "ti�tle" || got.InputMeta.PRTitle != "pr�" {
		t.Errorf("CreateCase(NUL) = %+v; want U+0000 and invalid bytes replaced by U+FFFD", got)
	}
	// An escaped backslash before the text u0000 is not a NUL and stays as written.
	in2 := manualCase(e.ws, domain.OwnerAgent, agent, "second")
	in2.Expectation.Title = sp(`\u0000`)
	got2, err := e.store.CreateCase(t.Context(), in2)
	if err != nil || *got2.Expectation.Title != `\u0000` {
		t.Errorf("CreateCase(backslash-u0000) = %v, %v; want the text kept", got2.Expectation.Title, err)
	}
}

func TestOwnerQueries(t *testing.T) {
	e := newEnv(t)
	other := e.workspace(t)
	agent, skill := e.agent(t, e.ws, "a"), e.skill(t, e.ws, "s", true)
	otherAgent := e.agent(t, other, "x")

	for _, tc := range []struct {
		name  string
		ws    string
		kind  domain.OwnerKind
		owner string
		want  bool
	}{
		{"agent", e.ws, domain.OwnerAgent, agent, true},
		{"skill", e.ws, domain.OwnerSkill, skill, true},
		{"skill id as agent", e.ws, domain.OwnerAgent, skill, false},
		{"other workspace", e.ws, domain.OwnerAgent, otherAgent, false},
		{"unknown", e.ws, domain.OwnerAgent, noSuchID, false},
		{"not a uuid", e.ws, domain.OwnerAgent, "nope", false},
	} {
		got, err := e.store.OwnerExists(t.Context(), tc.ws, tc.kind, tc.owner)
		if err != nil || got != tc.want {
			t.Errorf("OwnerExists(%s) = %v, %v; want %v, nil", tc.name, got, err, tc.want)
		}
	}

	c1 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "one")
	c2 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "two")
	e.newCase(t, e.ws, domain.OwnerSkill, skill, "skill case")
	// Same created_at: the id breaks the tie both ways.
	e.exec(t, `UPDATE eval_cases SET created_at = '2026-01-01T00:00:00Z' WHERE owner_id = ?`, agent)

	if n, err := e.store.CountCases(t.Context(), e.ws, domain.OwnerAgent, agent); err != nil || n != 2 {
		t.Errorf("CountCases(agent) = %d, %v; want 2, nil", n, err)
	}
	if n, _ := e.store.CountCases(t.Context(), e.ws, domain.OwnerSkill, skill); n != 1 {
		t.Errorf("CountCases(skill) = %d; want 1", n)
	}
	ids, err := e.store.CaseIDs(t.Context(), e.ws, domain.OwnerAgent, agent)
	wantIDs := []string{c1.ID, c2.ID}
	if c2.ID < c1.ID {
		wantIDs = []string{c2.ID, c1.ID}
	}
	if err != nil || cmp.Diff(wantIDs, ids) != "" {
		t.Errorf("CaseIDs() = %v, %v; want %v (created_at, id)", ids, err, wantIDs)
	}
	counts, err := e.store.AgentCaseCounts(t.Context(), e.ws)
	if err != nil || cmp.Diff(map[string]int{agent: 2}, counts) != "" {
		t.Errorf("AgentCaseCounts() = %v, %v; want only the agent's 2 cases", counts, err)
	}
	views, err := e.store.ListCases(t.Context(), e.ws, domain.OwnerAgent, agent)
	if err != nil || len(views) != 2 {
		t.Fatalf("ListCases() = %d views, %v; want 2", len(views), err)
	}
	wantDesc := []string{wantIDs[1], wantIDs[0]}
	if views[0].Case.ID != wantDesc[0] || views[1].Case.ID != wantDesc[1] {
		t.Errorf("ListCases() ids = %s, %s; want created_at desc then id desc %v", views[0].Case.ID, views[1].Case.ID, wantDesc)
	}
	if got, err := e.store.ListCases(t.Context(), e.ws, domain.OwnerAgent, "nope"); err != nil || len(got) != 0 {
		t.Errorf("ListCases(not a uuid) = %v, %v; want empty", got, err)
	}
}

func TestListCases_LastResultIsTheNewestFinishedRun(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c")
	bare := e.newCase(t, e.ws, domain.OwnerAgent, agent, "never ran")

	record := func(run domain.Run, status domain.CaseStatus) {
		t.Helper()
		if err := e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c.ID, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	old := e.newRun(t, e.ws, agent, c.ID)
	record(old, domain.CaseFailed)
	e.finish(t, old, domain.RunCompleted)
	newer := e.newRun(t, e.ws, agent, c.ID)
	record(newer, domain.CasePassed)
	e.finish(t, newer, domain.RunFailed)
	e.exec(t, `UPDATE eval_suite_runs SET finished_at = now() - interval '1 hour' WHERE id = ?`, old.ID)
	running := e.newRun(t, e.ws, agent, c.ID) // a running run never counts
	if err := e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: running.ID, CaseID: bare.ID, Status: domain.CaseError}); err != nil {
		t.Fatal(err)
	}

	view, err := e.store.GetCaseView(t.Context(), e.ws, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastResult == nil || view.LastResult.RunID != newer.ID || view.LastResult.Status != domain.CasePassed || view.LastResult.RanAt.IsZero() {
		t.Errorf("GetCaseView().LastResult = %+v; want the newer finished run's passed result", view.LastResult)
	}
	bareView, err := e.store.GetCaseView(t.Context(), e.ws, bare.ID)
	if err != nil || bareView.LastResult != nil {
		t.Errorf("GetCaseView(case only in a running run).LastResult = %+v, %v; want nil", bareView.LastResult, err)
	}
	if _, err := e.store.GetCaseView(t.Context(), e.workspace(t), c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetCaseView(other workspace) error = %v; want ErrNotFound", err)
	}
}

func TestGetCase_NotFound(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{noSuchID, "nope"} {
		if _, err := e.store.GetCase(t.Context(), e.ws, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetCase(%q) error = %v; want ErrNotFound", id, err)
		}
	}
}

func TestUpdateCase(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "old")
	stamp := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	got, err := e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) {
		if in.Name != "old" {
			t.Errorf("fn got name %q; want the stored one", in.Name)
		}
		in.Name = "new"
		in.Notes = sp("n")
		in.Expectation.Severity = nil
		in.Expectation.EndLine = 9
		in.InputDiff = "diff2"
		in.InputMeta.PRTitle = "pr2"
		in.InputMeta.PRDescription = nil
		in.ContentEditedAt = &stamp
		// Fields the store must not change.
		in.OwnerID, in.WorkspaceID = noSuchID, noSuchID
		return in, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new" || *got.Notes != "n" || got.Expectation.Severity != nil || got.Expectation.EndLine != 9 ||
		got.InputDiff != "diff2" || got.InputMeta.PRTitle != "pr2" || got.InputMeta.PRDescription != nil ||
		got.OwnerID != agent || got.WorkspaceID != e.ws || got.ContentEditedAt == nil || !got.ContentEditedAt.Equal(stamp) {
		t.Errorf("UpdateCase() = %+v; want the edit stored, owner and workspace kept", got)
	}

	// content_edited_at is set once and never cleared or moved.
	later := stamp.Add(time.Hour)
	got, err = e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) {
		in.Name = "newer"
		in.ContentEditedAt = &later
		return in, nil
	})
	if err != nil || !got.ContentEditedAt.Equal(stamp) || got.Name != "newer" {
		t.Errorf("UpdateCase(second edit) = %+v, %v; want the first stamp kept", got, err)
	}
	got, err = e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) {
		in.ContentEditedAt = nil
		return in, nil
	})
	if err != nil || got.ContentEditedAt == nil {
		t.Errorf("UpdateCase(clear stamp) = %+v, %v; want the stamp kept", got, err)
	}
}

func TestUpdateCase_UnchangedWritesNothing(t *testing.T) {
	e := newEnv(t)
	c := e.newCase(t, e.ws, domain.OwnerAgent, e.agent(t, e.ws, "a"), "c")
	before := e.scalar(t, `SELECT xmin::text FROM eval_cases WHERE id = ?`, c.ID)
	got, err := e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) { return in, nil })
	if err != nil {
		t.Fatal(err)
	}
	if after := e.scalar(t, `SELECT xmin::text FROM eval_cases WHERE id = ?`, c.ID); after != before {
		t.Errorf("row version after a no-op UpdateCase = %s; want %s (no write)", after, before)
	}
	if diff := cmp.Diff(c, got, cmpopts.EquateApproxTime(time.Millisecond)); diff != "" {
		t.Errorf("UpdateCase(no-op) mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdateCase_ErrorsRollBackAndPassThrough(t *testing.T) {
	e := newEnv(t)
	c := e.newCase(t, e.ws, domain.OwnerAgent, e.agent(t, e.ws, "a"), "c")
	boom := errors.New("boom")
	_, err := e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) {
		in.Name = "changed"
		return in, boom
	})
	if err != boom {
		t.Errorf("UpdateCase(fn error) = %v; want the very error fn returned", err)
	}
	if got, _ := e.store.GetCase(t.Context(), e.ws, c.ID); got.Name != "c" {
		t.Errorf("name after a failed UpdateCase = %q; want c", got.Name)
	}
	called := false
	for _, id := range []string{noSuchID, "nope"} {
		_, err = e.store.UpdateCase(t.Context(), e.ws, id, func(in domain.Case) (domain.Case, error) { called = true; return in, nil })
		if !errors.Is(err, domain.ErrNotFound) || called {
			t.Errorf("UpdateCase(%q) = %v, fn called %v; want ErrNotFound without calling fn", id, err, called)
		}
	}
	if _, err = e.store.UpdateCase(t.Context(), e.workspace(t), c.ID, func(in domain.Case) (domain.Case, error) { return in, nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdateCase(other workspace) = %v; want ErrNotFound", err)
	}
}

func TestUpdateCase_HoldsTheRowLock(t *testing.T) {
	e := newEnv(t)
	c := e.newCase(t, e.ws, domain.OwnerAgent, e.agent(t, e.ws, "a"), "c")

	inside, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, err := e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) {
			close(inside)
			<-release
			in.Name += "A"
			return in, nil
		})
		errs <- err
	})
	<-inside
	var sawFirst atomic.Bool
	wg.Go(func() {
		_, err := e.store.UpdateCase(t.Context(), e.ws, c.ID, func(in domain.Case) (domain.Case, error) {
			sawFirst.Store(strings.HasSuffix(in.Name, "A"))
			in.Name += "B"
			return in, nil
		})
		errs <- err
	})
	// The second caller must be blocked in SELECT ... FOR UPDATE before the first one lets go.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for blocked := "0"; blocked == "0"; {
		if ctx.Err() != nil {
			t.Fatal("the second UpdateCase never waited for the row lock")
		}
		blocked = e.scalar(t, `SELECT count(*)::text FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query ILIKE '%FOR UPDATE%'`)
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if got, _ := e.store.GetCase(t.Context(), e.ws, c.ID); got.Name != "cAB" || !sawFirst.Load() {
		t.Errorf("name = %q, second saw the first's commit = %v; want cAB, true", got.Name, sawFirst.Load())
	}
}

func TestDeleteCase(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c")
	run := e.newRun(t, e.ws, agent, c.ID)
	if err := e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c.ID, Status: domain.CasePassed}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteCase(t.Context(), e.workspace(t), c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteCase(other workspace) = %v; want ErrNotFound", err)
	}
	if err := e.store.DeleteCase(t.Context(), e.ws, c.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.scalar(t, `SELECT count(*)::text FROM eval_runs WHERE case_id = ?`, c.ID); n != "0" {
		t.Errorf("results of the deleted case = %s; want 0 (cascade)", n)
	}
	for _, id := range []string{c.ID, "nope"} {
		if err := e.store.DeleteCase(t.Context(), e.ws, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("DeleteCase(%q) again = %v; want ErrNotFound", id, err)
		}
	}
}

func TestSource(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	finding, review, pr := e.finding(t, e.ws, agent, "src/a.go", "@@ -0,0 +1 @@\n+x")
	e.exec(t, `UPDATE findings SET accepted_at = '2026-01-02T03:04:05Z' WHERE id = ?`, finding)

	got, err := e.store.Source(t.Context(), e.ws, finding)
	if err != nil {
		t.Fatal(err)
	}
	accepted := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := domain.FindingSource{
		FindingID: finding, ReviewID: review, PRID: pr, AgentID: &agent, AcceptedAt: &accepted,
		File: "src/a.go", StartLine: 3, EndLine: 5, Severity: "CRITICAL", Category: "security", Title: "T",
		Patch: sp("@@ -0,0 +1 @@\n+x"), PRTitle: "PR title", PRBody: sp("PR body"),
		RepoFullName: e.scalar(t, `SELECT full_name FROM repos LIMIT 1`), PRNumber: 7, HeadSHA: "abc123",
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateApproxTime(time.Second)); diff != "" {
		t.Errorf("Source() mismatch (-want +got):\n%s", diff)
	}

	// No stored patch and no agent on the review.
	noPatch, _, _ := e.finding(t, e.ws, "", "b.go", "")
	got, err = e.store.Source(t.Context(), e.ws, noPatch)
	if err != nil || got.Patch != nil || got.AgentID != nil || got.AcceptedAt != nil || got.DismissedAt != nil {
		t.Errorf("Source(no patch) = %+v, %v; want nil Patch, AgentID and decisions", got, err)
	}
	for _, tc := range []struct{ ws, id string }{{e.workspace(t), finding}, {e.ws, noSuchID}, {e.ws, "nope"}} {
		if _, err := e.store.Source(t.Context(), tc.ws, tc.id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Source(%q) error = %v; want ErrNotFound", tc.id, err)
		}
	}
}

func TestSnapshots(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "alpha")
	s1, s2, off := e.skill(t, e.ws, "s1", true), e.skill(t, e.ws, "s2", true), e.skill(t, e.ws, "off", false)
	e.link(t, agent, s2, 0)
	e.link(t, agent, off, 1)
	e.link(t, agent, s1, 2)

	snap, err := e.store.Agent(t.Context(), e.ws, agent)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.AgentSnapshot{
		AgentID: agent, Version: 3, Name: "alpha",
		Config: domain.Config{
			Provider: "openai", Model: "gpt-x", SystemPrompt: "prompt of alpha", Strategy: "auto",
			Skills: []domain.SkillSnapshot{
				{ID: s2, Name: "s2", Description: "desc s2", Body: "body of s2", Version: 2},
				{ID: s1, Name: "s1", Description: "desc s1", Body: "body of s1", Version: 2},
			},
		},
	}
	if diff := cmp.Diff(want, snap); diff != "" {
		t.Errorf("Agent() mismatch (-want +got):\n%s", diff)
	}
	bare, err := e.store.Agent(t.Context(), e.ws, e.agent(t, e.ws, "bare"))
	if err != nil || bare.Config.Skills == nil || len(bare.Config.Skills) != 0 {
		t.Errorf("Agent(no skills).Skills = %#v, %v; want an empty non-nil slice", bare.Config.Skills, err)
	}
	disabled, err := e.store.Skill(t.Context(), e.ws, off)
	if err != nil || disabled.Name != "off" || disabled.Body != "body of off" || disabled.Version != 2 {
		t.Errorf("Skill(disabled) = %+v, %v; want the skill", disabled, err)
	}
	other := e.workspace(t)
	for _, id := range []string{agent, noSuchID, "nope"} {
		if _, err := e.store.Agent(t.Context(), other, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Agent(other workspace, %q) error = %v; want ErrNotFound", id, err)
		}
	}
	if _, err := e.store.Skill(t.Context(), other, s1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Skill(other workspace) error = %v; want ErrNotFound", err)
	}
	if _, err := e.store.Skill(t.Context(), e.ws, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Skill(not a uuid) error = %v; want ErrNotFound", err)
	}
}

func TestAgentIDs_OrderOfGetAgents(t *testing.T) {
	e := newEnv(t)
	first, upper, lower := e.agent(t, e.ws, "omega"), e.agent(t, e.ws, "Zeta"), e.agent(t, e.ws, "alpha")
	e.exec(t, `UPDATE agents SET created_at = '2026-01-01T00:00:00Z' WHERE id = ?`, first)
	e.exec(t, `UPDATE agents SET created_at = '2026-01-02T00:00:00Z' WHERE id IN (?, ?)`, upper, lower)
	e.exec(t, `UPDATE agents SET enabled = false WHERE id = ?`, lower)
	e.agent(t, e.workspace(t), "elsewhere")
	got, err := e.store.AgentIDs(t.Context(), e.ws)
	// Creation time first (as GET /agents); a tie sorts by COLLATE "C" name, uppercase before
	// lowercase; a disabled agent is listed.
	if want := []string{first, upper, lower}; err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("AgentIDs() = %v, %v; want %v", got, err, want)
	}
}
