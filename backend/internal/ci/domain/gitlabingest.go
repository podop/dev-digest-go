package domain

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The GitLab CI ingest rules (spec FR32-FR36, EC27, EC28). Everything here is pure: the use case
// asks GitLab, these functions decide which pipelines and jobs count, whether an artifact is
// fetched, whether it is trusted and what row it becomes. The ledger key of a stored run is the
// spend package's (spec SCR1), not this one's.

// The error codes of a GitLab installation in a refresh (spec 7.1, FR31, FR32).
const (
	CodeGitLabPermissionDenied ErrorCode = "gitlab_permission_denied"
	CodeGitLabRateLimited      ErrorCode = "gitlab_rate_limited"
	CodeGitLabUnavailable      ErrorCode = "gitlab_unavailable"
	CodeRepoHostNotConnected   ErrorCode = "repo_host_not_connected"
	CodeGitLabTokenMissing     ErrorCode = "gitlab_token_missing"
)

// ReasonSourceProjectDiffers is the reason of the WARN line "ci refresh: run not inspected" for a
// merge request whose source project is not the installation's project (FR33).
const ReasonSourceProjectDiffers = "source_project_differs"

const (
	// GitLabAttempt is the run attempt of every GitLab run: a retried job is a new job id (FR36).
	GitLabAttempt = 1
	// GitLabPipelinesPerInstallation is the page size of the merge request pipeline list (FR32).
	GitLabPipelinesPerInstallation = 30
	// GitLabJobsPerPipeline is the page size of the job list of one pipeline (FR32).
	GitLabJobsPerPipeline = 100
	// GitLabResultReadCap is how many bytes of the result file are read: one more than the largest
	// accepted body, so that an oversized body is seen as such (FR34).
	GitLabResultReadCap = MaxArtifactJSONBytes + 1

	// GitLabMergeRequestSource is the pipeline source of a merge request pipeline.
	GitLabMergeRequestSource = "merge_request_event"

	gitlabJobSuccess = "success"
	gitlabJobFailed  = "failed"
)

// GitLabErrorCode maps a failed GitLab call of a refresh to the error code of the installation
// (FR32): 429 is rate limiting, 401 and 403 a permission problem, anything else (including no
// response, status 0) unavailability. A 404 of the pipeline list is not an error: the caller
// answers not_installed.
func GitLabErrorCode(status int) ErrorCode {
	switch status {
	case 429:
		return CodeGitLabRateLimited
	case 401, 403:
		return CodeGitLabPermissionDenied
	}
	return CodeGitLabUnavailable
}

// GitLabPipeline is a pipeline as GitLab lists it for a project. Ref is the raw ref text.
type GitLabPipeline struct {
	ID        int64
	ProjectID int64
	SHA       string
	Ref       string
	Source    string
}

// GitLabArtifactsFile is the artifacts archive of a job (the job's artifacts_file); only its
// size is used.
type GitLabArtifactsFile struct {
	Size int64
}

// GitLabJob is a job of a pipeline as GitLab lists it. CreatedAt and ArtifactsExpireAt are the raw
// timestamp texts (empty when absent); ArtifactsFile is nil when the job has no artifacts.
type GitLabJob struct {
	ID                int64
	Name              string
	Status            string
	CreatedAt         string
	ArtifactsExpireAt string
	ArtifactsFile     *GitLabArtifactsFile
}

const (
	mrRefPrefix = "refs/merge-requests/"
	// maxMRIID is the largest iid taken: the number is stored in an int32 column.
	maxMRIID = math.MaxInt32
)

