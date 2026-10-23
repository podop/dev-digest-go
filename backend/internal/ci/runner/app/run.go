package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
)

// RunFunc is satisfied by Run.
var _ RunFunc = Run

// The posting modes of DEVDIGEST_POST_AS.
const (
	postGitHubReview = "github_review"
	postPRComment    = "pr_comment"
	postNone         = "none"
)

// maxEventBytes caps the GitHub event payload (best-effort input: a larger or unreadable one
// counts as absent, like Node's swallowed read error).
const maxEventBytes = 16 << 20

// minRedactLen is the shortest secret value the failure line scrubs: a real token or key is far
// longer, and replacing a one-letter value would only garble the message.
const minRedactLen = 8

// Run is the runner (spec 7.8): it loads the manifest and skills, resolves the pull request,
// fetches and strips its diff, runs the review engine, writes the artifact and posts the review.
// A failure before the artifact is written leaves no artifact and posts nothing; a posting
// failure keeps the artifact. It returns ExitOK, or ExitFailed on any failure and when the gate
// triggered.
func Run(ctx context.Context, cfg Config, deps Deps) int {
	res, err := run(ctx, cfg, deps)
	if err != nil {
		deps.Out.Stderr("[agent-runner] FAILED: " + oneLine(redact(deps.Env, err.Error())))
		return ExitFailed
	}
	deps.Out.Stdout(fmt.Sprintf("[agent-runner] findings=%d blockers=%d gateTriggered=%t posted=%s",
		res.findings, res.blockers, res.triggered, res.posted))
	if res.triggered {
		return ExitFailed
	}
	return ExitOK
}

type result struct {
	findings, blockers int
	triggered          bool
	posted             string
}

