package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	"dev-digest-go/backend/internal/reviewer/domain"
)

// fakeLLM answers each call with fn and records the calls.
type fakeLLM struct {
	fn    func(ctx context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error)
	mu    sync.Mutex
	calls []reviewerapp.Call
}

func (f *fakeLLM) Review(ctx context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	return f.fn(ctx, c)
}

func (f *fakeLLM) called() []reviewerapp.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reviewerapp.Call(nil), f.calls...)
}

type event struct {
	Kind domain.EventKind
	Msg  string
	Data any
}

type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) Emit(kind domain.EventKind, msg string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event{kind, msg, data})
}

func (r *recorder) all() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.events...)
}

func (r *recorder) msgs() []string {
	var out []string
	for _, e := range r.all() {
		out = append(out, string(e.Kind)+": "+e.Msg)
	}
	return out
}

// diffOf builds a parsed diff of files; every file's patch adds lines 1..n.
func diffOf(files map[string]int) domain.Diff {
	var pf []domain.PatchFile
	for path, adds := range files {
		patch := fmt.Sprintf("@@ -0,0 +1,%d @@\n%s", adds, strings.Repeat("+x\n", adds))
		pf = append(pf, domain.PatchFile{Path: path, Patch: strings.TrimSuffix(patch, "\n")})
	}
	return domain.ParseUnifiedDiff(domain.RawDiff(pf))
}

func review(score int, verdict domain.Verdict, findings ...domain.Finding) domain.Review {
	return domain.Review{Verdict: verdict, Summary: fmt.Sprintf("s%d", score), Score: score, Findings: findings}
}

func finding(file string, line int, sev domain.Severity, title string) domain.Finding {
	return domain.Finding{ID: title, Severity: sev, Category: domain.CategoryBug, Title: title, File: file, StartLine: line, EndLine: line}
}

func cost(v float64) *float64 { return &v }

func echoLLM(r domain.Review) *fakeLLM {
	return &fakeLLM{fn: func(context.Context, reviewerapp.Call) (reviewerapp.CallResult, error) {
		return reviewerapp.CallResult{Review: r, Raw: "raw", Usage: domain.Usage{TokensIn: 10, TokensOut: 5, CostUSD: cost(0.5)}}, nil
	}}
}

