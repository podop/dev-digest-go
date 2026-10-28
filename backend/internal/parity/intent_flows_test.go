package parity_test

// S30 (AC5022, AC5025, AC5028 flows; EC5005–EC5007 scenarios): the intent service against the
// flows and scenarios of testdata/goldens/intent.json. This file needs intent/app.NewService
// (lane M, S23); the pure rules are in intent_test.go. The flows run the real service over fake
// ports built from the golden's world; the Node store is emulated (numeric(12,6) rounds the cost
// to six decimals). The scenarios drive single flight, joining, cancellation and the budget
// inside a testing/synctest bubble with a gated model call.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// --- the golden's wire shapes --------------------------------------------------------------

type sourceWire struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

// recordWire is a stored derivation in the golden's (snake_case) record shape.
type recordWire struct {
	PRID          string       `json:"pr_id"`
	Intent        string       `json:"intent"`
	InScope       []string     `json:"in_scope"`
	OutOfScope    []string     `json:"out_of_scope"`
	ChangeType    *string      `json:"change_type"`
	Confidence    *string      `json:"confidence"`
	DerivedFrom   string       `json:"derived_from"`
	Sources       []sourceWire `json:"sources"`
	HeadSHA       string       `json:"head_sha"`
	InputHash     string       `json:"input_hash"`
	PromptVersion int          `json:"prompt_version"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	TokensIn      int          `json:"tokens_in"`
	TokensOut     int          `json:"tokens_out"`
	CostUSD       *float64     `json:"cost_usd"`
	DerivedAt     string       `json:"derived_at"`
}

// intentWire is the intent a review uses (the record without its key material and usage).
type intentWire struct {
	Intent      string       `json:"intent"`
	InScope     []string     `json:"in_scope"`
	OutOfScope  []string     `json:"out_of_scope"`
	ChangeType  *string      `json:"change_type"`
	Confidence  *string      `json:"confidence"`
	DerivedFrom string       `json:"derived_from"`
	Sources     []sourceWire `json:"sources"`
}

// upsertWire is what the store is asked to write, in the camelCase of the Node row.
type upsertWire struct {
	PRID          string       `json:"prId"`
	Intent        string       `json:"intent"`
	InScope       []string     `json:"inScope"`
	OutOfScope    []string     `json:"outOfScope"`
	ChangeType    *string      `json:"changeType"`
	Confidence    *string      `json:"confidence"`
	DerivedFrom   string       `json:"derivedFrom"`
	Sources       []sourceWire `json:"sources"`
	HeadSHA       string       `json:"headSha"`
	InputHash     string       `json:"inputHash"`
	PromptVersion int          `json:"promptVersion"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	TokensIn      int          `json:"tokensIn"`
	TokensOut     int          `json:"tokensOut"`
	CostUSD       *float64     `json:"costUsd"`
	DerivedAt     string       `json:"derivedAt"`
}

const isoMillis = "2006-01-02T15:04:05.000Z"

