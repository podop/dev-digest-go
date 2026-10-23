// Package domain is the vocabulary of the leaderboard context (spec 2026-10-23-pr-leaderboard,
// sections 5 and 7): the validated query and its window, the facts the store reads, and the
// read models the page shows. The folds that turn facts into the page are pure functions added
// next to this file by the lanes of wave W1; this file fixes only the shared types and
// constants. It imports only the standard library and carries no JSON or database tags: the
// wire shapes live in transport/http.
//
// # Data flow
//
// The use case (app.Service) does, for one request:
//
//	q, issues := ParseQuery(values)               // transport, before any use case runs
//	win       := ResolveWindow(q.Period, q.Location, now)
//	repos     := Reader.RepoScope(ctx, ws, q.Repo)
//	facts     := Reader.WindowFacts(ctx, ws, repoIDs, win.PrevFrom, win.To)   // ONE read, both windows
//	open      := Reader.OpenState(ctx, ws, repoIDs)
//	cur, prev := BuildRows(win, facts, now)       // rows of the window / of the previous window
//	Rank(cur); Rank(prev)
//	Kpis(cur-span, cur, facts), Kpis(prev-span, prev, facts), Awards(cur), Attention(cur, open, now),
//	HotFiles(cur), HealthBySize(cur), Daily(win, cur, facts.Pulls)
//
// # Function signatures (implemented in W1, one file per lane; NOT in this file)
//
// All are pure (no I/O, no clock besides the passed now) and live in package domain:
//
//	// query.go (S6)
//	func ParseQuery(v url.Values) (Query, []Issue)
//	// window.go (S6)
//	func ResolveWindow(key PeriodKey, loc *time.Location, now time.Time) Window
//	func (w Window) DayIndex(t time.Time) int          // -1 when t is outside [From, To)
//	// rows.go (S7): rows of both windows from one Facts (Facts covers [PrevFrom, To))
//	func BuildRows(win Window, f Facts, now time.Time) (cur, prev []Row)
//	// rank.go (S7): sorts in place by health desc, CRITICAL asc, repo, number (nil health last)
//	func Rank(rows []Row)
//	// kpis.go (S8): span is [From, To) of the window the rows belong to
//	func Kpis(span Span, rows []Row, f Facts) KpiSet
//	// awards.go (S8): rows must already be ranked; ties keep the lower table rank
//	func Awards(rows []Row) AwardSet
//	// attention.go (S8): total counts all items, items is capped at AttentionLimit
//	func Attention(rows []Row, open []OpenPull, now time.Time) (total int, items []AttentionItem)
//	// files.go (S8)
//	func HotFiles(rows []Row) []HotFile
//	func HealthBySize(rows []Row) []SizeBucket
//	// daily.go (S8): one Day per calendar day of win (win.Days points)
//	func Daily(win Window, rows []Row, pulls []PullFact) []Day
//
// Conventions shared by all of them: durations are whole seconds (int64); money is float64
// USD; every time instant is a time.Time (the transport formats it); "unknown" is a nil
// pointer, never a sentinel number.
package domain

import (
	"errors"
	"time"
)

// ErrNotFound is returned by the store when the requested repository is not in the workspace;
// the use case turns it into apperr.NotFound("Repo not found").
var ErrNotFound = errors.New("leaderboard: not found")

// Query keys, defaults and limits (spec FR6, P3, P4).
const (
	// ParamPeriod, ParamTZ and ParamRepo are the only accepted query keys; any other key is an
	// Issue (422), as is a repeated key.
	ParamPeriod = "period"
	ParamTZ     = "tz"
	ParamRepo   = "repo"

	// RepoAll is the repo value that means "every repository of the workspace".
	RepoAll = "all"
	// DefaultTZ is the zone used when tz is absent.
	DefaultTZ = "UTC"
	// MaxTZLen is the longest accepted tz string (checked before the zone database is asked).
	MaxTZLen = 64
)

