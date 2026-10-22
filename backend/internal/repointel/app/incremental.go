package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/rank"
)

// incremental is the incremental index (FR3016-FR3021).
func (ix *Indexer) incremental(ctx context.Context, repoID string) (Result, error) {
	start := ix.now()
	repo, err := ix.d.Reader.GetRepo(ctx, repoID)
	if errors.Is(err, domain.ErrRepoNotFound) {
		return ix.ended(start, "no_clone"), nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("load repo %s: %w", repoID, err)
	}
	if repo.ClonePath == "" {
		return ix.ended(start, "no_clone"), nil
	}
	root := repo.ClonePath

	state, ok, err := ix.d.Reader.GetState(ctx, repoID)
	if err != nil {
		return Result{}, fmt.Errorf("read index state: %w", err)
	}
	if !ok || state.IndexerVersion != domain.IndexerVersion {
		return ix.full(ctx, repoID)
	}

	head, err := ix.d.Git.Head(ctx, repo.Owner, repo.Name)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("read HEAD: %w", ctxErr)
		}
		return ix.ended(start, "git_head_failed:"+errorText(ix.d.Redact, err.Error())), nil
	}
	if head == state.LastIndexedSHA {
		if err := ix.d.Writer.TouchState(ctx, repoID, ix.now()); err != nil {
			return Result{}, fmt.Errorf("touch index state: %w", err)
		}
		return Result{Status: state.Status, FilesIndexed: state.FilesIndexed, FilesSkipped: state.FilesSkipped,
			Duration: ix.since(start), Reason: "sha_unchanged"}, nil
	}

	changedAll, err := ix.d.Git.DiffNames(ctx, repo.Owner, repo.Name, state.LastIndexedSHA, head)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("list changed files: %w", ctxErr)
		}
		// A base commit missing from the shallow clone or output past the git cap (FR3016).
		return ix.full(ctx, repoID)
	}
	changed := supportedPaths(changedAll)
	if len(changed) == 0 {
		if err := ix.d.Writer.AdvanceSHA(ctx, repoID, head, ix.now()); err != nil {
			return Result{}, fmt.Errorf("advance indexed sha: %w", err)
		}
		return Result{Status: state.Status, FilesIndexed: state.FilesIndexed, FilesSkipped: state.FilesSkipped,
			Duration: ix.since(start), Reason: "no_supported_changes"}, nil
	}
	if len(changed) > domain.MaxIncrementalFiles {
		return ix.full(ctx, repoID)
	}

	walk, err := ix.d.Files.Walk(ctx, root)
	if err != nil {
		return Result{}, fmt.Errorf("walk clone: %w", err)
	}
	inWalk := make(map[string]struct{}, len(walk.Files))
	for _, f := range walk.Files {
		inWalk[f] = struct{}{}
	}
	// A changed path outside the walk (deleted, renamed away, excluded, too large, symlink,
	// beyond the bound) is removed: its rows go, it counts as skipped, never as degraded.
	var live []string
	removed := 0
	for _, p := range changed {
		if _, ok := inWalk[p]; ok {
			live = append(live, p)
		} else {
			removed++
		}
	}
	out, _, err := ix.parseFiles(ctx, root, live, nil, 0)
	if err != nil {
		return Result{}, err
	}
	skipped := out.skipped + removed

	// The graph, rank and map are global: rebuilt over the whole walk (P15 re-parses unchanged
	// files for their imports only).
	edges, graphFailed, err := ix.buildGraph(ctx, root, walk.Files)
	if err != nil {
		return Result{}, err
	}

	var status domain.Status
	err = ix.d.Tx.InTx(ctx, func(ctx context.Context) error {
		w := ix.d.Writer
		if err := w.LockRepo(ctx, repoID); err != nil {
			return fmt.Errorf("lock repo: %w", err)
		}
		if err := w.DeleteFileRows(ctx, repoID, changed); err != nil {
			return fmt.Errorf("delete changed rows: %w", err)
		}
		if err := w.InsertSymbols(ctx, repoID, out.symbols); err != nil {
			return fmt.Errorf("insert symbols: %w", err)
		}
		if err := w.InsertReferences(ctx, repoID, out.refs); err != nil {
			return fmt.Errorf("insert references: %w", err)
		}
		if err := w.PatchFileFacts(ctx, repoID, changed, out.facts); err != nil {
			return fmt.Errorf("patch file facts: %w", err)
		}
		if graphFailed == "" {
			err := ix.d.Tx.Savepoint(ctx, func(ctx context.Context) error {
				if err := w.ReplaceEdges(ctx, repoID, edges); err != nil {
					return fmt.Errorf("replace edges: %w", err)
				}
				if err := w.ResolveReferences(ctx, repoID, true); err != nil {
					return fmt.Errorf("resolve references: %w", err)
				}
				if err := w.ReplaceFileRank(ctx, repoID, rank.FileRank(walk.Files, edges)); err != nil {
					return fmt.Errorf("replace file rank: %w", err)
				}
				return ix.replaceRepoMap(ctx, repoID, head)
			})
			if err != nil {
				// Only the savepoint is rolled back; a vanished repo or a cancelled attempt
				// ends the whole index.
				if ctx.Err() != nil || errors.Is(err, domain.ErrRepoNotFound) {
					return err
				}
				graphFailed = errorText(ix.d.Redact, err.Error())
			}
		}
		status = domain.IncrementalStatus(state.Status, graphFailed != "", len(out.degraded))

		stats := map[string]any{
			"incremental":         true,
			"changedFiles":        len(changed),
			"symbolsWritten":      len(out.symbols),
			"referencesWritten":   len(out.refs),
			"edgesWritten":        len(edges),
			"hotnessAvailable":    false,
			"parseDegraded":       out.degraded,
			domain.StatDurationMs: int(ix.since(start).Milliseconds()),
		}
		if graphFailed != "" {
			stats["graphFailed"] = graphFailed
		}
		return w.UpsertState(ctx, domain.IndexState{
			RepoID: repoID, LastIndexedSHA: head, IndexerVersion: domain.IndexerVersion, Status: status,
			FilesIndexed: state.FilesIndexed + out.indexed, FilesSkipped: state.FilesSkipped + skipped,
			Stats: stats, UpdatedAt: ix.now(),
		})
	})
	if err != nil {
		return Result{}, fmt.Errorf("persist incremental index: %w", err)
	}
	return Result{Status: status, FilesIndexed: out.indexed, FilesSkipped: skipped, Duration: ix.since(start), Reason: "incremental"}, nil
}

// supportedPaths keeps the changed paths with a supported extension, sorted and without
// duplicates (FR3018).
func supportedPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if domain.IsSupported(p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