func TestRun_SinglePass(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 3})
	l := echoLLM(review(80, domain.VerdictComment,
		finding("a.ts", 2, domain.SeverityWarning, "kept"),
		finding("a.ts", 99, domain.SeverityCritical, "off-diff"),
		finding("zzz.ts", 1, domain.SeveritySuggestion, "no-file")))
	rec := &recorder{}
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{
		SystemPrompt: "SYS", Diff: d, Strategy: domain.StrategyAuto, Task: "Review PR #1",
	}, l, rec, reviewerapp.Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantEvents := []string{
		"info: Reviewing 1 changed file(s) in one pass",
		"tool: Reviewing all files in one pass",
		"result: all files: 3 candidate finding(s)",
		"result: Reduced to 3 finding(s); verdict=comment, score=80",
		`info: grounding dropped "off-diff": lines 99-99 do not intersect any diff hunk in 'a.ts'`,
		`info: grounding dropped "no-file": file 'zzz.ts' not present in diff`,
		"result: Citation grounding: 1/3 passed",
	}
	if diff := cmp.Diff(wantEvents, rec.msgs()); diff != "" {
		t.Errorf("Run() events mismatch (-want +got):\n%s", diff)
	}
	if got := rec.all()[1].Data; got != (reviewerapp.FileData{File: "all files"}) {
		t.Errorf("Run() tool event data = %#v, want FileData{all files}", got)
	}
	if out.Mode != domain.ModeSinglePass {
		t.Errorf("Run() mode = %q, want single-pass", out.Mode)
	}
	if got, want := out.Review.Score, 88; got != want { // one WARNING survived: 100-12, not the model's 80
		t.Errorf("Run() score = %d, want %d", got, want)
	}
	if len(out.Review.Findings) != 1 || out.Review.Findings[0].Title != "kept" || out.Review.Findings[0].OutOfScope != nil {
		t.Errorf("Run() findings = %+v, want only the grounded one with nil OutOfScope", out.Review.Findings)
	}
	if out.Grounding != "1/3 passed" || len(out.Dropped) != 2 {
		t.Errorf("Run() grounding = %q (%d dropped), want 1/3 passed (2 dropped)", out.Grounding, len(out.Dropped))
	}
	if diff := cmp.Diff([]string{"all files"}, out.ChunkLabels); diff != "" {
		t.Errorf("Run() chunk labels mismatch (-want +got):\n%s", diff)
	}
	if out.Usage.TokensIn != 10 || out.Usage.TokensOut != 5 || out.Usage.CostUSD == nil || *out.Usage.CostUSD != 0.5 {
		t.Errorf("Run() usage = %+v, want 10/5/0.5", out.Usage)
	}
	calls := l.called()
	if len(calls) != 1 || calls[0].Chunk != (domain.ChunkRef{Index: 0, Total: 1}) {
		t.Fatalf("Run() calls = %+v, want one call with chunk 0/1", calls)
	}
	wantMsgs := domain.AssemblePrompt(domain.PromptParts{System: "SYS", Task: "Review PR #1", Diff: d.Raw})
	if diff := cmp.Diff(wantMsgs.Messages, calls[0].Messages); diff != "" {
		t.Errorf("Run() call messages mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantMsgs.Sections, calls[0].Sections); diff != "" {
		t.Errorf("Run() call sections mismatch (-want +got):\n%s", diff)
	}
	if out.Assembly.User != wantMsgs.Assembly.User || !strings.HasSuffix(out.Assembly.System, domain.InjectionGuard) {
		t.Errorf("Run() assembly = %+v, want the single call's prompt", out.Assembly)
	}
}

func TestRun_MapReduce(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 2, "b.ts": 2, "c.ts": 2})
	l := &fakeLLM{fn: func(_ context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
		user := c.Messages[1].Content
		var path string
		for _, p := range []string{"a.ts", "b.ts", "c.ts"} {
			if strings.Contains(user, "diff --git a/"+p+" b/"+p) {
				path = p
			}
		}
		return reviewerapp.CallResult{
			Review: review(60, domain.VerdictRequestChanges, finding(path, 1, domain.SeverityCritical, "bug in "+path)),
			Raw:    "raw-" + path,
			Usage:  domain.Usage{TokensIn: 1, TokensOut: 1, CostUSD: cost(1)},
		}, nil
	}}
	rec := &recorder{}
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{
		SystemPrompt: "SYS", Diff: d, Strategy: domain.StrategyMapReduce,
	}, l, rec, reviewerapp.Options{MapConcurrency: 1})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{
		"info: Large diff → map-reduce over 3 files",
		"tool: map: reviewing a.ts", "result: a.ts: 1 candidate finding(s)",
		"tool: map: reviewing b.ts", "result: b.ts: 1 candidate finding(s)",
		"tool: map: reviewing c.ts", "result: c.ts: 1 candidate finding(s)",
		"result: Reduced to 3 finding(s); verdict=request_changes, score=60",
		"result: Citation grounding: 3/3 passed",
	}
	if diff := cmp.Diff(want, rec.msgs()); diff != "" {
		t.Errorf("Run() events mismatch (-want +got):\n%s", diff)
	}
	if out.Mode != domain.ModeMapReduce {
		t.Errorf("Run() mode = %q, want map-reduce", out.Mode)
	}
	if out.Raw != "raw-a.ts\n---\nraw-b.ts\n---\nraw-c.ts" {
		t.Errorf("Run() raw = %q, want the outputs joined in chunk order", out.Raw)
	}
	if got := *out.Usage.CostUSD; got != 3 || out.Usage.TokensIn != 3 {
		t.Errorf("Run() usage = %+v (cost %v), want 3 tokens in, cost 3", out.Usage, got)
	}
	if got, want := out.Review.Score, 100-3*35; got != max(want, 0) {
		t.Errorf("Run() score = %d, want %d", got, max(want, 0))
	}
	// The trace assembly of a map-reduce is the whole-diff prompt.
	whole := domain.AssemblePrompt(domain.PromptParts{System: "SYS", Diff: d.Raw}).Assembly
	if diff := cmp.Diff(whole, out.Assembly); diff != "" {
		t.Errorf("Run() assembly mismatch (-want +got):\n%s", diff)
	}
	for i, c := range l.called() {
		if c.Chunk != (domain.ChunkRef{Index: i, Total: 3}) {
			t.Errorf("Run() call %d chunk = %+v, want %d/3", i, c.Chunk, i)
		}
	}
}

