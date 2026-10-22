// Package domain is the vocabulary of the review engine (Phase 4): the parsed diff, the chunks
// the engine reviews, the findings and reviews a model returns, the prompt assembly and the
// outcome of a run. It imports only the standard library and carries no JSON tags: the JSON
// shapes of the LLM answer, the run trace and the HTTP responses live in the adapters and in
// transport.
//
// Pure rules (diff parsing, slicing, splitting, reducing, grounding, scoring, prompt assembly)
// are added next to this file by the engine steps; this file fixes only the shared types and the
// constants of spec section 7.1.
//
// Lengths are measured in UTF-16 code units, as JavaScript's string length does (spec 7.1); a
// token estimate is ceil(units / 4).
package domain

// Constants of the engine (spec section 7.1).
const (
	// MapThresholdLines is the added+deleted line count above which the auto strategy goes
	// map-reduce (when the diff has more than one file).
	MapThresholdLines = 400
	// MaxChunkUnits caps one chunk's diff text in UTF-16 units; larger chunks are split at hunk
	// boundaries and an oversize hunk is truncated.
	MaxChunkUnits = 200_000
	// MaxPRDescriptionUnits caps the PR description in the prompt, in UTF-16 units.
	MaxPRDescriptionUnits = 4_000
	// DefaultMapConcurrency is the number of chunk calls in flight when Options.MapConcurrency is 0.
	DefaultMapConcurrency = 3
	// Reprompts is the number of structured-output re-prompts per call.
	Reprompts = 2
	// Temperature is the sampling temperature of a review call (subject to the reasoning-model
	// rules of the LLM layer).
	Temperature = 0.0
)

// Strategy is an agent's review strategy.
type Strategy string

// The strategies an agent can have.
const (
	StrategyAuto       Strategy = "auto"
	StrategySinglePass Strategy = "single-pass"
	StrategyMapReduce  Strategy = "map-reduce"
)

// Mode is how a review actually ran.
type Mode string

// The modes. A single-pass review split into several parts runs and reports as map-reduce.
const (
	ModeSinglePass Mode = "single-pass"
	ModeMapReduce  Mode = "map-reduce"
)

// FailOn is an agent's CI gate (agents.ci_fail_on): the lowest severity that counts as a blocker.
type FailOn string

// The gates. never counts no blocker; any counts every finding.
const (
	FailOnNever    FailOn = "never"
	FailOnCritical FailOn = "critical"
	FailOnWarning  FailOn = "warning"
	FailOnAny      FailOn = "any"
)

// Severity of a finding.
type Severity string

// The severities, worst first. Score penalties: CRITICAL 35, WARNING 12, SUGGESTION 3; ranks
// (for the CI gate): CRITICAL 3, WARNING 2, SUGGESTION 1.
const (
	SeverityCritical   Severity = "CRITICAL"
	SeverityWarning    Severity = "WARNING"
	SeveritySuggestion Severity = "SUGGESTION"
)

// Category of a finding.
type Category string

// The categories.
const (
	CategoryBug      Category = "bug"
	CategorySecurity Category = "security"
	CategoryPerf     Category = "perf"
	CategoryStyle    Category = "style"
	CategoryTest     Category = "test"
)

// Kind of a finding. The empty Kind means the model sent none; it is stored as KindFinding.
type Kind string

// The kinds. All but KindFinding are full-file kinds: grounding only requires the file to be in
// the diff, not a line to be in a hunk.
const (
	KindFinding        Kind = "finding"
	KindSecretLeak     Kind = "secret_leak"
	KindLethalTrifecta Kind = "lethal_trifecta"
	KindPhantom        Kind = "phantom"
	KindHook           Kind = "hook"
)

// Verdict of a review. The reduce step takes the worst: request_changes > comment > approve.
type Verdict string

// The verdicts.
const (
	VerdictRequestChanges Verdict = "request_changes"
	VerdictComment        Verdict = "comment"
	VerdictApprove        Verdict = "approve"
)

// EventKind is the kind of a run event (the Live Log line type).
type EventKind string

// The event kinds.
const (
	EventInfo   EventKind = "info"
	EventTool   EventKind = "tool"
	EventResult EventKind = "result"
	EventError  EventKind = "error"
)

// Hunk is one "@@ -a,b +c,d @@" block of a file's patch.
type Hunk struct {
	File string
	// OldStart and OldLines are the old-side range; a header without a count means 1.
	OldStart, OldLines int
	// NewStart and NewLines are the new-side range; a header without a count means 1.
	NewStart, NewLines int
	// NewLineNumbers are the new-side line numbers of the hunk's added and context lines.
	NewLineNumbers []int
}

// DiffFile is one changed file of the diff.
type DiffFile struct {
	// Path is the new path (from the "+++" line, without "b/").
	Path                 string
	Additions, Deletions int
	Hunks                []Hunk
}

// Diff is a parsed unified diff.
type Diff struct {
	// Raw is the text the diff was parsed from.
	Raw   string
	Files []DiffFile
}

// PatchFile is one stored pull-request file with its patch; the raw diff of a run is rebuilt
// from these (spec 7.7.1). Only files that have a patch are listed.
type PatchFile struct {
	Path  string
	Patch string
}

// Chunk is the diff text of one LLM call: the whole diff, one file, or one part of an oversize
// file.
type Chunk struct {
	// Label is "all files", the file path, or "<label> (part <i>/<n>)".
	Label    string
	DiffText string
}