// MRIIDFromRef is the merge request iid of a merge request pipeline ref,
// refs/merge-requests/<iid>/head or refs/merge-requests/<iid>/merge. Any other ref, and an iid
// that is not a plain positive decimal number within int32, is not one.
func MRIIDFromRef(ref string) (int, bool) {
	rest, ok := strings.CutPrefix(ref, mrRefPrefix)
	if !ok {
		return 0, false
	}
	num, tail, ok := strings.Cut(rest, "/")
	if !ok || (tail != "head" && tail != "merge") {
		return 0, false
	}
	if num == "" || num[0] == '0' || len(num) > 10 {
		return 0, false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 || n > maxMRIID {
		return 0, false
	}
	return n, true
}

// GitLabCandidate is the pipeline job a refresh looks at, with the merge request it belongs to.
type GitLabCandidate struct {
	Pipeline GitLabPipeline
	Job      GitLabJob
	// MRIID is the iid of the merge request of the pipeline ref.
	MRIID int
}

// SelectGitLabCandidate decides whether a pipeline counts for the installation (FR32) and picks
// its job: the pipeline must belong to the installation's project, come from a merge request
// event and have a merge request ref; the job must be named devdigest-review-<slug> and have
// finished as success or failed (a canceled, skipped or running job is looked at by a later
// refresh, EC27). Of several jobs with the name the one with the highest id wins. The pipeline is
// not counted when it has no such job.
func SelectGitLabCandidate(p GitLabPipeline, jobs []GitLabJob, projectID int64, slug string) (GitLabCandidate, bool) {
	if projectID <= 0 || p.ProjectID != projectID || p.ID <= 0 || p.Source != GitLabMergeRequestSource {
		return GitLabCandidate{}, false
	}
	iid, ok := MRIIDFromRef(p.Ref)
	if !ok {
		return GitLabCandidate{}, false
	}
	name := GitLabJobName(slug)
	var best GitLabJob
	var found bool
	for _, j := range jobs {
		if j.Name != name || j.ID <= 0 || !gitlabFinished(j.Status) {
			continue
		}
		if !found || j.ID > best.ID {
			best, found = j, true
		}
	}
	if !found {
		return GitLabCandidate{}, false
	}
	return GitLabCandidate{Pipeline: p, Job: best, MRIID: iid}, true
}

func gitlabFinished(status string) bool {
	return status == gitlabJobSuccess || status == gitlabJobFailed
}

// GitLabKeyOf identifies a GitLab job as a run; the host belongs to the store's key (FR36, FR39).
func GitLabKeyOf(j GitLabJob) RunKey {
	return RunKey{GithubRunID: j.ID, Attempt: GitLabAttempt}
}

// GitLabFromBaseProject reports whether the merge request's source project is the installation's
// own project. A merge request from a fork runs in the parent project with the fork's job
// definition, which can write any result artifact, so such a job (or one with no known source
// project) is never inspected or stored (FR33).
func GitLabFromBaseProject(sourceProjectID, projectID int64) bool {
	return sourceProjectID > 0 && sourceProjectID == projectID
}

// GitLabArtifactAction says what to do with the artifacts of a job.
type GitLabArtifactAction int

// The actions of a job's artifacts (FR34).
const (
	// GitLabArtifactNone: the job has no artifacts; a failed run is stored.
	GitLabArtifactNone GitLabArtifactAction = iota
	// GitLabArtifactSkip: the run is skipped with GitLabArtifactPlan.Skip, nothing is downloaded.
	GitLabArtifactSkip
	// GitLabArtifactFetch: the result file is downloaded.
	GitLabArtifactFetch
)

// GitLabArtifactPlan is the decision of PlanGitLabArtifact.
type GitLabArtifactPlan struct {
	Action GitLabArtifactAction
	// Skip is the skip reason of GitLabArtifactSkip.
	Skip SkipReason
}

// PlanGitLabArtifact decides, from the job alone, whether its result file is fetched (FR34): no
// artifacts at all is a failed run, an expired archive (an expiry that does not parse counts as
// expired) is skipped as artifact_expired, an archive over MaxArtifactZipBytes (or of negative
// size) is skipped as artifact_too_large without a download.
func PlanGitLabArtifact(j GitLabJob, now time.Time) GitLabArtifactPlan {
	if j.ArtifactsFile == nil {
		return GitLabArtifactPlan{Action: GitLabArtifactNone}
	}
	if j.ArtifactsExpireAt != "" {
		at, ok := ParseGitHubTime(j.ArtifactsExpireAt)
		if !ok || !at.After(now) {
			return GitLabArtifactPlan{Action: GitLabArtifactSkip, Skip: SkipExpired}
		}
	}
	if j.ArtifactsFile.Size > MaxArtifactZipBytes || j.ArtifactsFile.Size < 0 {
		return GitLabArtifactPlan{Action: GitLabArtifactSkip, Skip: SkipTooLarge}
	}
	return GitLabArtifactPlan{Action: GitLabArtifactFetch}
}

// GitLabResultTooLarge reports whether a result file body, read with GitLabResultReadCap, is
// over the accepted size (FR34).
func GitLabResultTooLarge(body []byte) bool { return len(body) > MaxArtifactJSONBytes }

// VerifyGitLabArtifact checks that the artifact's own claims match what GitLab says about the job
// (FR35, EC28): repository_id is the installation's project id and the pipeline's project, run_id
// is the job id, commit_sha is the pipeline sha (all three must be present), the job has the name
// devdigest-review-<slug> and the pipeline comes from a merge request event. The path in the
// artifact's repository is not compared (D10): the ids are.
func VerifyGitLabArtifact(a CiResultArtifact, p GitLabPipeline, j GitLabJob, projectID int64, slug string) bool {
	if a.CommitSHA == nil || a.RepositoryID == nil || a.RunID == nil {
		return false
	}
	return projectID > 0 &&
		*a.RepositoryID == projectID && p.ProjectID == projectID &&
		j.ID > 0 && *a.RunID == j.ID &&
		p.SHA != "" && *a.CommitSHA == p.SHA &&
		j.Name == GitLabJobName(slug) &&
		p.Source == GitLabMergeRequestSource
}

// GitLabJobURL is the web address of a job, <base_url>/<full_name>/-/jobs/<id> (FR36), built from
// the connection's base URL and the stored project path; empty when either is missing or the id
// is not positive.
func GitLabJobURL(baseURL, fullName string, jobID int64) string {
	base := strings.TrimRight(baseURL, "/")
	path := strings.Trim(fullName, "/")
	if base == "" || path == "" || jobID <= 0 {
		return ""
	}
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return base + "/" + strings.Join(segments, "/") + "/-/jobs/" + strconv.FormatInt(jobID, 10)
}

// GitLabRunInput is what NewGitLabRun builds a row from.
type GitLabRunInput struct {
	WorkspaceID string
	// Installation is the gitlab_ci installation; its Repo is the stored project path.
	Installation Installation
	// Host and BaseURL are the installation's connection host and base URL.
	Host, BaseURL string
	Pipeline      GitLabPipeline
	Job           GitLabJob
	// MRIID is the iid of the pipeline ref (GitLabCandidate.MRIID).
	MRIID int
	// Artifact is the verified result, nil for a failed run without artifacts.
	Artifact *CiResultArtifact
	// Now stands in for ran_at when the job's created_at does not parse.
	Now time.Time
}

// GitLabNewRun is the row of a verified GitLab run: the common NewRun plus the host and source the
// store keys it by (host, job id, attempt).
type GitLabNewRun struct {
	NewRun
	Host   string
	Source string
}

// NewGitLabRun builds the row of a GitLab run (FR36): github_run_id is the job id, run_attempt 1,
// head_sha the artifact's commit_sha (the pipeline sha for a run without artifact), pr_number the
// artifact's else the iid of the pipeline ref, ran_at the job's created_at. With an artifact it
// is a succeeded or no_findings run in the ledger's terms (FromArtifact); without, a failed run.
func NewGitLabRun(in GitLabRunInput) GitLabNewRun {
	ranAt, ok := ParseGitHubTime(in.Job.CreatedAt)
	if !ok {
		ranAt = in.Now
	}
	iid := in.MRIID
	n := NewRun{
		WorkspaceID:    in.WorkspaceID,
		InstallationID: in.Installation.ID,
		AgentID:        in.Installation.AgentID,
		GithubRunID:    in.Job.ID,
		RunAttempt:     GitLabAttempt,
		HeadSHA:        in.Pipeline.SHA,
		GithubURL:      GitLabJobURL(in.BaseURL, in.Installation.Repo, in.Job.ID),
		RanAt:          ranAt,
	}
	if a := in.Artifact; a != nil {
		n.FromArtifact = true
		if a.CommitSHA != nil {
			n.HeadSHA = *a.CommitSHA
		}
		n.PRNumber = a.PRNumber
		n.ManifestVersion = a.ManifestVersion
		if n.ManifestVersion == nil {
			n.ManifestVersion = in.Installation.ManifestVersion
		}
		n.Status = RunNoFindings
		if a.FindingsCount > 0 {
			n.Status = RunSucceeded
		}
		n.Model = a.Model
		n.CostUSD = a.CostUSD
		n.DurationMS = a.DurationMS
		n.FindingsCount = a.FindingsCount
		if a.Blockers != nil {
			n.Blockers = *a.Blockers
		}
	} else {
		n.ManifestVersion = in.Installation.ManifestVersion
		n.Status = RunFailed
		msg := FailedRunError
		n.Error = &msg
	}
	if n.PRNumber == nil && iid > 0 {
		n.PRNumber = &iid
	}
	return GitLabNewRun{NewRun: n, Host: in.Host, Source: RunSourceGitLab}
}
