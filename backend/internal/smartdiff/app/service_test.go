package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/smartdiff/app"
	"dev-digest-go/backend/internal/smartdiff/domain"
)

// fakeStore is an in-memory app.Store holding one pull request.
type fakeStore struct {
	ws, pr   string
	files    []domain.PullFile
	findings []domain.FindingRef
	filesErr error
	findErr  error

	findingsCalls int
}

var _ app.Store = (*fakeStore)(nil)

func (f *fakeStore) PullFiles(_ context.Context, ws, pr string) ([]domain.PullFile, error) {
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	if ws != f.ws || pr != f.pr {
		return nil, domain.ErrNotFound
	}
	return f.files, nil
}

func (f *fakeStore) CurrentFindings(_ context.Context, pr string) ([]domain.FindingRef, error) {
	f.findingsCalls++
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findings, nil
}

func TestGet_BuildsTheReadModel(t *testing.T) {
	store := &fakeStore{
		ws: "w", pr: "p",
		files:    []domain.PullFile{{Path: "b.go", Additions: 2, Deletions: 1}, {Path: "a.go", Additions: 1}},
		findings: []domain.FindingRef{{File: "a.go", StartLine: 9}},
	}
	got, err := app.NewService(store).Get(t.Context(), "w", "p")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := domain.Build(store.files, store.findings)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get() mismatch (-want +got):\n%s", diff)
	}
	if got.TotalLines != 4 || got.Groups[0].Files[0].Path != "a.go" || got.Groups[0].Files[0].FindingLines[0] != 9 {
		t.Errorf("Get() = %+v, want files sorted by path with a.go's finding line 9 and 4 total lines", got)
	}
}

func TestGet_UnknownPullRequestIs404(t *testing.T) {
	store := &fakeStore{ws: "w", pr: "p"}
	_, err := app.NewService(store).Get(t.Context(), "other", "p")
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindNotFound || ae.Message != "Pull request not found" {
		t.Fatalf("Get(other workspace) error = %v, want not_found \"Pull request not found\"", err)
	}
	if store.findingsCalls != 0 {
		t.Errorf("findings read %d times for a missing pull request, want 0", store.findingsCalls)
	}
}

func TestGet_StoreFailureIsNotMaskedAs404(t *testing.T) {
	boom := errors.New("connection lost")
	for _, tc := range []struct {
		name  string
		store *fakeStore
	}{
		{"files", &fakeStore{filesErr: boom}},
		{"findings", &fakeStore{ws: "w", pr: "p", findErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.NewService(tc.store).Get(t.Context(), "w", "p")
			if !errors.Is(err, boom) {
				t.Fatalf("Get() error = %v, want it to wrap %v", err, boom)
			}
			if _, isApp := apperr.As(err); isApp {
				t.Errorf("Get() error = %v is an apperr, want a plain error (500)", err)
			}
		})
	}
}

func TestGet_PullRequestWithoutFilesIsFiveEmptyGroups(t *testing.T) {
	store := &fakeStore{ws: "w", pr: "p", findings: []domain.FindingRef{{File: "a.go", StartLine: 1}}}
	got, err := app.NewService(store).Get(t.Context(), "w", "p")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.Groups) != 5 || got.TotalLines != 0 {
		t.Errorf("Get() = %d groups, %d lines, want 5 groups, 0 lines", len(got.Groups), got.TotalLines)
	}
}
