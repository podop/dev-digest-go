package parity_test

// S31 (the service rows of AC5013, AC5014, AC5054 and AC5052): the onboarding use cases against the
// Node golden onboarding.json sections flows, scenarios and repoFiles. The real onboarding/app
// Service runs over in-memory fakes of its ports (the golden's "world"); the comparison covers the
// response, the error, the log lines (durationMs is not parity data), the model request, the blob
// reads and the stored tours.
//
// Not covered here: `stored` (OnboardingTour.safeParse of stored documents), which the adapter's own
// test runs from the golden (onboarding/adapters/postgres store_test.go). Differences that are not
// parity gaps: a Go blob read is one call per file (Node's reads are two list calls, so the set of
// paths read is compared); Node's plain `Error` has no Go equivalent text (the cause is compared).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// ---------------------------------------------------------------------------
// Wire shape of a stored tour
// ---------------------------------------------------------------------------

func pbPathsWire(ps []domain.PathItem) []map[string]any {
	out := []map[string]any{}
	for _, p := range ps {
		out = append(out, map[string]any{"path": p.Path, "reason": p.Reason})
	}
	return out
}

// pbTourWire is a stored tour as the contract spells it.
func pbTourWire(t *domain.Tour) any {
	if t == nil {
		return nil
	}
	nodes := []map[string]any{}
	for _, n := range t.Architecture.Nodes {
		nodes = append(nodes, map[string]any{"id": n.ID, "label": n.Label, "kind": string(n.Kind)})
	}
	edges := []map[string]any{}
	for _, e := range t.Architecture.Edges {
		edges = append(edges, map[string]any{"from": e.From, "to": e.To})
	}
	steps := []map[string]any{}
	for _, s := range t.RunSteps {
		m := map[string]any{"command": s.Command}
		if s.Comment != "" {
			m["comment"] = s.Comment
		}
		steps = append(steps, m)
	}
	tasks := []map[string]any{}
	for _, f := range t.FirstTasks {
		tasks = append(tasks, map[string]any{"title": f.Title, "path": f.Path, "complexity": string(f.Complexity)})
	}
	return map[string]any{
		"repo_id": t.RepoID, "generated_at": pbISO(t.GeneratedAt), "indexed_sha": t.IndexedSHA, "files_indexed": t.FilesIndexed,
		"provider": t.Provider, "model": t.Model, "tokens_in": t.TokensIn, "tokens_out": t.TokensOut, "cost_usd": t.CostUSD,
		"prompt_version": t.PromptVersion, "language": t.Language,
		"architecture":   map[string]any{"summary": t.Architecture.Summary, "nodes": nodes, "edges": edges},
		"critical_paths": pbPathsWire(t.CriticalPaths), "run_steps": steps, "reading_path": pbPathsWire(t.ReadingPath), "first_tasks": tasks,
	}
}

// pbStateWire is the answer of a read or a generation.
func pbStateWire(s domain.State) any {
	if s.Tour == nil {
		return map[string]any{"status": "none"}
	}
	m := map[string]any{"status": "ready", "stale": s.Stale, "tour": pbTourWire(s.Tour)}
	if s.Stale {
		m["stale_reason"] = string(s.StaleReason)
	}
	return m
}

