package app_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/app"
	"dev-digest-go/backend/internal/autoreview/domain"
)

const ws = "w1"

func (f *fakes) outcomes() map[string]domain.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]domain.Outcome{}
	for _, r := range f.records {
		out[r.PRID] = r.Outcome
	}
	return out
}

func TestAfterCycle_NotArmed(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*domain.Setting)
	}{
		{"disabled (AC11)", func(s *domain.Setting) { s.Enabled = false }},
		{"no agent ids", func(s *domain.Setting) { s.AgentIDs = nil }},
		{"no triggers", func(s *domain.Setting) { s.OnNewPR, s.OnNewCommits = false, false }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.pulls = candidates(2)
			tc.mod(&f.setting)
			f.service(nil).AfterCycle(t.Context(), ws)
			if got := f.callLog(); !slices.Equal(got, []string{"settings"}) {
				t.Errorf("AfterCycle calls = %v, want only the setting read", got)
			}
			if len(f.records) != 0 || len(f.starts()) != 0 {
				t.Errorf("AfterCycle recorded %v and started %v, want nothing", f.records, f.starts())
			}
		})
	}
}

func TestAfterCycle_SettingReadErrorDoesNothing(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.settErr = errBoom
	f.service(nil).AfterCycle(t.Context(), ws)
	if len(f.records) != 0 || len(f.starts()) != 0 {
		t.Errorf("AfterCycle with unreadable setting recorded %v and started %v", f.records, f.starts())
	}
}

func TestAfterCycle_StartsClaimedBeforeStartThenSettled(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.setting.AgentIDs = []string{"a2", "a1"}
	f.enabled["a2"] = true
	f.startRuns = 2
	f.service(nil).AfterCycle(t.Context(), ws)

	wantCalls := []string{"settings", "candidates", "enabled_agents", "budget",
		"claim:pr00:started", "start:pr00", "finish:pr00:started"}
	if diff := cmp.Diff(wantCalls, f.callLog()); diff != "" {
		t.Errorf("AfterCycle call order mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]startCall{{PR: "pr00", Agents: []string{"a2", "a1"}}}, f.starts()); diff != "" {
		t.Errorf("StartAuto calls mismatch (-want +got):\n%s", diff)
	}
	want := domain.Record{
		WorkspaceID: ws, PRID: "pr00", HeadSHA: candidates(1)[0].HeadSHA, Event: domain.EventNewPR,
		Outcome: domain.OutcomeStarted, RunsStarted: 2, DecidedAt: t0,
	}
	got, _ := f.record("pr00", want.HeadSHA)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stored record mismatch (-want +got):\n%s", diff)
	}
}

func TestAfterCycle_SingleAgentRun(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.service(nil).AfterCycle(t.Context(), ws)
	if got := f.starts(); len(got) != 1 || !slices.Equal(got[0].Agents, []string{"a1"}) {
		t.Errorf("StartAuto calls = %v, want one call with [a1]", got)
	}
}

// AC6: the event decides which trigger condition counts.
func TestAfterCycle_TriggerConditions(t *testing.T) {
	tests := []struct {
		name             string
		onNewPR, onNewCm bool
		hasRecord        bool
		want             domain.Outcome
		wantEvent        domain.Event
	}{
		{"new PR, on_new_pr ticked", true, false, false, domain.OutcomeStarted, domain.EventNewPR},
		{"new PR, on_new_pr off", false, true, false, domain.OutcomeSkippedTriggerOff, domain.EventNewPR},
		{"new head, on_new_commits ticked", false, true, true, domain.OutcomeStarted, domain.EventNewCommits},
		{"new head, on_new_commits off", true, false, true, domain.OutcomeSkippedTriggerOff, domain.EventNewCommits},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.setting.OnNewPR, f.setting.OnNewCommits = tc.onNewPR, tc.onNewCm
			f.pulls = candidates(1)
			if tc.hasRecord { // an earlier head of the same PR was decided
				f.records[key("pr00", "old")] = domain.Record{PRID: "pr00", HeadSHA: "old", Outcome: domain.OutcomeStarted, DecidedAt: t0.Add(-48 * time.Hour)}
			}
			f.service(nil).AfterCycle(t.Context(), ws)
			got, ok := f.record("pr00", f.pulls[0].HeadSHA)
			if !ok {
				t.Fatalf("AfterCycle stored no record for the candidate")
			}
			if got.Outcome != tc.want || got.Event != tc.wantEvent {
				t.Errorf("record = %s/%s, want %s/%s", got.Event, got.Outcome, tc.wantEvent, tc.want)
			}
			if tc.want == domain.OutcomeSkippedTriggerOff && (len(f.starts()) != 0 || f.budgetCalls != 0) {
				t.Errorf("a trigger-off skip started %v and asked the budget %d times, want neither", f.starts(), f.budgetCalls)
			}
		})
	}
}

