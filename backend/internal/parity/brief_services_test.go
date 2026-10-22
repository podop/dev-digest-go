package parity_test

// S31 (AC5044 and the service rows of AC5009-AC5011, AC5045-AC5049): the brief use cases against
// the Node golden brief.json sections flows, scenarios and issueSource. The real brief/app Service
// runs over in-memory fakes of its ports (the golden's "world"); the comparison covers the
// response, the error, the log lines (durationMs is not parity data), the model requests, the
// prompt-log sections, the document calls and the stored briefs.
//
// Not covered here: `stored` (PrBrief.safeParse of stored documents). It needs the Postgres
// adapter, whose own test runs the golden's cases (brief/adapters/postgres store_test.go).
// Differences that are not parity gaps: the Node spec call carries the agent's skills, the Go
// port takes the agent id and the adapter finds the skills (only agentId, repo, base and head SHA
// are compared); Node's plain `Error` has no Go equivalent text (the wrapped cause is compared).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/brief/adapters/peers"
	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/github"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ---------------------------------------------------------------------------
// Shared: log capture, errors, prompt-log sections
// ---------------------------------------------------------------------------

// pbLogLine is a captured log line in the golden's shape.
type pbLogLine struct {
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Obj   map[string]any `json:"obj"`
}

// pbLogCapture is a slog.Handler that records every line.
type pbLogCapture struct {
	mu    *sync.Mutex
	lines *[]pbLogLine
	attrs []slog.Attr
}

func newPbLogCapture() *pbLogCapture {
	return &pbLogCapture{mu: &sync.Mutex{}, lines: &[]pbLogLine{}}
}

func (h *pbLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *pbLogCapture) Handle(_ context.Context, r slog.Record) error {
	obj := map[string]any{}
	for _, a := range h.attrs {
		obj[a.Key] = pbAttrValue(a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		obj[a.Key] = pbAttrValue(a.Value)
		return true
	})
	level := strings.ToLower(r.Level.String())
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.lines = append(*h.lines, pbLogLine{Level: level, Msg: r.Message, Obj: obj})
	return nil
}

func (h *pbLogCapture) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return &c
}

func (h *pbLogCapture) WithGroup(string) slog.Handler { return h }

// snapshot returns the lines so far.
func (h *pbLogCapture) snapshot() []pbLogLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pbLogLine(nil), *h.lines...)
}

func pbAttrValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		m := map[string]any{}
		for _, a := range v.Group() {
			m[a.Key] = pbAttrValue(a.Value)
		}
		return m
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return x.Error()
		default:
			return x
		}
	default:
		return v.Any()
	}
}

// pbLogsShape is the lines as generic JSON, durationMs removed (a logical clock in Node).
func pbLogsShape(t testing.TB, lines []pbLogLine) []any {
	t.Helper()
	out := []any{}
	for _, l := range lines {
		obj := map[string]any{}
		for k, v := range l.Obj {
			if k != "durationMs" {
				obj[k] = v
			}
		}
		out = append(out, pbGeneric(t, pbLogLine{Level: l.Level, Msg: l.Msg, Obj: obj}))
	}
	return out
}

// pbWantLogs is the golden's lines as generic JSON, durationMs removed. A model-request line of
// Node whose token counts are undefined (the golden world gave none) has no keys for them; Go's
// are integers, so the expectation reads them as 0.
func pbWantLogs(t testing.TB, lines []pbLogLine) []any {
	out := make([]pbLogLine, len(lines))
	for i, l := range lines {
		obj := map[string]any{}
		for k, v := range l.Obj {
			obj[k] = v
		}
		if l.Msg == "pr brief model request" {
			for _, k := range []string{"tokensIn", "tokensOut"} {
				if _, ok := obj[k]; !ok {
					obj[k] = float64(0)
				}
			}
		}
		out[i] = pbLogLine{Level: l.Level, Msg: l.Msg, Obj: obj}
	}
	return pbLogsShape(t, out)
}

// pbWantError is the golden's error.
type pbWantError struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details"`
}

// pbCheckError compares a Go error with the golden's. An application error (Node kinds
// validation, not_found, conflict, external_service) must match kind, code, message and details; a
// plain Node Error must not be an application error and must wrap cause.
func pbCheckError(t testing.TB, want *pbWantError, err error, cause error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Errorf("error = %v, want none", err)
		}
		return
	}
	if err == nil {
		t.Errorf("error = nil, want %s %q", want.Name, want.Message)
		return
	}
	ae, isApp := apperr.As(err)
	if want.Kind == "" {
		if isApp {
			t.Errorf("error = %v (kind %v), want a plain error (Node: %s %q)", err, ae.Kind, want.Name, want.Message)
		}
		if cause != nil && !errors.Is(err, cause) {
			t.Errorf("error = %v, want it to wrap %v", err, cause)
		}
		return
	}
	if !isApp {
		t.Errorf("error = %v, want an application error of kind %s (%s)", err, want.Kind, want.Message)
		return
	}
	if ae.Kind.String() != want.Kind || ae.Code != want.Code || ae.Message != want.Message {
		t.Errorf("error = %s/%s/%q, want %s/%s/%q", ae.Kind, ae.Code, ae.Message, want.Kind, want.Code, want.Message)
	}
	if want.Details != nil || ae.Details != nil {
		pbDiff(t, "error details", want.Details, pbGeneric(t, ae.Details))
	}
}

