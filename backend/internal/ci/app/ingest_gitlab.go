package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"dev-digest-go/backend/internal/ci/domain"
)

// The GitLab part of the refresh (spec C FR31-FR36). A GitLab CI installation is walked next to the
// GitHub ones, newest installed_at first, under the same 45 s budget (callGitLab). Everything it
// needs from the outside is declared here, at its consumer: the connection lookup, the GitLab calls,
// the host-qualified run store and the ledger. The pure rules (which pipeline and job count, which
// artifact is fetched and trusted, the row it becomes) are domain/gitlabingest.go.

// Sentinels of GitLabIngestConns.Resolve.
var (
	// ErrGitLabHostNotConnected: no GitLab connection has the installation's host (FR31).
	ErrGitLabHostNotConnected = errors.New("gitlab host not connected")
	// ErrGitLabTokenMissing: the connection of the host has no token (FR31).
	ErrGitLabTokenMissing = errors.New("gitlab connection has no token")
)

// GitLabIngestConn is a GitLab connection as the ingest needs it. It carries no token: the
// adapter behind GitLabCI reads the token itself, at every call.
type GitLabIngestConn struct {
	// BaseURL is the instance URL (https://host[:port][/prefix], no /api/v4): the job web address
	// of a stored run is built from it (FR36).
	BaseURL string
}

// GitLabIngestConns finds the connection of a host key.
type GitLabIngestConns interface {
	// Resolve returns the connection whose host key is host. ErrGitLabHostNotConnected when there
	// is none, ErrGitLabTokenMissing when it has no token; any other error is a failure to read the
	// connections (the refresh fails with it).
	Resolve(ctx context.Context, host string) (GitLabIngestConn, error)
}

// GitLabCI is the part of the GitLab REST API the refresh calls (spec C FR32-FR34), over the strict
// client of platform/gitlab: the token of the ref's host connection is read per call, nothing
// follows a redirect with the token, and every failure is a *GitLabError (Step empty, its text never
// carries a token, a signed storage URL or a response body). A host without a usable token at call
// time is a *GitLabError with Status 401.
type GitLabCI interface {
	// Pipelines lists the project's pipelines of one source, newest id first; page is 1-based.
	// A missing project is a *GitLabError with Status 404.
	Pipelines(ctx context.Context, ref GitLabRef, source string, page, perPage int) ([]domain.GitLabPipeline, error)
	// PipelineJobs lists the first page (domain.GitLabJobsPerPipeline) of a pipeline's jobs whose
	// status is one of scopes.
	PipelineJobs(ctx context.Context, ref GitLabRef, pipelineID int64, scopes ...string) ([]domain.GitLabJob, error)
	// MergeRequestSourceProject returns the source_project_id of a merge request; 0 when GitLab
	// names none. A missing merge request is a *GitLabError with Status 404.
	MergeRequestSourceProject(ctx context.Context, ref GitLabRef, iid int) (int64, error)
	// JobArtifactFile returns the bytes of one file of a job's artifacts, at most limit bytes (read
	// with a cap of limit, so a longer body returns limit bytes). The storage redirect is followed
	// once, without credentials; any other redirect is an error with Status 0. A missing file is a
	// *GitLabError with Status 404.
	JobArtifactFile(ctx context.Context, ref GitLabRef, jobID int64, file string, limit int) ([]byte, error)
}

// GitLabIngestStore is the host-qualified part of the store the refresh needs. Every method joins
// the transaction of its ctx when there is one; failures are database errors.
type GitLabIngestStore interface {
	// ListGitLabInstallations returns the workspace's gitlab_ci installations (of the agent, when
	// f.AgentID is set), newest installed_at first, each with its newest run and with TargetType,
	// Host (the connection's host key) and ProjectID set. Repo is the stored project path.
	ListGitLabInstallations(ctx context.Context, workspaceID string, f InstallationFilter) ([]domain.Installation, error)
	// StoredGitLabRunKeys returns which of keys (GitLab job id, attempt) are already stored for
	// host.
	StoredGitLabRunKeys(ctx context.Context, host string, keys []domain.RunKey) (map[domain.RunKey]bool, error)
	// RecordGitLabRun writes the ci_runs row (host = run.Host, source = run.Source) and its
	// agent_runs row in one transaction, ON CONFLICT (host, github_run_id, run_attempt) DO NOTHING;
	// Stored is false when another refresh stored the run first. ErrNotStorable (checked before
	// anything is written) when a value cannot be held.
	RecordGitLabRun(ctx context.Context, run domain.GitLabNewRun) (Recorded, error)
}