func run(ctx context.Context, cfg Config, deps Deps) (result, error) {
	engine := deps.Engine
	if engine == nil {
		engine = reviewerapp.Run
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	env := deps.Env
	get := func(key string) string { v, _ := env.Lookup(key); return v }

	dir, ok := env.Lookup("DEVDIGEST_DIR")
	if !ok {
		dir = path.Join(env.Cwd(), ".devdigest")
	}
	resultPath, ok := env.Lookup("DEVDIGEST_RESULT_PATH")
	if !ok {
		resultPath = path.Join(env.Cwd(), "devdigest-result.json")
	}
	postAs := get("DEVDIGEST_POST_AS")
	if postAs != postPRComment && postAs != postNone {
		postAs = postGitHubReview
	}

	// 1. The manifest and the skills it names, before they are used for anything.
	manifest, err := LoadManifest(deps.Files, dir, get("DEVDIGEST_AGENT"))
	if err != nil {
		return result{}, err
	}
	skills, err := LoadSkillBodies(deps.Files, dir, manifest.Skills)
	if err != nil {
		return result{}, err
	}

	// 2. The pull request, from the environment and the event payload (read only when the
	// repository is usable, as Node does).
	repository := envValue(env, "GITHUB_REPOSITORY")
	prNumber := envValue(env, "PR_NUMBER")
	var event []byte
	if repository.Set && strings.Contains(repository.Value, "/") {
		event = readEvent(deps.Files, get("GITHUB_EVENT_PATH"))
	}
	prc, err := domain.ResolvePRContext(repository, prNumber, event)
	if err != nil {
		return result{}, err
	}

	commit := get("PR_HEAD_SHA")
	skillList := "none"
	if len(manifest.Skills) > 0 {
		skillList = strings.Join(manifest.Skills, ",")
	}
	commitText := commit
	if commit == "" {
		commitText = "unknown"
	}
	deps.Out.Stdout(oneLine(fmt.Sprintf("[agent-runner] trace manifest_version=%s model=%s skills=%s commit=%s runner=%s build=%s",
		versionText(manifest.Version), manifest.Model, skillList, commitText, domain.RunnerVersion, cfg.BuildID)))

	token := get("GITHUB_TOKEN")
	if postAs != postNone && token == "" {
		return result{}, domain.NewRunnerError("GITHUB_TOKEN is required to post as '" + postAs + "'")
	}

	// 3. The diff, without DevDigest's own files.
	pr := PullRef{Owner: prc.Owner, Repo: prc.Repo, Number: prc.Number}
	rawDiff, err := deps.GitHub.FetchDiff(ctx, token, pr)
	if err != nil {
		return result{}, err
	}
	diff := domain.ParseDiff(domain.StripIgnoredFiles(rawDiff))

	// 4. The one review engine. The model's own verdict is discarded below.
	var skillInput []string
	if len(skills) > 0 {
		skillInput = skills
	}
	start := now()
	outcome, err := engine(ctx, reviewerapp.Input{
		SystemPrompt:  manifest.SystemPrompt,
		Diff:          diff,
		Strategy:      manifest.Strategy,
		Skills:        skillInput,
		PRDescription: prc.Body,
		Task:          "Review PR #" + strconv.Itoa(prc.Number) + ": " + prc.Title,
	}, deps.LLM.For(manifest.Model), nil, reviewerapp.Options{})
	if err != nil {
		return result{}, err
	}
	durationMS := now().Sub(start).Milliseconds()

	// 5. The gate and the review follow the grounded findings and ci_fail_on.
	findings := outcome.Review.Findings
	payload := domain.ToReviewPayload(findings, manifest.CIFailOn, diff, manifest.Name)
	blockers := domain.CountBlockers(findings, manifest.CIFailOn)
	triggered := domain.GateTriggered(findings, manifest.CIFailOn)

	// 6. The artifact is written before anything is posted, so a posting failure never loses the
	// computed result.
	var commitSHA *string
	if commit != "" {
		commitSHA = &commit
	}
	artifact, err := domain.BuildArtifact(domain.ArtifactInput{
		Findings: findings, CostUSD: outcome.Usage.CostUSD, DurationMS: durationMS,
		Agent: manifest.Name, PRNumber: prc.Number, CommitSHA: commitSHA,
		Repository:   prc.Owner + "/" + prc.Repo,
		RepositoryID: domain.IntOrNull(get("GITHUB_REPOSITORY_ID")),
		RunID:        domain.IntOrNull(get("GITHUB_RUN_ID")),
		RunAttempt:   domain.IntOrNull(get("GITHUB_RUN_ATTEMPT")),
		Model:        manifest.Model, ManifestVersion: artifactVersion(manifest.Version),
		Skills: manifest.Skills, Blockers: blockers,
	})
	if err != nil {
		return result{}, err
	}
	if err := deps.Files.WriteFile(resultPath, []byte(artifact.JSON())); err != nil {
		return result{}, err
	}

	// 7. Post as configured.
	switch postAs {
	case postGitHubReview:
		if err := PostReview(ctx, deps.GitHub, token, pr, payload); err != nil {
			return result{}, err
		}
	case postPRComment:
		if err := deps.GitHub.PostComment(ctx, token, pr, payload.Body); err != nil {
			return result{}, err
		}
	}
	return result{findings: len(findings), blockers: blockers, triggered: triggered, posted: postAs}, nil
}

// PostReview posts the review. GitHub's Actions token may not approve a pull request (422), so
// APPROVE is sent as COMMENT (the body still says "Approved"). GitHub also rejects the whole
// review with 422 when one inline comment targets a diff it cannot resolve; the review is then
// posted once more without comments (every finding is in the body).
func PostReview(ctx context.Context, gh GitHub, token string, pr PullRef, p domain.ReviewPayload) error {
	event := p.Event
	if event == domain.EventApprove {
		event = domain.EventComment
	}
	post := ReviewPost{Body: p.Body, Event: event}
	for _, c := range p.Comments {
		post.Comments = append(post.Comments, InlineComment{Path: c.Path, Line: c.Line, Body: c.Body})
	}
	err := gh.PostReview(ctx, token, pr, post)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 422 && len(post.Comments) > 0 {
		post.Comments = nil
		err = gh.PostReview(ctx, token, pr, post)
	}
	return err
}

// readEvent reads the event payload best-effort: nil when the variable is unset or empty, the
// file is missing, unreadable or larger than the cap.
func readEvent(files Files, eventPath string) []byte {
	if eventPath == "" {
		return nil
	}
	raw, err := files.ReadFile(eventPath, maxEventBytes)
	if err != nil {
		return nil
	}
	return raw
}

func envValue(env Env, key string) domain.EnvValue {
	v, ok := env.Lookup(key)
	return domain.EnvValue{Value: v, Set: ok}
}

// redact removes the two secrets of the environment from a failure message. The clients scrub
// their own errors; this is the last line of defence for the job log.
func redact(env Env, s string) string {
	for _, key := range []string{"OPENROUTER_API_KEY", "GITHUB_TOKEN"} {
		if v, _ := env.Lookup(key); len(v) >= minRedactLen {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	return s
}

// oneLine escapes line breaks so a value from the repository cannot start a second log line (the
// Actions runner reads workflow commands at the start of a line).
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(s)
}
