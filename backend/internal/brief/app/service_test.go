package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/app/brieftest"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/inflight"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

const (
	wsID   = "ws-1"
	prID   = "pr-1"
	headID = "0123456789abcdef0123456789abcdef01234567"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// modelAnswer is a valid PrBriefLlm document for the files of newFixture.
const modelAnswer = `{"summary":"Adds a limiter.","risks":[{"kind":"security","title":"Header trusted","explanation":"Spoofable key.","severity":"high","file_refs":["src/limit.ts:6-9"]}],"review_focus":[{"file":"src/limit.ts","line":7,"reason":"Key from a header."}]}`

// logBuf collects the JSON log lines of the service.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuf) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// fixture is a service over fakes that make a generation succeed.
type fixture struct {
	store   *brieftest.Store
	intents *brieftest.Intents
	blast   *brieftest.Blast
	docs    *brieftest.Documents
	issues  *brieftest.Issues
	models  *brieftest.Models
	llm     *brieftest.LLM
	logs    *logBuf
	svc     *app.Service
}

func newFixture(t *testing.T, mutate ...func(*app.Deps)) *fixture {
	t.Helper()
	body := "Adds a limiter. Fixes #12."
	f := &fixture{
		store: &brieftest.Store{
			Pull: domain.Pull{
				ID: prID, WorkspaceID: wsID, RepoID: "repo-1", Owner: "acme", Name: "api", Number: 7,
				Title: "Add rate limiting", Body: &body, Base: "main", HeadSHA: headID,
			},
			Files: []domain.StoredFile{
				{Path: "src/limit.ts", Additions: 40, Deletions: 2, Patch: "@@ -1,3 +1,30 @@\n a"},
				{Path: "test/limit.test.ts", Additions: 20},
			},
		},
		intents: &brieftest.Intents{},
		blast: &brieftest.Blast{Blast: domain.Blast{
			ChangedSymbols: []domain.BlastSymbol{{Name: "limit", File: "src/limit.ts", Kind: "function"}},
			Summary:        "1 changed symbol",
		}},
		docs:   &brieftest.Documents{ByAgent: map[string][]domain.Document{}},
		issues: &brieftest.Issues{Fetched: domain.Issue{Number: 12, Title: "Rate limit"}},
		models: &brieftest.Models{},
		llm: &brieftest.LLM{
			Result: llm.Result{Data: json.RawMessage(modelAnswer), TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042), Attempts: 1},
			Usages: []llm.AttemptUsage{{TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042)}},
		},
		logs: &logBuf{},
	}
	deps := app.Deps{
		Store: f.store, Intents: f.intents, Blast: f.blast, Documents: f.docs, Issues: f.issues,
		Roles: brieftest.Roles("core"), LLM: f.llm, Models: f.models,
		Clock: func() time.Time { return now },
		Log:   slog.New(slog.NewJSONHandler(f.logs, nil)),
	}
	for _, m := range mutate {
		m(&deps)
	}
	f.svc = app.NewService(deps)
	return f
}

func ptr[T any](v T) *T { return &v }

func wantAppErr(t *testing.T, err error, kind apperr.Kind, code, message string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an apperr", err)
	}
	if ae.Kind != kind || ae.Code != code || ae.Message != message {
		t.Errorf("error = {%v %q %q}, want {%v %q %q}", ae.Kind, ae.Code, ae.Message, kind, code, message)
	}
	return ae
}

func generatedLine(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	for _, r := range f.logs.records(t) {
		if r["evt"] == "brief_generated" {
			return r
		}
	}
	t.Fatal("no brief_generated line was logged")
	return nil
}

func TestNewService_PanicsWithoutADependency(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewService(zero) did not panic")
		}
	}()
	app.NewService(app.Deps{})
}

func TestGet(t *testing.T) {
	stored := domain.Brief{HeadSHA: headID, PromptVersion: domain.PromptVersion, Summary: "S"}
	tests := []struct {
		name      string
		stored    *domain.Brief
		pullHead  string
		wantBrief bool
		wantStale bool
	}{
		{"no brief", nil, headID, false, false},
		{"fresh", &stored, headID, true, false},
		{"head moved", &stored, "ffff", true, true},
		{"prompt version moved", &domain.Brief{HeadSHA: headID, PromptVersion: "v0"}, headID, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.store.Stored = tc.stored
			f.store.Pull.HeadSHA = tc.pullHead
			got, err := f.svc.Get(t.Context(), wsID, prID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if (got.Brief != nil) != tc.wantBrief || got.Stale != tc.wantStale {
				t.Errorf("Get() = {brief %v, stale %v}, want {brief %v, stale %v}", got.Brief != nil, got.Stale, tc.wantBrief, tc.wantStale)
			}
			if len(f.llm.Requests) != 0 {
				t.Error("Get() called the model")
			}
		})
	}
}

func TestGet_Errors(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Get(t.Context(), "other-ws", prID); err == nil {
		t.Error("Get(other workspace) error = nil")
	} else {
		wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	}

	boom := errors.New("db down")
	f.store.BriefErr = boom
	_, err := f.svc.Get(t.Context(), wsID, prID)
	if !errors.Is(err, boom) {
		t.Errorf("Get(store error) = %v, want it to wrap the cause", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("Get(store error) = %v, want a plain error (500 internal_error)", err)
	}
}

func TestGenerate_StoresAndReturnsTheBrief(t *testing.T) {
	f := newFixture(t)
	f.intents.Intent = &reviewer.Intent{Text: "Rate limit", DerivedFrom: "explicit"}
	got, err := f.svc.Generate(t.Context(), wsID, prID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got.Stale || got.Brief == nil {
		t.Fatalf("Generate() = %+v, want a fresh brief", got)
	}
	want := domain.Brief{
		Summary: "Adds a limiter.",
		Risks: []domain.Risk{{Kind: "security", Title: "Header trusted", Explanation: "Spoofable key.",
			Severity: domain.RiskHigh, FileRefs: []string{"src/limit.ts:6-9"}}},
		ReviewFocus:   []domain.FocusItem{{File: "src/limit.ts", Line: 7, Reason: "Key from a header."}},
		Intent:        f.intents.Intent,
		Blast:         &domain.Blast{ChangedSymbols: []domain.BlastSymbol{{Name: "limit", File: "src/limit.ts", Kind: "function"}}, Summary: "1 changed symbol"},
		MissingInputs: []domain.MissingInput{{Input: domain.InputSpecs, Reason: domain.ReasonNoReviewRun}},
		SpecsUsed:     []string{},
		HeadSHA:       headID,
		GeneratedAt:   now,
		PromptVersion: "v1",
		Provider:      "openai",
		Model:         "gpt-4.1",
		TokensIn:      1200,
		TokensOut:     180,
		CostUSD:       ptr(0.00042),
		ModelRequests: 1,
	}
	if diff := cmp.Diff(want, *got.Brief, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Generate() brief mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]domain.Brief{*got.Brief}, f.store.Saved); diff != "" {
		t.Errorf("saved briefs mismatch (-got +stored):\n%s", diff)
	}
	if diff := cmp.Diff([]int{12}, f.issues.Calls); diff != "" {
		t.Errorf("issue reads mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"src/limit.ts", "test/limit.test.ts"}}, f.blast.Calls); diff != "" {
		t.Errorf("blast reads mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerate_TheModelCall(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(f.llm.Requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(f.llm.Requests))
	}
	req := f.llm.Requests[0]
	if req.Provider != llm.OpenAI || req.Model != "gpt-4.1" || req.SchemaName != "PrBriefLlm" || req.MaxTokens != 8000 ||
		req.Reprompts != 1 || req.Feature != "brief" || req.CorrelationID != "brief:pr-1:0123456789ab" {
		t.Errorf("request = {%s %s %s %d %d %s %s}, want openai/gpt-4.1/PrBriefLlm/8000/1/brief/brief:pr-1:0123456789ab",
			req.Provider, req.Model, req.SchemaName, req.MaxTokens, req.Reprompts, req.Feature, req.CorrelationID)
	}
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Errorf("temperature = %v, want 0.2", req.Temperature)
	}
	if string(req.Schema) != string(domain.SchemaJSON) {
		t.Error("the request does not carry domain.SchemaJSON")
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != llm.System || req.Messages[0].Content != domain.SystemPrompt ||
		req.Messages[1].Role != llm.User || !strings.HasSuffix(req.Messages[1].Content, domain.TaskSuffix) {
		t.Errorf("messages = %d, want the system prompt then the user message", len(req.Messages))
	}
	var names []string
	for _, s := range req.Sections {
		names = append(names, s.Name)
		if (s.Name == "system") != (s.Trust == llm.Trusted) || (s.Name == "system") != (s.Items == nil) || s.Chars == 0 {
			t.Errorf("section %+v: trust, items or size are wrong", s)
		}
	}
	if diff := cmp.Diff([]string{"system", "pr_description", "changed_files", "callers", "linked_issue"}, names); diff != "" {
		t.Errorf("prompt sections mismatch (-want +got):\n%s", diff)
	}
	if len(f.llm.Requests[0].Sections) > 0 && *req.Sections[1].Items != 2 {
		t.Errorf("pr_description items = %d, want 2 (title block and description)", *req.Sections[1].Items)
	}
}

func TestGenerate_ModelSettingFromTheBag(t *testing.T) {
	f := newFixture(t)
	f.models.Bag = map[string]json.RawMessage{"feature_models": json.RawMessage(`{"risk_brief":{"provider":"anthropic","model":"claude-x"}}`)}
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got := f.llm.Requests[0]; got.Provider != llm.Anthropic || got.Model != "claude-x" {
		t.Errorf("provider/model = %s/%s, want anthropic/claude-x", got.Provider, got.Model)
	}
}

func TestGenerate_LogLines(t *testing.T) {
	f := newFixture(t)
	f.llm.Result.Attempts = 3
	f.llm.Result.TokensIn, f.llm.Result.TokensOut = 2400, 400
	f.llm.Usages = []llm.AttemptUsage{{TokensIn: 1200, TokensOut: 200}, {TokensIn: 1200, TokensOut: 200}}
	got, err := f.svc.Generate(t.Context(), wsID, prID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got.Brief.ModelRequests != 2 {
		t.Errorf("brief model_requests = %d, want 2 for three attempts", got.Brief.ModelRequests)
	}
	recs := f.logs.records(t)
	var outcomes []any
	for _, r := range recs {
		if r["evt"] == "brief_model_request" {
			outcomes = append(outcomes, r["outcome"])
			if r["feature"] != "brief" || r["prId"] != prID || r["model"] != "gpt-4.1" {
				t.Errorf("model request line = %v, want feature, prId and model", r)
			}
		}
	}
	if diff := cmp.Diff([]any{"invalid_output", "ok"}, outcomes); diff != "" {
		t.Errorf("model request outcomes mismatch (-want +got):\n%s", diff)
	}
	line := generatedLine(t, f)
	if line["msg"] != "pr brief generated" || line["level"] != "INFO" || line["outcome"] != "ok" || line["modelRequests"] != float64(3) ||
		line["tokensIn"] != float64(2400) || line["durationMs"] != float64(0) {
		t.Errorf("generation line = %v", line)
	}
	if diff := cmp.Diff(map[string]any{"risks": float64(0), "review_focus": float64(0), "risk_refs": float64(0)}, line["dropped"]); diff != "" {
		t.Errorf("dropped mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{"intent/not_derived", "specs/no_review_run"}, line["missingInputs"]); diff != "" {
		t.Errorf("missingInputs mismatch (-want +got):\n%s", diff)
	}
	// No PR text anywhere in the lines.
	for _, r := range recs {
		raw, _ := json.Marshal(r)
		for _, text := range []string{"Add rate limiting", "Adds a limiter", "Rate limit", "src/limit.ts"} {
			if strings.Contains(string(raw), text) {
				t.Errorf("log line %s carries PR text %q", raw, text)
			}
		}
	}
}

func TestGenerate_FactsAreMissingInputsNotFailures(t *testing.T) {
	f := newFixture(t)
	f.intents.Err = errors.New("intent table gone")
	f.blast.Err = errors.New("index unreadable")
	f.issues.Err = errors.New("404")
	got, err := f.svc.Generate(t.Context(), wsID, prID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var missing []string
	for _, m := range got.Brief.MissingInputs {
		missing = append(missing, string(m.Input)+"/"+string(m.Reason))
	}
	want := []string{"intent/not_derived", "blast/degraded", "specs/no_review_run", "linked_issue/fetch_failed"}
	if diff := cmp.Diff(want, missing); diff != "" {
		t.Errorf("missing inputs mismatch (-want +got):\n%s", diff)
	}
	if got.Brief.Intent != nil || got.Brief.Blast != nil {
		t.Errorf("intent %v and blast %v of failed reads, want nil", got.Brief.Intent, got.Brief.Blast)
	}
	var warns []string
	for _, r := range f.logs.records(t) {
		if r["level"] == "WARN" {
			warns = append(warns, r["msg"].(string))
		}
	}
	slicesSort(warns)
	if diff := cmp.Diff([]string{"pr brief: blast read failed", "pr brief: intent read failed"}, warns); diff != "" {
		t.Errorf("warn lines mismatch (-want +got):\n%s", diff)
	}
}

func slicesSort(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestGenerate_DegradedBlastIsNeitherPromptedNorStored(t *testing.T) {
	f := newFixture(t)
	f.blast.Blast.Degraded, f.blast.Blast.Reason = true, "index_partial"
	got, err := f.svc.Generate(t.Context(), wsID, prID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got.Brief.Blast != nil {
		t.Errorf("stored blast = %+v, want nil", got.Brief.Blast)
	}
	if want := (domain.MissingInput{Input: domain.InputBlast, Reason: domain.ReasonDegraded, Detail: "index_partial"}); !contains(got.Brief.MissingInputs, want) {
		t.Errorf("missing inputs = %+v, want %+v among them", got.Brief.MissingInputs, want)
	}
	for _, s := range f.llm.Requests[0].Sections {
		if s.Name == "callers" {
			t.Error("the prompt carries a degraded blast")
		}
	}
}

func contains(ms []domain.MissingInput, m domain.MissingInput) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

func TestGenerate_DocumentsMergeInReviewOrderFirstPathWins(t *testing.T) {
	f := newFixture(t)
	a1, a2 := "a1", "a2"
	f.store.Reviews = []domain.ReviewFacts{
		{ReviewID: "r1", AgentID: &a1},
		{ReviewID: "r2"}, // deleted agent
		{ReviewID: "r3", AgentID: &a2},
	}
	f.docs.ByAgent = map[string][]domain.Document{
		"a1": {{Path: "docs/a.md", Status: domain.DocIncluded, Text: "A1"}, {Path: "docs/u.md", Status: domain.DocUnreadable}},
		"a2": {{Path: "docs/a.md", Status: domain.DocIncluded, Text: "A2"}, {Path: "docs/b.md", Status: domain.DocIncluded, Text: "B"}},
	}
	got, err := f.svc.Generate(t.Context(), wsID, prID)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if diff := cmp.Diff([]string{"docs/a.md", "docs/b.md"}, got.Brief.SpecsUsed); diff != "" {
		t.Errorf("SpecsUsed mismatch (-want +got):\n%s", diff)
	}
	if want := (domain.MissingInput{Input: domain.InputSpecs, Reason: domain.ReasonUnreadable, Detail: "1 of 3"}); !contains(got.Brief.MissingInputs, want) {
		t.Errorf("missing inputs = %+v, want %+v among them", got.Brief.MissingInputs, want)
	}
	agents := map[string]bool{}
	for _, c := range f.docs.Calls {
		if c.WorkspaceID != wsID || c.Pull.ID != prID {
			t.Errorf("ForAgent input = %+v, want the workspace and the pull request", c)
		}
		agents[c.AgentID] = true
	}
	if diff := cmp.Diff(map[string]bool{"a1": true, "a2": true}, agents); diff != "" {
		t.Errorf("agents asked for documents mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerate_DocumentFailureFailsTheGeneration(t *testing.T) {
	f := newFixture(t)
	a1 := "a1"
	f.store.Reviews = []domain.ReviewFacts{{ReviewID: "r1", AgentID: &a1}}
	f.docs.Err = errors.New("git outage")
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	if err == nil || !strings.Contains(err.Error(), "git outage") {
		t.Fatalf("Generate() error = %v, want the document failure", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("Generate() error = %v, want a plain error (500 internal_error)", err)
	}
	if got := generatedLine(t, f)["outcome"]; got != "internal_error" {
		t.Errorf("outcome = %v, want internal_error", got)
	}
	assertReleased(t, f)
}

// assertReleased checks that the marker of the pull request is free: another generation runs.
func assertReleased(t *testing.T, f *fixture) {
	t.Helper()
	f.docs.Err = nil
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err != nil {
		if ae, ok := apperr.As(err); ok && ae.Code == "generation_in_progress" {
			t.Fatal("the in-flight marker is still held")
		}
	}
}

func TestGenerate_PanicInAPeerIsRaisedOnTheCaller(t *testing.T) {
	f := newFixture(t)
	a1 := "a1"
	f.store.Reviews = []domain.ReviewFacts{{ReviewID: "r1", AgentID: &a1}}
	f.docs.Hook = func(context.Context, app.DocumentsInput) error { panic("reader bug") }
	func() {
		defer func() {
			if got := recover(); got != "reader bug" {
				t.Errorf("recovered %v, want the peer's panic on the caller's goroutine", got)
			}
		}()
		_, _ = f.svc.Generate(t.Context(), wsID, prID)
	}()
	f.docs.Hook = nil
	assertReleased(t, f)
}

func TestGenerate_Errors(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*fixture)
		kind        apperr.Kind
		code, msg   string
		wantOutcome string
		wantDetails any
	}{
		{
			name:  "no changed files",
			setup: func(f *fixture) { f.store.Files = nil },
			kind:  apperr.KindValidation, code: "no_changed_files", msg: "The pull request has no changed files",
			wantOutcome: "no_changed_files",
		},
		{
			name:  "missing key",
			setup: func(f *fixture) { f.llm.Err = apperr.Config("OPENAI_API_KEY is not configured") },
			kind:  apperr.KindValidation, code: "provider_not_configured", msg: "The openai provider is not configured",
			wantOutcome: "provider_not_configured", wantDetails: map[string]string{"provider": "openai"},
		},
		{
			name:  "provider failure",
			setup: func(f *fixture) { f.llm.Err = apperr.ExternalService("OpenAI said: secret PR text") },
			kind:  apperr.KindExternalService, code: "generation_failed", msg: "The model call failed",
			wantOutcome: "generation_failed",
		},
		{
			name:  "plain model failure",
			setup: func(f *fixture) { f.llm.Err = errors.New("connection reset") },
			kind:  apperr.KindExternalService, code: "generation_failed", msg: "The model call failed",
			wantOutcome: "generation_failed",
		},
		{
			name:  "malformed answer",
			setup: func(f *fixture) { f.llm.Result.Data = json.RawMessage(`[]`) },
			kind:  apperr.KindExternalService, code: "generation_failed", msg: "The model call failed",
			wantOutcome: "generation_failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			previous := &domain.Brief{Summary: "previous", HeadSHA: headID, PromptVersion: "v1"}
			f.store.Stored = previous
			tc.setup(f)
			_, err := f.svc.Generate(t.Context(), wsID, prID)
			ae := wantAppErr(t, err, tc.kind, tc.code, tc.msg)
			if diff := cmp.Diff(tc.wantDetails, ae.Details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
			if strings.Contains(err.Error(), "secret PR text") {
				t.Errorf("the provider's text reached the error: %v", err)
			}
			if len(f.store.Saved) != 0 || f.store.Stored != previous {
				t.Error("a failed generation wrote or replaced the stored brief")
			}
			line := generatedLine(t, f)
			if line["level"] != "WARN" || line["msg"] != "pr brief generation failed" || line["outcome"] != tc.wantOutcome {
				t.Errorf("generation line = %v, want a warn with outcome %s", line, tc.wantOutcome)
			}
			assertReleased(t, f)
		})
	}
}

func TestGenerate_UnknownPullRequestLogsNothing(t *testing.T) {
	f := newFixture(t)
	f.store.PullMissing = true
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	if recs := f.logs.records(t); len(recs) != 0 {
		t.Errorf("log lines = %v, want none", recs)
	}
}

func TestGenerate_FailedModelCallLogsItsRequests(t *testing.T) {
	f := newFixture(t)
	f.llm.Err = errors.New("boom")
	f.llm.Usages = []llm.AttemptUsage{{TokensIn: 1200, TokensOut: 200}, {TokensIn: 1200, TokensOut: 200}}
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err == nil {
		t.Fatal("Generate() error = nil")
	}
	var outcomes []any
	for _, r := range f.logs.records(t) {
		if r["evt"] == "brief_model_request" {
			outcomes = append(outcomes, r["outcome"])
		}
	}
	if diff := cmp.Diff([]any{"invalid_output", "failed"}, outcomes); diff != "" {
		t.Errorf("model request outcomes mismatch (-want +got):\n%s", diff)
	}
	line := generatedLine(t, f)
	for _, k := range []string{"modelRequests", "tokensIn", "tokensOut", "costUsd", "dropped"} {
		if v, present := line[k]; !present || v != nil {
			t.Errorf("failed generation line %s = %v (present %v), want null", k, v, present)
		}
	}
	if line["provider"] != "openai" || line["estTokensIn"] == nil {
		t.Errorf("failed generation line = %v, want provider and the estimate", line)
	}
}

// AC5024-style budget: the default 120 s budget expires during the model call.
func TestGenerate_BudgetExpiresDuringTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, func(d *app.Deps) { d.Clock = time.Now })
		f.llm.Hang = true
		started := time.Now()
		_, err := f.svc.Generate(t.Context(), wsID, prID)
		wantAppErr(t, err, apperr.KindExternalService, "generation_failed", "Generation timed out")
		if took := time.Since(started); took != domain.GenerationBudget {
			t.Errorf("Generate() took %v, want the %v budget", took, domain.GenerationBudget)
		}
		if len(f.store.Saved) != 0 {
			t.Error("a timed out generation stored a brief")
		}
		if got := generatedLine(t, f); got["outcome"] != "generation_failed" || got["durationMs"] != float64(120_000) {
			t.Errorf("generation line = %v, want generation_failed after 120000 ms", got)
		}
		assertReleased(t, f)
	})
}

func TestGenerate_BudgetExpiresWhileGathering(t *testing.T) {
	f := newFixture(t, func(d *app.Deps) { d.Budget = 30 * time.Millisecond })
	f.issues.Hang = true
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindExternalService, "generation_failed", "Generation timed out")
	if len(f.llm.Requests) != 0 {
		t.Error("the model was called after the budget expired")
	}
}

func TestGenerate_IssueReadIsBoundedByItsOwnTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, func(d *app.Deps) { d.Clock = time.Now })
		f.issues.Hang = true
		started := time.Now()
		got, err := f.svc.Generate(t.Context(), wsID, prID)
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if took := time.Since(started); took != domain.IssueTimeout {
			t.Errorf("Generate() took %v, want the %v issue timeout", took, domain.IssueTimeout)
		}
		if want := (domain.MissingInput{Input: domain.InputLinkedIssue, Reason: domain.ReasonFetchFailed}); !contains(got.Brief.MissingInputs, want) {
			t.Errorf("missing inputs = %+v, want %+v among them", got.Brief.MissingInputs, want)
		}
	})
}

func TestGenerate_SaveFailureIsInternal(t *testing.T) {
	f := newFixture(t)
	f.store.SaveErr = errors.New("disk full")
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	if err == nil || !errors.Is(err, f.store.SaveErr) {
		t.Fatalf("Generate() error = %v, want the save failure", err)
	}
	if got := generatedLine(t, f)["outcome"]; got != "internal_error" {
		t.Errorf("outcome = %v, want internal_error", got)
	}
}

// holdLLM makes the model call block until release is closed or its context ends.
func holdLLM(f *fixture) (entered <-chan struct{}, release func()) {
	in := make(chan struct{}, 8)
	gate := make(chan struct{})
	var once sync.Once
	f.llm.Enter = func(ctx context.Context) {
		in <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	return in, func() { once.Do(func() { close(gate) }) }
}

func TestGenerate_OneGenerationPerPullRequest(t *testing.T) {
	f := newFixture(t)
	entered, release := holdLLM(f)
	defer release()

	type outcome struct {
		state domain.State
		err   error
	}
	first := make(chan outcome, 1)
	go func() {
		st, err := f.svc.Generate(context.Background(), wsID, prID)
		first <- outcome{st, err}
	}()
	<-entered

	_, err := f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindConflict, "generation_in_progress", "A brief is already being generated for this pull request")
	if len(f.llm.Requests) != 1 {
		t.Errorf("model calls = %d, want 1: the 409 must not start a second generation", len(f.llm.Requests))
	}

	release()
	if got := <-first; got.err != nil || got.state.Brief == nil {
		t.Fatalf("first Generate() = %+v, %v, want a brief", got.state, got.err)
	}
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err != nil {
		t.Errorf("Generate() after the first finished: %v", err)
	}
}

func TestGenerate_AnotherPullRequestIsNotBlocked(t *testing.T) {
	f := newFixture(t)
	entered, release := holdLLM(f)
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Generate(context.Background(), wsID, prID)
		done <- err
	}()
	<-entered
	// The fake store knows one pull request: a second id is a 404, not a 409.
	_, err := f.svc.Generate(t.Context(), wsID, "pr-2")
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	release()
	if err := <-done; err != nil {
		t.Errorf("first Generate() error = %v", err)
	}
}

func TestGenerate_CallerCancellationStopsTheGeneration(t *testing.T) {
	f := newFixture(t)
	entered, release := holdLLM(f)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Generate(ctx, wsID, prID)
		done <- err
	}()
	<-entered
	cancel()
	err := <-done
	wantAppErr(t, err, apperr.KindExternalService, "generation_failed", "The model call failed")
	release() // the next generation must not be held
	assertReleased(t, f)
}

func TestShutdown_CancelsAndWaitsForRunningGenerations(t *testing.T) {
	f := newFixture(t)
	entered, release := holdLLM(f)
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Generate(context.Background(), wsID, prID)
		done <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	// Shutdown returns once the generation released its marker; Generate's return value reaches
	// done a moment later, so wait briefly instead of polling once.
	select {
	case err := <-done:
		wantAppErr(t, err, apperr.KindExternalService, "generation_failed", "The model call failed")
	case <-time.After(2 * time.Second):
		t.Fatal("the generation did not end after Shutdown returned")
	}
	if len(f.store.Saved) != 0 {
		t.Error("a generation cancelled by Shutdown stored a brief")
	}
}

func TestShutdown_WithoutGenerationsReturnsAtOnce(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}
}

func TestShutdown_ReportsGenerationsThatDoNotStop(t *testing.T) {
	f := newFixture(t)
	gate := make(chan struct{})
	defer close(gate)
	entered := make(chan struct{}, 1)
	f.llm.Enter = func(context.Context) { entered <- struct{}{}; <-gate } // ignores its context
	go func() { _, _ = f.svc.Generate(context.Background(), wsID, prID) }()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := f.svc.Shutdown(ctx); !errors.Is(err, inflight.ErrNotDrained) {
		t.Errorf("Shutdown() error = %v, want inflight.ErrNotDrained", err)
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

func withSpend(b *fakeSpend) func(*app.Deps) { return func(d *app.Deps) { d.Spend = b } }

func capReached() error {
	return apperr.PaymentRequired("Monthly spend cap of $50.00 reached").WithCode("spend_cap_reached")
}

func TestGenerate_SpendCap_ReachedIs402AndStartsNothing(t *testing.T) {
	spend := &fakeSpend{err: capReached()}
	f := newFixture(t, withSpend(spend))
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap of $50.00 reached")
	if len(f.llm.Requests) != 0 || len(f.store.Saved) != 0 || len(f.issues.Calls) != 0 || len(f.blast.Calls) != 0 {
		t.Errorf("model calls, saved briefs, issue reads, blast reads = %d, %d, %d, %d, want none",
			len(f.llm.Requests), len(f.store.Saved), len(f.issues.Calls), len(f.blast.Calls))
	}
	if recs := f.logs.records(t); len(recs) != 0 {
		t.Errorf("log lines = %v, want none (no generation started)", recs)
	}
	// No marker was left behind: once the cap is lifted the same pull request generates.
	spend.setErr(nil)
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err != nil {
		t.Errorf("Generate() after the refusal = %v, want a brief (the 402 left a marker)", err)
	}
}

func TestGenerate_SpendCap_ReadErrorFailsClosedWith500(t *testing.T) {
	boom := errors.New("ledger down")
	f := newFixture(t, withSpend(&fakeSpend{err: boom}))
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	if !errors.Is(err, boom) {
		t.Fatalf("Generate() error = %v, want the Admit error unchanged", err)
	}
	if _, isApp := apperr.As(err); isApp {
		t.Errorf("Generate() error = %v, want a plain error that httpx renders as 500", err)
	}
	if len(f.llm.Requests) != 0 || len(f.store.Saved) != 0 {
		t.Errorf("model calls, saved briefs = %d, %d, want none", len(f.llm.Requests), len(f.store.Saved))
	}
}

func TestGenerate_SpendCap_NilBudgetChangesNothing(t *testing.T) {
	f := newFixture(t)
	got, err := f.svc.Generate(t.Context(), wsID, prID)
	if err != nil || got.Brief == nil {
		t.Fatalf("Generate() = %+v, %v, want a brief", got, err)
	}
}

func TestGenerate_SpendCap_CheckedOnceWithTheOperation(t *testing.T) {
	spend := &fakeSpend{}
	f := newFixture(t, withSpend(spend))
	if _, err := f.svc.Generate(t.Context(), wsID, prID); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if diff := cmp.Diff([]admitCall{{wsID, "brief_generate"}}, spend.calls, cmp.AllowUnexported(admitCall{})); diff != "" {
		t.Errorf("Admit calls mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerate_SpendCap_UnknownPullRequestIs404NotChecked(t *testing.T) {
	spend := &fakeSpend{err: capReached()}
	f := newFixture(t, withSpend(spend))
	f.store.PullMissing = true
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	if len(spend.calls) != 0 {
		t.Errorf("Admit calls = %v, want none: the 404 comes first", spend.calls)
	}
}

func TestGenerate_SpendCap_WinsOverTheConflict(t *testing.T) {
	spend := &fakeSpend{}
	f := newFixture(t, withSpend(spend))
	entered, release := holdLLM(f)
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Generate(context.Background(), wsID, prID)
		done <- err
	}()
	<-entered

	spend.setErr(capReached())
	_, err := f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap of $50.00 reached")

	spend.setErr(nil)
	_, err = f.svc.Generate(t.Context(), wsID, prID)
	wantAppErr(t, err, apperr.KindConflict, "generation_in_progress", "A brief is already being generated for this pull request")

	release()
	if err := <-done; err != nil {
		t.Errorf("running Generate() error = %v, want it to finish: a reached cap does not stop admitted work", err)
	}
}
