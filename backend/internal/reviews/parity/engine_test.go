package parity_test

// S23 (AC4041, NFR4005): the review engine (reviewerapp.Run, S16) against engine.json — the Node
// reviewPullRequest with a scripted LLM — and the ordered-map semantics of reduce.json. This file
// is the only parity file that needs the engine package.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

type goldenEvent struct {
	Kind string          `json:"kind"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type goldenStep struct {
	Review *wireReview `json:"review"`
	Raw    string      `json:"raw"`
	Error  *string     `json:"error"`
	Usage  []struct {
		TokensIn  int      `json:"tokensIn"`
		TokensOut int      `json:"tokensOut"`
		CostUSD   *float64 `json:"costUsd"`
	} `json:"usage"`
}

type goldenMessage struct {
	Role    string `json:"role"`
	Content text   `json:"content"`
}

type goldenSection struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Role      string `json:"role"`
	Trust     string `json:"trust"`
	Chars     int    `json:"chars"`
	Tokens    int    `json:"tokens"`
	Items     *int   `json:"items"`
	Truncated *bool  `json:"truncated"`
}

type engineCase struct {
	Name  string `json:"name"`
	Input struct {
		SystemPrompt  string   `json:"systemPrompt"`
		Strategy      string   `json:"strategy"`
		Concurrency   int      `json:"concurrency"`
		CancelAtPoll  int      `json:"cancelAtPoll"`
		Task          string   `json:"task"`
		PRDescription string   `json:"prDescription"`
		Skills        []string `json:"skills"`
		RepoMap       string   `json:"repoMap"`
		Callers       string   `json:"callers"`
		Specs         []struct {
			Path string `json:"path"`
			Text string `json:"text"`
		} `json:"specs"`
	} `json:"input"`
	DiffRaw   text       `json:"diffRaw"`
	DiffSynth *synthSpec `json:"diffSynth"`
	Script    struct {
		Chunks  map[string]goldenStep `json:"chunks"`
		Default *goldenStep           `json:"default"`
	} `json:"script"`
	Calls []struct {
		ChunkIndex int             `json:"chunkIndex"`
		Messages   []goldenMessage `json:"messages"`
	} `json:"calls"`
	PromptEvents []struct {
		ChunkIndex int             `json:"chunkIndex"`
		ChunkCount int             `json:"chunkCount"`
		ChunkLabel string          `json:"chunkLabel"`
		Mode       string          `json:"mode"`
		Sections   []goldenSection `json:"sections"`
	} `json:"promptEvents"`
	Events      []goldenEvent `json:"events"`
	EventGroups struct {
		Pre    []goldenEvent `json:"pre"`
		Chunks []struct {
			Label  string       `json:"label"`
			Tool   goldenEvent  `json:"tool"`
			Result *goldenEvent `json:"result"`
		} `json:"chunks"`
		Post []goldenEvent `json:"post"`
	} `json:"eventGroups"`
	PollsMade int     `json:"pollsMade"`
	Error     *string `json:"error"`
	Outcome   *struct {
		Review    wireReview `json:"review"`
		Grounding string     `json:"grounding"`
		Dropped   []struct {
			Finding wireFinding `json:"finding"`
			Reason  string      `json:"reason"`
		} `json:"dropped"`
		Mode     string `json:"mode"`
		Assembly struct {
			System        string `json:"system"`
			Skills        *text  `json:"skills"`
			Memory        *text  `json:"memory"`
			Specs         *text  `json:"specs"`
			Callers       *text  `json:"callers"`
			RepoMap       *text  `json:"repo_map"`
			PRDescription *text  `json:"pr_description"`
			Intent        *text  `json:"intent"`
			User          text   `json:"user"`
		} `json:"assembly"`
		Chunks []struct {
			Label string `json:"label"`
		} `json:"chunks"`
		TokensIn  int      `json:"tokensIn"`
		TokensOut int      `json:"tokensOut"`
		CostUSD   *float64 `json:"costUsd"`
		Raw       text     `json:"raw"`
	} `json:"outcome"`
}

// recordedEvent is one engine event: kind, message and the data marshalled as the run bus does.
type recordedEvent struct {
	Kind string
	Msg  string
	Data string // compact JSON, "" = none
}

type eventLog struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (l *eventLog) Emit(kind reviewer.EventKind, msg string, data any) {
	e := recordedEvent{Kind: string(kind), Msg: msg}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			panic(err)
		}
		e.Data = string(b)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) all() []recordedEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// compact is the golden's data with insignificant whitespace removed, key order kept: the order
// of the keys is the order on the wire.
func compact(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return b.String()
}

func goldenEvents(t testing.TB, in []goldenEvent) []recordedEvent {
	t.Helper()
	out := make([]recordedEvent, len(in))
	for i, e := range in {
		out[i] = recordedEvent{Kind: e.Kind, Msg: e.Msg, Data: compact(t, e.Data)}
	}
	return out
}

// groupEvents splits events like the generator does: pre = before the first chunk tool event,
// chunks = tool/result pairs by label, post = the rest.
func groupEvents(events []recordedEvent) (pre []recordedEvent, chunks map[string][2]*recordedEvent, order []string, post []recordedEvent) {
	chunks = map[string][2]*recordedEvent{}
	for i := range events {
		e := events[i]
		if e.Kind == "tool" && e.Data != "" {
			var d struct {
				File *string `json:"file"`
			}
			if json.Unmarshal([]byte(e.Data), &d) == nil && d.File != nil {
				pair := chunks[*d.File]
				pair[0] = &events[i]
				chunks[*d.File] = pair
				order = append(order, *d.File)
				continue
			}
		}
		if e.Kind == "result" {
			matched := false
			for label, pair := range chunks {
				if e.Msg == fmt.Sprintf("%s: %s", label, candidateCount(e.Msg)) && pair[0] != nil {
					pair[1] = &events[i]
					chunks[label] = pair
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		if len(order) == 0 {
			pre = append(pre, e)
		} else {
			post = append(post, e)
		}
	}
	return pre, chunks, order, post
}

// candidateCount returns the "<n> candidate finding(s)" tail of a chunk result message, "" otherwise.
func candidateCount(msg string) string {
	const tail = " candidate finding(s)"
	if len(msg) < len(tail) || msg[len(msg)-len(tail):] != tail {
		return ""
	}
	i := len(msg) - len(tail)
	j := i
	for j > 0 && msg[j-1] >= '0' && msg[j-1] <= '9' {
		j--
	}
	if j == i {
		return ""
	}
	return msg[j:]
}

// scriptedLLM answers each call from the case script, indexed by chunk. All calls of the first
// wave start before any returns, so the in-flight semantics of Node (every call of the wave is
// running when one fails) hold regardless of goroutine scheduling.
type scriptedLLM struct {
	t       testing.TB
	script  *engineCase
	barrier int // calls that must be in flight before the first returns

	mu      sync.Mutex
	calls   map[int]reviewerapp.Call
	started int
	release chan struct{}
	closed  bool
}

func newScriptedLLM(t testing.TB, c *engineCase, firstWave int) *scriptedLLM {
	return &scriptedLLM{t: t, script: c, barrier: firstWave, calls: map[int]reviewerapp.Call{}, release: make(chan struct{})}
}

func (l *scriptedLLM) Review(ctx context.Context, call reviewerapp.Call) (reviewerapp.CallResult, error) {
	l.mu.Lock()
	l.calls[call.Chunk.Index] = call
	l.started++
	if l.started >= l.barrier && !l.closed {
		l.closed = true
		close(l.release)
	}
	l.mu.Unlock()

	select {
	case <-l.release:
	case <-ctx.Done():
		return reviewerapp.CallResult{}, ctx.Err()
	case <-time.After(10 * time.Second):
		return reviewerapp.CallResult{}, fmt.Errorf("only %d of %d first-wave calls started", l.started, l.barrier)
	}

	step, ok := l.script.Script.Chunks[fmt.Sprint(call.Chunk.Index)]
	if !ok {
		if l.script.Script.Default == nil {
			return reviewerapp.CallResult{}, fmt.Errorf("no script step for chunk %d", call.Chunk.Index)
		}
		step = *l.script.Script.Default
	}
	usage := reviewer.Usage{}
	zero := 0.0
	usage.CostUSD = &zero
	for _, u := range step.Usage {
		usage.TokensIn += u.TokensIn
		usage.TokensOut += u.TokensOut
		if usage.CostUSD != nil && u.CostUSD != nil {
			sum := *usage.CostUSD + *u.CostUSD
			usage.CostUSD = &sum
		} else {
			usage.CostUSD = nil
		}
	}
	if step.Error != nil {
		return reviewerapp.CallResult{}, errors.New(*step.Error)
	}
	return reviewerapp.CallResult{Review: step.Review.domain(), Raw: step.Raw, Usage: usage}, nil
}

func TestEngineParity_Run(t *testing.T) {
	var g struct {
		Cases []engineCase `json:"cases"`
	}
	loadGolden(t, "engine", &g)
	if len(g.Cases) == 0 {
		t.Fatal("engine.json has no cases")
	}
	for i := range g.Cases {
		tc := &g.Cases[i]
		t.Run(tc.Name, func(t *testing.T) {
			raw := rawOf(t, tc.DiffRaw, tc.DiffSynth, 0)
			diff := reviewer.ParseUnifiedDiff(raw)
			in := reviewerapp.Input{
				SystemPrompt: tc.Input.SystemPrompt, Diff: diff, Strategy: reviewer.Strategy(tc.Input.Strategy),
				Skills: tc.Input.Skills, Callers: tc.Input.Callers, RepoMap: tc.Input.RepoMap,
				PRDescription: tc.Input.PRDescription, Task: tc.Input.Task,
			}
			for _, s := range tc.Input.Specs {
				in.Specs = append(in.Specs, reviewer.ProjectDoc{Path: s.Path, Text: s.Text})
			}

			// The number of calls in the first wave: the chunks (one per call) up to the concurrency.
			concurrency := tc.Input.Concurrency
			firstWave := len(tc.PromptEvents)
			if wave := concurrency; wave == 0 {
				firstWave = min(firstWave, reviewer.DefaultMapConcurrency)
			} else {
				firstWave = min(firstWave, wave)
			}
			if firstWave < 1 {
				firstWave = 1
			}
			// A run that is cancelled before its first call or whose calls Node never made has no wave.
			if len(tc.PromptEvents) == 0 {
				firstWave = 1 << 30
			}
			llm := newScriptedLLM(t, tc, firstWave)
			events := &eventLog{}
			var polls atomic.Int64
			opts := reviewerapp.Options{MapConcurrency: concurrency}
			if tc.Input.CancelAtPoll > 0 || tc.PollsMade > 0 {
				cancelAt := int64(tc.Input.CancelAtPoll)
				opts.Cancelled = func() bool {
					n := polls.Add(1)
					return cancelAt > 0 && n >= cancelAt
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			out, err := reviewerapp.Run(ctx, in, llm, events, opts)

			// Prompts: byte-identical messages, per chunk index.
			if len(llm.calls) != len(tc.Calls) {
				t.Errorf("the engine made %d calls, Node made %d", len(llm.calls), len(tc.Calls))
			}
			for _, want := range tc.Calls {
				got, ok := llm.calls[want.ChunkIndex]
				if !ok {
					t.Errorf("no call for chunk %d", want.ChunkIndex)
					continue
				}
				if len(got.Messages) != len(want.Messages) {
					t.Errorf("chunk %d has %d messages, want %d", want.ChunkIndex, len(got.Messages), len(want.Messages))
					continue
				}
				for k, m := range want.Messages {
					if string(got.Messages[k].Role) != m.Role {
						t.Errorf("chunk %d message %d role = %q, want %q", want.ChunkIndex, k, got.Messages[k].Role, m.Role)
					}
					checkText(t, fmt.Sprintf("chunk %d message %d content", want.ChunkIndex, k), m.Content, got.Messages[k].Content)
				}
			}
			// Section metadata and the chunk position of every call.
			for _, pe := range tc.PromptEvents {
				got, ok := llm.calls[pe.ChunkIndex]
				if !ok {
					continue
				}
				if got.Chunk.Index != pe.ChunkIndex || got.Chunk.Total != pe.ChunkCount {
					t.Errorf("chunk %d position = %d/%d, want %d/%d", pe.ChunkIndex, got.Chunk.Index, got.Chunk.Total, pe.ChunkIndex, pe.ChunkCount)
				}
				var wantSections []reviewer.SectionMeta
				for _, s := range pe.Sections {
					wantSections = append(wantSections, reviewer.SectionMeta{
						Name: reviewer.SectionName(s.Name), Source: reviewer.SectionSource(s.Source), Role: reviewer.Role(s.Role),
						Trust: reviewer.Trust(s.Trust), Chars: s.Chars, Tokens: s.Tokens, Items: s.Items, Truncated: s.Truncated,
					})
				}
				if diff := cmp.Diff(wantSections, got.Sections); diff != "" {
					t.Errorf("chunk %d section metadata mismatch (-want +got):\n%s", pe.ChunkIndex, diff)
				}
			}
			if got := int(polls.Load()); opts.Cancelled != nil && got != tc.PollsMade {
				t.Errorf("the engine polled for cancellation %d times, Node %d", got, tc.PollsMade)
			}

			// Events: ordered when the run is sequential; per-chunk groups when calls overlap.
			got := events.all()
			ordered := concurrency == 1 || len(tc.PromptEvents) <= 1
			if ordered {
				if diff := cmp.Diff(goldenEvents(t, tc.Events), got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("events mismatch (-want +got):\n%s", diff)
				}
			} else {
				pre, chunks, order, post := groupEvents(got)
				if diff := cmp.Diff(goldenEvents(t, tc.EventGroups.Pre), pre, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("events before the first chunk mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(goldenEvents(t, tc.EventGroups.Post), post, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("events after the chunks mismatch (-want +got):\n%s", diff)
				}
				var wantLabels []string
				for _, c := range tc.EventGroups.Chunks {
					wantLabels = append(wantLabels, c.Label)
				}
				if diff := cmp.Diff(slices.Sorted(slices.Values(wantLabels)), slices.Sorted(slices.Values(order))); diff != "" {
					t.Errorf("chunk labels in the events mismatch (-want +got):\n%s", diff)
				}
				for _, c := range tc.EventGroups.Chunks {
					pair, ok := chunks[c.Label]
					if !ok {
						continue
					}
					if diff := cmp.Diff(goldenEvents(t, []goldenEvent{c.Tool})[0], *pair[0]); diff != "" {
						t.Errorf("tool event of %q mismatch (-want +got):\n%s", c.Label, diff)
					}
					switch {
					case c.Result == nil && pair[1] != nil:
						t.Errorf("chunk %q has a result event %q, Node has none", c.Label, pair[1].Msg)
					case c.Result != nil && pair[1] == nil:
						t.Errorf("chunk %q has no result event, want %q", c.Label, c.Result.Msg)
					case c.Result != nil:
						if diff := cmp.Diff(goldenEvents(t, []goldenEvent{*c.Result})[0], *pair[1]); diff != "" {
							t.Errorf("result event of %q mismatch (-want +got):\n%s", c.Label, diff)
						}
					}
				}
			}

			if tc.Outcome == nil {
				if err == nil || err.Error() != *tc.Error {
					t.Fatalf("Run() error = %v, want %q", err, *tc.Error)
				}
				if tc.Name == "cancelled_between_chunks" || tc.Name == "cancelled_before_the_first_call" {
					if !errors.Is(err, reviewerapp.ErrCancelled) {
						t.Errorf("Run() error = %v, want ErrCancelled", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v, want an outcome", err)
			}
			want := tc.Outcome
			if diff := cmp.Diff(want.Review.domain(), out.Review, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("review mismatch (-want +got):\n%s", diff)
			}
			if out.Grounding != want.Grounding {
				t.Errorf("grounding = %q, want %q", out.Grounding, want.Grounding)
			}
			var wantDropped []reviewer.Dropped
			for _, d := range want.Dropped {
				wantDropped = append(wantDropped, reviewer.Dropped{Finding: d.Finding.domain(), Reason: d.Reason})
			}
			if diff := cmp.Diff(wantDropped, out.Dropped, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("dropped findings mismatch (-want +got):\n%s", diff)
			}
			if string(out.Mode) != want.Mode {
				t.Errorf("mode = %q, want %q", out.Mode, want.Mode)
			}
			for _, pe := range tc.PromptEvents {
				if pe.Mode != want.Mode {
					t.Errorf("golden prompt event of chunk %d has mode %q, outcome %q", pe.ChunkIndex, pe.Mode, want.Mode)
				}
				if pe.ChunkIndex < len(out.ChunkLabels) && out.ChunkLabels[pe.ChunkIndex] != pe.ChunkLabel {
					t.Errorf("chunk %d label = %q, want %q", pe.ChunkIndex, out.ChunkLabels[pe.ChunkIndex], pe.ChunkLabel)
				}
			}
			var wantLabels []string
			for _, c := range want.Chunks {
				wantLabels = append(wantLabels, c.Label)
			}
			if diff := cmp.Diff(wantLabels, out.ChunkLabels, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("chunk labels mismatch (-want +got):\n%s", diff)
			}
			if out.Usage.TokensIn != want.TokensIn || out.Usage.TokensOut != want.TokensOut {
				t.Errorf("tokens = %d in / %d out, want %d / %d", out.Usage.TokensIn, out.Usage.TokensOut, want.TokensIn, want.TokensOut)
			}
			if diff := cmp.Diff(want.CostUSD, out.Usage.CostUSD); diff != "" {
				t.Errorf("cost mismatch (-want +got):\n%s", diff)
			}
			checkText(t, "raw", want.Raw, out.Raw)

			// The trace's prompt assembly. Memory and intent are constants of Phase 4 (null).
			a := want.Assembly
			if a.Memory != nil || a.Intent != nil {
				t.Errorf("golden assembly has memory/intent %v/%v, Phase 4 has none", a.Memory, a.Intent)
			}
			if out.Assembly.System != a.System {
				t.Errorf("assembly.system = %q, want %q", out.Assembly.System, a.System)
			}
			checkText(t, "assembly.user", a.User, out.Assembly.User)
			optional := func(name string, want *text, got *string) {
				// Node keeps an empty callers/repo map as "", Go reads "" as no section: nil.
				if want != nil && want.b == nil && want.s == "" {
					want = nil
				}
				switch {
				case want == nil && got != nil:
					t.Errorf("assembly.%s = %s, want null", name, describeGot(*got))
				case want != nil && got == nil:
					t.Errorf("assembly.%s = null, want %s", name, want.describe())
				case want != nil:
					checkText(t, "assembly."+name, *want, *got)
				}
			}
			optional("skills", a.Skills, out.Assembly.Skills)
			optional("specs", a.Specs, out.Assembly.Specs)
			optional("callers", a.Callers, out.Assembly.Callers)
			optional("repo_map", a.RepoMap, out.Assembly.RepoMap)
			optional("pr_description", a.PRDescription, out.Assembly.PRDescription)
		})
	}
}

// reduce.json mapOrdered: the ordered map with a concurrency limit, observed through the engine
// (one chunk per file, one scripted call per chunk, released one by one).
func TestEngineParity_OrderedMap(t *testing.T) {
	var g struct {
		MapOrdered []struct {
			Name           string `json:"name"`
			N              int    `json:"n"`
			Limit          int    `json:"limit"`
			InitialStarted []int  `json:"initialStarted"`
			Steps          []struct {
				Release      int     `json:"release"`
				OK           *string `json:"ok"`
				Fail         *string `json:"fail"`
				StartedAfter []int   `json:"startedAfter"`
			} `json:"steps"`
			Started []int    `json:"started"`
			Results []string `json:"results"`
			Error   *string  `json:"error"`
		} `json:"mapOrdered"`
	}
	loadGolden(t, "reduce", &g)
	if len(g.MapOrdered) == 0 {
		t.Fatal("reduce.json has no mapOrdered cases")
	}
	for _, tc := range g.MapOrdered {
		if tc.N < 2 {
			continue // below two files the engine does not map at all
		}
		t.Run(tc.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var spec synthSpec
				for i := range tc.N {
					spec.Files = append(spec.Files, struct {
						Path  string      `json:"path"`
						Hunks []synthHunk `json:"hunks"`
					}{Path: fmt.Sprintf("f%d.ts", i), Hunks: []synthHunk{{Start: 1, Add: 1}}})
				}
				diff := reviewer.ParseUnifiedDiff(spec.raw())

				type answer struct {
					ok  string
					err string
				}
				gates := make([]chan answer, tc.N)
				for i := range gates {
					gates[i] = make(chan answer, 1)
				}
				var mu sync.Mutex
				var started []int
				snapshot := func() []int {
					mu.Lock()
					defer mu.Unlock()
					s := slices.Clone(started)
					slices.Sort(s)
					return s
				}
				llm := llmFunc(func(ctx context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
					mu.Lock()
					started = append(started, c.Chunk.Index)
					mu.Unlock()
					a := <-gates[c.Chunk.Index]
					if a.err != "" {
						return reviewerapp.CallResult{}, errors.New(a.err)
					}
					return reviewerapp.CallResult{Review: reviewer.Review{Verdict: reviewer.VerdictApprove, Summary: a.ok, Score: 100}}, nil
				})

				var (
					out  reviewer.Outcome
					err  error
					done = make(chan struct{})
				)
				go func() {
					defer close(done)
					out, err = reviewerapp.Run(t.Context(), reviewerapp.Input{
						SystemPrompt: "S", Diff: diff, Strategy: reviewer.StrategyMapReduce,
					}, llm, nil, reviewerapp.Options{MapConcurrency: tc.Limit})
				}()

				// A failed check must not leave calls blocked on their gates: the bubble would deadlock.
				defer func() {
					for _, gate := range gates {
						select {
						case gate <- answer{err: "released at the end of the test"}:
						default:
						}
					}
					<-done
				}()

				synctest.Wait()
				if diff := cmp.Diff(tc.InitialStarted, snapshot(), cmpopts.EquateEmpty()); diff != "" {
					t.Fatalf("calls started before any is released (-want +got):\n%s", diff)
				}
				for i, st := range tc.Steps {
					if st.Fail != nil {
						gates[st.Release] <- answer{err: *st.Fail}
					} else {
						gates[st.Release] <- answer{ok: *st.OK}
					}
					synctest.Wait()
					if diff := cmp.Diff(st.StartedAfter, snapshot(), cmpopts.EquateEmpty()); diff != "" {
						t.Fatalf("calls started after step %d (release %d) (-want +got):\n%s", i, st.Release, diff)
					}
				}
				<-done

				if diff := cmp.Diff(tc.Started, snapshot(), cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("calls started overall (-want +got):\n%s", diff)
				}
				if tc.Error != nil {
					if err == nil || err.Error() != *tc.Error {
						t.Fatalf("Run() error = %v, want %q", err, *tc.Error)
					}
					return
				}
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				// Results keep item order: the reduced summary joins the chunk summaries in chunk order.
				wantSummary := ""
				for i, r := range tc.Results {
					if i > 0 {
						wantSummary += " "
					}
					wantSummary += r
				}
				if out.Review.Summary != wantSummary {
					t.Errorf("reduced summary = %q, want %q", out.Review.Summary, wantSummary)
				}
			})
		})
	}
}

// llmFunc adapts a function to the engine's LLM port.
type llmFunc func(ctx context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error)

func (f llmFunc) Review(ctx context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
	return f(ctx, c)
}