func TestRun_AutoStrategyThreshold(t *testing.T) {
	small := diffOf(map[string]int{"a.ts": 100, "b.ts": 100})
	large := diffOf(map[string]int{"a.ts": 300, "b.ts": 101})
	for _, tc := range []struct {
		name string
		diff domain.Diff
		want domain.Mode
	}{
		{"400 changed lines stays single pass", small, domain.ModeSinglePass},
		{"401 changed lines over two files goes map-reduce", large, domain.ModeMapReduce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: tc.diff, Strategy: domain.StrategyAuto},
				echoLLM(review(100, domain.VerdictApprove)), nil, reviewerapp.Options{})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if out.Mode != tc.want {
				t.Errorf("Run() mode = %q, want %q", out.Mode, tc.want)
			}
		})
	}
}

func TestRun_OversizeSplitReportsMapReduce(t *testing.T) {
	// One file with two hunks of about 120,000 units each: two parts.
	big := strings.Repeat("+"+strings.Repeat("y", 99)+"\n", 1_200)
	patch := "@@ -0,0 +1,1200 @@\n" + big + "@@ -2000,0 +2000,1200 @@\n" + strings.TrimSuffix(big, "\n")
	d := domain.ParseUnifiedDiff(domain.RawDiff([]domain.PatchFile{{Path: "big.ts", Patch: patch}}))
	rec := &recorder{}
	l := echoLLM(review(100, domain.VerdictApprove))
	// One chunk at a time: with the default concurrency the two parts' call events race.
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategySinglePass}, l, rec, reviewerapp.Options{MapConcurrency: 1})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out.Mode != domain.ModeMapReduce {
		t.Errorf("Run() mode = %q, want map-reduce for a split single pass", out.Mode)
	}
	if diff := cmp.Diff([]string{"all files (part 1/2)", "all files (part 2/2)"}, out.ChunkLabels); diff != "" {
		t.Errorf("Run() chunk labels mismatch (-want +got):\n%s", diff)
	}
	events := rec.all()
	if got := events[0].Msg; got != "Reviewing 1 changed file(s) in one pass" {
		t.Errorf("Run() first event = %q", got)
	}
	warn := events[1]
	if !strings.HasPrefix(warn.Msg, "warning: all files diff (") || !strings.HasSuffix(warn.Msg, "→ split into 2 part(s)") {
		t.Errorf("Run() oversize event = %q", warn.Msg)
	}
	if want := (reviewerapp.OversizeWarning{Warning: "diff_oversize", File: "all files", Parts: 2}); warn.Data != want {
		t.Errorf("Run() oversize data = %#v, want %#v", warn.Data, want)
	}
	if got := events[2].Msg; got != "map: reviewing all files (part 1/2)" {
		t.Errorf("Run() first call event = %q, want the map: label", got)
	}
}

