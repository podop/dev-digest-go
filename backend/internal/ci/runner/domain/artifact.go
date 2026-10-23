package domain

import (
	"math"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// RunnerVersion is the version string every artifact carries (informational) and the trace line
// prints.
const RunnerVersion = "2"

// ArtifactInput is what the artifact is built from. Nil pointers are JSON null.
type ArtifactInput struct {
	Findings []reviewer.Finding
	// CostUSD is nil when the model calls were not all priced.
	CostUSD *float64
	// DurationMS is the wall time of the review engine.
	DurationMS int64
	// Agent is the manifest name.
	Agent    string
	PRNumber int
	// CommitSHA, Repository and the three integers are the provenance the studio verifies on
	// ingest; counts and metadata only, never finding text (it may quote a secret).
	CommitSHA                       *string
	Repository                      string
	RepositoryID, RunID, RunAttempt *int64
	Model                           string
	ManifestVersion                 *int64
	// Skills are the slugs of the manifest.
	Skills   []string
	Blockers int
}

// Artifact is the devdigest-result.json the runner writes (spec 7.8.8): it satisfies the
// CiResultArtifact rules the ingest applies.
type Artifact struct {
	FindingsCount                   int
	Critical, Warning, Suggestion   int
	CostUSD                         *float64
	DurationMS                      int64
	Agent                           string
	Version                         string
	PRNumber                        int
	CommitSHA                       *string
	Repository                      string
	RepositoryID, RunID, RunAttempt *int64
	Model                           string
	ManifestVersion                 *int64
	Skills                          []string
	Blockers                        int
}

// BuildArtifact counts the findings by severity (any severity but CRITICAL and WARNING counts as
// a suggestion) and fills the provenance. The only failure is a cost that is not a number, which
// the Node CiResultArtifact validation rejected.
func BuildArtifact(in ArtifactInput) (Artifact, error) {
	if in.CostUSD != nil && math.IsNaN(*in.CostUSD) {
		return Artifact{}, NewRunnerError("Internal error: built result artifact failed CiResultArtifact validation: cost_usd: Expected number, received nan")
	}
	a := Artifact{
		FindingsCount: len(in.Findings),
		CostUSD:       in.CostUSD, DurationMS: in.DurationMS, Agent: in.Agent, Version: RunnerVersion,
		PRNumber: in.PRNumber, CommitSHA: in.CommitSHA, Repository: in.Repository,
		RepositoryID: in.RepositoryID, RunID: in.RunID, RunAttempt: in.RunAttempt,
		Model: in.Model, ManifestVersion: in.ManifestVersion, Skills: in.Skills, Blockers: in.Blockers,
	}
	for _, f := range in.Findings {
		switch f.Severity {
		case reviewer.SeverityCritical:
			a.Critical++
		case reviewer.SeverityWarning:
			a.Warning++
		default:
			a.Suggestion++
		}
	}
	return a, nil
}

// IntOrNull reads a CI-provided integer variable: digits only (ASCII) and a JavaScript safe
// integer, else nil. An unset variable is passed as "".
func IntOrNull(value string) *int64 {
	if value == "" {
		return nil
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return nil
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n > maxSafeInteger {
		return nil
	}
	return &n
}

// JSON is the file content the runner writes: JSON.stringify(artifact, null, 2) plus a final
// line break. Keys keep the order of the Node artifact; only the quote, the backslash and the
// control characters of a string are escaped (no HTML escaping, and U+2028/U+2029 are written
// as they are, which encoding/json does not do); numbers are formatted as JavaScript does.
func (a Artifact) JSON() string {
	var sb strings.Builder
	sb.WriteString("{\n")
	field := func(key, value string, last bool) {
		sb.WriteString("  " + JSONString(key) + ": " + value)
		if !last {
			sb.WriteByte(',')
		}
		sb.WriteByte('\n')
	}
	itoa := func(n int) string { return strconv.Itoa(n) }
	opt := func(p *int64) string {
		if p == nil {
			return "null"
		}
		return strconv.FormatInt(*p, 10)
	}
	cost := "null"
	if a.CostUSD != nil && !math.IsInf(*a.CostUSD, 0) {
		cost = reviewer.JSNumber(*a.CostUSD)
	}
	commit := "null"
	if a.CommitSHA != nil {
		commit = JSONString(*a.CommitSHA)
	}
	skills := "[]"
	if len(a.Skills) > 0 {
		items := make([]string, len(a.Skills))
		for i, s := range a.Skills {
			items[i] = "    " + JSONString(s)
		}
		skills = "[\n" + strings.Join(items, ",\n") + "\n  ]"
	}

	field("findings_count", itoa(a.FindingsCount), false)
	field("critical", itoa(a.Critical), false)
	field("warning", itoa(a.Warning), false)
	field("suggestion", itoa(a.Suggestion), false)
	field("cost_usd", cost, false)
	field("duration_ms", strconv.FormatInt(a.DurationMS, 10), false)
	field("agent", JSONString(a.Agent), false)
	field("version", JSONString(a.Version), false)
	field("pr_number", itoa(a.PRNumber), false)
	field("commit_sha", commit, false)
	field("repository", JSONString(a.Repository), false)
	field("repository_id", opt(a.RepositoryID), false)
	field("run_id", opt(a.RunID), false)
	field("run_attempt", opt(a.RunAttempt), false)
	field("model", JSONString(a.Model), false)
	field("manifest_version", opt(a.ManifestVersion), false)
	field("skills", skills, false)
	field("blockers", itoa(a.Blockers), true)
	sb.WriteString("}\n")
	return sb.String()
}

// JSONString is JSON.stringify of a string: only the quote, the backslash and the control
// characters are escaped (\b \t \n \f \r by letter, the rest as lower-case \u00xx); invalid UTF-8
// is written as U+FFFD.
func JSONString(s string) string {
	const hex = "0123456789abcdef"
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '\b':
			sb.WriteString(`\b`)
		case r == '\f':
			sb.WriteString(`\f`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20:
			sb.WriteString(`\u00`)
			sb.WriteByte(hex[r>>4])
			sb.WriteByte(hex[r&0xF])
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