// GitLabCIRun is the ledger entry of a stored GitLab run. Host is the host key of the connection:
// the ledger builds the dedupe key ci:<host>:<job id>:<attempt> from it (SCR1), never this package.
type GitLabCIRun struct {
	CIRun
	Host string
}

// GitLabLedger appends the cost ledger entry of a GitLab CI run, with the contract of Ledger.
type GitLabLedger interface {
	RecordGitLabCIRun(ctx context.Context, run GitLabCIRun) error
}

// GitLabIngestDeps are the ports of the GitLab refresh. The zero value switches it off: GitLab
// installations are then not walked. Otherwise Conns, CI and Store are required; Ledger is optional
// and needs IngestDeps.Tx.
type GitLabIngestDeps struct {
	Conns  GitLabIngestConns
	CI     GitLabCI
	Store  GitLabIngestStore
	Ledger GitLabLedger
}

func (d GitLabIngestDeps) enabled() bool { return d.Conns != nil }

func (d GitLabIngestDeps) validate() {
	if d.Conns == nil && d.CI == nil && d.Store == nil && d.Ledger == nil {
		return
	}
	if d.Conns == nil || d.CI == nil || d.Store == nil {
		panic("ci/app: NewIngestService needs Conns, CI and Store for the GitLab ingest")
	}
}

// refreshTarget is one installation of a refresh: a GitHub one or, with gitlab set, a GitLab one.
type refreshTarget struct {
	inst   domain.Installation
	gitlab bool
}

// refreshTargets lists the installations a refresh walks, newest installed_at first: those with a
// slug. A GitLab row that a store lists among the GitHub ones is left to the GitLab list.
func refreshTargets(gh, gl []domain.Installation) []refreshTarget {
	targets := make([]refreshTarget, 0, len(gh)+len(gl))
	for _, inst := range gh {
		if inst.TargetType == domain.TargetGitLabCI || inst.Slug == nil || *inst.Slug == "" {
			continue
		}
		targets = append(targets, refreshTarget{inst: inst})
	}
	for _, inst := range gl {
		if inst.Slug == nil || *inst.Slug == "" {
			continue
		}
		targets = append(targets, refreshTarget{inst: inst, gitlab: true})
	}
	if len(gl) > 0 { // each list is sorted already: a stable merge keeps GitHub first on a tie
		slices.SortStableFunc(targets, func(a, b refreshTarget) int {
			return b.inst.InstalledAt.Compare(a.inst.InstalledAt)
		})
	}
	return targets
}

// hasGitHub reports whether any target is a GitHub installation.
func hasGitHub(targets []refreshTarget) bool {
	return slices.ContainsFunc(targets, func(t refreshTarget) bool { return !t.gitlab })
}

// callGitLab runs one GitLab call under the refresh deadline and the per-call bound, as callGitHub.
func callGitLab[T any](ctx context.Context, r *refresh, call func(context.Context) (T, error)) (T, error) {
	return callGitHub(ctx, r, call)
}

// mrKey names a merge request in the per-refresh cache of source projects (A7).
type mrKey struct {
	host      string
	projectID int64
	iid       int
}

// failGitLab records a GitLab failure on the installation: its code, never the error text.
func (r *refresh) failGitLab(ctx context.Context, inst domain.Installation, res *domain.InstallationResult, err error) {
	status := 0
	var code domain.ErrorCode
	switch {
	case errors.Is(err, ErrGitLabHostNotConnected):
		code = domain.CodeRepoHostNotConnected
	case errors.Is(err, ErrGitLabTokenMissing):
		code = domain.CodeGitLabTokenMissing
	default:
		var ce *GitLabError
		if errors.As(err, &ce) {
			status = ce.Status
		}
		code = domain.GitLabErrorCode(status)
	}
	res.State = domain.StateError
	res.ErrorCode = code
	attrs := []any{"installation_id", inst.ID, "repo", inst.Repo, "code", string(code)}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	r.s.log.ErrorContext(ctx, "ci refresh: installation failed", attrs...)
}

