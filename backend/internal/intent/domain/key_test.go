package domain_test

import (
	"math"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/intent/domain"
)

func baseKey() domain.KeyInput {
	return domain.KeyInput{
		Body: "", Branch: "feat/x", HeadSHA: "abc1234", Model: "deepseek/deepseek-v4-flash",
		PromptVersion: domain.PromptVersion, Provider: "openrouter", Title: "Add <limiter> & more",
		DocPaths: []string{"docs/b.md", "docs/a.md"}, TicketRefs: []float64{12, 3},
	}
}

func TestKeyInput_CanonicalJSONAndHash(t *testing.T) {
	// The hash is sha256sum of exactly this text.
	const wantCanonical = `{"body":"","branch":"feat/x","doc_paths":["docs/a.md","docs/b.md"],"head_sha":"abc1234",` +
		`"model":"deepseek/deepseek-v4-flash","prompt_version":2,"provider":"openrouter","ticket_refs":[3,12],"title":"Add <limiter> & more"}`
	const wantHash = "6a0983956d7bd79d6fdf3a165f367ea3128771bbffe0fad470967b9629b34cfd"
	k := baseKey()
	canon, err := k.CanonicalJSON()
	if err != nil || canon != wantCanonical {
		t.Errorf("CanonicalJSON() = %q, %v, want %q", canon, err, wantCanonical)
	}
	if hash, err := k.Hash(); err != nil || hash != wantHash {
		t.Errorf("Hash() = %q, %v, want %q", hash, err, wantHash)
	}
	if k.DocPaths[0] != "docs/b.md" || k.TicketRefs[0] != 12 {
		t.Errorf("CanonicalJSON sorted the caller's slices: %v %v", k.DocPaths, k.TicketRefs)
	}
}

func TestKeyInput_HashIgnoresTheCallersOrder(t *testing.T) {
	a, b := baseKey(), baseKey()
	b.DocPaths, b.TicketRefs = []string{"docs/a.md", "docs/b.md"}, []float64{3, 12}
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha != hb {
		t.Errorf("Hash() depends on the order of refs: %s != %s", ha, hb)
	}
}

func TestKeyInput_EveryFieldChangesTheHash(t *testing.T) {
	base, _ := baseKey().Hash()
	mutations := map[string]func(*domain.KeyInput){
		"body":           func(k *domain.KeyInput) { k.Body = "x" },
		"branch":         func(k *domain.KeyInput) { k.Branch = "x" },
		"head_sha":       func(k *domain.KeyInput) { k.HeadSHA = "x" },
		"model":          func(k *domain.KeyInput) { k.Model = "x" },
		"prompt_version": func(k *domain.KeyInput) { k.PromptVersion++ },
		"provider":       func(k *domain.KeyInput) { k.Provider = "x" },
		"title":          func(k *domain.KeyInput) { k.Title = "x" },
		"doc_paths":      func(k *domain.KeyInput) { k.DocPaths = append(k.DocPaths, "docs/c.md") },
		"ticket_refs":    func(k *domain.KeyInput) { k.TicketRefs = append(k.TicketRefs, 99) },
	}
	for name, mutate := range mutations {
		k := baseKey()
		mutate(&k)
		if h, _ := k.Hash(); h == base {
			t.Errorf("changing %s leaves the hash unchanged", name)
		}
	}
}

// JSON.stringify never escapes U+2028/U+2029 or HTML characters and writes Infinity as null.
func TestKeyInput_CanonicalJSONFollowsJSONStringify(t *testing.T) {
	ls, ps := string(rune(0x2028)), string(rune(0x2029))
	k := baseKey()
	k.Title = "a" + ls + "b" + ps + "c</script>\u0001\u007f"
	k.TicketRefs = []float64{math.Inf(1), 5}
	got, err := k.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	for _, want := range []string{`"title":"a` + ls + `b` + ps + `c</script>\u0001` + "\u007f" + `"`, `"ticket_refs":[5,null]`} {
		if !strings.Contains(got, want) {
			t.Errorf("CanonicalJSON() = %s, want it to contain %s", got, want)
		}
	}
}

func TestKeyInput_DocPathsSortByCodeUnit(t *testing.T) {
	// U+FF5E sorts after the supplementary character in UTF-16 order, before it in UTF-8 order.
	k := baseKey()
	k.DocPaths = []string{"\uff5e.md", "😀.md"}
	got, _ := k.CanonicalJSON()
	if want := "\"doc_paths\":[\"😀.md\",\"\uff5e.md\"]"; !strings.Contains(got, want) {
		t.Errorf("CanonicalJSON() = %s, want doc_paths in code unit order", got)
	}
}

func TestNewKeyInput(t *testing.T) {
	pull := domain.Pull{Title: "t", Body: "b", Branch: "br", HeadSHA: "sha"}
	k := domain.NewKeyInput("openrouter", "m", pull, []domain.TicketRef{{Number: 4}, {Number: 2}}, []domain.DocRef{{Path: "docs/a.md"}})
	want := domain.KeyInput{
		Body: "b", Branch: "br", HeadSHA: "sha", Model: "m", PromptVersion: 2, Provider: "openrouter", Title: "t",
		DocPaths: []string{"docs/a.md"}, TicketRefs: []float64{4, 2},
	}
	gh, _ := k.Hash()
	wh, _ := want.Hash()
	if gh != wh {
		t.Errorf("NewKeyInput(...).Hash() = %s, want %s", gh, wh)
	}
}

func TestCorrelationID(t *testing.T) {
	if got, want := domain.CorrelationID("pr-1", "0123456789abcdef"), "intent:pr-1:0123456789ab"; got != want {
		t.Errorf("CorrelationID() = %q, want %q", got, want)
	}
	if got, want := domain.CorrelationID("pr-1", "abc"), "intent:pr-1:abc"; got != want {
		t.Errorf("CorrelationID(short hash) = %q, want %q", got, want)
	}
}
