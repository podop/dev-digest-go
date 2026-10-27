// Package domain holds the pull request model and its pure rules: the review-freshness status,
// the diff-stat backfill predicate, the same-path file merge, the linked-issue lookup and the
// list rollups. It imports only the standard library.
package domain

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Not-found sentinels returned by stores; the use cases turn them into apperr.NotFound.
var (
	ErrRepoNotFound = errors.New("repo not found")
	ErrPullNotFound = errors.New("pull request not found")
)

// GitHub merge states stored in pull_requests.status, plus the review-freshness statuses the
// list derives from them.
const (
	StatusOpen        = "open"
	StatusClosed      = "closed"
	StatusMerged      = "merged"
	StatusNeedsReview = "needs_review"
	StatusReviewed    = "reviewed"
	StatusStale       = "stale"
)

// StaleAfter is how long an open PR whose head was reviewed may stay untouched before it reads
// "stale".
const StaleAfter = 7 * 24 * time.Hour

// Code-host providers of a stored repository.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

// Repo is the code-host address of a stored repository. A GitHub repository is addressed by
// (Owner, Name); a GitLab project by its numeric ProjectID on Host (FR17), so a rename or move on
// GitLab keeps syncing. Provider "" reads as GitHub (rows and callers that predate GitLab).
type Repo struct {
	ID    string
	Owner string
	Name  string
	// Provider is ProviderGitHub or ProviderGitLab ("" = GitHub).
	Provider string
	// Host is the host key of a GitLab repository ("gitlab.com", "git.corp.example:8443"); empty or
	// "github.com" for GitHub.
	Host string
	// ProjectID is the GitLab project id; 0 for GitHub.
	ProjectID int64
	// WebURL is the stored web address of a GitLab project (refreshed by every sync, A FR47);
	// html_url of a GitLab comment is built from it, never from a GitLab response (B FR3, D14).
	// Empty for GitHub and for rows that predate it.
	WebURL string
}

// IsGitLab reports whether the repository is a GitLab project.
func (r Repo) IsGitLab() bool { return r.Provider == ProviderGitLab }

// PauseKey is the key of the rate-limit pause the repository falls under: its provider and host,
// so a GitLab instance never shares a pause with GitHub or with another instance (spec FR27).
func (r Repo) PauseKey() string {
	if r.IsGitLab() {
		return ProviderGitLab + "/" + r.Host
	}
	return ProviderGitHub + "/github.com"
}

// PullRef is how a pull request is referenced next to its repository: "!12" for a GitLab merge
// request, "#12" otherwise (spec FR43).
func PullRef(provider string, number int) string {
	if provider == ProviderGitLab {
		return "!" + strconv.Itoa(number)
	}
	return "#" + strconv.Itoa(number)
}

// Pull is a stored pull request.
type Pull struct {
	ID              string
	RepoID          string
	Number          int
	Title           string
	Author          string
	Branch          string
	Base            string
	HeadSHA         string
	Additions       int
	Deletions       int
	FilesCount      int
	Status          string // GitHub merge state, or the seeded review status
	Body            *string
	LastReviewedSHA *string
	OpenedAt        *time.Time
	UpdatedAt       *time.Time
	// DiffHead is the head SHA of the GitLab diff version the stored files came from (B FR35);
	// nil for GitHub rows and for GitLab rows fetched before it was stored. Never on the wire.
	DiffHead *string
}

// EffectiveDiffHead is the head the diff on screen belongs to: the stored diff head, else the
// stored head SHA (B FR4).
func (p Pull) EffectiveDiffHead() string {
	if p.DiffHead != nil && *p.DiffHead != "" {
		return *p.DiffHead
	}
	return p.HeadSHA
}

// Listed is a pull request as GitHub's list endpoint returns it. The list carries no diff stats.
type Listed struct {
	Number    int
	Title     string
	Author    string
	Branch    string
	Base      string
	HeadSHA   string
	Status    string // open | closed | merged
	OpenedAt  *time.Time
	UpdatedAt *time.Time
	MergedAt  *time.Time // nil unless merged
	ClosedAt  *time.Time // nil while open; set for closed and merged PRs
	Draft     bool
	// HeadIsFork is true when the head branch is in another repository (or none is named). Stored,
	// not served: the automatic-review trigger reads it.
	HeadIsFork bool
}

// DiffStats are the size fields the detail endpoint fills.
type DiffStats struct {
	Additions  int
	Deletions  int
	FilesCount int
}

