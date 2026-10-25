package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// IngestDeps are the ports of IngestService; every field except Clock and Log is required.
type IngestDeps struct {
	Agents AgentDirectory
	Store  Store
	GitHub GitHub
	Zip    ArtifactReader
	// Ledger appends the cost ledger entry of a run stored from an artifact, in the transaction
	// that stores the run; nil = ledger off. Tx is required whenever Ledger is set.
	Ledger Ledger
	Tx     Tx
	// Clock measures the refresh budget and stands in for an unparsable run time; nil = time.Now.
	Clock Clock
	// Log receives the per-installation failure and per-run skip lines; nil discards them.
	Log *slog.Logger
}

// IngestService implements Ingest.
type IngestService struct {
	d   IngestDeps
	now Clock
	log *slog.Logger
}

var _ Ingest = (*IngestService)(nil)

// NewIngestService builds the service. A missing dependency is a wiring bug and panics at startup.
func NewIngestService(d IngestDeps) *IngestService {
	if d.Agents == nil || d.Store == nil || d.GitHub == nil || d.Zip == nil {
		panic("ci/app: NewIngestService needs every dependency")
	}
	if d.Ledger != nil && d.Tx == nil {
		panic("ci/app: NewIngestService needs a Tx with a Ledger")
	}
	s := &IngestService{d: d, now: d.Clock, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// ListRuns returns the workspace's CI runs, newest first.
func (s *IngestService) ListRuns(ctx context.Context, workspaceID string, f RunFilter) ([]domain.Run, error) {
	runs, err := s.d.Store.ListRuns(ctx, workspaceID, f)
	if err != nil {
		return nil, fmt.Errorf("list ci runs: %w", err)
	}
	return runs, nil
}

// errBudget stops an installation (and every later one) whose next GitHub call would start after
// the refresh deadline.
var errBudget = errors.New("refresh budget exhausted")

// storeFailure marks a database error inside a refresh: it aborts the refresh (500) instead of
// becoming a GitHub error code of the installation.
type storeFailure struct{ err error }

func (e *storeFailure) Error() string { return "ci store: " + e.err.Error() }
func (e *storeFailure) Unwrap() error { return e.err }

// refresh is one Refresh call: the workspace and the deadline after which no GitHub call starts.
type refresh struct {
	s           *IngestService
	workspaceID string
	deadline    time.Time
}

// callGitHub runs one GitHub call. The deadline is checked before the call starts (spec 7.7.1) and the
// call itself is bounded by GitHubCallTimeout, so a refresh ends at most that long after the
// deadline (P11).
func callGitHub[T any](ctx context.Context, r *refresh, call func(context.Context) (T, error)) (T, error) {
	if !r.s.now().Before(r.deadline) {
		var zero T
		return zero, errBudget
	}
	ctx, cancel := context.WithTimeout(ctx, domain.GitHubCallTimeout)
	defer cancel()
	return call(ctx)
}

// Refresh reads the newest completed workflow runs of every installation with a slug and stores
// the verified results (spec 7.7).
func (s *IngestService) Refresh(ctx context.Context, workspaceID, agentID string) (domain.RefreshResult, error) {
	if agentID != "" {
		ok, err := s.d.Agents.Exists(ctx, workspaceID, agentID)
		if err != nil {
			return domain.RefreshResult{}, fmt.Errorf("check agent: %w", err)
		}
		if !ok {
			return domain.RefreshResult{}, apperr.NotFound("Agent not found").WithCode(codeAgentNotFound)
		}
	}
	if !s.d.GitHub.Configured() {
		return domain.RefreshResult{}, apperr.Config(msgNoToken)
	}
	installations, err := s.d.Store.ListInstallations(ctx, workspaceID, InstallationFilter{AgentID: agentID})
	if err != nil {
		return domain.RefreshResult{}, fmt.Errorf("list installations: %w", err)
	}

	r := &refresh{s: s, workspaceID: workspaceID, deadline: s.now().Add(domain.RefreshBudget)}
	out := domain.RefreshResult{Installations: []domain.InstallationResult{}}
	for _, inst := range installations {
		if inst.Slug == nil || *inst.Slug == "" {
			continue
		}
		res := domain.InstallationResult{
			InstallationID: inst.ID, Repo: inst.Repo, State: domain.StateOK, Skipped: []domain.SkippedRun{},
		}
		err := r.installation(ctx, inst, *inst.Slug, &res)
		switch {
		case err == nil:
		case errors.Is(err, errBudget):
			res.State = domain.StateNotChecked
		default:
			var sf *storeFailure
			if errors.As(err, &sf) {
				return domain.RefreshResult{}, fmt.Errorf("refresh %s: %w", inst.Repo, sf.err)
			}
			if ctx.Err() != nil { // the caller is gone: stop instead of failing every installation
				return domain.RefreshResult{}, fmt.Errorf("refresh %s: %w", inst.Repo, ctx.Err())
			}
			r.fail(ctx, inst, &res, err)
		}
		out.Ingested += res.Ingested
		if res.State == domain.StateError || res.State == domain.StateNotChecked {
			out.Degraded = true
		}
		out.Installations = append(out.Installations, res)
	}
	return out, nil
}

// fail records a GitHub failure on the installation: its code, never the error text.
func (r *refresh) fail(ctx context.Context, inst domain.Installation, res *domain.InstallationResult, err error) {
	status, rateLimited := 0, false
	var ge *GitHubError
	if errors.As(err, &ge) {
		status, rateLimited = ge.Status, ge.RateLimited
	}
	code := domain.GitHubErrorCode(status, rateLimited)
	res.State = domain.StateError
	res.ErrorCode = code
	attrs := []any{"installation_id", inst.ID, "repo", inst.Repo, "code", string(code)}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	r.s.log.ErrorContext(ctx, "ci refresh: installation failed", attrs...)
}

// installation walks the newest runs of one installation. A GitHub failure is returned as is, a
// database failure as *storeFailure, an exhausted budget as errBudget.
func (r *refresh) installation(ctx context.Context, inst domain.Installation, slug string, res *domain.InstallationResult) error {
	owner, name, ok := strings.Cut(inst.Repo, "/")
	if !ok || owner == "" || name == "" {
		return errors.New("malformed installation repository")
	}
	repo := RepoRef{Owner: owner, Name: name}
	workflowPath := domain.WorkflowPath(slug)

	runs, err := callGitHub(ctx, r, func(ctx context.Context) ([]domain.WorkflowRun, error) {
		return r.s.d.GitHub.ListWorkflowRuns(ctx, repo, domain.WorkflowFile(slug), domain.RunsPerInstallation)
	})
	if err != nil {
		var ge *GitHubError
		if errors.As(err, &ge) && ge.Status == 404 { // the workflow is not on the default branch yet
			res.State = domain.StateNotInstalled
			return nil
		}
		return err
	}
	if len(runs) == 0 {
		return nil
	}

	keys := make([]domain.RunKey, len(runs))
	for i, run := range runs {
		keys[i] = domain.KeyOf(run)
	}
	stored, err := r.s.d.Store.StoredRunKeys(ctx, keys)
	if err != nil {
		return &storeFailure{err: err}
	}
	for _, run := range runs {
		if !domain.Inspectable(run) || stored[domain.KeyOf(run)] {
			continue
		}
		if !domain.FromBaseRepository(run) { // a fork's copy of the workflow can forge the artifact
			r.s.log.WarnContext(ctx, "ci refresh: run not inspected",
				"installation_id", inst.ID, "repo", inst.Repo, "github_run_id", run.ID, "reason", "head_repository_differs")
			continue
		}
		out, err := r.inspect(ctx, inst, slug, workflowPath, repo, run)
		if err != nil {
			return err
		}
		if out.skip != "" {
			r.skipped(ctx, inst, res, run, out.skip)
			continue
		}
		inserted, err := r.record(ctx, out.run)
		if errors.Is(err, ErrNotStorable) {
			r.skipped(ctx, inst, res, run, domain.SkipInvalid)
			continue
		}
		if err != nil {
			return &storeFailure{err: err}
		}
		if inserted { // false: another refresh stored it first
			res.Ingested++
		}
	}
	return nil
}

// record stores the run. A run built from an artifact with the ledger on is stored together with
// its ledger entry in one transaction, so a ledger failure leaves no CI run behind and the next
// refresh retries it; a run without an artifact appends no entry (spec D9). It reports whether
// this call stored the run.
func (r *refresh) record(ctx context.Context, run domain.NewRun) (bool, error) {
	if r.s.d.Ledger == nil || !run.FromArtifact {
		rec, err := r.s.d.Store.RecordRun(ctx, run)
		return rec.Stored, err
	}
	stored := false
	err := r.s.d.Tx.InTx(ctx, func(ctx context.Context) error {
		stored = false
		rec, err := r.s.d.Store.RecordRun(ctx, run)
		if err != nil {
			return err
		}
		if !rec.Stored {
			return nil
		}
		if err := r.s.d.Ledger.RecordCIRun(ctx, ciRunOf(run, rec)); err != nil {
			return fmt.Errorf("record ci run in ledger: %w", err)
		}
		stored = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

func ciRunOf(run domain.NewRun, rec Recorded) CIRun {
	return CIRun{
		WorkspaceID: run.WorkspaceID,
		AgentRunID:  rec.AgentRunID,
		GithubRunID: run.GithubRunID,
		Attempt:     run.RunAttempt,
		OccurredAt:  run.RanAt,
		Provider:    domain.ProviderOpenRouter,
		Model:       run.Model,
		CostUSD:     run.CostUSD,
	}
}

func (r *refresh) skipped(ctx context.Context, inst domain.Installation, res *domain.InstallationResult, run domain.WorkflowRun, reason domain.SkipReason) {
	res.Skipped = append(res.Skipped, domain.SkippedRun{GithubRunID: run.ID, Reason: reason})
	r.s.log.WarnContext(ctx, "ci refresh: run skipped",
		"installation_id", inst.ID, "repo", inst.Repo, "github_run_id", run.ID, "reason", string(reason))
}

// inspected is what a run turns into: a skip reason, or the row to store.
type inspected struct {
	skip domain.SkipReason
	run  domain.NewRun
}

// inspect finds, downloads, reads and verifies the result artifact of a run (spec 7.7.3-7.7.4). A
// completed run without the artifact is a failed run: the runner crashed before writing it.
func (r *refresh) inspect(ctx context.Context, inst domain.Installation, slug, workflowPath string, repo RepoRef, run domain.WorkflowRun) (inspected, error) {
	artifacts, err := callGitHub(ctx, r, func(ctx context.Context) ([]domain.RunArtifact, error) {
		return r.s.d.GitHub.ListRunArtifacts(ctx, repo, run.ID)
	})
	if err != nil {
		return inspected{}, err
	}
	art, found := domain.NewestArtifact(artifacts, domain.ArtifactName(slug))
	if !found {
		return r.storable(domain.NewFailedRun(r.workspaceID, inst, run, r.s.now())), nil
	}
	if art.Expired {
		return inspected{skip: domain.SkipExpired}, nil
	}
	if art.SizeInBytes > domain.MaxArtifactZipBytes { // GitHub reports the size: do not download it
		return inspected{skip: domain.SkipTooLarge}, nil
	}

	zip, err := callGitHub(ctx, r, func(ctx context.Context) ([]byte, error) {
		return r.s.d.GitHub.DownloadArtifact(ctx, repo, art.ID, domain.MaxArtifactZipBytes)
	})
	if errors.Is(err, ErrArtifactTooLarge) {
		return inspected{skip: domain.SkipTooLarge}, nil
	}
	var ge *GitHubError
	if errors.As(err, &ge) && ge.Status == 410 { // an expired artifact answers its download with 410
		return inspected{skip: domain.SkipExpired}, nil
	}
	if err != nil {
		return inspected{}, err
	}
	if len(zip) > domain.MaxArtifactZipBytes {
		return inspected{skip: domain.SkipTooLarge}, nil
	}

	entry, err := r.s.d.Zip.Entry(zip, domain.ResultFileName, domain.MaxArtifactJSONBytes)
	if errors.Is(err, ErrEntryTooLarge) {
		return inspected{skip: domain.SkipTooLarge}, nil
	}
	if err != nil {
		return inspected{skip: domain.SkipInvalid}, nil
	}
	result, err := domain.ParseResultArtifact(entry)
	if err != nil {
		return inspected{skip: domain.SkipInvalid}, nil
	}
	if !domain.VerifyArtifact(result, domain.FactsOf(run), workflowPath) {
		return inspected{skip: domain.SkipMismatch}, nil
	}
	return r.storable(domain.NewRunFromArtifact(r.workspaceID, inst, run, result, r.s.now())), nil
}

// storable turns a row into inspected, or into the skip artifact_invalid when a value could not be
// held by its column. A verified artifact is still attacker-controlled data (anyone who can run
// the workflow writes it): a count past int32 or a NUL in the model must never reach the insert,
// where it would fail every refresh with a database error.
func (r *refresh) storable(n domain.NewRun) inspected {
	if !rowStorable(n) {
		return inspected{skip: domain.SkipInvalid}
	}
	return inspected{run: n}
}

// costLimitUSD is the first cost numeric(12,6) cannot hold: a value below it rounds, at six
// decimals, to at most 999999.999999.
const costLimitUSD = 999_999.9999995

// rowStorable reports whether every value of the row fits its column (ci_runs, agent_runs): int32
// columns hold non-negative values only, cost is finite, non-negative and within numeric(12,6),
// text holds valid UTF-8 without NUL (Postgres rejects NUL in text).
func rowStorable(n domain.NewRun) bool {
	ints := []int{n.RunAttempt, n.FindingsCount, n.Blockers}
	for _, p := range []*int{n.PRNumber, n.ManifestVersion, n.DurationMS} {
		if p != nil {
			ints = append(ints, *p)
		}
	}
	for _, v := range ints {
		if v < 0 || v > math.MaxInt32 {
			return false
		}
	}
	if n.GithubRunID < 0 {
		return false
	}
	if n.CostUSD != nil && (math.IsNaN(*n.CostUSD) || *n.CostUSD < 0 || *n.CostUSD >= costLimitUSD) {
		return false
	}
	texts := []string{n.HeadSHA, n.GithubURL, n.Status}
	for _, p := range []*string{n.Model, n.Error} {
		if p != nil {
			texts = append(texts, *p)
		}
	}
	for _, t := range texts {
		if !utf8.ValidString(t) || strings.ContainsRune(t, 0) {
			return false
		}
	}
	return true
}
