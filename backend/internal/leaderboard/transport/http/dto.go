package leaderboardhttp

import (
	"math"
	"time"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

// The wire shapes of spec 7 (contract file frontend/src/vendor/shared/contracts/leaderboard.ts).
// Struct field order is the key order of the JSON; nullable fields are pointers without
// omitempty and slices are never nil, so an empty list is [] and not null. period.* are local
// times of the request's zone with an offset and milliseconds; every other timestamp is UTC with
// milliseconds.

const (
	localMillisLayout = "2006-01-02T15:04:05.000-07:00"
	utcMillisLayout   = "2006-01-02T15:04:05.000Z"
	moneyScale        = 1e6 // domain.MoneyDecimals decimals
)

type periodDTO struct {
	Key          string  `json:"key"`
	TZ           string  `json:"tz"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	PreviousFrom *string `json:"previous_from"`
	PreviousTo   *string `json:"previous_to"`
}

type repoDTO struct {
	ID           string  `json:"id"`
	FullName     string  `json:"full_name"`
	LastPolledAt *string `json:"last_polled_at"`
	// provider, host and web_url are optional in the contract: present for a GitLab repository,
	// absent for GitHub (FR46).
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	WebURL   string `json:"web_url,omitempty"`
}

type kpisDTO struct {
	PRsReviewed        int     `json:"prs_reviewed"`
	Merged             int     `json:"merged"`
	AvgHealth          *int    `json:"avg_health"`
	TimeToFirstReviewS *int64  `json:"time_to_first_review_s"`
	TimeToMergeS       *int64  `json:"time_to_merge_s"`
	CriticalFound      int     `json:"critical_found"`
	OpenCritical       int     `json:"open_critical"`
	ReviewCostUSD      float64 `json:"review_cost_usd"`
}

type severityCountsDTO struct {
	Critical   int `json:"CRITICAL"`
	Warning    int `json:"WARNING"`
	Suggestion int `json:"SUGGESTION"`
}

type rowReviewDTO struct {
	ReviewID  string            `json:"review_id"`
	AgentID   *string           `json:"agent_id"`
	AgentName string            `json:"agent_name"`
	Score     *int              `json:"score"`
	Findings  severityCountsDTO `json:"findings"`
}

type topFindingDTO struct {
	FindingID string `json:"finding_id"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	AgentName string `json:"agent_name"`
	State     string `json:"state"`
}

type rowDTO struct {
	PRID                  string            `json:"pr_id"`
	RepoID                string            `json:"repo_id"`
	Repo                  string            `json:"repo"`
	Number                int               `json:"number"`
	Title                 string            `json:"title"`
	Author                string            `json:"author"`
	Status                string            `json:"status"`
	Draft                 bool              `json:"draft"`
	Additions             int               `json:"additions"`
	Deletions             int               `json:"deletions"`
	FilesCount            int               `json:"files_count"`
	Size                  *string           `json:"size"`
	Health                *int              `json:"health"`
	Risk                  string            `json:"risk"`
	Findings              severityCountsDTO `json:"findings"`
	OpenCritical          int               `json:"open_critical"`
	Rounds                int               `json:"rounds"`
	TimeToFirstReviewS    *int64            `json:"time_to_first_review_s"`
	TimeToMergeS          *int64            `json:"time_to_merge_s"`
	OpenAgeS              *int64            `json:"open_age_s"`
	CostUSD               float64           `json:"cost_usd"`
	NewCommitsSinceReview bool              `json:"new_commits_since_review"`
	OpenedAt              *string           `json:"opened_at"`
	MergedAt              *string           `json:"merged_at"`
	Reviews               []rowReviewDTO    `json:"reviews"`
	TopFindings           []topFindingDTO   `json:"top_findings"`
}

type awardDTO struct {
	PRID   string  `json:"pr_id"`
	RepoID string  `json:"repo_id"`
	Repo   string  `json:"repo"`
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Value  float64 `json:"value"`
}

type awardsDTO struct {
	Cleanest     *awardDTO `json:"cleanest"`
	Hottest      *awardDTO `json:"hottest"`
	FastestMerge *awardDTO `json:"fastest_merge"`
	MostRounds   *awardDTO `json:"most_rounds"`
	Monster      *awardDTO `json:"monster"`
	Priciest     *awardDTO `json:"priciest"`
}

// reasonDTO omits count and days when they are 0 (the domain's "not set"): the contract has
// them optional, never null.
type reasonDTO struct {
	Kind  string `json:"kind"`
	Count int    `json:"count,omitempty"`
	Days  int    `json:"days,omitempty"`
}

type attentionDTO struct {
	PRID     string      `json:"pr_id"`
	RepoID   string      `json:"repo_id"`
	Repo     string      `json:"repo"`
	Number   int         `json:"number"`
	Title    string      `json:"title"`
	Author   string      `json:"author"`
	OpenedAt *string     `json:"opened_at"`
	Reasons  []reasonDTO `json:"reasons"`
}

type needsAttentionDTO struct {
	Total int            `json:"total"`
	Items []attentionDTO `json:"items"`
}

type hotFileDTO struct {
	Path     string `json:"path"`
	RepoID   string `json:"repo_id"`
	Repo     string `json:"repo"`
	Findings int    `json:"findings"`
	Critical int    `json:"critical"`
	PRs      []int  `json:"prs"`
}

type sizeBucketDTO struct {
	Size        string   `json:"size"`
	PRs         int      `json:"prs"`
	AvgHealth   *int     `json:"avg_health"`
	AvgFindings *float64 `json:"avg_findings"`
}

type authorHealthDTO struct {
	Author      string   `json:"author"`
	PRs         int      `json:"prs"`
	AvgHealth   *int     `json:"avg_health"`
	AvgFindings *float64 `json:"avg_findings"`
}

type repoHealthDTO struct {
	RepoID      string   `json:"repo_id"`
	Repo        string   `json:"repo"`
	PRs         int      `json:"prs"`
	AvgHealth   *int     `json:"avg_health"`
	AvgFindings *float64 `json:"avg_findings"`
}

type activityPointDTO struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Opened   int    `json:"opened"`
	Merged   int    `json:"merged"`
	Critical int    `json:"critical"`
}

