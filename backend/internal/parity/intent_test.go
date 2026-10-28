package parity_test

// S30 (AC5019–AC5022, AC5025): the pure rules of the intent context against testdata/goldens/
// intent.json — link extraction, the cache key, the text rules, the sources block and the
// messages. The derivation flows and the single-flight scenarios need the intent service and are
// in intent_flows_test.go. The shared Node differential in intent/domain/node_parity_test.go runs
// seeded random inputs; these cases are the curated corpus of the generator.

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

type goldenRepo struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (r goldenRepo) domain() domain.Repo { return domain.Repo{Owner: r.Owner, Name: r.Name} }

type goldenSkipped struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

func skippedOf(in []goldenSkipped) []domain.Skipped {
	out := []domain.Skipped{}
	for _, s := range in {
		out = append(out, domain.Skipped{Ref: s.Ref, Reason: s.Reason})
	}
	return out
}

func TestIntentGolden_Constants(t *testing.T) {
	var g struct {
		Constants struct {
			SystemPrompt   string                     `json:"systemPrompt"`
			TaskText       string                     `json:"taskText"`
			UserMessageOfX string                     `json:"userMessageOfX"`
			SchemaName     string                     `json:"schemaName"`
			PromptVersion  int                        `json:"promptVersion"`
			Limits         map[string]json.RawMessage `json:"limits"`
			DocExtensions  []string                   `json:"docExtensions"`
		} `json:"constants"`
	}
	loadGolden(t, "intent", &g)
	c := g.Constants
	if domain.SystemPrompt != c.SystemPrompt {
		t.Errorf("SystemPrompt =\n%q\nwant\n%q", domain.SystemPrompt, c.SystemPrompt)
	}
	if got := domain.TaskText(); got != c.TaskText {
		t.Errorf("TaskText() = %q, want %q", got, c.TaskText)
	}
	if got := domain.UserMessage("X"); got != c.UserMessageOfX {
		t.Errorf("UserMessage(%q) = %q, want %q", "X", got, c.UserMessageOfX)
	}
	if domain.SchemaName != c.SchemaName || domain.PromptVersion != c.PromptVersion {
		t.Errorf("SchemaName, PromptVersion = %q, %d, want %q, %d", domain.SchemaName, domain.PromptVersion, c.SchemaName, c.PromptVersion)
	}

	num := func(k string) int {
		t.Helper()
		var n int
		if raw, ok := c.Limits[k]; !ok || json.Unmarshal(raw, &n) != nil {
			t.Fatalf("golden limit %q is missing or not a number: %s", k, raw)
		}
		return n
	}
	var rate struct {
		Max int `json:"max"`
	}
	if err := json.Unmarshal(c.Limits["refreshRateLimit"], &rate); err != nil {
		t.Fatalf("golden refreshRateLimit: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"TitleMaxUnits", domain.TitleMaxUnits, num("titleMaxChars")},
		{"BodyMaxUnits", domain.BodyMaxUnits, num("bodyMaxChars")},
		{"TicketMax", domain.TicketMax, num("ticketMax")},
		{"TicketMaxUnits", domain.TicketMaxUnits, num("ticketMaxChars")},
		{"DocMax", domain.DocMax, num("docMax")},
		{"DocFetchMaxBytes", domain.DocFetchMaxBytes, num("docFetchMaxBytes")},
		{"DocMaxUnits", domain.DocMaxUnits, num("docMaxChars")},
		{"DocTotalMaxUnits", domain.DocTotalMaxUnits, num("docTotalMaxChars")},
		{"CommitsMax", domain.CommitsMax, num("commitsMax")},
		{"CommitMaxUnits", domain.CommitMaxUnits, num("commitMaxChars")},
		{"BranchMaxUnits", domain.BranchMaxUnits, num("branchMaxChars")},
		{"ChangedFilesMax", domain.ChangedFilesMax, num("changedFilesMax")},
		{"DiffExcerptMaxUnits", domain.DiffExcerptMaxUnits, num("diffExcerptMaxChars")},
		{"SubstantiveBodyMinUnits", domain.SubstantiveBodyMinUnits, num("substantiveBodyMinChars")},
		{"IntentMaxUnits", domain.IntentMaxUnits, num("intentTextMaxChars")},
		{"ScopeMaxItems", domain.ScopeMaxItems, num("scopeMaxItems")},
		{"ScopeItemMaxUnits", domain.ScopeItemMaxUnits, num("scopeItemMaxChars")},
		// Deliberate divergence (2026-10-28): the Go budget is twice Node's (nodeIntentBudget).
		{"Budget (ms)", int(domain.Budget.Milliseconds()) / 2, num("budgetMs")},
		{"MaxOutputTokens", domain.MaxOutputTokens, num("llmMaxOutputTokens")},
		{"Reprompts", domain.Reprompts, num("llmMaxRetries")},
		{"RefreshRatePerMinute", domain.RefreshRatePerMinute, rate.Max},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d (Node constant)", tc.name, tc.got, tc.want)
		}
	}

	// The doc extensions are an unexported list: probe every golden one and a few that are not.
	for _, ext := range c.DocExtensions {
		if !domain.HasDocExtension("docs/a" + ext) {
			t.Errorf("HasDocExtension(%q) = false, want true", "docs/a"+ext)
		}
	}
	for _, p := range []string{"docs/a.png", "docs/a.md.bak", "docs/a", "docs/a.rst"} {
		if domain.HasDocExtension(p) {
			t.Errorf("HasDocExtension(%q) = true, want false", p)
		}
	}
}

