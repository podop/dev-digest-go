package peers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/blast/adapters/peers"
	"dev-digest-go/backend/internal/blast/domain"
	ghclient "dev-digest-go/backend/internal/platform/github"
	repointel "dev-digest-go/backend/internal/repointel/domain"
)

type fakeReader struct {
	res     repointel.BlastResult
	err     error
	repo    string
	changed []string
}

func (f *fakeReader) BlastRadius(_ context.Context, repoID string, changed []string) (repointel.BlastResult, error) {
	f.repo, f.changed = repoID, changed
	return f.res, f.err
}

func TestRepoIntel_BlastRadiusMapsOneToOne(t *testing.T) {
	r := &fakeReader{res: repointel.BlastResult{
		ChangedSymbols: []repointel.BlastSymbol{{File: "src/a.ts", Name: "foo", Kind: "function"}},
		Callers:        []repointel.BlastCaller{{File: "src/x.ts", Symbol: "h", ViaSymbol: "foo", Line: 4, Rank: 0.5}},
		FactsByFile:    map[string]repointel.BlastFacts{"src/x.ts": {Endpoints: []string{"GET /x"}, Crons: []string{}}},
		Degraded:       true,
		Reason:         repointel.ReasonIndexPartial,
	}}

	got, err := peers.NewRepoIntel(r).BlastRadius(t.Context(), "repo-1", []string{"src/a.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	want := domain.PortResult{
		ChangedSymbols: []domain.ChangedSymbol{{Name: "foo", File: "src/a.ts", Kind: "function"}},
		Callers:        []domain.Caller{{File: "src/x.ts", Symbol: "h", ViaSymbol: "foo", Line: 4, Rank: 0.5}},
		FactsByFile:    map[string]domain.Facts{"src/x.ts": {Endpoints: []string{"GET /x"}, Crons: []string{}}},
		Degraded:       true,
		Reason:         domain.ReasonIndexPartial,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BlastRadius() mismatch (-want +got):\n%s", diff)
	}
	if r.repo != "repo-1" || !cmp.Equal(r.changed, []string{"src/a.ts"}) {
		t.Errorf("reader saw %q %v, want repo-1 [src/a.ts]", r.repo, r.changed)
	}
}

// The clone-scan answer has no facts: FactsByFile stays nil, the slices stay non-nil.
func TestRepoIntel_BlastRadiusDegradedScanKeepsFactsNil(t *testing.T) {
	r := &fakeReader{res: repointel.BlastResult{
		ChangedSymbols: []repointel.BlastSymbol{},
		Callers:        []repointel.BlastCaller{},
		Degraded:       true,
		Reason:         repointel.ReasonFlagOff,
	}}
	got, err := peers.NewRepoIntel(r).BlastRadius(t.Context(), "repo-1", nil)
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	if got.FactsByFile != nil || got.ChangedSymbols == nil || got.Callers == nil || got.Reason != domain.ReasonFlagOff {
		t.Errorf("BlastRadius() = %#v, want nil facts, non-nil slices, reason flag_off", got)
	}
}

func TestRepoIntel_BlastRadiusError(t *testing.T) {
	boom := errors.New("connection reset")
	_, err := peers.NewRepoIntel(&fakeReader{err: boom}).BlastRadius(t.Context(), "repo-1", nil)
	if !errors.Is(err, boom) {
		t.Errorf("BlastRadius() error = %v, want %v", err, boom)
	}
}

type fakeClient struct {
	pulls []ghclient.MergedPull
	err   error
	owner string
	name  string
	limit int
}

func (f *fakeClient) ListMergedPulls(_ context.Context, owner, name string, limit int) ([]ghclient.MergedPull, error) {
	f.owner, f.name, f.limit = owner, name, limit
	return f.pulls, f.err
}

func TestGitHub_ListMergedPullsCopiesFieldsInOrder(t *testing.T) {
	c := &fakeClient{pulls: []ghclient.MergedPull{
		{Number: 4, Title: "Touches a", Author: "ann", MergedAt: "2026-01-01T00:00:00Z", Files: []string{"src/a.ts", "zzz.ts"}},
		{Number: 3, Title: "Unrelated", Author: "unknown", MergedAt: "2026-01-02T00:00:00Z", Files: []string{"other.ts"}},
	}}

	got, err := peers.NewGitHub(c).ListMergedPulls(t.Context(), "acme", "api", 30)
	if err != nil {
		t.Fatalf("ListMergedPulls() error = %v", err)
	}
	want := []domain.MergedPR{
		{Number: 4, Title: "Touches a", Author: "ann", MergedAt: "2026-01-01T00:00:00Z", Files: []string{"src/a.ts", "zzz.ts"}},
		{Number: 3, Title: "Unrelated", Author: "unknown", MergedAt: "2026-01-02T00:00:00Z", Files: []string{"other.ts"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListMergedPulls() mismatch (-want +got):\n%s", diff)
	}
	if c.owner != "acme" || c.name != "api" || c.limit != 30 {
		t.Errorf("client saw %s/%s limit %d, want acme/api 30", c.owner, c.name, c.limit)
	}
}

// P7: a missing token reads as the setting the user has to fill in.
func TestGitHub_ListMergedPullsNoTokenMessage(t *testing.T) {
	_, err := peers.NewGitHub(&fakeClient{err: ghclient.ErrNoToken}).ListMergedPulls(t.Context(), "acme", "api", 30)
	if err == nil || err.Error() != "GITHUB_TOKEN is not configured" {
		t.Errorf("ListMergedPulls(no token) error = %v, want \"GITHUB_TOKEN is not configured\"", err)
	}
	if !errors.Is(err, ghclient.ErrNoToken) {
		t.Errorf("ListMergedPulls(no token) error = %v, want it to match ghclient.ErrNoToken", err)
	}
}

func TestGitHub_ListMergedPullsOtherErrorsPassThrough(t *testing.T) {
	boom := errors.New("GitHub answered 500")
	_, err := peers.NewGitHub(&fakeClient{err: boom}).ListMergedPulls(t.Context(), "acme", "api", 30)
	if !errors.Is(err, boom) {
		t.Errorf("ListMergedPulls() error = %v, want %v", err, boom)
	}
}