type activityDTO struct {
	Granularity string             `json:"granularity"`
	Points      []activityPointDTO `json:"points"`
}

type leaderboardDTO struct {
	Period          periodDTO         `json:"period"`
	Repos           []repoDTO         `json:"repos"`
	Summary         kpisDTO           `json:"summary"`
	PreviousSummary *kpisDTO          `json:"previous_summary"`
	Awards          awardsDTO         `json:"awards"`
	Rows            []rowDTO          `json:"rows"`
	NeedsAttention  needsAttentionDTO `json:"needs_attention"`
	HotFiles        []hotFileDTO      `json:"hot_files"`
	HealthBySize    []sizeBucketDTO   `json:"health_by_size"`
	HealthByAuthor  []authorHealthDTO `json:"health_by_author"`
	HealthByRepo    []repoHealthDTO   `json:"health_by_repo"`
	Activity        activityDTO       `json:"activity"`
}

// utcMillis renders t as UTC with milliseconds (truncated, like JavaScript's toISOString).
func utcMillis(t time.Time) string { return t.UTC().Format(utcMillisLayout) }

func utcPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := utcMillis(*t)
	return &s
}

// money rounds a USD amount to domain.MoneyDecimals decimals.
func money(v float64) float64 { return math.Round(v*moneyScale) / moneyScale }

func toPeriod(w domain.Window) periodDTO {
	loc := w.Location
	if loc == nil {
		loc = time.UTC
	}
	local := func(t time.Time) string { return t.In(loc).Format(localMillisLayout) }
	p := periodDTO{Key: string(w.Key), TZ: loc.String(), From: local(w.From), To: local(w.To)}
	if w.HasPrev {
		from, to := local(w.PrevFrom), local(w.PrevTo)
		p.PreviousFrom, p.PreviousTo = &from, &to
	}
	return p
}

func toKpis(k domain.KpiSet) kpisDTO {
	return kpisDTO{
		PRsReviewed: k.PRsReviewed, Merged: k.Merged, AvgHealth: k.AvgHealth,
		TimeToFirstReviewS: k.TimeToFirstReviewS, TimeToMergeS: k.TimeToMergeS,
		CriticalFound: k.CriticalFound, OpenCritical: k.OpenCritical, ReviewCostUSD: money(k.ReviewCostUSD),
	}
}

func toPreviousKpis(k *domain.KpiSet) *kpisDTO {
	if k == nil {
		return nil
	}
	dto := toKpis(*k)
	return &dto
}

func toCounts(c domain.FindingCounts) severityCountsDTO {
	return severityCountsDTO{Critical: c.Critical, Warning: c.Warning, Suggestion: c.Suggestion}
}