// PeriodKey is the reporting period of a request.
type PeriodKey string

// The period keys. The default is PeriodWeek.
const (
	PeriodWeek     PeriodKey = "week"
	PeriodLastWeek PeriodKey = "last_week"
	Period30d      PeriodKey = "30d"
)

// Limits of the response (spec FR13, FR14).
const (
	// AttentionLimit caps the needs-attention items; the total still counts all.
	AttentionLimit = 20
	// HotFilesLimit is the number of hot files returned.
	HotFilesLimit = 6
	// NoReviewGrace: an open PR counts as "no review" only when opened longer ago than this.
	NoReviewGrace = 24 * time.Hour
	// OpenLongAfter: an open PR not updated for this long (or longer) is "open long".
	OpenLongAfter = 7 * 24 * time.Hour
	// StaleAfter is the PR list's stale threshold (same value as OpenLongAfter).
	StaleAfter = 7 * 24 * time.Hour
	// MoneyDecimals is the number of decimals money is rounded to.
	MoneyDecimals = 6
	// DeletedAgentName names the reviews whose agent was deleted (agent_id NULL); all of them
	// count as ONE agent of the snapshot (plan Q2).
	DeletedAgentName = "Deleted agent"
)

// Severities of findings.
const (
	SeverityCritical   = "CRITICAL"
	SeverityWarning    = "WARNING"
	SeveritySuggestion = "SUGGESTION"
)

// Risk weights per non-dismissed snapshot finding (spec FR10) and the thresholds on their sum.
const (
	WeightCritical   = 35
	WeightWarning    = 12
	WeightSuggestion = 3
	// RiskMediumFrom and RiskHighFrom: low < 30, medium 30-69, high >= 70.
	RiskMediumFrom = 30
	RiskHighFrom   = 70
)

// Size bucket upper bounds in changed lines (additions + deletions): S <= 100, M <= 400,
// L <= 1000, XL above.
const (
	SizeMaxS = 100
	SizeMaxM = 400
	SizeMaxL = 1000
)

// Size is the size bucket of a PR. SizeNone ("") means unknown: additions, deletions and files
// are all 0 (the diff stats were not back-filled yet, plan Q9) and the PR is left out of
// health-by-size and Monster PR.
type Size string

// The size buckets in display order.
const (
	SizeNone Size = ""
	SizeS    Size = "S"
	SizeM    Size = "M"
	SizeL    Size = "L"
	SizeXL   Size = "XL"
)

// Sizes lists the real buckets in display order (health-by-size always returns all four).
var Sizes = [...]Size{SizeS, SizeM, SizeL, SizeXL}

// Risk is the risk level of a row.
type Risk string

// The risk levels.
const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// PR statuses. Stored statuses come from GitHub sync (open|closed|merged) or the seed
// (needs_review|reviewed|stale); a row shows merged/closed as stored, else the derived
// needs_review / stale / reviewed exactly as the PR list does (re-implemented here, plan P1).
const (
	StatusOpen        = "open"
	StatusClosed      = "closed"
	StatusMerged      = "merged"
	StatusNeedsReview = "needs_review"
	StatusReviewed    = "reviewed"
	StatusStale       = "stale"
)

// ReasonKind is why a PR needs attention (spec FR13 a-e).
type ReasonKind string

// The reasons in their display and ordering priority (a)..(e).
const (
	ReasonMergedOpenCritical ReasonKind = "merged_open_critical" // (a) merged row with open criticals; Count
	ReasonOpenCritical       ReasonKind = "open_critical"        // (b) open PR, open criticals in current reviews; Count
	ReasonNoReview           ReasonKind = "no_review"            // (c) open, non-draft, never reviewed, opened > 24 h ago
	ReasonNewCommits         ReasonKind = "new_commits"          // (d) open, head differs from the last reviewed sha
	ReasonOpenLong           ReasonKind = "open_long"            // (e) open, not updated for >= 7 days; Days
)

