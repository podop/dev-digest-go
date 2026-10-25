package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// baseHash is the Node key hash of basePull() with the default review_intent model
// (backend/internal/parity/testdata/goldens/intent.json, scenario two_callers_same_key_share_one_llm_call).
const baseHash = "3d306092dcd3e4d2ebd4d5c70a5c7e2a5d7e9d92d42ad2beb59791c56089166c"

var (
	derivedEvents = []ev{
		{Kind: "info", Msg: "PR intent sources: 2 used, 0 skipped, 0 failed"},
		{Kind: "result", Msg: "PR intent derived (confidence=high, 1200+180 tok, $0.0004)"},
	}
	titleSource = reviewer.IntentSource{Kind: "title", Ref: "title", Status: "used"}
	bodySource  = reviewer.IntentSource{Kind: "body", Ref: "body", Status: "used"}
)

func TestResolve_DerivesAndStoresOnACacheMiss(t *testing.T) {
	w := newWorld(t, app.Options{})
	var c capture

	got := w.svc.Resolve(t.Context(), w.input(false), c.sink)

	want := domain.Record{
		PRID: prID, Text: "Add a token-bucket rate limiter to the public webhook endpoints.",
		InScope: []string{"Rate limiting middleware", "Limits in the config"}, OutOfScope: []string{"Authentication changes"},
		ChangeType: ptr("feature"), Confidence: ptr("high"), DerivedFrom: "explicit",
		Sources: []reviewer.IntentSource{titleSource, bodySource},
		HeadSHA: headSHA, InputHash: baseHash, PromptVersion: 2, Provider: "openrouter", Model: "deepseek/deepseek-v4-flash",
		TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042), DerivedAt: fixedNow,
	}
	if diff := cmp.Diff(app.Result{Used: true, Record: want}, got); diff != "" {
		t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(derivedEvents, c.got()); diff != "" {
		t.Errorf("Resolve() events mismatch (-want +got):\n%s", diff)
	}
	if stored, ok := w.store.row(prID); !ok || stored.InputHash != baseHash {
		t.Errorf("stored row = %+v, %v, want the derived record", stored, ok)
	}
	if n := w.svc.InFlight(); n != 0 {
		t.Errorf("InFlight() = %d after the derivation, want 0", n)
	}
}

func TestResolve_ClassificationRequest(t *testing.T) {
	w := newWorld(t, app.Options{})

	w.svc.Resolve(t.Context(), w.input(false), nil)

	calls := w.llm.calls()
	if len(calls) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(calls))
	}
	req := calls[0]
	block := domain.RenderSourcesBlock([]domain.Source{
		domain.TitleSource(basePull().Title), mustBody(t, basePull().Body),
	})
	want := llm.Request{
		Provider: llm.OpenRouter, Model: "deepseek/deepseek-v4-flash",
		Messages: []llm.Message{
			{Role: llm.System, Content: domain.SystemPrompt},
			{Role: llm.User, Content: domain.UserMessage(block)},
		},
		SchemaName: "IntentClassification", MaxTokens: 5000, Reprompts: 1, Feature: "intent",
		CorrelationID: "intent:" + prID + ":" + baseHash[:12],
		Charge:        &llm.Charge{WorkspaceID: wsID, Kind: llm.KindReviewIntent, RefID: prID},
	}
	opts := []cmp.Option{
		cmp.FilterPath(func(p cmp.Path) bool { s := p.String(); return s == "Schema" || s == "Temperature" || s == "Sections" }, cmp.Ignore()),
		cmp.FilterPath(func(p cmp.Path) bool { return p.String() == "OnUsage" }, cmp.Ignore()),
	}
	if diff := cmp.Diff(want, req, opts...); diff != "" {
		t.Errorf("Structured request mismatch (-want +got):\n%s", diff)
	}
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Errorf("Temperature = %v, want 0", req.Temperature)
	}
	if string(req.Schema) != string(domain.ClassificationSchemaJSON) {
		t.Errorf("Schema is not the IntentClassification schema:\n%s", req.Schema)
	}
	items := 2
	wantSections := []llm.PromptSection{
		{Name: "system", Source: "engine", Trust: llm.Trusted, Chars: llm.UTF16Len(domain.SystemPrompt)},
		{Name: "task", Source: "engine", Trust: llm.Trusted, Chars: llm.UTF16Len(domain.TaskText())},
		{Name: "intent_sources", Source: "repo", Trust: llm.Untrusted, Chars: llm.UTF16Len(block), Items: &items},
	}
	if diff := cmp.Diff(wantSections, req.Sections); diff != "" {
		t.Errorf("prompt sections mismatch (-want +got):\n%s", diff)
	}
}

