package domain

import (
	"cmp"
	"slices"
	"time"
)

// Event is why a candidate is looked at: the PR is new to the trigger, or it has a new head.
type Event string

// The events of an auto-review record (spec 8).
const (
	EventNewPR      Event = "new_pr"
	EventNewCommits Event = "new_commits"
)

// Outcome is the stored decision of an auto-review record.
type Outcome string

// The outcomes of an auto-review record (spec 8).
const (
	OutcomeStarted                Outcome = "started"
	OutcomeSkippedTriggerOff      Outcome = "skipped_trigger_off"
	OutcomeSkippedAlreadyReviewed Outcome = "skipped_already_reviewed"
	OutcomeSkippedNoAgents        Outcome = "skipped_no_agents"
	OutcomeSkippedSpendCap        Outcome = "skipped_spend_cap"
	OutcomeFailed                 Outcome = "failed"
)

// Valid reports whether o is one of the stored outcomes.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeStarted, OutcomeSkippedTriggerOff, OutcomeSkippedAlreadyReviewed,
		OutcomeSkippedNoAgents, OutcomeSkippedSpendCap, OutcomeFailed:
		return true
	default:
		return false
	}
}

// Valid reports whether e is one of the stored events.
func (e Event) Valid() bool { return e == EventNewPR || e == EventNewCommits }

// Rate limits per workspace (FR9, decision D7).
const (
	// MaxStartsPerEvaluation is the most automatic starts one evaluation makes.
	MaxStartsPerEvaluation = 5
	// MaxStartsPerWindow is the most automatic starts within RateWindow.
	MaxStartsPerWindow = 20
	// RateWindow is the rolling window of MaxStartsPerWindow.
	RateWindow = 60 * time.Minute
)

// Candidate is a stored pull request the trigger may decide on: open, not a draft, opened at or
// after the setting's EnabledAt, with no record for its current head (see Eligible). The store
// returns candidates ordered by SortCandidates.
type Candidate struct {
	PRID   string
	Repo   string // owner/name, for logs
	Number int
	// HeadSHA is the current head commit.
	HeadSHA string
	// LastReviewedSHA is the head the PR was last reviewed at; empty when never.
	LastReviewedSHA string
	// HasAnyRecord is true when the PR has an auto-review record for some head SHA.
	HasAnyRecord bool
	// Running is true when the PR has a review run in progress, whatever its source.
	Running bool
	// UpdatedAt is GitHub's update time of the PR; nil when unknown (it sorts last).
	UpdatedAt *time.Time
}

// Pull is the stored state of a pull request from which candidacy is decided. The store applies
// Eligible in SQL; this form is the single readable statement of the rule, used by fakes and tests.
type Pull struct {
	Candidate
	Open  bool
	Draft bool
	// OpenedAt is GitHub's opened time; nil when unknown.
	OpenedAt *time.Time
	// HasHeadRecord is true when a record exists for the current head SHA.
	HasHeadRecord bool
}

// Eligible reports whether the stored pull request is a candidate under the setting (FR4): open,
// not a draft, opened on GitHub at or after EnabledAt (D5, D6: PRs opened earlier never trigger,
// not even on new commits), and without a record for its current head. A PR without an opened time
// is never a candidate, and a setting without EnabledAt makes none.
func Eligible(p Pull, s Setting) bool {
	if s.EnabledAt == nil || p.OpenedAt == nil || p.HeadSHA == "" {
		return false
	}
	return p.Open && !p.Draft && !p.OpenedAt.Before(*s.EnabledAt) && !p.HasHeadRecord
}

// SortCandidates orders candidates the way they are decided (FR9, A6): oldest PR update first, PRs
// without an update time last, ties by PR id. It sorts in place.
func SortCandidates(cs []Candidate) {
	slices.SortFunc(cs, func(a, b Candidate) int {
		switch {
		case a.UpdatedAt == nil && b.UpdatedAt != nil:
			return 1
		case a.UpdatedAt != nil && b.UpdatedAt == nil:
			return -1
		case a.UpdatedAt != nil && b.UpdatedAt != nil:
			if c := a.UpdatedAt.Compare(*b.UpdatedAt); c != 0 {
				return c
			}
		}
		return cmp.Compare(a.PRID, b.PRID)
	})
}

