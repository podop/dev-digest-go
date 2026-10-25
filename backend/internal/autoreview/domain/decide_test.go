package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/domain"
)

func at(d time.Duration) *time.Time { v := t0.Add(d); return &v }

func TestEligible(t *testing.T) {
	base := func() domain.Pull {
		return domain.Pull{
			Candidate: domain.Candidate{PRID: "p1", HeadSHA: "h1"},
			Open:      true,
			OpenedAt:  at(time.Minute),
		}
	}
	tests := []struct {
		name string
		mod  func(*domain.Pull, *domain.Setting)
		want bool
	}{
		{"opened after enabled_at", func(*domain.Pull, *domain.Setting) {}, true},
		{"opened exactly at enabled_at", func(p *domain.Pull, _ *domain.Setting) { p.OpenedAt = at(0) }, true},
		{"opened before enabled_at (D5, EC1, EC2)", func(p *domain.Pull, _ *domain.Setting) { p.OpenedAt = at(-time.Second) }, false},
		{"old PR with a new head never triggers (D6)", func(p *domain.Pull, _ *domain.Setting) {
			p.OpenedAt = at(-time.Hour)
			p.HasAnyRecord = true
		}, false},
		{"no opened time", func(p *domain.Pull, _ *domain.Setting) { p.OpenedAt = nil }, false},
		{"draft (EC3)", func(p *domain.Pull, _ *domain.Setting) { p.Draft = true }, false},
		{"closed or merged (EC4)", func(p *domain.Pull, _ *domain.Setting) { p.Open = false }, false},
		{"record for the head exists", func(p *domain.Pull, _ *domain.Setting) { p.HasHeadRecord = true }, false},
		{"record only for an older head", func(p *domain.Pull, _ *domain.Setting) { p.HasAnyRecord = true }, true},
		{"no head sha", func(p *domain.Pull, _ *domain.Setting) { p.HeadSHA = "" }, false},
		{"setting without enabled_at", func(_ *domain.Pull, s *domain.Setting) { s.EnabledAt = nil }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, s := base(), armed()
			tc.mod(&p, &s)
			if got := domain.Eligible(p, s); got != tc.want {
				t.Errorf("Eligible(%+v) = %v, want %v", p, got, tc.want)
			}
		})
	}
}

func TestSortCandidates(t *testing.T) {
	cs := []domain.Candidate{
		{PRID: "d", UpdatedAt: nil},
		{PRID: "c", UpdatedAt: at(2 * time.Minute)},
		{PRID: "b", UpdatedAt: at(time.Minute)},
		{PRID: "a", UpdatedAt: at(2 * time.Minute)},
		{PRID: "0", UpdatedAt: nil},
	}
	domain.SortCandidates(cs)
	var got []string
	for _, c := range cs {
		got = append(got, c.PRID)
	}
	want := []string{"b", "a", "c", "0", "d"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SortCandidates order mismatch (-want +got):\n%s", diff)
	}
}

func TestEventFor(t *testing.T) {
	if got := domain.EventFor(domain.Candidate{}); got != domain.EventNewPR {
		t.Errorf("EventFor(no record) = %q, want %q", got, domain.EventNewPR)
	}
	if got := domain.EventFor(domain.Candidate{HasAnyRecord: true}); got != domain.EventNewCommits {
		t.Errorf("EventFor(has a record) = %q, want %q", got, domain.EventNewCommits)
	}
}

func TestRateLeft(t *testing.T) {
	tests := []struct {
		name               string
		lastHour, thisEval int
		want               int
	}{
		{"fresh", 0, 0, 5},
		{"two started this evaluation", 0, 2, 3},
		{"evaluation limit", 0, 5, 0},
		{"hour limit nearly used", 18, 0, 2},
		{"hour limit binds before the evaluation limit", 18, 1, 1},
		{"hour limit used", 20, 0, 0},
		{"over the hour limit never negative", 25, 0, 0},
		{"over the evaluation limit never negative", 0, 9, 0},
		{"exactly at the window edge", 19, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.RateLeft(tc.lastHour, tc.thisEval); got != tc.want {
				t.Errorf("RateLeft(%d, %d) = %d, want %d", tc.lastHour, tc.thisEval, got, tc.want)
			}
		})
	}
}