func pbTourFromWire(t testing.TB, raw json.RawMessage) *domain.Tour {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var w struct {
		RepoID        string   `json:"repo_id"`
		GeneratedAt   string   `json:"generated_at"`
		IndexedSHA    string   `json:"indexed_sha"`
		FilesIndexed  int      `json:"files_indexed"`
		Provider      string   `json:"provider"`
		Model         string   `json:"model"`
		TokensIn      int      `json:"tokens_in"`
		TokensOut     int      `json:"tokens_out"`
		CostUSD       *float64 `json:"cost_usd"`
		PromptVersion int      `json:"prompt_version"`
		Language      string   `json:"language"`
		Architecture  struct {
			Summary string                             `json:"summary"`
			Nodes   []struct{ ID, Label, Kind string } `json:"nodes"`
			Edges   []struct{ From, To string }        `json:"edges"`
		} `json:"architecture"`
		CriticalPaths []struct{ Path, Reason string }            `json:"critical_paths"`
		RunSteps      []struct{ Command, Comment string }        `json:"run_steps"`
		ReadingPath   []struct{ Path, Reason string }            `json:"reading_path"`
		FirstTasks    []struct{ Title, Path, Complexity string } `json:"first_tasks"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("decode stored tour %.200s: %v", raw, err)
	}
	tour := domain.Tour{
		RepoID: w.RepoID, GeneratedAt: pbTime(t, w.GeneratedAt), IndexedSHA: w.IndexedSHA, FilesIndexed: w.FilesIndexed,
		Provider: w.Provider, Model: w.Model, TokensIn: w.TokensIn, TokensOut: w.TokensOut, CostUSD: w.CostUSD,
		PromptVersion: w.PromptVersion, Language: w.Language,
		Architecture: domain.Architecture{Summary: w.Architecture.Summary, Nodes: []domain.Node{}, Edges: []domain.Edge{}},
	}
	for _, n := range w.Architecture.Nodes {
		tour.Architecture.Nodes = append(tour.Architecture.Nodes, domain.Node{ID: n.ID, Label: n.Label, Kind: domain.NodeKind(n.Kind)})
	}
	for _, e := range w.Architecture.Edges {
		tour.Architecture.Edges = append(tour.Architecture.Edges, domain.Edge{From: e.From, To: e.To})
	}
	for _, p := range w.CriticalPaths {
		tour.CriticalPaths = append(tour.CriticalPaths, domain.PathItem{Path: p.Path, Reason: p.Reason})
	}
	for _, s := range w.RunSteps {
		tour.RunSteps = append(tour.RunSteps, domain.RunStep{Command: s.Command, Comment: s.Comment})
	}
	for _, p := range w.ReadingPath {
		tour.ReadingPath = append(tour.ReadingPath, domain.PathItem{Path: p.Path, Reason: p.Reason})
	}
	for _, f := range w.FirstTasks {
		tour.FirstTasks = append(tour.FirstTasks, domain.FirstTask{Title: f.Title, Path: f.Path, Complexity: domain.Complexity(f.Complexity)})
	}
	return &tour
}

// ---------------------------------------------------------------------------
// The world of a flow as fakes of the ports
// ---------------------------------------------------------------------------

type pbOnbWorld struct {
	Workspace string `json:"workspace"`
	Now       string `json:"now"`
	TimeoutMs int    `json:"timeoutMs"`
	Model     struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"model"`
	Repo *struct {
		ID            string `json:"id"`
		Owner         string `json:"owner"`
		Name          string `json:"name"`
		FullName      string `json:"fullName"`
		DefaultBranch string `json:"defaultBranch"`
	} `json:"repo"`
	Index struct {
		FilesIndexed   int    `json:"filesIndexed"`
		LastIndexedSHA string `json:"lastIndexedSha"`
	} `json:"index"`
	RepoMap *struct {
		Text pbText `json:"text"`
	} `json:"repoMap"`
	Ranked      []string           `json:"ranked"`
	Chains      [][]string         `json:"chains"`
	Indexed     []string           `json:"indexed"`
	Blobs       map[string]*pbText `json:"blobs"`
	BlobDelayMs *int               `json:"blobDelayMs"`
	LLM         json.RawMessage    `json:"llm"`
}

type pbOnbRig struct {
	t     testing.TB
	w     *pbOnbWorld
	logs  *pbLogCapture
	cause error

	mu       sync.Mutex
	upserts  []any
	stored   *domain.Tour
	reads    []string
	ranked   []int
	llmReqs  []pbLLMRequest
	llmCalls int

	// gate and entered hold the model call in the scenarios.
	gate    chan error
	entered chan struct{}
}

func newPbOnbRig(t testing.TB, w *pbOnbWorld, stored json.RawMessage) *pbOnbRig {
	return &pbOnbRig{t: t, w: w, logs: newPbLogCapture(), stored: pbTourFromWire(t, stored)}
}

func (r *pbOnbRig) service(t testing.TB) *app.Service {
	now := pbTime(t, r.w.Now)
	return app.NewService(app.Deps{
		Store: pbOnbStore{r}, IndexStates: pbOnbIndexStates{r}, Index: pbOnbIndex{r}, Blobs: pbOnbBlobs{r},
		LLM: pbOnbLLM{r}, Models: pbOnbModels{r},
		Now: func() time.Time { return now }, Log: slog.New(r.logs), Budget: time.Duration(r.w.TimeoutMs) * time.Millisecond,
	})
}

type pbOnbStore struct{ r *pbOnbRig }

func (s pbOnbStore) FindRepo(_ context.Context, workspaceID, repoID string) (domain.Repo, error) {
	w := s.r.w
	if w.Repo == nil || workspaceID != w.Workspace || repoID != w.Repo.ID {
		return domain.Repo{}, domain.ErrNotFound
	}
	return domain.Repo{ID: w.Repo.ID, WorkspaceID: workspaceID, Owner: w.Repo.Owner, Name: w.Repo.Name, FullName: w.Repo.FullName, DefaultBranch: w.Repo.DefaultBranch}, nil
}

func (s pbOnbStore) Tour(context.Context, string) (domain.Tour, bool, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.r.stored == nil {
		return domain.Tour{}, false, nil
	}
	return *s.r.stored, true, nil
}

func (s pbOnbStore) SaveTour(_ context.Context, t domain.Tour) error {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.upserts = append(s.r.upserts, pbTourWire(&t))
	s.r.stored = &t
	return nil
}

type pbOnbIndexStates struct{ r *pbOnbRig }

func (s pbOnbIndexStates) IndexState(context.Context, string) domain.IndexState {
	return domain.IndexState{FilesIndexed: s.r.w.Index.FilesIndexed, LastIndexedSHA: s.r.w.Index.LastIndexedSHA}
}

type pbOnbIndex struct{ r *pbOnbRig }

func (s pbOnbIndex) RepoMap(context.Context, string, int) (string, error) {
	if s.r.w.RepoMap == nil {
		return "", nil
	}
	return s.r.w.RepoMap.Text.plain(s.r.t), nil
}

func (s pbOnbIndex) TopFilesByRank(_ context.Context, _ string, n int) ([]string, error) {
	s.r.mu.Lock()
	s.r.ranked = append(s.r.ranked, n)
	s.r.mu.Unlock()
	return s.r.w.Ranked[:min(n, len(s.r.w.Ranked))], nil
}

func (s pbOnbIndex) CriticalPaths(context.Context, string) ([][]string, error) {
	return s.r.w.Chains, nil
}

func (s pbOnbIndex) ListIndexedFiles(context.Context, string) ([]string, error) {
	return s.r.w.Indexed, nil
}

type pbOnbBlobs struct{ r *pbOnbRig }

func (s pbOnbBlobs) Read(ctx context.Context, _, _, _, path string, _ int64) ([]byte, bool) {
	s.r.mu.Lock()
	s.r.reads = append(s.r.reads, path)
	s.r.mu.Unlock()
	if d := s.r.w.BlobDelayMs; d != nil && *d > 0 {
		select {
		case <-time.After(time.Duration(*d) * time.Millisecond):
		case <-ctx.Done():
			return nil, false
		}
	}
	text, ok := s.r.w.Blobs[path]
	if !ok || text == nil {
		return nil, false
	}
	return []byte(text.plain(s.r.t)), true
}

type pbOnbModels struct{ r *pbOnbRig }

func (s pbOnbModels) Get(context.Context, string) (map[string]json.RawMessage, error) {
	m := s.r.w.Model
	bag := fmt.Sprintf(`{"onboarding":{"provider":%q,"model":%q}}`, m.Provider, m.Model)
	return map[string]json.RawMessage{"feature_models": json.RawMessage(bag)}, nil
}

type pbOnbLLM struct{ r *pbOnbRig }

func (s pbOnbLLM) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	r := s.r
	var spec struct {
		Data      json.RawMessage `json:"data"`
		TokensIn  int             `json:"tokensIn"`
		TokensOut int             `json:"tokensOut"`
		CostUSD   *float64        `json:"costUsd"`
		Error     string          `json:"error"`
		Hang      bool            `json:"hang"`
		Mock      bool            `json:"mock"`
	}
	if err := json.Unmarshal(r.w.LLM, &spec); err != nil {
		r.t.Fatalf("decode world llm: %v", err)
	}
	temperature := 0.0
	if req.Temperature != nil {
		temperature = *req.Temperature
	}
	var roles []string
	user := ""
	for _, m := range req.Messages {
		roles = append(roles, string(m.Role))
		if m.Role == llm.User {
			user = m.Content
		}
	}
	system := len(req.Messages) > 0 && req.Messages[0].Content == domain.SystemPrompt
	rec := pbLLMRequest{
		Provider: string(req.Provider), Model: req.Model, SchemaName: req.SchemaName, Temperature: temperature, MaxTokens: req.MaxTokens,
		MaxRetries: req.Reprompts, HasUsageObserver: req.OnUsage != nil, HasSignal: ctx.Done() != nil, Roles: roles,
		SystemIsTheNodeConstant: system, User: pbText{s: user},
	}
	r.mu.Lock()
	if spec.Error != "config" {
		r.llmReqs = append(r.llmReqs, rec)
	}
	gate, entered := r.gate, r.entered
	r.mu.Unlock()

	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		select {
		case err := <-gate:
			if err != nil {
				return llm.Result{}, err
			}
		case <-ctx.Done():
			return llm.Result{}, ctx.Err()
		}
	}
	switch {
	case spec.Hang:
		<-ctx.Done()
		return llm.Result{}, ctx.Err()
	case spec.Error == "config":
		return llm.Result{}, apperr.Config("provider key missing")
	case spec.Error != "" || spec.Mock:
		return llm.Result{}, errors.New("provider failed")
	}
	return llm.Result{Data: spec.Data, Model: req.Model, TokensIn: spec.TokensIn, TokensOut: spec.TokensOut, CostUSD: spec.CostUSD, Attempts: 1}, nil
}