func sourcesWire(in []reviewer.IntentSource) []sourceWire {
	out := []sourceWire{}
	for _, s := range in {
		out = append(out, sourceWire{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
	}
	return out
}

func recordToWire(r domain.Record) recordWire {
	return recordWire{
		PRID: r.PRID, Intent: r.Text, InScope: r.InScope, OutOfScope: r.OutOfScope, ChangeType: r.ChangeType,
		Confidence: r.Confidence, DerivedFrom: r.DerivedFrom, Sources: sourcesWire(r.Sources), HeadSHA: r.HeadSHA,
		InputHash: r.InputHash, PromptVersion: r.PromptVersion, Provider: r.Provider, Model: r.Model,
		TokensIn: r.TokensIn, TokensOut: r.TokensOut, CostUSD: r.CostUSD, DerivedAt: r.DerivedAt.UTC().Format(isoMillis),
	}
}

func recordToUpsert(r domain.Record) upsertWire {
	return upsertWire{
		PRID: r.PRID, Intent: r.Text, InScope: r.InScope, OutOfScope: r.OutOfScope, ChangeType: r.ChangeType,
		Confidence: r.Confidence, DerivedFrom: r.DerivedFrom, Sources: sourcesWire(r.Sources), HeadSHA: r.HeadSHA,
		InputHash: r.InputHash, PromptVersion: r.PromptVersion, Provider: r.Provider, Model: r.Model,
		TokensIn: r.TokensIn, TokensOut: r.TokensOut, CostUSD: r.CostUSD, DerivedAt: r.DerivedAt.UTC().Format(isoMillis),
	}
}

func (w recordWire) record(t testing.TB) domain.Record {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, w.DerivedAt)
	if err != nil {
		t.Fatalf("derived_at %q: %v", w.DerivedAt, err)
	}
	r := domain.Record{
		PRID: w.PRID, Text: w.Intent, InScope: w.InScope, OutOfScope: w.OutOfScope, ChangeType: w.ChangeType,
		Confidence: w.Confidence, DerivedFrom: w.DerivedFrom, HeadSHA: w.HeadSHA, InputHash: w.InputHash,
		PromptVersion: w.PromptVersion, Provider: w.Provider, Model: w.Model, TokensIn: w.TokensIn,
		TokensOut: w.TokensOut, CostUSD: w.CostUSD, DerivedAt: at,
	}
	for _, s := range w.Sources {
		r.Sources = append(r.Sources, reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
	}
	return r
}

type worldPull struct {
	ID      string `json:"id"`
	RepoID  string `json:"repoId"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"headSha"`
}

func (p worldPull) domain() domain.Pull {
	return domain.Pull{ID: p.ID, RepoID: p.RepoID, Number: p.Number, Title: p.Title, Body: p.Body, Branch: p.Branch, HeadSHA: p.HeadSHA}
}

type world struct {
	Workspace   string `json:"workspace"`
	Now         string `json:"now"`
	PullMissing bool   `json:"pullMissing"`
	Pull        worldPull
	Repo        *struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repo"`
	Commits []struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
	} `json:"commits"`
	ChangedFiles []string `json:"changedFiles"`
	DiffExcerpt  *string  `json:"diffExcerpt"`
	Model        struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Error    string `json:"error"`
	} `json:"model"`
	Tickets map[string]json.RawMessage `json:"tickets"`
	Docs    map[string]json.RawMessage `json:"docs"`
	LLM     struct {
		Data      json.RawMessage `json:"data"`
		TokensIn  int             `json:"tokensIn"`
		TokensOut int             `json:"tokensOut"`
		CostUSD   *float64        `json:"costUsd"`
		Error     string          `json:"error"`
	} `json:"llm"`
}

// --- fake ports over the world -------------------------------------------------------------

type fakePulls struct{ w *world }

func (f fakePulls) Pull(_ context.Context, _, prID string) (domain.Pull, error) {
	if f.w.PullMissing || prID != f.w.Pull.ID {
		return domain.Pull{}, domain.ErrNotFound
	}
	return f.w.Pull.domain(), nil
}

func (f fakePulls) Repo(context.Context, string) (domain.Repo, error) {
	if f.w.Repo == nil {
		return domain.Repo{}, domain.ErrNotFound
	}
	return domain.Repo{ID: f.w.Repo.ID, Owner: f.w.Repo.Owner, Name: f.w.Repo.Name}, nil
}

func (f fakePulls) Commits(context.Context, string) ([]domain.Commit, error) {
	var out []domain.Commit
	for _, c := range f.w.Commits {
		out = append(out, domain.Commit{SHA: c.SHA, Message: c.Message})
	}
	return out, nil
}

func (f fakePulls) ChangedFiles(context.Context, string) ([]string, error) {
	return append([]string(nil), f.w.ChangedFiles...), nil
}

type fakeSettings struct{ w *world }

func (f fakeSettings) Get(context.Context, string) (map[string]json.RawMessage, error) {
	if f.w.Model.Error != "" {
		return nil, errors.New(f.w.Model.Error)
	}
	choice, _ := json.Marshal(map[string]any{"review_intent": map[string]string{"provider": f.w.Model.Provider, "model": f.w.Model.Model}})
	return map[string]json.RawMessage{"feature_models": choice}, nil
}

// readLog records what the service read, goroutine safe.
type readLog struct {
	mu      sync.Mutex
	tickets []float64
	docs    []docRead
}

type docRead struct {
	SHA  string `json:"sha"`
	Path string `json:"path"`
}

func (l *readLog) ticket(n float64) { l.mu.Lock(); l.tickets = append(l.tickets, n); l.mu.Unlock() }
func (l *readLog) doc(sha, path string) {
	l.mu.Lock()
	l.docs = append(l.docs, docRead{sha, path})
	l.mu.Unlock()
}

type fakeTickets struct {
	w   *world
	log *readLog
}