// pbSection is a prompt-log section in the golden's shape (role and tokens are derived by the LLM
// client from the section name and its length: ceil(chars / 4)).
type pbSection struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Role   string `json:"role"`
	Trust  string `json:"trust"`
	Chars  int    `json:"chars"`
	Tokens int    `json:"tokens"`
	Items  *int   `json:"items"`
}

type pbPromptLogEntry struct {
	Feature       string      `json:"feature"`
	CorrelationID string      `json:"correlationId"`
	Provider      string      `json:"provider"`
	Model         string      `json:"model"`
	Sections      []pbSection `json:"sections"`
}

// pbPromptLogOf is what the LLM client's prompt line would carry for a request.
func pbPromptLogOf(req llm.Request) pbPromptLogEntry {
	e := pbPromptLogEntry{Feature: req.Feature, CorrelationID: req.CorrelationID, Provider: string(req.Provider), Model: req.Model}
	for _, s := range req.Sections {
		role := s.Role
		if role == "" {
			role = "user"
			if s.Name == "system" {
				role = "system"
			}
		}
		e.Sections = append(e.Sections, pbSection{Name: s.Name, Source: s.Source, Role: role, Trust: string(s.Trust), Chars: s.Chars, Tokens: (s.Chars + 3) / 4, Items: s.Items})
	}
	return e
}

type pbLLMRequest struct {
	Provider                string   `json:"provider"`
	Model                   string   `json:"model"`
	SchemaName              string   `json:"schemaName"`
	Temperature             float64  `json:"temperature"`
	MaxTokens               int      `json:"maxTokens"`
	MaxRetries              int      `json:"maxRetries"`
	HasUsageObserver        bool     `json:"hasUsageObserver"`
	HasSignal               bool     `json:"hasSignal"`
	Roles                   []string `json:"roles"`
	SystemIsTheNodeConstant bool     `json:"systemIsTheNodeConstant"`
	User                    pbText   `json:"user"`
}

// ---------------------------------------------------------------------------
// Wire shape of a stored brief
// ---------------------------------------------------------------------------

func pbNonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func pbIntentWire(i *reviewer.Intent) any {
	if i == nil {
		return nil
	}
	sources := []map[string]any{}
	for _, s := range i.Sources {
		sources = append(sources, map[string]any{"kind": s.Kind, "ref": s.Ref, "status": s.Status, "detail": s.Detail})
	}
	return map[string]any{
		"intent": i.Text, "in_scope": pbNonNil(i.InScope), "out_of_scope": pbNonNil(i.OutOfScope),
		"change_type": i.ChangeType, "confidence": i.Confidence, "derived_from": i.DerivedFrom, "sources": sources,
	}
}

func pbBlastWire(b *domain.Blast) any {
	if b == nil {
		return nil
	}
	syms := []map[string]any{}
	for _, s := range b.ChangedSymbols {
		syms = append(syms, map[string]any{"name": s.Name, "file": s.File, "kind": s.Kind})
	}
	down := []map[string]any{}
	for _, g := range b.Downstream {
		callers := []map[string]any{}
		for _, c := range g.Callers {
			callers = append(callers, map[string]any{"name": c.Name, "file": c.File, "line": c.Line})
		}
		down = append(down, map[string]any{
			"symbol": g.Symbol, "callers": callers,
			"endpoints_affected": pbNonNil(g.EndpointsAffected), "crons_affected": pbNonNil(g.CronsAffected),
		})
	}
	m := map[string]any{"changed_symbols": syms, "downstream": down, "summary": b.Summary, "degraded": b.Degraded}
	if b.Reason != "" {
		m["reason"] = b.Reason
	}
	return m
}

// pbBriefWire is a stored brief as the contract (and Node's stored document) spells it.
func pbBriefWire(b *domain.Brief) any {
	if b == nil {
		return nil
	}
	risks := []map[string]any{}
	for _, r := range b.Risks {
		risks = append(risks, map[string]any{
			"kind": r.Kind, "title": r.Title, "explanation": r.Explanation, "severity": string(r.Severity), "file_refs": pbNonNil(r.FileRefs),
		})
	}
	focus := []map[string]any{}
	for _, f := range b.ReviewFocus {
		focus = append(focus, map[string]any{"file": f.File, "line": f.Line, "reason": f.Reason})
	}
	missing := []map[string]any{}
	for _, m := range b.MissingInputs {
		e := map[string]any{"input": string(m.Input), "reason": string(m.Reason)}
		if m.Detail != "" {
			e["detail"] = m.Detail
		}
		missing = append(missing, e)
	}
	return map[string]any{
		"summary": b.Summary, "risks": map[string]any{"risks": risks}, "review_focus": focus,
		"intent": pbIntentWire(b.Intent), "blast": pbBlastWire(b.Blast), "missing_inputs": missing,
		"specs_used": pbNonNil(b.SpecsUsed), "head_sha": b.HeadSHA, "generated_at": pbISO(b.GeneratedAt),
		"prompt_version": b.PromptVersion, "provider": b.Provider, "model": b.Model,
		"tokens_in": b.TokensIn, "tokens_out": b.TokensOut, "cost_usd": b.CostUSD, "model_requests": b.ModelRequests,
	}
}

