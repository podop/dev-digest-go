package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

type svcEnv struct {
	svc   *app.Service
	store *memStore
	queue *queue
}

func newSvc(t *testing.T, repos map[string]app.Repo) *svcEnv {
	t.Helper()
	e := &svcEnv{store: &memStore{}, queue: &queue{}}
	e.svc = app.NewService(e.store, memRepos{repos}, e.queue, app.ServiceOptions{
		Redact: redactKey("sk-secret"), Now: func() time.Time { return t0 },
	})
	return e
}

func cloned() map[string]app.Repo {
	return map[string]app.Repo{repoID: {ID: repoID, FullName: "o/r", ClonePath: "/clones/o/r"}}
}

func wantAppErr(t *testing.T, err error, kind apperr.Kind, code, msg string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an apperr of kind %v", err, kind)
	}
	if ae.Kind != kind || ae.Code != code || ae.Message != msg {
		t.Errorf("apperr = {%v %q %q}, want {%v %q %q}", ae.Kind, ae.Code, ae.Message, kind, code, msg)
	}
}

func TestExtract_StartsRunningScanAndEnqueues(t *testing.T) {
	e := newSvc(t, cloned())
	scan, err := e.svc.Extract(t.Context(), ws, repoID)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if scan.Status != domain.ScanRunning || scan.RepoID != repoID || len(scan.SampledFiles) != 0 || scan.FinishedAt != nil {
		t.Errorf("Extract() = %+v, want a running, empty scan of %s", scan, repoID)
	}
	want := []queued{{Workspace: ws, Kind: "conventions.extract",
		Payload: app.ExtractPayload{ScanID: scan.ID, WorkspaceID: ws, RepoID: repoID}}}
	if diff := cmp.Diff(want, e.queue.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}
}

func TestExtract_Refusals(t *testing.T) {
	notCloned := map[string]app.Repo{repoID: {ID: repoID, FullName: "o/r"}}
	tests := []struct {
		name    string
		repos   map[string]app.Repo
		repo    string
		prepare func(*memStore)
		kind    apperr.Kind
		code    string
		msg     string
	}{
		{"unknown repo", cloned(), "other", nil, apperr.KindNotFound, "not_found", "Repo not found"},
		{"not cloned", notCloned, repoID, nil, apperr.KindValidation, "not_cloned",
			"The repo is not cloned yet — wait for the clone to finish"},
		{"scan running", cloned(), repoID, func(m *memStore) {
			m.scans = append(m.scans, &domain.Scan{ID: "old", RepoID: repoID, Status: domain.ScanRunning, StartedAt: t0.Add(-time.Minute)})
		}, apperr.KindConflict, "scan_running", "A conventions scan is already running for this repo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newSvc(t, tc.repos)
			before := 0
			if tc.prepare != nil {
				tc.prepare(e.store)
				before = len(e.store.scans)
			}
			_, err := e.svc.Extract(t.Context(), ws, tc.repo)
			wantAppErr(t, err, tc.kind, tc.code, tc.msg)
			if got := len(e.store.scans); got != before {
				t.Errorf("scan rows = %d, want %d (no new row)", got, before)
			}
			if len(e.queue.jobs) != 0 {
				t.Errorf("enqueued %d jobs, want none", len(e.queue.jobs))
			}
		})
	}
}

func TestExtract_FailsStaleRunningScanFirst(t *testing.T) {
	e := newSvc(t, cloned())
	e.store.scans = append(e.store.scans, &domain.Scan{ID: "old", RepoID: repoID, Status: domain.ScanRunning, StartedAt: t0.Add(-11 * time.Minute)})
	scan, err := e.svc.Extract(t.Context(), ws, repoID)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	old := e.store.scan("old")
	if old.Status != domain.ScanFailed || old.Error == nil || *old.Error != "The scan was interrupted" || old.FinishedAt == nil {
		t.Errorf("old scan = %+v, want failed with %q and finished_at", old, "The scan was interrupted")
	}
	if scan.ID == "old" || scan.Status != domain.ScanRunning {
		t.Errorf("Extract() = %+v, want a new running scan", scan)
	}
}

