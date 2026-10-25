package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/spend/domain"
)

// appendTimeout bounds one live append: the recorder runs on the calling goroutine of an LLM
// call, so a slow database must not hold the call longer (A10, D3).
const appendTimeout = 3 * time.Second

// Recorder turns the usage of an answered LLM attempt, and a newly ingested CI run, into ledger
// entries. It implements llm.Recorder. internal/llm is shared infrastructure, imported by the
// feature contexts' app packages alike.
type Recorder struct {
	entries Entries
	now     Clock
	log     *slog.Logger
}

var _ llm.Recorder = (*Recorder)(nil)

// NewRecorder builds the recorder. now stamps the occurrence time of live entries (the moment the
// answer is received).
func NewRecorder(entries Entries, now Clock, log *slog.Logger) *Recorder {
	return &Recorder{entries: entries, now: now, log: log}
}

// RecordUsage appends one live entry for the answered attempt (FR1, FR5). The caller's context is
// only used for its values: the append runs detached from its cancellation, because a cancelled
// call has still spent its tokens, and is bounded by appendTimeout. A failure logs one ERROR line
// and returns: the unit of work goes on and that cost is missing from the total (D8, FR15).
func (r *Recorder) RecordUsage(ctx context.Context, u llm.RecordedUsage) {
	kind, err := domain.ParseKind(string(u.Charge.Kind))
	if err == nil && kind == domain.KindCIRun {
		// CI runs arrive through RecordCIRun, never through an LLM call.
		err = fmt.Errorf("%w: %q is not an LLM unit", domain.ErrInvalidKind, kind)
	}
	e := domain.Entry{
		WorkspaceID: u.Charge.WorkspaceID,
		OccurredAt:  r.now().UTC(),
		Kind:        kind,
		Origin:      domain.OriginLive,
		Provider:    string(u.Provider),
		Model:       u.Model,
		TokensIn:    tokens(u.Tokens.TokensIn),
		TokensOut:   tokens(u.Tokens.TokensOut),
		Cost:        domain.CostFromUSD(u.CostUSD),
		RefID:       refID(u.Charge.RefID),
	}
	if err == nil {
		e.DedupeKey, err = liveDedupeKey()
	}
	if err != nil {
		r.logAppendFailure(ctx, u.Charge.WorkspaceID, string(u.Charge.Kind), e, err)
		return
	}

	appendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), appendTimeout)
	defer cancel()
	if _, err := r.entries.Append(appendCtx, e); err != nil {
		r.logAppendFailure(ctx, e.WorkspaceID, string(e.Kind), e, err)
	}
}

// logAppendFailure writes the one FR15 ERROR line of an entry that could not be recorded. It
// names the kind, provider, model, cost and workspace and nothing of the call's content (NFR4).
func (r *Recorder) logAppendFailure(ctx context.Context, workspaceID, kind string, e domain.Entry, err error) {
	attrs := []any{
		"kind", kind,
		"provider", e.Provider,
		"model", e.Model,
		"workspace_id", workspaceID,
		"unpriced", e.Cost == nil,
		"error", err,
	}
	if e.Cost != nil {
		attrs = append(attrs, "cost_usd", e.Cost.USD())
	}
	r.log.ErrorContext(context.WithoutCancel(ctx), "cost ledger: entry not recorded; its cost is missing from the month total", attrs...)
}

// CIRun is a CI run newly stored by CI ingest, with the artifact's reported cost.
type CIRun struct {
	WorkspaceID string
	// AgentRunID is the id of the stored run record; it becomes the entry's RefID.
	AgentRunID string
	// GithubRunID and Attempt identify the workflow run attempt; they make the dedupe key (FR6).
	GithubRunID int64
	Attempt     int
	// OccurredAt is the run time the ingest stores for the run.
	OccurredAt time.Time
	Provider   *string
	Model      *string
	// CostUSD is the artifact's reported total; nil = unknown.
	CostUSD *float64
}

// RecordCIRun appends the entry of a new CI run (kind ci_run, origin ci_ingest, FR6). Unlike
// RecordUsage it runs on the caller's context as is, so the append joins the transaction that
// stores the run, and it returns the error: a failed append must roll the run back. Recording a
// run that already has its entry (same run id and attempt) is a no-op, not an error: after a
// history reset a re-ingested run is new for CI and only this key stops double counting.
func (r *Recorder) RecordCIRun(ctx context.Context, run CIRun) error {
	e := domain.Entry{
		WorkspaceID: run.WorkspaceID,
		OccurredAt:  run.OccurredAt.UTC(),
		Kind:        domain.KindCIRun,
		Origin:      domain.OriginCIIngest,
		Provider:    deref(run.Provider),
		Model:       deref(run.Model),
		Cost:        domain.CostFromUSD(run.CostUSD),
		RefID:       refID(run.AgentRunID),
		DedupeKey:   domain.CIDedupeKey(run.GithubRunID, run.Attempt),
	}
	if _, err := r.entries.Append(ctx, e); err != nil {
		return fmt.Errorf("record ci run entry: %w", err)
	}
	return nil
}

// BackfillSummary is what the upgrade backfill reports about itself (FR10, FR15).
type BackfillSummary struct {
	// Created is the number of entries created per kind.
	Created map[domain.Kind]int
	// Skipped is the number of records skipped as malformed.
	Skipped  int
	Duration time.Duration
}

// ReportBackfill logs the one FR15 INFO line of the backfill: entries created per kind, records
// skipped as malformed and the duration. It holds counts only.
func (r *Recorder) ReportBackfill(ctx context.Context, s BackfillSummary) {
	kinds := make([]domain.Kind, 0, len(s.Created))
	total := 0
	for k, n := range s.Created {
		kinds = append(kinds, k)
		total += n
	}
	slices.Sort(kinds)
	created := make([]any, 0, len(kinds))
	for _, k := range kinds {
		created = append(created, slog.Int(string(k), s.Created[k]))
	}
	r.log.InfoContext(ctx, "cost ledger: backfill finished",
		slog.Int("created_total", total),
		slog.Group("created", created...),
		slog.Int("skipped_malformed", s.Skipped),
		slog.Int64("duration_ms", s.Duration.Milliseconds()),
	)
}

// uuidPattern matches the canonical textual form of a UUID, the type of the ledger's ref_id.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// refID returns id when it is a UUID and "" otherwise: the reference is informational and never
// enforced, so an unusable one must not lose the entry (FR2).
func refID(id string) string {
	if uuidPattern.MatchString(id) {
		return id
	}
	return ""
}

// tokens converts a token count; a negative count is unknown.
func tokens(n int) *int64 {
	if n < 0 {
		return nil
	}
	v := int64(n)
	return &v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// liveDedupeKey returns a fresh random key: every answered attempt is its own entry (D3), so a
// live entry is never a duplicate of another.
func liveDedupeKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate dedupe key: %w", err)
	}
	return "live:" + hex.EncodeToString(b[:]), nil
}
