package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	"dev-digest-go/backend/internal/reviewer/domain"
)

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

func testIntent() *domain.Intent {
	return &domain.Intent{
		Text:        "Add rate limiting.",
		InScope:     []string{"limiter"},
		OutOfScope:  []string{"auth"},
		ChangeType:  strPtr("feature"),
		Confidence:  strPtr("high"),
		DerivedFrom: "explicit",
	}
}

const wantIntentSection = "## PR intent\n" + domain.IntentScopeRule +
	"\n\n<untrusted source=\"pr-intent\">\nAdd rate limiting.\nChange type: feature\nConfidence: high\n" +
	"In scope:\n- limiter\nOut of scope:\n- auth\n</untrusted>"

// AC5034: every chunk of a map-reduce review carries the identical section; the scope event
// appears once, after the grounding result.
func TestRun_IntentReachesEveryChunk(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 2, "b.ts": 2, "c.ts": 2})
	l := &fakeLLM{fn: func(_ context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
		return reviewerapp.CallResult{Review: review(100, domain.VerdictApprove)}, nil
	}}
	rec := &recorder{}
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{
		SystemPrompt: "SYS", Diff: d, Strategy: domain.StrategyMapReduce, Intent: testIntent(),
	}, l, rec, reviewerapp.Options{MapConcurrency: 1})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := l.called()
	if len(calls) != 3 {
		t.Fatalf("Run() made %d calls, want 3", len(calls))
	}
	for i, c := range calls {
		if got := strings.Count(c.Messages[1].Content, wantIntentSection); got != 1 {
			t.Errorf("call %d: %d intent sections, want 1", i, got)
		}
		names := map[domain.SectionName]bool{}
		for _, s := range c.Sections {
			names[s.Name] = true
		}
		if !names[domain.SectionIntentRule] || !names[domain.SectionIntent] {
			t.Errorf("call %d sections lack intent_rule/intent", i)
		}
	}
	scope := 0
	msgs := rec.msgs()
	for i, m := range msgs {
		if strings.HasPrefix(m, "info: scope:") {
			scope++
			if i == 0 || msgs[i-1] != "result: Citation grounding: 0/0 passed" {
				t.Errorf("scope event follows %q, want the grounding result", msgs[i-1])
			}
		}
	}
	if scope != 1 || msgs[len(msgs)-1] != "info: scope: 0 out-of-scope finding(s) kept (0 critical)" {
		t.Errorf("Run() events = %q, want one scope event, last", msgs)
	}
	// The trace assembly is the whole-diff prompt, with the rendered intent text.
	if out.Assembly.Intent == nil || !strings.HasPrefix(*out.Assembly.Intent, "Add rate limiting.\nChange type: feature") {
		t.Errorf("Run() assembly intent = %v, want the rendered text", out.Assembly.Intent)
	}
	if !strings.Contains(out.Assembly.User, wantIntentSection) {
		t.Errorf("Run() assembly user lacks the intent section")
	}
}

// FR5025: findings are kept and their flags become booleans; the event counts them.
func TestRun_IntentScopePolicy(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 6})
	crit := finding("a.ts", 1, domain.SeverityCritical, "crit")
	crit.OutOfScope = boolPtr(true)
	warn := finding("a.ts", 2, domain.SeverityWarning, "warn")
	warn.OutOfScope = boolPtr(true)
	plain := finding("a.ts", 3, domain.SeveritySuggestion, "plain")
	plain.OutOfScope = boolPtr(false)
	unset := finding("a.ts", 4, domain.SeveritySuggestion, "unset")
	l := echoLLM(review(50, domain.VerdictComment, crit, warn, plain, unset))
	rec := &recorder{}
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyAuto, Intent: testIntent()},
		l, rec, reviewerapp.Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var got []bool
	for _, f := range out.Review.Findings {
		if f.OutOfScope == nil {
			t.Fatalf("finding %q: OutOfScope = nil, want a boolean with intent", f.Title)
		}
		got = append(got, *f.OutOfScope)
	}
	if diff := cmp.Diff([]bool{true, true, false, false}, got); diff != "" {
		t.Errorf("Run() flags mismatch (-want +got):\n%s", diff)
	}
	msgs := rec.msgs()
	if last := msgs[len(msgs)-1]; last != "info: scope: 2 out-of-scope finding(s) kept (1 critical)" {
		t.Errorf("Run() last event = %q, want the scope line", last)
	}
	if got, want := out.Review.Score, 47; got != want { // nothing dropped or re-scored by scope
		t.Errorf("Run() score = %d, want %d", got, want)
	}
}

// FR5027: without an intent nothing changes: no section, no event, flags reset to nil.
func TestRun_NoIntentKeepsPhase4Behaviour(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 2})
	f := finding("a.ts", 1, domain.SeverityWarning, "w")
	f.OutOfScope = boolPtr(true)
	l := echoLLM(review(100, domain.VerdictComment, f))
	rec := &recorder{}
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyAuto}, l, rec, reviewerapp.Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out.Review.Findings[0].OutOfScope != nil {
		t.Errorf("Run() OutOfScope = %v, want nil without intent", *out.Review.Findings[0].OutOfScope)
	}
	for _, m := range rec.msgs() {
		if strings.Contains(m, "scope") {
			t.Errorf("Run() emitted %q without an intent", m)
		}
	}
	if strings.Contains(l.called()[0].Messages[1].Content, "## PR intent") || out.Assembly.Intent != nil {
		t.Errorf("Run() prompt or assembly carries an intent without one")
	}
}