func TestIntentGolden_Tickets(t *testing.T) {
	var g struct {
		Tickets []struct {
			Name  string     `json:"name"`
			Repo  goldenRepo `json:"repo"`
			Texts []string   `json:"texts"`
			Refs  []struct {
				Number  float64 `json:"number"`
				Ref     string  `json:"ref"`
				Closing bool    `json:"closing"`
			} `json:"refs"`
			Skipped []goldenSkipped `json:"skipped"`
		} `json:"tickets"`
	}
	loadGolden(t, "intent", &g)
	if len(g.Tickets) == 0 {
		t.Fatal("intent.json has no ticket cases")
	}
	for _, tc := range g.Tickets {
		t.Run(tc.Name, func(t *testing.T) {
			got := domain.ExtractTickets(tc.Texts, tc.Repo.domain())
			type ref struct {
				Number  float64
				Ref     string
				Closing bool
			}
			var gotRefs, wantRefs []ref
			for _, r := range got.Refs {
				gotRefs = append(gotRefs, ref{r.Number, r.Ref(), r.Closing})
			}
			for _, r := range tc.Refs {
				wantRefs = append(wantRefs, ref{r.Number, r.Ref, r.Closing})
			}
			if diff := cmp.Diff(wantRefs, gotRefs); diff != "" {
				t.Errorf("ExtractTickets(%q) refs mismatch (-want +got):\n%s", tc.Texts, diff)
			}
			if diff := cmp.Diff(skippedOf(tc.Skipped), got.Skipped); diff != "" {
				t.Errorf("ExtractTickets(%q) skipped mismatch (-want +got):\n%s", tc.Texts, diff)
			}
		})
	}
}

func TestIntentGolden_Docs(t *testing.T) {
	var g struct {
		Docs []struct {
			Name    string          `json:"name"`
			Repo    goldenRepo      `json:"repo"`
			Texts   []string        `json:"texts"`
			Refs    []string        `json:"refs"`
			Skipped []goldenSkipped `json:"skipped"`
			Error   string          `json:"error"`
		} `json:"docs"`
	}
	loadGolden(t, "intent", &g)
	if len(g.Docs) == 0 {
		t.Fatal("intent.json has no doc cases")
	}
	var failures int
	for _, tc := range g.Docs {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := domain.ExtractDocs(tc.Texts, tc.Repo.domain())
			if tc.Error != "" {
				failures++
				if err == nil || err.Error() != tc.Error {
					t.Errorf("ExtractDocs(%q) error = %v, want %q", tc.Texts, err, tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractDocs(%q) error = %v, want none", tc.Texts, err)
			}
			gotRefs := []string{}
			for _, r := range got.Refs {
				gotRefs = append(gotRefs, r.Path)
			}
			if diff := cmp.Diff(tc.Refs, gotRefs); diff != "" {
				t.Errorf("ExtractDocs(%q) refs mismatch (-want +got):\n%s", tc.Texts, diff)
			}
			if diff := cmp.Diff(skippedOf(tc.Skipped), got.Skipped); diff != "" {
				t.Errorf("ExtractDocs(%q) skipped mismatch (-want +got):\n%s", tc.Texts, diff)
			}
		})
	}
	if failures == 0 {
		t.Error("the doc corpus holds no malformed-escape (URI malformed) case")
	}
}