// Query is the validated request: ParseQuery applies the defaults.
type Query struct {
	Period PeriodKey
	// TZ is the validated IANA zone name and Location its loaded form (never time.Local).
	TZ       string
	Location *time.Location
	// Repo is RepoAll or a repository UUID (existence is checked by the store, 404).
	Repo string
}

// Issue is one reason a query is invalid; transport renders the list as the 422
// validation_error details. Param is the query key ("period", "tz", "repo"), or the offending
// extra key itself.
type Issue struct {
	Param   string
	Message string
}

// Span is a half-open time range [From, To).
type Span struct {
	From, To time.Time
}

// Contains reports whether t lies in [From, To).
func (s Span) Contains(t time.Time) bool { return !t.Before(s.From) && t.Before(s.To) }

// Window is a resolved period in the viewer's zone (spec FR7). The previous window is the
// equally long window directly before (PrevTo == From). All ranges are [from, to).
type Window struct {
	Key      PeriodKey
	Location *time.Location
	From, To time.Time
	PrevFrom time.Time
	PrevTo   time.Time
	// Days is the number of calendar days of the window in Location: 7 for week and last_week,
	// 30 for 30d. A DST day keeps 23 or 25 hours and still counts once. For week the window is
	// the whole week even though data after now cannot exist.
	Days int
}

// Cur is the window as a Span.
func (w Window) Cur() Span { return Span{From: w.From, To: w.To} }

// Prev is the previous window as a Span.
func (w Window) Prev() Span { return Span{From: w.PrevFrom, To: w.PrevTo} }

// RepoRef is a repository in the filter.
type RepoRef struct {
	ID       string
	Owner    string
	Name     string // short name, shown in rows, awards and hot files
	FullName string // owner/name, echoed in the response's repos list
	// LastPolledAt is the repository's last successful poll, nil if never polled.
	LastPolledAt *time.Time
}

// Facts is everything one window read returns, covering [From, To) = [Window.PrevFrom,
// Window.To): both the window and the previous window, so the folds can compute deltas without
// a second read. Every slice is in no particular order. Rows of the two windows are told apart
// by the review / merge / run instants against the Window, never by the store.
//
// Membership (the store guarantees at least this, it may return more; the folds ignore what
// does not apply):
//   - Pulls: every PR of the filter that has a review (kind review) created in the range, OR a
//     merge time in the range, OR an opened time in the range.
//   - Reviews: every review of kind "review" created in the range, for the PRs above.
//   - Findings: all findings of every review that is the newest of its (PR, agent) inside the
//     current window OR inside the previous window (the two snapshots). Findings of other
//     reviews are not needed and ignored by the folds. "Agent" here is Review.AgentID, and
//     reviews of deleted agents ("") are one agent.
//   - Runs: every agent run with a PR whose RanAt lies in the range (any status; CI-ingested
//     runs have no PR and are absent).
type Facts struct {
	From, To time.Time
	Pulls    []PullFact
	Reviews  []ReviewFact
	Findings []FindingFact
	Runs     []RunFact
}

// PullFact is a stored pull request with the review facts the folds need.
type PullFact struct {
	PRID       string
	RepoID     string
	Repo       string // short repository name
	Number     int
	Title      string
	Author     string
	HeadSHA    string
	Additions  int
	Deletions  int
	FilesCount int
	// Status is the stored status (see the Status constants).
	Status string
	// Draft is the stored draft flag.
	Draft bool
	// LastReviewedSHA is the head that the last review ran against, nil if never reviewed.
	// New commits exist when it is non-nil and differs from HeadSHA (plan Q10).
	LastReviewedSHA *string
	// OpenedAt is GitHub created_at, nil for seeded rows (EC1).
	OpenedAt *time.Time
	// UpdatedAt is the last GitHub update.
	UpdatedAt *time.Time
	// MergedAt and ClosedAt are nil until synced after the 0002 migration (EC3): a merged PR
	// without MergedAt counts as merged in no window.
	MergedAt *time.Time
	ClosedAt *time.Time
	// FirstReviewAt is the creation time of the PR's first review of kind "review" EVER
	// (including before the range), nil when it has none.
	FirstReviewAt *time.Time
}