func TestRun_EmptyDiffReviewsOnceAndDropsEverything(t *testing.T) { // EC4008
	l := echoLLM(review(40, domain.VerdictRequestChanges, finding("a.ts", 1, domain.SeverityCritical, "ghost")))
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: domain.ParseUnifiedDiff(""), Strategy: domain.StrategyAuto}, l, nil, reviewerapp.Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if n := len(l.called()); n != 1 {
		t.Errorf("Run() made %d calls, want 1", n)
	}
	if len(out.Review.Findings) != 0 || out.Review.Score != 100 || out.Grounding != "0/1 passed" {
		t.Errorf("Run() = %d findings, score %d, grounding %q; want 0, 100, 0/1 passed", len(out.Review.Findings), out.Review.Score, out.Grounding)
	}
}

func TestRun_FailureStopsLaterChunks(t *testing.T) { // AC4031 (engine half), EC4019
	d := diffOf(map[string]int{"a.ts": 1, "b.ts": 1, "c.ts": 1})
	boom := errors.New("file b failed")
	l := &fakeLLM{fn: func(_ context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
		if c.Chunk.Index == 1 {
			return reviewerapp.CallResult{}, boom
		}
		return reviewerapp.CallResult{Review: review(100, domain.VerdictApprove)}, nil
	}}
	_, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyMapReduce}, l, nil, reviewerapp.Options{MapConcurrency: 1})
	if !errors.Is(err, boom) {
		t.Fatalf("Run() error = %v, want %v", err, boom)
	}
	var idx []int
	for _, c := range l.called() {
		idx = append(idx, c.Chunk.Index)
	}
	if diff := cmp.Diff([]int{0, 1}, idx); diff != "" {
		t.Errorf("Run() requested chunks mismatch (-want +got):\n%s", diff)
	}
}

func TestRun_FailureAwaitsInFlightChunks(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 1, "b.ts": 1, "c.ts": 1})
	boom := errors.New("file b failed")
	var finished atomic.Int32
	release := make(chan struct{})
	l := &fakeLLM{fn: func(_ context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
		switch c.Chunk.Index {
		case 0:
			<-release
			finished.Add(1)
		case 1:
			defer close(release)
			return reviewerapp.CallResult{}, boom
		}
		return reviewerapp.CallResult{}, nil
	}}
	_, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyMapReduce}, l, nil, reviewerapp.Options{MapConcurrency: 2})
	if !errors.Is(err, boom) {
		t.Fatalf("Run() error = %v, want %v", err, boom)
	}
	if finished.Load() != 1 {
		t.Error("Run() returned before the in-flight chunk finished")
	}
}

func TestRun_CancelledCheckpoint(t *testing.T) { // AC4033 (engine half)
	d := diffOf(map[string]int{"a.ts": 1, "b.ts": 1})
	var cancelled atomic.Bool
	l := &fakeLLM{fn: func(context.Context, reviewerapp.Call) (reviewerapp.CallResult, error) {
		cancelled.Store(true) // the cancel arrives while chunk 1 runs
		return reviewerapp.CallResult{Review: review(100, domain.VerdictApprove)}, nil
	}}
	rec := &recorder{}
	_, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyMapReduce}, l, rec,
		reviewerapp.Options{MapConcurrency: 1, Cancelled: cancelled.Load})
	if !errors.Is(err, reviewerapp.ErrCancelled) {
		t.Fatalf("Run() error = %v, want ErrCancelled", err)
	}
	if n := len(l.called()); n != 1 {
		t.Errorf("Run() requested %d chunks, want 1 (chunk 2 must never be requested)", n)
	}
}

func TestRun_CancelledBeforeFirstCall(t *testing.T) {
	l := echoLLM(review(100, domain.VerdictApprove))
	_, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: diffOf(map[string]int{"a.ts": 1})}, l, nil,
		reviewerapp.Options{Cancelled: func() bool { return true }})
	if !errors.Is(err, reviewerapp.ErrCancelled) || len(l.called()) != 0 {
		t.Errorf("Run() = %v with %d calls, want ErrCancelled and none", err, len(l.called()))
	}
}