func (f fakeTickets) Read(ctx context.Context, _ domain.Repo, number float64) app.TicketRead {
	f.log.ticket(number)
	raw, ok := f.w.Tickets[reviewer.JSNumber(number)]
	if !ok {
		return app.TicketRead{Reason: "Not Found"}
	}
	if string(raw) == `"hang"` {
		<-ctx.Done()
		return app.TicketRead{Reason: "unreachable"}
	}
	var e struct {
		OK     bool    `json:"ok"`
		Title  string  `json:"title"`
		Body   *string `json:"body"`
		Reason string  `json:"reason"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		panic(err)
	}
	// One golden case scripts a null body at the Node TicketSource port, and the JavaScript
	// template literal `${title}\n\n${body}` then prints "null". The real GitHubTicketSource never
	// answers null (`issue.body ?? ''`) and Go's port cannot (Body is "" for an issue without
	// one), so the fake spells the scripted null the way JavaScript prints it.
	body := "null"
	if e.Body != nil {
		body = *e.Body
	}
	return app.TicketRead{OK: e.OK, Title: e.Title, Body: body, Reason: e.Reason}
}

type fakeDocs struct {
	w   *world
	log *readLog
}

func (f fakeDocs) Read(ctx context.Context, _ domain.Repo, headSHA, path string) app.DocRead {
	f.log.doc(headSHA, path)
	raw, ok := f.w.Docs[path]
	if !ok {
		return app.DocRead{Reason: "not found"}
	}
	if string(raw) == `"hang"` {
		<-ctx.Done()
		return app.DocRead{Reason: "unreachable"}
	}
	var e struct {
		OK        bool   `json:"ok"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		panic(err)
	}
	return app.DocRead{OK: e.OK, Content: e.Content, Truncated: e.Truncated, Reason: e.Reason}
}

// fakeStore emulates the pr_intent table: one row, cost rounded like numeric(12,6).
type fakeStore struct {
	mu      sync.Mutex
	row     *domain.Record
	upserts []domain.Record
}

func (s *fakeStore) Get(context.Context, string) (domain.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil {
		return domain.Record{}, false, nil
	}
	return *s.row, true, nil
}

func (s *fakeStore) Upsert(_ context.Context, rec domain.Record) (domain.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, rec)
	stored := rec
	if rec.CostUSD != nil {
		rounded, err := strconv.ParseFloat(reviewer.ToFixed(*rec.CostUSD, 6), 64)
		if err != nil {
			panic(err)
		}
		stored.CostUSD = &rounded
	}
	s.row = &stored
	return stored, nil
}

// fakeLLM records the requests and answers from the world, or through gate when one is set.
type fakeLLM struct {
	w    *world
	mu   sync.Mutex
	reqs []llm.Request
	gate chan gateAnswer // nil = answer at once

	sawAbort    bool
	abortReason string
}

type gateAnswer struct {
	err error // nil = the world's answer
}

func (f *fakeLLM) result() (llm.Result, error) {
	if f.w.LLM.Error != "" {
		return llm.Result{}, errors.New(f.w.LLM.Error)
	}
	return llm.Result{Data: f.w.LLM.Data, TokensIn: f.w.LLM.TokensIn, TokensOut: f.w.LLM.TokensOut, CostUSD: f.w.LLM.CostUSD, Attempts: 1}, nil
}

func (f *fakeLLM) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.gate == nil {
		return f.result()
	}
	select {
	case a := <-f.gate:
		if a.err != nil {
			return llm.Result{}, a.err
		}
		return f.result()
	case <-ctx.Done():
		f.mu.Lock()
		f.sawAbort, f.abortReason = true, jsReason(context.Cause(ctx))
		f.mu.Unlock()
		return llm.Result{}, context.Cause(ctx)
	}
}

// jsReason is the message JavaScript shows for an abort reason: the default AbortError for a bare
// cancellation.
func jsReason(cause error) string {
	if errors.Is(cause, context.Canceled) && errors.Unwrap(cause) == nil {
		return "This operation was aborted"
	}
	return cause.Error()
}

type flowRig struct {
	svc   *app.Service
	store *fakeStore
	llm   *fakeLLM
	reads *readLog
	w     *world
	now   time.Time
}

func newRig(t testing.TB, w *world, opts app.Options) *flowRig {
	t.Helper()
	now, err := time.Parse(time.RFC3339Nano, w.Now)
	if err != nil {
		t.Fatalf("world.now %q: %v", w.Now, err)
	}
	r := &flowRig{store: &fakeStore{}, llm: &fakeLLM{w: w}, reads: &readLog{}, w: w, now: now}
	r.svc = app.NewService(app.Deps{
		Pulls: fakePulls{w}, Settings: fakeSettings{w}, LLM: r.llm, Docs: fakeDocs{w, r.reads},
		Tickets: fakeTickets{w, r.reads}, Store: r.store, Clock: func() time.Time { return now },
		Log: slog.New(slog.DiscardHandler),
	}, opts)
	return r
}

