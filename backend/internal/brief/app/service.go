package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/inflight"
)

// Deps are the collaborators of the Service. Every field but Clock, Log and Budget is required.
type Deps struct {
	Store     Store
	Intents   Intents
	Blast     Blast
	Documents Documents
	Issues    Issues
	Roles     Roles
	LLM       LLM
	Models    FeatureModels
	// Clock stamps generated_at and measures the generation; nil = time.Now.
	Clock Clock
	// Log receives the content-free generation lines; nil discards them.
	Log *slog.Logger
	// Budget bounds one generation (facts, model call, storage); zero = domain.GenerationBudget.
	Budget time.Duration
}

// Service implements API and Lifecycle.
type Service struct {
	d       Deps
	log     *slog.Logger
	clock   Clock
	budget  time.Duration
	markers *inflight.Markers
}

var (
	_ API       = (*Service)(nil)
	_ Lifecycle = (*Service)(nil)
)

// NewService builds a Service. A missing dependency is a wiring bug and panics at startup.
func NewService(d Deps) *Service {
	if d.Store == nil || d.Intents == nil || d.Blast == nil || d.Documents == nil || d.Issues == nil ||
		d.Roles == nil || d.LLM == nil || d.Models == nil {
		panic("brief/app: NewService needs every dependency")
	}
	s := &Service{d: d, log: d.Log, clock: d.Clock, budget: d.Budget, markers: inflight.NewMarkers()}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.budget <= 0 {
		s.budget = domain.GenerationBudget
	}
	return s
}

// Get returns the stored brief and whether it is stale; it never calls a model.
func (s *Service) Get(ctx context.Context, workspaceID, prID string) (domain.State, error) {
	pull, err := s.findPull(ctx, workspaceID, prID)
	if err != nil {
		return domain.State{}, err
	}
	b, ok, err := s.d.Store.Brief(ctx, prID)
	if err != nil {
		return domain.State{}, fmt.Errorf("load brief: %w", err)
	}
	if !ok {
		return domain.State{}, nil
	}
	return domain.State{Brief: &b, Stale: domain.IsStale(b.HeadSHA, b.PromptVersion, pull.HeadSHA)}, nil
}

// Generate makes, stores and returns a brief; at most one generation per pull request runs at a
// time, and its context also ends when the service is shut down.
func (s *Service) Generate(ctx context.Context, workspaceID, prID string) (domain.State, error) {
	pull, err := s.findPull(ctx, workspaceID, prID)
	if err != nil {
		return domain.State{}, err
	}
	gctx, release, ok := s.markers.Begin(ctx, prID)
	if !ok {
		return domain.State{}, apperr.Conflict("A brief is already being generated for this pull request").
			WithCode("generation_in_progress")
	}
	defer release()

	started := s.clock()
	st := genStats{outcome: "ok"}
	brief, err := s.run(gctx, workspaceID, pull, &st)
	if err != nil {
		st.outcome = outcomeOf(err)
	}
	s.logGenerated(gctx, pull.ID, &st, s.clock().Sub(started))
	if err != nil {
		return domain.State{}, err
	}
	return domain.State{Brief: &brief}, nil
}

// Shutdown cancels the running generations and waits for them.
func (s *Service) Shutdown(ctx context.Context) error {
	if err := s.markers.Shutdown(ctx); err != nil {
		return fmt.Errorf("brief generations: %w", err)
	}
	return nil
}

func (s *Service) findPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error) {
	pull, err := s.d.Store.FindPull(ctx, workspaceID, prID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Pull{}, apperr.NotFound("Pull request not found").WithCause(err)
	}
	if err != nil {
		return domain.Pull{}, fmt.Errorf("load pull request: %w", err)
	}
	return pull, nil
}

// run is one generation. ctx is the in-flight context; the budget starts once the stored files
// are known to exist.
func (s *Service) run(ctx context.Context, workspaceID string, pull domain.Pull, st *genStats) (domain.Brief, error) {
	stored, err := s.d.Store.PullFiles(ctx, pull.ID)
	if err != nil {
		return domain.Brief{}, fmt.Errorf("load pull request files: %w", err)
	}
	if len(stored) == 0 {
		return domain.Brief{}, apperr.Validation("The pull request has no changed files").WithCode("no_changed_files")
	}
	bctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()

	b, err := s.generate(bctx, workspaceID, pull, stored, st)
	if err != nil && budgetExpired(bctx) {
		if _, isApp := apperr.As(err); !isApp {
			// A read cut short by the budget is a timeout, not an internal error.
			return domain.Brief{}, errTimedOut()
		}
	}
	return b, err
}

