// Package app holds the use cases of the conventions context: reading the scans and rules of a
// repo, starting a scan, reviewing rules, and the background job that runs a scan.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Job kind and limits of the extract job (spec FR242, D208).
const (
	// KindExtract is the job kind a scan runs under.
	KindExtract = "conventions.extract"
	// JobTimeout is the per-attempt limit of KindExtract; register the handler with it.
	JobTimeout = 300 * time.Second
)

// Texts the client sees (spec FR242, FR247, FR248, D210).
const (
	repoNotFound   = "Repo not found"
	notClonedHint  = "The repo is not cloned yet — wait for the clone to finish"
	scanRunningMsg = "A conventions scan is already running for this repo"
	// MsgInterrupted is the error of a scan cut short by a shutdown, a restart or a stale start.
	MsgInterrupted = "The scan was interrupted"
	// errorMaxChars caps a stored scan error (UTF-16 code units, as in Node).
	errorMaxChars = 500
)

// ExtractPayload is the payload of an extract job.
type ExtractPayload struct {
	ScanID      string `json:"scanId"`
	WorkspaceID string `json:"workspaceId"`
	RepoID      string `json:"repoId"`
}

// State is the conventions page of a repo: its latest scan and all its rules.
type State struct {
	// Scan is nil when the repo was never scanned.
	Scan        *domain.Scan
	Conventions []domain.Convention
}

// Patch is a validated update of a convention; nil fields are left alone. At least one is set.
type Patch struct {
	Status   *domain.Status
	Rule     *string
	Category *string
}

