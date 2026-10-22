package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/rank"
)

// full is the full index (FR3011-FR3015).
func (ix *Indexer) full(ctx context.Context, repoID string) (Result, error) {
	start := ix.now()
	repo, err := ix.d.Reader.GetRepo(ctx, repoID)
	if errors.Is(err, domain.ErrRepoNotFound) {
		return ix.ended(start, "repo_not_found"), nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("load repo %s: %w", repoID, err)
	}
	if repo.ClonePath == "" {
		ix.persistEarly(ctx, domain.IndexState{
			RepoID: repoID, Status: domain.StatusDegraded,
			Stats: map[string]any{
				domain.StatReason:         "no_clone",
				domain.StatDegradedReason: string(domain.ReasonNoData),
				domain.StatDurationMs:     int(ix.since(start).Milliseconds()),
			},
		})
		return ix.ended(start, "no_clone"), nil
	}
	root := repo.ClonePath

	sha, err := ix.d.Git.Head(ctx, repo.Owner, repo.Name)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("read HEAD: %w", ctxErr)
		}
		sha = ""
	}
	walk, err := ix.d.Files.Walk(ctx, root)
	if err != nil {
		return Result{}, fmt.Errorf("walk clone: %w", err)
	}
	if len(walk.Files) == 0 {
		stats := walkStats(walk.Stats)
		stats[domain.StatReason] = "no_files"
		stats[domain.StatDurationMs] = int(ix.since(start).Milliseconds())
		ix.persistEarly(ctx, domain.IndexState{
			RepoID: repoID, LastIndexedSHA: sha, Status: domain.StatusPartial,
			FilesSkipped: walk.Stats.SkippedTooLarge, Stats: stats,
		})
		return Result{
			Status: domain.StatusPartial, FilesSkipped: walk.Stats.SkippedTooLarge,
			Duration: ix.since(start), Reason: "no_files",
		}, nil
	}

	// Parse phase: once the soft budget is spent no new file is started.
	out, softBudget, err := ix.parseFiles(ctx, root, walk.Files, func() bool { return ix.since(start) >= domain.SoftBudget })
	if err != nil {
		return Result{}, err
	}
	filesSkipped := walk.Stats.SkippedTooLarge + out.skipped

	// Import graph, outside the transaction; skipped when the budget is spent.
	var (
		edges       []domain.Edge
		graphFailed string
	)
	if !softBudget {
		if edges, graphFailed, err = ix.buildGraph(ctx, root, walk.Files); err != nil {
			return Result{}, err
		}
	}
	status := domain.FullStatus(softBudget, graphFailed != "", len(out.degraded))

	var ranks []domain.FileRankRow
	if !softBudget {
		ranks = rank.FileRank(walk.Files, edges)
	}
	rankCount := len(ranks)

	err = ix.d.Tx.InTx(ctx, func(ctx context.Context) error {
		w := ix.d.Writer
		if err := w.LockRepo(ctx, repoID); err != nil {
			return fmt.Errorf("lock repo: %w", err)
		}
		if err := w.DeleteRepoRows(ctx, repoID); err != nil {
			return fmt.Errorf("delete symbols and references: %w", err)
		}
		if err := w.InsertSymbols(ctx, repoID, out.symbols); err != nil {
			return fmt.Errorf("insert symbols: %w", err)
		}
		if err := w.InsertReferences(ctx, repoID, out.refs); err != nil {
			return fmt.Errorf("insert references: %w", err)
		}
		if !softBudget {
			if err := w.ReplaceEdges(ctx, repoID, edges); err != nil {
				return fmt.Errorf("replace edges: %w", err)
			}
			if err := w.ResolveReferences(ctx, repoID, false); err != nil {
				return fmt.Errorf("resolve references: %w", err)
			}
			if err := w.ReplaceFileRank(ctx, repoID, ranks); err != nil {
				return fmt.Errorf("replace file rank: %w", err)
			}
			if err := ix.replaceRepoMap(ctx, repoID, sha); err != nil {
				return err
			}
			if err := w.ReplaceFileFacts(ctx, repoID, out.facts); err != nil {
				return fmt.Errorf("replace file facts: %w", err)
			}
		}
		stats := walkStats(walk.Stats)
		stats["filesSeen"] = len(walk.Files)
		stats["symbolsWritten"] = len(out.symbols)
		stats["referencesWritten"] = len(out.refs)
		stats["edgesWritten"] = len(edges)
		stats["ranked"] = rankCount
		stats["factsWritten"] = len(out.facts)
		stats["hotnessAvailable"] = false
		if graphFailed != "" {
			stats["graphFailed"] = graphFailed
		}
		stats["softBudgetReached"] = softBudget
		stats["parseDegraded"] = out.degraded
		stats[domain.StatDurationMs] = int(ix.since(start).Milliseconds())
		return w.UpsertState(ctx, domain.IndexState{
			RepoID: repoID, LastIndexedSHA: sha, IndexerVersion: domain.IndexerVersion, Status: status,
			FilesIndexed: out.indexed, FilesSkipped: filesSkipped, Stats: stats, UpdatedAt: ix.now(),
		})
	})
	if err != nil {
		return Result{}, fmt.Errorf("persist full index: %w", err)
	}
	res := Result{Status: status, FilesIndexed: out.indexed, FilesSkipped: filesSkipped, Duration: ix.since(start)}
	switch {
	case softBudget:
		res.Reason = "soft_budget"
	case graphFailed != "":
		res.Reason = "graph_failed"
	}
	return res, nil
}

// replaceRepoMap renders the repo map from the transaction's own rows, drops the repo's cached
// maps and stores the new one for sha (none when sha is empty).
func (ix *Indexer) replaceRepoMap(ctx context.Context, repoID, sha string) error {
	candidates, err := ix.d.Reader.MapCandidates(ctx, repoID)
	if err != nil {
		return fmt.Errorf("read map candidates: %w", err)
	}
	text, tokens := rank.RenderMap(candidates, ix.d.Tokens, domain.DefaultTokenBudget)
	if err := ix.d.Writer.DeleteRepoMaps(ctx, repoID); err != nil {
		return fmt.Errorf("delete repo maps: %w", err)
	}
	if sha == "" {
		return nil
	}
	m := domain.RepoMap{CommitSHA: sha, Budget: domain.DefaultTokenBudget, Text: text, Tokens: tokens}
	if err := ix.d.Writer.PutRepoMap(ctx, repoID, m); err != nil {
		return fmt.Errorf("put repo map: %w", err)
	}
	return nil
}

// walkStats are the walk counters as the keys of the state's stats document.
func walkStats(s domain.WalkStats) map[string]any {
	return map[string]any{
		"totalCandidates": s.TotalCandidates,
		"skippedTooLarge": s.SkippedTooLarge,
		"bounded":         s.Bounded,
	}
}
