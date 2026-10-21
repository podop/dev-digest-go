package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/domain"
)

func ptr[T any](v T) *T { return &v }

func TestDeriveStatus(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		stored   string
		reviewed *string
		head     string
		updated  *time.Time
		want     string
	}{
		{name: "merged stays merged", stored: "merged", reviewed: ptr("h"), head: "h", updated: ptr(now), want: "merged"},
		{name: "closed stays closed", stored: "closed", head: "h", want: "closed"},
		{name: "never reviewed", stored: "open", head: "h", want: "needs_review"},
		{name: "head moved", stored: "open", reviewed: ptr("old"), head: "h", updated: ptr(now), want: "needs_review"},
		{name: "reviewed 3 days ago", stored: "open", reviewed: ptr("h"), head: "h", updated: ptr(now.Add(-3 * 24 * time.Hour)), want: "reviewed"},
		{name: "exactly 7 days is not stale", stored: "open", reviewed: ptr("h"), head: "h", updated: ptr(now.Add(-7 * 24 * time.Hour)), want: "reviewed"},
		{name: "8 days is stale", stored: "open", reviewed: ptr("h"), head: "h", updated: ptr(now.Add(-8 * 24 * time.Hour)), want: "stale"},
		{name: "no updated_at is not stale", stored: "open", reviewed: ptr("h"), head: "h", want: "reviewed"},
		{name: "seeded needs_review follows the same rule", stored: "needs_review", head: "h", want: "needs_review"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.DeriveStatus(tc.stored, tc.reviewed, tc.head, tc.updated, now)
			if got != tc.want {
				t.Errorf("DeriveStatus(%q, %v, %q, %v) = %q, want %q", tc.stored, tc.reviewed, tc.head, tc.updated, got, tc.want)
			}
		})
	}
}

func TestNeedsDiffStats(t *testing.T) {
	tests := []struct {
		name string
		in   domain.DiffStats
		want bool
	}{
		{"all zero", domain.DiffStats{}, true},
		{"additions", domain.DiffStats{Additions: 1}, false},
		{"deletions", domain.DiffStats{Deletions: 1}, false},
		{"files", domain.DiffStats{FilesCount: 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.NeedsDiffStats(tc.in); got != tc.want {
				t.Errorf("NeedsDiffStats(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLinkedIssueNumber(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		want   int
		wantOK bool
	}{
		{name: "fixes", body: "Fixes #12", want: 12, wantOK: true},
		{name: "closes lower case", body: "closes #7 and more", want: 7, wantOK: true},
		{name: "resolves upper case", body: "RESOLVES #3", want: 3, wantOK: true},
		{name: "bare reference", body: "see #99 for context", want: 99, wantOK: true},
		{name: "first of several", body: "#5 then #6", want: 5, wantOK: true},
		{name: "no digits", body: "issue # here", wantOK: false},
		{name: "empty", body: "", wantOK: false},
		{name: "zero is not an issue", body: "#0", wantOK: false},
		{name: "overflow", body: "#99999999999999999999999", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.LinkedIssueNumber(tc.body)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("LinkedIssueNumber(%q) = (%d, %v), want (%d, %v)", tc.body, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestMergeFiles(t *testing.T) {
	in := []domain.File{
		{Path: "a.go", Additions: 1, Deletions: 2, Patch: ptr("p1")},
		{Path: "b.go", Additions: 5, Patch: nil},
		{Path: "a.go", Additions: 3, Deletions: 4, Patch: ptr("p2")},
		{Path: "b.go", Additions: 1, Patch: ptr("")},
		{Path: "c.go", Patch: ptr("only")},
		{Path: "c.go", Patch: nil},
	}
	want := []domain.File{
		{Path: "a.go", Additions: 4, Deletions: 6, Patch: ptr("p1\np2")},
		{Path: "b.go", Additions: 6, Patch: nil},
		{Path: "c.go", Patch: ptr("only")},
	}
	got := domain.MergeFiles(in)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MergeFiles() mismatch (-want +got):\n%s", diff)
	}
}