func mustBody(t *testing.T, body string) domain.Source {
	t.Helper()
	s, ok := domain.BodySource(body)
	if !ok {
		t.Fatal("BodySource: no source for a non-empty body")
	}
	return s
}

func TestResolve_UsesTheConfiguredFeatureModel(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.settings.setIntentModel("openai", "gpt-4.1")

	got := w.svc.Resolve(t.Context(), w.input(false), nil)

	if !got.Used || got.Record.Provider != "openai" || got.Record.Model != "gpt-4.1" {
		t.Fatalf("Resolve() = %+v, want a record of openai/gpt-4.1", got)
	}
	if calls := w.llm.calls(); len(calls) != 1 || calls[0].Provider != llm.OpenAI || calls[0].Model != "gpt-4.1" {
		t.Errorf("LLM request = %+v, want provider openai, model gpt-4.1", calls)
	}
	if got.Record.InputHash == baseHash {
		t.Errorf("InputHash = %s for another model, want a different key", got.Record.InputHash)
	}
}

func TestResolve_CacheHit(t *testing.T) {
	tests := []struct {
		name       string
		confidence *string
		wantMsg    string
	}{
		{"with confidence", ptr("high"), "PR intent ready (cached, confidence=high)"},
		{"null confidence prints like JavaScript", nil, "PR intent ready (cached, confidence=null)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, app.Options{})
			rec := domain.Record{PRID: prID, Text: "stored", InputHash: baseHash, Confidence: tc.confidence, DerivedFrom: "explicit", DerivedAt: fixedNow}
			w.store.put(rec)
			var c capture

			got := w.svc.Resolve(t.Context(), w.input(false), c.sink)

			if diff := cmp.Diff(app.Result{Used: true, Record: rec, CacheHit: true}, got); diff != "" {
				t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]ev{{Kind: "info", Msg: tc.wantMsg}}, c.got()); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
			if n := len(w.llm.calls()); n != 0 {
				t.Errorf("LLM calls = %d on a cache hit, want 0", n)
			}
		})
	}
}

func TestResolve_StoredRecordOfAnotherKeyIsReplaced(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.store.put(domain.Record{PRID: prID, Text: "old", InputHash: "other", DerivedAt: fixedNow})

	got := w.svc.Resolve(t.Context(), w.input(false), nil)

	if !got.Used || got.CacheHit || got.Record.InputHash != baseHash {
		t.Fatalf("Resolve() = %+v, want a fresh derivation with hash %s", got, baseHash)
	}
	if n := len(w.llm.calls()); n != 1 {
		t.Errorf("LLM calls = %d, want 1", n)
	}
}

func TestResolve_ForceSkipsOnlyTheCacheCheck(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.store.put(domain.Record{PRID: prID, Text: "stored", InputHash: baseHash, DerivedAt: fixedNow})

	got := w.svc.Resolve(t.Context(), w.input(true), nil)

	if !got.Used || got.CacheHit || got.Record.Text == "stored" {
		t.Fatalf("Resolve(force) = %+v, want a fresh derivation", got)
	}
	if n := len(w.llm.calls()); n != 1 {
		t.Errorf("LLM calls = %d, want 1", n)
	}
}

