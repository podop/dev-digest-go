package domain

import (
	"regexp"
	"time"
)

// FailedRunError is the note stored on a run that produced no result artifact (spec 7.7.5).
const FailedRunError = "The CI run produced no result artifact"

// Conclusions of a completed workflow run that are never stored (spec 7.7.2).
const (
	conclusionSkipped   = "skipped"
	conclusionCancelled = "cancelled"
)

// Inspectable reports whether a listed workflow run is worth looking at: a run whose conclusion
// is skipped, cancelled or missing is not.
func Inspectable(r WorkflowRun) bool {
	if r.Conclusion == nil {
		return false
	}
	return *r.Conclusion != conclusionSkipped && *r.Conclusion != conclusionCancelled
}

// AttemptOf is the attempt number of a run; GitHub omits it for the first attempt.
func AttemptOf(r WorkflowRun) int {
	if r.RunAttempt <= 0 {
		return 1
	}
	return r.RunAttempt
}

// KeyOf identifies a workflow run attempt.
func KeyOf(r WorkflowRun) RunKey {
	return RunKey{GithubRunID: r.ID, Attempt: AttemptOf(r)}
}

// ParseGitHubTime parses a timestamp GitHub sends (RFC 3339).
func ParseGitHubTime(raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// NewestArtifact returns the artifact with the given name that was created last; false when none
// has the name. An artifact whose created_at does not parse never replaces an earlier one, and is
// replaced by none.
func NewestArtifact(artifacts []RunArtifact, name string) (RunArtifact, bool) {
	var best RunArtifact
	var bestAt time.Time
	var bestOK, found bool
	for _, a := range artifacts {
		if a.Name != name {
			continue
		}
		at, ok := ParseGitHubTime(a.CreatedAt)
		if !found {
			best, bestAt, bestOK, found = a, at, ok, true
			continue
		}
		if ok && bestOK && at.After(bestAt) {
			best, bestAt = a, at
		}
	}
	return best, found
}

// NewRunFromArtifact builds the row of a verified run (spec 7.7.5). now stands in for ran_at when
// the run's created_at does not parse.
func NewRunFromArtifact(workspaceID string, inst Installation, run WorkflowRun, a CiResultArtifact, now time.Time) NewRun {
	n := baseRun(workspaceID, inst, run, now)
	n.PRNumber = a.PRNumber
	if n.PRNumber == nil {
		n.PRNumber = firstPR(run)
	}
	n.ManifestVersion = a.ManifestVersion
	if n.ManifestVersion == nil {
		n.ManifestVersion = inst.ManifestVersion
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
	return n
}

// NewFailedRun builds the row of a completed run that left no result artifact: the runner failed
// hard (spec 7.7.5).
func NewFailedRun(workspaceID string, inst Installation, run WorkflowRun, now time.Time) NewRun {
	n := baseRun(workspaceID, inst, run, now)
	n.PRNumber = firstPR(run)
	n.ManifestVersion = inst.ManifestVersion
	n.Status = RunFailed
	msg := FailedRunError
	n.Error = &msg
	return n
}

func baseRun(workspaceID string, inst Installation, run WorkflowRun, now time.Time) NewRun {
	ranAt, ok := ParseGitHubTime(run.CreatedAt)
	if !ok {
		ranAt = now
	}
	return NewRun{
		WorkspaceID:    workspaceID,
		InstallationID: inst.ID,
		AgentID:        inst.AgentID,
		GithubRunID:    run.ID,
		RunAttempt:     AttemptOf(run),
		HeadSHA:        run.HeadSHA,
		GithubURL:      run.HTMLURL,
		RanAt:          ranAt,
	}
}

func firstPR(run WorkflowRun) *int {
	if len(run.PRNumbers) == 0 {
		return nil
	}
	n := run.PRNumbers[0]
	return &n
}

// Verdicts of a run in the run list.
const (
	VerdictBlocked = "blocked"
	VerdictPassed  = "passed"
)

// RunVerdict is the verdict the run list shows: none for a run without a result (failed),
// otherwise blocked when it has blockers.
func RunVerdict(r Run) *string {
	if r.Status != nil && *r.Status == RunFailed {
		return nil
	}
	v := VerdictPassed
	if r.Blockers != nil && *r.Blockers > 0 {
		v = VerdictBlocked
	}
	return &v
}

// RunDurationSeconds is the run's duration in seconds, nil when unknown.
func RunDurationSeconds(r Run) *float64 {
	if r.DurationMS == nil {
		return nil
	}
	s := float64(*r.DurationMS) / 1000
	return &s
}

var runURLRepoRE = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/`)

// RunRepo is the repository a run belongs to: the installation's, else the one in the run's
// GitHub URL (the installation may be gone); nil when neither is known.
func RunRepo(r Run) *string {
	if r.Repo != nil {
		return r.Repo
	}
	if r.GithubURL == nil {
		return nil
	}
	m := runURLRepoRE.FindStringSubmatch(*r.GithubURL)
	if m == nil {
		return nil
	}
	return &m[1]
}

// WorkflowPathOf is the workflow path shown for an installation: nil for a row without a slug.
func WorkflowPathOf(i Installation) *string {
	if i.Slug == nil || *i.Slug == "" {
		return nil
	}
	p := WorkflowPath(*i.Slug)
	return &p
}
