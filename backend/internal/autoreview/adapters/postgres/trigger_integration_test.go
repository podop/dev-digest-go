package postgres_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/adapters/postgres"
	"dev-digest-go/backend/internal/autoreview/app"
	"dev-digest-go/backend/internal/autoreview/domain"
)

// Two autoreview Services over ONE database stand for two server processes (S19). The real store
// gives the at-most-once guarantee through the unique (PR, head SHA) claim; the Starter is a fake
// that counts starts. Rate limits are per process (A5), so the tests assert at-most-once, never
// exact totals.

// stubSettings, stubInterval, stubCaps, stubTokens and allowBudget are the ports that need no state.
type stubSettings struct{ s domain.Setting }

func (s stubSettings) AutomaticReviews(context.Context, string) (domain.Setting, error) {
	return s.s, nil
}

type stubInterval struct{}

func (stubInterval) Interval(context.Context, string) (time.Duration, error) {
	return 5 * time.Minute, nil
}

type stubCaps struct{}

func (stubCaps) CapReached(context.Context, string) (bool, error) { return false, nil }

type stubTokens struct{}

func (stubTokens) CodeHostAccess(context.Context, string) (domain.CodeHostAccess, error) {
	return domain.CodeHostAccess{GitHubToken: true, RepoWithToken: true}, nil
}

type allowBudget struct{}

func (allowBudget) Admit(context.Context, string) error { return nil }

// countingStarter records every start as "<pr id>@<head sha>" (the head is read from the database
// when the start happens, as the real reviews start does) and can be told to panic for a PR.
type countingStarter struct {
	f *fixture

	mu      sync.Mutex
	starts  []string
	panicOn map[string]bool // PR id -> panic instead of starting
}

func (s *countingStarter) StartAuto(ctx context.Context, _, prID string, _ []string) (int, error) {
	s.mu.Lock()
	panics := s.panicOn[prID]
	s.mu.Unlock()
	if panics {
		panic("process died mid-evaluation")
	}
	var head string
	if err := s.f.db.WithContext(ctx).Raw(`SELECT head_sha FROM pull_requests WHERE id = ?`, prID).Scan(&head).Error; err != nil {
		return 0, fmt.Errorf("read head of %s: %w", prID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, prID+"@"+head)
	return 1, nil
}

func (s *countingStarter) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.starts...)
}

// process is one "server process": its own store handle, Service and Starter.
type process struct {
	svc     *app.Service
	starter *countingStarter
}