// pbBriefFromWire reads a golden stored brief (a valid one) into the domain type.
func pbBriefFromWire(t testing.TB, raw json.RawMessage) *domain.Brief {
	t.Helper()
	if string(raw) == "null" || len(raw) == 0 {
		return nil
	}
	var w struct {
		Summary string `json:"summary"`
		Risks   struct {
			Risks []struct {
				Kind        string   `json:"kind"`
				Title       string   `json:"title"`
				Explanation string   `json:"explanation"`
				Severity    string   `json:"severity"`
				FileRefs    []string `json:"file_refs"`
			} `json:"risks"`
		} `json:"risks"`
		Focus []struct {
			File   string `json:"file"`
			Line   int    `json:"line"`
			Reason string `json:"reason"`
		} `json:"review_focus"`
		Intent  *pbIntentGolden `json:"intent"`
		Blast   *pbBlastGolden  `json:"blast"`
		Missing []struct {
			Input  string `json:"input"`
			Reason string `json:"reason"`
			Detail string `json:"detail"`
		} `json:"missing_inputs"`
		SpecsUsed     []string `json:"specs_used"`
		HeadSHA       string   `json:"head_sha"`
		GeneratedAt   string   `json:"generated_at"`
		PromptVersion string   `json:"prompt_version"`
		Provider      string   `json:"provider"`
		Model         string   `json:"model"`
		TokensIn      int      `json:"tokens_in"`
		TokensOut     int      `json:"tokens_out"`
		CostUSD       *float64 `json:"cost_usd"`
		ModelRequests int      `json:"model_requests"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("decode stored brief %.200s: %v", raw, err)
	}
	b := domain.Brief{
		Summary: w.Summary, Intent: w.Intent.domain(), Blast: w.Blast.domain(), SpecsUsed: w.SpecsUsed, HeadSHA: w.HeadSHA,
		GeneratedAt: pbTime(t, w.GeneratedAt), PromptVersion: w.PromptVersion, Provider: w.Provider, Model: w.Model,
		TokensIn: w.TokensIn, TokensOut: w.TokensOut, CostUSD: w.CostUSD, ModelRequests: w.ModelRequests,
	}
	for _, r := range w.Risks.Risks {
		b.Risks = append(b.Risks, domain.Risk{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: domain.RiskSeverity(r.Severity), FileRefs: r.FileRefs})
	}
	for _, f := range w.Focus {
		b.ReviewFocus = append(b.ReviewFocus, domain.FocusItem{File: f.File, Line: f.Line, Reason: f.Reason})
	}
	for _, m := range w.Missing {
		b.MissingInputs = append(b.MissingInputs, domain.MissingInput{Input: domain.MissingInputName(m.Input), Reason: domain.MissingReason(m.Reason), Detail: m.Detail})
	}
	return &b
}

// ---------------------------------------------------------------------------
// The world of a flow as fakes of the ports
// ---------------------------------------------------------------------------

type pbBriefWorld struct {
	Workspace string `json:"workspace"`
	Now       string `json:"now"`
	Model     struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"model"`
	TimeoutMs   int  `json:"timeoutMs"`
	PullMissing bool `json:"pullMissing"`
	Pull        struct {
		ID     string `json:"id"`
		RepoID string `json:"repoId"`
		Repo   struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
		} `json:"repo"`
		Title   string  `json:"title"`
		Body    *string `json:"body"`
		Base    string  `json:"base"`
		HeadSHA string  `json:"headSha"`
	} `json:"pull"`
	Files []struct {
		Path      string  `json:"path"`
		Additions int     `json:"additions"`
		Deletions int     `json:"deletions"`
		Patch     *string `json:"patch"`
	} `json:"files"`
	Reviews []struct {
		AgentID  *string          `json:"agentId"`
		Findings []pbBriefFinding `json:"findings"`
	} `json:"reviews"`
	Specs map[string][]struct {
		Path   string  `json:"path"`
		Status string  `json:"status"`
		Text   *pbText `json:"text"` // nil: the golden's document has no text at all
	} `json:"specs"`
	Intent       json.RawMessage `json:"intent"`
	Blast        json.RawMessage `json:"blast"`
	Issue        json.RawMessage `json:"issue"`
	LLM          json.RawMessage `json:"llm"`
	ResolveError *string         `json:"resolveError"`
}

// pbBriefRig is the fakes of one world plus what they recorded.
type pbBriefRig struct {
	t     testing.TB
	w     *pbBriefWorld
	logs  *pbLogCapture
	cause error // the resolveError / intent error, when the golden expects one to surface

	mu          sync.Mutex
	upserts     []any
	stored      *domain.Brief
	specCalls   []map[string]any
	issueCalls  []map[string]any
	intentCalls int
	blastCalls  int
	llmReqs     []pbLLMRequest
	promptLog   []pbPromptLogEntry

	// gate, when set, makes every model call wait for a release (scenarios).
	gate     chan error
	entered  chan string
	llmFails error
}

