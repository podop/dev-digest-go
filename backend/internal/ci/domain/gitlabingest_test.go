package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

var glNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

const glProject int64 = 77

func glPipeline() domain.GitLabPipeline {
	return domain.GitLabPipeline{
		ID: 5, ProjectID: glProject, SHA: "abc123", Ref: "refs/merge-requests/12/head",
		Source: "merge_request_event",
	}
}

func glJob() domain.GitLabJob {
	return domain.GitLabJob{
		ID: 900, Name: "devdigest-review-security", Status: "success",
		CreatedAt: "2026-10-27T10:11:12.345Z", ArtifactsFile: &domain.GitLabArtifactsFile{Size: 300},
	}
}

func glArtifact() domain.CiResultArtifact {
	return domain.CiResultArtifact{
		FindingsCount: 2, CommitSHA: ptr("abc123"), RepositoryID: ptr(glProject), RunID: ptr(int64(900)),
		PRNumber: ptr(12), Model: ptr("m"), CostUSD: ptr(0.5), DurationMS: ptr(1500), Blockers: ptr(1),
	}
}

func TestMRIIDFromRef(t *testing.T) {
	tests := []struct {
		ref  string
		want int
		ok   bool
	}{
		{"refs/merge-requests/12/head", 12, true},
		{"refs/merge-requests/7/merge", 7, true},
		{"refs/merge-requests/2147483647/head", 2147483647, true},
		{"refs/merge-requests/2147483648/head", 0, false},
		{"refs/merge-requests/0/head", 0, false},
		{"refs/merge-requests/012/head", 0, false},
		{"refs/merge-requests/-1/head", 0, false},
		{"refs/merge-requests/+1/head", 0, false},
		{"refs/merge-requests/1_0/head", 0, false},
		{"refs/merge-requests/١٢/head", 0, false},
		{"refs/merge-requests//head", 0, false},
		{"refs/merge-requests/12/train", 0, false},
		{"refs/merge-requests/12/head/x", 0, false},
		{"refs/merge-requests/12", 0, false},
		{"refs/merge-requests/12/", 0, false},
		{"refs/heads/main", 0, false},
		{"refs/tags/refs/merge-requests/1/head", 0, false},
		{"/refs/merge-requests/1/head", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		got, ok := domain.MRIIDFromRef(tc.ref)
		if got != tc.want || ok != tc.ok {
			t.Errorf("MRIIDFromRef(%q) = %d, %v, want %d, %v", tc.ref, got, ok, tc.want, tc.ok)
		}
	}
}

func FuzzMRIIDFromRef(f *testing.F) {
	for _, s := range []string{"refs/merge-requests/12/head", "refs/merge-requests/99999999999/merge", "x", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		n, ok := domain.MRIIDFromRef(ref)
		if ok && (n <= 0 || !strings.HasPrefix(ref, "refs/merge-requests/")) {
			t.Fatalf("MRIIDFromRef(%q) = %d, true", ref, n)
		}
		if !ok && n != 0 {
			t.Fatalf("MRIIDFromRef(%q) = %d, false", ref, n)
		}
	})
}

