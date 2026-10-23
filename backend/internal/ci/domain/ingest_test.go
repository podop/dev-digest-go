package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

var testNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func TestInspectable(t *testing.T) {
	tests := []struct {
		name string
		c    *string
		want bool
	}{
		{"success", ptr("success"), true},
		{"failure", ptr("failure"), true},
		{"timed out", ptr("timed_out"), true},
		{"skipped", ptr("skipped"), false},
		{"cancelled", ptr("cancelled"), false},
		{"null", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Inspectable(domain.WorkflowRun{Conclusion: tc.c}); got != tc.want {
				t.Errorf("Inspectable(%v) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

func TestKeyOf_AttemptDefaultsToOne(t *testing.T) {
	if got, want := domain.KeyOf(domain.WorkflowRun{ID: 9}), (domain.RunKey{GithubRunID: 9, Attempt: 1}); got != want {
		t.Errorf("KeyOf(no attempt) = %+v, want %+v", got, want)
	}
	if got, want := domain.KeyOf(domain.WorkflowRun{ID: 9, RunAttempt: 3}), (domain.RunKey{GithubRunID: 9, Attempt: 3}); got != want {
		t.Errorf("KeyOf(attempt 3) = %+v, want %+v", got, want)
	}
}

func TestNewestArtifact(t *testing.T) {
	const name = "devdigest-result-sec"
	a := func(id int64, n, at string) domain.RunArtifact {
		return domain.RunArtifact{ID: id, Name: n, CreatedAt: at}
	}
	tests := []struct {
		name string
		in   []domain.RunArtifact
		want int64
		ok   bool
	}{
		{"none", []domain.RunArtifact{a(1, "other", "2026-01-01T00:00:00Z")}, 0, false},
		{"empty", nil, 0, false},
		{"newest wins", []domain.RunArtifact{a(1, name, "2026-01-01T00:00:00Z"), a(2, name, "2026-01-02T00:00:00Z"), a(3, name, "2026-01-01T12:00:00Z")}, 2, true},
		{"other names ignored", []domain.RunArtifact{a(1, name, "2026-01-01T00:00:00Z"), a(2, "x", "2027-01-01T00:00:00Z")}, 1, true},
		{"tie keeps first", []domain.RunArtifact{a(1, name, "2026-01-01T00:00:00Z"), a(2, name, "2026-01-01T00:00:00Z")}, 1, true},
		{"offsets compared as instants", []domain.RunArtifact{a(1, name, "2026-01-01T00:00:00+02:00"), a(2, name, "2025-12-31T23:00:00Z")}, 2, true},
		{"unparsable never replaces", []domain.RunArtifact{a(1, name, "2026-01-01T00:00:00Z"), a(2, name, "garbage")}, 1, true},
		{"unparsable first is kept", []domain.RunArtifact{a(1, name, "garbage"), a(2, name, "2026-01-01T00:00:00Z")}, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.NewestArtifact(tc.in, name)
			if ok != tc.ok || got.ID != tc.want {
				t.Errorf("NewestArtifact() = id %d, %v; want id %d, %v", got.ID, ok, tc.want, tc.ok)
			}
		})
	}
}

func testInstallation() domain.Installation {
	return domain.Installation{ID: "inst", AgentID: "agent", Repo: "o/r", ManifestVersion: ptr(2)}
}

func testRun() domain.WorkflowRun {
	return domain.WorkflowRun{
		ID: 100, RunAttempt: 2, HeadSHA: "sha", HTMLURL: "https://github.com/o/r/actions/runs/100",
		PRNumbers: []int{7, 8}, CreatedAt: "2026-10-20T10:00:00Z",
	}
}

func TestNewRunFromArtifact(t *testing.T) {
	ranAt := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	base := domain.NewRun{
		WorkspaceID: "ws", InstallationID: "inst", AgentID: "agent", GithubRunID: 100, RunAttempt: 2,
		HeadSHA: "sha", GithubURL: "https://github.com/o/r/actions/runs/100", RanAt: ranAt,
	}
	t.Run("artifact values win", func(t *testing.T) {
		a := domain.CiResultArtifact{
			FindingsCount: 3, CostUSD: ptr(0.5), DurationMS: ptr(900), PRNumber: ptr(17), ManifestVersion: ptr(5),
			Model: ptr("m"), Blockers: ptr(2),
		}
		want := base
		want.PRNumber, want.ManifestVersion = ptr(17), ptr(5)
		want.Status, want.Model, want.CostUSD, want.DurationMS = domain.RunSucceeded, ptr("m"), ptr(0.5), ptr(900)
		want.FindingsCount, want.Blockers = 3, 2
		got := domain.NewRunFromArtifact("ws", testInstallation(), testRun(), a, testNow)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("NewRunFromArtifact() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("fallbacks and no findings", func(t *testing.T) {
		want := base
		want.PRNumber, want.ManifestVersion = ptr(7), ptr(2)
		want.Status = domain.RunNoFindings
		got := domain.NewRunFromArtifact("ws", testInstallation(), testRun(), domain.CiResultArtifact{}, testNow)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("NewRunFromArtifact() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("no pull request anywhere", func(t *testing.T) {
		run := testRun()
		run.PRNumbers = nil
		got := domain.NewRunFromArtifact("ws", testInstallation(), run, domain.CiResultArtifact{}, testNow)
		if got.PRNumber != nil {
			t.Errorf("PRNumber = %v, want nil", *got.PRNumber)
		}
	})
	t.Run("unparsable created_at uses now", func(t *testing.T) {
		run := testRun()
		run.CreatedAt = "never"
		got := domain.NewRunFromArtifact("ws", testInstallation(), run, domain.CiResultArtifact{}, testNow)
		if !got.RanAt.Equal(testNow) {
			t.Errorf("RanAt = %v, want %v", got.RanAt, testNow)
		}
	})
}

func TestNewFailedRun(t *testing.T) {
	inst := testInstallation()
	inst.ManifestVersion = nil
	got := domain.NewFailedRun("ws", inst, testRun(), testNow)
	want := domain.NewRun{
		WorkspaceID: "ws", InstallationID: "inst", AgentID: "agent", GithubRunID: 100, RunAttempt: 2,
		HeadSHA: "sha", GithubURL: "https://github.com/o/r/actions/runs/100",
		RanAt: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), PRNumber: ptr(7),
		Status: domain.RunFailed, Error: ptr("The CI run produced no result artifact"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewFailedRun() mismatch (-want +got):\n%s", diff)
	}
}

func TestRunVerdict(t *testing.T) {
	tests := []struct {
		name     string
		status   *string
		blockers *int
		want     *string
	}{
		{"failed has none", ptr("failed"), nil, nil},
		{"failed with blockers has none", ptr("failed"), ptr(3), nil},
		{"blocked", ptr("succeeded"), ptr(1), ptr("blocked")},
		{"passed", ptr("succeeded"), ptr(0), ptr("passed")},
		{"passed without blockers", ptr("no_findings"), nil, ptr("passed")},
		{"passed without status", nil, nil, ptr("passed")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.RunVerdict(domain.Run{Status: tc.status, Blockers: tc.blockers})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RunVerdict() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunDurationSeconds(t *testing.T) {
	if got := domain.RunDurationSeconds(domain.Run{}); got != nil {
		t.Errorf("RunDurationSeconds(nil) = %v, want nil", *got)
	}
	if got := domain.RunDurationSeconds(domain.Run{DurationMS: ptr(1500)}); got == nil || *got != 1.5 {
		t.Errorf("RunDurationSeconds(1500) = %v, want 1.5", got)
	}
}

func TestRunRepo(t *testing.T) {
	tests := []struct {
		name string
		run  domain.Run
		want *string
	}{
		{"installation repo", domain.Run{Repo: ptr("a/b"), GithubURL: ptr("https://github.com/o/r/actions/runs/1")}, ptr("a/b")},
		{"from url", domain.Run{GithubURL: ptr("https://github.com/o/r/actions/runs/1")}, ptr("o/r")},
		{"other host", domain.Run{GithubURL: ptr("https://example.com/o/r/actions/runs/1")}, nil},
		{"http", domain.Run{GithubURL: ptr("http://github.com/o/r/actions/runs/1")}, nil},
		{"no url", domain.Run{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, domain.RunRepo(tc.run)); diff != "" {
				t.Errorf("RunRepo() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWorkflowPathOf(t *testing.T) {
	if got := domain.WorkflowPathOf(domain.Installation{}); got != nil {
		t.Errorf("WorkflowPathOf(no slug) = %q, want nil", *got)
	}
	if got := domain.WorkflowPathOf(domain.Installation{Slug: ptr("")}); got != nil {
		t.Errorf("WorkflowPathOf(empty slug) = %q, want nil", *got)
	}
	got := domain.WorkflowPathOf(domain.Installation{Slug: ptr("sec")})
	if want := ptr(".github/workflows/devdigest-review-sec.yml"); cmp.Diff(want, got) != "" {
		t.Errorf("WorkflowPathOf(sec) = %v, want %q", got, *want)
	}
}