// ReviewFact is one review of kind "review".
type ReviewFact struct {
	ReviewID string
	PRID     string
	// AgentID is "" when the agent was deleted (reviews.agent_id NULL); AgentName is then ""
	// too and the folds name it DeletedAgentName.
	AgentID   string
	AgentName string
	// Score is the review's score 0-100, nil when the review has none.
	Score     *int
	CreatedAt time.Time
}

// FindingFact is one finding of a snapshot review.
type FindingFact struct {
	ReviewID string
	File     string
	// Severity is SeverityCritical, SeverityWarning or SeveritySuggestion.
	Severity string
	// AcceptedAt and DismissedAt are the triage times; accepting and dismissing clear each
	// other, so at most one is set (if both are, dismissed wins, EC7). A finding with neither
	// is open.
	AcceptedAt  *time.Time
	DismissedAt *time.Time
}

// RunFact is one agent run attached to a PR.
type RunFact struct {
	PRID  string
	RanAt time.Time
	// CostUSD is nil when the cost is unknown; the folds count it as 0 (plan Q3).
	CostUSD *float64
}

// OpenPull is the current state of an open PR (stored status neither merged nor closed),
// independent of any window (spec FR13 b-e).
type OpenPull struct {
	PRID            string
	RepoID          string
	Repo            string
	Number          int
	Title           string
	Author          string
	HeadSHA         string
	LastReviewedSHA *string
	Draft           bool
	OpenedAt        *time.Time
	UpdatedAt       *time.Time
	// Reviewed reports whether the PR has any review of kind "review", ever.
	Reviewed bool
	// OpenCritical counts CRITICAL findings, neither accepted nor dismissed, in the PR's
	// current reviews (the newest review per agent, ever; deleted agents are one agent).
	OpenCritical int
}

// FindingCounts are counts per severity.
type FindingCounts struct {
	Critical   int
	Warning    int
	Suggestion int
}

// Row is a ranked PR of a window (spec FR8-FR10): a PR with at least one review created in the
// window. Fields marked "domain only" are used by the aggregate folds and are not on the wire.
type Row struct {
	PRID   string
	RepoID string
	Repo   string // short repository name
	Number int
	Title  string
	Author string
	Status string // merged/closed as stored, else derived (needs_review|stale|reviewed)
	Draft  bool
	// Additions, Deletions and FilesCount are the stored diff stats; Size is derived from them.
	Additions  int
	Deletions  int
	FilesCount int
	Size       Size // SizeNone when unknown (wire null)
	// Health is the rounded mean of the snapshot scores, nil when no snapshot review has one.
	Health *int
	Risk   Risk
	// Findings sums all snapshot findings of the row, whatever their triage status.
	Findings FindingCounts
	// OpenCritical counts snapshot CRITICAL findings with neither accepted nor dismissed time.
	OpenCritical int
	// Rounds is the highest number of reviews any one agent wrote for the PR in the window.
	Rounds int
	// TimeToFirstReviewS is the first review ever minus opened time; nil without OpenedAt or
	// FirstReviewAt.
	TimeToFirstReviewS *int64
	// TimeToMergeS is merge time minus opened time; nil unless merged with both instants.
	TimeToMergeS *int64
	// OpenAgeS is now minus opened time for an open PR; nil for merged/closed PRs and without
	// OpenedAt.
	OpenAgeS *int64
	// CostUSD sums the cost of the PR's runs started in the window (unknown cost = 0).
	CostUSD float64
	// NewCommitsSinceReview is true when LastReviewedSHA is non-nil and differs from HeadSHA.
	NewCommitsSinceReview bool
	OpenedAt              *time.Time
	MergedAt              *time.Time
	// Reviews is the window snapshot: the newest review per agent created in the window,
	// ordered by AgentName, then ReviewID.
	Reviews []RowReview

	// FirstReviewAt is the first review ever (domain only; KPI "time to first review" takes
	// the rows whose first review lies in the window).
	FirstReviewAt *time.Time
}

