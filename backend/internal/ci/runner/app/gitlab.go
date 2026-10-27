package app

// GitLab mode of the runner (spec FR16-FR30, FR38, FR48): the same manifest, provider, review
// engine, gate and artifact as GitHub mode; the merge request comes from the CI variables, the
// diff from the job's checkout, and the review is posted as draft notes published in one batch (or
// as one note). Every body sent to GitLab passes the note body rules of slice B (notebody.Apply)
// once, on the assembled body.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/runner/domain"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/gitlab/notebody"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// gitLabTokenEnv is the variable of the posting token.
const gitLabTokenEnv = "DEVDIGEST_GITLAB_TOKEN"

// glRunAttempt is the run_attempt of a GitLab job (FR22): a retried job is a new job with a new id.
const glRunAttempt = 1

// cleanupTimeout bounds the best-effort deletion of unpublished drafts after a failure; it runs on
// a context detached from the run's, so a cancelled job still tidies up.
const cleanupTimeout = 60 * time.Second

// glSkipLine is printed when the merge request moved on since the pipeline started (FR25).
const glSkipLine = "[agent-runner] inline comments skipped: the merge request changed since this pipeline started"

// unusableGitLab is implemented by a GitLab port whose client could not be built; Unusable returns
// the reason, nil when the port works. The composition root hands such a port instead of failing
// itself so that the failure goes through the run's own failure line (redaction, job-log safety).
type unusableGitLab interface{ Unusable() error }

// glRun is what run has built before GitLab mode takes over.
type glRun struct {
	engine     Engine
	now        func() time.Time
	manifest   Manifest
	llm        reviewerapp.LLM
	skills     []string
	resultPath string
}

// runGitLab is run for a GitLab merge request pipeline: environment (FR16-FR19), diff (FR20),
// review (FR21), artifact (FR22), posting (FR24-FR28).
func runGitLab(ctx context.Context, cfg Config, deps Deps, in glRun) (result, error) {
	env := deps.Env
	m := in.manifest

	// 1. The merge request, from the environment: mode, fork guard, token and API URL are
	// checked before the diff, the model or GitLab are touched.
	glEnv, err := domain.ParseGitLabEnv(env.Lookup)
	if err != nil {
		return result{}, err
	}
	if glEnv.PostAs != postNone && deps.GitLab == nil {
		return result{}, domain.NewRunnerError("This runner cannot call GitLab")
	}
	if deps.GitDiff == nil {
		return result{}, domain.NewRunnerError("This runner cannot compute the merge request diff")
	}
	// A GitLab client the composition root could not build (an unreadable CI_SERVER_TLS_CA_FILE, an
	// API URL the client refuses) is an environment failure like FR19's: it fails here, before the
	// diff and the paid model call, with the reason (it names a variable, never a value).
	if u, ok := deps.GitLab.(unusableGitLab); ok && glEnv.PostAs != postNone {
		if err := u.Unusable(); err != nil {
			return result{}, domain.NewRunnerError(err.Error())
		}
	}

	skillList := "none"
	if len(m.Skills) > 0 {
		skillList = strings.Join(m.Skills, ",")
	}
	deps.Out.Stdout(gitLabLine(env, "[agent-runner] trace manifest_version="+versionText(m.Version)+" model="+m.Model+
		" skills="+skillList+" commit="+glEnv.CommitSHA+" runner="+domain.RunnerVersion+" build="+cfg.BuildID))

	// 2. The diff from the checkout, without DevDigest's own files.
	rawDiff, err := deps.GitDiff.Diff(ctx, glEnv.BaseSHA, glEnv.HeadSHA())
	if err != nil {
		var missing interface{ CommitMissing() string }
		if errors.As(err, &missing) {
			return result{}, err // the FR20 message, as it is
		}
		return result{}, domain.NewRunnerError("Could not compute the merge request diff: " + err.Error())
	}
	rawDiff = domain.StripGitLabIgnored(rawDiff)
	diff := domain.ParseDiff(rawDiff)

	// 3. The one review engine. The model's own verdict is discarded below.
	start := in.now()
	outcome, err := reviewWith(ctx, in.engine, in.llm, m, in.skills, diff, glEnv.PRDescription(),
		"Review PR #"+strconv.FormatInt(glEnv.MRIID, 10)+": "+glEnv.Title)
	if err != nil {
		return result{}, err
	}
	durationMS := in.now().Sub(start).Milliseconds()

	// 4. The gate and the review follow the grounded findings and ci_fail_on.
	findings := outcome.Review.Findings
	payload := domain.ToReviewPayload(findings, m.CIFailOn, diff, m.Name)
	blockers := domain.CountBlockers(findings, m.CIFailOn)
	triggered := domain.GateTriggered(findings, m.CIFailOn)

	// 5. The artifact is written before anything is posted (FR22).
	commit, projectID, jobID, attempt := glEnv.CommitSHA, glEnv.ProjectID, glEnv.JobID, int64(glRunAttempt)
	artifact, err := domain.BuildArtifact(domain.ArtifactInput{
		Findings: findings, CostUSD: outcome.Usage.CostUSD, DurationMS: durationMS,
		Agent: m.Name, PRNumber: int(glEnv.MRIID), CommitSHA: &commit,
		Repository: glEnv.ProjectPath, RepositoryID: &projectID, RunID: &jobID, RunAttempt: &attempt,
		Model: m.Model, ManifestVersion: artifactVersion(m.Version),
		Skills: m.Skills, Blockers: blockers,
	})
	if err != nil {
		return result{}, err
	}
	if err := deps.Files.WriteFile(in.resultPath, []byte(artifact.JSON())); err != nil {
		return result{}, err
	}

	// 6. Post as configured (FR24).
	p := glPoster{
		gl: deps.GitLab, out: deps.Out, env: env, token: glEnv.Token,
		ref:  MRRef{ProjectID: strconv.FormatInt(glEnv.ProjectID, 10), IID: int(glEnv.MRIID)},
		head: glEnv.HeadSHA(),
	}
	switch glEnv.PostAs {
	case postGitHubReview:
		cands := inlineCandidates(findings, m, diff)
		if err := p.postReview(ctx, payload.Body, cands, domain.PositionIndex(rawDiff)); err != nil {
			return result{}, err
		}
	case postPRComment:
		if err := p.gl.CreateNote(ctx, p.token, p.ref, p.safeBody(payload.Body)); err != nil {
			return result{}, err
		}
	}
	return result{findings: len(findings), blockers: blockers, triggered: triggered, posted: glEnv.PostAs}, nil
}