func TestGitLabErrorCode(t *testing.T) {
	tests := []struct {
		status int
		want   domain.ErrorCode
	}{
		{429, "gitlab_rate_limited"},
		{401, "gitlab_permission_denied"},
		{403, "gitlab_permission_denied"},
		{500, "gitlab_unavailable"},
		{502, "gitlab_unavailable"},
		{404, "gitlab_unavailable"},
		{0, "gitlab_unavailable"},
	}
	for _, tc := range tests {
		if got := domain.GitLabErrorCode(tc.status); got != tc.want {
			t.Errorf("GitLabErrorCode(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
	if domain.CodeRepoHostNotConnected != "repo_host_not_connected" || domain.CodeGitLabTokenMissing != "gitlab_token_missing" {
		t.Errorf("FR31 codes = %q, %q", domain.CodeRepoHostNotConnected, domain.CodeGitLabTokenMissing)
	}
}

func TestSelectGitLabCandidate(t *testing.T) {
	job := func(id int64, name, status string) domain.GitLabJob {
		j := glJob()
		j.ID, j.Name, j.Status = id, name, status
		return j
	}
	const name = "devdigest-review-security"
	tests := []struct {
		name   string
		mutate func(*domain.GitLabPipeline)
		jobs   []domain.GitLabJob
		wantID int64
		wantIQ int
	}{
		{"success job", nil, []domain.GitLabJob{job(1, name, "success")}, 1, 12},
		{"failed job", nil, []domain.GitLabJob{job(2, name, "failed")}, 2, 12},
		{"other job names skipped", nil, []domain.GitLabJob{job(3, "lint", "success"), job(4, name, "success")}, 4, 12},
		{"name is exact", nil, []domain.GitLabJob{job(5, name+"-x", "success"), job(6, "x"+name, "success")}, 0, 0},
		{"canceled job", nil, []domain.GitLabJob{job(7, name, "canceled")}, 0, 0},
		{"skipped job", nil, []domain.GitLabJob{job(8, name, "skipped")}, 0, 0},
		{"running job", nil, []domain.GitLabJob{job(9, name, "running")}, 0, 0},
		{"no jobs", nil, nil, 0, 0},
		{"highest id of the name wins", nil, []domain.GitLabJob{job(10, name, "failed"), job(30, name, "success"), job(20, name, "success")}, 30, 12},
		{"unfinished newer job is ignored", nil, []domain.GitLabJob{job(10, name, "failed"), job(30, name, "running")}, 10, 12},
		{"merge ref", func(p *domain.GitLabPipeline) { p.Ref = "refs/merge-requests/3/merge" }, []domain.GitLabJob{job(1, name, "success")}, 1, 3},
		{"branch pipeline ref", func(p *domain.GitLabPipeline) { p.Ref = "main" }, []domain.GitLabJob{job(1, name, "success")}, 0, 0},
		{"other project", func(p *domain.GitLabPipeline) { p.ProjectID = glProject + 1 }, []domain.GitLabJob{job(1, name, "success")}, 0, 0},
		{"push source", func(p *domain.GitLabPipeline) { p.Source = "push" }, []domain.GitLabJob{job(1, name, "success")}, 0, 0},
		{"no source", func(p *domain.GitLabPipeline) { p.Source = "" }, []domain.GitLabJob{job(1, name, "success")}, 0, 0},
		{"job id zero", nil, []domain.GitLabJob{job(0, name, "success")}, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := glPipeline()
			if tc.mutate != nil {
				tc.mutate(&p)
			}
			got, ok := domain.SelectGitLabCandidate(p, tc.jobs, glProject, "security")
			if ok != (tc.wantID != 0) {
				t.Fatalf("SelectGitLabCandidate() ok = %v, want %v", ok, tc.wantID != 0)
			}
			if ok && (got.Job.ID != tc.wantID || got.MRIID != tc.wantIQ || got.Pipeline.ID != p.ID) {
				t.Errorf("SelectGitLabCandidate() = job %d, iid %d, pipeline %d, want job %d, iid %d, pipeline %d",
					got.Job.ID, got.MRIID, got.Pipeline.ID, tc.wantID, tc.wantIQ, p.ID)
			}
		})
	}
	if _, ok := domain.SelectGitLabCandidate(glPipeline(), []domain.GitLabJob{job(1, name, "success")}, 0, "security"); ok {
		t.Error("SelectGitLabCandidate(project 0) = true, want false")
	}
}

func TestGitLabKeyOf(t *testing.T) {
	want := domain.RunKey{GithubRunID: 900, Attempt: 1}
	if got := domain.GitLabKeyOf(glJob()); got != want {
		t.Errorf("GitLabKeyOf() = %+v, want %+v", got, want)
	}
}

func TestGitLabFromBaseProject(t *testing.T) {
	tests := []struct {
		name            string
		source, project int64
		want            bool
	}{
		{"same", 77, 77, true},
		{"fork", 78, 77, false},
		{"unknown source", 0, 77, false},
		{"both unknown", 0, 0, false},
	}
	for _, tc := range tests {
		if got := domain.GitLabFromBaseProject(tc.source, tc.project); got != tc.want {
			t.Errorf("%s: GitLabFromBaseProject(%d, %d) = %v, want %v", tc.name, tc.source, tc.project, got, tc.want)
		}
	}
}

func TestPlanGitLabArtifact(t *testing.T) {
	job := func(mutate func(*domain.GitLabJob)) domain.GitLabJob {
		j := glJob()
		mutate(&j)
		return j
	}
	tests := []struct {
		name string
		job  domain.GitLabJob
		want domain.GitLabArtifactPlan
	}{
		{"no artifacts", job(func(j *domain.GitLabJob) { j.ArtifactsFile = nil }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactNone}},
		{"fetch", job(func(*domain.GitLabJob) {}),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactFetch}},
		{"expiry in the future", job(func(j *domain.GitLabJob) { j.ArtifactsExpireAt = "2026-10-28T00:00:00.000Z" }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactFetch}},
		{"expired", job(func(j *domain.GitLabJob) { j.ArtifactsExpireAt = "2026-10-26T00:00:00.000Z" }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipExpired}},
		{"expires right now", job(func(j *domain.GitLabJob) { j.ArtifactsExpireAt = "2026-10-27T12:00:00Z" }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipExpired}},
		{"expiry does not parse", job(func(j *domain.GitLabJob) { j.ArtifactsExpireAt = "tomorrow" }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipExpired}},
		{"archive at the limit", job(func(j *domain.GitLabJob) { j.ArtifactsFile.Size = 1_048_576 }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactFetch}},
		{"archive over the limit (2 MB)", job(func(j *domain.GitLabJob) { j.ArtifactsFile.Size = 2 << 20 }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipTooLarge}},
		{"archive one byte over", job(func(j *domain.GitLabJob) { j.ArtifactsFile.Size = 1_048_577 }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipTooLarge}},
		{"negative size", job(func(j *domain.GitLabJob) { j.ArtifactsFile.Size = -1 }),
			domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipTooLarge}},
		{"expired wins over too large", job(func(j *domain.GitLabJob) {
			j.ArtifactsExpireAt = "2026-10-26T00:00:00Z"
			j.ArtifactsFile.Size = 2 << 20
		}), domain.GitLabArtifactPlan{Action: domain.GitLabArtifactSkip, Skip: domain.SkipExpired}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.PlanGitLabArtifact(tc.job, glNow); got != tc.want {
				t.Errorf("PlanGitLabArtifact() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGitLabResultTooLarge(t *testing.T) {
	if domain.GitLabResultReadCap != 65_537 {
		t.Errorf("GitLabResultReadCap = %d, want 65537", domain.GitLabResultReadCap)
	}
	for _, tc := range []struct {
		n    int
		want bool
	}{{0, false}, {65_536, false}, {65_537, true}} {
		if got := domain.GitLabResultTooLarge(make([]byte, tc.n)); got != tc.want {
			t.Errorf("GitLabResultTooLarge(%d bytes) = %v, want %v", tc.n, got, tc.want)
		}
	}
}

func TestVerifyGitLabArtifact(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*domain.CiResultArtifact, *domain.GitLabPipeline, *domain.GitLabJob)
		want bool
	}{
		{"matching", func(*domain.CiResultArtifact, *domain.GitLabPipeline, *domain.GitLabJob) {}, true},
		{"repository path of the artifact is not compared", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.Repository = ptr("someone/else")
		}, true},
		{"other repository id (EC28)", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.RepositoryID = ptr(glProject + 1)
		}, false},
		{"run id differs (EC28)", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.RunID = ptr(int64(901))
		}, false},
		{"commit differs (EC28)", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.CommitSHA = ptr("def456")
		}, false},
		{"commit case differs", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.CommitSHA = ptr("ABC123")
		}, false},
		{"commit missing", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.CommitSHA = nil
		}, false},
		{"repository id missing", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.RepositoryID = nil
		}, false},
		{"run id missing", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.RunID = nil
		}, false},
		{"pipeline of another project", func(_ *domain.CiResultArtifact, p *domain.GitLabPipeline, _ *domain.GitLabJob) {
			p.ProjectID = glProject + 1
		}, false},
		{"pipeline without sha", func(a *domain.CiResultArtifact, p *domain.GitLabPipeline, _ *domain.GitLabJob) {
			p.SHA, a.CommitSHA = "", ptr("")
		}, false},
		{"job of another name", func(_ *domain.CiResultArtifact, _ *domain.GitLabPipeline, j *domain.GitLabJob) {
			j.Name = "devdigest-review-other"
		}, false},
		{"pipeline source is a push", func(_ *domain.CiResultArtifact, p *domain.GitLabPipeline, _ *domain.GitLabJob) {
			p.Source = "push"
		}, false},
		{"run attempt is not compared", func(a *domain.CiResultArtifact, _ *domain.GitLabPipeline, _ *domain.GitLabJob) {
			a.RunAttempt = ptr(3)
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, p, j := glArtifact(), glPipeline(), glJob()
			tc.mut(&a, &p, &j)
			if got := domain.VerifyGitLabArtifact(a, p, j, glProject, "security"); got != tc.want {
				t.Errorf("VerifyGitLabArtifact() = %v, want %v", got, tc.want)
			}
		})
	}
	a, p, j := glArtifact(), glPipeline(), glJob()
	if domain.VerifyGitLabArtifact(a, p, j, glProject+1, "security") {
		t.Error("VerifyGitLabArtifact(installation of another project) = true, want false")
	}
	if domain.VerifyGitLabArtifact(a, p, j, glProject, "other") {
		t.Error("VerifyGitLabArtifact(other slug) = true, want false")
	}
	zero := domain.GitLabPipeline{SHA: "x", Source: "merge_request_event"}
	za := domain.CiResultArtifact{CommitSHA: ptr("x"), RepositoryID: ptr(int64(0)), RunID: ptr(int64(0))}
	if domain.VerifyGitLabArtifact(za, zero, domain.GitLabJob{Name: "devdigest-review-security"}, 0, "security") {
		t.Error("VerifyGitLabArtifact(all zero ids) = true, want false")
	}
}