func newPbBriefRig(t testing.TB, w *pbBriefWorld, stored json.RawMessage) *pbBriefRig {
	r := &pbBriefRig{t: t, w: w, logs: newPbLogCapture()}
	r.stored = pbBriefFromWire(t, stored)
	if w.ResolveError != nil {
		r.cause = errors.New(*w.ResolveError)
	}
	return r
}

// service builds the real use cases over the rig.
func (r *pbBriefRig) service(t testing.TB, issues app.Issues) *app.Service {
	now := pbTime(t, r.w.Now)
	if issues == nil {
		issues = pbRigIssues{r}
	}
	return app.NewService(app.Deps{
		Store: pbRigStore{r}, Intents: pbRigIntents{r}, Blast: pbRigBlast{r}, Documents: pbRigDocuments{r}, Issues: issues,
		Roles: peers.Roles{}, LLM: pbRigLLM{r}, Models: pbRigModels{r},
		Clock: func() time.Time { return now }, Log: slog.New(r.logs),
		Budget: time.Duration(r.w.TimeoutMs) * time.Millisecond,
	})
}

type pbRigStore struct{ r *pbBriefRig }

func (s pbRigStore) FindPull(_ context.Context, workspaceID, prID string) (domain.Pull, error) {
	w := s.r.w
	if w.PullMissing || workspaceID != w.Workspace || (prID != w.Pull.ID && !strings.HasPrefix(prID, "pr-")) {
		return domain.Pull{}, domain.ErrNotFound
	}
	return domain.Pull{
		ID: prID, WorkspaceID: workspaceID, RepoID: w.Pull.RepoID, Owner: w.Pull.Repo.Owner, Name: w.Pull.Repo.Name,
		Number: 1, Title: w.Pull.Title, Body: w.Pull.Body, Base: w.Pull.Base, HeadSHA: w.Pull.HeadSHA,
	}, nil
}

func (s pbRigStore) PullFiles(context.Context, string) ([]domain.StoredFile, error) {
	var out []domain.StoredFile
	for _, f := range s.r.w.Files {
		sf := domain.StoredFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
		if f.Patch != nil {
			sf.Patch = *f.Patch
		}
		out = append(out, sf)
	}
	return out, nil
}

func (s pbRigStore) CurrentReviews(context.Context, string) ([]domain.ReviewFacts, error) {
	var out []domain.ReviewFacts
	for i, rv := range s.r.w.Reviews {
		f := domain.ReviewFacts{ReviewID: fmt.Sprintf("rv%d", i+1), AgentID: rv.AgentID}
		for _, x := range rv.Findings {
			f.Findings = append(f.Findings, x.fact())
		}
		out = append(out, f)
	}
	return out, nil
}

func (s pbRigStore) Brief(context.Context, string) (domain.Brief, bool, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.r.stored == nil {
		return domain.Brief{}, false, nil
	}
	return *s.r.stored, true, nil
}

func (s pbRigStore) SaveBrief(_ context.Context, _ string, b domain.Brief) error {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.upserts = append(s.r.upserts, pbBriefWire(&b))
	s.r.stored = &b
	return nil
}

type pbRigIntents struct{ r *pbBriefRig }

func (s pbRigIntents) Stored(context.Context, string) (*reviewer.Intent, error) {
	s.r.mu.Lock()
	s.r.intentCalls++
	s.r.mu.Unlock()
	raw := s.r.w.Intent
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var thrower struct {
		Throws string `json:"throws"`
	}
	if json.Unmarshal(raw, &thrower) == nil && thrower.Throws != "" {
		return nil, errors.New(thrower.Throws)
	}
	var i pbIntentGolden
	if err := json.Unmarshal(raw, &i); err != nil {
		s.r.t.Fatalf("decode world intent: %v", err)
	}
	return i.domain(), nil
}

type pbRigBlast struct{ r *pbBriefRig }

func (s pbRigBlast) RadiusFor(context.Context, string, []string) (domain.Blast, error) {
	s.r.mu.Lock()
	s.r.blastCalls++
	s.r.mu.Unlock()
	var thrower struct {
		Throws string `json:"throws"`
	}
	if json.Unmarshal(s.r.w.Blast, &thrower) == nil && thrower.Throws != "" {
		return domain.Blast{}, errors.New(thrower.Throws)
	}
	var b pbBlastGolden
	if err := json.Unmarshal(s.r.w.Blast, &b); err != nil {
		s.r.t.Fatalf("decode world blast: %v", err)
	}
	return *b.domain(), nil
}

type pbRigDocuments struct{ r *pbBriefRig }

