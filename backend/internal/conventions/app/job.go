package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

const (
	// featureName is the feature-model key and the log label of a scan.
	featureName = "conventions"
	// failWriteTimeout bounds the write that ends a failed scan, made after the job's own
	// context may be gone.
	failWriteTimeout = 10 * time.Second

	msgNotCloned = "The repo is not cloned"
	msgNoSample  = "No source or config files to sample in the clone"
)

// JobsDeps are the collaborators of the extract job.
type JobsDeps struct {
	Store    jobStore
	Repos    repoLookup
	Files    cloneFiles
	LLM      structuredLLM
	Features featureModels
	// Ranker supplies the ranked source files of an indexed repo; nil (or no usable answer)
	// means the Phase 2 fallback walk.
	Ranker sampleRanker
	// Redact scrubs provider keys out of every error text before it is stored or logged; nil
	// leaves texts as they are.
	Redact redactor
}

// JobsOptions tune Jobs.
type JobsOptions struct {
	// Timeout is the per-attempt limit the handler is registered with; it only words the
	// timeout error of a scan. Zero means JobTimeout.
	Timeout time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Log receives the warning of a failed scan; nil discards it.
	Log *slog.Logger
}

// Jobs is the background handler of the conventions context. Register Extract under
// KindExtract with jobs.WithTimeout(JobTimeout).
type Jobs struct {
	d       JobsDeps
	timeout time.Duration
	now     func() time.Time
	log     *slog.Logger
}

// NewJobs builds the job handler.
func NewJobs(d JobsDeps, opts JobsOptions) *Jobs {
	j := &Jobs{d: d, timeout: opts.Timeout, now: opts.Now, log: opts.Log}
	if j.d.Redact == nil {
		j.d.Redact = noRedact{}
	}
	if j.timeout <= 0 {
		j.timeout = JobTimeout
	}
	if j.now == nil {
		j.now = time.Now
	}
	if j.log == nil {
		j.log = slog.New(slog.DiscardHandler)
	}
	return j
}

// Extract is the handler of the extract job: sample the clone, ask the model for conventions,
// verify their evidence against the clone and store the result.
//
// It returns an error only for a payload it cannot read. Any other failure, including the job
// timeout and a shutdown, ends the scan failed with a short text and returns nil, so the runner
// marks the job done and never retries it (a retry would bill the model twice).
func (j *Jobs) Extract(ctx context.Context, payload json.RawMessage) error {
	var p ExtractPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode extract payload: %w", err)
	}
	if p.ScanID == "" || p.WorkspaceID == "" || p.RepoID == "" {
		return errors.New("decode extract payload: scanId, workspaceId and repoId are required")
	}
	var sampled []string
	if err := j.runGuarded(ctx, p, &sampled); err != nil {
		j.fail(ctx, p, sampled, err)
	}
	return nil
}

// runGuarded runs the scan and turns a panic into an error, so the scan never stays running.
func (j *Jobs) runGuarded(ctx context.Context, p ExtractPayload, sampled *[]string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("scan panicked: %v", r)
		}
	}()
	return j.run(ctx, p, sampled)
}