func (s *Service) generate(ctx context.Context, workspaceID string, pull domain.Pull, stored []domain.StoredFile, st *genStats) (domain.Brief, error) {
	bag, err := s.d.Models.Get(ctx, workspaceID)
	if err != nil {
		return domain.Brief{}, fmt.Errorf("read settings: %w", err)
	}
	provider, model := llm.ResolveFeatureModel(bag, domain.FeatureModel)
	st.provider, st.model = ptr(string(provider)), ptr(model)

	files := domain.ChangedFiles(stored)
	reviews, err := s.d.Store.CurrentReviews(ctx, pull.ID)
	if err != nil {
		return domain.Brief{}, fmt.Errorf("load current reviews: %w", err)
	}
	findings := domain.FlattenFindings(reviews)

	facts, err := s.gather(ctx, workspaceID, pull, reviews, files)
	if err != nil {
		return domain.Brief{}, err
	}
	if budgetExpired(ctx) {
		return domain.Brief{}, errTimedOut()
	}

	usable := domain.UsableBlast(facts.blast)
	in := domain.BuildInput(domain.InputFacts{
		Pull: pull, Files: files, Findings: findings, Intent: facts.intent, Blast: facts.blast,
		Issue: facts.issue, Docs: facts.docs,
	}, domain.RoleFunc(s.d.Roles.Role))
	trim := domain.TrimToBudget(in, domain.InputBudgetTokens)
	st.estTokensIn = ptr(trim.EstTokens)

	missing := domain.BuildMissingInputs(domain.MissingInputFacts{
		Description: pull.Body,
		HasIntent:   facts.intent != nil,
		Blast:       facts.blast,
		Specs:       domain.NewSpecsOutcome(len(reviews), facts.docs, trim.DroppedSpecs),
		Issue:       domain.IssueOutcomeOf(facts.linked, facts.issue != nil),
	})
	st.missingInputs = make([]string, len(missing))
	for i, m := range missing {
		st.missingInputs[i] = string(m.Input) + "/" + string(m.Reason)
	}

	res, err := s.callModel(ctx, pull, provider, model, trim)
	if err != nil {
		return domain.Brief{}, err
	}
	st.modelRequests = ptr(res.Attempts)
	st.tokensIn, st.tokensOut, st.costUSD = ptr(res.TokensIn), ptr(res.TokensOut), res.CostUSD
	if budgetExpired(ctx) {
		return domain.Brief{}, errTimedOut()
	}

	out, err := domain.ParseLLMOutput(res.Data)
	if err != nil {
		return domain.Brief{}, errCallFailed()
	}
	norm := domain.NormalizeBrief(out, domain.NewGroundingFacts(files, findings, usable))
	st.dropped = &norm.Dropped

	brief := domain.Brief{
		Summary:       norm.Summary,
		Risks:         norm.Risks,
		ReviewFocus:   norm.ReviewFocus,
		Intent:        facts.intent,
		Blast:         usable,
		MissingInputs: missing,
		SpecsUsed:     domain.SpecPaths(trim.Input),
		HeadSHA:       pull.HeadSHA,
		GeneratedAt:   s.clock().UTC(),
		PromptVersion: domain.PromptVersion,
		Provider:      string(provider),
		Model:         model,
		TokensIn:      res.TokensIn,
		TokensOut:     res.TokensOut,
		CostUSD:       res.CostUSD,
		ModelRequests: min(res.Attempts, 2),
	}
	brief.ModelRequests = max(brief.ModelRequests, 1)
	if err := s.d.Store.SaveBrief(ctx, pull.ID, brief); err != nil {
		if budgetExpired(ctx) {
			return domain.Brief{}, errTimedOut()
		}
		return domain.Brief{}, fmt.Errorf("save brief: %w", err)
	}
	return brief, nil
}

// callModel makes the one structured call and maps its failure: a missing key is the 422 the UI
// acts on, an expired budget a timeout, anything else a generic 502 (provider texts may echo PR
// content and never reach the response).
func (s *Service) callModel(ctx context.Context, pull domain.Pull, provider llm.Provider, model string, trim domain.TrimResult) (llm.Result, error) {
	var usages []llm.AttemptUsage
	temperature := domain.Temperature
	res, err := s.d.LLM.Structured(ctx, llm.Request{
		Provider: provider,
		Model:    model,
		Messages: []llm.Message{
			{Role: llm.System, Content: domain.SystemPrompt},
			{Role: llm.User, Content: domain.UserMessage(trim.Input)},
		},
		SchemaName:    domain.SchemaName,
		Schema:        domain.SchemaJSON,
		Temperature:   &temperature,
		MaxTokens:     domain.MaxOutputTokens,
		Reprompts:     domain.Reprompts,
		Feature:       domain.Feature,
		CorrelationID: correlationID(pull),
		Sections:      promptSections(trim.Input),
		OnUsage:       func(u llm.AttemptUsage) { usages = append(usages, u) },
	})
	s.logModelRequests(ctx, pull.ID, model, trim.EstTokens, usages, err == nil)
	if err == nil {
		return res, nil
	}
	if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindConfig {
		return llm.Result{}, apperr.Validation(fmt.Sprintf("The %s provider is not configured", provider)).
			WithCode("provider_not_configured").
			WithDetails(map[string]string{"provider": string(provider)})
	}
	if budgetExpired(ctx) {
		return llm.Result{}, errTimedOut()
	}
	return llm.Result{}, errCallFailed()
}

// budgetExpired reports whether the generation budget (not the caller or a shutdown) ended ctx.
func budgetExpired(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), context.DeadlineExceeded)
}

func errTimedOut() *apperr.Error {
	return apperr.ExternalService("Generation timed out").WithCode("generation_failed")
}

func errCallFailed() *apperr.Error {
	return apperr.ExternalService("The model call failed").WithCode("generation_failed")
}

func correlationID(pull domain.Pull) string {
	sha := pull.HeadSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return "brief:" + pull.ID + ":" + sha
}

func promptSections(in domain.BriefInput) []llm.PromptSection {
	logSections := domain.LogSections(in)
	out := make([]llm.PromptSection, len(logSections))
	for i, ls := range logSections {
		trust := llm.Untrusted
		if ls.Trusted {
			trust = llm.Trusted
		}
		out[i] = llm.PromptSection{Name: ls.Name, Source: ls.Origin, Trust: trust, Chars: llm.UTF16Len(ls.Text)}
		if ls.Name != "system" {
			out[i].Items = ptr(ls.Items)
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }
