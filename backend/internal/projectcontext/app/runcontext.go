package app

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

var _ gitReader = (*gitcli.Client)(nil)

// RunSkill is an enabled skill linked to the run's agent.
type RunSkill struct {
	ID   string
	Name string
}

// RunContextInput identifies the documents one review run attaches.
type RunContextInput struct {
	WorkspaceID string
	RepoID      string
	// Owner and Name locate the repository's clone.
	Owner, Name string
	// Base is the pull request's base branch name and HeadSHA its head commit: repo documents
	// are read at the base commit resolved from them, never from the head.
	Base, HeadSHA string
	AgentID       string
	// Skills are the agent's enabled skills in link order.
	Skills []RunSkill
}

// RunContext is the documents of a run.
type RunContext struct {
	// Docs lists every attached document in prompt order; empty when nothing is attached.
	Docs         []domain.RunDoc
	TokensTotal  int
	BudgetTokens int
	// LogLine is the run's single event; empty when Docs is empty.
	LogLine string
}

// WithGit returns a copy of the service that reads repo documents of a run through g.
// RunContext fails without it.
func (s *Service) WithGit(g gitReader) *Service {
	c := *s
	c.git = g
	return &c
}

// RunContext resolves the documents of one run: the agent's attached paths, then each skill's.
// A store file comes from the database (its latest save, even without a clone); any other
// document is read at the base commit, with at most domain.RunReadConcurrency reads in flight.
// The budget is applied in list order. A document that cannot be read gets a status; only an
// unexpected failure (store, git outage, cancellation) is an error.
func (s *Service) RunContext(ctx context.Context, in RunContextInput) (RunContext, error) {
	none := RunContext{BudgetTokens: domain.RunBudgetTokens}

	agentPaths, err := s.store.AgentPaths(ctx, in.AgentID, in.RepoID)
	if err != nil {
		return none, fmt.Errorf("agent paths: %w", err)
	}
	sets := make([]domain.SkillPaths, 0, len(in.Skills))
	for _, sk := range in.Skills {
		paths, err := s.store.SkillPaths(ctx, sk.ID, in.RepoID)
		if err != nil {
			return none, fmt.Errorf("skill paths: %w", err)
		}
		sets = append(sets, domain.SkillPaths{SkillID: sk.ID, SkillName: sk.Name, Paths: paths})
	}
	refs := domain.BuildRunDocList(agentPaths, sets)
	if len(refs) == 0 {
		return none, nil
	}
	if s.git == nil {
		return none, errors.New("git reader is not configured")
	}

	commit, err := s.git.BaseCommit(ctx, in.Owner, in.Name, in.Base, in.HeadSHA)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return none, fmt.Errorf("resolve base commit: %w", ctxErr)
		}
		commit = "" // an unresolvable base is "no base commit", not a failure
	}
	// No base commit: a repo that was never cloned has none of its documents (missing); a
	// clone whose base cannot be resolved cannot be read (unreadable).
	noClone := false
	if commit == "" {
		repo, ok, err := s.store.FindRepo(ctx, in.WorkspaceID, in.RepoID)
		if err != nil {
			return none, fmt.Errorf("find repo: %w", err)
		}
		noClone = !ok || repo.ClonePath == nil
	}

	read := make([]domain.RunDoc, len(refs))
	errs := make([]error, len(refs))
	sem := make(chan struct{}, domain.RunReadConcurrency)
	var wg sync.WaitGroup
	for i, ref := range refs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			read[i], errs[i] = s.readRunDocSafely(ctx, in, commit, noClone, ref)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return none, err
	}

	docs, total := domain.ApplyBudget(read, domain.RunBudgetTokens)
	return RunContext{
		Docs:         docs,
		TokensTotal:  total,
		BudgetTokens: domain.RunBudgetTokens,
		LogLine:      domain.RunLogLine(docs, total),
	}, nil
}

// readRunDocSafely is readRunDoc on its own goroutine: a panic there would end the process, so it
// becomes an unreadable document and a log line with the panic's type and stack, never its value
// (it may quote document content).
func (s *Service) readRunDocSafely(ctx context.Context, in RunContextInput, commit string, noClone bool, ref domain.RunDocRef) (doc domain.RunDoc, err error) {
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.ErrorContext(ctx, "project context: reading a run document panicked",
					"repoId", in.RepoID, "path", ref.Path, "panic", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
			}
			doc, err = domain.SkippedRunDoc(ref, domain.RunSourceRepo, domain.RunUnreadable), nil
		}
	}()
	return s.readRunDoc(ctx, in, commit, noClone, ref)
}

// readRunDoc reads one document: a store file from the database, anything else at commit.
func (s *Service) readRunDoc(ctx context.Context, in RunContextInput, commit string, noClone bool, ref domain.RunDocRef) (domain.RunDoc, error) {
	if err := ctx.Err(); err != nil {
		return domain.RunDoc{}, fmt.Errorf("read %s: %w", ref.Path, err)
	}
	if domain.CheckStorePath(ref.Path) {
		file, ok, err := s.store.FindFile(ctx, in.RepoID, ref.Path)
		if err != nil {
			return domain.RunDoc{}, fmt.Errorf("find store file %s: %w", ref.Path, err)
		}
		if ok {
			return domain.NewRunDoc(ref, domain.RunSourceStore, file.Content), nil
		}
	}
	skip := func(status domain.RunStatus) (domain.RunDoc, error) {
		return domain.SkippedRunDoc(ref, domain.RunSourceRepo, status), nil
	}
	if !domain.CheckPath(ref.Path) {
		return skip(domain.RunUnreadable)
	}
	if commit == "" {
		if noClone {
			return skip(domain.RunMissing)
		}
		return skip(domain.RunUnreadable)
	}
	blob, err := s.git.BlobAt(ctx, in.Owner, in.Name, commit, ref.Path, domain.MaxDocBytes)
	switch {
	case err == nil:
		return domain.NewRunDoc(ref, domain.RunSourceRepo, domain.DecodeUTF8(blob)), nil
	case errors.Is(err, gitcli.ErrBlobTooLarge):
		return skip(domain.RunTooLarge)
	case errors.Is(err, gitcli.ErrBlobMissing):
		return skip(domain.RunMissing)
	case ctx.Err() != nil:
		return domain.RunDoc{}, fmt.Errorf("read %s: %w", ref.Path, ctx.Err())
	default:
		return skip(domain.RunUnreadable)
	}
}