// File is one changed file. Patch is nil when there is none.
type File struct {
	Path      string
	Additions int
	Deletions int
	Patch     *string
	// PreviousPath is the GitLab old_path of the file (B FR35); nil for GitHub files and for
	// GitLab files stored before it was kept. Never on the wire.
	PreviousPath *string
}

// Commit is one commit of a pull request.
type Commit struct {
	SHA         string
	Message     string
	Author      string
	CommittedAt *time.Time
}

// Issue is a linked GitHub issue. It is never stored.
type Issue struct {
	Number int
	Title  string
	Body   *string
	State  string
}

// Fetched is a pull request as GitHub's detail fetch returns it.
type Fetched struct {
	Listed
	Body *string
	DiffStats
	Files   []File
	Commits []Commit
	// DiffHead is the GitLab diff head (B FR35); nil for a GitHub fetch.
	DiffHead *string
}

// Detail is what GET /pulls/:id answers. FromGitHub tells a fresh fetch (status is GitHub's
// state, patches may be absent) from the stored mirror (status is the raw stored value).
type Detail struct {
	Pull        Pull
	Files       []File
	Commits     []Commit
	LinkedIssue *Issue
	FromGitHub  bool
}

// ReviewComment is a GitHub review comment as the API returns it.
type ReviewComment struct {
	ID           int64
	Path         string
	Line         *int
	OriginalLine *int
	Side         string // LEFT | RIGHT
	Body         string
	User         string
	CreatedAt    time.Time
	HTMLURL      string
	InReplyToID  *int64
	IsOutdated   bool
}

// CommentInput is a comment to post. With InReplyTo set it is a reply to that comment.
type CommentInput struct {
	CommitID  string
	Path      string
	Line      int
	Side      string // LEFT | RIGHT; empty means RIGHT
	Body      string
	InReplyTo *int64
}

// DeriveStatus is the status of a PR in the list. Stored merged or closed is returned as is.
// Otherwise it is needs_review when no review ran against the current head, stale when the head
// was reviewed but the PR was last updated more than StaleAfter before now, else reviewed.
func DeriveStatus(stored string, lastReviewedSHA *string, headSHA string, updatedAt *time.Time, now time.Time) string {
	if stored == StatusMerged || stored == StatusClosed {
		return stored
	}
	if lastReviewedSHA == nil || *lastReviewedSHA != headSHA {
		return StatusNeedsReview
	}
	if updatedAt != nil && now.Sub(*updatedAt) > StaleAfter {
		return StatusStale
	}
	return StatusReviewed
}

// NeedsDiffStats reports whether a stored PR still has the zeroed stats of a fresh list import.
func NeedsDiffStats(s DiffStats) bool {
	return s.Additions == 0 && s.Deletions == 0 && s.FilesCount == 0
}

// MergeFiles folds entries with the same path into one, in first-seen order: additions and
// deletions are summed and non-empty patches are joined with a newline.
func MergeFiles(files []File) []File {
	out := make([]File, 0, len(files))
	index := make(map[string]int, len(files))
	for _, f := range files {
		i, seen := index[f.Path]
		if !seen {
			index[f.Path] = len(out)
			out = append(out, f)
			continue
		}
		prev := &out[i]
		prev.Additions += f.Additions
		prev.Deletions += f.Deletions
		prev.Patch = joinPatches(prev.Patch, f.Patch)
	}
	return out
}

func joinPatches(a, b *string) *string {
	var parts []string
	for _, p := range []*string{a, b} {
		if p != nil && *p != "" {
			parts = append(parts, *p)
		}
	}
	if len(parts) == 0 {
		return a
	}
	joined := strings.Join(parts, "\n")
	return &joined
}

var issueRef = regexp.MustCompile(`#(\d+)`)

// LinkedIssueNumber returns the number of the first "#<digits>" in a PR body. A leading
// "closes", "fixes" or "resolves" is allowed but not required, so it does not change the match.
func LinkedIssueNumber(body string) (int, bool) {
	m := issueRef.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// DetailMirror is what a successful GitHub detail fetch writes to the store in one transaction:
// the PR's body and diff stats, and the full file and commit lists (anything stored but absent
// here is removed).
type DetailMirror struct {
	Body *string
	DiffStats
	Files   []File
	Commits []Commit
	// DiffHead is stored with the files in the same transaction (B FR35); nil clears it.
	DiffHead *string
}