// gitlabInstallation walks the newest merge request pipelines of one GitLab installation (FR32). A
// GitLab failure is returned as is, a database failure as *storeFailure, an exhausted budget as
// errBudget; the two connection sentinels are failures of the installation too.
func (r *refresh) gitlabInstallation(ctx context.Context, gi domain.Installation, res *domain.InstallationResult) error {
	d := r.s.d.GitLab
	conn, err := d.Conns.Resolve(ctx, gi.Host)
	switch {
	case errors.Is(err, ErrGitLabHostNotConnected), errors.Is(err, ErrGitLabTokenMissing):
		return err
	case err != nil:
		return &storeFailure{err: fmt.Errorf("resolve gitlab connection: %w", err)}
	}
	if gi.Host == "" || gi.ProjectID <= 0 {
		return errors.New("malformed installation project")
	}
	ref := GitLabRef{Host: gi.Host, ProjectID: gi.ProjectID}

	pipelines, err := callGitLab(ctx, r, func(ctx context.Context) ([]domain.GitLabPipeline, error) {
		return d.CI.Pipelines(ctx, ref, domain.GitLabMergeRequestSource, 1, domain.GitLabPipelinesPerInstallation)
	})
	if err != nil {
		var ce *GitLabError
		if errors.As(err, &ce) && ce.Status == 404 { // no such project, or no access to its pipelines
			res.State = domain.StateNotInstalled
			return nil
		}
		return err
	}
	for _, p := range pipelines {
		if p.ProjectID != gi.ProjectID || p.Source != domain.GitLabMergeRequestSource {
			continue
		}
		if _, ok := domain.MRIIDFromRef(p.Ref); !ok {
			continue
		}
		jobs, err := callGitLab(ctx, r, func(ctx context.Context) ([]domain.GitLabJob, error) {
			return d.CI.PipelineJobs(ctx, ref, p.ID, "success", "failed")
		})
		if err != nil {
			return err
		}
		cand, ok := domain.SelectGitLabCandidate(p, jobs, gi.ProjectID, *gi.Slug)
		if !ok {
			continue
		}
		if err := r.gitlabCandidate(ctx, gi, conn, cand, res); err != nil {
			return err
		}
	}
	return nil
}

// gitlabCandidate stores, skips or ignores one candidate job (FR33-FR36).
func (r *refresh) gitlabCandidate(ctx context.Context, gi domain.Installation, conn GitLabIngestConn, cand domain.GitLabCandidate, res *domain.InstallationResult) error {
	d := r.s.d.GitLab
	key := domain.GitLabKeyOf(cand.Job)
	stored, err := d.Store.StoredGitLabRunKeys(ctx, gi.Host, []domain.RunKey{key})
	if err != nil {
		return &storeFailure{err: err}
	}
	if stored[key] {
		return nil
	}

	source, err := r.mergeRequestSource(ctx, gi, cand.MRIID)
	if err != nil {
		return err
	}
	if !domain.GitLabFromBaseProject(source, gi.ProjectID) { // a fork's job definition can forge the artifact
		r.s.log.WarnContext(ctx, "ci refresh: run not inspected",
			"installation_id", gi.ID, "repo", gi.Repo, "github_run_id", cand.Job.ID, "reason", domain.ReasonSourceProjectDiffers)
		return nil
	}

	out, err := r.gitlabInspect(ctx, gi, conn, cand)
	if err != nil {
		return err
	}
	if out.skip != "" {
		r.gitlabSkipped(ctx, gi, res, cand.Job.ID, out.skip)
		return nil
	}
	inserted, err := r.recordGitLab(ctx, out.run, gi.Provider)
	if errors.Is(err, ErrNotStorable) {
		r.gitlabSkipped(ctx, gi, res, cand.Job.ID, domain.SkipInvalid)
		return nil
	}
	if err != nil {
		return &storeFailure{err: err}
	}
	if inserted { // false: another refresh stored it first
		res.Ingested++
	}
	return nil
}

// mergeRequestSource is the source project of the merge request, read once per refresh (A7). A
// merge request GitLab no longer knows (404) has none (0), which GitLabFromBaseProject refuses.
func (r *refresh) mergeRequestSource(ctx context.Context, gi domain.Installation, iid int) (int64, error) {
	k := mrKey{host: gi.Host, projectID: gi.ProjectID, iid: iid}
	if src, ok := r.mrSource[k]; ok {
		return src, nil
	}
	src, err := callGitLab(ctx, r, func(ctx context.Context) (int64, error) {
		return r.s.d.GitLab.CI.MergeRequestSourceProject(ctx, GitLabRef{Host: gi.Host, ProjectID: gi.ProjectID}, iid)
	})
	if err != nil {
		var ce *GitLabError
		if !errors.As(err, &ce) || ce.Status != 404 {
			return 0, err
		}
		src = 0
	}
	if r.mrSource == nil {
		r.mrSource = map[mrKey]int64{}
	}
	r.mrSource[k] = src
	return src, nil
}