func TestRun_ContextErrorComesFromThePort(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l := &fakeLLM{fn: func(ctx context.Context, _ reviewerapp.Call) (reviewerapp.CallResult, error) {
		return reviewerapp.CallResult{}, ctx.Err()
	}}
	_, err := reviewerapp.Run(ctx, reviewerapp.Input{Diff: diffOf(map[string]int{"a.ts": 1})}, l, nil, reviewerapp.Options{})
	if !errors.Is(err, context.Canceled) || errors.Is(err, reviewerapp.ErrCancelled) {
		t.Errorf("Run() error = %v, want the port's context.Canceled, not ErrCancelled", err)
	}
}

// gatedLLM blocks every call until release is closed and records the peak of calls in flight.
func gatedLLM(release <-chan struct{}, entered chan<- struct{}) (*fakeLLM, *atomic.Int32) {
	var inFlight, peak atomic.Int32
	return &fakeLLM{fn: func(context.Context, reviewerapp.Call) (reviewerapp.CallResult, error) {
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		entered <- struct{}{}
		<-release
		inFlight.Add(-1)
		return reviewerapp.CallResult{Review: review(100, domain.VerdictApprove)}, nil
	}}, &peak
}

func TestRun_MapConcurrency(t *testing.T) { // AC4070 (map)
	d := diffOf(map[string]int{"a.ts": 1, "b.ts": 1, "c.ts": 1, "d.ts": 1})
	for _, tc := range []struct {
		name        string
		concurrency int
		wantPeak    int32
	}{
		{"zero means the default of 3", 0, 3},
		{"one", 1, 1},
		{"two", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			entered := make(chan struct{}, 4)
			l, peak := gatedLLM(release, entered)
			done := make(chan error, 1)
			go func() {
				_, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyMapReduce}, l, nil,
					reviewerapp.Options{MapConcurrency: tc.concurrency})
				done <- err
			}()
			for range tc.wantPeak { // the pool fills up to its width
				<-entered
			}
			// With the pool full nothing else may enter; releasing lets the remaining chunks through.
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := peak.Load(); got != tc.wantPeak {
				t.Errorf("Run(MapConcurrency=%d) peak calls in flight = %d, want %d", tc.concurrency, got, tc.wantPeak)
			}
		})
	}
}

func TestRun_UnpricedCallMakesCostUnknown(t *testing.T) {
	d := diffOf(map[string]int{"a.ts": 1, "b.ts": 1})
	l := &fakeLLM{fn: func(_ context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
		u := domain.Usage{TokensIn: 1, CostUSD: cost(1)}
		if c.Chunk.Index == 1 {
			u.CostUSD = nil
		}
		return reviewerapp.CallResult{Review: review(100, domain.VerdictApprove), Usage: u}, nil
	}}
	out, err := reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: d, Strategy: domain.StrategyMapReduce}, l, nil, reviewerapp.Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out.Usage.CostUSD != nil || out.Usage.TokensIn != 2 {
		t.Errorf("Run() usage = %+v, want 2 tokens in and unknown cost", out.Usage)
	}
}

func TestRun_PromptSectionsFollowTheInput(t *testing.T) {
	l := echoLLM(review(100, domain.VerdictApprove))
	_, err := reviewerapp.Run(t.Context(), reviewerapp.Input{
		SystemPrompt: "SYS", Diff: diffOf(map[string]int{"a.ts": 1}), Skills: []string{"### s\n\nbody"},
		Specs: []domain.ProjectDoc{{Path: "docs/a.md", Text: "T"}}, Callers: "c", RepoMap: "m",
		PRDescription: "desc", Task: "task",
	}, l, nil, reviewerapp.Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var names []domain.SectionName
	for _, s := range l.called()[0].Sections {
		names = append(names, s.Name)
	}
	want := []domain.SectionName{
		domain.SectionSystem, domain.SectionInjectionGuard, domain.SectionTask, domain.SectionPRDescription,
		domain.SectionSkills, domain.SectionRepoMap, domain.SectionSpecsRule, domain.SectionSpecs,
		domain.SectionCallers, domain.SectionDiff,
	}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("Run() section names mismatch (-want +got):\n%s", diff)
	}
}