func TestGitLabJobURL(t *testing.T) {
	tests := []struct {
		name, base, path string
		id               int64
		want             string
	}{
		{"plain", "https://gitlab.com", "grp/proj", 900, "https://gitlab.com/grp/proj/-/jobs/900"},
		{"subgroups", "https://gitlab.com", "a/b/c/proj", 1, "https://gitlab.com/a/b/c/proj/-/jobs/1"},
		{"trailing slash and prefix", "https://host.example:8443/gl/", "grp/proj", 5, "https://host.example:8443/gl/grp/proj/-/jobs/5"},
		{"stray slashes in the path", "https://gitlab.com", "/grp/proj/", 5, "https://gitlab.com/grp/proj/-/jobs/5"},
		{"segments are escaped", "https://gitlab.com", "grp/p r?#oj", 5, "https://gitlab.com/grp/p%20r%3F%23oj/-/jobs/5"},
		{"no base", "", "grp/proj", 5, ""},
		{"no path", "https://gitlab.com", "", 5, ""},
		{"only slashes", "https://gitlab.com", "//", 5, ""},
		{"no id", "https://gitlab.com", "grp/proj", 0, ""},
		{"negative id", "https://gitlab.com", "grp/proj", -3, ""},
	}
	for _, tc := range tests {
		if got := domain.GitLabJobURL(tc.base, tc.path, tc.id); got != tc.want {
			t.Errorf("%s: GitLabJobURL(%q, %q, %d) = %q, want %q", tc.name, tc.base, tc.path, tc.id, got, tc.want)
		}
	}
}

