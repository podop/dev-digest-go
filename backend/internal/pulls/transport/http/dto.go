package pullshttp

import (
	"time"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

// Wire types follow the PrMeta, PrDetail and PrReviewComment contracts. Nullable fields are
// pointers without omitempty (always present, null when unset); the only omitted keys are
// linked_issue and the patch of a freshly fetched file.

// pullScalars are the PrMeta fields that PrDetail shares.
type pullScalars struct {
	ID         string      `json:"id"`
	Number     int         `json:"number"`
	Title      string      `json:"title"`
	Author     string      `json:"author"`
	Branch     string      `json:"branch"`
	Base       string      `json:"base"`
	HeadSHA    string      `json:"head_sha"`
	Additions  int         `json:"additions"`
	Deletions  int         `json:"deletions"`
	FilesCount int         `json:"files_count"`
	Status     string      `json:"status"`
	OpenedAt   *httpx.Time `json:"opened_at"`
	UpdatedAt  *httpx.Time `json:"updated_at"`
}

type findingsCountsDTO struct {
	Critical   int `json:"CRITICAL"`
	Warning    int `json:"WARNING"`
	Suggestion int `json:"SUGGESTION"`
}

// prMetaDTO is one item of GET /repos/:id/pulls.
type prMetaDTO struct {
	pullScalars
	Score           *int               `json:"score"`
	CostUSD         *float64           `json:"cost_usd"`
	LatestReviewID  *string            `json:"latest_review_id"`
	LastReviewedAt  *httpx.Time        `json:"last_reviewed_at"`
	LatestReviewIDs []string           `json:"latest_review_ids"`
	FindingsCounts  *findingsCountsDTO `json:"findings_counts"`
}

func toTime(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	v := httpx.NewTime(*t)
	return &v
}

func toScalars(p domain.Pull, status string) pullScalars {
	return pullScalars{
		ID: p.ID, Number: p.Number, Title: p.Title, Author: p.Author, Branch: p.Branch, Base: p.Base,
		HeadSHA: p.HeadSHA, Additions: p.Additions, Deletions: p.Deletions, FilesCount: p.FilesCount,
		Status: status, OpenedAt: toTime(p.OpenedAt), UpdatedAt: toTime(p.UpdatedAt),
	}
}

func toMeta(it app.ListItem) prMetaDTO {
	r := it.Rollup
	dto := prMetaDTO{
		pullScalars:     toScalars(it.Pull, it.Status),
		Score:           r.Score,
		CostUSD:         r.CostUSD,
		LatestReviewID:  r.LatestReviewID,
		LastReviewedAt:  toTime(r.LastReviewedAt),
		LatestReviewIDs: r.LatestReviewIDs,
	}
	if dto.LatestReviewIDs == nil {
		dto.LatestReviewIDs = []string{}
	}
	if c := r.FindingsCounts; c != nil {
		dto.FindingsCounts = &findingsCountsDTO{Critical: c.Critical, Warning: c.Warning, Suggestion: c.Suggestion}
	}
	return dto
}

func toMetas(items []app.ListItem) []prMetaDTO {
	out := make([]prMetaDTO, len(items))
	for i, it := range items {
		out[i] = toMeta(it)
	}
	return out
}

type commitDTO struct {
	SHA         string      `json:"sha"`
	Message     string      `json:"message"`
	Author      string      `json:"author"`
	CommittedAt *httpx.Time `json:"committed_at"`
}

type issueDTO struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   *string `json:"body"`
	State  string  `json:"state"`
}

// storedFileDTO is a file of the stored mirror: a missing patch is null.
type storedFileDTO struct {
	Path      string  `json:"path"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Patch     *string `json:"patch"`
}

// freshFileDTO is a file of a fresh GitHub fetch: a missing patch is omitted.
type freshFileDTO struct {
	Path      string  `json:"path"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Patch     *string `json:"patch,omitempty"`
}

// detailDTO is GET /pulls/:id; F is the file shape (see storedFileDTO, freshFileDTO).
type detailDTO[F any] struct {
	pullScalars
	Body        *string     `json:"body"`
	Files       []F         `json:"files"`
	Commits     []commitDTO `json:"commits"`
	LinkedIssue *issueDTO   `json:"linked_issue,omitempty"`
}

func toDetail[F any](d domain.Detail, file func(domain.File) F) detailDTO[F] {
	out := detailDTO[F]{
		pullScalars: toScalars(d.Pull, d.Pull.Status),
		Body:        d.Pull.Body,
		Files:       make([]F, len(d.Files)),
		Commits:     make([]commitDTO, len(d.Commits)),
	}
	for i, f := range d.Files {
		out.Files[i] = file(f)
	}
	for i, c := range d.Commits {
		out.Commits[i] = commitDTO{SHA: c.SHA, Message: c.Message, Author: c.Author, CommittedAt: toTime(c.CommittedAt)}
	}
	if is := d.LinkedIssue; is != nil {
		out.LinkedIssue = &issueDTO{Number: is.Number, Title: is.Title, Body: is.Body, State: is.State}
	}
	return out
}

func storedFile(f domain.File) storedFileDTO {
	return storedFileDTO{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch}
}

func freshFile(f domain.File) freshFileDTO {
	return freshFileDTO{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch}
}

type commentDTO struct {
	ID           int64      `json:"id"`
	Path         string     `json:"path"`
	Line         *int       `json:"line"`
	OriginalLine *int       `json:"original_line"`
	Side         string     `json:"side"`
	Body         string     `json:"body"`
	User         string     `json:"user"`
	CreatedAt    httpx.Time `json:"created_at"`
	HTMLURL      string     `json:"html_url"`
	InReplyToID  *int64     `json:"in_reply_to_id"`
	IsOutdated   bool       `json:"is_outdated"`
}

func toComment(c domain.ReviewComment) commentDTO {
	return commentDTO{
		ID: c.ID, Path: c.Path, Line: c.Line, OriginalLine: c.OriginalLine, Side: c.Side, Body: c.Body,
		User: c.User, CreatedAt: httpx.NewTime(c.CreatedAt), HTMLURL: c.HTMLURL,
		InReplyToID: c.InReplyToID, IsOutdated: c.IsOutdated,
	}
}

func toComments(cs []domain.ReviewComment) []commentDTO {
	out := make([]commentDTO, len(cs))
	for i, c := range cs {
		out[i] = toComment(c)
	}
	return out
}

type pollDTO struct {
	Synced          int  `json:"synced"`
	ReviewTriggered bool `json:"reviewTriggered"`
}
