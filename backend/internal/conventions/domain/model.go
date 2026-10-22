package domain

import (
	"errors"
	"strings"
	"time"
)

// Sentinel errors of the conventions context.
var (
	// ErrNotFound means a repo, scan or convention does not exist in the workspace.
	ErrNotFound = errors.New("conventions: not found")
	// ErrScanRunning means the repo already has a running scan.
	ErrScanRunning = errors.New("conventions: a scan is already running")
	// ErrCloneMissing means the clone directory of a repo is gone.
	ErrCloneMissing = errors.New("conventions: clone directory is missing")
)

// ScanStatus is the state of a scan.
type ScanStatus string

// Scan states.
const (
	ScanRunning ScanStatus = "running"
	ScanDone    ScanStatus = "done"
	ScanFailed  ScanStatus = "failed"
)

// Status is the review state of a convention.
type Status string

// Convention states.
const (
	StatusPending  Status = "pending"
	StatusAccepted Status = "accepted"
	StatusRejected Status = "rejected"
)

// Statuses lists every convention status.
func Statuses() []Status { return []Status{StatusPending, StatusAccepted, StatusRejected} }

// Scan is one extraction run over a repo.
type Scan struct {
	ID          string
	WorkspaceID string
	RepoID      string
	Status      ScanStatus
	// SampledFiles are the files rendered into the prompt, in order; never nil from a store.
	SampledFiles []string
	// Proposed is the model's candidate count (uncapped); Kept passed the gate.
	Proposed, Kept int
	Dropped        []Dropped
	// Model is the requested model; nil until the scan is done.
	Model   *string
	CostUSD *float64
	Error   *string
	// StartedAt is when the scan began; FinishedAt is nil while it runs.
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Convention is a stored house rule of a repo.
type Convention struct {
	ID          string
	WorkspaceID string
	RepoID      string
	ScanID      *string
	Category    string
	Rule        string
	Evidence    []Evidence
	// Confidence is the model's confidence, 0..1 (a stored NULL reads as 0).
	Confidence float64
	Status     Status
	// Edited is true once the user changed the rule text or category.
	Edited    bool
	SkillID   *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Decided reports whether the user took a decision on the rule (accepted or rejected it) or
// edited it; such a rule survives a re-scan and is never proposed again (FR246, FR247).
func (c Convention) Decided() bool { return c.Status != StatusPending || c.Edited }

// IsBlank reports whether text holds only white space, in the sense of JavaScript's
// String.prototype.trim (a blank file is never sampled).
func IsBlank(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return !isJSSpace(r) }) < 0
}
