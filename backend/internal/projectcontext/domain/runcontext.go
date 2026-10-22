package domain

import "fmt"

// Limits of the documents a review run receives.
const (
	// RunBudgetTokens is the token budget of all included documents of a run.
	RunBudgetTokens = 16_000
	// RunReadConcurrency is the number of documents read at once.
	RunReadConcurrency = 8
)

// Kinds of RunOrigin.
const (
	OriginAgent = "agent"
	OriginSkill = "skill"
)

// Sources of a run document.
const (
	RunSourceRepo  = "repo"
	RunSourceStore = "store"
)

// RunStatus is the outcome for one document of a run.
type RunStatus string

// The statuses of a run document.
const (
	RunIncluded   RunStatus = "included"
	RunMissing    RunStatus = "missing"
	RunTooLarge   RunStatus = "too_large"
	RunOverBudget RunStatus = "over_budget"
	RunUnreadable RunStatus = "unreadable"
)

// RunOrigin says whose attachment put a document into the run: the agent's or a skill's.
type RunOrigin struct {
	Kind string // OriginAgent or OriginSkill
	// SkillID and SkillName are set when Kind is OriginSkill.
	SkillID, SkillName string
}

// RunDocRef is a document of a run before it is read.
type RunDocRef struct {
	Path   string
	Origin RunOrigin
}

// SkillPaths are the paths one skill attaches in the run's repo.
type SkillPaths struct {
	SkillID, SkillName string
	Paths              []string
}

// RunDoc is one document of a run.
type RunDoc struct {
	Path    string
	DocType DocType
	Origin  RunOrigin
	// Tokens is the estimate of Text; 0 unless the document was read.
	Tokens int
	Status RunStatus
	// Source is RunSourceRepo or RunSourceStore.
	Source string
	// Text is set for included documents only.
	Text *string
}

// BuildRunDocList lists the agent's paths in order, then each skill's in the given order. A
// path already listed is not added again: its origin stays the first one.
func BuildRunDocList(agentPaths []string, skills []SkillPaths) []RunDocRef {
	seen := make(map[string]struct{}, len(agentPaths))
	var out []RunDocRef
	add := func(path string, origin RunOrigin) {
		if _, dup := seen[path]; dup {
			return
		}
		seen[path] = struct{}{}
		out = append(out, RunDocRef{Path: path, Origin: origin})
	}
	for _, p := range agentPaths {
		add(p, RunOrigin{Kind: OriginAgent})
	}
	for _, s := range skills {
		for _, p := range s.Paths {
			add(p, RunOrigin{Kind: OriginSkill, SkillID: s.SkillID, SkillName: s.SkillName})
		}
	}
	return out
}

// RunTextTokens is the token estimate of a document text: ceil(UTF-16 code units / 4).
func RunTextTokens(text string) int { return Tokens(UTF16Units([]byte(text))) }

// NewRunDoc is an included document: its tokens are estimated from text.
func NewRunDoc(ref RunDocRef, source, text string) RunDoc {
	return RunDoc{
		Path: ref.Path, DocType: DocTypeOf(ref.Path), Origin: ref.Origin, Source: source,
		Tokens: RunTextTokens(text), Status: RunIncluded, Text: &text,
	}
}

// SkippedRunDoc is a document that was not read, with its status.
func SkippedRunDoc(ref RunDocRef, source string, status RunStatus) RunDoc {
	return RunDoc{
		Path: ref.Path, DocType: DocTypeOf(ref.Path), Origin: ref.Origin, Source: source,
		Status: status,
	}
}

// ApplyBudget applies the token budget in order: the first included document that would pass
// it becomes over_budget, and so does every document after it. Earlier documents stay as they
// are. Text is dropped from the cut documents. It returns the documents and the tokens of the
// included ones. docs is not modified.
func ApplyBudget(docs []RunDoc, budget int) ([]RunDoc, int) {
	out := make([]RunDoc, 0, len(docs))
	total := 0
	cut := false
	for _, d := range docs {
		if !cut && d.Status == RunIncluded && total+d.Tokens > budget {
			cut = true
		}
		if cut {
			d.Status, d.Text = RunOverBudget, nil
			out = append(out, d)
			continue
		}
		if d.Status == RunIncluded {
			total += d.Tokens
		}
		out = append(out, d)
	}
	return out, total
}

// RunLogLine is the run's single log line: counts and tokens, never document text.
func RunLogLine(docs []RunDoc, tokensTotal int) string {
	included := 0
	for _, d := range docs {
		if d.Status == RunIncluded {
			included++
		}
	}
	return fmt.Sprintf("project context: %d included, %d skipped · ~%d tokens", included, len(docs)-included, tokensTotal)
}