func glInput() domain.GitLabRunInput {
	return domain.GitLabRunInput{
		WorkspaceID: "ws1",
		Installation: domain.Installation{
			ID: "inst1", AgentID: "agent1", Repo: "grp/proj", ManifestVersion: ptr(4),
		},
		Host: "gitlab.com", BaseURL: "https://gitlab.com",
		Pipeline: glPipeline(), Job: glJob(), MRIID: 12, Now: glNow,
	}
}

func TestNewGitLabRun_FromArtifact(t *testing.T) {
	in := glInput()
	a := glArtifact()
	in.Artifact = &a
	got := domain.NewGitLabRun(in)

	want := domain.GitLabNewRun{
		NewRun: domain.NewRun{
			WorkspaceID: "ws1", InstallationID: "inst1", AgentID: "agent1",
			GithubRunID: 900, RunAttempt: 1, HeadSHA: "abc123",
			GithubURL: "https://gitlab.com/grp/proj/-/jobs/900",
			RanAt:     time.Date(2026, 10, 27, 10, 11, 12, 345_000_000, time.UTC),
			PRNumber:  ptr(12), ManifestVersion: ptr(4), Status: domain.RunSucceeded,
			Model: ptr("m"), CostUSD: ptr(0.5), DurationMS: ptr(1500), FindingsCount: 2, Blockers: 1,
			FromArtifact: true,
		},
		Host: "gitlab.com", Source: "gitlab_ci",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewGitLabRun() mismatch (-want +got):\n%s", diff)
	}
}