func (r *flowRig) input() app.ResolveInput {
	in := app.ResolveInput{WorkspaceID: r.w.Workspace, Pull: r.w.Pull.domain(), ChangedFiles: r.w.ChangedFiles}
	if r.w.Repo != nil {
		in.Repo = domain.Repo{ID: r.w.Repo.ID, Owner: r.w.Repo.Owner, Name: r.w.Repo.Name}
	}
	if r.w.DiffExcerpt != nil {
		in.DiffExcerpt = *r.w.DiffExcerpt
	}
	return in
}

// --- event recording -----------------------------------------------------------------------

type eventLine struct {
	Kind string          `json:"kind"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data,omitempty"`
}

type eventSink struct {
	mu     sync.Mutex
	events []eventLine
}

func (s *eventSink) put(kind, msg string, data json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, eventLine{Kind: kind, Msg: msg, Data: data})
}

func (s *eventSink) lines(t testing.TB) []eventLine {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]eventLine{}, s.events...)
}

func eventsValue(t testing.TB, in []eventLine) any {
	t.Helper()
	return toValue(t, append([]eventLine{}, in...))
}

// --- the prompt_assembled line --------------------------------------------------------------

type promptSection struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Role   string `json:"role"`
	Trust  string `json:"trust"`
	Chars  int    `json:"chars"`
	Tokens int    `json:"tokens"`
	Items  *int   `json:"items,omitempty"`
}

type promptLine struct {
	Feature       string          `json:"feature"`
	CorrelationID string          `json:"correlationId"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	Sections      []promptSection `json:"sections"`
}

// promptLineOf is the content-free line llm.Client logs for a request (internal/llm/promptlog.go):
// the role of a section is "system" for one named system, else "user"; tokens are chars/4 rounded
// up. TestIntentGolden_PromptLineMirrorsTheClient proves it against the real client.
func promptLineOf(req llm.Request) promptLine {
	line := promptLine{Feature: req.Feature, CorrelationID: req.CorrelationID, Provider: string(req.Provider), Model: req.Model}
	for _, s := range req.Sections {
		role := s.Role
		if role == "" {
			role = "user"
			if s.Name == "system" {
				role = "system"
			}
		}
		line.Sections = append(line.Sections, promptSection{
			Name: s.Name, Source: s.Source, Role: role, Trust: s.Trust, Chars: s.Chars, Tokens: (s.Chars + 3) / 4, Items: s.Items,
		})
	}
	return line
}

func TestIntentGolden_PromptLineMirrorsTheClient(t *testing.T) {
	var logged bytes.Buffer
	ad := &scriptedAdapter{outputs: []string{`{}`}}
	c := llm.New(llm.Options{
		Keys:     keyMap{"OPENROUTER_API_KEY": "sk-test-key"},
		Adapters: map[llm.Provider]llm.Adapter{llm.OpenRouter: ad},
		Logger:   slog.New(slog.NewJSONHandler(&logged, nil)),
		Prices:   nilPricer{},
	})
	t.Cleanup(c.Close)
	items := 3
	req := llm.Request{
		Provider: llm.OpenRouter, Model: "m", SchemaName: "S", Schema: json.RawMessage(`{}`), Feature: "intent", CorrelationID: "intent:p:abc",
		Messages: []llm.Message{{Role: llm.System, Content: "S"}, {Role: llm.User, Content: "U"}},
		Sections: []llm.PromptSection{
			{Name: "system", Source: "engine", Trust: llm.Trusted, Chars: 1246},
			{Name: "task", Source: "engine", Trust: llm.Trusted, Chars: 114},
			{Name: "intent_sources", Source: "repo", Trust: llm.Untrusted, Chars: 325, Items: &items},
		},
	}
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	var got promptLine
	for _, l := range bytes.Split(bytes.TrimSpace(logged.Bytes()), []byte("\n")) {
		var rec struct {
			Msg string `json:"msg"`
			promptLine
		}
		if err := json.Unmarshal(l, &rec); err != nil {
			t.Fatalf("log line %s: %v", l, err)
		}
		if rec.Msg == "prompt_assembled" {
			got = rec.promptLine
		}
	}
	if diff := cmp.Diff(promptLineOf(req), got); diff != "" {
		t.Errorf("the client's prompt_assembled line differs from the mirror (-mirror +client):\n%s", diff)
	}
}

// --- AC5022 / AC5025 / AC5028: the flows ----------------------------------------------------

type flowCase struct {
	Name   string      `json:"name"`
	Call   string      `json:"call"`
	Force  bool        `json:"force"`
	World  world       `json:"world"`
	Stored *recordWire `json:"stored"`
	Result *struct {
		Status  string      `json:"status"`
		Intent  *intentWire `json:"intent"`
		Record  *recordWire `json:"record"`
		Warning string      `json:"warning"`
	} `json:"result"`
	Response *struct {
		Intent *recordWire `json:"intent"`
		Stale  bool        `json:"stale"`
	} `json:"response"`
	Error *struct {
		Kind    string `json:"kind"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Events   []eventLine `json:"events"`
	LLMCalls []struct {
		Provider                string   `json:"provider"`
		Model                   string   `json:"model"`
		Roles                   []string `json:"roles"`
		SystemIsTheNodeConstant bool     `json:"systemIsTheNodeConstant"`
		User                    text     `json:"user"`
	} `json:"llmCalls"`
	TicketReads []float64    `json:"ticketReads"`
	DocReads    []docRead    `json:"docReads"`
	Upserts     []upsertWire `json:"upserts"`
	PromptLog   []promptLine `json:"promptLog"`
	StoredAfter *recordWire  `json:"storedAfter"`
}

