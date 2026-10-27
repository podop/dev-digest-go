package briefhttp

// Node parity of the whole brief path: every flow of the Node golden (BriefService.get/generate
// over fake ports, goldens/brief.json) runs here through the real Echo instance, the real
// transport and the real use case over the fakes of brieftest, and must give the same response
// (key order included), error, log lines, model request, prompt log, document, issue, intent and
// blast reads and stored brief. durationMs is not parity data (the Node run used a logical clock).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/app/brieftest"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	smartdiff "dev-digest-go/backend/internal/smartdiff/domain"
	"dev-digest-go/backend/internal/tenancy"
)

// The goldens name the pull request "pr-1"; the routes take UUIDs, so both sides use this one.
const (
	goldenPR = "pr-1"
	flowPR   = "7e1d0000-0000-4000-8000-000000000001"
)

type flowFile struct {
	Flows []flowCase `json:"flows"`
}

type flowCase struct {
	Name        string          `json:"name"`
	Call        string          `json:"call"`
	World       world           `json:"world"`
	Stored      json.RawMessage `json:"stored"`
	Response    json.RawMessage `json:"response"`
	Error       *wantError      `json:"error"`
	Logs        []wantLog       `json:"logs"`
	LLMRequests []wantLLM       `json:"llmRequests"`
	Upserts     []string        `json:"upserts"`
	PromptLog   []wantPrompt    `json:"promptLog"`
	SpecCalls   []wantSpecCall  `json:"specCalls"`
	IssueCalls  []wantIssue     `json:"issueCalls"`
	IntentCalls int             `json:"intentCalls"`
	BlastCalls  int             `json:"blastCalls"`
	StoredAfter json.RawMessage `json:"storedAfter"`
}

type world struct {
	Workspace   string `json:"workspace"`
	Now         string `json:"now"`
	Model       struct{ Provider, Model string }
	TimeoutMs   int  `json:"timeoutMs"`
	PullMissing bool `json:"pullMissing"`
	Pull        struct {
		ID, RepoID string
		Repo       struct{ Owner, Name string }
		Title      string
		Body       *string
		Base       string
		HeadSha    string
	} `json:"pull"`
	Files []struct {
		Path                 string
		Additions, Deletions int
		Patch                *string
	} `json:"files"`
	Reviews []struct {
		AgentID   *string
		CreatedAt string
		Findings  []struct {
			Severity, File     string
			StartLine, EndLine int
			Title              string
		}
	} `json:"reviews"`
	Specs        map[string][]specDoc `json:"specs"`
	Intent       json.RawMessage      `json:"intent"`
	Blast        json.RawMessage      `json:"blast"`
	Issue        json.RawMessage      `json:"issue"`
	LLM          llmScript            `json:"llm"`
	ResolveError *string              `json:"resolveError"`
}

type specDoc struct {
	Path   string          `json:"path"`
	Status string          `json:"status"`
	Text   json.RawMessage `json:"text"`
}

type llmScript struct {
	Data      json.RawMessage `json:"data"`
	TokensIn  int             `json:"tokensIn"`
	TokensOut int             `json:"tokensOut"`
	CostUSD   *float64        `json:"costUsd"`
	Attempts  int             `json:"attempts"`
	Usages    []struct {
		TokensIn, TokensOut int
		CostUSD             *float64
	} `json:"usages"`
	Error string `json:"error"`
	Hang  bool   `json:"hang"`
}

type wantError struct {
	Name, Kind, Code, Message string
	Details                   json.RawMessage
}

type wantLog struct {
	Level string
	Msg   string
	Obj   map[string]any
}

type wantLLM struct {
	Provider, Model, SchemaName string
	Temperature                 float64
	MaxTokens, MaxRetries       int
	Roles                       []string
	SystemIsTheNodeConstant     bool
	User                        json.RawMessage
}

type wantPrompt struct {
	Feature, CorrelationID, Provider, Model string
	Sections                                []struct {
		Name, Source, Role, Trust string
		Chars, Tokens             int
		Items                     *int
	}
}

type wantSpecCall struct {
	AgentID, RepoID string
	Repo            struct{ Owner, Name string }
	Base, HeadSha   string
}

type wantIssue struct {
	Repo   struct{ Owner, Name string }
	Number int
}