func (j *Jobs) run(ctx context.Context, p ExtractPayload, sampled *[]string) error {
	repo, err := j.d.Repos.Get(ctx, p.WorkspaceID, p.RepoID)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && repo.ClonePath == "") {
		return errors.New(msgNotCloned)
	}
	if err != nil {
		return fmt.Errorf("load repo %s: %w", p.RepoID, err)
	}

	sample, err := j.sample(ctx, repo)
	if err != nil {
		return err
	}
	*sampled = sample.Included
	if len(sample.Included) == 0 {
		return errors.New(msgNoSample)
	}

	candidates, res, err := j.propose(ctx, p, repo, sample)
	if err != nil {
		return err
	}

	lines := map[string][]string{}
	for _, path := range domain.CitedPaths(candidates) {
		if text, ok := j.d.Files.Read(repo.ClonePath, path); ok {
			lines[path] = domain.SplitLines(text)
		}
	}
	existing, err := j.d.Store.List(ctx, p.RepoID)
	if err != nil {
		return fmt.Errorf("list conventions of %s: %w", p.RepoID, err)
	}
	known := map[string]struct{}{}
	for _, c := range existing {
		if c.Decided() {
			known[domain.RuleKey(c.Rule)] = struct{}{}
		}
	}
	gate := domain.GateCandidates(candidates, func(path string) ([]string, bool) {
		l, ok := lines[path]
		return l, ok
	}, known)

	if err := ctx.Err(); err != nil {
		return err
	}
	sum := Summary{
		SampledFiles: sample.Included,
		Proposed:     len(candidates),
		Kept:         len(gate.Kept),
		Dropped:      gate.Dropped,
		Model:        res.Model,
		TokensIn:     res.TokensIn,
		TokensOut:    res.TokensOut,
		CostUSD:      res.CostUSD,
	}
	ref := ScanRef{ID: p.ScanID, WorkspaceID: p.WorkspaceID, RepoID: p.RepoID}
	if err := j.d.Store.CompleteScan(ctx, ref, gate.Kept, sum, j.now()); err != nil {
		return fmt.Errorf("complete scan %s: %w", p.ScanID, err)
	}
	return nil
}

// sample reads the clone and renders the prompt sample: up to 8 config files, then up to 12
// source files, the ranked ones when repo-intel has any in the clone's file list, else chosen by
// the fallback rule. Unreadable, blank, binary and oversized files are
// skipped.
func (j *Jobs) sample(ctx context.Context, repo Repo) (domain.Sample, error) {
	paths, err := j.d.Files.List(ctx, repo.ClonePath)
	if errors.Is(err, domain.ErrCloneMissing) {
		return domain.Sample{}, errors.New(msgNotCloned)
	}
	if err != nil {
		return domain.Sample{}, fmt.Errorf("list clone of %s: %w", repo.FullName, err)
	}
	configs := domain.PickConfigFiles(paths)
	var picked []domain.SampledFile
	for _, p := range configs {
		picked = append(picked, domain.SampledFile{Path: p, Kind: domain.KindConfig})
	}
	sources := j.rankedSources(ctx, repo.ID, paths, configs)
	if len(sources) == 0 {
		sources = domain.PickFallbackSources(paths, domain.SampleTopN)
	}
	for _, p := range sources {
		if !slices.Contains(configs, p) {
			picked = append(picked, domain.SampledFile{Path: p, Kind: domain.KindSource})
		}
	}
	var files []domain.SampledFile
	for _, f := range picked {
		if err := ctx.Err(); err != nil {
			return domain.Sample{}, err
		}
		text, ok := j.d.Files.Read(repo.ClonePath, f.Path)
		if !ok || domain.IsBlank(text) {
			continue
		}
		f.Content = text
		files = append(files, f)
	}
	return domain.RenderSample(files), nil
}

// rankedSources asks the ranker for the repo's top files and keeps, in the returned order, those
// that are in the clone's file list and are not picked config files (at most SampleTopN, each
// once). A missing ranker, an error or an empty answer yields nil: the caller falls back.
func (j *Jobs) rankedSources(ctx context.Context, repoID string, paths, configs []string) []string {
	if j.d.Ranker == nil {
		return nil
	}
	ranked, err := j.d.Ranker.ConventionSamples(ctx, repoID, domain.SampleTopN)
	if err != nil {
		j.log.WarnContext(ctx, "ranked samples unavailable, using fallback walk", "repo_id", repoID, "error_kind", "ranker")
		return nil
	}
	present := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		present[p] = struct{}{}
	}
	var out []string
	for _, p := range ranked {
		if _, ok := present[p]; !ok || slices.Contains(configs, p) || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
		if len(out) == domain.SampleTopN {
			break
		}
	}
	return out
}