// glInspected is what a candidate job turns into: a skip reason, or the row to store.
type glInspected struct {
	skip domain.SkipReason
	run  domain.GitLabNewRun
}

// gitlabInspect decides what happens to the job's artifacts and builds the row (FR34, FR35). A
// finished job without artifacts is a failed run: the runner crashed before writing its result.
func (r *refresh) gitlabInspect(ctx context.Context, gi domain.Installation, conn GitLabIngestConn, cand domain.GitLabCandidate) (glInspected, error) {
	in := domain.GitLabRunInput{
		WorkspaceID: r.workspaceID, Installation: gi, Host: gi.Host, BaseURL: conn.BaseURL,
		Pipeline: cand.Pipeline, Job: cand.Job, MRIID: cand.MRIID, Now: r.s.now(),
	}
	plan := domain.PlanGitLabArtifact(cand.Job, in.Now)
	switch plan.Action {
	case domain.GitLabArtifactSkip:
		return glInspected{skip: plan.Skip}, nil
	case domain.GitLabArtifactNone:
		return r.glStorable(domain.NewGitLabRun(in)), nil
	}

	body, err := callGitLab(ctx, r, func(ctx context.Context) ([]byte, error) {
		return r.s.d.GitLab.CI.JobArtifactFile(ctx, GitLabRef{Host: gi.Host, ProjectID: gi.ProjectID}, cand.Job.ID, domain.ResultFileName, domain.GitLabResultReadCap)
	})
	if err != nil {
		var ce *GitLabError
		if errors.As(err, &ce) && ce.Status == 404 { // the archive has no result file, or it expired
			return glInspected{skip: domain.SkipInvalid}, nil
		}
		return glInspected{}, err
	}
	if domain.GitLabResultTooLarge(body) {
		return glInspected{skip: domain.SkipTooLarge}, nil
	}
	result, err := domain.ParseResultArtifact(body)
	if err != nil {
		return glInspected{skip: domain.SkipInvalid}, nil
	}
	if !domain.VerifyGitLabArtifact(result, cand.Pipeline, cand.Job, gi.ProjectID, *gi.Slug) {
		return glInspected{skip: domain.SkipMismatch}, nil
	}
	in.Artifact = &result
	return r.glStorable(domain.NewGitLabRun(in)), nil
}

// glStorable is storable for a GitLab row: a value its column cannot hold is the skip
// artifact_invalid, the artifact being attacker-controlled data even when verified.
func (r *refresh) glStorable(n domain.GitLabNewRun) glInspected {
	if !rowStorable(n.NewRun) {
		return glInspected{skip: domain.SkipInvalid}
	}
	return glInspected{run: n}
}

// recordGitLab is record for a GitLab run: the run of a verified artifact is stored together with
// its ledger entry in one transaction, a failed run without artifact appends none (spec D9). The
// entry carries the host key, so the ledger's dedupe key is host-qualified (SCR1).
func (r *refresh) recordGitLab(ctx context.Context, run domain.GitLabNewRun, instProvider string) (bool, error) {
	d := r.s.d.GitLab
	if d.Ledger == nil || !run.FromArtifact {
		rec, err := d.Store.RecordGitLabRun(ctx, run)
		return rec.Stored, err
	}
	stored := false
	err := r.s.d.Tx.InTx(ctx, func(ctx context.Context) error {
		stored = false
		rec, err := d.Store.RecordGitLabRun(ctx, run)
		if err != nil {
			return err
		}
		if !rec.Stored {
			return nil
		}
		entry := GitLabCIRun{CIRun: ciRunOf(run.NewRun, rec, instProvider), Host: run.Host}
		if err := d.Ledger.RecordGitLabCIRun(ctx, entry); err != nil {
			return fmt.Errorf("record gitlab ci run in ledger: %w", err)
		}
		stored = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

func (r *refresh) gitlabSkipped(ctx context.Context, gi domain.Installation, res *domain.InstallationResult, jobID int64, reason domain.SkipReason) {
	res.Skipped = append(res.Skipped, domain.SkippedRun{GithubRunID: jobID, Reason: reason})
	r.s.log.WarnContext(ctx, "ci refresh: run skipped",
		"installation_id", gi.ID, "repo", gi.Repo, "github_run_id", jobID, "reason", string(reason))
}