// gap is a flow whose golden world holds a state the Go types cannot express.
type gap struct {
	// extraSpec is a document Go puts in specs_used and the prompt, Node does not.
	extraSpec string
	// skipPrompt skips what depends on that document: the exact user message, the estimate and the
	// prompt log sections (the block is asserted instead).
	skipPrompt bool
}

// representationGaps: the golden's `docs/c.md` is {status "included"} with no `text` key, which
// Node's `status === 'included' && text !== undefined` keeps out of the prompt while it still
// counts as attached. A Go domain.Document has a string Text, and the project-context resolver
// always sets it for an included document (an empty file gives ""), so the state does not exist
// here: the document is attached, included and empty, like Node's empty-file case.
var representationGaps = map[string]gap{
	"generate_spec_documents_first_path_wins_and_unreadable_counts": {extraSpec: "docs/c.md", skipPrompt: true},
}

func loadFlows(t *testing.T) []flowCase {
	t.Helper()
	raw, err := os.ReadFile("../../../parity/testdata/goldens/brief.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	raw = bytes.ReplaceAll(raw, []byte(goldenPR), []byte(flowPR))
	var f flowFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(f.Flows) < 35 {
		t.Fatalf("golden holds %d flows, want the whole list", len(f.Flows))
	}
	return f.Flows
}

func TestFlows_NodeParity(t *testing.T) {
	for _, fl := range loadFlows(t) {
		t.Run(fl.Name, func(t *testing.T) { runFlow(t, fl) })
	}
}

// logBuf collects the service's JSON log lines.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

type tenants struct{ ws string }

func (t tenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: t.ws}, nil
}

func runFlow(t *testing.T, fl flowCase) {
	w := fl.World
	store, intents, blast, docs, issues, models, model := fakesOf(t, fl)
	if len(fl.Stored) > 0 && string(fl.Stored) != "null" {
		b := briefOf(t, fl.Stored)
		store.Stored = &b
	}
	logs := &logBuf{}
	svc := app.NewService(app.Deps{
		Store: store, Intents: intents, Blast: blast, Documents: docs, Issues: issues,
		Roles: roleFake{}, LLM: model, Models: models,
		Clock:  func() time.Time { return mustTime(t, w.Now) },
		Log:    slog.New(slog.NewJSONHandler(logs, nil)),
		Budget: time.Duration(w.TimeoutMs) * time.Millisecond,
	})
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), AllowedHosts: []string{"127.0.0.1"}})
	AddRoutes(e, svc, tenants{ws: w.Workspace})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	method := http.MethodPost
	if fl.Call == "get" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+"/pulls/"+flowPR+"/brief", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	gap := representationGaps[fl.Name]
	var responseBrief json.RawMessage
	if fl.Error != nil {
		checkError(t, fl.Error, resp.StatusCode, body)
	} else {
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d (%s), want 200", resp.StatusCode, body)
		}
		compared := body
		if gap.extraSpec != "" {
			compared = bytes.Replace(body, []byte(`,"`+gap.extraSpec+`"]`), []byte(`]`), 1)
		}
		checkJSON(t, "response", fl.Response, compared)
		var r struct {
			Brief json.RawMessage `json:"brief"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		responseBrief = r.Brief
	}

	var ignoredLogKeys []string
	if gap.skipPrompt {
		ignoredLogKeys = []string{"estTokensIn"}
	}
	checkLogs(t, fl.Logs, logs, ignoredLogKeys...)
	checkModelCalls(t, fl, model)
	checkReads(t, fl, intents, blast, docs, issues)
	checkStorage(t, fl, store, responseBrief)
}

// fakesOf builds the ports from the golden's world.
func fakesOf(t *testing.T, fl flowCase) (*brieftest.Store, *brieftest.Intents, *brieftest.Blast, *brieftest.Documents, *brieftest.Issues, *brieftest.Models, *brieftest.LLM) {
	t.Helper()
	w := fl.World
	store := &brieftest.Store{
		Pull: domain.Pull{
			ID: w.Pull.ID, WorkspaceID: w.Workspace, RepoID: w.Pull.RepoID, Owner: w.Pull.Repo.Owner, Name: w.Pull.Repo.Name,
			Number: 7, Title: w.Pull.Title, Body: w.Pull.Body, Base: w.Pull.Base, HeadSHA: w.Pull.HeadSha,
		},
		PullMissing: w.PullMissing,
	}
	for _, f := range w.Files {
		sf := domain.StoredFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
		if f.Patch != nil {
			sf.Patch = *f.Patch
		}
		store.Files = append(store.Files, sf)
	}
	for i, r := range w.Reviews {
		rf := domain.ReviewFacts{ReviewID: fmt.Sprintf("r%d", i), AgentID: r.AgentID}
		for _, f := range r.Findings {
			rf.Findings = append(rf.Findings, domain.FindingFact{Severity: reviewer.Severity(f.Severity), File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Title: f.Title})
		}
		store.Reviews = append(store.Reviews, rf)
	}

	intents := &brieftest.Intents{}
	if throws, ok := throwsOf(w.Intent); ok {
		intents.Err = errors.New(throws)
	} else if len(w.Intent) > 0 && string(w.Intent) != "null" {
		var in struct {
			Intent      string
			InScope     []string `json:"in_scope"`
			OutOfScope  []string `json:"out_of_scope"`
			ChangeType  *string  `json:"change_type"`
			Confidence  *string
			DerivedFrom string `json:"derived_from"`
			Sources     []struct {
				Kind, Ref, Status string
				Detail            *string
			}
		}
		decode(t, w.Intent, &in)
		it := &reviewer.Intent{Text: in.Intent, InScope: in.InScope, OutOfScope: in.OutOfScope, ChangeType: in.ChangeType, Confidence: in.Confidence, DerivedFrom: in.DerivedFrom}
		for _, s := range in.Sources {
			it.Sources = append(it.Sources, reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
		}
		intents.Intent = it
	}

	blast := &brieftest.Blast{}
	if throws, ok := throwsOf(w.Blast); ok {
		blast.Err = errors.New(throws)
	} else {
		var b blastWire
		decode(t, w.Blast, &b)
		blast.Blast = b.domain()
	}

	docs := &brieftest.Documents{ByAgent: map[string][]domain.Document{}}
	for agent, list := range w.Specs {
		for _, d := range list {
			doc := domain.Document{Path: d.Path, Status: domain.DocStatus(d.Status)}
			if len(d.Text) > 0 {
				doc.Text = textOf(t, d.Text)
			}
			docs.ByAgent[agent] = append(docs.ByAgent[agent], doc)
		}
	}

	issues := &brieftest.Issues{Err: errors.New("not found")}
	if len(w.Issue) > 0 && string(w.Issue) != "null" {
		var is struct {
			Number  int
			Title   string
			Body    *string
			DelayMs int
		}
		decode(t, w.Issue, &is)
		issues.Err = nil
		issues.Fetched = domain.Issue{Number: is.Number, Title: is.Title, Body: is.Body}
		issues.Hang = is.DelayMs > w.TimeoutMs // the budget ends first; the read is cut short
	}

	models := &brieftest.Models{Bag: map[string]json.RawMessage{
		"feature_models": json.RawMessage(fmt.Sprintf(`{"risk_brief":{"provider":%q,"model":%q}}`, w.Model.Provider, w.Model.Model)),
	}}
	if w.ResolveError != nil {
		models.Err = errors.New(*w.ResolveError)
	}

	s := w.LLM
	model := &brieftest.LLM{Hang: s.Hang}
	for _, u := range s.Usages {
		model.Usages = append(model.Usages, llm.AttemptUsage{TokensIn: u.TokensIn, TokensOut: u.TokensOut, CostUSD: u.CostUSD})
	}
	switch s.Error {
	case "":
		model.Result = llm.Result{Data: s.Data, TokensIn: s.TokensIn, TokensOut: s.TokensOut, CostUSD: s.CostUSD, Attempts: s.Attempts}
		if len(model.Usages) == 0 {
			model.Usages = []llm.AttemptUsage{{TokensIn: s.TokensIn, TokensOut: s.TokensOut, CostUSD: s.CostUSD}}
		}
	case "config":
		model.Err = apperr.Config("OPENAI_API_KEY is not configured")
	case "other":
		model.Err = errors.New("provider exploded")
	case "invalid":
		model.Err = apperr.ExternalService("structured output failed schema validation")
	default:
		t.Fatalf("unknown llm error script %q", s.Error)
	}
	return store, intents, blast, docs, issues, models, model
}

func throwsOf(raw json.RawMessage) (string, bool) {
	var m struct {
		Throws *string `json:"throws"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Throws == nil {
		return "", false
	}
	return *m.Throws, true
}