func (s pbRigDocuments) ForAgent(_ context.Context, in app.DocumentsInput) ([]domain.Document, error) {
	w := s.r.w
	s.r.mu.Lock()
	s.r.specCalls = append(s.r.specCalls, map[string]any{
		"agentId": in.AgentID, "repoId": in.Pull.RepoID,
		"repo": map[string]any{"owner": in.Pull.Owner, "name": in.Pull.Name}, "base": in.Pull.Base, "headSha": in.Pull.HeadSHA,
	})
	s.r.mu.Unlock()
	var out []domain.Document
	for _, d := range w.Specs[in.AgentID] {
		doc := domain.Document{Path: d.Path, Status: domain.DocStatus(d.Status)}
		if d.Text != nil {
			doc.Text = d.Text.plain(s.r.t)
		}
		out = append(out, doc)
	}
	return out, nil
}

type pbRigIssues struct{ r *pbBriefRig }

func (s pbRigIssues) Issue(ctx context.Context, owner, name string, number int) (domain.Issue, error) {
	s.r.mu.Lock()
	s.r.issueCalls = append(s.r.issueCalls, map[string]any{"repo": map[string]any{"owner": owner, "name": name}, "number": number})
	s.r.mu.Unlock()
	raw := s.r.w.Issue
	if len(raw) == 0 || string(raw) == "null" {
		return domain.Issue{}, errors.New("issue not found")
	}
	var is struct {
		Number  int     `json:"number"`
		Title   string  `json:"title"`
		Body    *string `json:"body"`
		DelayMs int     `json:"delayMs"`
	}
	if err := json.Unmarshal(raw, &is); err != nil {
		s.r.t.Fatalf("decode world issue: %v", err)
	}
	if is.DelayMs > 0 {
		select {
		case <-time.After(time.Duration(is.DelayMs) * time.Millisecond):
		case <-ctx.Done():
			return domain.Issue{}, ctx.Err()
		}
	}
	return domain.Issue{Number: is.Number, Title: is.Title, Body: is.Body}, nil
}

type pbRigModels struct{ r *pbBriefRig }

func (s pbRigModels) Get(context.Context, string) (map[string]json.RawMessage, error) {
	if s.r.cause != nil {
		return nil, s.r.cause
	}
	m := s.r.w.Model
	bag := fmt.Sprintf(`{"risk_brief":{"provider":%q,"model":%q}}`, m.Provider, m.Model)
	return map[string]json.RawMessage{"feature_models": json.RawMessage(bag)}, nil
}

type pbRigLLM struct{ r *pbBriefRig }

func (s pbRigLLM) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	r := s.r
	var spec struct {
		Data      json.RawMessage `json:"data"`
		TokensIn  int             `json:"tokensIn"`
		TokensOut int             `json:"tokensOut"`
		CostUSD   *float64        `json:"costUsd"`
		Attempts  int             `json:"attempts"`
		Usages    []struct {
			TokensIn  int      `json:"tokensIn"`
			TokensOut int      `json:"tokensOut"`
			CostUSD   *float64 `json:"costUsd"`
		} `json:"usages"`
		Error string `json:"error"`
		Hang  bool   `json:"hang"`
	}
	if err := json.Unmarshal(r.w.LLM, &spec); err != nil {
		r.t.Fatalf("decode world llm: %v", err)
	}
	temperature := 0.0
	if req.Temperature != nil {
		temperature = *req.Temperature
	}
	roles := []string{}
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
	r.promptLog = append(r.promptLog, pbPromptLogOf(req))
	if spec.Error != "config" { // the key check comes after the prompt line and before any request
		r.llmReqs = append(r.llmReqs, rec)
	}
	gate, entered := r.gate, r.entered
	r.mu.Unlock()

	if entered != nil {
		entered <- strings.Split(req.CorrelationID, ":")[1]
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
	}
	usages := spec.Usages
	if len(usages) == 0 {
		usages = append(usages, struct {
			TokensIn  int      `json:"tokensIn"`
			TokensOut int      `json:"tokensOut"`
			CostUSD   *float64 `json:"costUsd"`
		}{spec.TokensIn, spec.TokensOut, spec.CostUSD})
	}
	if req.OnUsage != nil {
		for _, u := range usages {
			req.OnUsage(llm.AttemptUsage{TokensIn: u.TokensIn, TokensOut: u.TokensOut, CostUSD: u.CostUSD})
		}
	}
	if spec.Error != "" {
		return llm.Result{}, errors.New("provider failed: " + spec.Error)
	}
	attempts := spec.Attempts
	if attempts == 0 {
		attempts = 1
	}
	return llm.Result{Data: spec.Data, Model: req.Model, TokensIn: spec.TokensIn, TokensOut: spec.TokensOut, CostUSD: spec.CostUSD, Attempts: attempts}, nil
}

// ---------------------------------------------------------------------------
// Flows
// ---------------------------------------------------------------------------

type pbBriefFlow struct {
	Name        string             `json:"name"`
	Call        string             `json:"call"`
	World       pbBriefWorld       `json:"world"`
	Stored      json.RawMessage    `json:"stored"`
	Response    *pbWantResponse    `json:"response"`
	Error       *pbWantError       `json:"error"`
	Logs        []pbLogLine        `json:"logs"`
	LLMRequests []pbLLMRequest     `json:"llmRequests"`
	Upserts     []any              `json:"upserts"`
	PromptLog   []pbPromptLogEntry `json:"promptLog"`
	SpecCalls   []map[string]any   `json:"specCalls"`
	IssueCalls  []map[string]any   `json:"issueCalls"`
	IntentCalls int                `json:"intentCalls"`
	BlastCalls  int                `json:"blastCalls"`
	StoredAfter any                `json:"storedAfter"`
}

type pbWantResponse struct {
	Brief any  `json:"brief"`
	Stale bool `json:"stale"`
}

// pbResolveRefs replaces the golden's "$response.brief" marker with the response's brief.
func pbResolveRefs(v any, brief any) any {
	switch x := v.(type) {
	case string:
		if x == "$response.brief" {
			return brief
		}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pbResolveRefs(e, brief)
		}
		return out
	}
	return v
}

func pbBriefFlows(t *testing.T) []pbBriefFlow {
	t.Helper()
	var g struct {
		Flows []pbBriefFlow `json:"flows"`
	}
	pbLoad(t, "brief", &g)
	if len(g.Flows) < 40 {
		t.Fatalf("golden has %d brief flows, a generator change shrank it", len(g.Flows))
	}
	return g.Flows
}

func TestBriefParity_Flows(t *testing.T) {
	for _, c := range pbBriefFlows(t) {
		t.Run(c.Name, func(t *testing.T) {
			w := c.World
			r := newPbBriefRig(t, &w, c.Stored)
			svc := r.service(t, nil)

			var state domain.State
			var err error
			switch c.Call {
			case "generate":
				state, err = svc.Generate(t.Context(), w.Workspace, w.Pull.ID)
			case "get":
				state, err = svc.Get(t.Context(), w.Workspace, w.Pull.ID)
			default:
				t.Fatalf("unknown call %q", c.Call)
			}
			pbCheckError(t, c.Error, err, r.cause)

			// Node leaves an included document without text out of the prompt and of specs_used
			// (`text !== undefined`) but still counts it as attached. A Go document is included with
			// its text (possibly empty), as the project-context resolution always delivers it, so
			// such a document is a prompt block and a spec in Go. This deviation is asserted, then
			// the parts it changes are removed from the Go side before the comparison.
			textless := map[string]bool{}
			for _, docs := range w.Specs {
				for _, d := range docs {
					if d.Status == string(domain.DocIncluded) && d.Text == nil {
						textless[d.Path] = true
					}
				}
			}
			gotBrief := pbGeneric(t, pbBriefWire(state.Brief))
			gotUser := ""
			if len(r.llmReqs) == 1 {
				gotUser = r.llmReqs[0].User.s
			}
			gotLogs := pbLogsShape(t, r.logs.snapshot())
			gotPrompt := append([]pbPromptLogEntry(nil), r.promptLog...)
			wantLogs := pbWantLogs(t, c.Logs)
			wantPrompt := c.PromptLog
			if len(textless) > 0 && c.Response != nil {
				if m, ok := gotBrief.(map[string]any); ok {
					var kept []any
					for _, p := range m["specs_used"].([]any) {
						if textless[p.(string)] {
							continue
						}
						kept = append(kept, p)
					}
					if len(kept) == len(m["specs_used"].([]any)) {
						t.Errorf("specs_used = %v, want the textless included documents %v listed in Go", m["specs_used"], textless)
					}
					m["specs_used"] = pbNonNil(kept)
				}
				for p := range textless {
					block := "### Project spec document\n<untrusted source=\"spec\">\n" + p + "\n\n</untrusted>\n\n"
					if !strings.Contains(gotUser, block) {
						t.Errorf("the model request lacks the block of the textless document %s", p)
					}
					gotUser = strings.Replace(gotUser, block, "", 1)
				}
				// The size figures and the specs section depend on the removed blocks.
				for _, ls := range [][]any{gotLogs, wantLogs} {
					for _, l := range ls {
						delete(l.(map[string]any)["obj"].(map[string]any), "estTokensIn")
					}
				}
				strip := func(es []pbPromptLogEntry) []pbPromptLogEntry {
					var out []pbPromptLogEntry
					for _, e := range es {
						var secs []pbSection
						for _, sc := range e.Sections {
							if sc.Name != "specs" {
								secs = append(secs, sc)
							}
						}
						e.Sections = secs
						out = append(out, e)
					}
					return out
				}
				gotPrompt, wantPrompt = strip(gotPrompt), strip(wantPrompt)
			}

			var respBrief any
			if c.Response != nil {
				respBrief = gotBrief
				pbDiff(t, "response brief", c.Response.Brief, respBrief)
				if state.Stale != c.Response.Stale {
					t.Errorf("stale = %v, want %v", state.Stale, c.Response.Stale)
				}
			} else if err == nil {
				t.Errorf("the call succeeded, Node failed with %+v", c.Error)
			}

			pbDiff(t, "log lines", wantLogs, gotLogs)
			gotReqs := append([]pbLLMRequest(nil), r.llmReqs...)
			if len(gotReqs) == 1 {
				gotReqs[0].User = pbText{s: gotUser}
			}
			pbCheckLLMRequests(t, c.LLMRequests, gotReqs)
			pbDiff(t, "prompt-log sections", pbGeneric(t, pbNonNil(wantPrompt)), pbGeneric(t, pbNonNil(gotPrompt)))
			if len(textless) == 0 { // the stored brief carries the textless document in specs_used
				pbDiff(t, "upserts", pbGeneric(t, pbResolveRefs(pbNonNil(c.Upserts), respBrief)), pbGeneric(t, pbNonNil(r.upserts)))
				var after any
				if r.stored != nil {
					after = pbGeneric(t, pbBriefWire(r.stored))
				}
				pbDiff(t, "stored brief after", pbResolveRefs(c.StoredAfter, respBrief), after)
			}
			if r.intentCalls != c.IntentCalls || r.blastCalls != c.BlastCalls {
				t.Errorf("intent/blast reads = %d/%d, want %d/%d", r.intentCalls, r.blastCalls, c.IntentCalls, c.BlastCalls)
			}
			pbDiff(t, "issue reads", pbGeneric(t, pbNonNil(c.IssueCalls)), pbGeneric(t, pbNonNil(r.issueCalls)))

			// Document reads: the Node call also carries the agent's skills (the Go port takes the
			// agent id and its adapter finds them), and the reads run in parallel (order not fixed).
			wantSpec := []map[string]any{}
			for _, s := range c.SpecCalls {
				wantSpec = append(wantSpec, map[string]any{"agentId": s["agentId"], "repoId": s["repoId"], "repo": s["repo"], "base": s["base"], "headSha": s["headSha"]})
			}
			sort.Slice(wantSpec, func(i, j int) bool { return wantSpec[i]["agentId"].(string) < wantSpec[j]["agentId"].(string) })
			gotSpec := append([]map[string]any{}, r.specCalls...)
			sort.Slice(gotSpec, func(i, j int) bool { return gotSpec[i]["agentId"].(string) < gotSpec[j]["agentId"].(string) })
			pbDiff(t, "document reads", pbGeneric(t, wantSpec), pbGeneric(t, gotSpec))
		})
	}
}

// pbCheckLLMRequests compares the model requests; user is a golden text (a blob digest when long).
func pbCheckLLMRequests(t testing.TB, want, got []pbLLMRequest) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%d model requests, want %d", len(got), len(want))
		return
	}
	for i := range want {
		w, g := want[i], got[i]
		pbCheckText(t, fmt.Sprintf("model request %d user message", i), w.User, g.User.s)
		w.User, g.User = pbText{}, pbText{}
		pbDiff(t, fmt.Sprintf("model request %d", i), pbGeneric(t, w), pbGeneric(t, g))
	}
}

// ---------------------------------------------------------------------------
// Scenarios: one generation per pull request
// ---------------------------------------------------------------------------

type pbScenarioCaller struct {
	Result struct {
		Response *pbWantResponse `json:"response"`
		Error    *pbWantError    `json:"error"`
	} `json:"result"`
	Logs []pbLogLine `json:"logs"`
}

func TestBriefParity_Scenarios(t *testing.T) {
	var g struct {
		Flows []struct {
			Name  string       `json:"name"`
			World pbBriefWorld `json:"world"`
		} `json:"flows"`
		Scenarios []struct {
			Name  string `json:"name"`
			Steps []struct {
				Start   string `json:"start"`
				PR      string `json:"pr"`
				Settle  bool   `json:"settle"`
				Release int    `json:"release"`
				Fail    string `json:"fail"`
				Await   string `json:"await"`
			} `json:"steps"`
			Callers     map[string]pbScenarioCaller `json:"callers"`
			LLMRequests int                         `json:"llmRequests"`
			Upserts     int                         `json:"upserts"`
		} `json:"scenarios"`
	}
	pbLoad(t, "brief", &g)
	if len(g.Scenarios) != 2 {
		t.Fatalf("golden has %d scenarios, want 2", len(g.Scenarios))
	}
	// The scenarios run on the default world (the first flow's, whose world is the defaults).
	base := g.Flows[0].World

	for _, sc := range g.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			w := base
			r := newPbBriefRig(t, &w, nil)
			r.gate = make(chan error, 8)
			r.entered = make(chan string, 8)
			svc := r.service(t, nil)

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
						s, err := svc.Generate(context.Background(), w.Workspace, st.PR)
						ch <- outcome{s, err}
					}()
					// Wait until the call is parked in the model or has finished.
					select {
					case pr := <-r.entered:
						if pr != st.PR {
							t.Fatalf("model entered for %q, expected %q", pr, st.PR)
						}
					case o := <-ch:
						results[st.Start] = o
						ch <- o
					case <-time.After(5 * time.Second):
						t.Fatalf("caller %s neither reached the model nor finished", st.Start)
					}
				case st.Settle:
					// every start above already waited for its call to park or finish
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

			for name, want := range sc.Callers {
				got, ok := results[name]
				if !ok {
					t.Errorf("caller %s has no result", name)
					continue
				}
				pbCheckError(t, want.Result.Error, got.err, nil)
				if want.Result.Response != nil {
					pbDiff(t, "caller "+name+" brief", want.Result.Response.Brief, pbGeneric(t, pbBriefWire(got.state.Brief)))
					if got.state.Stale != want.Result.Response.Stale {
						t.Errorf("caller %s stale = %v, want %v", name, got.state.Stale, want.Result.Response.Stale)
					}
				}
			}
			if len(r.llmReqs) != sc.LLMRequests {
				t.Errorf("%d model requests, want %d", len(r.llmReqs), sc.LLMRequests)
			}
			if len(r.upserts) != sc.Upserts {
				t.Errorf("%d briefs stored, want %d", len(r.upserts), sc.Upserts)
			}
			// Each caller's lines are the generation lines of its pull request, in order.
			perPR := map[string][]pbLogLine{}
			for _, l := range r.logs.snapshot() {
				if pr, _ := l.Obj["prId"].(string); pr != "" {
					perPR[pr] = append(perPR[pr], l)
				}
			}
			var wantLines, gotLines []pbLogLine
			for _, name := range sortedKeys(sc.Callers) {
				wantLines = append(wantLines, sc.Callers[name].Logs...)
			}
			for _, pr := range sortedKeys(perPR) {
				gotLines = append(gotLines, perPR[pr]...)
			}
			sortShapes := func(ls []any) []any {
				sort.SliceStable(ls, func(i, j int) bool {
					a, _ := json.Marshal(ls[i])
					b, _ := json.Marshal(ls[j])
					return string(a) < string(b)
				})
				return ls
			}
			pbDiff(t, "log lines of all callers", sortShapes(pbWantLogs(t, wantLines)), sortShapes(pbLogsShape(t, gotLines)))
		})
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// Linked issue source: ok, null body, GitHub error, no token, slower than the deadline
// ---------------------------------------------------------------------------