func TestNewGitLabRun_ArtifactDetails(t *testing.T) {
	t.Run("no findings", func(t *testing.T) {
		in, a := glInput(), glArtifact()
		a.FindingsCount, a.Blockers = 0, nil
		in.Artifact = &a
		got := domain.NewGitLabRun(in)
		if got.Status != domain.RunNoFindings || got.Blockers != 0 {
			t.Errorf("NewGitLabRun() = status %q, blockers %d, want no_findings, 0", got.Status, got.Blockers)
		}
	})
	t.Run("pr number falls back to the iid", func(t *testing.T) {
		in, a := glInput(), glArtifact()
		a.PRNumber = nil
		in.Artifact = &a
		got := domain.NewGitLabRun(in)
		if got.PRNumber == nil || *got.PRNumber != 12 {
			t.Errorf("NewGitLabRun().PRNumber = %v, want 12", got.PRNumber)
		}
	})
	t.Run("artifact pr number wins", func(t *testing.T) {
		in, a := glInput(), glArtifact()
		a.PRNumber = ptr(99)
		in.Artifact = &a
		if got := domain.NewGitLabRun(in); got.PRNumber == nil || *got.PRNumber != 99 {
			t.Errorf("NewGitLabRun().PRNumber = %v, want 99", got.PRNumber)
		}
	})
	t.Run("artifact manifest version wins", func(t *testing.T) {
		in, a := glInput(), glArtifact()
		a.ManifestVersion = ptr(9)
		in.Artifact = &a
		if got := domain.NewGitLabRun(in); got.ManifestVersion == nil || *got.ManifestVersion != 9 {
			t.Errorf("NewGitLabRun().ManifestVersion = %v, want 9", got.ManifestVersion)
		}
	})
	t.Run("head sha is the artifact commit", func(t *testing.T) {
		in, a := glInput(), glArtifact()
		in.Artifact = &a
		in.Pipeline.SHA = "other"
		if got := domain.NewGitLabRun(in); got.HeadSHA != "abc123" {
			t.Errorf("NewGitLabRun().HeadSHA = %q, want the artifact's abc123", got.HeadSHA)
		}
	})
}

func TestNewGitLabRun_FailedWithoutArtifact(t *testing.T) {
	in := glInput()
	in.Job.Status = "failed"
	in.Job.ArtifactsFile = nil
	got := domain.NewGitLabRun(in)

	msg := domain.FailedRunError
	want := domain.GitLabNewRun{
		NewRun: domain.NewRun{
			WorkspaceID: "ws1", InstallationID: "inst1", AgentID: "agent1",
			GithubRunID: 900, RunAttempt: 1, HeadSHA: "abc123",
			GithubURL: "https://gitlab.com/grp/proj/-/jobs/900",
			RanAt:     time.Date(2026, 10, 27, 10, 11, 12, 345_000_000, time.UTC),
			PRNumber:  ptr(12), ManifestVersion: ptr(4), Status: domain.RunFailed, Error: &msg,
		},
		Host: "gitlab.com", Source: "gitlab_ci",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewGitLabRun(no artifact) mismatch (-want +got):\n%s", diff)
	}
	if got.FromArtifact {
		t.Error("NewGitLabRun(no artifact).FromArtifact = true, want false (no ledger entry)")
	}
}

func TestNewGitLabRun_RanAtFallback(t *testing.T) {
	in := glInput()
	in.Job.CreatedAt = "not a time"
	if got := domain.NewGitLabRun(in); !got.RanAt.Equal(glNow) {
		t.Errorf("NewGitLabRun().RanAt = %v, want now %v", got.RanAt, glNow)
	}
}
