package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/inflight"
)

// Error codes and messages of the generation (spec 7.2). They are user-visible: the onboarding
// screen branches on the codes.
const (
	codeRepoNotFound         = "repo_not_found"
	codeGenerationInProgress = "generation_in_progress"
	codeIndexNotReady        = "index_not_ready"
	codeProviderNotConfig    = "provider_not_configured"
	codeGenerationFailed     = "generation_failed"
	codeInternal             = "internal_error"
)

// Deps are the collaborators of a Service. Every field except Now, Log and Budget is required.
type Deps struct {
	Store       Store
	IndexStates IndexStates
	Index       Index
	Blobs       Blobs
	LLM         LLM
	Models      FeatureModels
	// Now defaults to time.Now.
	Now Clock
	// Log receives the outcome line of each generation; nil discards it.
	Log *slog.Logger
	// Budget bounds one generation; 0 is domain.GenerationBudget (tests shorten it).
	Budget time.Duration
}

// Service implements API and Lifecycle.
type Service struct {
	d       Deps
	budget  time.Duration
	now     Clock
	log     *slog.Logger
	markers *inflight.Markers
}

var (
	_ API       = (*Service)(nil)
	_ Lifecycle = (*Service)(nil)
)

// NewService builds the onboarding use cases.
func NewService(d Deps) *Service {
	s := &Service{d: d, budget: d.Budget, now: d.Now, log: d.Log, markers: inflight.NewMarkers()}
	if s.budget <= 0 {
		s.budget = domain.GenerationBudget
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// Shutdown implements Lifecycle: it cancels the running generations and waits for them.
func (s *Service) Shutdown(ctx context.Context) error { return s.markers.Shutdown(ctx) }

// Get implements API.
func (s *Service) Get(ctx context.Context, workspaceID, repoID string) (domain.State, error) {
	if _, err := s.requireRepo(ctx, workspaceID, repoID); err != nil {
		return domain.State{}, err
	}
	tour, ok, err := s.d.Store.Tour(ctx, repoID)
	if err != nil {
		return domain.State{}, fmt.Errorf("read tour: %w", err)
	}
	if !ok {
		return domain.State{}, nil
	}
	return domain.StateOf(tour, s.d.IndexStates.IndexState(ctx, repoID).LastIndexedSHA), nil
}

// runStats is what the outcome line reports: ids, counts and labels, never repository text. A
// nil field was not reached.
type runStats struct {
	provider, model     *string
	tokensIn, tokensOut *int
	costUSD             *float64
	dropped             *droppedLog
	outcome             string
}

// droppedLog is the dropped-counts object of the outcome line (field names as Node logs them).
type droppedLog struct {
	Nodes         int `json:"nodes"`
	Edges         int `json:"edges"`
	CriticalPaths int `json:"critical_paths"`
	RunSteps      int `json:"run_steps"`
	ReadingPath   int `json:"reading_path"`
	FirstTasks    int `json:"first_tasks"`
}

// Generate implements API.
func (s *Service) Generate(ctx context.Context, workspaceID, repoID string) (domain.State, error) {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return domain.State{}, err
	}
	gctx, release, ok := s.markers.Begin(ctx, repoID)
	if !ok {
		return domain.State{}, apperr.Conflict("A tour is already being generated for this repository").
			WithCode(codeGenerationInProgress)
	}
	defer release()

	started := s.now()
	// A panic leaves the outcome as internal_error; the deferred line still reports it.
	stats := &runStats{outcome: codeInternal}
	defer func() { s.logOutcome(ctx, repoID, stats, s.now().Sub(started)) }()

	tour, err := s.generate(gctx, workspaceID, repo, stats)
	if err != nil {
		stats.outcome = outcomeOf(err)
		return domain.State{}, err
	}
	stats.outcome = "ok"
	return domain.State{Tour: &tour}, nil
}

func (s *Service) generate(ctx context.Context, workspaceID string, repo domain.Repo, stats *runStats) (domain.Tour, error) {
	state := s.d.IndexStates.IndexState(ctx, repo.ID)
	if !state.Ready() {
		return domain.Tour{}, apperr.Validation("The repository index is not ready yet").WithCode(codeIndexNotReady)
	}
	sha := state.LastIndexedSHA

	bctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()

	bag, err := s.d.Models.Get(ctx, workspaceID)
	if err != nil {
		return domain.Tour{}, fmt.Errorf("read feature models: %w", err)
	}
	provider, model := llm.ResolveFeatureModel(bag, domain.FeatureModel)
	stats.provider, stats.model = ptr(string(provider)), ptr(model)

	facts, err := s.readIndex(bctx, repo.ID)
	if err != nil {
		if ended := endedBy(ctx, bctx); ended != nil {
			return domain.Tour{}, ended
		}
		return domain.Tour{}, err
	}
	roots, todos := s.readFiles(bctx, repo, sha, facts.ranked)
	// Only an expiry after the reads is "timed out"; one during the model call is a failed call
	// (spec 7.10.4, amendment of 2026-10-22).
	if ended := endedBy(ctx, bctx); ended != nil {
		return domain.Tour{}, ended
	}

	res, err := s.callModel(bctx, repo, provider, model, buildInput(repo, facts, roots, todos), stats)
	if err != nil {
		return domain.Tour{}, err
	}
	out, err := domain.ParseOutput(res.Data)
	if err != nil {
		return domain.Tour{}, failedCall(err)
	}
	content, dropped := domain.NormalizeTour(out, facts.indexed)
	stats.dropped = &droppedLog{
		Nodes: dropped.Nodes, Edges: dropped.Edges, CriticalPaths: dropped.CriticalPaths,
		RunSteps: dropped.RunSteps, ReadingPath: dropped.ReadingPath, FirstTasks: dropped.FirstTasks,
	}

	tour := domain.Tour{
		RepoID:        repo.ID,
		GeneratedAt:   s.now().UTC().Truncate(time.Millisecond),
		IndexedSHA:    sha,
		FilesIndexed:  state.FilesIndexed,
		Provider:      string(provider),
		Model:         model,
		TokensIn:      res.TokensIn,
		TokensOut:     res.TokensOut,
		CostUSD:       res.CostUSD,
		PromptVersion: domain.PromptVersion,
		Language:      domain.Language,
		Architecture:  content.Architecture,
		CriticalPaths: content.CriticalPaths,
		RunSteps:      content.RunSteps,
		ReadingPath:   content.ReadingPath,
		FirstTasks:    content.FirstTasks,
	}
	// The write is not bound by the budget: a call that succeeded just before it ran out is kept.
	if err := s.d.Store.SaveTour(ctx, tour); err != nil {
		return domain.Tour{}, fmt.Errorf("save tour: %w", err)
	}
	return tour, nil
}

// callModel makes the one structured call (reprompted once) and maps its failures: a missing key
// is 422 provider_not_configured, anything else 502 generation_failed. Provider texts never
// reach the response; they are kept as the error's cause for the server log only.
func (s *Service) callModel(ctx context.Context, repo domain.Repo, provider llm.Provider, model string, in domain.TourInput, stats *runStats) (llm.Result, error) {
	msgs := domain.BuildMessages(in)
	messages := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		messages[i] = llm.Message{Role: llm.Role(m.Role), Content: m.Content}
	}
	temperature := domain.Temperature
	res, err := s.d.LLM.Structured(ctx, llm.Request{
		Provider:      provider,
		Model:         model,
		Messages:      messages,
		SchemaName:    domain.SchemaName,
		Schema:        domain.SchemaJSON,
		Temperature:   &temperature,
		MaxTokens:     domain.MaxOutputTokens,
		Reprompts:     domain.Reprompts,
		Feature:       domain.Feature,
		CorrelationID: "onboarding:" + repo.ID,
	})
	if err != nil {
		if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindConfig {
			return llm.Result{}, apperr.Validation(fmt.Sprintf("The %s provider is not configured", provider)).
				WithCode(codeProviderNotConfig).
				WithDetails(map[string]string{"provider": string(provider)}).
				WithCause(err)
		}
		return llm.Result{}, failedCall(err)
	}
	stats.tokensIn, stats.tokensOut, stats.costUSD = ptr(res.TokensIn), ptr(res.TokensOut), res.CostUSD
	return res, nil
}

