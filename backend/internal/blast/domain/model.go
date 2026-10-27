// Package domain is the vocabulary of the blast context (Phase 5, spec sections 7.7 and 7.8): the
// answer of the repo-intel BlastRadius port as this context reads it, the BlastRadius read model
// the PR page shows, and the prior merged pull requests that touched the same files. The pure
// rules (mapping the port answer to the read model, summary text, history selection) are added
// next to this file; it fixes only the shared types and constants. It imports only the standard
// library (and platform/codehost's provider names) and carries no JSON or database tags: the wire shapes live in transport/http.
package domain

import (
	"errors"

	"dev-digest-go/backend/internal/platform/codehost"
)

// ErrNotFound is returned by the store for a pull request that does not exist in the workspace.
var ErrNotFound = errors.New("blast: not found")

// History limits (spec section 7.8).
const (
	// HistoryListLimit is the number of recently closed pull requests the GitHub read lists.
	HistoryListLimit = 30
	// HistoryMax is the most prior pull requests the answer holds.
	HistoryMax = 10
)

// Reason says why a blast answer is degraded; the values are the five of the RepoIndexState
// contract, as repo-intel's degraded reasons.
type Reason string

// The degraded reasons.
const (
	ReasonFlagOff      Reason = "flag_off"
	ReasonIndexFailed  Reason = "index_failed"
	ReasonIndexPartial Reason = "index_partial"
	ReasonRepoTooLarge Reason = "repo_too_large"
	ReasonNoData       Reason = "no_data"
)

// ChangedSymbol is a symbol declared in a changed file.
type ChangedSymbol struct {
	Name string
	File string
	Kind string
}

// Caller is one reference to a changed symbol, as the port answers it.
type Caller struct {
	// File is the file of the reference and Symbol the symbol enclosing it (the file's base name
	// when none encloses it).
	File   string
	Symbol string
	// ViaSymbol is the changed symbol that is referenced.
	ViaSymbol string
	Line      int
	// Rank is the file rank of File; higher is more central. 0 on the degraded clone-scan path.
	Rank float64
}

// Facts are the endpoints and cron entries a file registers, in stored order.
type Facts struct {
	Endpoints []string
	Crons     []string
}

// PortResult is the answer of the repo-intel BlastRadius operation. The peers adapter maps the
// repo-intel result to it, so this context never imports repo-intel.
type PortResult struct {
	ChangedSymbols []ChangedSymbol
	Callers        []Caller
	// FactsByFile holds the facts of the files of Callers; nil on the degraded clone-scan path.
	FactsByFile map[string]Facts
	// Degraded is true when the answer is incomplete or came from the clone scan; Reason is then
	// set.
	Degraded bool
	Reason   Reason
}

// CallerRef is a caller as the response lists it.
type CallerRef struct {
	Name string
	File string
	Line int
}

// Downstream is the impact of one changed symbol.
type Downstream struct {
	Symbol            string
	Callers           []CallerRef
	EndpointsAffected []string
	CronsAffected     []string
}

// BlastRadius is the read model of a pull request. Degraded is always present on the wire; Reason
// is present only when Degraded is true. Every slice is non-nil.
type BlastRadius struct {
	ChangedSymbols []ChangedSymbol
	Downstream     []Downstream
	Summary        string
	Degraded       bool
	Reason         Reason
}

// Pull is the pull request a blast or history read is about.
type Pull struct {
	ID     string
	RepoID string
	Number int
	// Owner and Name are the repository's.
	Owner, Name string
	// Provider is the repository's code host ("github" | "gitlab"; "" reads as github).
	Provider string
	// Host is the GitLab repository's connection host key and ProjectID its numeric project id
	// (ProjectID is zero for a GitHub repository, whose Host is "github.com").
	Host      string
	ProjectID int64
}

// IsGitLab reports whether the pull request belongs to a GitLab project.
func (p Pull) IsGitLab() bool { return p.Provider == codehost.GitLab }

// MergedPR is a merged pull request of the repository with the files it touched, as the GitHub
// read answers it.
type MergedPR struct {
	Number int
	Title  string
	// Author is the user's login or "unknown".
	Author string
	// MergedAt is the timestamp as GitHub sent it.
	MergedAt string
	// Files are the distinct filenames, first-seen order.
	Files []string
}

// PriorPR is a prior merged pull request that touched files of this one.
type PriorPR struct {
	PRNumber int
	Title    string
	MergedAt string
	Author   string
	// FilesOverlap are the shared paths, sorted in code-unit order; never empty, never nil.
	FilesOverlap []string
	// Notes is always "" on the wire.
}
