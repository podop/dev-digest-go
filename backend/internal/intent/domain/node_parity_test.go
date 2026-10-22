package domain_test

// testdata/node_cases.json holds reference values computed by the Node implementation of
// dev-digest (commit d1c9093, modules/intent/domain; generator testdata/node_gen.mts) on seeded
// random inputs full of awkward text: surrogate pairs, NBSP, BOM, U+2028, CR, bad percent
// escapes, JavaScript-only regex semantics. The Go port must give identical results.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

type nodeRepo struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type nodeSkipped struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

type nodeCases struct {
	SystemPrompt string `json:"system_prompt"`
	TaskText     string `json:"task_text"`
	Tickets      []struct {
		Repo  nodeRepo `json:"repo"`
		Texts []string `json:"texts"`
		Refs  []struct {
			Ref     string `json:"ref"`
			Closing bool   `json:"closing"`
		} `json:"refs"`
		Skipped []nodeSkipped `json:"skipped"`
	} `json:"tickets"`
	Docs []struct {
		Repo    nodeRepo      `json:"repo"`
		Texts   []string      `json:"texts"`
		Refs    []string      `json:"refs"`
		Skipped []nodeSkipped `json:"skipped"`
		Error   string        `json:"error"`
	} `json:"docs"`
	Keys []struct {
		Provider   string   `json:"provider"`
		Model      string   `json:"model"`
		Title      string   `json:"title"`
		Body       *string  `json:"body"`
		Branch     string   `json:"branch"`
		HeadSHA    string   `json:"head_sha"`
		TicketRefs []string `json:"ticket_refs"`
		DocPaths   []string `json:"doc_paths"`
		Canonical  string   `json:"canonical"`
		Hash       string   `json:"hash"`
	} `json:"keys"`
	Bodies []struct {
		Body        string `json:"body"`
		Stripped    string `json:"stripped"`
		Substantive bool   `json:"substantive"`
	} `json:"bodies"`
	ClampText []struct {
		Text   string `json:"text"`
		Intent string `json:"intent"`
	} `json:"clampText"`
	ClampLists []struct {
		Items []string `json:"items"`
		List  []string `json:"list"`
	} `json:"clampLists"`
	Blocks []struct {
		Sources []struct {
			Kind    string  `json:"kind"`
			Ref     string  `json:"ref"`
			Status  string  `json:"status"`
			Content *string `json:"content"`
		} `json:"sources"`
		Block   string `json:"block"`
		Message string `json:"message"`
	} `json:"blocks"`
}

func loadCases(t *testing.T) *nodeCases {
	t.Helper()
	raw, err := os.ReadFile("testdata/node_cases.json")
	if err != nil {
		t.Fatalf("read Node cases: %v", err)
	}
	var c nodeCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("decode Node cases: %v", err)
	}
	return &c
}

func skippedOf(in []nodeSkipped) []domain.Skipped {
	out := []domain.Skipped{}
	for _, s := range in {
		out = append(out, domain.Skipped{Ref: s.Ref, Reason: s.Reason})
	}
	return out
}

// parseNumber is JavaScript's Number("<digits>").
func parseNumber(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		t.Fatalf("ParseFloat(%q): %v", s, err)
	}
	return f
}

func TestNodeParity_Tickets(t *testing.T) {
	for i, tc := range loadCases(t).Tickets {
		got := domain.ExtractTickets(tc.Texts, domain.Repo{Owner: tc.Repo.Owner, Name: tc.Repo.Name})
		type ref struct {
			Ref     string
			Closing bool
		}
		var gotRefs, wantRefs []ref
		for _, r := range got.Refs {
			gotRefs = append(gotRefs, ref{r.Ref(), r.Closing})
		}
		for _, r := range tc.Refs {
			wantRefs = append(wantRefs, ref{r.Ref, r.Closing})
		}
		if diff := cmp.Diff(wantRefs, gotRefs); diff != "" {
			t.Errorf("case %d ExtractTickets(%q, %v) refs mismatch (-want +got):\n%s", i, tc.Texts, tc.Repo, diff)
		}
		if diff := cmp.Diff(skippedOf(tc.Skipped), got.Skipped); diff != "" {
			t.Errorf("case %d ExtractTickets(%q, %v) skipped mismatch (-want +got):\n%s", i, tc.Texts, tc.Repo, diff)
		}
	}
}