func TestRateWindowStart(t *testing.T) {
	if got, want := domain.RateWindowStart(t0), t0.Add(-60*time.Minute); !got.Equal(want) {
		t.Errorf("RateWindowStart(%v) = %v, want %v", t0, got, want)
	}
}

func TestDecide(t *testing.T) {
	agents := []string{"a1"}
	cand := func() domain.Candidate {
		return domain.Candidate{PRID: "p1", HeadSHA: "h2", LastReviewedSHA: "h1"}
	}
	type in struct {
		c      domain.Candidate
		s      domain.Setting
		agents []string
		budget domain.Budget
		rate   int
	}
	base := func() in { return in{c: cand(), s: armed(), agents: agents, budget: domain.BudgetAllowed, rate: 5} }
	rec := func(ev domain.Event, o domain.Outcome) domain.Decision {
		return domain.Decision{Action: domain.ActionRecord, Event: ev, Outcome: o}
	}
	deferred := func(ev domain.Event) domain.Decision { return domain.Decision{Action: domain.ActionDefer, Event: ev} }

	tests := []struct {
		name string
		mod  func(*in)
		want domain.Decision
	}{
		{"new PR starts", func(*in) {}, domain.Decision{Action: domain.ActionStart, Event: domain.EventNewPR}},
		{"fork PR is recorded (D13)", func(i *in) { i.c.IsFork = true },
			rec(domain.EventNewPR, domain.OutcomeSkippedFork)},
		{"fork PR with new commits is recorded", func(i *in) {
			i.c.IsFork = true
			i.c.HasAnyRecord = true
		}, rec(domain.EventNewCommits, domain.OutcomeSkippedFork)},
		{"fork wins over every other rule", func(i *in) {
			i.c.IsFork = true
			i.c.Running = true
			i.c.LastReviewedSHA = i.c.HeadSHA
			i.s.OnNewPR = false
			i.agents = nil
			i.budget = domain.BudgetDenied
			i.rate = 0
		}, rec(domain.EventNewPR, domain.OutcomeSkippedFork)},
		{"fork is recorded before the budget is asked", func(i *in) {
			i.c.IsFork = true
			i.budget = domain.BudgetUnknown
		}, rec(domain.EventNewPR, domain.OutcomeSkippedFork)},
		{"unknown head repository defers without a record", func(i *in) { i.c.HeadRepoUnknown = true },
			deferred(domain.EventNewPR)},
		{"unknown head repository wins over fork", func(i *in) {
			i.c.HeadRepoUnknown = true
			i.c.IsFork = true
		}, deferred(domain.EventNewPR)},
		{"fork PR with the setting not armed still defers", func(i *in) {
			i.c.IsFork = true
			i.s.Enabled = false
		}, domain.Decision{Action: domain.ActionDefer}},
		{"new commits start", func(i *in) { i.c.HasAnyRecord = true },
			domain.Decision{Action: domain.ActionStart, Event: domain.EventNewCommits}},
		{"new PR with on_new_pr off is recorded", func(i *in) { i.s.OnNewPR = false },
			rec(domain.EventNewPR, domain.OutcomeSkippedTriggerOff)},
		{"new commits with on_new_commits off is recorded", func(i *in) {
			i.s.OnNewCommits = false
			i.c.HasAnyRecord = true
		}, rec(domain.EventNewCommits, domain.OutcomeSkippedTriggerOff)},
		{"trigger off wins over a running review", func(i *in) {
			i.s.OnNewPR = false
			i.c.Running = true
		}, rec(domain.EventNewPR, domain.OutcomeSkippedTriggerOff)},
		{"running review defers (a)", func(i *in) { i.c.Running = true }, deferred(domain.EventNewPR)},
		{"running wins over already reviewed", func(i *in) {
			i.c.Running = true
			i.c.LastReviewedSHA = i.c.HeadSHA
		}, deferred(domain.EventNewPR)},
		{"already reviewed (b)", func(i *in) { i.c.LastReviewedSHA = i.c.HeadSHA },
			rec(domain.EventNewPR, domain.OutcomeSkippedAlreadyReviewed)},
		{"never reviewed is not already reviewed", func(i *in) { i.c.LastReviewedSHA = "" }, domain.Decision{Action: domain.ActionStart, Event: domain.EventNewPR}},
		{"no effective agents (c)", func(i *in) { i.agents = nil }, rec(domain.EventNewPR, domain.OutcomeSkippedNoAgents)},
		{"no agents wins over the budget and rate", func(i *in) {
			i.agents = nil
			i.budget = domain.BudgetDenied
			i.rate = 0
		}, rec(domain.EventNewPR, domain.OutcomeSkippedNoAgents)},
		{"budget not asked yet (d)", func(i *in) { i.budget = domain.BudgetUnknown },
			domain.Decision{Action: domain.ActionAskBudget, Event: domain.EventNewPR}},
		{"budget not asked yet, rate used up: still asks (SCR2)", func(i *in) {
			i.budget = domain.BudgetUnknown
			i.rate = 0
		}, domain.Decision{Action: domain.ActionAskBudget, Event: domain.EventNewPR}},
		{"cap reached (EC11)", func(i *in) { i.budget = domain.BudgetDenied },
			rec(domain.EventNewPR, domain.OutcomeSkippedSpendCap)},
		{"cap reached wins over rate used up", func(i *in) {
			i.budget = domain.BudgetDenied
			i.rate = 0
		}, rec(domain.EventNewPR, domain.OutcomeSkippedSpendCap)},
		{"budget check failed defers without a record", func(i *in) { i.budget = domain.BudgetError }, deferred(domain.EventNewPR)},
		{"unknown budget value fails closed", func(i *in) { i.budget = domain.Budget(99) }, deferred(domain.EventNewPR)},
		{"rate used up defers (e)", func(i *in) { i.rate = 0 }, deferred(domain.EventNewPR)},
		{"last start of the rate", func(i *in) { i.rate = 1 }, domain.Decision{Action: domain.ActionStart, Event: domain.EventNewPR}},
		{"not armed defers everything", func(i *in) { i.s.Enabled = false }, domain.Decision{Action: domain.ActionDefer}},
		{"no triggers defers everything", func(i *in) { i.s.OnNewPR, i.s.OnNewCommits = false, false }, domain.Decision{Action: domain.ActionDefer}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i := base()
			tc.mod(&i)
			got := domain.Decide(i.c, i.s, i.agents, i.budget, i.rate)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Decide(%+v, budget %d, rate %d) mismatch (-want +got):\n%s", i.c, i.budget, i.rate, diff)
			}
		})
	}
}

func TestOutcomeAndEventValid(t *testing.T) {
	for _, o := range []domain.Outcome{domain.OutcomeStarted, domain.OutcomeSkippedTriggerOff,
		domain.OutcomeSkippedAlreadyReviewed, domain.OutcomeSkippedNoAgents, domain.OutcomeSkippedSpendCap, domain.OutcomeSkippedFork, domain.OutcomeFailed} {
		if !o.Valid() {
			t.Errorf("Outcome(%q).Valid() = false, want true", o)
		}
	}
	if domain.Outcome("nope").Valid() {
		t.Error(`Outcome("nope").Valid() = true, want false`)
	}
	if !domain.EventNewPR.Valid() || !domain.EventNewCommits.Valid() || domain.Event("x").Valid() {
		t.Error("Event.Valid() mismatch for new_pr / new_commits / x")
	}
}
