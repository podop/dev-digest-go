package app

import (
	"context"
	"sync"
	"sync/atomic"

	"dev-digest-go/backend/internal/onboarding/domain"
)

// group runs goroutines and waits for them. A panic in one is recorded, and re-raised on the
// goroutine that calls Wait, after every member has returned: a panic on a worker would otherwise
// bypass the request's recover and end the whole server (backend/INSIGHTS.md, worker panics).
type group struct {
	wg       sync.WaitGroup
	mu       sync.Mutex
	panicked bool
	value    any
}

func (g *group) Go(fn func()) {
	g.wg.Go(func() {
		defer func() {
			if v := recover(); v != nil {
				g.mu.Lock()
				defer g.mu.Unlock()
				if !g.panicked {
					g.panicked, g.value = true, v
				}
			}
		}()
		fn()
	})
}

// Wait blocks until every goroutine started with Go has returned, then re-raises the first panic.
func (g *group) Wait() {
	g.wg.Wait()
	if g.panicked {
		panic(g.value)
	}
}

// readBlobs reads paths at sha and calls visit(i, text) for every file that exists, from at most
// domain.MaxBlobReaders goroutines. A missing, oversize, unsafe or failing read is skipped (the
// file is absent); once ctx is done no further read starts. visit sees one file at a time per
// worker and the text is not kept by readBlobs, so a caller can reduce a large blob to a few
// lines and let it go (NFR5006).
func (s *Service) readBlobs(ctx context.Context, repo domain.Repo, sha string, paths []string, visit func(i int, text string)) {
	var next atomic.Int64
	var g group
	for range min(domain.MaxBlobReaders, len(paths)) {
		g.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= len(paths) || ctx.Err() != nil {
					return
				}
				data, ok := s.d.Blobs.Read(ctx, repo.Owner, repo.Name, sha, paths[i], domain.MaxBlobBytes)
				if !ok {
					continue
				}
				visit(i, string(data))
			}
		})
	}
	g.Wait()
}

// indexFacts is what the repo-intel index tells about the repository.
type indexFacts struct {
	repoMap string
	ranked  []string
	chains  [][]string
	indexed []string
}

// readIndex runs the four index reads in parallel; the first error wins.
func (s *Service) readIndex(ctx context.Context, repoID string) (indexFacts, error) {
	var (
		f    indexFacts
		g    group
		mu   sync.Mutex
		fail error
	)
	note := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if fail == nil {
			fail = err
		}
	}
	g.Go(func() {
		var err error
		f.repoMap, err = s.d.Index.RepoMap(ctx, repoID, domain.RepoMapBudget)
		note(wrapStep("read repo map", err))
	})
	g.Go(func() {
		var err error
		f.ranked, err = s.d.Index.TopFilesByRank(ctx, repoID, domain.TopFilesLimit)
		note(wrapStep("read top files", err))
	})
	g.Go(func() {
		var err error
		f.chains, err = s.d.Index.CriticalPaths(ctx, repoID)
		note(wrapStep("read critical paths", err))
	})
	g.Go(func() {
		var err error
		f.indexed, err = s.d.Index.ListIndexedFiles(ctx, repoID)
		note(wrapStep("read indexed files", err))
	})
	g.Wait()
	return f, fail
}

// readFiles reads the root files and the ranked files at the indexed commit, the two lists in
// parallel. It returns the root texts aligned with domain.RootFilePaths (nil = absent) and each
// ranked file's TODO lines; the ranked blobs themselves are dropped as they are read.
func (s *Service) readFiles(ctx context.Context, repo domain.Repo, sha string, ranked []string) (domain.RootTexts, [][]domain.TodoLine) {
	rootPaths := domain.RootFilePaths()
	rootTexts := make([]*string, len(rootPaths))
	todos := make([][]domain.TodoLine, len(ranked))
	var g group
	g.Go(func() {
		s.readBlobs(ctx, repo, sha, rootPaths, func(i int, text string) { rootTexts[i] = &text })
	})
	g.Go(func() {
		s.readBlobs(ctx, repo, sha, ranked, func(i int, text string) {
			todos[i] = domain.FindTodoLines(ranked[i], text, domain.TodoMax)
		})
	})
	g.Wait()
	return domain.PickRootFiles(rootTexts), todos
}

// buildInput assembles the prompt input (spec 7.10.2): excerpts of the root files, the signals,
// and the index facts. The redactions are the one CPU-heavy step over repository text: ctx is
// checked before each, and built is false once it is done (the generation has ended).
func buildInput(ctx context.Context, repo domain.Repo, f indexFacts, roots domain.RootTexts, todos [][]domain.TodoLine) (in domain.TourInput, built bool) {
	in = domain.TourInput{
		RepoName:      repo.FullName,
		DefaultBranch: repo.DefaultBranch,
		RepoMap:       f.repoMap,
		TopFiles:      f.ranked[:min(len(f.ranked), domain.TopFilesInPrompt)],
		Chains:        f.chains,
		TodoLines:     domain.CollectTodoLines(todos),
		UntestedFiles: domain.FindUntestedFiles(f.ranked, f.indexed),
	}
	if ctx.Err() != nil {
		return in, false
	}
	if roots.Readme != nil {
		in.Readme, _ = domain.ReadmeExcerpt(*roots.Readme)
	}
	if ctx.Err() != nil {
		return in, false
	}
	if roots.PackageJSON != nil {
		in.PackageScripts, _ = domain.PackageScripts(*roots.PackageJSON)
	}
	if roots.Compose != nil {
		in.Compose, _ = domain.ComposeExcerpt(*roots.Compose)
	}
	if roots.EnvExample != nil {
		in.EnvNames = domain.EnvVariableNames(*roots.EnvExample)
	}
	return in, ctx.Err() == nil
}