type pbIssueGitHub struct {
	Issue *struct {
		Number int     `json:"number"`
		Title  string  `json:"title"`
		Body   *string `json:"body"`
	} `json:"issue"`
	Error        string `json:"error"`
	FactoryError string `json:"factoryError"`
	Hang         bool   `json:"hang"`
}

func (g pbIssueGitHub) GetIssue(ctx context.Context, _, _ string, _ int) (github.Issue, error) {
	switch {
	case g.FactoryError != "":
		return github.Issue{}, github.ErrNoToken
	case g.Hang:
		<-ctx.Done()
		return github.Issue{}, ctx.Err()
	case g.Error != "":
		return github.Issue{}, errors.New(g.Error)
	}
	return github.Issue{Number: g.Issue.Number, Title: g.Issue.Title, Body: g.Issue.Body}, nil
}

// TestBriefParity_IssueSource runs the real peers.Issues over a fake GitHub client inside a
// generation: an issue that is read shows in the prompt, one that is not (error, missing token, no
// answer within the 10 s deadline, run on the synctest clock) is the linked_issue/fetch_failed
// missing input (EC5022).
func TestBriefParity_IssueSource(t *testing.T) {
	var g struct {
		Flows []struct {
			World pbBriefWorld `json:"world"`
		} `json:"flows"`
		IssueSource []struct {
			Name       string        `json:"name"`
			GitHub     pbIssueGitHub `json:"github"`
			TimerFired bool          `json:"timerFired"`
			Result     *struct {
				Number int     `json:"number"`
				Title  string  `json:"title"`
				Body   *string `json:"body"`
			} `json:"result"`
		} `json:"issueSource"`
	}
	pbLoad(t, "brief", &g)
	if len(g.IssueSource) != 5 {
		t.Fatalf("golden has %d issue-source cases, want 5", len(g.IssueSource))
	}
	for _, c := range g.IssueSource {
		t.Run(c.Name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := g.Flows[0].World // the PR body links #12
				w.TimeoutMs = 120_000
				r := newPbBriefRig(t, &w, nil)
				svc := r.service(t, peers.NewIssues(c.GitHub))
				start := time.Now()
				state, err := svc.Generate(context.Background(), w.Workspace, w.Pull.ID)
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
				missing := false
				for _, m := range state.Brief.MissingInputs {
					missing = missing || m.Input == domain.InputLinkedIssue && m.Reason == domain.ReasonFetchFailed
				}
				user := ""
				if len(r.llmReqs) == 1 {
					user = r.llmReqs[0].User.s
				}
				if c.Result == nil {
					if !missing || strings.Contains(user, "### Linked issue") {
						t.Errorf("issue unavailable: missing fetch_failed = %v, prompt has issue block = %v, want true/false", missing, strings.Contains(user, "### Linked issue"))
					}
				} else {
					want := fmt.Sprintf("#%d %s\n", c.Result.Number, c.Result.Title)
					if c.Result.Body != nil {
						want += *c.Result.Body
					}
					if missing || !strings.Contains(user, want) {
						t.Errorf("issue read: missing fetch_failed = %v, prompt lacks %q", missing, want)
					}
				}
				if took := time.Since(start); c.TimerFired != (took >= domain.IssueTimeout) {
					t.Errorf("generation took %v on the test clock, deadline timer fired = %v, want %v", took, took >= domain.IssueTimeout, c.TimerFired)
				}
			})
		})
	}
}
