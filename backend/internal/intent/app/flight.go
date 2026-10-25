package app

// Resolving an intent: the stored record when it matches the key, else one shared derivation per
// pull request and key (spec 7.4.6) — tickets and docs read, one classification call, the upsert.

import (
	"context"
	"encoding/json"
	"fmt"

	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
)

// Resolve implements Resolver.
func (s *Service) Resolve(ctx context.Context, in ResolveInput, sink Sink) Result {
	res, err := s.resolve(ctx, in, sink)
	if err != nil {
		warning := warningText(err)
		deliver(sink, unavailableEvent(warning))
		return Result{Warning: warning}
	}
	return res
}

func deliver(sink Sink, e event) {
	if sink != nil {
		sink(e.kind, e.msg, e.data)
	}
}

func (s *Service) resolve(ctx context.Context, in ResolveInput, sink Sink) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, context.Cause(ctx)
	}
	key, err := s.computeKey(ctx, in.WorkspaceID, in.Pull, in.Repo)
	if err != nil {
		return Result{}, err
	}
	if !in.Force {
		rec, ok, err := s.d.Store.Get(ctx, in.Pull.ID)
		if err != nil {
			return Result{}, err
		}
		if ok && rec.InputHash == key.hash {
			deliver(sink, cachedEvent(rec.Confidence))
			return Result{Used: true, Record: rec, CacheHit: true}, nil
		}
	}
	var onEvent func(event)
	if sink != nil {
		onEvent = func(e event) { deliver(sink, e) }
	}
	return s.flights.Do(ctx, in.Pull.ID+":"+key.hash, onEvent, func(fctx context.Context, emit func(event)) (Result, error) {
		return s.derive(fctx, in, key, emit)
	})
}

// derive is the shared work of a flight. ctx ends on the budget, when every caller has left and on
// shutdown; every step honours it, and the error then is the cancellation's cause.
func (s *Service) derive(ctx context.Context, in ResolveInput, key derivation, emit func(event)) (Result, error) {
	core, err := s.coreSources(ctx, in, key)
	if err != nil {
		return Result{}, err
	}
	derivedFrom := domain.DerivedFrom(in.Pull.Body, core)
	sources := core
	if derivedFrom == domain.DerivedInferred {
		sources = append(sources[:len(sources):len(sources)],
			domain.FallbackSources(key.commits, in.Pull.Branch, in.ChangedFiles, in.DiffExcerpt)...)
	}
	confidence := domain.ConfidenceFor(derivedFrom, sources)
	block := domain.RenderSourcesBlock(sources)

	if err := ctx.Err(); err != nil {
		return Result{}, context.Cause(ctx)
	}
	class, usage, err := s.classify(ctx, in, key, sources, block)
	if err != nil {
		return Result{}, err
	}
	emit(sourcesEvent(sources))

	intent := domain.BuildIntent(class, derivedFrom, confidence, sources)
	stored, err := s.d.Store.Upsert(ctx, domain.Record{
		PRID: in.Pull.ID, Text: intent.Text, InScope: intent.InScope, OutOfScope: intent.OutOfScope,
		ChangeType: intent.ChangeType, Confidence: intent.Confidence, DerivedFrom: intent.DerivedFrom,
		Sources: intent.Sources, HeadSHA: in.Pull.HeadSHA, InputHash: key.hash, PromptVersion: domain.PromptVersion,
		Provider: string(key.provider), Model: key.model, TokensIn: usage.TokensIn, TokensOut: usage.TokensOut,
		CostUSD: usage.CostUSD, DerivedAt: s.clock(),
	})
	if err != nil {
		return Result{}, ctxCause(ctx, err)
	}
	emit(derivedEvent(confidence, usage.TokensIn, usage.TokensOut, usage.CostUSD))
	s.log.InfoContext(ctx, "pr intent derived", "prId", in.Pull.ID, "confidence", confidence,
		"sources", len(sources), "tokensIn", usage.TokensIn, "tokensOut", usage.TokensOut)
	return Result{Used: true, Record: stored}, nil
}

// ctxCause is the cancellation's cause when ctx has ended (the reason a caller or the budget gave),
// else err.
func ctxCause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