func decode(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return at
}

type roleFake struct{}

func (roleFake) RoleFor(provider, path string) string {
	return string(smartdiff.ClassifyFor(provider, path))
}

// blastWire is the golden's BlastRadius.
type blastWire struct {
	ChangedSymbols []struct{ Name, File, Kind string } `json:"changed_symbols"`
	Downstream     []struct {
		Symbol  string
		Callers []struct {
			Name, File string
			Line       int
		}
		EndpointsAffected []string `json:"endpoints_affected"`
		CronsAffected     []string `json:"crons_affected"`
	}
	Summary  string
	Degraded bool
	Reason   string
}

func (b blastWire) domain() domain.Blast {
	out := domain.Blast{Summary: b.Summary, Degraded: b.Degraded, Reason: b.Reason}
	for _, s := range b.ChangedSymbols {
		out.ChangedSymbols = append(out.ChangedSymbols, domain.BlastSymbol{Name: s.Name, File: s.File, Kind: s.Kind})
	}
	for _, d := range b.Downstream {
		g := domain.BlastGroup{Symbol: d.Symbol, EndpointsAffected: d.EndpointsAffected, CronsAffected: d.CronsAffected}
		for _, c := range d.Callers {
			g.Callers = append(g.Callers, domain.BlastCaller{Name: c.Name, File: c.File, Line: c.Line})
		}
		out.Downstream = append(out.Downstream, g)
	}
	return out
}

// textOf reads a golden text: a string, or the digest of one above 16,384 UTF-16 units, which is
// rebuilt from the repeating unit its head shows and checked against the digest.
func textOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blob struct {
		Len    int    `json:"len"`
		SHA256 string `json:"sha256"`
		Head   string `json:"head"`
	}
	decode(t, raw, &blob)
	head := []rune(blob.Head)
	for period := 1; period <= len(head)/2; period++ {
		unit := string(head[:period])
		if blob.Len%len(utf16.Encode(head[:period])) != 0 || !strings.HasPrefix(blob.Head, strings.Repeat(unit, len(head)/period)) {
			continue
		}
		text := strings.Repeat(unit, blob.Len/len(utf16.Encode(head[:period])))
		sum := sha256.Sum256([]byte(text))
		if len(utf16.Encode([]rune(text))) == blob.Len && hex.EncodeToString(sum[:]) == blob.SHA256 {
			return text
		}
	}
	t.Fatalf("cannot rebuild the text of blob %s", raw)
	return ""
}

// briefOf turns a golden's stored PrBrief document into the stored domain brief.
func briefOf(t *testing.T, raw json.RawMessage) domain.Brief {
	t.Helper()
	var dto briefDTO
	decode(t, raw, &dto)
	b := domain.Brief{
		Summary: dto.Summary, SpecsUsed: dto.SpecsUsed, HeadSHA: dto.HeadSHA, GeneratedAt: dto.GeneratedAt.Std(),
		PromptVersion: dto.PromptVersion, Provider: dto.Provider, Model: dto.Model,
		TokensIn: dto.TokensIn, TokensOut: dto.TokensOut, CostUSD: dto.CostUSD, ModelRequests: dto.ModelRequests,
	}
	for _, r := range dto.Risks.Risks {
		b.Risks = append(b.Risks, domain.Risk{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: domain.RiskSeverity(r.Severity), FileRefs: r.FileRefs})
	}
	for _, f := range dto.ReviewFocus {
		b.ReviewFocus = append(b.ReviewFocus, domain.FocusItem{File: f.File, Line: f.Line, Reason: f.Reason})
	}
	for _, m := range dto.MissingInputs {
		b.MissingInputs = append(b.MissingInputs, domain.MissingInput{Input: domain.MissingInputName(m.Input), Reason: domain.MissingReason(m.Reason), Detail: m.Detail})
	}
	if in := dto.Intent; in != nil {
		it := &reviewer.Intent{Text: in.Intent, InScope: in.InScope, OutOfScope: in.OutOfScope, ChangeType: in.ChangeType, Confidence: in.Confidence}
		if in.DerivedFrom != nil {
			it.DerivedFrom = *in.DerivedFrom
		}
		for _, s := range in.Sources {
			it.Sources = append(it.Sources, reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
		}
		b.Intent = it
	}
	if bl := dto.Blast; bl != nil {
		out := domain.Blast{Summary: bl.Summary, Degraded: bl.Degraded, Reason: bl.Reason}
		for _, s := range bl.ChangedSymbols {
			out.ChangedSymbols = append(out.ChangedSymbols, domain.BlastSymbol{Name: s.Name, File: s.File, Kind: s.Kind})
		}
		for _, d := range bl.Downstream {
			g := domain.BlastGroup{Symbol: d.Symbol, EndpointsAffected: d.EndpointsAffected, CronsAffected: d.CronsAffected}
			for _, c := range d.Callers {
				g.Callers = append(g.Callers, domain.BlastCaller{Name: c.Name, File: c.File, Line: c.Line})
			}
			out.Downstream = append(out.Downstream, g)
		}
		b.Blast = &out
	}
	return b
}

// checkJSON compares a golden JSON value with the body: same values, and the same key order.
func checkJSON(t *testing.T, what string, want json.RawMessage, got []byte) {
	t.Helper()
	var w, g any
	decode(t, want, &w)
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s is not JSON: %v: %s", what, err, got)
	}
	if diff := cmp.Diff(w, g); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", what, diff)
	}
	if diff := cmp.Diff(keysOf(t, want), keysOf(t, got)); diff != "" {
		t.Errorf("%s key order mismatch (-want +got):\n%s", what, diff)
	}
}

// keysOf lists every object key of a JSON document in document order.
func keysOf(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var keys []string
	if err := collectKeys(dec, &keys); err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	return keys
}

func collectKeys(dec *json.Decoder, keys *[]string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	for dec.More() {
		if delim == '{' {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			*keys = append(*keys, key.(string))
		}
		if err := collectKeys(dec, keys); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}

func checkError(t *testing.T, want *wantError, status int, body []byte) {
	t.Helper()
	wantStatus := map[string]int{"validation": 422, "not_found": 404, "conflict": 409, "external_service": 502, "": 500}[want.Kind]
	if status != wantStatus {
		t.Errorf("status = %d, want %d (%s)", status, wantStatus, want.Kind)
	}
	var got struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	decode(t, body, &got)
	wantCode, wantMessage := want.Code, want.Message
	if want.Kind == "" { // a plain Error: Node and Go both answer the generic 500
		wantCode, wantMessage = "internal_error", "Internal error"
	}
	if got.Error.Code != wantCode || got.Error.Message != wantMessage {
		t.Errorf("error = {%s %q}, want {%s %q}", got.Error.Code, got.Error.Message, wantCode, wantMessage)
	}
	if len(want.Details) > 0 {
		checkJSON(t, "error details", want.Details, got.Error.Details)
	} else if len(got.Error.Details) > 0 {
		t.Errorf("error details = %s, want none", got.Error.Details)
	}
}

func checkLogs(t *testing.T, want []wantLog, logs *logBuf, ignore ...string) {
	t.Helper()
	var got []wantLog
	logs.mu.Lock()
	lines := strings.Split(strings.TrimSpace(logs.buf.String()), "\n")
	logs.mu.Unlock()
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec map[string]any
		decode(t, json.RawMessage(line), &rec)
		l := wantLog{Level: strings.ToLower(rec["level"].(string)), Msg: rec["msg"].(string), Obj: map[string]any{}}
		for k, v := range rec {
			if k != "time" && k != "level" && k != "msg" && k != "durationMs" && !slices.Contains(ignore, k) {
				l.Obj[k] = v
			}
		}
		got = append(got, l)
	}
	// A Node model request line without token counts is a failure scripted without a response; the
	// Go client reports usage per received response, so a call that got none logs no such line.
	want = slices.DeleteFunc(slices.Clone(want), func(l wantLog) bool {
		_, counted := l.Obj["tokensIn"]
		return l.Msg == "pr brief model request" && !counted
	})
	for i := range want {
		delete(want[i].Obj, "durationMs") // not parity data
		for _, k := range ignore {
			delete(want[i].Obj, k)
		}
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("log lines mismatch (-want +got):\n%s", diff)
	}
}

