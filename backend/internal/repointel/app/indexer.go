package app

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"dev-digest-go/backend/internal/repointel/domain"
)

// failWriteTimeout bounds the write of a failed state, made after the job's own context may be
// gone.
const failWriteTimeout = 10 * time.Second

// indexGit is the part of Git the indexer uses.
type indexGit interface {
	Head(ctx context.Context, owner, name string) (string, error)
	DiffNames(ctx context.Context, owner, name, base, head string) ([]string, error)
}

// IndexerDeps are the collaborators of the Indexer.
type IndexerDeps struct {
	Reader   IndexReader
	Writer   IndexWriter
	Tx       Tx
	Git      indexGit
	Files    domain.SourceFS
	Analyzer Analyzer
	Graph    GraphBuilder
	Tokens   domain.TokenCounter
	// Redact scrubs provider keys out of every error text before it is stored or logged; nil
	// leaves texts as they are.
	Redact Redactor
}

// IndexerOptions tune the Indexer.
type IndexerOptions struct {
	// Now is the clock; nil means time.Now. It drives the soft budget, the durations and the
	// state's updated_at.
	Now func() time.Time
	// Workers bounds the parallel file parses; zero or less means max(1, CPU count - 1).
	Workers int
	// Log receives warnings; nil discards them. Nothing logged carries source text.
	Log *slog.Logger
}

// Result is what an index run reports to its job: the state it ended in and the counters of
// this run. A run that ended without writing reports status degraded and a reason.
type Result struct {
	Status       domain.Status
	FilesIndexed int
	FilesSkipped int
	Duration     time.Duration
	// Reason names how the run ended, e.g. "no_clone", "no_files", "sha_unchanged"; empty for a
	// plain full or partial pass.
	Reason string
}

// Indexer runs the full and the incremental index of a repo (FR3011-FR3021, FR3037). One
// Indexer serves concurrent runs of different repos; runs of one repo are serialised by the
// repo lock inside the transaction.
type Indexer struct {
	d       IndexerDeps
	now     func() time.Time
	workers int
	log     *slog.Logger
}

// NewIndexer builds an Indexer.
func NewIndexer(d IndexerDeps, opts IndexerOptions) *Indexer {
	ix := &Indexer{d: d, now: opts.Now, workers: opts.Workers, log: opts.Log}
	if ix.d.Redact == nil {
		ix.d.Redact = noRedact{}
	}
	if ix.now == nil {
		ix.now = time.Now
	}
	if ix.workers <= 0 {
		ix.workers = max(1, runtime.NumCPU()-1)
	}
	if ix.log == nil {
		ix.log = slog.New(slog.DiscardHandler)
	}
	return ix
}

// since is the time elapsed from start by the indexer's clock.
func (ix *Indexer) since(start time.Time) time.Duration { return ix.now().Sub(start) }

// ended is the Result of a run that wrote nothing.
func (ix *Indexer) ended(start time.Time, reason string) Result {
	return Result{Status: domain.StatusDegraded, Duration: ix.since(start), Reason: reason}
}

// persistEarly upserts a state at an early exit and ignores a failure of the write (FR3011,
// FR3012): the next run stamps the row again.
func (ix *Indexer) persistEarly(ctx context.Context, st domain.IndexState) {
	st.IndexerVersion = domain.IndexerVersion
	st.UpdatedAt = ix.now()
	if err := ix.d.Writer.UpsertState(ctx, st); err != nil {
		ix.log.WarnContext(context.WithoutCancel(ctx), "repo intel state not written",
			"repo_id", st.RepoID, "status", string(st.Status), "error", errorText(ix.d.Redact, err.Error()))
	}
}

type noRedact struct{}

func (noRedact) Redact(s string) string { return s }

// errorText is the error text stored in a state row or a log line: provider keys scrubbed
// first (so a key cut in half is never stored), then cut to MaxErrorChars UTF-16 units.
func errorText(r Redactor, s string) string {
	return domain.ClampUTF16(r.Redact(s), domain.MaxErrorChars)
}
