package peers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	intentapp "dev-digest-go/backend/internal/intent/app"
	intent "dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

func ptr[T any](v T) *T { return &v }

// fakeIntentService records its input and answers with a canned result, sending the given events
// to the sink first.
type fakeIntentService struct {
	in     intentapp.ResolveInput
	events []ev
	res    intentapp.Result
	sinkOK bool
}

type ev struct {
	Kind, Msg string
	Data      string
}

func (f *fakeIntentService) Resolve(_ context.Context, in intentapp.ResolveInput, sink intentapp.Sink) intentapp.Result {
	f.in = in
	f.sinkOK = sink != nil
	for _, e := range f.events {
		if sink != nil {
			sink(e.Kind, e.Msg, json.RawMessage(e.Data))
		}
	}
	return f.res
}

func request() reviewsapp.IntentRequest {
	return reviewsapp.IntentRequest{
		WorkspaceID: "ws-1",
		Pull: domain.ReviewPull{
			ID: "pr-1", WorkspaceID: "ws-1", RepoID: "repo-1", Number: 482, Title: "Add rate limiting", Author: "dev",
			Base: "main", Branch: "feat/rate-limit", HeadSHA: "0123456789abcdef", Body: ptr("The body"),
		},
		Repo:         domain.ReviewRepo{ID: "repo-1", Owner: "acme", Name: "api"},
		ChangedFiles: []string{"a.go", "b.go"},
		DiffExcerpt:  "diff --git a/a.go b/a.go",
	}
}

func usedRecord() intent.Record {
	return intent.Record{
		PRID: "pr-1", Text: "Add a limiter.", InScope: []string{"limiter"}, OutOfScope: []string{"auth"},
		ChangeType: ptr("feature"), Confidence: ptr("high"), DerivedFrom: "explicit",
		Sources:  []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}},
		Provider: "openrouter", Model: "deepseek/deepseek-v4-flash", TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042),
	}
}

func TestIntent_ResolveMapsTheRequestOntoTheDerivation(t *testing.T) {
	svc := &fakeIntentService{res: intentapp.Result{Used: true, Record: usedRecord()}}

	peers.NewIntent(svc).Resolve(t.Context(), request(), func(string, string, json.RawMessage) {})

	want := intentapp.ResolveInput{
		WorkspaceID: "ws-1",
		Pull:        intent.Pull{ID: "pr-1", RepoID: "repo-1", Number: 482, Title: "Add rate limiting", Body: "The body", Branch: "feat/rate-limit", HeadSHA: "0123456789abcdef"},
		Repo:        intent.Repo{ID: "repo-1", Owner: "acme", Name: "api"}, ChangedFiles: []string{"a.go", "b.go"},
		DiffExcerpt: "diff --git a/a.go b/a.go",
	}
	if diff := cmp.Diff(want, svc.in); diff != "" {
		t.Errorf("derivation input mismatch (-want +got):\n%s", diff)
	}
	if svc.in.Force {
		t.Error("Force = true, want the review path never forced")
	}
}

func TestIntent_CarriesTheGitLabRepositoryLocationAndWebURL(t *testing.T) {
	svc := &fakeIntentService{res: intentapp.Result{Warning: "x"}}
	req := request()
	req.Repo = domain.ReviewRepo{ID: "repo-1", Owner: "grp/sub", Name: "web", Provider: "gitlab",
		Host: "git.corp.example", ProjectID: 4242, WebURL: "https://git.corp.example/grp/sub/web"}

	peers.NewIntent(svc).Resolve(t.Context(), req, nil)

	want := intent.Repo{ID: "repo-1", Owner: "grp/sub", Name: "web", Provider: "gitlab",
		Host: "git.corp.example", ProjectID: 4242, WebURL: "https://git.corp.example/grp/sub/web"}
	if diff := cmp.Diff(want, svc.in.Repo); diff != "" {
		t.Errorf("derivation repo mismatch (-want +got):\n%s", diff)
	}
}

func TestIntent_ANilBodyIsAnEmptyBody(t *testing.T) {
	svc := &fakeIntentService{res: intentapp.Result{Warning: "x"}}
	req := request()
	req.Pull.Body = nil

	peers.NewIntent(svc).Resolve(t.Context(), req, nil)

	if svc.in.Pull.Body != "" {
		t.Errorf("Body = %q, want empty", svc.in.Pull.Body)
	}
	if svc.sinkOK {
		t.Error("sink set with a nil emit, want none")
	}
}

func TestIntent_ResolveUsedResult(t *testing.T) {
	tests := []struct {
		name     string
		cacheHit bool
	}{{"fresh derivation", false}, {"cache hit", true}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeIntentService{res: intentapp.Result{Used: true, Record: usedRecord(), CacheHit: tc.cacheHit}}

			got := peers.NewIntent(svc).Resolve(t.Context(), request(), nil)

			wantIntent := &reviewer.Intent{
				Text: "Add a limiter.", InScope: []string{"limiter"}, OutOfScope: []string{"auth"}, ChangeType: ptr("feature"),
				Confidence: ptr("high"), DerivedFrom: "explicit", Sources: []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}},
			}
			wantTrace := &domain.IntentTrace{
				Status: "used", CacheHit: tc.cacheHit, Confidence: ptr("high"), DerivedFrom: ptr("explicit"), ChangeType: ptr("feature"),
				Provider: "openrouter", Model: "deepseek/deepseek-v4-flash", TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042),
				Sources: []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}},
			}
			if diff := cmp.Diff(reviewsapp.IntentResult{Intent: wantIntent, Trace: wantTrace}, got); diff != "" {
				t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIntent_ResolveUnavailableResult(t *testing.T) {
	svc := &fakeIntentService{res: intentapp.Result{Warning: "OPENROUTER_API_KEY is not configured"}}

	got := peers.NewIntent(svc).Resolve(t.Context(), request(), nil)

	want := reviewsapp.IntentResult{Trace: domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured")}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
	}
}

func TestIntent_ResolveForwardsTheEventsInOrder(t *testing.T) {
	svc := &fakeIntentService{
		events: []ev{
			{"info", "PR intent sources: 2 used, 0 skipped, 0 failed", ""},
			{"result", "PR intent derived (confidence=high, 1200+180 tok, $0.0004)", ""},
			{"info", "warning: intent unavailable — x", `{"warning":"intent_unavailable"}`},
		},
		res: intentapp.Result{Used: true, Record: usedRecord()},
	}
	var got []ev

	peers.NewIntent(svc).Resolve(t.Context(), request(), func(kind, msg string, data json.RawMessage) {
		got = append(got, ev{kind, msg, string(data)})
	})

	if diff := cmp.Diff(svc.events, got); diff != "" {
		t.Errorf("emitted events mismatch (-want +got):\n%s", diff)
	}
}

func TestNewIntent_NilServicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewIntent(nil) did not panic")
		}
	}()
	peers.NewIntent(nil)
}