// inlineCandidates pairs every inline comment of the review payload with the finding it comes
// from (the cap orders and lists by the finding). The payload's own comment list does not say which
// finding a comment belongs to, so each finding is run through the payload alone, on the part of
// the diff that holds its file: the comment is the same one the whole payload holds, in engine
// order.
func inlineCandidates(findings []reviewer.Finding, m Manifest, diff reviewer.Diff) []domain.InlineCandidate {
	var out []domain.InlineCandidate
	for _, f := range findings {
		one := domain.ToReviewPayload([]reviewer.Finding{f}, m.CIFailOn, fileOnly(diff, f.File), m.Name)
		if len(one.Comments) == 1 {
			out = append(out, domain.InlineCandidate{Finding: f, Comment: one.Comments[0]})
		}
	}
	return out
}

// fileOnly is the diff reduced to the sections of one path.
func fileOnly(d reviewer.Diff, path string) reviewer.Diff {
	var files []reviewer.DiffFile
	for _, f := range d.Files {
		if f.Path == path {
			files = append(files, f)
		}
	}
	return reviewer.Diff{Files: files}
}

// glPoster posts to one merge request with one token.
type glPoster struct {
	gl    GitLab
	out   Out
	env   Env
	token string
	ref   MRRef
	// head is the commit the review ran on; inline threads are created only while the merge
	// request is still at it.
	head string
}

// safeBody is the body that is sent to GitLab: secrets removed (FR30), then the note body rules
// once on the whole assembled body (FR38, NFR3). Nothing is added to it afterwards.
func (p glPoster) safeBody(body string) string {
	return notebody.Apply(redactGitLab(p.env, body))
}

