// Package domain is the vocabulary and the pure rules of the intent context (Phase 5, spec 7.4):
// deriving what a pull request is meant to change from its title, body, commits, linked tickets
// and docs. It imports only the standard library, reviewer/domain (the shared kernel that
// holds Intent) and platform/codehost (stdlib-only provider names) and carries no JSON tags: the wire shapes live in transport/http, the stored
// shapes in adapters/postgres.
//
// This file fixes the shared types and constants; the rules sit next to it: links.go (ticket and
// doc extraction), key.go (the cache key), sources.go (collecting the sources and the prompt
// block), classify.go (the classification call), clamp.go (clamps of the model's output) and
// schema.go (the structured-output schema). Lengths are UTF-16 code units, as in JavaScript.
package domain

import (
	"errors"
	"time"

	"dev-digest-go/backend/internal/platform/codehost"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Limits and labels of a derivation (Node intent/domain/constants.ts, spec 7.4).
const (
	// PromptVersion is part of the cache key: bumping it makes every stored row miss.
	PromptVersion = 2
	// FeatureModel is the settings feature key of the model (llm.ResolveFeatureModel).
	FeatureModel = "review_intent"
	// SchemaName is the structured-output schema name (and the mock fixture key).
	SchemaName = "IntentClassification"
	// LogFeature labels the call's prompt_assembled line.
	LogFeature = "intent"

	// Budget bounds one derivation, from its start; the LLM call, ticket and doc reads honour it.
	// Twice Node's 120 s: one request may take the provider's whole 120 s request timeout.
	Budget = 240 * time.Second
	// MaxOutputTokens, Reprompts and Temperature are the classification call's parameters.
	MaxOutputTokens = 5_000
	Reprompts       = 1
	Temperature     = 0.0
	// RefreshRatePerMinute is the bucket of POST /pulls/:id/intent/refresh.
	RefreshRatePerMinute = 10

	// TitleMaxUnits and BodyMaxUnits cut the title and body sources.
	TitleMaxUnits = 300
	BodyMaxUnits  = 6_000
	// TicketMax is the most tickets read; TicketMaxUnits cuts one ticket's text.
	TicketMax      = 3
	TicketMaxUnits = 3_000
	// DocMax is the most docs read; DocFetchMaxBytes caps a doc's size (clone or GitHub),
	// DocMaxUnits cuts one doc's text, DocTotalMaxUnits is the budget over all docs.
	DocMax           = 5
	DocFetchMaxBytes = 64 * 1024
	DocMaxUnits      = 8_000
	DocTotalMaxUnits = 20_000
	// Fallback sources (derived_from inferred): commit messages, branch, changed paths, diff.
	CommitsMax          = 30
	CommitMaxUnits      = 200
	BranchMaxUnits      = 120
	ChangedFilesMax     = 100
	DiffExcerptMaxUnits = 6_000
	// SubstantiveBodyMinUnits is the length of the stripped body from which it counts as explicit.
	SubstantiveBodyMinUnits = 40
	// Clamps of the model's output.
	IntentMaxUnits    = 500
	ScopeMaxItems     = 10
	ScopeItemMaxUnits = 240
)

// DerivedFrom values.
const (
	DerivedExplicit = "explicit"
	DerivedInferred = reviewer.DerivedInferred
)

// Confidence values (set by code, never by the model).
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Source kinds and statuses (IntentSource.Kind and .Status).
const (
	KindTitle   = "title"
	KindBody    = "body"
	KindTicket  = "ticket"
	KindDoc     = "doc"
	KindCommits = "commits"
	KindBranch  = "branch"
	KindDiff    = "diff"

	StatusUsed      = "used"
	StatusTruncated = "truncated"
	StatusSkipped   = "skipped"
	StatusFailed    = "failed"
)

// Run event kinds the derivation emits (the values of reviewer.EventInfo and EventResult).
const (
	EventInfo   = "info"
	EventResult = "result"
)

// WarningCode is the data.warning of the "intent unavailable" event.
const WarningCode = "intent_unavailable"

// ErrNotFound is returned by the lookups and the store for a missing row; the service maps it to
// the 404 of its operation.
var ErrNotFound = errors.New("not found")

// Pull is the pull request as a derivation needs it. The review path passes the values read at
// request time; the routes read them from the store.
type Pull struct {
	ID, RepoID string
	Number     int
	Title      string
	// Body is "" for a NULL or empty body (the rules treat both alike).
	Body    string
	Branch  string
	HeadSHA string
}

// Repo is the repository of a pull request.
type Repo struct {
	ID, Owner, Name string
	// Provider ("github" | "gitlab"; "" reads as github), Host (the host key) and ProjectID (the
	// GitLab project id; 0 = none) say where the repository lives: the GitHub reads apply to a
	// GitHub repository only (FR29) and the clone location comes from codehost.CloneKey.
	Provider  string
	Host      string
	ProjectID int64
	// WebURL is the stored web address of a GitLab project (connection base address + project
	// path); "" for GitHub. The GitLab link rules read the "same project" from it (gitlab_links.go).
	WebURL string
}

// IsGitLab reports whether the repository is a GitLab project.
func (r Repo) IsGitLab() bool { return r.Provider == codehost.GitLab }

// Commit is one commit of the pull request (pr_commits).
type Commit struct {
	SHA, Message string
}

// Skipped is a ticket or doc reference that is not read, with the reason shown on its source.
type Skipped struct {
	Ref    string
	Reason string
}

// TicketRef is a same-repo issue to read. Number is the JavaScript Number of the digits (a float,
// so absurdly long digit runs behave as in Node); Closing is true when a closing keyword
// precedes any of its mentions.
type TicketRef struct {
	Number  float64
	Closing bool
}

// DocRef is a repo-relative doc path to read.
type DocRef struct {
	Path string
}

// TicketLinks is the result of ticket extraction: at most TicketMax refs in reading order, the
// rest and the foreign or unsupported references as skipped.
type TicketLinks struct {
	Refs    []TicketRef
	Skipped []Skipped
}

// DocLinks is the result of doc extraction: at most DocMax refs in priority order, the rest and
// the rejected references as skipped.
type DocLinks struct {
	Refs    []DocRef
	Skipped []Skipped
}

// KeyInput is the material of the cache key: its canonical JSON (sorted keys, JavaScript
// JSON.stringify escaping) hashed with SHA-256 gives input_hash. Ticket and doc bodies are not
// part of it, only their numbers and paths.
type KeyInput struct {
	Body    string
	Branch  string
	HeadSHA string
	Model   string
	// PromptVersion is PromptVersion.
	PromptVersion int
	Provider      string
	Title         string
	// DocPaths are sorted in code-unit order, TicketRefs ascending.
	DocPaths   []string
	TicketRefs []float64
}

// Source is one collected input of a derivation: the part shown on the record (kind, ref,
// status, detail) plus the text that goes into the prompt.
type Source struct {
	Kind, Ref, Status string
	// Detail is the reason of a skipped or failed source; nil otherwise.
	Detail *string
	// Content is the prompt text; "" for skipped and failed sources.
	Content string
}

// IntentSource is the record part of s.
func (s Source) IntentSource() reviewer.IntentSource {
	return reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
}

// Classification is the model's answer (schema IntentClassification), before the clamps.
type Classification struct {
	Intent              string
	InScope, OutOfScope []string
	// ChangeType is one of feature, bugfix, refactor, docs, test, chore, security, perf, mixed.
	ChangeType string
}

// Record is a stored derivation (a pr_intent row): the intent, the key material and the usage.
// A row with a NULL derived_at is no record. Its slices are never nil.
type Record struct {
	PRID                string
	Text                string
	InScope, OutOfScope []string
	ChangeType          *string
	Confidence          *string
	DerivedFrom         string
	// Sources are in collection order; each source's Detail is nil when none.
	Sources []reviewer.IntentSource
	// HeadSHA, InputHash, Provider and Model are "" for NULL columns; PromptVersion, TokensIn and
	// TokensOut are 0 for NULL.
	HeadSHA, InputHash  string
	PromptVersion       int
	Provider, Model     string
	TokensIn, TokensOut int
	// CostUSD is nil when the derivation was unpriced; a stored value has 6 decimals.
	CostUSD   *float64
	DerivedAt time.Time
}

// Snapshot is the intent a review prompt and a brief use: the record without its usage. The
// slices are shared with the record and must not be modified.
func (r Record) Snapshot() reviewer.Intent {
	return reviewer.Intent{
		Text: r.Text, InScope: r.InScope, OutOfScope: r.OutOfScope,
		ChangeType: r.ChangeType, Confidence: r.Confidence, DerivedFrom: r.DerivedFrom, Sources: r.Sources,
	}
}
