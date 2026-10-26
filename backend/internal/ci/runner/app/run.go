package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/runner/domain"
	"dev-digest-go/backend/internal/llm/custom"
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
		deps.Out.Stderr(FailureLine(redact(deps.Env, err.Error())))
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

	// 1b. The environment of the manifest's provider (A9): checked now, before the skills, the pull
	// request or any GitHub call, so a bad configuration fails with nothing posted (EC18). The
	// OpenRouter key is read only for an openrouter manifest (A10).
	sel, err := providerSelection(manifest, env, deps.Out)
	if err != nil {
		return result{}, err
	}
	reviewLLM, err := llmFor(deps, manifest, sel)
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
	}, reviewLLM, nil, reviewerapp.Options{})
	if err != nil {
		class := modelCallClass(err)
		return result{}, &ModelCallError{Class: class, cause: err, Hint: strictModeHint(manifest, class)}
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

// The names of the runner's environment (spec 7.8.3, FR30).
const (
	envLLMBaseURL = "DEVDIGEST_LLM_BASE_URL"
	envLLMAPIKey  = "DEVDIGEST_LLM_API_KEY"
	envLLMHeader  = "DEVDIGEST_LLM_HEADER_" // + n, 1..custom.MaxHeaders
)

// providerSelection reads the environment of the manifest's provider and nothing else (A10):
// nil for an openrouter manifest (Deps.LLM serves it, bound to OPENROUTER_API_KEY), else the
// provider, its one key and, for a custom provider, the base URL and header values, checked
// (A9, FR30). A plain-http non-loopback base URL prints one warning line.
func providerSelection(m Manifest, env Env, out Out) (*ProviderSelection, error) {
	get := func(key string) string { v, _ := env.Lookup(key); return v }
	switch m.Provider {
	case cidomain.ProviderOpenAI:
		return &ProviderSelection{Provider: m.Provider, Model: m.Model, Key: get(cidomain.SecretOpenAI), Price: m.Pricing}, nil
	case cidomain.ProviderAnthropic:
		return &ProviderSelection{Provider: m.Provider, Model: m.Model, Key: get(cidomain.SecretAnthropic), Price: m.Pricing}, nil
	}
	slug, isCustom := cidomain.CustomSlug(m.Provider)
	if !isCustom || m.CustomProvider == nil {
		return nil, nil
	}
	envName, _ := cidomain.EnvName(slug)
	cp := m.CustomProvider

	baseURL, set := env.Lookup(envLLMBaseURL)
	where := envLLMBaseURL + " (repository variable " + cidomain.BaseURLVariable(envName) + ")"
	if !set || baseURL == "" {
		return nil, domain.NewRunnerError(where + " is empty: it must be the base URL of the custom provider, http:// or https://")
	}
	norm, plainHTTP, msg := custom.CheckBaseURL(baseURL)
	if msg != "" {
		return nil, domain.NewRunnerError(where + " is not usable: " + msg)
	}
	if plainHTTP {
		out.Stderr(LogLine("[agent-runner] warning: ", envLLMBaseURL+" is a plain http address: the API key and the headers travel unencrypted"))
	}

	key := get(envLLMAPIKey)
	if key != "" {
		if msg := custom.CheckHeaderValue(key); msg != "" {
			return nil, domain.NewRunnerError(envLLMAPIKey + " (repository secret " + cidomain.APIKeySecret(envName) + ") is not usable: " + msg)
		}
	}
	headers := make([]Header, 0, len(cp.ExtraHeaders))
	for i, name := range cp.ExtraHeaders {
		n := strconv.Itoa(i + 1)
		value := get(envLLMHeader + n)
		hwhere := envLLMHeader + n + " (repository secret " + cidomain.HeaderSecret(envName, i+1) + ")"
		if value == "" {
			return nil, domain.NewRunnerError(hwhere + " is empty: it must hold the value of an extra header of the custom provider")
		}
		if msg := custom.CheckHeaderValue(value); msg != "" {
			return nil, domain.NewRunnerError(hwhere + " is not usable: " + msg)
		}
		headers = append(headers, Header{Name: name, Value: value})
	}
	authHeader := ""
	if cp.AuthMode == custom.AuthHeader {
		authHeader = cp.AuthHeader
	}
	return &ProviderSelection{
		Provider: m.Provider, Model: m.Model, Key: key, Price: m.Pricing,
		Custom: &CustomEndpoint{
			BaseURL: norm, Protocol: cp.Protocol, AuthMode: cp.AuthMode, AuthHeader: authHeader,
			Headers: headers, Mode: cp.StructuredOutput,
		},
	}, nil
}

// llmFor is the engine's LLM port for the manifest: Deps.LLM for openrouter, else Deps.Providers.
func llmFor(deps Deps, m Manifest, sel *ProviderSelection) (reviewerapp.LLM, error) {
	if sel == nil {
		return deps.LLM.For(m.Model), nil
	}
	if deps.Providers == nil {
		return nil, domain.NewRunnerError("This runner cannot call the provider of the manifest")
	}
	l, err := deps.Providers.For(*sel)
	if err != nil {
		return nil, &ModelCallError{Class: modelCallClass(err), cause: err}
	}
	return l, nil
}

// knownSecretNames are the environment variables whose values never reach the job log (NFR7,
// A10): every key and token the runner knows, whether the manifest's provider reads it or not.
func knownSecretNames() []string {
	names := []string{cidomain.SecretOpenRouter, cidomain.SecretOpenAI, cidomain.SecretAnthropic, "GITHUB_TOKEN", envLLMAPIKey}
	for n := 1; n <= custom.MaxHeaders; n++ {
		names = append(names, envLLMHeader+strconv.Itoa(n))
	}
	return names
}

// redact removes every known secret of the environment from a failure message. The clients scrub
// their own errors; this is the last line of defence for the job log.
func redact(env Env, s string) string {
	for _, key := range knownSecretNames() {
		if v, _ := env.Lookup(key); len(v) >= minRedactLen {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	return s
}
