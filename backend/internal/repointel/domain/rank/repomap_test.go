package rank_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/rank"
)

// wordCounter counts whitespace-separated words: deterministic and easy to reason about.
type wordCounter struct{}

func (wordCounter) Count(text string) int { return len(strings.Fields(text)) }

// header is 10 words: "#", "Repo", "skeleton", "(top-ranked", "by", "import", "graph", "only,", "partial", "view)".
const header = "# Repo skeleton (top-ranked by import graph only, partial view)\n"

func cand(path, sig string) domain.MapCandidate {
	return domain.MapCandidate{Path: path, Name: "n", Signature: sig}
}

func TestRenderMap_NoItemsIsTheHeader(t *testing.T) {
	tests := []struct {
		name       string
		candidates []domain.MapCandidate
	}{
		{name: "empty", candidates: nil},
		{name: "only empty signatures", candidates: []domain.MapCandidate{cand("a.ts", ""), cand("b.ts", "")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, tokens := rank.RenderMap(tc.candidates, wordCounter{}, 1500)
			if text != header || tokens != 10 {
				t.Errorf("RenderMap(%s) = %q, %d, want %q, 10", tc.name, text, tokens, header)
			}
		})
	}
}

func TestRenderMap_LayoutGroupsByFileInFirstAppearanceOrder(t *testing.T) {
	candidates := []domain.MapCandidate{
		cand("b.ts", "function b1()"),
		cand("a.ts", "function a1()"),
		cand("b.ts", "function b2()"),
	}
	text, tokens := rank.RenderMap(candidates, wordCounter{}, 1500)
	want := header + "b.ts:\n  function b1()\n  function b2()\na.ts:\n  function a1()\n"
	if text != want {
		t.Errorf("RenderMap layout =\n%q\nwant\n%q", text, want)
	}
	if got := (wordCounter{}).Count(text); tokens != got {
		t.Errorf("RenderMap tokens = %d, want %d", tokens, got)
	}
}

func TestRenderMap_SkipsEmptyAndRepeatedPathSignaturePairs(t *testing.T) {
	candidates := []domain.MapCandidate{
		cand("a.ts", "class A"),
		cand("a.ts", "run(): void"), // A.run
		cand("a.ts", "run(): void"), // dual-emitted bare run
		cand("a.ts", ""),
		cand("b.ts", "run(): void"), // same signature in another file stays
	}
	text, _ := rank.RenderMap(candidates, wordCounter{}, 1500)
	want := header + "a.ts:\n  class A\n  run(): void\nb.ts:\n  run(): void\n"
	if text != want {
		t.Errorf("RenderMap(dual emit) =\n%q\nwant\n%q", text, want)
	}
}

func TestRenderMap_BudgetKeepsLargestFittingPrefix(t *testing.T) {
	// Each item adds 2 words ("sig", "N") plus a file line ("x.ts:") once per file.
	candidates := []domain.MapCandidate{
		cand("x.ts", "sig 1"),
		cand("x.ts", "sig 2"),
		cand("y.ts", "sig 3"),
		cand("y.ts", "sig 4"),
	}
	tests := []struct {
		budget     int
		wantTokens int
		wantTail   string
	}{
		{budget: 1500, wantTokens: 10 + 1 + 4 + 1 + 4, wantTail: "x.ts:\n  sig 1\n  sig 2\ny.ts:\n  sig 3\n  sig 4\n"},
		{budget: 10 + 1 + 4 + 1 + 2, wantTokens: 10 + 1 + 4 + 1 + 2, wantTail: "x.ts:\n  sig 1\n  sig 2\ny.ts:\n  sig 3\n"},
		{budget: 10 + 1 + 4, wantTokens: 10 + 1 + 4, wantTail: "x.ts:\n  sig 1\n  sig 2\n"},
		{budget: 10 + 1 + 3, wantTokens: 10 + 1 + 2, wantTail: "x.ts:\n  sig 1\n"}, // cuts mid-file
		{budget: 10, wantTokens: 10, wantTail: ""},
		{budget: 3, wantTokens: 10, wantTail: ""}, // not even the header fits: the header anyway
	}
	for _, tc := range tests {
		text, tokens := rank.RenderMap(candidates, wordCounter{}, tc.budget)
		if want := header + tc.wantTail; text != want || tokens != tc.wantTokens {
			t.Errorf("RenderMap(budget %d) = %q, %d, want %q, %d", tc.budget, text, tokens, want, tc.wantTokens)
		}
	}
}

func TestRenderMap_ReturnsTokenCountOfBestRender(t *testing.T) {
	// A counter that is not monotonic in item count would still return the count of the text it
	// picked; here the counter charges per byte.
	counter := byteCounter{}
	candidates := []domain.MapCandidate{cand("a.ts", "function a()"), cand("a.ts", "function b()")}
	text, tokens := rank.RenderMap(candidates, counter, len(header)+len("a.ts:\n  function a()\n"))
	if want := header + "a.ts:\n  function a()\n"; text != want || tokens != len(want) {
		t.Errorf("RenderMap(byte budget) = %q, %d, want %q, %d", text, tokens, want, len(want))
	}
}

type byteCounter struct{}

func (byteCounter) Count(text string) int { return len(text) }