func checkModelCalls(t *testing.T, fl flowCase, model *brieftest.LLM) {
	t.Helper()
	// The Node run never reached its provider when the key was missing; the Go client logs the
	// prompt and then fails on the key, so the request is made. Everything else is one for one.
	if fl.World.LLM.Error != "config" {
		if len(fl.LLMRequests) != len(model.Requests) {
			t.Fatalf("model requests = %d, want %d", len(model.Requests), len(fl.LLMRequests))
		}
		for i, want := range fl.LLMRequests {
			req := model.Requests[i]
			if string(req.Provider) != want.Provider || req.Model != want.Model || req.SchemaName != want.SchemaName ||
				req.MaxTokens != want.MaxTokens || req.Reprompts != want.MaxRetries || req.Temperature == nil || *req.Temperature != want.Temperature {
				t.Errorf("request %d = {%s %s %s max %d reprompts %d temp %v}, want %+v", i, req.Provider, req.Model, req.SchemaName,
					req.MaxTokens, req.Reprompts, req.Temperature, want)
			}
			if len(req.Messages) != 2 || string(req.Messages[0].Role) != want.Roles[0] || string(req.Messages[1].Role) != want.Roles[1] {
				t.Fatalf("request %d messages = %v, want roles %v", i, req.Messages, want.Roles)
			}
			if want.SystemIsTheNodeConstant && req.Messages[0].Content != domain.SystemPrompt {
				t.Errorf("request %d system message is not the Node prompt", i)
			}
			if gap := representationGaps[fl.Name]; gap.skipPrompt {
				if !strings.Contains(req.Messages[1].Content, "### Project spec document\n<untrusted source=\"spec\">\n"+gap.extraSpec+"\n") {
					t.Errorf("request %d user message lacks the block of %s", i, gap.extraSpec)
				}
			} else if !sameText(want.User, req.Messages[1].Content) {
				t.Errorf("request %d user message mismatch:\n got %q\nwant %s", i, clip(req.Messages[1].Content), clip(string(want.User)))
			}
			if req.OnUsage == nil {
				t.Errorf("request %d has no usage observer", i)
			}
		}
	}

	if representationGaps[fl.Name].skipPrompt {
		return
	}
	if len(fl.PromptLog) == 0 {
		if len(model.Requests) != 0 {
			t.Errorf("model requests = %d, want none: the Node run logged no prompt", len(model.Requests))
		}
		return
	}
	if len(model.Requests) != len(fl.PromptLog) {
		t.Fatalf("model requests = %d, want %d prompt logs", len(model.Requests), len(fl.PromptLog))
	}
	for i, want := range fl.PromptLog {
		req := model.Requests[i]
		if req.Feature != want.Feature || req.CorrelationID != want.CorrelationID || string(req.Provider) != want.Provider || req.Model != want.Model {
			t.Errorf("prompt log %d = {%s %s %s %s}, want {%s %s %s %s}", i, req.Feature, req.CorrelationID, req.Provider, req.Model,
				want.Feature, want.CorrelationID, want.Provider, want.Model)
		}
		type section struct {
			Name, Source, Role, Trust string
			Chars, Tokens             int
			Items                     *int
		}
		var wantSections, gotSections []section
		for _, s := range want.Sections {
			wantSections = append(wantSections, section(s))
		}
		for _, s := range req.Sections {
			role := "user"
			if s.Name == "system" {
				role = "system"
			}
			gotSections = append(gotSections, section{Name: s.Name, Source: s.Source, Role: role, Trust: s.Trust, Chars: s.Chars, Tokens: (s.Chars + 3) / 4, Items: s.Items})
		}
		if diff := cmp.Diff(wantSections, gotSections); diff != "" {
			t.Errorf("prompt log %d sections mismatch (-want +got):\n%s", i, diff)
		}
	}
}