// coreSources reads the title, body, ticket and doc sources, in that order (spec 7.4.3). A failed
// read is a failed source; the only error is the end of ctx.
func (s *Service) coreSources(ctx context.Context, in ResolveInput, key derivation) ([]domain.Source, error) {
	pull := in.Pull
	sources := []domain.Source{domain.TitleSource(pull.Title)}
	if body, ok := domain.BodySource(pull.Body); ok {
		sources = append(sources, body)
	}

	for _, t := range key.tickets.Refs {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		read := s.d.Tickets.Read(ctx, in.Repo, t.Number)
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		if read.OK {
			sources = append(sources, domain.TicketSource(t.Ref(), read.Title, read.Body))
		} else {
			sources = append(sources, domain.FailedSource(domain.KindTicket, t.Ref(), read.Reason))
		}
	}
	sources = append(sources, domain.SkippedSources(domain.KindTicket, key.tickets.Skipped)...)

	budget := domain.NewDocBudget()
	for _, d := range key.docs.Refs {
		if budget.Exhausted() {
			sources = append(sources, domain.BudgetSkipped(d.Path))
			continue
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		read := s.d.Docs.Read(ctx, in.Repo, pull.HeadSHA, d.Path)
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		if !read.OK {
			sources = append(sources, domain.FailedSource(domain.KindDoc, d.Path, read.Reason))
			continue
		}
		sources = append(sources, budget.Used(d.Path, read.Content, read.Truncated))
	}
	sources = append(sources, domain.SkippedSources(domain.KindDoc, key.docs.Skipped)...)
	return sources, nil
}

// chargeKind is the ledger kind of a derivation: a refresh (Force) is intent_refresh, a review
// run's derivation review_intent. A flight is shared, so the input is the starter's: the kind of
// whichever caller started the derivation (spec A7).
func chargeKind(in ResolveInput) llm.UsageKind {
	if in.Force {
		return llm.KindIntentRefresh
	}
	return llm.KindReviewIntent
}

// classification is the model's answer (schema IntentClassification).
type classification struct {
	Intent     string   `json:"intent"`
	InScope    []string `json:"in_scope"`
	OutOfScope []string `json:"out_of_scope"`
	ChangeType string   `json:"change_type"`
}

// classify makes the one structured call of a derivation and returns the answer with its usage
// (tokens and cost over all attempts).
func (s *Service) classify(ctx context.Context, in ResolveInput, key derivation, sources []domain.Source, block string) (domain.Classification, llm.Result, error) {
	pull := in.Pull
	temperature := domain.Temperature
	items := len(sources)
	res, err := s.d.LLM.Structured(ctx, llm.Request{
		Provider: key.provider,
		Model:    key.model,
		Messages: []llm.Message{
			{Role: llm.System, Content: domain.SystemPrompt},
			{Role: llm.User, Content: domain.UserMessage(block)},
		},
		SchemaName:    domain.SchemaName,
		Schema:        domain.ClassificationSchemaJSON,
		Temperature:   &temperature,
		MaxTokens:     domain.MaxOutputTokens,
		Reprompts:     domain.Reprompts,
		Feature:       domain.LogFeature,
		CorrelationID: domain.CorrelationID(pull.ID, key.hash),
		Charge:        &llm.Charge{WorkspaceID: in.WorkspaceID, Kind: chargeKind(in), RefID: pull.ID},
		Sections: []llm.PromptSection{
			{Name: "system", Source: "engine", Trust: llm.Trusted, Chars: llm.UTF16Len(domain.SystemPrompt)},
			{Name: "task", Source: "engine", Trust: llm.Trusted, Chars: llm.UTF16Len(domain.TaskText())},
			{Name: "intent_sources", Source: "repo", Trust: llm.Untrusted, Chars: llm.UTF16Len(block), Items: &items},
		},
	})
	if err != nil {
		return domain.Classification{}, llm.Result{}, ctxCause(ctx, err)
	}
	var out classification
	if err := json.Unmarshal(res.Data, &out); err != nil {
		return domain.Classification{}, llm.Result{}, fmt.Errorf("decode the classification: %w", err)
	}
	return domain.Classification{Intent: out.Intent, InScope: out.InScope, OutOfScope: out.OutOfScope, ChangeType: out.ChangeType}, res, nil
}
