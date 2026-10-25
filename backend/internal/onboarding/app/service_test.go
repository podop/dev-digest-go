package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// The Node goldens (parity/testdata/goldens/onboarding.json, flows) drive the use cases through
// fakes of every port: the same world, the same calls, and the expected response or error, log
// line, model request, reads and writes.

const goldenPath = "../../parity/testdata/goldens/onboarding.json"

type goldenFile struct {
	Flows []flow `json:"flows"`
}

type flow struct {
	Name           string          `json:"name"`
	Call           string          `json:"call"`
	World          worldDoc        `json:"world"`
	Stored         json.RawMessage `json:"stored"`
	Response       json.RawMessage `json:"response"`
	Error          *errorDoc       `json:"error"`
	Logs           []logDoc        `json:"logs"`
	LLMRequests    []llmRequestDoc `json:"llmRequests"`
	Upserts        []string        `json:"upserts"`
	ReadCalls      []readCallDoc   `json:"readCalls"`
	RankedRequests []int           `json:"rankedRequests"`
	StoredAfter    json.RawMessage `json:"storedAfter"`
}

type worldDoc struct {
	Workspace string `json:"workspace"`
	Now       string `json:"now"`
	TimeoutMs int    `json:"timeoutMs"`
	Model     struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"model"`
	Repo  *repoDoc `json:"repo"`
	Index struct {
		FilesIndexed   int    `json:"filesIndexed"`
		LastIndexedSha string `json:"lastIndexedSha"`
	} `json:"index"`
	RepoMap     struct{ Text string } `json:"repoMap"`
	Ranked      []string              `json:"ranked"`
	Chains      [][]string            `json:"chains"`
	Indexed     []string              `json:"indexed"`
	Blobs       map[string]string     `json:"blobs"`
	BlobDelayMs *int                  `json:"blobDelayMs"`
	LLM         llmDoc                `json:"llm"`
}

type repoDoc struct {
	ID            string `json:"id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	FullName      string `json:"fullName"`
	DefaultBranch string `json:"defaultBranch"`
}

type llmDoc struct {
	Data      json.RawMessage `json:"data"`
	TokensIn  int             `json:"tokensIn"`
	TokensOut int             `json:"tokensOut"`
	CostUsd   *float64        `json:"costUsd"`
	Error     string          `json:"error"`
	Mock      bool            `json:"mock"`
	Hang      bool            `json:"hang"`
}

type errorDoc struct {
	Kind    string          `json:"kind"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

type logDoc struct {
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Obj   map[string]any `json:"obj"`
}

type llmRequestDoc struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	SchemaName string   `json:"schemaName"`
	Temp       float64  `json:"temperature"`
	MaxTokens  int      `json:"maxTokens"`
	MaxRetries int      `json:"maxRetries"`
	Roles      []string `json:"roles"`
	User       string   `json:"user"`
}

type readCallDoc struct {
	Repo struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repo"`
	Sha   string          `json:"sha"`
	Paths json.RawMessage `json:"paths"`
}

// tourDoc is the wire form of a tour in the goldens (spec 7.3).
type tourDoc struct {
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
		Summary string `json:"summary"`
		Nodes   []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Kind  string `json:"kind"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	} `json:"architecture"`
	CriticalPaths []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"critical_paths"`
	RunSteps []struct {
		Command string `json:"command"`
		Comment string `json:"comment,omitempty"`
	} `json:"run_steps"`
	ReadingPath []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"reading_path"`
	FirstTasks []struct {
		Title      string `json:"title"`
		Path       string `json:"path"`
		Complexity string `json:"complexity"`
	} `json:"first_tasks"`
}

func (d tourDoc) domain(t *testing.T) domain.Tour {
	t.Helper()
	at, err := time.Parse(time.RFC3339, d.GeneratedAt)
	if err != nil {
		t.Fatalf("golden generated_at %q: %v", d.GeneratedAt, err)
	}
	tour := domain.Tour{
		RepoID: d.RepoID, GeneratedAt: at, IndexedSHA: d.IndexedSHA, FilesIndexed: d.FilesIndexed,
		Provider: d.Provider, Model: d.Model, TokensIn: d.TokensIn, TokensOut: d.TokensOut, CostUSD: d.CostUSD,
		PromptVersion: d.PromptVersion, Language: d.Language,
		Architecture:  domain.Architecture{Summary: d.Architecture.Summary, Nodes: []domain.Node{}, Edges: []domain.Edge{}},
		CriticalPaths: []domain.PathItem{}, RunSteps: []domain.RunStep{}, ReadingPath: []domain.PathItem{}, FirstTasks: []domain.FirstTask{},
	}
	for _, n := range d.Architecture.Nodes {
		tour.Architecture.Nodes = append(tour.Architecture.Nodes, domain.Node{ID: n.ID, Label: n.Label, Kind: domain.NodeKind(n.Kind)})
	}
	for _, e := range d.Architecture.Edges {
		tour.Architecture.Edges = append(tour.Architecture.Edges, domain.Edge{From: e.From, To: e.To})
	}
	for _, p := range d.CriticalPaths {
		tour.CriticalPaths = append(tour.CriticalPaths, domain.PathItem{Path: p.Path, Reason: p.Reason})
	}
	for _, s := range d.RunSteps {
		tour.RunSteps = append(tour.RunSteps, domain.RunStep{Command: s.Command, Comment: s.Comment})
	}
	for _, p := range d.ReadingPath {
		tour.ReadingPath = append(tour.ReadingPath, domain.PathItem{Path: p.Path, Reason: p.Reason})
	}
	for _, f := range d.FirstTasks {
		tour.FirstTasks = append(tour.FirstTasks, domain.FirstTask{Title: f.Title, Path: f.Path, Complexity: domain.Complexity(f.Complexity)})
	}
	return tour
}

// wireOf encodes a tour as the goldens show it (generated_at with milliseconds).
func wireOf(t *testing.T, tour domain.Tour) any {
	t.Helper()
	d := tourDoc{
		RepoID: tour.RepoID, GeneratedAt: tour.GeneratedAt.UTC().Format("2006-01-02T15:04:05.000Z"), IndexedSHA: tour.IndexedSHA,
		FilesIndexed: tour.FilesIndexed, Provider: tour.Provider, Model: tour.Model, TokensIn: tour.TokensIn,
		TokensOut: tour.TokensOut, CostUSD: tour.CostUSD, PromptVersion: tour.PromptVersion, Language: tour.Language,
	}
	d.Architecture.Summary = tour.Architecture.Summary
	d.Architecture.Nodes = make([]struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Kind  string `json:"kind"`
	}, len(tour.Architecture.Nodes))
	for i, n := range tour.Architecture.Nodes {
		d.Architecture.Nodes[i].ID, d.Architecture.Nodes[i].Label, d.Architecture.Nodes[i].Kind = n.ID, n.Label, string(n.Kind)
	}
	d.Architecture.Edges = make([]struct {
		From string `json:"from"`
		To   string `json:"to"`
	}, len(tour.Architecture.Edges))
	for i, e := range tour.Architecture.Edges {
		d.Architecture.Edges[i].From, d.Architecture.Edges[i].To = e.From, e.To
	}
	d.CriticalPaths = make([]struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}, len(tour.CriticalPaths))
	for i, p := range tour.CriticalPaths {
		d.CriticalPaths[i].Path, d.CriticalPaths[i].Reason = p.Path, p.Reason
	}
	d.RunSteps = make([]struct {
		Command string `json:"command"`
		Comment string `json:"comment,omitempty"`
	}, len(tour.RunSteps))
	for i, s := range tour.RunSteps {
		d.RunSteps[i].Command, d.RunSteps[i].Comment = s.Command, s.Comment
	}
	d.ReadingPath = make([]struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}, len(tour.ReadingPath))
	for i, p := range tour.ReadingPath {
		d.ReadingPath[i].Path, d.ReadingPath[i].Reason = p.Path, p.Reason
	}
	d.FirstTasks = make([]struct {
		Title      string `json:"title"`
		Path       string `json:"path"`
		Complexity string `json:"complexity"`
	}, len(tour.FirstTasks))
	for i, f := range tour.FirstTasks {
		d.FirstTasks[i].Title, d.FirstTasks[i].Path, d.FirstTasks[i].Complexity = f.Title, f.Path, string(f.Complexity)
	}
	return jsonAny(t, d)
}

func stateWire(t *testing.T, s domain.State) any {
	t.Helper()
	if s.Tour == nil {
		return map[string]any{"status": "none"}
	}
	m := map[string]any{"status": "ready", "stale": s.Stale, "tour": wireOf(t, *s.Tour)}
	if s.Stale {
		m["stale_reason"] = string(s.StaleReason)
	}
	return m
}

func jsonAny(t *testing.T, v any) any {
	t.Helper()
	var raw []byte
	if r, ok := v.(json.RawMessage); ok {
		raw = r
	} else {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// fakes ------------------------------------------------------------------------------------------

type fakeStore struct {
	mu    sync.Mutex
	repo  *domain.Repo
	tour  *domain.Tour
	saved []domain.Tour
	// saveErr fails SaveTour.
	saveErr error
}

func (s *fakeStore) FindRepo(_ context.Context, workspaceID, repoID string) (domain.Repo, error) {
	if s.repo == nil || s.repo.WorkspaceID != workspaceID || s.repo.ID != repoID {
		return domain.Repo{}, domain.ErrNotFound
	}
	return *s.repo, nil
}

func (s *fakeStore) Tour(context.Context, string) (domain.Tour, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tour == nil {
		return domain.Tour{}, false, nil
	}
	return *s.tour, true, nil
}

func (s *fakeStore) SaveTour(_ context.Context, t domain.Tour) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, t)
	s.tour = &t
	return nil
}

type fakeIndexStates struct{ state domain.IndexState }

func (f fakeIndexStates) IndexState(context.Context, string) domain.IndexState { return f.state }

type fakeIndex struct {
	mu          sync.Mutex
	repoMap     string
	ranked      []string
	chains      [][]string
	indexed     []string
	rankedAsked []int
}

func (f *fakeIndex) RepoMap(_ context.Context, _ string, budget int) (string, error) {
	if budget != domain.RepoMapBudget {
		return "", fmt.Errorf("repo map budget = %d, want %d", budget, domain.RepoMapBudget)
	}
	return f.repoMap, nil
}

func (f *fakeIndex) TopFilesByRank(_ context.Context, _ string, n int) ([]string, error) {
	f.mu.Lock()
	f.rankedAsked = append(f.rankedAsked, n)
	f.mu.Unlock()
	return f.ranked[:min(n, len(f.ranked))], nil
}

func (f *fakeIndex) CriticalPaths(context.Context, string) ([][]string, error) { return f.chains, nil }

func (f *fakeIndex) ListIndexedFiles(context.Context, string) ([]string, error) {
	return f.indexed, nil
}

type blobCall struct {
	owner, name, sha, path string
	max                    int64
}

// fakeBlobs serves a map of files. Each read can take delay (fake time under synctest), and the
// concurrency of the reads is recorded per list (root files versus ranked files).
type fakeBlobs struct {
	mu       sync.Mutex
	files    map[string]string
	delay    time.Duration
	calls    []blobCall
	inFlight map[bool]int
	peak     map[bool]int
	onRead   func(path string)
}

func newFakeBlobs(files map[string]string) *fakeBlobs {
	return &fakeBlobs{files: files, inFlight: map[bool]int{}, peak: map[bool]int{}}
}

func (b *fakeBlobs) Read(ctx context.Context, owner, name, sha, path string, limit int64) ([]byte, bool) {
	isRoot := slices.Contains(domain.RootFilePaths(), path)
	b.mu.Lock()
	b.calls = append(b.calls, blobCall{owner, name, sha, path, limit})
	b.inFlight[isRoot]++
	b.peak[isRoot] = max(b.peak[isRoot], b.inFlight[isRoot])
	hook := b.onRead
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight[isRoot]--
		b.mu.Unlock()
	}()
	if hook != nil {
		hook(path)
	}
	if b.delay > 0 {
		select {
		case <-time.After(b.delay):
		case <-ctx.Done():
			return nil, false
		}
	}
	text, ok := b.files[path]
	return []byte(text), ok
}

type fakeLLM struct {
	mu       sync.Mutex
	requests []llm.Request
	do       func(ctx context.Context, req llm.Request) (llm.Result, error)
}

func (f *fakeLLM) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return f.do(ctx, req)
}

type fakeModels struct{ bag map[string]json.RawMessage }

func (f fakeModels) Get(context.Context, string) (map[string]json.RawMessage, error) {
	return f.bag, nil
}

// rig is a service with its fakes.
type rig struct {
	svc    *app.Service
	store  *fakeStore
	index  *fakeIndex
	blobs  *fakeBlobs
	llm    *fakeLLM
	logs   *bytes.Buffer
	repo   domain.Repo
	ws     string
	budget time.Duration
	// spend is the cap check port; nil (an untyped nil interface) leaves the check off.
	spend app.SpendBudget
}

var okOutput = json.RawMessage(`{"architecture":{"summary":"s","nodes":[],"edges":[]},"critical_paths":[],"run_steps":[],"reading_path":[],"first_tasks":[]}`)

func newRig(t *testing.T) *rig {
	t.Helper()
	repo := domain.Repo{ID: "repo-1", WorkspaceID: "ws-1", Owner: "acme", Name: "shop", FullName: "acme/shop", DefaultBranch: "main"}
	r := &rig{
		store: &fakeStore{repo: &repo},
		index: &fakeIndex{ranked: []string{"src/a.ts", "src/b.ts"}, indexed: []string{"src/a.ts", "src/b.ts"}},
		blobs: newFakeBlobs(map[string]string{}),
		logs:  &bytes.Buffer{},
		repo:  repo,
		ws:    "ws-1",
	}
	r.llm = &fakeLLM{do: func(context.Context, llm.Request) (llm.Result, error) {
		return llm.Result{Data: okOutput, TokensIn: 1, TokensOut: 2}, nil
	}}
	return r
}

func (r *rig) build(states domain.IndexState, bag map[string]json.RawMessage, now func() time.Time) {
	r.svc = app.NewService(app.Deps{
		Store: r.store, IndexStates: fakeIndexStates{state: states}, Index: r.index, Blobs: r.blobs,
		LLM: r.llm, Models: fakeModels{bag: bag}, Now: now,
		Log:    slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Budget: r.budget, Spend: r.spend,
	})
}

func readyState() domain.IndexState {
	return domain.IndexState{FilesIndexed: 42, LastIndexedSHA: "abc123def456"}
}

func fixedNow() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }

// golden flows -----------------------------------------------------------------------------------

func TestFlows_MatchNode(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(goldenPath))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(g.Flows) == 0 {
		t.Fatal("golden has no flows")
	}
	for _, f := range g.Flows {
		t.Run(f.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runFlow(t, f) })
		})
	}
}

func runFlow(t *testing.T, f flow) {
	t.Helper()
	w := f.World
	now, err := time.Parse(time.RFC3339, w.Now)
	if err != nil {
		t.Fatalf("world.now %q: %v", w.Now, err)
	}
	r := &rig{
		store: &fakeStore{},
		index: &fakeIndex{repoMap: w.RepoMap.Text, ranked: w.Ranked, chains: w.Chains, indexed: w.Indexed},
		blobs: newFakeBlobs(w.Blobs),
		logs:  &bytes.Buffer{},
		ws:    w.Workspace,
	}
	if w.Repo != nil {
		r.repo = domain.Repo{ID: w.Repo.ID, WorkspaceID: w.Workspace, Owner: w.Repo.Owner, Name: w.Repo.Name, FullName: w.Repo.FullName, DefaultBranch: w.Repo.DefaultBranch}
		r.store.repo = &r.repo
	} else {
		r.repo = domain.Repo{ID: "repo-1"}
	}
	if len(f.Stored) > 0 && string(f.Stored) != "null" {
		var d tourDoc
		if err := json.Unmarshal(f.Stored, &d); err != nil {
			t.Fatalf("decode stored: %v", err)
		}
		tour := d.domain(t)
		r.store.tour = &tour
	}
	if w.BlobDelayMs != nil {
		r.blobs.delay = time.Duration(*w.BlobDelayMs) * time.Millisecond
	}
	r.budget = time.Duration(w.TimeoutMs) * time.Millisecond
	r.llm = &fakeLLM{do: llmBehaviour(w.LLM)}
	bag := map[string]json.RawMessage{
		"feature_models": json.RawMessage(fmt.Sprintf(`{"onboarding":{"provider":%q,"model":%q}}`, w.Model.Provider, w.Model.Model)),
	}
	r.build(domain.IndexState{FilesIndexed: w.Index.FilesIndexed, LastIndexedSHA: w.Index.LastIndexedSha}, bag, func() time.Time { return now })

	var (
		state domain.State
		gerr  error
	)
	switch f.Call {
	case "generate":
		state, gerr = r.svc.Generate(t.Context(), w.Workspace, r.repo.ID)
	case "get":
		state, gerr = r.svc.Get(t.Context(), w.Workspace, r.repo.ID)
	default:
		t.Fatalf("unknown call %q", f.Call)
	}

	if f.Error != nil {
		checkError(t, f.Error, gerr)
	} else {
		if gerr != nil {
			t.Fatalf("%s: unexpected error: %v", f.Call, gerr)
		}
		if diff := cmp.Diff(jsonAny(t, f.Response), stateWire(t, state)); diff != "" {
			t.Errorf("%s response mismatch (-want +got):\n%s", f.Call, diff)
		}
	}
	checkLogs(t, f.Logs, r.logs)
	checkModelRequests(t, f, r)
	checkWrites(t, f, r, state)
	switch {
	case len(f.ReadCalls) == 0:
		if len(r.blobs.calls) != 0 {
			t.Errorf("blob reads = %d, want none", len(r.blobs.calls))
		}
	case w.BlobDelayMs == nil: // a delayed read is cut short by the budget: the set of reads varies
		checkReads(t, f, r)
	}
	if want := f.RankedRequests; len(want) > 0 || f.Call == "get" {
		if diff := cmp.Diff(emptyIfNil(want), emptyIfNil(r.index.rankedAsked)); diff != "" {
			t.Errorf("top-files requests mismatch (-want +got):\n%s", diff)
		}
	}
}

func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func llmBehaviour(d llmDoc) func(context.Context, llm.Request) (llm.Result, error) {
	return func(ctx context.Context, _ llm.Request) (llm.Result, error) {
		switch {
		case d.Hang:
			<-ctx.Done()
			return llm.Result{}, fmt.Errorf("structured call was cancelled: %w", ctx.Err())
		case d.Error == "config":
			return llm.Result{}, apperr.Config("OPENROUTER_API_KEY is not configured")
		case d.Error == "other":
			return llm.Result{}, errors.New("upstream said: secret repo text")
		case d.Error == "invalid":
			return llm.Result{}, apperr.ExternalService("structured output failed schema validation")
		case d.Mock:
			return llm.Result{}, apperr.ExternalService("Mock LLM provider has no fixture for structured output 'OnboardingTour'")
		}
		return llm.Result{Data: d.Data, TokensIn: d.TokensIn, TokensOut: d.TokensOut, CostUSD: d.CostUsd}, nil
	}
}

func checkError(t *testing.T, want *errorDoc, got error) {
	t.Helper()
	ae, ok := apperr.As(got)
	if !ok {
		t.Fatalf("error = %v, want an apperr %s/%s", got, want.Kind, want.Code)
	}
	if ae.Kind.String() != want.Kind || ae.Code != want.Code || ae.Message != want.Message {
		t.Errorf("error = {%s %s %q}, want {%s %s %q}", ae.Kind, ae.Code, ae.Message, want.Kind, want.Code, want.Message)
	}
	var wantDetails any
	if len(want.Details) > 0 {
		wantDetails = jsonAny(t, want.Details)
	}
	var gotDetails any
	if ae.Details != nil {
		gotDetails = jsonAny(t, ae.Details)
	}
	if diff := cmp.Diff(wantDetails, gotDetails); diff != "" {
		t.Errorf("error details mismatch (-want +got):\n%s", diff)
	}
}

func checkLogs(t *testing.T, want []logDoc, buf *bytes.Buffer) {
	t.Helper()
	var got []logDoc
	dec := json.NewDecoder(buf)
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		obj := map[string]any{}
		for k, v := range line {
			if k != "time" && k != "level" && k != "msg" {
				obj[k] = v
			}
		}
		got = append(got, logDoc{Level: fmt.Sprint(line["level"]), Msg: fmt.Sprint(line["msg"]), Obj: obj})
	}
	if len(got) != len(want) {
		t.Fatalf("log lines = %d (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		// The duration comes from a logical clock in the golden: not parity data.
		delete(want[i].Obj, "durationMs")
		if _, ok := got[i].Obj["durationMs"]; !ok {
			t.Errorf("log line %d has no durationMs", i)
		}
		delete(got[i].Obj, "durationMs")
		if !equalFold(want[i].Level, got[i].Level) || want[i].Msg != got[i].Msg {
			t.Errorf("log line %d = %s %q, want %s %q", i, got[i].Level, got[i].Msg, want[i].Level, want[i].Msg)
		}
		if diff := cmp.Diff(want[i].Obj, got[i].Obj); diff != "" {
			t.Errorf("log line %d fields mismatch (-want +got):\n%s", i, diff)
		}
	}
}

func equalFold(a, b string) bool { return len(a) == len(b) && bytes.EqualFold([]byte(a), []byte(b)) }

func checkModelRequests(t *testing.T, f flow, r *rig) {
	t.Helper()
	if f.World.LLM.Error == "config" && len(f.LLMRequests) == 0 {
		// Node resolves the key before it builds a request; here the client makes the check, so the
		// call reaches it once and is rejected.
		if len(r.llm.requests) != 1 {
			t.Fatalf("model calls = %d, want the one the client rejects", len(r.llm.requests))
		}
		return
	}
	if len(r.llm.requests) != len(f.LLMRequests) {
		t.Fatalf("model requests = %d, want %d", len(r.llm.requests), len(f.LLMRequests))
	}
	for i, want := range f.LLMRequests {
		got := r.llm.requests[i]
		if string(got.Provider) != want.Provider || got.Model != want.Model || got.SchemaName != want.SchemaName ||
			got.MaxTokens != want.MaxTokens || got.Reprompts != want.MaxRetries ||
			got.Temperature == nil || *got.Temperature != want.Temp {
			t.Errorf("model request %d = {%s %s %s temp=%v max=%d reprompts=%d}, want %+v", i,
				got.Provider, got.Model, got.SchemaName, got.Temperature, got.MaxTokens, got.Reprompts, want)
		}
		if got.Feature != "onboarding" {
			t.Errorf("model request %d feature = %q, want onboarding", i, got.Feature)
		}
		var roles []string
		for _, m := range got.Messages {
			roles = append(roles, string(m.Role))
		}
		if diff := cmp.Diff(want.Roles, roles); diff != "" {
			t.Errorf("model request %d roles mismatch (-want +got):\n%s", i, diff)
		}
		if got.Messages[0].Content != domain.SystemPrompt {
			t.Errorf("model request %d: system message is not the Node constant", i)
		}
		if diff := cmp.Diff(want.User, got.Messages[1].Content); diff != "" {
			t.Errorf("model request %d user message mismatch (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(string(domain.SchemaJSON), string(got.Schema)); diff != "" {
			t.Errorf("model request %d schema mismatch (-want +got):\n%s", i, diff)
		}
	}
}

func checkWrites(t *testing.T, f flow, r *rig, state domain.State) {
	t.Helper()
	if len(r.store.saved) != len(f.Upserts) {
		t.Fatalf("saved tours = %d, want %d", len(r.store.saved), len(f.Upserts))
	}
	for i := range f.Upserts {
		if f.Upserts[i] != "$response.tour" {
			t.Fatalf("golden upsert %q is not understood", f.Upserts[i])
		}
		if state.Tour == nil {
			t.Fatalf("saved a tour but returned none")
		}
		if diff := cmp.Diff(wireOf(t, *state.Tour), wireOf(t, r.store.saved[i])); diff != "" {
			t.Errorf("saved tour differs from the response (-response +saved):\n%s", diff)
		}
		if !r.store.saved[i].GeneratedAt.Equal(state.Tour.GeneratedAt) {
			t.Errorf("saved generated_at = %v, response %v", r.store.saved[i].GeneratedAt, state.Tour.GeneratedAt)
		}
	}
	if f.Call != "generate" {
		return
	}
	var want any
	switch s := string(f.StoredAfter); {
	case s == "" || s == "null":
	case s == `"$response.tour"`:
		want = wireOf(t, *state.Tour)
	default:
		var d tourDoc
		if err := json.Unmarshal(f.StoredAfter, &d); err != nil {
			t.Fatalf("decode storedAfter: %v", err)
		}
		want = wireOf(t, d.domain(t))
	}
	var got any
	if r.store.tour != nil {
		got = wireOf(t, *r.store.tour)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stored tour after the call mismatch (-want +got):\n%s", diff)
	}
}

func checkReads(t *testing.T, f flow, r *rig) {
	t.Helper()
	if len(f.ReadCalls) != 2 {
		return
	}
	var root, scan []string
	for _, c := range r.blobs.calls {
		if c.owner != f.ReadCalls[0].Repo.Owner || c.name != f.ReadCalls[0].Repo.Name || c.sha != f.ReadCalls[0].Sha || c.max != domain.MaxBlobBytes {
			t.Errorf("blob read = %+v, want %s/%s at %s capped at %d", c, f.ReadCalls[0].Repo.Owner, f.ReadCalls[0].Repo.Name, f.ReadCalls[0].Sha, domain.MaxBlobBytes)
		}
		if slices.Contains(domain.RootFilePaths(), c.path) {
			root = append(root, c.path)
		} else {
			scan = append(scan, c.path)
		}
	}
	slices.Sort(root)
	var wantRoot []string
	if err := json.Unmarshal(f.ReadCalls[0].Paths, &wantRoot); err != nil {
		t.Fatalf("decode root read list: %v", err)
	}
	slices.Sort(wantRoot)
	if diff := cmp.Diff(wantRoot, root); diff != "" {
		t.Errorf("root files read mismatch (-want +got):\n%s", diff)
	}
	// The ranked list is compact in the golden when long ({count, first, last}).
	var wantScan []string
	if err := json.Unmarshal(f.ReadCalls[1].Paths, &wantScan); err != nil {
		var compact struct {
			Count int      `json:"count"`
			First []string `json:"first"`
			Last  []string `json:"last"`
		}
		if err := json.Unmarshal(f.ReadCalls[1].Paths, &compact); err != nil {
			t.Fatalf("decode scan read list: %v", err)
		}
		if len(scan) != compact.Count {
			t.Errorf("ranked files read = %d, want %d", len(scan), compact.Count)
		}
		slices.Sort(scan)
		for _, p := range append(slices.Clone(compact.First), compact.Last...) {
			if _, found := slices.BinarySearch(scan, p); !found {
				t.Errorf("ranked file %q was not read", p)
			}
		}
		return
	}
	slices.Sort(scan)
	slices.Sort(wantScan)
	if diff := cmp.Diff(emptyIfNil(wantScan), emptyIfNil(scan)); diff != "" {
		t.Errorf("ranked files read mismatch (-want +got):\n%s", diff)
	}
}

// behaviour not in the goldens -------------------------------------------------------------------

func TestGenerate_ASecondCallForTheSameRepoIs409UntilTheFirstEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		entered, release := make(chan struct{}), make(chan struct{})
		r.llm.do = func(context.Context, llm.Request) (llm.Result, error) {
			entered <- struct{}{}
			<-release
			return llm.Result{Data: okOutput}, nil
		}
		r.build(readyState(), nil, fixedNow)

		first := make(chan error, 1)
		go func() {
			_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
			first <- err
		}()
		<-entered

		_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
		ae, ok := apperr.As(err)
		if !ok || ae.Kind != apperr.KindConflict || ae.Code != "generation_in_progress" ||
			ae.Message != "A tour is already being generated for this repository" {
			t.Fatalf("second Generate error = %v, want 409 generation_in_progress", err)
		}
		release <- struct{}{}
		if err := <-first; err != nil {
			t.Fatalf("first Generate: %v", err)
		}

		go func() { <-entered; release <- struct{}{} }()
		if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err != nil {
			t.Fatalf("Generate after the first ended: %v", err)
		}
	})
}

func TestGenerate_AFailedGenerationReleasesTheMarker(t *testing.T) {
	r := newRig(t)
	calls := 0
	r.llm.do = func(context.Context, llm.Request) (llm.Result, error) {
		calls++
		if calls == 1 {
			return llm.Result{}, errors.New("boom")
		}
		return llm.Result{Data: okOutput}, nil
	}
	r.build(readyState(), nil, fixedNow)
	if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err == nil {
		t.Fatal("first Generate = nil error, want the model failure")
	}
	if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err != nil {
		t.Fatalf("second Generate after a failure: %v", err)
	}
}

func TestGenerate_ErrorsDoNotCarryProviderText(t *testing.T) {
	r := newRig(t)
	r.llm.do = func(context.Context, llm.Request) (llm.Result, error) {
		return llm.Result{}, errors.New("provider echoed README secret-token-123")
	}
	r.build(readyState(), nil, fixedNow)
	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an apperr", err)
	}
	if ae.Message != "The model call failed" || ae.Details != nil {
		t.Errorf("error = %q details %v, want the generic message without details", ae.Message, ae.Details)
	}
	if bytes.Contains(r.logs.Bytes(), []byte("secret-token-123")) {
		t.Errorf("the outcome log line carries provider text: %s", r.logs.String())
	}
}

func TestGenerate_AMissingKeyNamesTheProvider(t *testing.T) {
	r := newRig(t)
	r.llm.do = func(context.Context, llm.Request) (llm.Result, error) {
		return llm.Result{}, apperr.Config("OPENAI_API_KEY is not configured")
	}
	bag := map[string]json.RawMessage{"feature_models": json.RawMessage(`{"onboarding":{"provider":"openai","model":"gpt-4.1"}}`)}
	r.build(readyState(), bag, fixedNow)
	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindValidation || ae.Code != "provider_not_configured" ||
		ae.Message != "The openai provider is not configured" {
		t.Fatalf("error = %v, want 422 provider_not_configured for openai", err)
	}
	if diff := cmp.Diff(map[string]string{"provider": "openai"}, ae.Details); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerate_AStoreFailureIsNotAModelFailure(t *testing.T) {
	r := newRig(t)
	r.store.saveErr = errors.New("db down")
	r.build(readyState(), nil, fixedNow)
	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	if err == nil {
		t.Fatal("Generate = nil error, want the store failure")
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("error = %v, want a plain error (500 internal_error)", err)
	}
	if !bytes.Contains(r.logs.Bytes(), []byte(`"outcome":"internal_error"`)) {
		t.Errorf("outcome line = %s, want outcome internal_error", r.logs.String())
	}
}

func TestGenerate_ReadsAreBoundedPerList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		var ranked []string
		files := map[string]string{}
		for i := range 60 {
			p := fmt.Sprintf("src/f%03d.ts", i)
			ranked = append(ranked, p)
			files[p] = "// TODO " + p
		}
		for _, p := range domain.RootFilePaths() {
			files[p] = "x"
		}
		r.index.ranked, r.index.indexed = ranked, ranked
		r.blobs.files = files
		r.blobs.delay = time.Second
		r.build(readyState(), nil, fixedNow)
		if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := r.blobs.peak[false]; got != domain.MaxBlobReaders {
			t.Errorf("peak concurrent ranked reads = %d, want %d", got, domain.MaxBlobReaders)
		}
		if got := r.blobs.peak[true]; got != domain.MaxBlobReaders {
			t.Errorf("peak concurrent root reads = %d, want %d", got, domain.MaxBlobReaders)
		}
		if got, want := len(r.blobs.calls), len(domain.RootFilePaths())+len(ranked); got != want {
			t.Errorf("blob reads = %d, want %d", got, want)
		}
	})
}

func TestGenerate_NoReadStartsAfterTheBudgetEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		var ranked []string
		for i := range 100 {
			ranked = append(ranked, fmt.Sprintf("src/f%03d.ts", i))
		}
		r.index.ranked, r.index.indexed = ranked, ranked
		r.blobs.delay = time.Second
		r.budget = 2500 * time.Millisecond
		r.build(readyState(), nil, fixedNow)
		_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
		ae, ok := apperr.As(err)
		if !ok || ae.Code != "generation_failed" || ae.Message != "Generation timed out" {
			t.Fatalf("Generate error = %v, want 502 generation_failed \"Generation timed out\"", err)
		}
		// 8 workers per list start a read at t=0, 1s and 2s (the budget ends at 2.5s).
		if got, want := len(r.blobs.calls), 3*domain.MaxBlobReaders+3*8; got > want {
			t.Errorf("blob reads = %d, want at most %d", got, want)
		}
		if len(r.llm.requests) != 0 {
			t.Errorf("model requests = %d, want 0 after a budget expiry in the reads", len(r.llm.requests))
		}
		if len(r.store.saved) != 0 {
			t.Errorf("saved tours = %d, want 0", len(r.store.saved))
		}
	})
}

func TestGenerate_APanicInABlobReadReachesTheCallerAndReleasesTheMarker(t *testing.T) {
	r := newRig(t)
	r.blobs.files = map[string]string{"README.md": "x", "src/a.ts": "y"}
	r.blobs.onRead = func(path string) {
		if path == "src/b.ts" {
			panic("boom in a read")
		}
	}
	r.build(readyState(), nil, fixedNow)

	func() {
		defer func() {
			if v := recover(); v != "boom in a read" {
				t.Errorf("recovered %v, want the worker's panic re-raised on the caller", v)
			}
		}()
		_, _ = r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	}()
	if !bytes.Contains(r.logs.Bytes(), []byte(`"outcome":"internal_error"`)) {
		t.Errorf("outcome line = %s, want one with outcome internal_error", r.logs.String())
	}

	r.blobs.onRead = nil
	if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err != nil {
		t.Fatalf("Generate after a panic: %v (marker not released?)", err)
	}
}

func TestShutdown_CancelsARunningGenerationAndWaitsForIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		entered := make(chan struct{})
		r.llm.do = func(ctx context.Context, _ llm.Request) (llm.Result, error) {
			close(entered)
			<-ctx.Done()
			return llm.Result{}, fmt.Errorf("structured call was cancelled: %w", ctx.Err())
		}
		r.build(readyState(), nil, fixedNow)

		done := make(chan error, 1)
		go func() {
			_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
			done <- err
		}()
		<-entered

		sctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := r.svc.Shutdown(sctx); err != nil {
			t.Fatalf("Shutdown = %v, want nil once the generation returned", err)
		}
		err := <-done
		ae, ok := apperr.As(err)
		if !ok || ae.Code != "generation_failed" {
			t.Fatalf("Generate error = %v, want 502 generation_failed", err)
		}
		if len(r.store.saved) != 0 {
			t.Errorf("saved tours = %d, want 0 for a cancelled generation", len(r.store.saved))
		}
	})
}

func TestGet_ARepoOutsideTheWorkspaceIs404(t *testing.T) {
	r := newRig(t)
	r.build(readyState(), nil, fixedNow)
	_, err := r.svc.Get(t.Context(), "another-workspace", r.repo.ID)
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindNotFound || ae.Code != "repo_not_found" || ae.Message != "Repo not found" {
		t.Fatalf("Get error = %v, want 404 repo_not_found", err)
	}
}

// fakeSpend is the budget port: it records every Admit call and answers err.
type fakeSpend struct {
	mu    sync.Mutex
	err   error
	calls []admitCall
}

type admitCall struct{ workspaceID, op string }

func (b *fakeSpend) Admit(_ context.Context, workspaceID, op string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, admitCall{workspaceID, op})
	return b.err
}

func (b *fakeSpend) setErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = err
}

func capReached() error {
	return apperr.PaymentRequired("Monthly spend cap of $50.00 reached").WithCode("spend_cap_reached")
}

func wantAppErr(t *testing.T, err error, kind apperr.Kind, code string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != kind || ae.Code != code {
		t.Fatalf("error = %v, want {%v %q}", err, kind, code)
	}
}

func TestGenerate_SpendCap_ReachedIs402AndStartsNothing(t *testing.T) {
	r := newRig(t)
	spend := &fakeSpend{err: capReached()}
	r.spend = spend
	r.build(readyState(), nil, fixedNow)

	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached")
	if len(r.llm.requests) != 0 || len(r.store.saved) != 0 || len(r.index.rankedAsked) != 0 {
		t.Errorf("model calls, saved tours, index reads = %d, %d, %d, want none",
			len(r.llm.requests), len(r.store.saved), len(r.index.rankedAsked))
	}
	if r.logs.Len() != 0 {
		t.Errorf("log output = %q, want none (no generation started)", r.logs.String())
	}
	// No marker was left behind: once the cap is lifted the same repository generates.
	spend.setErr(nil)
	if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err != nil {
		t.Errorf("Generate() after the refusal = %v, want a tour (the 402 left a marker)", err)
	}
}

func TestGenerate_SpendCap_ReadErrorFailsClosedWith500(t *testing.T) {
	r := newRig(t)
	boom := errors.New("ledger down")
	r.spend = &fakeSpend{err: boom}
	r.build(readyState(), nil, fixedNow)

	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	if !errors.Is(err, boom) {
		t.Fatalf("Generate() error = %v, want the Admit error unchanged", err)
	}
	if _, isApp := apperr.As(err); isApp {
		t.Errorf("Generate() error = %v, want a plain error that httpx renders as 500", err)
	}
	if len(r.llm.requests) != 0 || len(r.store.saved) != 0 {
		t.Errorf("model calls, saved tours = %d, %d, want none", len(r.llm.requests), len(r.store.saved))
	}
}

func TestGenerate_SpendCap_NilBudgetChangesNothing(t *testing.T) {
	r := newRig(t)
	r.build(readyState(), nil, fixedNow)
	got, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	if err != nil || got.Tour == nil {
		t.Fatalf("Generate() = %+v, %v, want a tour", got, err)
	}
}

func TestGenerate_SpendCap_CheckedOnceWithTheOperation(t *testing.T) {
	r := newRig(t)
	spend := &fakeSpend{}
	r.spend = spend
	r.build(readyState(), nil, fixedNow)
	if _, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if diff := cmp.Diff([]admitCall{{"ws-1", "onboarding_generate"}}, spend.calls, cmp.AllowUnexported(admitCall{})); diff != "" {
		t.Errorf("Admit calls mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerate_SpendCap_UnknownRepoIs404NotChecked(t *testing.T) {
	r := newRig(t)
	spend := &fakeSpend{err: capReached()}
	r.spend = spend
	r.build(readyState(), nil, fixedNow)
	_, err := r.svc.Generate(t.Context(), "another-workspace", r.repo.ID)
	wantAppErr(t, err, apperr.KindNotFound, "repo_not_found")
	if len(spend.calls) != 0 {
		t.Errorf("Admit calls = %v, want none: the 404 comes first", spend.calls)
	}
}

// A7: the cap check precedes the marker, so a reached cap wins over index_not_ready, and the
// index check (which runs after the marker) is not reached on a 402.
func TestGenerate_SpendCap_WinsOverIndexNotReady(t *testing.T) {
	r := newRig(t)
	spend := &fakeSpend{err: capReached()}
	r.spend = spend
	r.build(domain.IndexState{}, nil, fixedNow)

	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached")

	spend.setErr(nil)
	_, err = r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	wantAppErr(t, err, apperr.KindValidation, "index_not_ready")
}

func TestGenerate_SpendCap_WinsOverTheConflict(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		spend := &fakeSpend{}
		r.spend = spend
		entered, release := make(chan struct{}), make(chan struct{})
		r.llm.do = func(context.Context, llm.Request) (llm.Result, error) {
			entered <- struct{}{}
			<-release
			return llm.Result{Data: okOutput}, nil
		}
		r.build(readyState(), nil, fixedNow)

		first := make(chan error, 1)
		go func() {
			_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
			first <- err
		}()
		<-entered

		spend.setErr(capReached())
		_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
		wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached")

		spend.setErr(nil)
		_, err = r.svc.Generate(t.Context(), r.ws, r.repo.ID)
		wantAppErr(t, err, apperr.KindConflict, "generation_in_progress")

		release <- struct{}{}
		if err := <-first; err != nil {
			t.Fatalf("running Generate() error = %v, want it to finish: a reached cap does not stop admitted work", err)
		}
	})
}