// sameText reports whether got is the golden text: equal, or, for a digest of a text above
// 16,384 UTF-16 units, of the same length and SHA-256.
func sameText(want json.RawMessage, got string) bool {
	var s string
	if len(want) == 0 {
		return got == ""
	}
	if json.Unmarshal(want, &s) == nil {
		return got == s
	}
	var blob struct {
		Len    int    `json:"len"`
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(want, &blob) != nil {
		return false
	}
	sum := sha256.Sum256([]byte(got))
	return len(utf16.Encode([]rune(got))) == blob.Len && hex.EncodeToString(sum[:]) == blob.SHA256
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func checkReads(t *testing.T, fl flowCase, intents *brieftest.Intents, blast *brieftest.Blast, docs *brieftest.Documents, issues *brieftest.Issues) {
	t.Helper()
	if intents.Calls != fl.IntentCalls {
		t.Errorf("intent reads = %d, want %d", intents.Calls, fl.IntentCalls)
	}
	if len(blast.Calls) != fl.BlastCalls {
		t.Errorf("blast reads = %d, want %d", len(blast.Calls), fl.BlastCalls)
	}

	type specCall struct{ Agent, Repo, Owner, Name, Base, Head string }
	var wantSpecs, gotSpecs []specCall
	for _, c := range fl.SpecCalls {
		wantSpecs = append(wantSpecs, specCall{c.AgentID, c.RepoID, c.Repo.Owner, c.Repo.Name, c.Base, c.HeadSha})
	}
	for _, c := range docs.Calls {
		gotSpecs = append(gotSpecs, specCall{c.AgentID, c.Pull.RepoID, c.Pull.Owner, c.Pull.Name, c.Pull.Base, c.Pull.HeadSHA})
		if c.WorkspaceID != fl.World.Workspace {
			t.Errorf("document read for workspace %q, want %q", c.WorkspaceID, fl.World.Workspace)
		}
	}
	byAgent := func(a, b specCall) int { return strings.Compare(a.Agent, b.Agent) }
	slices.SortFunc(wantSpecs, byAgent)
	slices.SortFunc(gotSpecs, byAgent)
	if diff := cmp.Diff(wantSpecs, gotSpecs); diff != "" {
		t.Errorf("document reads mismatch (-want +got):\n%s", diff)
	}

	var wantIssues, gotIssues []string
	for _, c := range fl.IssueCalls {
		wantIssues = append(wantIssues, fmt.Sprintf("%s/%s#%d", c.Repo.Owner, c.Repo.Name, c.Number))
	}
	for i, n := range issues.Calls {
		gotIssues = append(gotIssues, fmt.Sprintf("%s#%d", issues.Repos[i], n))
	}
	if diff := cmp.Diff(wantIssues, gotIssues); diff != "" {
		t.Errorf("issue reads mismatch (-want +got):\n%s", diff)
	}
}

func checkStorage(t *testing.T, fl flowCase, store *brieftest.Store, responseBrief json.RawMessage) {
	t.Helper()
	wire := func(b domain.Brief) []byte {
		raw, err := json.Marshal(toBrief(b))
		if err != nil {
			t.Fatalf("encode brief: %v", err)
		}
		return raw
	}
	if len(fl.Upserts) != len(store.Saved) {
		t.Fatalf("stored briefs written = %d, want %d", len(store.Saved), len(fl.Upserts))
	}
	for i, u := range fl.Upserts {
		if u != "$response.brief" {
			t.Fatalf("unexpected upsert marker %q", u)
		}
		checkJSON(t, fmt.Sprintf("upsert %d", i), responseBrief, wire(store.Saved[i]))
	}
	switch s := strings.TrimSpace(string(fl.StoredAfter)); {
	case s == "null" || s == "":
		if store.Stored != nil {
			t.Errorf("stored brief after = %s, want none", wire(*store.Stored))
		}
	case s == `"$response.brief"`:
		if store.Stored == nil {
			t.Fatal("no stored brief after, want the response's")
		}
		checkJSON(t, "stored brief", responseBrief, wire(*store.Stored))
	default:
		if store.Stored == nil {
			t.Fatal("no stored brief after, want the previous one")
		}
		checkJSON(t, "stored brief", fl.StoredAfter, wire(*store.Stored))
	}
}