// EventFor returns the event of a candidate (FR5): a PR with no record for any head is new, else
// its head is new commits.
func EventFor(c Candidate) Event {
	if c.HasAnyRecord {
		return EventNewCommits
	}
	return EventNewPR
}

// RateLeft returns how many more starts the workspace may make in this evaluation (FR9).
// startedLastHour is the number of `started` records decided within the last RateWindow when the
// evaluation began; startedThisEval is the number of starts this evaluation has made since. Never
// negative.
func RateLeft(startedLastHour, startedThisEval int) int {
	left := min(MaxStartsPerEvaluation-startedThisEval, MaxStartsPerWindow-startedLastHour-startedThisEval)
	return max(left, 0)
}

// RateWindowStart is the beginning of the rolling window that ends at now.
func RateWindowStart(now time.Time) time.Time { return now.Add(-RateWindow) }

// Budget is the answer of the spend-cap check as Decide sees it.
type Budget int

// The budget answers. The zero value means the cap was not asked yet.
const (
	BudgetUnknown Budget = iota
	// BudgetAllowed: new LLM work may start.
	BudgetAllowed
	// BudgetDenied: the cap is reached.
	BudgetDenied
	// BudgetError: the check failed; it counts as not allowed for this evaluation, without a record.
	BudgetError
)

// Action is what the trigger does with a candidate.
type Action int

// The actions of a Decision.
const (
	// ActionDefer: do nothing and write no record; the candidate is looked at again next cycle.
	ActionDefer Action = iota
	// ActionAskBudget: ask the budget once, then Decide again with the answer.
	ActionAskBudget
	// ActionRecord: write a record with Decision.Outcome and start nothing.
	ActionRecord
	// ActionStart: claim a `started` record, then start the review.
	ActionStart
)

// Decision is the result of Decide. Event is set for ActionRecord and ActionStart.
type Decision struct {
	Action  Action
	Event   Event
	Outcome Outcome // ActionRecord only
}

// Decide applies the rules of FR5 and FR6 to one candidate, in order: the trigger condition of its
// event is not ticked -> record skipped_trigger_off; (a) a review is running -> defer; (b) the last
// reviewed SHA equals the head -> record skipped_already_reviewed; (c) agents (the effective agents)
// is empty -> record skipped_no_agents; (d) the budget: unknown -> ask, denied -> record
// skipped_spend_cap, error -> defer; (e) rateLeft is used up -> defer; (f) start. A setting that is
// not armed defers everything.
func Decide(c Candidate, s Setting, agents []string, budget Budget, rateLeft int) Decision {
	if !s.Armed() {
		return Decision{Action: ActionDefer}
	}
	ev := EventFor(c)
	record := func(o Outcome) Decision { return Decision{Action: ActionRecord, Event: ev, Outcome: o} }
	switch {
	case !s.Ticked(ev):
		return record(OutcomeSkippedTriggerOff)
	case c.Running:
		return Decision{Action: ActionDefer, Event: ev}
	case c.LastReviewedSHA != "" && c.LastReviewedSHA == c.HeadSHA:
		return record(OutcomeSkippedAlreadyReviewed)
	case len(agents) == 0:
		return record(OutcomeSkippedNoAgents)
	}
	switch budget {
	case BudgetUnknown:
		return Decision{Action: ActionAskBudget, Event: ev}
	case BudgetDenied:
		return record(OutcomeSkippedSpendCap)
	case BudgetAllowed:
	default: // BudgetError and anything unknown fail closed
		return Decision{Action: ActionDefer, Event: ev}
	}
	if rateLeft <= 0 {
		return Decision{Action: ActionDefer, Event: ev}
	}
	return Decision{Action: ActionStart, Event: ev}
}

// Record is an auto-review record (spec 8): the stored decision for one (PR, head SHA).
type Record struct {
	WorkspaceID string
	PRID        string
	HeadSHA     string
	Event       Event
	Outcome     Outcome
	// RunsStarted is the number of runs the start created; zero unless Outcome is OutcomeStarted.
	RunsStarted int
	// DecidedAt is the trigger clock's time of the decision.
	DecidedAt time.Time
}