// ---------------------------------------------------------------------------
// Flows
// ---------------------------------------------------------------------------

type pbOnbFlow struct {
	Name        string          `json:"name"`
	Call        string          `json:"call"`
	World       pbOnbWorld      `json:"world"`
	Stored      json.RawMessage `json:"stored"`
	Response    any             `json:"response"`
	Error       *pbWantError    `json:"error"`
	Logs        []pbLogLine     `json:"logs"`
	LLMRequests []pbLLMRequest  `json:"llmRequests"`
	Upserts     []any           `json:"upserts"`
	ReadCalls   []struct {
		Paths json.RawMessage `json:"paths"` // a list, or {count, first, last} for a long one
		SHA   string          `json:"sha"`
	} `json:"readCalls"`
	RankedRequests []int `json:"rankedRequests"`
	StoredAfter    any   `json:"storedAfter"`
}

func pbResolveTourRefs(v any, tour any) any {
	switch x := v.(type) {
	case string:
		if x == "$response.tour" {
			return tour
		}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pbResolveTourRefs(e, tour)
		}
		return out
	}
	return v
}

func TestOnboardingParity_Flows(t *testing.T) {
	var g struct {
		Flows []pbOnbFlow `json:"flows"`
	}
	pbLoad(t, "onboarding", &g)
	if len(g.Flows) < 30 {
		t.Fatalf("golden has %d onboarding flows, a generator change shrank it", len(g.Flows))
	}
	for _, c := range g.Flows {
		t.Run(c.Name, func(t *testing.T) {
			w := c.World
			r := newPbOnbRig(t, &w, c.Stored)
			svc := r.service(t)
			repoID := "repo-1"

			var state domain.State
			var err error
			switch c.Call {
			case "generate":
				state, err = svc.Generate(t.Context(), w.Workspace, repoID)
			case "get":
				state, err = svc.Get(t.Context(), w.Workspace, repoID)
			default:
				t.Fatalf("unknown call %q", c.Call)
			}
			pbCheckError(t, c.Error, err, nil)

			var resp any
			if c.Response != nil {
				resp = pbGeneric(t, pbStateWire(state))
				pbDiff(t, "response", c.Response, resp)
			} else if err == nil {
				t.Errorf("the call succeeded, Node failed with %+v", c.Error)
			}
			var respTour any
			if m, ok := resp.(map[string]any); ok {
				respTour = m["tour"]
			}

			pbDiff(t, "log lines", pbWantLogs(t, c.Logs), pbLogsShape(t, r.logs.snapshot()))
			pbCheckLLMRequests(t, c.LLMRequests, r.llmReqs)
			pbDiff(t, "upserts", pbGeneric(t, pbResolveTourRefs(pbNonNil(c.Upserts), respTour)), pbGeneric(t, pbNonNil(r.upserts)))
			var after any
			if r.stored != nil {
				after = pbGeneric(t, pbTourWire(r.stored))
			}
			pbDiff(t, "stored tour after", pbResolveTourRefs(c.StoredAfter, respTour), after)
			pbDiff(t, "ranked-file requests", pbGeneric(t, pbNonNil(c.RankedRequests)), pbGeneric(t, pbNonNil(r.ranked)))

			// Blob reads: Node's calls are the two lists, Go reads file by file in a pool, so the
			// set of paths read is compared. When the budget ran out during the reads, Go starts no
			// further read (Node still issued both lists): the reads are then a subset.
			want := map[string]bool{}
			wantCount := 0
			var wantEnds []string
			for _, call := range c.ReadCalls {
				var list []string
				var long struct {
					Count int      `json:"count"`
					First []string `json:"first"`
					Last  []string `json:"last"`
				}
				if json.Unmarshal(call.Paths, &list) != nil {
					if err := json.Unmarshal(call.Paths, &long); err != nil {
						t.Fatalf("read call paths %.100s: %v", call.Paths, err)
					}
					wantCount += long.Count
					wantEnds = append(wantEnds, append(long.First, long.Last...)...)
					continue
				}
				wantCount += len(list)
				for _, p := range list {
					want[p] = true
				}
			}
			got := map[string]bool{}
			for _, p := range r.reads {
				got[p] = true
			}
			for _, p := range wantEnds {
				want[p] = true
			}
			for p := range got {
				if !want[p] && wantEnds == nil {
					t.Errorf("read of %q, which no Node list holds", p)
				}
			}
			if w.BlobDelayMs == nil || *w.BlobDelayMs == 0 {
				for p := range want {
					if !got[p] {
						t.Errorf("path %q was not read", p)
					}
				}
				if len(r.reads) != len(got) || len(got) != wantCount {
					t.Errorf("%d reads of %d distinct paths, want %d", len(r.reads), len(got), wantCount)
				}
			}
			for _, call := range c.ReadCalls {
				if call.SHA != w.Index.LastIndexedSHA {
					t.Errorf("Node read at %q, golden index says %q", call.SHA, w.Index.LastIndexedSHA)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scenarios: one generation per repository
// ---------------------------------------------------------------------------

func TestOnboardingParity_Scenarios(t *testing.T) {
	var g struct {
		Flows []struct {
			Name  string     `json:"name"`
			World pbOnbWorld `json:"world"`
		} `json:"flows"`
		Scenarios []struct {
			Name  string `json:"name"`
			Steps []struct {
				Start   string `json:"start"`
				Settle  bool   `json:"settle"`
				Release int    `json:"release"`
				Fail    string `json:"fail"`
				Await   string `json:"await"`
			} `json:"steps"`
			Callers map[string]struct {
				Result struct {
					Response any          `json:"response"`
					Error    *pbWantError `json:"error"`
				} `json:"result"`
				Logs []pbLogLine `json:"logs"`
			} `json:"callers"`
			LLMRequests int `json:"llmRequests"`
			Upserts     int `json:"upserts"`
		} `json:"scenarios"`
	}
	pbLoad(t, "onboarding", &g)
	if len(g.Scenarios) != 2 {
		t.Fatalf("golden has %d scenarios, want 2", len(g.Scenarios))
	}
	base := g.Flows[0].World

	for _, sc := range g.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			w := base
			r := newPbOnbRig(t, &w, nil)
			r.gate = make(chan error, 8)
			r.entered = make(chan struct{}, 8)
			svc := r.service(t)

			type outcome struct {
				state domain.State
				err   error
			}
			done := map[string]chan outcome{}
			results := map[string]outcome{}
			for _, st := range sc.Steps {
				switch {
				case st.Start != "":
					ch := make(chan outcome, 1)
					done[st.Start] = ch
					go func() {
						s, err := svc.Generate(context.Background(), w.Workspace, "repo-1")
						ch <- outcome{s, err}
					}()
					select { // parked in the model, or finished (a refused second call)
					case <-r.entered:
					case o := <-ch:
						results[st.Start] = o
						ch <- o
					case <-time.After(5 * time.Second):
						t.Fatalf("caller %s neither reached the model nor finished", st.Start)
					}
				case st.Settle:
				case st.Release > 0:
					for range st.Release {
						r.gate <- nil
					}
				case st.Fail != "":
					r.gate <- errors.New(st.Fail)
				case st.Await != "":
					select {
					case o := <-done[st.Await]:
						results[st.Await] = o
					case <-time.After(5 * time.Second):
						if _, ok := results[st.Await]; !ok {
							t.Fatalf("caller %s never finished", st.Await)
						}
					}
				}
			}
			var wantLines []pbLogLine
			for _, name := range sortedKeys(sc.Callers) {
				want := sc.Callers[name]
				got, ok := results[name]
				if !ok {
					t.Errorf("caller %s has no result", name)
					continue
				}
				pbCheckError(t, want.Result.Error, got.err, nil)
				if want.Result.Response != nil {
					pbDiff(t, "caller "+name+" response", want.Result.Response, pbGeneric(t, pbStateWire(got.state)))
				}
				wantLines = append(wantLines, want.Logs...)
			}
			if len(r.llmReqs) != sc.LLMRequests || len(r.upserts) != sc.Upserts {
				t.Errorf("%d model requests and %d stored tours, want %d and %d", len(r.llmReqs), len(r.upserts), sc.LLMRequests, sc.Upserts)
			}
			sortShapes := func(ls []any) []any {
				sort.SliceStable(ls, func(i, j int) bool {
					a, _ := json.Marshal(ls[i])
					b, _ := json.Marshal(ls[j])
					return string(a) < string(b)
				})
				return ls
			}
			pbDiff(t, "log lines of all callers", sortShapes(pbWantLogs(t, wantLines)), sortShapes(pbLogsShape(t, r.logs.snapshot())))
		})
	}
}

// ---------------------------------------------------------------------------
// Bounded blob reads (the golden's repoFiles section, driven through the use case)
// ---------------------------------------------------------------------------

// pbBoundedBlobs holds every read until released, counting the reads in flight per list.
type pbBoundedBlobs struct {
	mu       sync.Mutex
	scan     map[string]bool
	inFlight int
	peak     int
	started  int
	hold     chan struct{} // closed to let reads finish
	onRead   func(started int)
}

func (b *pbBoundedBlobs) Read(ctx context.Context, _, _, _, path string, _ int64) ([]byte, bool) {
	if !b.scan[path] { // the root files
		return nil, false
	}
	b.mu.Lock()
	b.started++
	b.inFlight++
	b.peak = max(b.peak, b.inFlight)
	started := b.started
	b.mu.Unlock()
	if b.onRead != nil {
		b.onRead(started)
	}
	select {
	case <-b.hold:
	case <-ctx.Done():
	}
	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()
	return nil, false
}

// TestOnboardingParity_BoundedReads: at most eight reads of the scan list are in flight (the
// golden: peak 8 for 30 paths, 30 reads) and, once the generation is cancelled, no further read
// starts. Node's golden stops at exactly 10 reads; reads already past the check in the other seven
// workers may still start, so Go stops within 10 plus seven.
func TestOnboardingParity_BoundedReads(t *testing.T) {
	var g struct {
		Flows []struct {
			World pbOnbWorld `json:"world"`
		} `json:"flows"`
		RepoFiles []struct {
			Name           string          `json:"name"`
			Reads          int             `json:"reads"`
			PeakInFlight   int             `json:"peakInFlight"`
			AbortAfterRead *int            `json:"abortAfterReads"`
			Paths          json.RawMessage `json:"paths"`
		} `json:"repoFiles"`
	}
	pbLoad(t, "onboarding", &g)
	byName := map[string]int{}
	for i, c := range g.RepoFiles {
		byName[c.Name] = i
	}
	for _, name := range []string{"at_most_eight_in_flight", "no_new_read_after_abort"} {
		i, ok := byName[name]
		if !ok {
			t.Fatalf("golden lacks the repoFiles case %q", name)
		}
		c := g.RepoFiles[i]
		t.Run(name, func(t *testing.T) {
			var spec struct {
				Count int `json:"count"`
			}
			if err := json.Unmarshal(c.Paths, &spec); err != nil || spec.Count != 30 {
				t.Fatalf("paths spec %s: count %d, err %v; the test builds 30 paths", c.Paths, spec.Count, err)
			}
			w := g.Flows[0].World
			w.Ranked, w.Indexed = nil, nil
			scan := map[string]bool{}
			for i := 1; i <= spec.Count; i++ {
				p := fmt.Sprintf("f%03d.ts", i)
				w.Ranked = append(w.Ranked, p)
				w.Indexed = append(w.Indexed, p)
				scan[p] = true
			}
			r := newPbOnbRig(t, &w, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			blobs := &pbBoundedBlobs{scan: scan, hold: make(chan struct{})}
			var release, abort sync.Once
			blobs.onRead = func(started int) {
				if started == domain.MaxBlobReaders { // eight in flight: let the held reads finish
					release.Do(func() { close(blobs.hold) })
				}
				if c.AbortAfterRead != nil && started == *c.AbortAfterRead {
					abort.Do(cancel)
				}
			}
			svc := app.NewService(app.Deps{
				Store: pbOnbStore{r}, IndexStates: pbOnbIndexStates{r}, Index: pbOnbIndex{r}, Blobs: blobs,
				LLM: pbOnbLLM{r}, Models: pbOnbModels{r}, Now: func() time.Time { return pbTime(t, w.Now) },
			})
			_, _ = svc.Generate(ctx, w.Workspace, "repo-1")

			blobs.mu.Lock()
			defer blobs.mu.Unlock()
			if blobs.peak != c.PeakInFlight {
				t.Errorf("peak reads in flight = %d, want %d", blobs.peak, c.PeakInFlight)
			}
			if c.AbortAfterRead == nil {
				if blobs.started != c.Reads {
					t.Errorf("%d reads, want %d", blobs.started, c.Reads)
				}
			} else if blobs.started < c.Reads || blobs.started > c.Reads+domain.MaxBlobReaders-1 {
				t.Errorf("%d reads after the cancel at read %d, want %d..%d", blobs.started, c.Reads, c.Reads, c.Reads+domain.MaxBlobReaders-1)
			}
		})
	}
}