// propose makes the one structured call of a scan and decodes the validated answer.
func (j *Jobs) propose(ctx context.Context, p ExtractPayload, repo Repo, sample domain.Sample) ([]domain.Candidate, llm.Result, error) {
	bag, err := j.d.Features.Get(ctx, p.WorkspaceID)
	if err != nil {
		return nil, llm.Result{}, fmt.Errorf("load settings: %w", err)
	}
	provider, model := llm.ResolveFeatureModel(bag, featureName)
	files := len(sample.Included)
	temperature := domain.LLMTemperature
	res, err := j.d.LLM.Structured(ctx, llm.Request{
		Provider: provider,
		Model:    model,
		Messages: []llm.Message{
			{Role: llm.System, Content: domain.ExtractionSystemPrompt},
			{Role: llm.User, Content: domain.UserMessage(repo.FullName, sample.Text)},
		},
		SchemaName:    domain.ExtractionSchemaName,
		Schema:        domain.ExtractionSchemaJSON,
		Temperature:   &temperature,
		MaxTokens:     domain.LLMMaxOutputTokens,
		Timeout:       domain.LLMTimeout,
		Reprompts:     domain.LLMReprompts,
		Feature:       featureName,
		CorrelationID: p.ScanID,
		Charge:        &llm.Charge{WorkspaceID: p.WorkspaceID, Kind: llm.KindConventionsScan, RefID: p.ScanID},
		Sections: []llm.PromptSection{
			{Name: "system", Source: "engine", Trust: llm.Trusted, Chars: llm.UTF16Len(domain.ExtractionSystemPrompt)},
			{Name: "task", Source: "engine", Trust: llm.Trusted, Chars: llm.UTF16Len(domain.TaskText(repo.FullName))},
			{Name: "repository_sample", Source: "repo", Trust: llm.Untrusted,
				Chars: llm.UTF16Len(domain.WrappedRepositorySample(sample.Text)), Items: &files},
		},
	})
	if err != nil {
		return nil, llm.Result{}, err
	}
	var doc any
	if err := json.Unmarshal(res.Data, &doc); err != nil {
		return nil, llm.Result{}, fmt.Errorf("decode model answer: %w", err)
	}
	extraction, issues := domain.ValidateExtraction(doc)
	if len(issues) > 0 {
		return nil, llm.Result{}, fmt.Errorf("model answer invalid: %s: %s", issues[0].Path, issues[0].Message)
	}
	return extraction.Candidates, res, nil
}

// fail ends the scan failed. The job context may already be over (timeout, shutdown), so the
// write uses a context that outlives it.
func (j *Jobs) fail(ctx context.Context, p ExtractPayload, sampled []string, cause error) {
	text := j.failureText(ctx, cause)
	// The stored text can quote model output (and so repository lines); the log line stays
	// content-free (NFR202): ids, the failure kind and the stored text's length only.
	j.log.WarnContext(context.WithoutCancel(ctx), "conventions scan failed",
		"scan_id", p.ScanID, "repo_id", p.RepoID, "error_kind", failureKind(ctx, cause),
		"error_chars", llm.UTF16Len(text))
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failWriteTimeout)
	defer cancel()
	if err := j.d.Store.FailScan(wctx, p.ScanID, text, sampled, j.now()); err != nil {
		j.log.ErrorContext(wctx, "conventions scan not marked failed",
			"scan_id", p.ScanID, "repo_id", p.RepoID, "error", errorText(j.d.Redact, err.Error()))
	}
}

// failureKind names the class of a scan failure for the log line, without its text.
func failureKind(ctx context.Context, cause error) string {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return "timeout"
	case context.Canceled:
		return "interrupted"
	}
	if e, ok := apperr.As(cause); ok {
		return e.Kind.String()
	}
	return "internal"
}

// failureText is the error stored on a failed scan: a fixed text when the job's context ended
// (timeout or shutdown), else the failure with provider keys scrubbed, cut to 500 characters.
func (j *Jobs) failureText(ctx context.Context, cause error) string {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return fmt.Sprintf("The scan timed out after %d s", int64(j.timeout/time.Second))
	case context.Canceled:
		return MsgInterrupted
	}
	return errorText(j.d.Redact, cause.Error())
}
