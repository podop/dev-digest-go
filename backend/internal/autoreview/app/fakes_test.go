package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/autoreview/app"
	"dev-digest-go/backend/internal/autoreview/domain"
)

var t0 = time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC)

// fakes is an in-memory world: one store, one starter, one budget, shared by the use cases under
// test. Calls are recorded in order in calls.
type fakes struct {
	mu sync.Mutex

	now      time.Time
	setting  domain.Setting
	settErr  error
	interval time.Duration

	// pulls are the stored candidates, in the order the store returns them; one with a record for
	// its head is not returned (the store's FR4 filter).
	pulls    []domain.Candidate
	enabled  map[string]bool // agent id -> exists and enabled
	records  map[string]domain.Record
	claimErr error

	budgetErr   error
	budgetCalls int

	startErr   error
	startRuns  int
	startCalls []startCall
	// onStart runs inside StartAuto, before it answers.
	onStart func(ctx context.Context)

	capReached bool
	capErr     error
	hasToken   bool
	pollOff    bool // PR_POLL_ENABLED=false
	facts      app.PollFacts

	// candidatesGate, when set, is waited on inside Candidates (honouring ctx).
	candidatesGate chan struct{}
	active, peak   int

	calls []string
}

type startCall struct {
	PR     string
	Agents []string
}

func newFakes() *fakes {
	return &fakes{
		now: t0,
		setting: domain.Setting{
			Enabled: true, AgentIDs: []string{"a1"}, OnNewPR: true, OnNewCommits: true,
			EnabledAt: ptr(t0.Add(-24 * time.Hour)),
		},
		interval:  5 * time.Minute,
		enabled:   map[string]bool{"a1": true},
		records:   map[string]domain.Record{},
		startRuns: 1,
		hasToken:  true,
	}
}

func ptr[T any](v T) *T { return &v }

func (f *fakes) called(name string) { f.calls = append(f.calls, name) }

func key(pr, head string) string { return pr + "@" + head }

// service builds the use cases over f; log is the sink of the service's log lines.
func (f *fakes) service(log *slog.Logger) *app.Service {
	return app.NewService(app.Deps{
		Settings: &fakeSettings{f}, Interval: &fakeInterval{f}, Store: &fakeStore{f},
		Starter: &fakeStarter{f}, Budget: &fakeBudget{f}, Caps: &fakeCaps{f}, Tokens: &fakeTokens{f},
		PollEnabled: !f.pollOff,
		Now:         func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Log:         log,
	})
}

func (f *fakes) record(pr, head string) (domain.Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.records[key(pr, head)]
	return r, ok
}

func (f *fakes) starts() []startCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.startCalls)
}

func (f *fakes) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// candidates makes n candidates with ascending PR ids and update times (oldest first).
func candidates(n int) []domain.Candidate {
	out := make([]domain.Candidate, n)
	for i := range out {
		out[i] = domain.Candidate{
			PRID: fmt.Sprintf("pr%02d", i), Repo: "o/r", Number: i + 1,
			HeadSHA: fmt.Sprintf("%040d", i+1), UpdatedAt: ptr(t0.Add(time.Duration(i-100) * time.Minute)),
		}
	}
	return out
}

type fakeSettings struct{ *fakes }

func (f *fakeSettings) AutomaticReviews(context.Context, string) (domain.Setting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("settings")
	return f.setting, f.settErr
}

type fakeInterval struct{ *fakes }

func (f *fakeInterval) Interval(context.Context, string) (time.Duration, error) {
	return f.interval, nil
}

type fakeStore struct{ *fakes }

func (f *fakeStore) Candidates(ctx context.Context, _ string, _ time.Time) ([]domain.Candidate, error) {
	f.mu.Lock()
	f.called("candidates")
	f.active++
	f.peak = max(f.peak, f.active)
	gate := f.candidatesGate
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Candidate
	for _, c := range f.pulls {
		if _, done := f.records[key(c.PRID, c.HeadSHA)]; done {
			continue
		}
		for _, r := range f.records {
			if r.PRID == c.PRID {
				c.HasAnyRecord = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeStore) EnabledAgents(_ context.Context, _ string, ids []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("enabled_agents")
	var out []string
	for _, id := range ids {
		if f.enabled[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeStore) StartedSince(_ context.Context, _ string, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.records {
		if r.Outcome == domain.OutcomeStarted && !r.DecidedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) Claim(_ context.Context, rec domain.Record) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("claim:" + rec.PRID + ":" + string(rec.Outcome))
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if _, exists := f.records[key(rec.PRID, rec.HeadSHA)]; exists {
		return false, nil
	}
	f.records[key(rec.PRID, rec.HeadSHA)] = rec
	return true, nil
}

func (f *fakeStore) Finish(_ context.Context, _, pr, head string, outcome domain.Outcome, runs int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("finish:" + pr + ":" + string(outcome))
	r := f.records[key(pr, head)]
	r.Outcome, r.RunsStarted = outcome, runs
	f.records[key(pr, head)] = r
	return nil
}

func (f *fakeStore) StatusFacts(context.Context, string) (app.PollFacts, error) {
	return f.facts, nil
}

type fakeStarter struct{ *fakes }

func (f *fakeStarter) StartAuto(ctx context.Context, _, pr string, agents []string) (int, error) {
	f.mu.Lock()
	f.called("start:" + pr)
	f.startCalls = append(f.startCalls, startCall{PR: pr, Agents: slices.Clone(agents)})
	hook, runs, err := f.onStart, f.startRuns, f.startErr
	f.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	return runs, err
}

type fakeBudget struct{ *fakes }

func (f *fakeBudget) Admit(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("budget")
	f.budgetCalls++
	return f.budgetErr
}

type fakeCaps struct{ *fakes }

func (f *fakeCaps) CapReached(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("cap")
	return f.capReached, f.capErr
}

type fakeTokens struct{ *fakes }

func (f *fakeTokens) HasGitHubToken(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("token")
	return f.hasToken, nil
}

// logBuf is a concurrency-safe log sink.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Split(strings.TrimSpace(b.buf.String()), "\n")
}

func newLog(level slog.Level) (*slog.Logger, *logBuf) {
	b := &logBuf{}
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: level})), b
}

var errBoom = errors.New("boom")