// ChunkRef locates one call among the review's calls.
type ChunkRef struct {
	// Index is 0-based.
	Index int
	// Total is the number of calls the review makes (1 for a single pass).
	Total int
}

// Finding is one issue a model reports.
type Finding struct {
	// ID is the model's own id; it is not stored.
	ID       string
	Severity Severity
	Category Category
	Title    string
	File     string
	// StartLine and EndLine are the cited range; the model may send them in either order.
	StartLine, EndLine int
	Rationale          string
	// Suggestion is nil when the model sent none.
	Suggestion *string
	Confidence float64
	Kind       Kind
	// TrifectaComponents is nil unless Kind is KindLethalTrifecta.
	TrifectaComponents []string
	// Skill is the name of the skill rule the finding enforces; nil when none.
	Skill *string
	// OutOfScope is the model's scope judgment; nil means null. The engine result carries nil
	// without a PR intent (the model had nothing to judge against) and a plain boolean with one
	// (ApplyScopePolicy); a stored finding is false for nil (spec FR4028, FR5025).
	OutOfScope *bool
}

// Review is one structured answer of the model, or the reduced result of several.
type Review struct {
	Verdict  Verdict
	Summary  string
	Score    int
	Findings []Finding
}

// Dropped is a finding the grounding gate removed, with the reason.
type Dropped struct {
	Finding Finding
	Reason  string
}

// Usage is the token usage and cost of one or more LLM responses.
type Usage struct {
	TokensIn, TokensOut int
	// CostUSD is nil when any response was unpriced.
	CostUSD *float64
}

// Role of a prompt message.
type Role string

// The roles of a review prompt.
const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
)

// Message is one prompt message.
type Message struct {
	Role    Role
	Content string
}

// SectionName names a prompt section in the prompt log.
type SectionName string

// The prompt sections a review can contain, in render order (no memory). The intent pair follows
// pr_description and appears only with a PR intent (Phase 5 spec 7.5).
const (
	SectionSystem         SectionName = "system"
	SectionInjectionGuard SectionName = "injection_guard"
	SectionTask           SectionName = "task"
	SectionPRDescription  SectionName = "pr_description"
	SectionIntentRule     SectionName = "intent_rule"
	SectionIntent         SectionName = "intent"
	SectionSkills         SectionName = "skills"
	SectionRepoMap        SectionName = "repo_map"
	SectionSpecsRule      SectionName = "specs_rule"
	SectionSpecs          SectionName = "specs"
	SectionCallers        SectionName = "callers"
	SectionDiff           SectionName = "diff"
)

// SectionSource says where a section's content comes from.
type SectionSource string

// The section sources.
const (
	SourceEngine      SectionSource = "engine"
	SourceAgentConfig SectionSource = "agent_config"
	SourceAuthor      SectionSource = "author"
	SourceRepo        SectionSource = "repo"
	SourceCurated     SectionSource = "curated"
	// SourceModelDerived marks text a separate model derived (the PR intent).
	SourceModelDerived SectionSource = "model_derived"
)

// Trust says whether the model may treat a section as instructions.
type Trust string

// The trust levels.
const (
	Trusted   Trust = "trusted"
	Untrusted Trust = "untrusted"
)

// SectionMeta is the content-free description of one rendered prompt section (spec 7.8): names,
// counts and closed labels only, never text. Chars and Tokens are measured on the exact string
// the section contributes to its message.
type SectionMeta struct {
	Name          SectionName
	Source        SectionSource
	Role          Role
	Trust         Trust
	Chars, Tokens int
	// Items is the number of skills or documents the section was built from; nil for other sections.
	Items *int
	// Truncated is set on the PR description only: whether the body was cut at the cap.
	Truncated *bool
}

// Assembly is the prompt of a call as the run trace records it (spec 7.8, "Trace assembly").
// Memory is always null in Phases 4 and 5 and is left out of the type; Intent is nil without a PR
// intent.
type Assembly struct {
	// System is the agent's system prompt followed by the injection guard.
	System string
	// Skills is the joined skill blocks; nil when the run has no skills.
	Skills *string
	// Specs is the joined wrapped project-context documents; nil when there are none.
	Specs *string
	// Callers, RepoMap and PRDescription are nil when absent (PRDescription is the cut text).
	Callers, RepoMap, PRDescription *string
	// Intent is the rendered PR intent text (before wrapping); nil when the run had no intent.
	Intent *string
	// User is the user message.
	User string
}

// ProjectDoc is a project-context document handed to the engine: the repo-relative path (the
// delimiter label) and its text.
type ProjectDoc struct {
	Path string
	Text string
}

// Outcome is the result of a successful engine run.
type Outcome struct {
	// Review is the reduced, grounded review; its Score is recomputed from the kept findings.
	// Every kept finding's OutOfScope is nil without a PR intent and a plain boolean with one.
	Review Review
	// Grounding is the summary "<kept>/<total> passed".
	Grounding string
	// Dropped lists the findings grounding removed.
	Dropped []Dropped
	// Mode is the mode that ran (a split single pass reports map-reduce).
	Mode Mode
	// Assembly is the call's prompt (single pass) or the whole-diff prompt (map-reduce).
	Assembly Assembly
	// ChunkLabels are the labels of the calls, in order.
	ChunkLabels []string
	// Usage sums the usage of every call.
	Usage Usage
	// Raw is the raw model outputs joined by "\n---\n".
	Raw string
}