func (f *fixture) process(setting domain.Setting) *process {
	st := &countingStarter{f: f, panicOn: map[string]bool{}}
	svc := app.NewService(app.Deps{
		Settings:    stubSettings{setting},
		Interval:    stubInterval{},
		Store:       postgres.NewStore(f.db),
		Starter:     st,
		Budget:      allowBudget{},
		Caps:        stubCaps{},
		Tokens:      stubTokens{},
		PollEnabled: true,
		Now:         func() time.Time { return decided },
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &process{svc: svc, starter: st}
}

func armedSetting(agents ...string) domain.Setting {
	return domain.Setting{Enabled: true, AgentIDs: agents, OnNewPR: true, OnNewCommits: true, EnabledAt: ptr(enabledAt)}
}

// race runs one AfterCycle per process at the same moment and waits for all of them.
func (f *fixture) race(ws string, procs ...*process) {
	var ready, done sync.WaitGroup
	gate := make(chan struct{})
	for _, p := range procs {
		ready.Add(1)
		done.Go(func() {
			ready.Done()
			<-gate
			p.svc.AfterCycle(context.Background(), ws)
		})
	}
	ready.Wait()
	close(gate)
	done.Wait()
}

// settle races the processes round after round until the store has no candidate left.
func (f *fixture) settle(t *testing.T, procs ...*process) {
	t.Helper()
	for round := 1; round <= 8; round++ {
		f.race(f.ws, procs...)
		if len(f.candidates(t)) == 0 {
			return
		}
	}
	t.Fatalf("candidates left after 8 rounds: %v", ids(f.candidates(t)))
}

// startsOf merges the starts of all processes and counts them per "<pr>@<head>".
func startsOf(procs ...*process) map[string]int {
	out := map[string]int{}
	for _, p := range procs {
		for _, s := range p.starter.taken() {
			out[s]++
		}
	}
	return out
}

type storedRecord struct {
	PRID, HeadSHA, Outcome string
	RunsStarted            int
}

func (f *fixture) records(t *testing.T) map[string]storedRecord {
	t.Helper()
	var rows []storedRecord
	if err := f.db.Raw(`SELECT pr_id, head_sha, outcome, runs_started FROM auto_review_records WHERE workspace_id = ?`, f.ws).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]storedRecord{}
	for _, r := range rows {
		out[r.PRID+"@"+r.HeadSHA] = r
	}
	return out
}

func TestTwoProcesses_EachHeadIsClaimedAndStartedOnce(t *testing.T) { // AC9, EC7, EC8, NFR4
	f := newFixture(t)
	agent := f.agent(t, f.ws, "reviewer", true)
	const n = 8 // more than one evaluation starts (5); two heads each stay under the window (20)
	prs := make([]string, n)
	for i := range prs {
		prs[i] = f.pr(t, prSpec{head: "h1"})
	}
	a, b := f.process(armedSetting(agent)), f.process(armedSetting(agent))

	f.settle(t, a, b)

	want := map[string]int{}
	for _, pr := range prs {
		want[pr+"@h1"] = 1
	}
	if diff := cmp.Diff(want, startsOf(a, b)); diff != "" {
		t.Errorf("starts per (PR, head) after the first heads mismatch (-want +got):\n%s", diff)
	}
	recs := f.records(t)
	if len(recs) != n {
		t.Errorf("stored records = %d, want %d (one per PR head)", len(recs), n)
	}
	for key, r := range recs {
		if r.Outcome != "started" || r.RunsStarted != 1 {
			t.Errorf("record %s = %s/%d runs, want started/1", key, r.Outcome, r.RunsStarted)
		}
	}

	// A new head on every PR: each new head is claimed and started once, the old ones are not
	// touched again.
	f.exec(t, `UPDATE pull_requests SET head_sha = 'h2' WHERE workspace_id = ?`, f.ws)
	f.settle(t, a, b)

	for _, pr := range prs {
		want[pr+"@h2"] = 1
	}
	if diff := cmp.Diff(want, startsOf(a, b)); diff != "" {
		t.Errorf("starts per (PR, head) after new commits mismatch (-want +got):\n%s", diff)
	}
	if got := len(f.records(t)); got != 2*n {
		t.Errorf("stored records = %d, want %d", got, 2*n)
	}

	// Another round changes nothing.
	f.race(f.ws, a, b)
	if diff := cmp.Diff(want, startsOf(a, b)); diff != "" {
		t.Errorf("starts after an idle round mismatch (-want +got):\n%s", diff)
	}
}

func TestTwoProcesses_RestartMidEvaluationNeverStartsTheHeadTwice(t *testing.T) { // EC7, EC8, NFR4
	f := newFixture(t)
	agent := f.agent(t, f.ws, "reviewer", true)
	stuck := f.pr(t, prSpec{head: "h1"})
	others := []string{f.pr(t, prSpec{head: "h1"}), f.pr(t, prSpec{head: "h1"}), f.pr(t, prSpec{head: "h1"})}

	// The first process dies inside the start of `stuck`: its record is claimed (started, 0 runs)
	// and never settled. The panic is recovered by AfterCycle, as a crash would end the process.
	first := f.process(armedSetting(agent))
	first.starter.panicOn[stuck] = true
	// Make `stuck` the first decided PR: it is the one with the oldest update time.
	f.exec(t, `UPDATE pull_requests SET updated_at = ? WHERE id = ?`, before, stuck)
	f.race(f.ws, first)

	recs := f.records(t)
	got, ok := recs[stuck+"@h1"]
	if !ok || got.Outcome != "started" || got.RunsStarted != 0 {
		t.Fatalf("record of the interrupted head = %+v (present %v), want started with 0 runs", got, ok)
	}
	if n := len(first.starter.taken()); n != 0 {
		t.Fatalf("starts before the crash = %d, want 0", n)
	}

	// Restart: two fresh processes. The interrupted head must stay untouched (reviewed zero
	// times rather than twice); every other head is started once.
	second, third := f.process(armedSetting(agent)), f.process(armedSetting(agent))
	f.settle(t, second, third)

	want := map[string]int{}
	for _, pr := range others {
		want[pr+"@h1"] = 1
	}
	if diff := cmp.Diff(want, startsOf(second, third)); diff != "" {
		t.Errorf("starts after the restart mismatch (-want +got):\n%s", diff)
	}
	if got := f.records(t)[stuck+"@h1"]; got.Outcome != "started" || got.RunsStarted != 0 {
		t.Errorf("interrupted record after the restart = %+v, want unchanged started/0", got)
	}

	// A new head of the same PR is a different (PR, head) and is reviewed once.
	f.exec(t, `UPDATE pull_requests SET head_sha = 'h2' WHERE id = ?`, stuck)
	f.settle(t, second, third)
	want[stuck+"@h2"] = 1
	if diff := cmp.Diff(want, startsOf(second, third)); diff != "" {
		t.Errorf("starts after the new head mismatch (-want +got):\n%s", diff)
	}
}

func TestTwoProcesses_RateLimitIsPerProcessAndAtMostOnceStillHolds(t *testing.T) { // A5, NFR4
	f := newFixture(t)
	agent := f.agent(t, f.ws, "reviewer", true)
	const n = 30 // more than the window allows for one process
	prs := make([]string, n)
	for i := range prs {
		prs[i] = f.pr(t, prSpec{head: "h1"})
	}
	a, b := f.process(armedSetting(agent)), f.process(armedSetting(agent))

	for range 6 {
		f.race(f.ws, a, b)
	}

	// The two processes may together exceed 5 per evaluation and 20 per hour a little; what must
	// hold is that no head is started twice and every start has its claimed record.
	starts := startsOf(a, b)
	total := 0
	for key, c := range starts {
		total += c
		if c != 1 {
			t.Errorf("%s started %d times, want at most once", key, c)
		}
	}
	if total == 0 || total > n {
		t.Errorf("total starts = %d, want between 1 and %d", total, n)
	}
	started := 0
	for key, r := range f.records(t) {
		if r.Outcome == "started" {
			started++
			if starts[key] != 1 {
				t.Errorf("record %s is started but the starter ran %d times", key, starts[key])
			}
		}
	}
	if started != total {
		t.Errorf("started records = %d, starts = %d, want equal", started, total)
	}
}