func TestResolve_ChargeKindFollowsWhoStartedTheDerivation(t *testing.T) { // FR1
	for _, tc := range []struct {
		name  string
		force bool
		want  llm.UsageKind
	}{
		{name: "review run", force: false, want: llm.KindReviewIntent},
		{name: "refresh", force: true, want: llm.KindIntentRefresh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, app.Options{})

			w.svc.Resolve(t.Context(), w.input(tc.force), nil)

			calls := w.llm.calls()
			if len(calls) != 1 {
				t.Fatalf("LLM calls = %d, want 1", len(calls))
			}
			want := &llm.Charge{WorkspaceID: wsID, Kind: tc.want, RefID: prID}
			if diff := cmp.Diff(want, calls[0].Charge); diff != "" {
				t.Errorf("Structured request Charge mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolve_ThinPullRequestIsInferredFromTheFallbackSources(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.pulls.pull.Body = ""
	w.pulls.commits = []domain.Commit{{SHA: "a", Message: "add limiter"}, {SHA: "b", Message: "wire config"}}
	in := w.input(false)
	in.Pull = *w.pulls.pull
	in.DiffExcerpt = "diff --git a/x b/x"

	got := w.svc.Resolve(t.Context(), in, nil)

	if !got.Used {
		t.Fatalf("Resolve() = %+v, want used", got)
	}
	wantSources := []reviewer.IntentSource{
		titleSource,
		{Kind: "commits", Ref: "commits", Status: "used"},
		{Kind: "branch", Ref: "branch", Status: "used"},
		{Kind: "diff", Ref: "changed-files", Status: "used"},
	}
	if diff := cmp.Diff(wantSources, got.Record.Sources); diff != "" {
		t.Errorf("sources mismatch (-want +got):\n%s", diff)
	}
	if got.Record.DerivedFrom != "inferred" || *got.Record.Confidence != "low" {
		t.Errorf("derived_from, confidence = %q, %q, want inferred, low", got.Record.DerivedFrom, *got.Record.Confidence)
	}
	user := w.llm.calls()[0].Messages[1].Content
	for _, want := range []string{
		"### Commit messages\n<untrusted source=\"intent-commits-commits\">\nadd limiter\nwire config\n</untrusted>",
		"### Branch name\n<untrusted source=\"intent-branch-branch\">\nfeat/rate-limit\n</untrusted>",
		"### Changed files\n<untrusted source=\"intent-diff-changed-files\">\nChanged files:\nsrc/middleware/ratelimit.ts\nsrc/config.ts\n\ndiff --git a/x b/x\n</untrusted>",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user message lacks %q:\n%s", want, user)
		}
	}
}

func TestResolve_ALoadedTicketMakesAThinPullRequestExplicit(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.pulls.pull.Body = "Fixes #471"
	w.tickets.reads[471] = app.TicketRead{OK: true, Title: "Flooding", Body: "Unauthenticated callers flood the webhooks."}
	in := w.input(false)
	in.Pull = *w.pulls.pull

	got := w.svc.Resolve(t.Context(), in, nil)

	wantSources := []reviewer.IntentSource{titleSource, {Kind: "body", Ref: "body", Status: "used"}, {Kind: "ticket", Ref: "471", Status: "used"}}
	if diff := cmp.Diff(wantSources, got.Record.Sources); diff != "" {
		t.Errorf("sources mismatch (-want +got):\n%s", diff)
	}
	if got.Record.DerivedFrom != "explicit" || *got.Record.Confidence != "high" {
		t.Errorf("derived_from, confidence = %q, %q, want explicit, high", got.Record.DerivedFrom, *got.Record.Confidence)
	}
	if diff := cmp.Diff([]float64{471}, w.tickets.calls); diff != "" {
		t.Errorf("ticket reads mismatch (-want +got):\n%s", diff)
	}
	user := w.llm.calls()[0].Messages[1].Content
	if want := "### Linked ticket #471\n<untrusted source=\"intent-ticket-471\">\nFlooding\n\nUnauthenticated callers flood the webhooks.\n</untrusted>"; !strings.Contains(user, want) {
		t.Errorf("user message lacks the ticket block:\n%s", user)
	}
}

func TestResolve_FailedAndSkippedReferencesLowerTheConfidence(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.pulls.pull.Body = "Closes #7, see acme/other#9 and docs/plans/limits.md and docs/missing.md.\n\nA long enough description to count as substantive."
	w.tickets.reads[7] = app.TicketRead{Reason: "GITHUB_TOKEN is not configured"}
	w.docs.reads["docs/plans/limits.md"] = app.DocRead{OK: true, Content: "Plan text", Truncated: true}
	in := w.input(false)
	in.Pull = *w.pulls.pull

	got := w.svc.Resolve(t.Context(), in, nil)

	wantSources := []reviewer.IntentSource{
		titleSource,
		{Kind: "body", Ref: "body", Status: "used"},
		{Kind: "ticket", Ref: "7", Status: "failed", Detail: ptr("GITHUB_TOKEN is not configured")},
		{Kind: "ticket", Ref: "acme/other#9", Status: "skipped", Detail: ptr("issue reference from a different repository (acme/other)")},
		{Kind: "doc", Ref: "docs/plans/limits.md", Status: "truncated"},
		{Kind: "doc", Ref: "docs/missing.md", Status: "failed", Detail: ptr("not found")},
	}
	if diff := cmp.Diff(wantSources, got.Record.Sources); diff != "" {
		t.Errorf("sources mismatch (-want +got):\n%s", diff)
	}
	if *got.Record.Confidence != "medium" {
		t.Errorf("confidence = %q, want medium", *got.Record.Confidence)
	}
	if diff := cmp.Diff([]string{headSHA + ":docs/plans/limits.md", headSHA + ":docs/missing.md"}, w.docs.calls); diff != "" {
		t.Errorf("doc reads (head sha, path) mismatch (-want +got):\n%s", diff)
	}
}

func TestResolve_DocBudgetIsSharedByAllDocs(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.pulls.pull.Body = "See docs/plans/a.md docs/plans/b.md docs/plans/c.md docs/plans/d.md docs/plans/e.md for the design of this change."
	big := strings.Repeat("x", 9000)
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		w.docs.reads["docs/plans/"+p+".md"] = app.DocRead{OK: true, Content: big}
	}
	in := w.input(false)
	in.Pull = *w.pulls.pull

	got := w.svc.Resolve(t.Context(), in, nil)

	var statuses []string
	for _, s := range got.Record.Sources {
		if s.Kind == "doc" {
			statuses = append(statuses, s.Status)
		}
	}
	// 8,000 + 8,000 + 4,000 units of the 20,000 budget are read; the rest is skipped unread.
	if diff := cmp.Diff([]string{"truncated", "truncated", "truncated", "skipped", "skipped"}, statuses); diff != "" {
		t.Errorf("doc statuses mismatch (-want +got):\n%s", diff)
	}
	if n := len(w.docs.calls); n != 3 {
		t.Errorf("doc reads = %d, want 3 (the budget is gone before the 4th)", n)
	}
}

func TestResolve_ClampsTheModelAnswer(t *testing.T) {
	w := newWorld(t, app.Options{})
	long := strings.Repeat("a", 600)
	scope := make([]string, 12)
	for i := range scope {
		scope[i] = strings.Repeat("b", 300)
	}
	scope[1] = "   "
	w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) {
		return answer(`{"intent":"` + long + `","in_scope":["` + strings.Join(scope, `","`) + `"],"out_of_scope":[],"change_type":"perf"}`), nil
	})

	got := w.svc.Resolve(t.Context(), w.input(false), nil)

	if n := len([]rune(got.Record.Text)); n != 500 || !strings.HasSuffix(got.Record.Text, "…") {
		t.Errorf("intent text = %d runes ending %q, want 500 cut with an ellipsis", n, got.Record.Text[len(got.Record.Text)-3:])
	}
	// The first ten items are kept, the blank one among them is dropped, each is cut to 240 units.
	if len(got.Record.InScope) != 9 || len([]rune(got.Record.InScope[0])) != 240 {
		t.Errorf("in_scope = %d items (first %d runes), want 9 items cut to 240", len(got.Record.InScope), len([]rune(got.Record.InScope[0])))
	}
	if got.Record.OutOfScope == nil || len(got.Record.OutOfScope) != 0 || *got.Record.ChangeType != "perf" {
		t.Errorf("out_of_scope, change_type = %#v, %v, want [] and perf", got.Record.OutOfScope, got.Record.ChangeType)
	}
}

func TestResolve_EventCostComesFromTheCallNotTheRow(t *testing.T) {
	tests := []struct {
		name string
		cost *float64
		want string
	}{
		{"priced", ptr(0.0004236789), "PR intent derived (confidence=high, 1200+180 tok, $0.0004)"},
		{"half rounds away from zero", ptr(0.00005), "PR intent derived (confidence=high, 1200+180 tok, $0.0001)"},
		{"zero", ptr(0.0), "PR intent derived (confidence=high, 1200+180 tok, $0.0000)"},
		{"unpriced has no cost part", nil, "PR intent derived (confidence=high, 1200+180 tok)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, app.Options{})
			w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) {
				r := answer(defaultAnswer)
				r.CostUSD = tc.cost
				return r, nil
			})
			var c capture

			got := w.svc.Resolve(t.Context(), w.input(false), c.sink)

			events := c.got()
			if len(events) != 2 || events[1].Msg != tc.want {
				t.Errorf("events = %v, want the result event %q", events, tc.want)
			}
			if tc.cost != nil && (got.Record.CostUSD == nil) {
				t.Errorf("record cost = nil, want the rounded cost")
			}
			if tc.cost == nil && got.Record.CostUSD != nil {
				t.Errorf("record cost = %v, want nil for an unpriced call", *got.Record.CostUSD)
			}
		})
	}
}