// AC7, EC9, EC11, EC12: one table of what each kind of candidate gets.
func TestAfterCycle_SkipRules(t *testing.T) {
	tests := []struct {
		name       string
		cand       func(*domain.Candidate)
		selected   []string
		budgetErr  error
		wantRecord bool
		want       domain.Outcome
		wantAgents []string
	}{
		{name: "manual run in progress: no record (EC12)", cand: func(c *domain.Candidate) { c.Running = true }},
		{name: "already reviewed at this head", cand: func(c *domain.Candidate) { c.LastReviewedSHA = c.HeadSHA },
			wantRecord: true, want: domain.OutcomeSkippedAlreadyReviewed},
		{name: "reviewed at an older head still starts", cand: func(c *domain.Candidate) { c.LastReviewedSHA = "older" },
			wantRecord: true, want: domain.OutcomeStarted, wantAgents: []string{"a1"}},
		{name: "enabled, disabled and deleted agents: only the enabled runs (EC9)", cand: func(*domain.Candidate) {},
			selected: []string{"gone", "off", "a1"}, wantRecord: true, want: domain.OutcomeStarted, wantAgents: []string{"a1"}},
		{name: "only unavailable agents", cand: func(*domain.Candidate) {},
			selected: []string{"gone", "off"}, wantRecord: true, want: domain.OutcomeSkippedNoAgents},
		{name: "cap reached (EC11)", cand: func(*domain.Candidate) {}, budgetErr: app.ErrCapReached,
			wantRecord: true, want: domain.OutcomeSkippedSpendCap},
		{name: "cap reached, wrapped", cand: func(*domain.Candidate) {}, budgetErr: fmt.Errorf("admit: %w", app.ErrCapReached),
			wantRecord: true, want: domain.OutcomeSkippedSpendCap},
		{name: "budget check failed: fail closed, no record", cand: func(*domain.Candidate) {}, budgetErr: errBoom},
		{name: "fork PR: recorded, no start (D13)", cand: func(c *domain.Candidate) { c.IsFork = true },
			wantRecord: true, want: domain.OutcomeSkippedFork},
		{name: "fork PR with the cap reached: still skipped_fork", cand: func(c *domain.Candidate) { c.IsFork = true },
			budgetErr: app.ErrCapReached, wantRecord: true, want: domain.OutcomeSkippedFork},
		{name: "unknown head repository: no record, no start (D13)", cand: func(c *domain.Candidate) { c.HeadRepoUnknown = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.enabled["off"] = false
			if tc.selected != nil {
				f.setting.AgentIDs = tc.selected
			}
			f.budgetErr = tc.budgetErr
			f.pulls = candidates(1)
			tc.cand(&f.pulls[0])
			f.service(nil).AfterCycle(t.Context(), ws)

			got, ok := f.record("pr00", f.pulls[0].HeadSHA)
			if ok != tc.wantRecord {
				t.Fatalf("record stored = %v, want %v", ok, tc.wantRecord)
			}
			if ok && got.Outcome != tc.want {
				t.Errorf("record outcome = %s, want %s", got.Outcome, tc.want)
			}
			var wantStarts []startCall
			if tc.wantAgents != nil {
				wantStarts = []startCall{{PR: "pr00", Agents: tc.wantAgents}}
			}
			if diff := cmp.Diff(wantStarts, f.starts()); diff != "" {
				t.Errorf("StartAuto calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// EC12: after the manual run ends on this head, the next cycle records skipped_already_reviewed.
func TestAfterCycle_RunningThenFinished(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.pulls[0].Running = true
	svc := f.service(nil)
	svc.AfterCycle(t.Context(), ws)
	if len(f.records) != 0 {
		t.Fatalf("a running PR was recorded: %v", f.records)
	}
	f.pulls[0].Running, f.pulls[0].LastReviewedSHA = false, f.pulls[0].HeadSHA
	svc.AfterCycle(t.Context(), ws)
	if got := f.outcomes()["pr00"]; got != domain.OutcomeSkippedAlreadyReviewed {
		t.Errorf("outcome after the run ended = %q, want skipped_already_reviewed", got)
	}
}

// A2: the cap is asked once per evaluation, at the first candidate that reaches it.
func TestAfterCycle_BudgetAskedOnce(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		f := newFakes()
		f.pulls = candidates(4)
		f.service(nil).AfterCycle(t.Context(), ws)
		if f.budgetCalls != 1 || len(f.starts()) != 4 {
			t.Errorf("budget calls = %d, starts = %d, want 1 and 4", f.budgetCalls, len(f.starts()))
		}
	})
	t.Run("denied", func(t *testing.T) {
		f := newFakes()
		f.pulls = candidates(4)
		f.budgetErr = app.ErrCapReached
		f.service(nil).AfterCycle(t.Context(), ws)
		if f.budgetCalls != 1 || len(f.starts()) != 0 {
			t.Errorf("budget calls = %d, starts = %d, want 1 and 0", f.budgetCalls, len(f.starts()))
		}
		for pr, o := range f.outcomes() {
			if o != domain.OutcomeSkippedSpendCap {
				t.Errorf("outcome of %s = %s, want skipped_spend_cap", pr, o)
			}
		}
		if len(f.records) != 4 {
			t.Errorf("records = %d, want 4", len(f.records))
		}
	})
	t.Run("failed check", func(t *testing.T) {
		f := newFakes()
		f.pulls = candidates(4)
		f.budgetErr = errBoom
		f.service(nil).AfterCycle(t.Context(), ws)
		if f.budgetCalls != 1 || len(f.starts()) != 0 || len(f.records) != 0 {
			t.Errorf("budget calls = %d, starts = %d, records = %d, want 1, 0, 0", f.budgetCalls, len(f.starts()), len(f.records))
		}
	})
	t.Run("never reached when every candidate is skipped before it", func(t *testing.T) {
		f := newFakes()
		f.pulls = candidates(2)
		f.setting.OnNewPR = false
		f.service(nil).AfterCycle(t.Context(), ws)
		if f.budgetCalls != 0 {
			t.Errorf("budget calls = %d, want 0 (NFR: no cap read without a start)", f.budgetCalls)
		}
	})
}

// FR7, SCR3: the start itself refused by the cap settles the claimed record as skipped_spend_cap and
// the rest of the evaluation does not try again.
func TestAfterCycle_StartRefusedByCap(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(3)
	f.startErr = fmt.Errorf("start: %w", app.ErrCapReached)
	f.service(nil).AfterCycle(t.Context(), ws)
	if len(f.starts()) != 1 {
		t.Errorf("StartAuto calls = %d, want 1 (later candidates are skipped by the cap)", len(f.starts()))
	}
	for pr, o := range f.outcomes() {
		if o != domain.OutcomeSkippedSpendCap {
			t.Errorf("outcome of %s = %s, want skipped_spend_cap", pr, o)
		}
	}
	if r, _ := f.record("pr00", f.pulls[0].HeadSHA); r.RunsStarted != 0 {
		t.Errorf("runs of the refused start = %d, want 0", r.RunsStarted)
	}
}

// AC8: a start error is `failed`, is not retried at the next cycle, and does not stop the others.
func TestAfterCycle_StartErrorFailsOnceWithoutRetry(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(2)
	f.startErr = errBoom
	svc := f.service(nil)
	svc.AfterCycle(t.Context(), ws)
	want := map[string]domain.Outcome{"pr00": domain.OutcomeFailed, "pr01": domain.OutcomeFailed}
	if diff := cmp.Diff(want, f.outcomes()); diff != "" {
		t.Errorf("outcomes mismatch (-want +got):\n%s", diff)
	}
	f.startErr = nil
	svc.AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 2 {
		t.Errorf("StartAuto calls after the next cycle = %d, want 2 (no retry)", got)
	}
}

// EC17: a start that succeeds is `started`, whatever the runs do later.
func TestAfterCycle_StartedEvenIfRunsFailLater(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.startRuns = 1
	f.service(nil).AfterCycle(t.Context(), ws)
	if got := f.outcomes()["pr00"]; got != domain.OutcomeStarted {
		t.Errorf("outcome = %s, want started", got)
	}
}

// FR8, EC8, NFR4: the claim decides; a lost claim starts nothing.
func TestAfterCycle_LostClaimStartsNothing(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	// A rival evaluation claimed the head after our candidate read: the store still lists it.
	f.records[key("pr00", f.pulls[0].HeadSHA)] = domain.Record{
		WorkspaceID: ws, PRID: "pr00", HeadSHA: f.pulls[0].HeadSHA, Event: domain.EventNewPR,
		Outcome: domain.OutcomeStarted, DecidedAt: t0,
	}
	stale := &staleStore{fakeStore: &fakeStore{f}, stale: f.pulls}
	svc := app.NewService(app.Deps{
		Settings: &fakeSettings{f}, Interval: &fakeInterval{f}, Store: stale, Starter: &fakeStarter{f},
		Budget: &fakeBudget{f}, Caps: &fakeCaps{f}, Tokens: &fakeTokens{f}, PollEnabled: true,
		Now: func() time.Time { return t0 },
	})
	svc.AfterCycle(t.Context(), ws)
	if got := f.starts(); len(got) != 0 {
		t.Errorf("StartAuto calls after a lost claim = %v, want none", got)
	}
	if !slices.Contains(f.callLog(), "claim:pr00:started") {
		t.Errorf("calls = %v, want a claim attempt", f.callLog())
	}
}

// staleStore returns a fixed candidate list, as a read taken before a rival's claim would.
type staleStore struct {
	*fakeStore
	stale []domain.Candidate
}

func (s *staleStore) Candidates(context.Context, string, time.Time) ([]domain.Candidate, error) {
	return s.stale, nil
}

// EC7, AC9: a new service over the same store decides nothing again.
func TestAfterCycle_RestartDecidesNothingAgain(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(3)
	f.service(nil).AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 3 {
		t.Fatalf("first run started %d, want 3", got)
	}
	f.service(nil).AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 3 {
		t.Errorf("StartAuto calls after a restart = %d, want still 3", got)
	}
}

// A claim error ends the evaluation without a start.
func TestAfterCycle_ClaimErrorStops(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(3)
	f.claimErr = errBoom
	f.service(nil).AfterCycle(t.Context(), ws)
	if len(f.starts()) != 0 {
		t.Errorf("StartAuto calls after a claim error = %v, want none", f.starts())
	}
	if n := countPrefix(f.callLog(), "claim:"); n != 1 {
		t.Errorf("claim attempts = %d, want 1 (the evaluation stops)", n)
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// AC10, EC13: 5 per evaluation, oldest update first; the rest wait without a record.
func TestAfterCycle_RatePerEvaluation(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(8)
	svc := f.service(nil)
	svc.AfterCycle(t.Context(), ws)
	var gotPRs []string
	for _, s := range f.starts() {
		gotPRs = append(gotPRs, s.PR)
	}
	if want := []string{"pr00", "pr01", "pr02", "pr03", "pr04"}; !slices.Equal(gotPRs, want) {
		t.Errorf("first evaluation started %v, want %v", gotPRs, want)
	}
	if len(f.records) != 5 {
		t.Errorf("records after the first evaluation = %d, want 5 (deferred ones have none)", len(f.records))
	}
	svc.AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 8 {
		t.Errorf("StartAuto calls after the second evaluation = %d, want 8", got)
	}
}

// AC10: 20 starts in the last 60 minutes block new ones until one ages out.
func TestAfterCycle_RatePerHour(t *testing.T) {
	f := newFakes()
	for i := range 20 {
		age := 30 * time.Minute
		if i < 2 {
			age = 59 * time.Minute // ages out first
		}
		f.records[key(fmt.Sprintf("old%02d", i), "h")] = domain.Record{
			PRID: fmt.Sprintf("old%02d", i), HeadSHA: "h", Outcome: domain.OutcomeStarted, DecidedAt: t0.Add(-age),
		}
	}
	f.pulls = candidates(3)
	svc := f.service(nil)
	svc.AfterCycle(t.Context(), ws)
	if len(f.starts()) != 0 {
		t.Fatalf("StartAuto calls at 20 starts/h = %v, want none", f.starts())
	}
	if _, ok := f.record("pr00", f.pulls[0].HeadSHA); ok {
		t.Error("a rate-limited candidate was recorded, want it left for the next cycle")
	}
	f.mu.Lock()
	f.now = t0.Add(2 * time.Minute) // the oldest two records are now outside the window
	f.mu.Unlock()
	svc.AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 2 {
		t.Errorf("StartAuto calls after two records aged out = %d, want 2", got)
	}
}

// A failed start does not count toward the rate (only stored `started` records do).
func TestAfterCycle_FailedStartsDoNotUseTheRate(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(7)
	f.startErr = errBoom
	f.service(nil).AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 7 {
		t.Errorf("StartAuto calls = %d, want 7", got)
	}
}

// NFR5: the loop checks ctx between candidates.
func TestAfterCycle_CancelBetweenCandidates(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(3)
	ctx, cancel := context.WithCancel(t.Context())
	f.onStart = func(context.Context) { cancel() }
	f.service(nil).AfterCycle(ctx, ws)
	if got := len(f.starts()); got != 1 {
		t.Errorf("StartAuto calls after cancel = %d, want 1", got)
	}
	// The started one was still settled on the detached context.
	if got := f.outcomes()["pr00"]; got != domain.OutcomeStarted {
		t.Errorf("outcome of the in-flight start = %q, want started", got)
	}
}

// A4: claim, start and settle run on a context that survives the poller's cancellation, bounded.
func TestAfterCycle_StartRunsOnDetachedBoundedContext(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	ctx, cancel := context.WithCancel(t.Context())
	var cancelledInStart bool
	var bounded bool
	f.onStart = func(sctx context.Context) {
		cancel()
		cancelledInStart = sctx.Err() != nil
		dl, ok := sctx.Deadline()
		bounded = ok && time.Until(dl) <= 3*time.Second
	}
	f.service(nil).AfterCycle(ctx, ws)
	if cancelledInStart {
		t.Error("StartAuto ctx was cancelled with the poller's, want it detached")
	}
	if !bounded {
		t.Error("StartAuto ctx has no deadline within 3 s")
	}
}

// NFR5, AC11: cancelling during a start that hangs returns within 5 s (the 3 s bound).
func TestAfterCycle_ShutdownWithinBudget(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fakes, cancel context.CancelFunc)
	}{
		{"hung start", func(f *fakes, cancel context.CancelFunc) {
			f.pulls = candidates(3)
			f.onStart = func(ctx context.Context) { cancel(); <-ctx.Done() }
		}},
		{"hung candidate read", func(f *fakes, cancel context.CancelFunc) {
			f.pulls = candidates(3)
			f.candidatesGate = make(chan struct{})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFakes()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				tc.setup(f, cancel)
				begin := time.Now()
				done := make(chan struct{})
				go func() { f.service(nil).AfterCycle(ctx, ws); close(done) }()
				time.Sleep(time.Second)
				cancel()
				<-done
				if took := time.Since(begin); took > 5*time.Second {
					t.Errorf("AfterCycle returned %v after start, want within 1s + 5s of cancel", took)
				}
				if took := time.Since(begin); took > 4*time.Second {
					t.Errorf("AfterCycle took %v with cancel at 1s, want the 3 s bound to hold", took)
				}
			})
		})
	}
}

// A5: evaluations of one process never overlap; a waiter gives up when its ctx is cancelled.
func TestAfterCycle_SerialisedPerProcess(t *testing.T) {
	t.Run("never overlap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFakes()
			f.pulls = candidates(1)
			f.candidatesGate = make(chan struct{})
			svc := f.service(nil)
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() { svc.AfterCycle(t.Context(), ws) })
			}
			synctest.Wait()
			f.mu.Lock()
			active := f.active
			f.mu.Unlock()
			if active != 1 {
				t.Errorf("evaluations in flight = %d, want 1", active)
			}
			close(f.candidatesGate)
			wg.Wait()
			if f.peak != 1 {
				t.Errorf("peak concurrent evaluations = %d, want 1", f.peak)
			}
			if got := len(f.starts()); got != 1 {
				t.Errorf("StartAuto calls = %d, want 1", got)
			}
		})
	})
	t.Run("waiter leaves on cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFakes()
			f.candidatesGate = make(chan struct{})
			svc := f.service(nil)
			first, stopFirst := context.WithCancel(t.Context())
			defer stopFirst()
			go svc.AfterCycle(first, ws)
			synctest.Wait()
			waiterCtx, stopWaiter := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { svc.AfterCycle(waiterCtx, ws); close(done) }()
			synctest.Wait()
			stopWaiter()
			<-done
			stopFirst()
			synctest.Wait()
		})
	})
}