func TestNodeParity_Docs(t *testing.T) {
	var errors int
	for i, tc := range loadCases(t).Docs {
		got, err := domain.ExtractDocs(tc.Texts, domain.Repo{Owner: tc.Repo.Owner, Name: tc.Repo.Name})
		if tc.Error != "" {
			errors++
			if err == nil || err.Error() != tc.Error {
				t.Errorf("case %d ExtractDocs(%q) error = %v, want %q", i, tc.Texts, err, tc.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("case %d ExtractDocs(%q) error = %v, want none", i, tc.Texts, err)
			continue
		}
		gotPaths := []string{}
		for _, r := range got.Refs {
			gotPaths = append(gotPaths, r.Path)
		}
		if diff := cmp.Diff(tc.Refs, gotPaths); diff != "" {
			t.Errorf("case %d ExtractDocs(%q) refs mismatch (-want +got):\n%s", i, tc.Texts, diff)
		}
		if diff := cmp.Diff(skippedOf(tc.Skipped), got.Skipped); diff != "" {
			t.Errorf("case %d ExtractDocs(%q) skipped mismatch (-want +got):\n%s", i, tc.Texts, diff)
		}
	}
	if errors == 0 {
		t.Errorf("the corpus holds no malformed-escape case")
	}
}

func TestNodeParity_Key(t *testing.T) {
	var infinite int
	for i, tc := range loadCases(t).Keys {
		body := ""
		if tc.Body != nil {
			body = *tc.Body
		}
		k := domain.KeyInput{
			Body: body, Branch: tc.Branch, HeadSHA: tc.HeadSHA, Model: tc.Model,
			PromptVersion: domain.PromptVersion, Provider: tc.Provider, Title: tc.Title,
			DocPaths: tc.DocPaths, TicketRefs: []float64{},
		}
		for _, r := range tc.TicketRefs {
			n := parseNumber(t, r)
			if math.IsInf(n, 0) {
				infinite++
			}
			k.TicketRefs = append(k.TicketRefs, n)
		}
		canon, err := k.CanonicalJSON()
		if err != nil {
			t.Fatalf("case %d CanonicalJSON: %v", i, err)
		}
		if canon != tc.Canonical {
			t.Errorf("case %d CanonicalJSON() =\n%s\nwant\n%s", i, canon, tc.Canonical)
		}
		hash, err := k.Hash()
		if err != nil || hash != tc.Hash {
			t.Errorf("case %d Hash() = %q, %v, want %q", i, hash, err, tc.Hash)
		}
		sum := sha256.Sum256([]byte(tc.Canonical))
		if hex.EncodeToString(sum[:]) != tc.Hash {
			t.Errorf("case %d: the Node hash is not the sha256 of the Node canonical JSON", i)
		}
	}
	if infinite == 0 {
		t.Errorf("the corpus holds no Infinity ticket number")
	}
}

func TestNodeParity_SubstantiveBody(t *testing.T) {
	for i, tc := range loadCases(t).Bodies {
		if got := domain.StripBodyBoilerplate(tc.Body); got != tc.Stripped {
			t.Errorf("case %d StripBodyBoilerplate(%q) = %q, want %q", i, tc.Body, got, tc.Stripped)
		}
		if got := domain.IsSubstantiveBody(tc.Body); got != tc.Substantive {
			t.Errorf("case %d IsSubstantiveBody(%q) = %v, want %v", i, tc.Body, got, tc.Substantive)
		}
	}
}

func TestNodeParity_Clamps(t *testing.T) {
	c := loadCases(t)
	for i, tc := range c.ClampText {
		if got := domain.ClampIntentText(tc.Text); got != tc.Intent {
			t.Errorf("case %d ClampIntentText(%q) = %q, want %q", i, tc.Text, got, tc.Intent)
		}
	}
	for i, tc := range c.ClampLists {
		want := tc.List
		if want == nil {
			want = []string{}
		}
		if diff := cmp.Diff(want, domain.ClampScopeList(tc.Items)); diff != "" {
			t.Errorf("case %d ClampScopeList(%q) mismatch (-want +got):\n%s", i, tc.Items, diff)
		}
	}
}

func TestNodeParity_SourcesBlockAndMessages(t *testing.T) {
	c := loadCases(t)
	if domain.SystemPrompt != c.SystemPrompt {
		t.Errorf("SystemPrompt differs from Node CLASSIFICATION_SYSTEM_PROMPT:\n got %q\nwant %q", domain.SystemPrompt, c.SystemPrompt)
	}
	if got := domain.TaskText(); got != c.TaskText {
		t.Errorf("TaskText() = %q, want %q", got, c.TaskText)
	}
	for i, tc := range c.Blocks {
		var sources []domain.Source
		for _, s := range tc.Sources {
			src := domain.Source{Kind: s.Kind, Ref: s.Ref, Status: s.Status}
			if s.Content != nil {
				src.Content = *s.Content
			}
			sources = append(sources, src)
		}
		block := domain.RenderSourcesBlock(sources)
		if block != tc.Block {
			t.Errorf("case %d RenderSourcesBlock(%+v) =\n%q\nwant\n%q", i, tc.Sources, block, tc.Block)
		}
		if got := domain.UserMessage(block); got != tc.Message {
			t.Errorf("case %d UserMessage() = %q, want %q", i, got, tc.Message)
		}
	}
}

// The reference fixtures really exercise the awkward inputs (a generator regression would
// otherwise turn the parity tests into no-ops).
func TestNodeParity_CorpusIsRepresentative(t *testing.T) {
	c := loadCases(t)
	var closing, skipped, capped int
	for _, tc := range c.Tickets {
		for _, r := range tc.Refs {
			if r.Closing {
				closing++
			}
		}
		if len(tc.Skipped) > 0 {
			skipped++
		}
		if len(tc.Refs) == domain.TicketMax {
			capped++
		}
	}
	if closing < 20 || skipped < 20 || capped < 20 {
		t.Errorf("ticket corpus: closing=%d skipped=%d capped=%d, want at least 20 each", closing, skipped, capped)
	}
	var docCapped, unsafe int
	for _, tc := range c.Docs {
		if len(tc.Refs) == domain.DocMax {
			docCapped++
		}
		for _, s := range tc.Skipped {
			if s.Reason == "unsafe path" {
				unsafe++
			}
		}
	}
	if docCapped < 20 || unsafe < 20 {
		t.Errorf("doc corpus: capped=%d unsafe=%d, want at least 20 each", docCapped, unsafe)
	}
	if reviewer.Len16(c.SystemPrompt) == 0 {
		t.Errorf("empty system prompt in the corpus")
	}
}
