package domain

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Kind is the type of unit of work an LLM answer (or a CI run) was paid for (FR1, FR6).
type Kind string

// The eight kinds of ledger entry. The first seven are LLM units; KindCIRun is the CI ingest.
const (
	KindReviewRun       Kind = "review_run"
	KindReviewIntent    Kind = "review_intent"
	KindIntentRefresh   Kind = "intent_refresh"
	KindBrief           Kind = "brief"
	KindOnboarding      Kind = "onboarding"
	KindConventionsScan Kind = "conventions_scan"
	KindEvalCase        Kind = "eval_case"
	KindCIRun           Kind = "ci_run"
)

// ErrInvalidKind is returned by ParseKind for a string that names no kind.
var ErrInvalidKind = errors.New("invalid ledger kind")

// ParseKind returns the Kind named by s, or an error wrapping ErrInvalidKind.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindReviewRun, KindReviewIntent, KindIntentRefresh, KindBrief, KindOnboarding,
		KindConventionsScan, KindEvalCase, KindCIRun:
		return k, nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidKind, s)
}

// Origin tells how an entry came to be.
type Origin string

// The three origins of an entry (FR2).
const (
	// OriginLive is an answer recorded when it was received.
	OriginLive Origin = "live"
	// OriginCIIngest is a CI run recorded by CI ingest (FR6).
	OriginCIIngest Origin = "ci_ingest"
	// OriginBackfill is a cost record copied from the pre-ledger data by the migration (FR10).
	OriginBackfill Origin = "backfill"
)

// ErrInvalidOrigin is returned by ParseOrigin for a string that names no origin.
var ErrInvalidOrigin = errors.New("invalid ledger origin")

// ParseOrigin returns the Origin named by s, or an error wrapping ErrInvalidOrigin.
func ParseOrigin(s string) (Origin, error) {
	switch o := Origin(s); o {
	case OriginLive, OriginCIIngest, OriginBackfill:
		return o, nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidOrigin, s)
}

// Entry is one append-only ledger record: what one answered LLM response (or one CI run) cost
// (FR2). It holds no prompt, output, diff, path, title, name, key or header.
type Entry struct {
	WorkspaceID string
	// OccurredAt is when the answer was received (server clock); the month it counts in is its
	// UTC month (EC11).
	OccurredAt time.Time
	Kind       Kind
	Origin     Origin
	// Provider and Model are as used for the call; empty only for backfilled entries.
	Provider string
	Model    string
	// TokensIn and TokensOut are nil when the call site does not know them.
	TokensIn  *int64
	TokensOut *int64
	// Cost is nil when the cost is unknown (no price, or a corrupt reported cost, FR3).
	Cost *Micros
	// RefID is an informational reference to the unit of work (never enforced); empty if none.
	RefID string
	// DedupeKey is unique across the ledger: a fresh random key for live entries, CIDedupeKey
	// for CI runs, a source-derived key for backfilled entries. Appending a key that already
	// exists is a no-op.
	DedupeKey string
}

// CIDedupeKey is the dedupe key of the entry of one GitHub workflow run attempt (FR6, D16), so a
// re-ingest, even after a history reset, never counts it twice.
func CIDedupeKey(githubRunID int64, attempt int) string {
	return "ci:" + strconv.FormatInt(githubRunID, 10) + ":" + strconv.Itoa(attempt)
}

// GitHubHost is the host key whose CI runs keep the original dedupe key.
const GitHubHost = "github.com"

// CIDedupeKeyFor is the dedupe key of the entry of one CI run attempt on a given host (FR36,
// EC30, SCR1). host is the repository's host key (lower case, ":port" unless 443). For github.com
// (and "" for callers that predate hosts) it is exactly CIDedupeKey, so stored GitHub keys stay
// valid; any other host gets "ci:<host>:<id>:<attempt>", so a GitLab job id never collides with a
// GitHub run id, nor the same id on two GitLab hosts. id is the GitHub run id or the GitLab job id.
func CIDedupeKeyFor(host string, id int64, attempt int) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || h == GitHubHost {
		return CIDedupeKey(id, attempt)
	}
	return "ci:" + h + ":" + strconv.FormatInt(id, 10) + ":" + strconv.Itoa(attempt)
}

// MonthTotal is the stored total of one workspace and calendar month: the exact sum of the known
// costs of its entries and the number of entries whose cost is unknown (FR7).
type MonthTotal struct {
	Spent    Micros
	Unpriced int
}

// maxCostUSD is the exclusive upper bound of a plausible cost in dollars. It keeps the micro-USD
// amount (and a month's sum of such amounts) well inside int64.
const maxCostUSD = 1e12

// CostFromUSD converts a reported or estimated cost in dollars to Micros, rounded half away from
// zero to 6 decimals on the decimal value of v (FR3, D15). A nil, NaN, infinite, negative or
// implausibly large (>= 1e12) cost is unknown and returns nil (EC21).
func CostFromUSD(usd *float64) *Micros {
	if usd == nil {
		return nil
	}
	v := *usd
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= maxCostUSD {
		return nil
	}
	// Round on the shortest decimal form of v, not on v*1e6: the product of a binary float can
	// land just below a .5 boundary (NFR5).
	s := strconv.FormatFloat(v, 'f', -1, 64)
	whole, frac, _ := strings.Cut(s, ".")
	frac = (frac + "0000000")[:7]
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return nil
	}
	f, err := strconv.ParseInt(frac[:6], 10, 64)
	if err != nil {
		return nil
	}
	m := w*microsPerUSD + f
	if frac[6] >= '5' {
		m++
	}
	out := Micros(m)
	return &out
}