// P10: a panic in the evaluation is recovered, logged, and does not wedge the next evaluation.
func TestAfterCycle_PanicRecovered(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.onStart = func(context.Context) { panic("kaboom") }
	log, buf := newLog(slog.LevelInfo)
	svc := f.service(log)
	svc.AfterCycle(t.Context(), ws)
	if !strings.Contains(strings.Join(buf.lines(), "\n"), "panicked") {
		t.Errorf("log = %q, want a panic line", buf.lines())
	}
	f.onStart = nil
	f.pulls = append(f.pulls, candidates(2)[1])
	svc.AfterCycle(t.Context(), ws)
	if got := f.outcomes()["pr01"]; got != domain.OutcomeStarted {
		t.Errorf("outcome of the next evaluation's candidate = %q, want started", got)
	}
}

// EC14: disabling between cycles lets nothing new start.
func TestAfterCycle_DisabledBetweenCycles(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	svc := f.service(nil)
	svc.AfterCycle(t.Context(), ws)
	f.pulls = append(f.pulls, candidates(2)[1])
	f.setting.Enabled = false
	svc.AfterCycle(t.Context(), ws)
	if got := len(f.starts()); got != 1 {
		t.Errorf("StartAuto calls = %d, want 1", got)
	}
}

// NFR3, AC14: one INFO line per decision with the identifying fields and nothing secret.
func TestAfterCycle_OneInfoLinePerDecision(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(4)
	f.pulls[1].Running = true                       // defer: no record, no INFO
	f.pulls[2].LastReviewedSHA = f.pulls[2].HeadSHA // skipped_already_reviewed
	f.pulls[3].HeadSHA = strings.Repeat("ab", 20)   // started, sha cut to 12
	f.startRuns = 2
	log, buf := newLog(slog.LevelInfo)
	f.service(log).AfterCycle(t.Context(), ws)

	lines := buf.lines()
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3 (started, skipped_already_reviewed, started):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		for _, field := range []string{"level=INFO", "workspace_id=w1", "pr_id=", "repo=o/r", "number=", "head_sha=", "event=new_pr", "outcome=", "runs="} {
			if !strings.Contains(l, field) {
				t.Errorf("log line %q lacks %q", l, field)
			}
		}
	}
	if !strings.Contains(lines[2], "head_sha="+strings.Repeat("ab", 6)+" ") || !strings.Contains(lines[2], "outcome=started") || !strings.Contains(lines[2], "runs=2") {
		t.Errorf("log line of the started review = %q, want a 12-char head_sha, outcome=started, runs=2", lines[2])
	}
	if !strings.Contains(lines[1], "outcome=skipped_already_reviewed") || !strings.Contains(lines[1], "runs=0") {
		t.Errorf("log line of the skip = %q", lines[1])
	}
}

func TestAfterCycle_FailedStartLogsOneInfoLine(t *testing.T) {
	f := newFakes()
	f.pulls = candidates(1)
	f.startErr = errBoom
	log, buf := newLog(slog.LevelInfo)
	f.service(log).AfterCycle(t.Context(), ws)
	var info int
	for _, l := range buf.lines() {
		if strings.Contains(l, "level=INFO") {
			info++
			if !strings.Contains(l, "outcome=failed") {
				t.Errorf("INFO line %q, want outcome=failed", l)
			}
		}
	}
	if info != 1 {
		t.Errorf("INFO lines = %d, want 1", info)
	}
}
