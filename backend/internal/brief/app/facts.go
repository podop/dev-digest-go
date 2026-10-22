package app

import (
	"context"
	"errors"
	"sync"

	"dev-digest-go/backend/internal/brief/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// gathered are the facts a generation reads besides its own pull request rows.
type gathered struct {
	// intent is the stored intent; nil when there is none or its read failed.
	intent *reviewer.Intent
	// blast is the blast as read; nil when the read failed.
	blast *domain.Blast
	// docs are the merged documents of the current reviews' agents.
	docs []domain.Document
	// linked is true when the description links an issue; issue is it when it was fetched.
	linked bool
	issue  *domain.Issue
}

// gather reads the intent, the blast radius, the attached documents and the linked issue in
// parallel (spec 7.9.1). A failed intent or blast read and any issue failure leave that fact
// missing; only a failed document read fails the generation. The reads share ctx, so the budget
// and a shutdown cut them short.
func (s *Service) gather(ctx context.Context, workspaceID string, pull domain.Pull, reviews []domain.ReviewFacts, files []domain.ChangedFile) (gathered, error) {
	var g gathered
	number, linked := domain.LinkedIssueNumber(pull.Body)
	g.linked = linked

	perReview := make([][]domain.Document, len(reviews))
	tasks := []func() error{
		func() error {
			intent, err := s.d.Intents.Stored(ctx, pull.ID)
			if err != nil {
				s.log.WarnContext(ctx, "pr brief: intent read failed", "prId", pull.ID, "err", err)
				return nil
			}
			g.intent = intent
			return nil
		},
		func() error {
			b, err := s.d.Blast.RadiusFor(ctx, pull.RepoID, changedPaths(files))
			if err != nil {
				s.log.WarnContext(ctx, "pr brief: blast read failed", "prId", pull.ID, "err", err)
				return nil
			}
			g.blast = &b
			return nil
		},
	}
	if linked {
		tasks = append(tasks, func() error {
			g.issue = s.fetchIssue(ctx, pull, number)
			return nil
		})
	}
	for i, r := range reviews {
		if r.AgentID == nil {
			continue // the agent was deleted: its review counts, it attaches nothing
		}
		tasks = append(tasks, func() error {
			docs, err := s.d.Documents.ForAgent(ctx, DocumentsInput{WorkspaceID: workspaceID, Pull: pull, AgentID: *r.AgentID})
			perReview[i] = docs
			return err
		})
	}
	if err := parallel(tasks); err != nil {
		return gathered{}, err
	}
	g.docs = domain.MergeDocuments(perReview...)
	return g, nil
}

// fetchIssue reads the linked issue within domain.IssueTimeout; every failure is a missing issue.
func (s *Service) fetchIssue(ctx context.Context, pull domain.Pull, number int) *domain.Issue {
	ictx, cancel := context.WithTimeout(ctx, domain.IssueTimeout)
	defer cancel()
	issue, err := s.d.Issues.Issue(ictx, pull.Owner, pull.Name, number)
	if err != nil {
		return nil
	}
	return &issue
}

// changedPaths lists the distinct stored paths in stored order.
func changedPaths(files []domain.ChangedFile) []string {
	paths := make([]string, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if !seen[f.Path] {
			seen[f.Path] = true
			paths = append(paths, f.Path)
		}
	}
	return paths
}

// parallel runs the tasks concurrently and waits for all of them. A panic on a worker would end
// the process (INSIGHTS: worker panics), so it is recovered there and raised again on the
// caller's goroutine, where the request's own recovery handles it.
func parallel(tasks []func() error) error {
	errs := make([]error, len(tasks))
	panics := make([]any, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Go(func() {
			defer func() { panics[i] = recover() }()
			errs[i] = task()
		})
	}
	wg.Wait()
	for _, p := range panics {
		if p != nil {
			panic(p)
		}
	}
	return errors.Join(errs...)
}