// ServiceOptions are the optional parts of a Service.
type ServiceOptions struct {
	// Redact scrubs provider keys out of error texts; nil leaves texts as they are.
	Redact redactor
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service implements the conventions use cases. It is safe for concurrent use.
type Service struct {
	store  scanStore
	repos  repoLookup
	jobs   enqueuer
	redact redactor
	now    func() time.Time
}

// NewService builds a Service.
func NewService(store scanStore, repos repoLookup, jobs enqueuer, opts ServiceOptions) *Service {
	s := &Service{store: store, repos: repos, jobs: jobs, redact: opts.Redact, now: opts.Now}
	if s.redact == nil {
		s.redact = noRedact{}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

type noRedact struct{}

func (noRedact) Redact(s string) string { return s }

// State returns the latest scan and every rule of the repo. An unknown repo is a not-found error.
func (s *Service) State(ctx context.Context, workspaceID, repoID string) (State, error) {
	if _, err := s.repo(ctx, workspaceID, repoID); err != nil {
		return State{}, err
	}
	var st State
	scan, err := s.store.LatestScan(ctx, repoID)
	switch {
	case err == nil:
		st.Scan = &scan
	case !errors.Is(err, domain.ErrNotFound):
		return State{}, fmt.Errorf("load latest scan of %s: %w", repoID, err)
	}
	if st.Conventions, err = s.store.List(ctx, repoID); err != nil {
		return State{}, fmt.Errorf("list conventions of %s: %w", repoID, err)
	}
	return st, nil
}

// Extract starts a background scan of the repo and returns the running scan at once. A running
// scan older than domain.ScanStaleAfter is failed first. Errors: not found (unknown repo),
// validation not_cloned (no clone yet), conflict scan_running (one is already running).
func (s *Service) Extract(ctx context.Context, workspaceID, repoID string) (domain.Scan, error) {
	repo, err := s.repo(ctx, workspaceID, repoID)
	if err != nil {
		return domain.Scan{}, err
	}
	if repo.ClonePath == "" {
		return domain.Scan{}, apperr.Validation(notClonedHint).WithCode("not_cloned")
	}
	now := s.now()
	if err := s.store.FailStaleScans(ctx, repoID, now.Add(-domain.ScanStaleAfter), MsgInterrupted, now); err != nil {
		return domain.Scan{}, fmt.Errorf("fail stale scans of %s: %w", repoID, err)
	}
	scan, err := s.store.StartScan(ctx, workspaceID, repoID, now)
	if errors.Is(err, domain.ErrScanRunning) {
		return domain.Scan{}, apperr.Conflict(scanRunningMsg).WithCode("scan_running").WithCause(err)
	}
	if err != nil {
		return domain.Scan{}, fmt.Errorf("start scan of %s: %w", repoID, err)
	}
	payload := ExtractPayload{ScanID: scan.ID, WorkspaceID: workspaceID, RepoID: repoID}
	if _, err := s.jobs.Enqueue(ctx, workspaceID, KindExtract, payload); err != nil {
		text := errorText(s.redact, err.Error())
		// The request context may be gone already; the scan must not stay running.
		wctx := context.WithoutCancel(ctx)
		if ferr := s.store.FailScan(wctx, scan.ID, text, nil, s.now()); ferr != nil {
			err = errors.Join(err, fmt.Errorf("fail scan %s: %w", scan.ID, ferr))
		}
		return domain.Scan{}, fmt.Errorf("enqueue scan %s: %w", scan.ID, err)
	}
	return scan, nil
}

// FailInterrupted fails every scan left running, with the interrupted text, and returns how many
// it failed. Call it at startup before the server listens (FR248).
func (s *Service) FailInterrupted(ctx context.Context) (int, error) {
	n, err := s.store.FailRunningScans(ctx, MsgInterrupted, s.now())
	if err != nil {
		return 0, fmt.Errorf("fail interrupted scans: %w", err)
	}
	return n, nil
}

// Update applies the patch to a convention of the workspace. A different rule or category sets
// edited for good; a status change alone keeps it. Any status transition is allowed.
func (s *Service) Update(ctx context.Context, workspaceID, id string, p Patch) (domain.Convention, error) {
	now := s.now()
	got, err := s.store.Update(ctx, workspaceID, id, func(c domain.Convention) (domain.Convention, error) {
		if p.Status != nil {
			c.Status = *p.Status
		}
		if p.Rule != nil && *p.Rule != c.Rule {
			c.Rule, c.Edited = *p.Rule, true
		}
		if p.Category != nil && *p.Category != c.Category {
			c.Category, c.Edited = *p.Category, true
		}
		c.UpdatedAt = now
		return c, nil
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Convention{}, apperr.NotFound("Convention not found").WithCause(err)
	}
	if err != nil {
		return domain.Convention{}, fmt.Errorf("update convention %s: %w", id, err)
	}
	return got, nil
}

// NotAccepted returns, in input order and without repeats, the ids that are not accepted
// conventions of the repo. An unknown repo is a not-found error. It runs in the caller's
// transaction when ctx carries one.
func (s *Service) NotAccepted(ctx context.Context, workspaceID, repoID string, ids []string) ([]string, error) {
	if _, err := s.repo(ctx, workspaceID, repoID); err != nil {
		return nil, err
	}
	unique := dedupe(ids)
	if len(unique) == 0 {
		return nil, nil
	}
	accepted, err := s.store.AcceptedIDs(ctx, workspaceID, repoID, unique)
	if err != nil {
		return nil, fmt.Errorf("load accepted conventions of %s: %w", repoID, err)
	}
	ok := make(map[string]struct{}, len(accepted))
	for _, id := range accepted {
		ok[strings.ToLower(id)] = struct{}{}
	}
	var out []string
	for _, id := range unique {
		if _, found := ok[strings.ToLower(id)]; !found {
			out = append(out, id)
		}
	}
	return out, nil
}

// AttachSkill records the skill the conventions were merged into. It runs in the caller's
// transaction when ctx carries one.
func (s *Service) AttachSkill(ctx context.Context, workspaceID, repoID string, ids []string, skillID string) error {
	unique := dedupe(ids)
	if len(unique) == 0 {
		return nil
	}
	if err := s.store.SetSkill(ctx, workspaceID, repoID, unique, skillID, s.now()); err != nil {
		return fmt.Errorf("attach skill %s to conventions of %s: %w", skillID, repoID, err)
	}
	return nil
}

// repo loads the repo of the workspace or returns the not-found error of the API.
func (s *Service) repo(ctx context.Context, workspaceID, repoID string) (Repo, error) {
	repo, err := s.repos.Get(ctx, workspaceID, repoID)
	if errors.Is(err, domain.ErrNotFound) {
		return Repo{}, apperr.NotFound(repoNotFound).WithCause(err)
	}
	if err != nil {
		return Repo{}, fmt.Errorf("load repo %s: %w", repoID, err)
	}
	return repo, nil
}

func dedupe(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	var out []string
	for _, id := range ids {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// errorText scrubs provider keys out of text and cuts the result to errorMaxChars UTF-16 code
// units, without splitting a character. Scrubbing comes first, so a key cut in half is never
// stored.
func errorText(r redactor, text string) string {
	text = r.Redact(text)
	units := 0
	for i, c := range text {
		units += utf16.RuneLen(c)
		if units > errorMaxChars {
			return text[:i]
		}
	}
	return text
}