// postReview posts the MR review (FR25-FR27): a summary draft and one positioned draft per
// selected inline comment, published as one batch. On a failure after the first draft the drafts
// of this run that are still unpublished are deleted.
func (p glPoster) postReview(ctx context.Context, summary string, cands []domain.InlineCandidate, positions domain.DiffPositions) error {
	mr, err := p.gl.MR(ctx, p.token, p.ref)
	if err != nil {
		return err
	}
	existing, err := p.gl.ListDrafts(ctx, p.token, p.ref)
	if err != nil {
		return err
	}
	foreign := len(existing) > 0

	refs := mr.DiffRefs
	threads := refs != nil && refs.HeadSHA == p.head
	if !threads {
		p.out.Stdout(glSkipLine)
		cands = nil
	}
	selected, rest := domain.SelectInline(cands, domain.MaxInlineThreads)
	capped := len(selected) > 0 && len(rest) > 0

	summaryBody := summary
	if capped {
		summaryBody = domain.InsertCapSection(summary, domain.CapSection(rest))
	}

	var ids []int // the drafts of this run, summary first, in the order they are published
	create := func(body string, pos *DraftPosition) error {
		d, err := p.gl.CreateDraft(ctx, p.token, p.ref, NewDraft{Note: p.safeBody(body), Position: pos})
		if err != nil {
			return err
		}
		ids = append(ids, d.ID)
		return nil
	}

	if err := create(summaryBody, nil); err != nil {
		return p.fail(ctx, ids, err)
	}
	leftover := false // a draft of this run that could not be deleted: bulk publish would publish it
	for _, c := range selected {
		oldPath, oldLine := positions.At(c.Comment.Path, c.Comment.Line)
		err := create(c.Comment.Body, &DraftPosition{
			Refs: *refs, NewPath: c.Comment.Path, OldPath: oldPath, NewLine: c.Comment.Line, OldLine: oldLine,
		})
		if err == nil {
			continue
		}
		var apiErr *GitLabAPIError
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			return p.fail(ctx, ids, err)
		}
		// GitLab refuses a position: delete this run's drafts and post the summary alone, once.
		capped = false
		leftover = p.deleteDrafts(ctx, ids)
		ids = nil
		if err := create(summary, nil); err != nil {
			return p.fail(ctx, ids, err)
		}
		break
	}

	if !foreign && !leftover {
		if err := p.gl.BulkPublish(ctx, p.token, p.ref); err != nil {
			return p.fail(ctx, ids, err)
		}
	} else {
		// Drafts the token user wrote elsewhere stay unpublished: only this run's are published.
		for i, id := range ids {
			if err := p.gl.PublishDraft(ctx, p.token, p.ref, id); err != nil {
				return p.fail(ctx, ids[i:], err)
			}
		}
	}
	if capped {
		p.out.Stdout(domain.CapLogLine(len(selected), len(rest)))
	}
	return nil
}

// fail cleans up the unpublished drafts of this run and returns err as the posting failure.
func (p glPoster) fail(ctx context.Context, unpublished []int, err error) error {
	p.deleteDrafts(ctx, unpublished)
	return err
}

// deleteDrafts deletes drafts best effort and reports whether one could not be deleted.
func (p glPoster) deleteDrafts(ctx context.Context, ids []int) (failed bool) {
	if len(ids) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	for _, id := range ids {
		if err := p.gl.DeleteDraft(ctx, p.token, p.ref, id); err != nil {
			failed = true
		}
	}
	return failed
}

// The job log of a GitLab job (FR29, FR30, FR23): secrets are redacted first, then text from
// outside is made safe, then the FAILED line is cut to its cap.

// gitLabLine is text as one job-log line of GitLab mode: no secret of §7.5, no control character,
// no line break, no section marker.
func gitLabLine(env Env, text string) string {
	return domain.GitLabLogText(redactGitLab(env, text))
}

// gitLabFailureLine is the failure line of GitLab mode, cut to MaxFailureBytes after redaction.
func gitLabFailureLine(env Env, text string) string {
	return cutBytes(failurePrefix+gitLabLine(env, text), MaxFailureBytes)
}

// gitLabSecretNames are the variables of §7.5 whose values never reach a log line, an error text
// or a posted body (FR30, A14): the GitLab token and every provider secret.
func gitLabSecretNames() []string {
	names := []string{gitLabTokenEnv, cidomain.SecretOpenRouter, cidomain.SecretOpenAI, cidomain.SecretAnthropic, envLLMAPIKey}
	for n := 1; n <= custom.MaxHeaders; n++ {
		names = append(names, envLLMHeader+strconv.Itoa(n))
	}
	return names
}

// RedactGitLabSecrets is redactGitLab for the composition root: the GitLab API adapter applies it
// to an error body before cutting it (FR30, AC29).
func RedactGitLabSecrets(env Env, s string) string { return redactGitLab(env, s) }

// redactGitLab replaces the secret values of §7.5 in s. The GitLab token is replaced whatever its
// length; the other secrets only from minRedactLen bytes on, like in GitHub mode.
func redactGitLab(env Env, s string) string {
	noToken := false
	if v, _ := env.Lookup("DEVDIGEST_POST_AS"); v == postNone {
		noToken = true // post_as none reads no GitLab token (FR19); nothing posts it
	}
	for _, key := range gitLabSecretNames() {
		if noToken && key == gitLabTokenEnv {
			continue
		}
		v, _ := env.Lookup(key)
		if v == "" || (key != gitLabTokenEnv && len(v) < minRedactLen) {
			continue
		}
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return s
}