func TestResolve_UnavailableWhenAStepFails(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		arrange     func(w *world)
		wantWarning string
		wantLLM     int
	}{
		{"malformed URI escape in a doc link", func(w *world) {
			w.pulls.pull.Body = "[spec](https://github.com/acme/api/blob/main/docs/%E0%A4%A.md)"
		}, "URI malformed", 0},
		{"settings cannot be read", func(w *world) { w.settings.setErr(errors.New("settings unavailable")) }, "settings unavailable", 0},
		{"commits cannot be read", func(w *world) { w.pulls.commitsErr = boom }, "boom", 0},
		{"stored record cannot be read", func(w *world) { w.store.getErr = boom }, "boom", 0},
		{"missing provider key", func(w *world) {
			w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) {
				return llm.Result{}, errors.New("OPENROUTER_API_KEY is not configured")
			})
		}, "OPENROUTER_API_KEY is not configured", 1},
		{"unreadable answer", func(w *world) {
			w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) { return answer(`[]`), nil })
		}, "decode the classification: json: cannot unmarshal array into Go value of type app.classification", 1},
		{"upsert fails", func(w *world) { w.store.putErr = boom }, "boom", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, app.Options{})
			tc.arrange(w)
			in := w.input(false)
			in.Pull = *w.pulls.pull
			var c capture

			got := w.svc.Resolve(t.Context(), in, c.sink)

			if diff := cmp.Diff(app.Result{Warning: tc.wantWarning}, got); diff != "" {
				t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
			}
			wantEvents := []ev{{Kind: "info", Msg: "warning: intent unavailable — " + tc.wantWarning, Data: `{"warning":"intent_unavailable"}`}}
			events := c.got()
			if tc.wantLLM > 0 { // the sources event goes out after the call, before the upsert
				events = events[len(events)-1:]
			}
			if diff := cmp.Diff(wantEvents, events); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
			if n := len(w.llm.calls()); n != tc.wantLLM {
				t.Errorf("LLM calls = %d, want %d", n, tc.wantLLM)
			}
			if n := w.store.upsertCount(); n != 0 {
				t.Errorf("rows written = %d, want 0", n)
			}
		})
	}
}

func TestResolve_ANilSinkDiscardsEvents(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.settings.setErr(errors.New("settings unavailable"))

	if got := w.svc.Resolve(t.Context(), w.input(false), nil); got.Used || got.Warning != "settings unavailable" {
		t.Errorf("Resolve() = %+v, want unavailable", got)
	}
}

func TestResolve_APanicInTheModelCallIsReRaisedOnTheCaller(t *testing.T) {
	w := newWorld(t, app.Options{})
	w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) { panic("model client broke") })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Resolve() returned, want the flight's panic re-raised")
		}
		if err, ok := r.(error); !ok || !strings.Contains(err.Error(), "model client broke") {
			t.Errorf("recovered %v, want a PanicError carrying the original value", r)
		}
		if n := w.svc.InFlight(); n != 0 {
			t.Errorf("InFlight() = %d after a panic, want 0", n)
		}
	}()
	w.svc.Resolve(t.Context(), w.input(false), nil)
}

func TestStored(t *testing.T) {
	w := newWorld(t, app.Options{})
	if got, err := w.svc.Stored(t.Context(), prID); got != nil || err != nil {
		t.Fatalf("Stored() = %v, %v with no row, want nil, nil", got, err)
	}
	w.store.put(domain.Record{
		PRID: prID, Text: "stored", InScope: []string{"a"}, OutOfScope: []string{}, ChangeType: ptr("bugfix"),
		Confidence: ptr("low"), DerivedFrom: "inferred", Sources: []reviewer.IntentSource{titleSource}, InputHash: "h", DerivedAt: fixedNow,
	})

	got, err := w.svc.Stored(t.Context(), prID)

	want := &reviewer.Intent{
		Text: "stored", InScope: []string{"a"}, OutOfScope: []string{}, ChangeType: ptr("bugfix"), Confidence: ptr("low"),
		DerivedFrom: "inferred", Sources: []reviewer.IntentSource{titleSource},
	}
	if err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("Stored() = %+v, %v, want %+v", got, err, want)
	}

	w.store.getErr = errors.New("db down")
	if got, err := w.svc.Stored(t.Context(), prID); got != nil || err == nil {
		t.Errorf("Stored() = %v, %v on a store error, want nil and an error", got, err)
	}
}
