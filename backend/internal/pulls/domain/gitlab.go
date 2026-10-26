package domain

import (
	"strings"
	"time"
)

// GitLabStatsBatch is how many merge requests one diff-stats query asks for (spec FR26).
const GitLabStatsBatch = 20

// GitLab merge request states (the REST API's `state`).
const (
	mrOpened = "opened"
	mrMerged = "merged"
	mrClosed = "closed"
	mrLocked = "locked"
)

// MergeRequest is the part of a GitLab merge request (list or detail) the mapping reads, with
// plain values: the adapter decodes the REST payload and fills it. A missing project id is 0.
type MergeRequest struct {
	IID             int
	Title           string
	Author          string // author.username; empty when GitLab sent none
	SourceBranch    string
	TargetBranch    string
	SHA             string
	State           string // opened | closed | locked | merged
	CreatedAt       *time.Time
	UpdatedAt       *time.Time
	MergedAt        *time.Time
	ClosedAt        *time.Time
	Draft           bool
	SourceProjectID int64
	TargetProjectID int64
	Description     *string
}

// MRHeadIsFork reports whether the head branch of a merge request is in another project: the
// source project differs from the target project. A missing value (0 or less) on either side
// counts as a fork, so the automatic review of an unknown head fails closed (spec D36, SCR1).
func MRHeadIsFork(sourceProjectID, targetProjectID int64) bool {
	if sourceProjectID <= 0 || targetProjectID <= 0 {
		return true
	}
	return sourceProjectID != targetProjectID
}

// MRStatus maps a GitLab merge request state to the stored status: opened -> open, merged ->
// merged, closed and locked -> closed (spec FR22, D20). A state GitLab may add later reads as
// closed, never open, so it can not start an automatic review.
func MRStatus(state string) string {
	switch state {
	case mrOpened:
		return StatusOpen
	case mrMerged:
		return StatusMerged
	case mrClosed, mrLocked:
		return StatusClosed
	default:
		return StatusClosed
	}
}

// MapMergeRequest maps a GitLab merge request to a listed pull request (spec FR22). ok is false
// for a merge request without a head sha: it is skipped in that sync. The author reads "unknown"
// when GitLab names none. MergedAt is kept only for a merged request and ClosedAt only for a
// request that is not open, as the stored columns mean for GitHub. Merge requests from forks are
// mapped like any other; HeadIsFork carries the SCR1 rule.
func MapMergeRequest(mr MergeRequest) (Listed, bool) {
	if mr.SHA == "" {
		return Listed{}, false
	}
	status := MRStatus(mr.State)
	author := mr.Author
	if author == "" {
		author = "unknown"
	}
	l := Listed{
		Number: mr.IID, Title: mr.Title, Author: author,
		Branch: mr.SourceBranch, Base: mr.TargetBranch, HeadSHA: mr.SHA, Status: status,
		OpenedAt: mr.CreatedAt, UpdatedAt: mr.UpdatedAt,
		Draft:      mr.Draft,
		HeadIsFork: MRHeadIsFork(mr.SourceProjectID, mr.TargetProjectID),
	}
	if status == StatusMerged {
		l.MergedAt = mr.MergedAt
	}
	if status != StatusOpen {
		l.ClosedAt = mr.ClosedAt
	}
	return l, true
}

// MapMergeRequests maps a page of merge requests, dropping those MapMergeRequest skips and
// keeping GitLab's order.
func MapMergeRequests(mrs []MergeRequest) []Listed {
	out := make([]Listed, 0, len(mrs))
	for _, mr := range mrs {
		if l, ok := MapMergeRequest(mr); ok {
			out = append(out, l)
		}
	}
	return out
}

// GitLabIssueState maps a GitLab issue state to the linked issue's state: opened -> open,
// closed -> closed; anything else is kept as is (spec FR25).
func GitLabIssueState(state string) string {
	if state == mrOpened {
		return "open"
	}
	return state
}

// GitLabDiff is one entry of a merge request's diffs.
type GitLabDiff struct {
	OldPath     string
	NewPath     string
	DeletedFile bool
	Diff        string
	// Collapsed and TooLarge are GitLab's flags for a diff it did not send in full.
	Collapsed bool
	TooLarge  bool
}

// FileFromDiff maps a GitLab diff to a changed file (spec FR24): the path is NewPath (OldPath for
// a deleted file); additions and deletions are the "+" and "-" lines of the diff, hunk headers
// excluded; the patch keeps GitLab's hunk text. A collapsed, too-large or empty diff has no patch
// and 0/0.
func FileFromDiff(d GitLabDiff) File {
	path := d.NewPath
	if d.DeletedFile && d.OldPath != "" {
		path = d.OldPath
	}
	if d.Collapsed || d.TooLarge || d.Diff == "" {
		return File{Path: path}
	}
	add, del := CountDiffLines(d.Diff)
	patch := d.Diff
	return File{Path: path, Additions: add, Deletions: del, Patch: &patch}
}

// CountDiffLines counts the added and removed lines of a unified-diff body without file headers
// (GitLab's `diff` field starts at the first hunk header). Hunk headers ("@@ ...") and the
// "\ No newline at end of file" marker are not counted.
func CountDiffLines(diff string) (additions, deletions int) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// FileTotals sums the files' additions and deletions and counts them: the stats a merge request
// gets when the diff-stats source has none (spec FR24).
func FileTotals(files []File) DiffStats {
	var s DiffStats
	for _, f := range files {
		s.Additions += f.Additions
		s.Deletions += f.Deletions
	}
	s.FilesCount = len(files)
	return s
}

// GitLabProject is the current identity of a GitLab project, read before every list sync (spec
// FR47). WebURL is built from the connection's base URL, never taken from a GitLab response.
type GitLabProject struct {
	ID            int64
	FullName      string // path_with_namespace
	Owner         string // namespace full path
	Name          string // project path
	DefaultBranch string
	WebURL        string
}