func (s *Service) requireRepo(ctx context.Context, workspaceID, repoID string) (domain.Repo, error) {
	repo, err := s.d.Store.FindRepo(ctx, workspaceID, repoID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Repo{}, apperr.NotFound("Repo not found").WithCode(codeRepoNotFound)
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("find repo: %w", err)
	}
	return repo, nil
}

// failedCall is the generic 502 of a model call that did not give an answer.
func failedCall(cause error) error {
	return apperr.ExternalService("The model call failed").WithCode(codeGenerationFailed).WithCause(cause)
}

// endedBy reports why the generation has to stop, nil while it may go on: the budget ran out
// (parent still alive) or the parent ended (shutdown, caller gone).
func endedBy(parent, budget context.Context) error {
	switch {
	case parent.Err() != nil:
		return apperr.ExternalService("The model call failed").WithCode(codeGenerationFailed).WithCause(context.Cause(parent))
	case budget.Err() != nil:
		return apperr.ExternalService("Generation timed out").WithCode(codeGenerationFailed)
	}
	return nil
}

func wrapStep(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

func outcomeOf(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Code
	}
	return codeInternal
}

func ptr[T any](v T) *T { return &v }

// logOutcome writes the one line per generation (spec 7.15): info on success, warn otherwise.
func (s *Service) logOutcome(ctx context.Context, repoID string, st *runStats, took time.Duration) {
	level, msg := slog.LevelInfo, "onboarding tour generated"
	if st.outcome != "ok" {
		level, msg = slog.LevelWarn, "onboarding tour generation failed"
	}
	s.log.Log(context.WithoutCancel(ctx), level, msg,
		"repoId", repoID,
		"provider", st.provider,
		"model", st.model,
		"tokensIn", st.tokensIn,
		"tokensOut", st.tokensOut,
		"costUsd", st.costUSD,
		"dropped", st.dropped,
		"outcome", st.outcome,
		"durationMs", took.Milliseconds(),
	)
}
