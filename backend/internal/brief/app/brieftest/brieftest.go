// Package brieftest holds in-memory fakes of the ports of brief/app, shared by the use case tests
// and the transport tests. They are safe for concurrent use and record what they were asked.
package brieftest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Store is an in-memory app.Store holding one pull request.
type Store struct {
	mu sync.Mutex
	// Pull is the pull request; PullMissing makes FindPull answer domain.ErrNotFound.
	Pull        domain.Pull
	PullMissing bool
	Files       []domain.StoredFile
	Reviews     []domain.ReviewFacts
	// Stored is the brief on file; nil reads as absent.
	Stored *domain.Brief
	// SaveErr makes SaveBrief fail; the others make their method fail.
	SaveErr, FilesErr, ReviewsErr, BriefErr error
	// Saved lists every brief SaveBrief accepted, oldest first.
	Saved []domain.Brief
}

var _ app.Store = (*Store)(nil)

// FindPull implements app.Store.
func (s *Store) FindPull(_ context.Context, workspaceID, prID string) (domain.Pull, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PullMissing || prID != s.Pull.ID || (s.Pull.WorkspaceID != "" && workspaceID != s.Pull.WorkspaceID) {
		return domain.Pull{}, domain.ErrNotFound
	}
	return s.Pull, nil
}

// PullFiles implements app.Store.
func (s *Store) PullFiles(context.Context, string) ([]domain.StoredFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Files, s.FilesErr
}

// CurrentReviews implements app.Store.
func (s *Store) CurrentReviews(context.Context, string) ([]domain.ReviewFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Reviews, s.ReviewsErr
}

// Brief implements app.Store.
func (s *Store) Brief(context.Context, string) (domain.Brief, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.BriefErr != nil {
		return domain.Brief{}, false, s.BriefErr
	}
	if s.Stored == nil {
		return domain.Brief{}, false, nil
	}
	return *s.Stored, true, nil
}

// SaveBrief implements app.Store: the brief becomes the stored one.
func (s *Store) SaveBrief(_ context.Context, _ string, b domain.Brief) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SaveErr != nil {
		return s.SaveErr
	}
	s.Saved = append(s.Saved, b)
	s.Stored = &b
	return nil
}

// Intents is an app.Intents answering one stored intent.
type Intents struct {
	mu     sync.Mutex
	Intent *reviewer.Intent
	Err    error
	Calls  int
}

var _ app.Intents = (*Intents)(nil)

// Stored implements app.Intents.
func (i *Intents) Stored(context.Context, string) (*reviewer.Intent, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.Calls++
	return i.Intent, i.Err
}

// Blast is an app.Blast answering one blast radius.
type Blast struct {
	mu    sync.Mutex
	Blast domain.Blast
	Err   error
	// Calls records the changed paths of every call.
	Calls [][]string
}

var _ app.Blast = (*Blast)(nil)

// RadiusFor implements app.Blast.
func (b *Blast) RadiusFor(_ context.Context, _ string, changed []string) (domain.Blast, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Calls = append(b.Calls, changed)
	return b.Blast, b.Err
}

// Documents is an app.Documents answering the documents of each agent.
type Documents struct {
	mu      sync.Mutex
	ByAgent map[string][]domain.Document
	Err     error
	// Hook runs first in every call (to block, to panic); its error fails the call.
	Hook  func(ctx context.Context, in app.DocumentsInput) error
	Calls []app.DocumentsInput
}

var _ app.Documents = (*Documents)(nil)

// ForAgent implements app.Documents.
func (d *Documents) ForAgent(ctx context.Context, in app.DocumentsInput) ([]domain.Document, error) {
	d.mu.Lock()
	d.Calls = append(d.Calls, in)
	hook, docs, err := d.Hook, d.ByAgent[in.AgentID], d.Err
	d.mu.Unlock()
	if hook != nil {
		if herr := hook(ctx, in); herr != nil {
			return nil, herr
		}
	}
	return docs, err
}

// Issues is an app.Issues answering one issue.
type Issues struct {
	mu sync.Mutex
	// Fetched is the issue every call answers with, unless Err is set.
	Fetched domain.Issue
	Err     error
	// Hang blocks every call until its context ends.
	Hang bool
	// Calls and Repos record the issue number and "owner/name" of every call.
	Calls []int
	Repos []string
}

var _ app.Issues = (*Issues)(nil)

// Issue implements app.Issues.
func (i *Issues) Issue(ctx context.Context, owner, name string, number int) (domain.Issue, error) {
	i.mu.Lock()
	i.Calls = append(i.Calls, number)
	i.Repos = append(i.Repos, owner+"/"+name)
	hang, issue, err := i.Hang, i.Fetched, i.Err
	i.mu.Unlock()
	if hang {
		<-ctx.Done()
		return domain.Issue{}, ctx.Err()
	}
	return issue, err
}

// GitLabIssues is an app.GitLabIssues answering one issue.
type GitLabIssues struct {
	mu sync.Mutex
	// Fetched is the issue every call answers with, unless Err is set.
	Fetched domain.Issue
	Err     error
	// Hang blocks every call until its context ends.
	Hang bool
	// Calls records "host/projectID#number" of every call.
	Calls []string
}

var _ app.GitLabIssues = (*GitLabIssues)(nil)

// Issue implements app.GitLabIssues.
func (i *GitLabIssues) Issue(ctx context.Context, host string, projectID int64, number int) (domain.Issue, error) {
	i.mu.Lock()
	i.Calls = append(i.Calls, fmt.Sprintf("%s/%d#%d", host, projectID, number))
	hang, issue, err := i.Hang, i.Fetched, i.Err
	i.mu.Unlock()
	if hang {
		<-ctx.Done()
		return domain.Issue{}, ctx.Err()
	}
	return issue, err
}

// Roles is an app.Roles that answers every path with one role.
type Roles string

var _ app.Roles = Roles("")

// RoleFor implements app.Roles.
func (r Roles) RoleFor(string, string) string { return string(r) }

// Models is an app.FeatureModels answering a settings bag.
type Models struct {
	Bag map[string]json.RawMessage
	Err error
}

var _ app.FeatureModels = (*Models)(nil)

// Get implements app.FeatureModels.
func (m *Models) Get(context.Context, string) (map[string]json.RawMessage, error) {
	return m.Bag, m.Err
}

// LLM is an app.LLM answering a script.
type LLM struct {
	mu sync.Mutex
	// Result is the answer; Usages are reported through OnUsage before it (one per attempt).
	Result llm.Result
	Usages []llm.AttemptUsage
	Err    error
	// Hang blocks the call until its context ends, then fails with that context's error.
	Hang bool
	// Enter, when set, is called with the request's context on entry; the call goes on when it
	// returns (a test blocks in it to hold a generation open).
	Enter    func(ctx context.Context)
	Requests []llm.Request
}

var _ app.LLM = (*LLM)(nil)

// Structured implements app.LLM.
func (l *LLM) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	l.mu.Lock()
	l.Requests = append(l.Requests, req)
	enter, hang, usages, res, err := l.Enter, l.Hang, l.Usages, l.Result, l.Err
	l.mu.Unlock()
	if enter != nil {
		enter(ctx)
		if err := ctx.Err(); err != nil {
			return llm.Result{}, err
		}
	}
	if hang {
		<-ctx.Done()
		return llm.Result{}, ctx.Err()
	}
	if req.OnUsage != nil {
		for _, u := range usages {
			req.OnUsage(u)
		}
	}
	return res, err
}
