package peers

import (
	"context"

	intentapp "dev-digest-go/backend/internal/intent/app"
	intent "dev-digest-go/backend/internal/intent/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// IntentService is the part of the intent context's service the adapter uses
// (intent/app.Resolver, implemented by *intent/app.Service).
type IntentService interface {
	Resolve(ctx context.Context, in intentapp.ResolveInput, sink intentapp.Sink) intentapp.Result
}

// Intent implements reviews/app.IntentResolver over the intent context: it maps the request's
// pull request, repository and diff onto the derivation's input and its record onto the intent
// and the trace of the runs (Phase 5 spec 7.5).
type Intent struct{ svc IntentService }

var _ reviewsapp.IntentResolver = (*Intent)(nil)

// NewIntent wraps the intent service. A nil service is a wiring error and panics.
func NewIntent(svc IntentService) *Intent {
	if svc == nil {
		panic("reviews/adapters/peers: NewIntent needs the intent service")
	}
	return &Intent{svc: svc}
}

// Resolve derives or reuses the intent of the request. It never fails: an unavailable derivation
// is a result with no intent and an unavailable trace. The derivation's events go to emit as
// they happen. It is never forced, so a stored derivation of the same key is reused.
func (a *Intent) Resolve(ctx context.Context, req reviewsapp.IntentRequest, emit reviewsapp.IntentEmit) reviewsapp.IntentResult {
	body := ""
	if req.Pull.Body != nil {
		body = *req.Pull.Body
	}
	in := intentapp.ResolveInput{
		WorkspaceID: req.WorkspaceID,
		Pull: intent.Pull{
			ID: req.Pull.ID, RepoID: req.Pull.RepoID, Number: req.Pull.Number, Title: req.Pull.Title,
			Body: body, Branch: req.Pull.Branch, HeadSHA: req.Pull.HeadSHA,
		},
		Repo: intent.Repo{ID: req.Repo.ID, Owner: req.Repo.Owner, Name: req.Repo.Name,
			Provider: req.Repo.Provider, Host: req.Repo.Host, ProjectID: req.Repo.ProjectID},
		ChangedFiles: req.ChangedFiles,
		DiffExcerpt:  req.DiffExcerpt,
	}
	var sink intentapp.Sink
	if emit != nil {
		sink = intentapp.Sink(emit)
	}

	res := a.svc.Resolve(ctx, in, sink)

	if !res.Used {
		return reviewsapp.IntentResult{Trace: domain.UnavailableIntentTrace(res.Warning)}
	}
	snapshot := res.Record.Snapshot()
	return reviewsapp.IntentResult{Intent: &snapshot, Trace: usedTrace(res)}
}

// usedTrace is trace.intent of a usable result, from the stored row. The call's tokens and cost
// are informational: they never enter a run's usage.
func usedTrace(res intentapp.Result) *domain.IntentTrace {
	rec := res.Record
	derivedFrom := rec.DerivedFrom
	return &domain.IntentTrace{
		Status: domain.IntentStatusUsed, CacheHit: res.CacheHit,
		Confidence: rec.Confidence, DerivedFrom: &derivedFrom, ChangeType: rec.ChangeType,
		Provider: rec.Provider, Model: rec.Model, TokensIn: rec.TokensIn, TokensOut: rec.TokensOut,
		CostUSD: rec.CostUSD, Sources: rec.Sources,
	}
}