func TestExtract_EnqueueFailureFailsTheScanWithRedactedText(t *testing.T) {
	e := newSvc(t, cloned())
	e.queue.err = errors.New("queue down for sk-secret")
	_, err := e.svc.Extract(t.Context(), ws, repoID)
	if err == nil {
		t.Fatal("Extract() error = nil, want the enqueue error")
	}
	if _, isApp := apperr.As(err); isApp {
		t.Errorf("Extract() error = %v, want a plain error (500 internal_error at the edge)", err)
	}
	s := e.store.lastScan(t)
	if s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "queue down for [REDACTED]" {
		t.Errorf("scan = %+v, want failed with the redacted enqueue error", s)
	}
}

func TestExtract_EnqueueFailureStillFailsTheScanWhenRequestIsCancelled(t *testing.T) {
	e := newSvc(t, cloned())
	e.queue.err = errBoom
	ctx, cancel := contextCancelled(t)
	defer cancel()
	if _, err := e.svc.Extract(ctx, ws, repoID); err == nil {
		t.Fatal("Extract() error = nil")
	}
	if got := e.store.failCtxErr; len(got) != 1 || got[0] != nil {
		t.Errorf("FailScan context errors = %v, want one live context", got)
	}
}

func TestUpdate_EditedFlag(t *testing.T) {
	str := func(s string) *string { return &s }
	status := func(s domain.Status) *domain.Status { return &s }
	tests := []struct {
		name       string
		start      domain.Convention
		patch      app.Patch
		wantStatus domain.Status
		wantRule   string
		wantCat    string
		wantEdited bool
	}{
		{"status alone keeps edited false", domain.Convention{Status: domain.StatusPending, Rule: "r", Category: "style"},
			app.Patch{Status: status(domain.StatusAccepted)}, domain.StatusAccepted, "r", "style", false},
		{"different rule sets edited", domain.Convention{Status: domain.StatusPending, Rule: "r", Category: "style"},
			app.Patch{Rule: str("New rule")}, domain.StatusPending, "New rule", "style", true},
		{"same rule is not an edit", domain.Convention{Status: domain.StatusPending, Rule: "r", Category: "style"},
			app.Patch{Rule: str("r"), Category: str("style")}, domain.StatusPending, "r", "style", false},
		{"different category sets edited", domain.Convention{Status: domain.StatusPending, Rule: "r", Category: "style"},
			app.Patch{Category: str("naming")}, domain.StatusPending, "r", "naming", true},
		{"edited stays after a status change", domain.Convention{Status: domain.StatusAccepted, Rule: "r", Category: "style", Edited: true},
			app.Patch{Status: status(domain.StatusPending)}, domain.StatusPending, "r", "style", true},
		{"any transition is allowed", domain.Convention{Status: domain.StatusRejected, Rule: "r", Category: "style"},
			app.Patch{Status: status(domain.StatusAccepted)}, domain.StatusAccepted, "r", "style", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newSvc(t, cloned())
			tc.start.ID = "c1"
			e.store.add(tc.start)
			got, err := e.svc.Update(t.Context(), ws, "c1", tc.patch)
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if got.Status != tc.wantStatus || got.Rule != tc.wantRule || got.Category != tc.wantCat || got.Edited != tc.wantEdited {
				t.Errorf("Update() = {%s %q %s edited=%v}, want {%s %q %s edited=%v}", got.Status, got.Rule, got.Category,
					got.Edited, tc.wantStatus, tc.wantRule, tc.wantCat, tc.wantEdited)
			}
			if !got.UpdatedAt.Equal(t0) {
				t.Errorf("Update() updated_at = %v, want %v", got.UpdatedAt, t0)
			}
		})
	}
}

func TestUpdate_UnknownConvention(t *testing.T) {
	e := newSvc(t, cloned())
	status := domain.StatusAccepted
	_, err := e.svc.Update(t.Context(), ws, "nope", app.Patch{Status: &status})
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Convention not found")
}

func TestNotAccepted(t *testing.T) {
	e := newSvc(t, cloned())
	e.store.add(domain.Convention{ID: "a1", Status: domain.StatusAccepted})
	e.store.add(domain.Convention{ID: "a2", Status: domain.StatusAccepted})
	e.store.add(domain.Convention{ID: "p1", Status: domain.StatusPending})
	e.store.add(domain.Convention{ID: "other", RepoID: "repo-2", Status: domain.StatusAccepted})

	got, err := e.svc.NotAccepted(t.Context(), ws, repoID, []string{"a1", "p1", "a2", "a1", "other", "ghost", "p1"})
	if err != nil {
		t.Fatalf("NotAccepted() error = %v", err)
	}
	if diff := cmp.Diff([]string{"p1", "other", "ghost"}, got); diff != "" {
		t.Errorf("NotAccepted() mismatch (-want +got):\n%s", diff)
	}

	if _, err := e.svc.NotAccepted(t.Context(), ws, "nope", []string{"a1"}); err == nil {
		t.Error("NotAccepted(unknown repo) error = nil, want not found")
	} else {
		wantAppErr(t, err, apperr.KindNotFound, "not_found", "Repo not found")
	}
}

func TestAttachSkill(t *testing.T) {
	e := newSvc(t, cloned())
	e.store.add(domain.Convention{ID: "a1", Status: domain.StatusAccepted})
	e.store.add(domain.Convention{ID: "a2", Status: domain.StatusAccepted})
	if err := e.svc.AttachSkill(t.Context(), ws, repoID, []string{"a1", "a1"}, "skill-9"); err != nil {
		t.Fatalf("AttachSkill() error = %v", err)
	}
	var got []string
	for _, c := range e.store.conventions {
		if c.SkillID != nil {
			got = append(got, c.ID+"="+*c.SkillID)
		}
	}
	if diff := cmp.Diff([]string{"a1=skill-9"}, got); diff != "" {
		t.Errorf("conventions with a skill mismatch (-want +got):\n%s", diff)
	}
}

func TestFailInterrupted(t *testing.T) {
	e := newSvc(t, cloned())
	e.store.scans = append(e.store.scans,
		&domain.Scan{ID: "r1", RepoID: "x", Status: domain.ScanRunning},
		&domain.Scan{ID: "r2", RepoID: "y", Status: domain.ScanRunning},
		&domain.Scan{ID: "d", RepoID: "y", Status: domain.ScanDone},
	)
	n, err := e.svc.FailInterrupted(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("FailInterrupted() = %d, %v, want 2, nil", n, err)
	}
	for _, id := range []string{"r1", "r2"} {
		s := e.store.scan(id)
		if s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "The scan was interrupted" || s.FinishedAt == nil || !s.FinishedAt.Equal(t0) {
			t.Errorf("scan %s = %+v, want failed, interrupted, finished at %v", id, s, t0)
		}
	}
	if s := e.store.scan("d"); s.Status != domain.ScanDone {
		t.Errorf("done scan status = %s, want done", s.Status)
	}
}

func TestState(t *testing.T) {
	e := newSvc(t, cloned())
	st, err := e.svc.State(t.Context(), ws, repoID)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if st.Scan != nil || len(st.Conventions) != 0 {
		t.Errorf("State() of an unscanned repo = %+v, want no scan and no conventions", st)
	}
	e.store.scans = append(e.store.scans,
		&domain.Scan{ID: "old", RepoID: repoID, Status: domain.ScanDone, StartedAt: t0.Add(-time.Hour)},
		&domain.Scan{ID: "new", RepoID: repoID, Status: domain.ScanDone, StartedAt: t0})
	e.store.add(domain.Convention{ID: "c1"})
	st, err = e.svc.State(t.Context(), ws, repoID)
	if err != nil || st.Scan == nil || st.Scan.ID != "new" || len(st.Conventions) != 1 {
		t.Errorf("State() = %+v, %v, want the newer scan and one convention", st, err)
	}
	_, err = e.svc.State(t.Context(), ws, "nope")
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Repo not found")
}

func TestExtract_StartError(t *testing.T) {
	e := newSvc(t, cloned())
	e.store.startErr = errBoom
	_, err := e.svc.Extract(t.Context(), ws, repoID)
	if !errors.Is(err, errBoom) || strings.Contains(err.Error(), "scan_running") {
		t.Errorf("Extract() error = %v, want it to wrap the store error", err)
	}
}

// fakeBudget is the spend cap port: it records every call and answers with err.
type fakeBudget struct {
	err   error
	calls []budgetCall
}

type budgetCall struct{ Workspace, Op string }

func (b *fakeBudget) Admit(_ context.Context, workspaceID, op string) error {
	b.calls = append(b.calls, budgetCall{workspaceID, op})
	return b.err
}

func newBudgetSvc(t *testing.T, repos map[string]app.Repo, b *fakeBudget) *svcEnv {
	t.Helper()
	e := &svcEnv{store: &memStore{}, queue: &queue{}}
	e.svc = app.NewService(e.store, memRepos{repos}, e.queue, app.ServiceOptions{
		Now: func() time.Time { return t0 }, Budget: b,
	})
	return e
}

func capReached() error {
	return apperr.PaymentRequired("Monthly spend cap reached").WithCode("spend_cap_reached")
}

func TestExtract_Budget(t *testing.T) {
	t.Run("admitted once with the op, then the scan starts", func(t *testing.T) {
		b := &fakeBudget{}
		e := newBudgetSvc(t, cloned(), b)
		if _, err := e.svc.Extract(t.Context(), ws, repoID); err != nil {
			t.Fatalf("Extract() error = %v", err)
		}
		want := []budgetCall{{ws, "conventions_extract"}}
		if diff := cmp.Diff(want, b.calls); diff != "" {
			t.Errorf("Admit calls mismatch (-want +got):\n%s", diff)
		}
		if len(e.queue.jobs) != 1 {
			t.Errorf("enqueued %d jobs, want 1", len(e.queue.jobs))
		}
	})
	t.Run("cap reached is a 402 and nothing is written or enqueued", func(t *testing.T) {
		b := &fakeBudget{err: capReached()}
		e := newBudgetSvc(t, cloned(), b)
		stale := &domain.Scan{ID: "old", RepoID: repoID, Status: domain.ScanRunning, StartedAt: t0.Add(-11 * time.Minute)}
		e.store.scans = append(e.store.scans, stale)

		_, err := e.svc.Extract(t.Context(), ws, repoID)

		wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap reached")
		if len(e.store.scans) != 1 || e.store.scan("old").Status != domain.ScanRunning {
			t.Errorf("scans = %+v, want the stale scan untouched and no new row", e.store.scans)
		}
		if len(e.queue.jobs) != 0 {
			t.Errorf("enqueued %d jobs, want none", len(e.queue.jobs))
		}
	})
	t.Run("a failed spend read passes through as a plain error", func(t *testing.T) {
		b := &fakeBudget{err: errBoom}
		e := newBudgetSvc(t, cloned(), b)

		_, err := e.svc.Extract(t.Context(), ws, repoID)

		if !errors.Is(err, errBoom) {
			t.Fatalf("Extract() error = %v, want errBoom unchanged", err)
		}
		if _, isApp := apperr.As(err); isApp {
			t.Errorf("Extract() error = %v, want a plain error (500 at the edge)", err)
		}
		if len(e.store.scans) != 0 || len(e.queue.jobs) != 0 {
			t.Errorf("scans = %d, jobs = %d, want none (fail closed)", len(e.store.scans), len(e.queue.jobs))
		}
	})
	t.Run("not found and not cloned win over 402 and skip the check", func(t *testing.T) {
		tests := []struct {
			name  string
			repos map[string]app.Repo
			repo  string
			kind  apperr.Kind
		}{
			{"unknown repo", cloned(), "other", apperr.KindNotFound},
			{"not cloned", map[string]app.Repo{repoID: {ID: repoID, FullName: "o/r"}}, repoID, apperr.KindValidation},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				b := &fakeBudget{err: capReached()}
				e := newBudgetSvc(t, tc.repos, b)

				_, err := e.svc.Extract(t.Context(), ws, tc.repo)

				if ae, ok := apperr.As(err); !ok || ae.Kind != tc.kind {
					t.Errorf("Extract() error = %v, want kind %v", err, tc.kind)
				}
				if len(b.calls) != 0 {
					t.Errorf("Admit called %d times, want 0", len(b.calls))
				}
			})
		}
	})
	t.Run("a running scan (409) is checked after the cap, so 402 wins", func(t *testing.T) {
		b := &fakeBudget{err: capReached()}
		e := newBudgetSvc(t, cloned(), b)
		e.store.scans = append(e.store.scans, &domain.Scan{ID: "old", RepoID: repoID, Status: domain.ScanRunning, StartedAt: t0.Add(-time.Minute)})

		_, err := e.svc.Extract(t.Context(), ws, repoID)

		wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap reached")
	})
	t.Run("a nil budget leaves Extract unchanged", func(t *testing.T) {
		e := newSvc(t, cloned()) // newSvc sets no Budget
		if _, err := e.svc.Extract(t.Context(), ws, repoID); err != nil {
			t.Fatalf("Extract() error = %v", err)
		}
		if len(e.queue.jobs) != 1 {
			t.Errorf("enqueued %d jobs, want 1", len(e.queue.jobs))
		}
	})
}