// RowReview is one snapshot review of a row.
type RowReview struct {
	ReviewID  string
	AgentID   *string // nil (wire null) for the deleted-agent review
	AgentName string  // DeletedAgentName for the deleted-agent review
	Score     *int
	Findings  FindingCounts

	// CreatedAt is the review's time (domain only; the daily chart buckets CRITICAL findings
	// by the day of their review).
	CreatedAt time.Time
	// Items are the review's findings (domain only; hot files group them by file).
	Items []FindingItem
}

// FindingItem is a finding as the aggregate folds see it.
type FindingItem struct {
	File        string
	Severity    string
	AcceptedAt  *time.Time
	DismissedAt *time.Time
}

// KpiSet is the KPIs of one window (spec FR11). Nil means "not computable" (no value).
type KpiSet struct {
	PRsReviewed        int
	Merged             int
	AvgHealth          *int   // rounded mean of row health
	TimeToFirstReviewS *int64 // median
	TimeToMergeS       *int64 // median
	CriticalFound      int
	OpenCritical       int
	ReviewCostUSD      float64 // Σ cost of runs with a PR started in the window, rounded to MoneyDecimals
}

// Award is one award winner. Value is the metric of the award: score, count, seconds, lines or
// USD, depending on the award.
type Award struct {
	PRID   string
	Repo   string
	Number int
	Title  string
	Value  float64
}

// AwardSet is the six awards (spec FR12); an award without an eligible row is nil.
type AwardSet struct {
	Cleanest     *Award // highest health
	Hottest      *Award // most CRITICAL (value > 0)
	FastestMerge *Award // shortest time to merge among rows merged in the window
	MostRounds   *Award
	Monster      *Award // most changed lines (size known, value > 0)
	Priciest     *Award // highest cost (value > 0)
}

// AttentionReason is one reason of an attention item. Count is set (>= 1) for the critical
// reasons, Days (whole days since the last update, >= 7) for ReasonOpenLong; otherwise 0. The
// wire contract has both optional: transport omits a field whose value is 0 (never sends null).
type AttentionReason struct {
	Kind  ReasonKind
	Count int
	Days  int
}

// AttentionItem is a PR that needs attention with all its reasons (in (a)..(e) order).
type AttentionItem struct {
	PRID     string
	Repo     string
	Number   int
	Title    string
	Author   string
	OpenedAt *time.Time
	Reasons  []AttentionReason
}

// AttentionList is the needs-attention block: Total counts every PR, Items holds at most
// AttentionLimit of them. (It is not called Attention: that name is the fold function.)
type AttentionList struct {
	Total int
	Items []AttentionItem
}

// HotFile is a file with many snapshot findings (dismissed excluded).
type HotFile struct {
	Path     string
	Repo     string
	Findings int
	Critical int
	// PRs are the numbers of the rows with a finding in the file, ascending.
	PRs []int
}

// SizeBucket is the health-by-size entry of one bucket; AvgHealth and AvgFindings are nil when
// the bucket has no rows. AvgHealth is an integer, AvgFindings has one decimal.
type SizeBucket struct {
	Size        Size
	PRs         int
	AvgHealth   *int
	AvgFindings *float64
}

// Day is one calendar day of the window in the viewer's zone.
type Day struct {
	Date     string // YYYY-MM-DD
	Opened   int
	Merged   int
	Critical int
}

// Leaderboard is the use case's answer: everything the page shows.
type Leaderboard struct {
	Window          Window
	Repos           []RepoRef
	Summary         KpiSet
	PreviousSummary KpiSet
	Awards          AwardSet
	Rows            []Row
	NeedsAttention  AttentionList
	HotFiles        []HotFile
	HealthBySize    []SizeBucket
	Daily           []Day
}