func TestIntentGolden_Flows(t *testing.T) {
	var g struct {
		Flows []flowCase `json:"flows"`
	}
	loadGolden(t, "intent", &g)
	if len(g.Flows) < 60 {
		t.Fatalf("intent.json holds %d flows, want the 68-case corpus", len(g.Flows))
	}
	for _, tc := range g.Flows {
		t.Run(tc.Name, func(t *testing.T) {
			rig := newRig(t, &tc.World, app.Options{})
			if tc.Stored != nil {
				rec := tc.Stored.record(t)
				rig.store.row = &rec
			}
			sink := &eventSink{}
			ctx := t.Context()

			var gotResult *recordWire
			var gotIntent *intentWire
			var gotStatus, gotWarning string
			var gotResponse *struct {
				Intent *recordWire
				Stale  bool
			}
			var gotErr error
			switch tc.Call {
			case "resolve":
				in := rig.input()
				in.Force = tc.Force
				res := rig.svc.Resolve(ctx, in, sink.put)
				if res.Used {
					gotStatus = "used"
					rw := recordToWire(res.Record)
					gotResult = &rw
					snap := res.Record.Snapshot()
					gotIntent = &intentWire{
						Intent: snap.Text, InScope: snap.InScope, OutOfScope: snap.OutOfScope, ChangeType: snap.ChangeType,
						Confidence: snap.Confidence, DerivedFrom: snap.DerivedFrom, Sources: sourcesWire(snap.Sources),
					}
				} else {
					gotStatus, gotWarning = "unavailable", res.Warning
				}
			case "get", "refresh":
				var resp app.Response
				if tc.Call == "get" {
					resp, gotErr = rig.svc.Get(ctx, tc.World.Workspace, tc.World.Pull.ID)
				} else {
					resp, gotErr = rig.svc.Refresh(ctx, tc.World.Workspace, tc.World.Pull.ID)
				}
				if gotErr == nil {
					gotResponse = &struct {
						Intent *recordWire
						Stale  bool
					}{Stale: resp.Stale}
					if resp.Intent != nil {
						rw := recordToWire(*resp.Intent)
						gotResponse.Intent = &rw
					}
				}
			default:
				t.Fatalf("unknown call %q", tc.Call)
			}

			// Result or response or error.
			switch {
			case tc.Result != nil:
				if gotStatus != tc.Result.Status || gotWarning != tc.Result.Warning {
					t.Fatalf("Resolve() = %s %q, want %s %q", gotStatus, gotWarning, tc.Result.Status, tc.Result.Warning)
				}
				if tc.Result.Status == "used" {
					if diff := cmp.Diff(tc.Result.Record, gotResult, cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("Resolve() record mismatch (-want +got):\n%s", diff)
					}
					if diff := cmp.Diff(tc.Result.Intent, gotIntent, cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("Resolve() intent mismatch (-want +got):\n%s", diff)
					}
				}
			case tc.Response != nil:
				if gotErr != nil {
					t.Fatalf("%s() error = %v, want a response", tc.Call, gotErr)
				}
				if gotResponse.Stale != tc.Response.Stale {
					t.Errorf("%s() stale = %v, want %v", tc.Call, gotResponse.Stale, tc.Response.Stale)
				}
				if diff := cmp.Diff(tc.Response.Intent, gotResponse.Intent, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("%s() intent mismatch (-want +got):\n%s", tc.Call, diff)
				}
			case tc.Error != nil:
				ae, ok := apperr.As(gotErr)
				if !ok {
					t.Fatalf("%s() error = %v, want an apperr %s %q", tc.Call, gotErr, tc.Error.Kind, tc.Error.Message)
				}
				if ae.Kind.String() != tc.Error.Kind || ae.Code != tc.Error.Code || ae.Message != tc.Error.Message {
					t.Errorf("%s() error = {%s %s %q}, want {%s %s %q}", tc.Call, ae.Kind, ae.Code, ae.Message, tc.Error.Kind, tc.Error.Code, tc.Error.Message)
				}
			default:
				t.Fatal("the golden case has neither result, response nor error")
			}

			// Events.
			if diff := cmp.Diff(eventsValue(t, tc.Events), eventsValue(t, sink.lines(t)), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
			// Model calls: the messages and the prompt log line.
			if len(rig.llm.reqs) != len(tc.LLMCalls) {
				t.Fatalf("the model was called %d times, want %d", len(rig.llm.reqs), len(tc.LLMCalls))
			}
			for i, want := range tc.LLMCalls {
				req := rig.llm.reqs[i]
				roles := []string{}
				for _, m := range req.Messages {
					roles = append(roles, string(m.Role))
				}
				if string(req.Provider) != want.Provider || req.Model != want.Model || !cmp.Equal(roles, want.Roles) {
					t.Errorf("call %d = %s/%s %v, want %s/%s %v", i, req.Provider, req.Model, roles, want.Provider, want.Model, want.Roles)
				}
				if (req.Messages[0].Content == domain.SystemPrompt) != want.SystemIsTheNodeConstant {
					t.Errorf("call %d: system message is the Node constant = %v, want %v", i, !want.SystemIsTheNodeConstant, want.SystemIsTheNodeConstant)
				}
				checkText(t, "the user message of call "+strconv.Itoa(i), want.User, req.Messages[1].Content)
				if req.SchemaName != domain.SchemaName || req.MaxTokens != domain.MaxOutputTokens || req.Reprompts != domain.Reprompts ||
					req.Temperature == nil || *req.Temperature != domain.Temperature {
					t.Errorf("call %d parameters = %q %d %d %v, want the intent call's", i, req.SchemaName, req.MaxTokens, req.Reprompts, req.Temperature)
				}
			}
			var lines []promptLine
			for _, req := range rig.llm.reqs {
				lines = append(lines, promptLineOf(req))
			}
			if diff := cmp.Diff(tc.PromptLog, lines, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("prompt_assembled lines mismatch (-want +got):\n%s", diff)
			}
			// Reads, writes, final row.
			if diff := cmp.Diff(tc.TicketReads, rig.reads.tickets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ticket reads mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.DocReads, rig.reads.docs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("doc reads mismatch (-want +got):\n%s", diff)
			}
			var upserts []upsertWire
			for _, u := range rig.store.upserts {
				upserts = append(upserts, recordToUpsert(u))
			}
			if diff := cmp.Diff(tc.Upserts, upserts, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("store upserts mismatch (-want +got):\n%s", diff)
			}
			var after *recordWire
			if rig.store.row != nil {
				w := recordToWire(*rig.store.row)
				after = &w
			}
			if diff := cmp.Diff(tc.StoredAfter, after, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("stored row after the call mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- EC5005–EC5007: single flight, cancellation, budget ----------------------------------------

// signalAbortedBeforeTheCall is the scenario where Go differs from Node by design (see runScenario).
const signalAbortedBeforeTheCall = "a_signal_aborted_before_the_call_counts"

type scenarioCase struct {
	Name  string `json:"name"`
	World struct {
		Pull    worldPull                  `json:"pull"`
		Tickets map[string]json.RawMessage `json:"tickets"`
		Docs    map[string]json.RawMessage `json:"docs"`
		LLM     struct {
			Data      json.RawMessage `json:"data"`
			TokensIn  int             `json:"tokensIn"`
			TokensOut int             `json:"tokensOut"`
			CostUSD   *float64        `json:"costUsd"`
		} `json:"llm"`
	} `json:"world"`
	Callers map[string]struct {
		Events []eventLine `json:"events"`
		Result *struct {
			Status  string `json:"status"`
			Warning string `json:"warning"`
			Intent  *intentWire
		} `json:"result"`
		SignalAbortedAtEnd *bool `json:"signalAbortedAtEnd"`
	} `json:"callers"`
	Steps []struct {
		Step    map[string]any `json:"step"`
		Flights int            `json:"flights"`
	} `json:"steps"`
	LLMCalls       int       `json:"llmCalls"`
	LLMSawAbort    bool      `json:"llmSawAbort"`
	LLMAbortReason *string   `json:"llmAbortReason"`
	TicketReads    []float64 `json:"ticketReads"`
	DocReads       []string  `json:"docReads"`
	Upserts        int       `json:"upserts"`
	StoredRow      bool      `json:"storedRow"`
	FlightsLeft    int       `json:"flightsLeft"`
}

type scenarioScript struct {
	Name    string `json:"name"`
	Callers map[string]struct {
		Signal        bool `json:"signal"`
		AbortedBefore bool `json:"abortedBefore"`
		Force         bool `json:"force"`
		Pull          *struct {
			Title string `json:"title"`
		} `json:"pull"`
	} `json:"callers"`
}

type runningCaller struct {
	ctx    context.Context
	cancel context.CancelCauseFunc // nil: a caller without a signal
	sink   *eventSink
	done   chan struct{}
	res    app.Result
}

// nodeIntentBudget is Node's intent budget (constants.budgetMs of the golden); Go's domain.Budget
// is twice it since 2026-10-28.
const nodeIntentBudget = 120 * time.Second

func TestIntentGolden_Scenarios(t *testing.T) {
	var g struct {
		Scenarios []scenarioCase `json:"scenarios"`
	}
	loadGolden(t, "intent", &g)
	var script struct {
		Scenarios []scenarioScript `json:"scenarios"`
	}
	loadCorpus(t, "intent", "scenarios", &script)
	scripts := map[string]scenarioScript{}
	for _, s := range script.Scenarios {
		scripts[s.Name] = s
	}
	if len(g.Scenarios) == 0 {
		t.Fatal("intent.json has no scenarios")
	}

	for _, tc := range g.Scenarios {
		t.Run(tc.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runScenario(t, tc, scripts[tc.Name])
			})
		})
	}
}

func runScenario(t *testing.T, tc scenarioCase, sc scenarioScript) {
	t.Helper()
	// The golden carries the world of the scenario; everything else is the flows' default world.
	var defaults struct {
		Flows []struct {
			World world `json:"world"`
		} `json:"flows"`
	}
	loadGolden(t, "intent", &defaults)
	w := defaults.Flows[0].World
	w.Pull = tc.World.Pull
	w.Tickets, w.Docs = tc.World.Tickets, tc.World.Docs
	w.LLM.Data, w.LLM.TokensIn, w.LLM.TokensOut, w.LLM.CostUSD = tc.World.LLM.Data, tc.World.LLM.TokensIn, tc.World.LLM.TokensOut, tc.World.LLM.CostUSD
	w.LLM.Error = ""

	// The scenarios carry Node's budget in their messages, so they run with Node's budget.
	rig := newRig(t, &w, app.Options{Budget: nodeIntentBudget})
	rig.llm.gate = make(chan gateAnswer)
	callers := map[string]*runningCaller{}

	// flightsAfter lists the steps after which the golden's live-flight count is compared: the
	// ones that let the work settle (Node settles pending microtasks there).
	settles := func(step map[string]any) bool {
		for _, k := range []string{"settle", "release", "fire", "await"} {
			if _, ok := step[k]; ok {
				return true
			}
		}
		return false
	}

	for i, st := range tc.Steps {
		step := st.Step
		switch {
		case step["start"] != nil:
			name := step["start"].(string)
			opt := sc.Callers[name]
			rc := &runningCaller{sink: &eventSink{}, done: make(chan struct{})}
			if opt.Signal {
				rc.ctx, rc.cancel = context.WithCancelCause(context.Background())
				if opt.AbortedBefore {
					rc.cancel(context.Canceled)
				}
			} else {
				rc.ctx = context.Background()
			}
			callers[name] = rc
			in := rig.input()
			in.Force = opt.Force
			if opt.Pull != nil && opt.Pull.Title != "" {
				in.Pull.Title = opt.Pull.Title
			}
			go func() {
				defer close(rc.done)
				rc.res = rig.svc.Resolve(rc.ctx, in, rc.sink.put)
			}()
		case step["settle"] != nil:
			synctest.Wait()
		case step["abort"] != nil:
			rc := callers[step["abort"].(string)]
			if reason, ok := step["reason"].(string); ok {
				rc.cancel(errors.New(reason))
			} else {
				rc.cancel(context.Canceled)
			}
		case step["release"] != nil:
			a := gateAnswer{}
			if step["release"] == "fail" {
				a.err = errors.New(step["message"].(string))
			}
			rig.llm.gate <- a
			synctest.Wait()
		case step["fire"] != nil:
			time.Sleep(nodeIntentBudget + time.Second)
			synctest.Wait()
		case step["await"] != nil:
			<-callers[step["await"].(string)].done
		default:
			t.Fatalf("unknown step %v", step)
		}
		if settles(step) {
			if got := rig.svc.InFlight(); got != st.Flights {
				t.Errorf("step %d %v: %d live flights, want %d", i, step, got, st.Flights)
			}
		}
	}
	synctest.Wait()

	for name, want := range tc.Callers {
		rc := callers[name]
		if rc == nil {
			t.Fatalf("caller %s never started", name)
		}
		<-rc.done
		if diff := cmp.Diff(eventsValue(t, want.Events), eventsValue(t, rc.sink.lines(t)), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("caller %s events mismatch (-want +got):\n%s", name, diff)
		}
		if want.Result == nil {
			t.Errorf("caller %s has no golden result", name)
			continue
		}
		if rc.res.Used != (want.Result.Status == "used") || rc.res.Warning != want.Result.Warning {
			t.Errorf("caller %s result = used %v warning %q, want %s %q", name, rc.res.Used, rc.res.Warning, want.Result.Status, want.Result.Warning)
		}
		if want.SignalAbortedAtEnd != nil {
			if rc.cancel == nil {
				t.Errorf("caller %s has a signal in the golden but none here", name)
			} else if aborted := rc.ctx.Err() != nil; aborted != *want.SignalAbortedAtEnd {
				t.Errorf("caller %s context cancelled at the end = %v, want %v", name, aborted, *want.SignalAbortedAtEnd)
			}
		}
	}
	wantCalls, wantSawAbort, wantAbortReason := tc.LLMCalls, tc.LLMSawAbort, tc.LLMAbortReason
	if tc.Name == signalAbortedBeforeTheCall {
		// Deliberate difference (reported, not a mismatch to hide): Node starts the derivation of
		// a caller whose signal is already aborted, makes the model call, and that call sees the
		// abort (and leaves an unobserved rejection, README "Intent"). Go checks the caller's
		// context first and never starts the derivation: the caller's result, events, the absence
		// of a row and of live flights are those of the golden, but no provider call is spent.
		if tc.LLMCalls != 1 || !tc.LLMSawAbort {
			t.Fatalf("the golden of %s no longer shows the Node quirk (llmCalls %d, sawAbort %v): drop the deviation", tc.Name, tc.LLMCalls, tc.LLMSawAbort)
		}
		wantCalls, wantSawAbort, wantAbortReason = 0, false, nil
	}
	if got := len(rig.llm.reqs); got != wantCalls {
		t.Errorf("model calls = %d, want %d", got, wantCalls)
	}
	if rig.llm.sawAbort != wantSawAbort {
		t.Errorf("the model call saw an abort = %v, want %v", rig.llm.sawAbort, wantSawAbort)
	}
	if wantAbortReason != nil && rig.llm.abortReason != *wantAbortReason {
		t.Errorf("abort reason seen by the model call = %q, want %q", rig.llm.abortReason, *wantAbortReason)
	}
	if diff := cmp.Diff(tc.TicketReads, rig.reads.tickets, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("ticket reads mismatch (-want +got):\n%s", diff)
	}
	var docs []string
	for _, d := range rig.reads.docs {
		docs = append(docs, d.Path)
	}
	if diff := cmp.Diff(tc.DocReads, docs, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("doc reads mismatch (-want +got):\n%s", diff)
	}
	if len(rig.store.upserts) != tc.Upserts || (rig.store.row != nil) != tc.StoredRow {
		t.Errorf("store upserts = %d, row stored = %v, want %d, %v", len(rig.store.upserts), rig.store.row != nil, tc.Upserts, tc.StoredRow)
	}
	if got := rig.svc.InFlight(); got != tc.FlightsLeft {
		t.Errorf("live flights at the end = %d, want %d", got, tc.FlightsLeft)
	}
}