func toRows(rows []domain.Row) []rowDTO {
	out := make([]rowDTO, len(rows))
	for i := range rows {
		r := &rows[i]
		var size *string
		if r.Size != domain.SizeNone {
			s := string(r.Size)
			size = &s
		}
		reviews := make([]rowReviewDTO, len(r.Reviews))
		for j, rv := range r.Reviews {
			reviews[j] = rowReviewDTO{
				ReviewID: rv.ReviewID, AgentID: rv.AgentID, AgentName: rv.AgentName,
				Score: rv.Score, Findings: toCounts(rv.Findings),
			}
		}
		top := make([]topFindingDTO, len(r.TopFindings))
		for j, f := range r.TopFindings {
			top[j] = topFindingDTO{
				FindingID: f.FindingID, Severity: f.Severity, Title: f.Title, File: f.File,
				StartLine: f.StartLine, AgentName: f.AgentName, State: f.State,
			}
		}
		out[i] = rowDTO{
			PRID: r.PRID, RepoID: r.RepoID, Repo: r.Repo, Number: r.Number, Title: r.Title, Author: r.Author,
			Status: r.Status, Draft: r.Draft, Additions: r.Additions, Deletions: r.Deletions,
			FilesCount: r.FilesCount, Size: size, Health: r.Health, Risk: string(r.Risk),
			Findings: toCounts(r.Findings), OpenCritical: r.OpenCritical, Rounds: r.Rounds,
			TimeToFirstReviewS: r.TimeToFirstReviewS, TimeToMergeS: r.TimeToMergeS, OpenAgeS: r.OpenAgeS,
			CostUSD: money(r.CostUSD), NewCommitsSinceReview: r.NewCommitsSinceReview,
			OpenedAt: utcPtr(r.OpenedAt), MergedAt: utcPtr(r.MergedAt), Reviews: reviews, TopFindings: top,
		}
	}
	return out
}

func toAward(a *domain.Award) *awardDTO {
	if a == nil {
		return nil
	}
	return &awardDTO{
		PRID: a.PRID, RepoID: a.RepoID, Repo: a.Repo, Number: a.Number, Title: a.Title, Value: money(a.Value),
	}
}

func toAwards(a domain.AwardSet) awardsDTO {
	return awardsDTO{
		Cleanest: toAward(a.Cleanest), Hottest: toAward(a.Hottest), FastestMerge: toAward(a.FastestMerge),
		MostRounds: toAward(a.MostRounds), Monster: toAward(a.Monster), Priciest: toAward(a.Priciest),
	}
}

func toAttention(l domain.AttentionList) needsAttentionDTO {
	items := make([]attentionDTO, len(l.Items))
	for i, it := range l.Items {
		reasons := make([]reasonDTO, len(it.Reasons))
		for j, r := range it.Reasons {
			reasons[j] = reasonDTO{Kind: string(r.Kind), Count: r.Count, Days: r.Days}
		}
		items[i] = attentionDTO{
			PRID: it.PRID, RepoID: it.RepoID, Repo: it.Repo, Number: it.Number, Title: it.Title,
			Author: it.Author, OpenedAt: utcPtr(it.OpenedAt), Reasons: reasons,
		}
	}
	return needsAttentionDTO{Total: l.Total, Items: items}
}

func toLeaderboard(lb domain.Leaderboard) leaderboardDTO {
	repos := make([]repoDTO, len(lb.Repos))
	for i, r := range lb.Repos {
		repos[i] = repoDTO{
			ID: r.ID, FullName: r.FullName, LastPolledAt: utcPtr(r.LastPolledAt),
			Provider: r.Provider, Host: r.Host, WebURL: r.WebURL,
		}
	}
	files := make([]hotFileDTO, len(lb.HotFiles))
	for i, f := range lb.HotFiles {
		prs := f.PRs
		if prs == nil {
			prs = []int{}
		}
		files[i] = hotFileDTO{
			Path: f.Path, RepoID: f.RepoID, Repo: f.Repo, Findings: f.Findings, Critical: f.Critical, PRs: prs,
		}
	}
	buckets := make([]sizeBucketDTO, len(lb.HealthBySize))
	for i, b := range lb.HealthBySize {
		buckets[i] = sizeBucketDTO{Size: string(b.Size), PRs: b.PRs, AvgHealth: b.AvgHealth, AvgFindings: b.AvgFindings}
	}
	authors := make([]authorHealthDTO, len(lb.HealthByAuthor))
	for i, g := range lb.HealthByAuthor {
		authors[i] = authorHealthDTO{Author: g.Label, PRs: g.PRs, AvgHealth: g.AvgHealth, AvgFindings: g.AvgFindings}
	}
	byRepo := make([]repoHealthDTO, len(lb.HealthByRepo))
	for i, g := range lb.HealthByRepo {
		byRepo[i] = repoHealthDTO{RepoID: g.Key, Repo: g.Label, PRs: g.PRs, AvgHealth: g.AvgHealth, AvgFindings: g.AvgFindings}
	}
	points := make([]activityPointDTO, len(lb.Activity.Points))
	for i, p := range lb.Activity.Points {
		points[i] = activityPointDTO{From: p.From, To: p.To, Opened: p.Opened, Merged: p.Merged, Critical: p.Critical}
	}
	return leaderboardDTO{
		Period: toPeriod(lb.Window), Repos: repos,
		Summary: toKpis(lb.Summary), PreviousSummary: toPreviousKpis(lb.PreviousSummary),
		Awards: toAwards(lb.Awards), Rows: toRows(lb.Rows), NeedsAttention: toAttention(lb.NeedsAttention),
		HotFiles: files, HealthBySize: buckets, HealthByAuthor: authors, HealthByRepo: byRepo,
		Activity: activityDTO{Granularity: lb.Activity.Granularity, Points: points},
	}
}