func TestIntentGolden_Keys(t *testing.T) {
	var g struct {
		Keys []struct {
			Name string `json:"name"`
			Args struct {
				Provider   string    `json:"provider"`
				Model      string    `json:"model"`
				Title      string    `json:"title"`
				Body       *text     `json:"body"`
				Branch     string    `json:"branch"`
				HeadSHA    string    `json:"headSha"`
				TicketRefs []float64 `json:"ticketRefs"`
				DocPaths   []string  `json:"docPaths"`
			} `json:"args"`
			Canonical text   `json:"canonical"`
			Hash      string `json:"input_hash"`
		} `json:"keys"`
	}
	loadGolden(t, "intent", &g)
	var corpus struct {
		Cases []struct {
			Name string `json:"name"`
			Body any    `json:"body"`
		} `json:"cases"`
	}
	loadCorpus(t, "intent", "keys", &corpus)
	bodies := map[string]any{}
	for _, c := range corpus.Cases {
		bodies[c.Name] = c.Body
	}
	if len(g.Keys) == 0 {
		t.Fatal("intent.json has no key cases")
	}
	for _, tc := range g.Keys {
		t.Run(tc.Name, func(t *testing.T) {
			var body string
			switch {
			case tc.Args.Body == nil:
			case tc.Args.Body.b != nil: // a long body is rebuilt from the corpus and checked against its digest
				body = expandStrings(t, bodies[tc.Name])
				checkText(t, "the rebuilt body", *tc.Args.Body, body)
			default:
				body = tc.Args.Body.s
			}
			k := domain.KeyInput{
				Body: body, Branch: tc.Args.Branch, HeadSHA: tc.Args.HeadSHA, Model: tc.Args.Model,
				PromptVersion: domain.PromptVersion, Provider: tc.Args.Provider, Title: tc.Args.Title,
				DocPaths: tc.Args.DocPaths, TicketRefs: tc.Args.TicketRefs,
			}
			canon, err := k.CanonicalJSON()
			if err != nil {
				t.Fatalf("CanonicalJSON() error = %v", err)
			}
			checkText(t, "CanonicalJSON()", tc.Canonical, canon)
			if hash, err := k.Hash(); err != nil || hash != tc.Hash {
				t.Errorf("Hash() = %q, %v, want %q", hash, err, tc.Hash)
			}
		})
	}
}

// NaN or Infinity cannot be in a golden (JSON), so the key golden holds finite numbers only; this
// pins how the Go key writes the others (JSON.stringify prints null).
func TestIntentGolden_NonFiniteTicketNumbersAreNull(t *testing.T) {
	k := domain.KeyInput{PromptVersion: domain.PromptVersion, TicketRefs: []float64{math.Inf(1), 3}, DocPaths: []string{}}
	canon, err := k.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	const want = `{"body":"","branch":"","doc_paths":[],"head_sha":"","model":"","prompt_version":2,"provider":"","ticket_refs":[3,null],"title":""}`
	if canon != want {
		t.Errorf("CanonicalJSON() = %s, want %s", canon, want)
	}
}

func TestIntentGolden_TextRules(t *testing.T) {
	var g struct {
		Text struct {
			Substantive []struct {
				Name        string  `json:"name"`
				Body        *string `json:"body"`
				Stripped    *string `json:"stripped"`
				Substantive bool    `json:"substantive"`
			} `json:"substantive"`
			Truncate []struct {
				Name   string `json:"name"`
				Text   string `json:"text"`
				Max    int    `json:"max"`
				Result struct {
					Text      string `json:"text"`
					Truncated bool   `json:"truncated"`
				} `json:"result"`
			} `json:"truncate"`
			ClampIntent []struct {
				Name   string `json:"name"`
				Text   string `json:"text"`
				Result string `json:"result"`
			} `json:"clampIntent"`
			ClampScope []struct {
				Name   string   `json:"name"`
				Items  []string `json:"items"`
				Result []string `json:"result"`
			} `json:"clampScope"`
			Confidence []struct {
				DerivedFrom string `json:"derived_from"`
				Degraded    bool   `json:"degraded"`
				Result      string `json:"result"`
			} `json:"confidence"`
		} `json:"text"`
	}
	loadGolden(t, "intent", &g)
	x := g.Text

	t.Run("substantive", func(t *testing.T) {
		for _, tc := range x.Substantive {
			body := ""
			if tc.Body != nil {
				body = *tc.Body
			}
			if tc.Stripped != nil {
				if got := domain.StripBodyBoilerplate(body); got != *tc.Stripped {
					t.Errorf("%s: StripBodyBoilerplate(%q) = %q, want %q", tc.Name, body, got, *tc.Stripped)
				}
			}
			if got := domain.IsSubstantiveBody(body); got != tc.Substantive {
				t.Errorf("%s: IsSubstantiveBody(%q) = %v, want %v", tc.Name, body, got, tc.Substantive)
			}
		}
	})
	t.Run("truncate", func(t *testing.T) {
		for _, tc := range x.Truncate {
			got, cut := reviewer.CutTruncated(tc.Text, tc.Max)
			if got != tc.Result.Text || cut != tc.Result.Truncated {
				t.Errorf("%s: CutTruncated(%q, %d) = %q, %v, want %q, %v", tc.Name, tc.Text, tc.Max, got, cut, tc.Result.Text, tc.Result.Truncated)
			}
		}
	})
	t.Run("clampIntent", func(t *testing.T) {
		for _, tc := range x.ClampIntent {
			if got := domain.ClampIntentText(tc.Text); got != tc.Result {
				t.Errorf("%s: ClampIntentText(%q) = %q, want %q", tc.Name, tc.Text, got, tc.Result)
			}
		}
	})
	t.Run("clampScope", func(t *testing.T) {
		for _, tc := range x.ClampScope {
			want := tc.Result
			if want == nil {
				want = []string{}
			}
			if diff := cmp.Diff(want, domain.ClampScopeList(tc.Items)); diff != "" {
				t.Errorf("%s: ClampScopeList(%q) mismatch (-want +got):\n%s", tc.Name, tc.Items, diff)
			}
		}
	})
	t.Run("confidence", func(t *testing.T) {
		for _, tc := range x.Confidence {
			// "degraded" is a ticket whose read failed.
			var sources []domain.Source
			if tc.Degraded {
				sources = []domain.Source{domain.FailedSource(domain.KindTicket, "1", "boom")}
			}
			if got := domain.ConfidenceFor(tc.DerivedFrom, sources); got != tc.Result {
				t.Errorf("ConfidenceFor(%q, degraded=%v) = %q, want %q", tc.DerivedFrom, tc.Degraded, got, tc.Result)
			}
		}
	})
}

func TestIntentGolden_SourcesBlockMessageAndRender(t *testing.T) {
	var g struct {
		Text struct {
			RenderSources []struct {
				Name    string `json:"name"`
				Sources []struct {
					Kind    string  `json:"kind"`
					Ref     string  `json:"ref"`
					Status  string  `json:"status"`
					Content *string `json:"content"`
					Detail  *string `json:"detail"`
				} `json:"sources"`
				Block string `json:"block"`
			} `json:"renderSources"`
			UserMessage []struct {
				Name    string `json:"name"`
				Block   string `json:"block"`
				Message string `json:"message"`
			} `json:"userMessage"`
			RenderIntent []struct {
				Name   string `json:"name"`
				Intent struct {
					Intent      string   `json:"intent"`
					InScope     []string `json:"in_scope"`
					OutOfScope  []string `json:"out_of_scope"`
					ChangeType  *string  `json:"change_type"`
					Confidence  *string  `json:"confidence"`
					DerivedFrom string   `json:"derived_from"`
				} `json:"intent"`
				Text string `json:"text"`
			} `json:"renderIntent"`
		} `json:"text"`
	}
	loadGolden(t, "intent", &g)

	for _, tc := range g.Text.RenderSources {
		t.Run("block/"+tc.Name, func(t *testing.T) {
			var sources []domain.Source
			for _, s := range tc.Sources {
				src := domain.Source{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
				if s.Content != nil {
					src.Content = *s.Content
				}
				sources = append(sources, src)
			}
			if got := domain.RenderSourcesBlock(sources); got != tc.Block {
				t.Errorf("RenderSourcesBlock() =\n%q\nwant\n%q", got, tc.Block)
			}
		})
	}
	for _, tc := range g.Text.UserMessage {
		if got := domain.UserMessage(tc.Block); got != tc.Message {
			t.Errorf("%s: UserMessage(%q) = %q, want %q", tc.Name, tc.Block, got, tc.Message)
		}
	}
	for _, tc := range g.Text.RenderIntent {
		in := reviewer.Intent{
			Text: tc.Intent.Intent, InScope: tc.Intent.InScope, OutOfScope: tc.Intent.OutOfScope,
			ChangeType: tc.Intent.ChangeType, Confidence: tc.Intent.Confidence, DerivedFrom: tc.Intent.DerivedFrom,
		}
		if got := reviewer.RenderIntent(in); got != tc.Text {
			t.Errorf("%s: RenderIntent() =\n%q\nwant\n%q", tc.Name, got, tc.Text)
		}
	}
}
