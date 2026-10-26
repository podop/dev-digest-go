package domain

import (
	"errors"
	"fmt"
	"time"
)

// Errors the skills store reports. The use cases turn them into application errors.
var (
	// ErrNotFound means no skill matches the id in the workspace.
	ErrNotFound = errors.New("skill not found")
	// ErrVersionNotFound means the skill has no snapshot of that version.
	ErrVersionNotFound = errors.New("skill version not found")
	// ErrDuplicateName means the workspace already has a skill with that name.
	ErrDuplicateName = errors.New("skill name already exists in the workspace")
	// ErrRepoNotFound means no repo matches the id in the workspace.
	ErrRepoNotFound = errors.New("repo not found")
)

// Source says where a skill came from.
type Source string

// The skill sources. A client may set every source except Extracted.
const (
	SourceManual       Source = "manual"
	SourceImportedFile Source = "imported_file"
	SourceImportedURL  Source = "imported_url"
	SourceExtracted    Source = "extracted"
	SourceCommunity    Source = "community"
)

// SkillTypeNames lists the skill types in contract order.
func SkillTypeNames() []string {
	return []string{string(TypeRubric), string(TypeConvention), string(TypeSecurity), string(TypeCustom)}
}

// CreatableSourceNames lists the sources a client may send, in contract order.
func CreatableSourceNames() []string {
	return []string{string(SourceManual), string(SourceImportedFile), string(SourceImportedURL), string(SourceCommunity)}
}

// ParseCreatableSource returns the source spelled exactly like s when a client may set it.
func ParseCreatableSource(s string) (Source, bool) {
	switch src := Source(s); src {
	case SourceManual, SourceImportedFile, SourceImportedURL, SourceCommunity:
		return src, true
	default:
		return "", false
	}
}

// Skill is a named, versioned piece of review guidance an agent can link.
type Skill struct {
	ID          string
	WorkspaceID string
	Name        string
	Description string
	Type        SkillType
	Source      Source
	// SourceRef is the provenance (file name, URL, community:<id>, conventions:<repo>); nil for
	// manual skills.
	SourceRef *string
	Body      string
	Enabled   bool
	// Version is the current version, bumped by a change of body or description.
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	// UsedBy is the number of agents linking the skill.
	UsedBy int
	// Plugin is the installed plugin that owns the skill; nil for a skill the user made. Only the
	// store's reads fill it; no write changes the owner.
	Plugin *PluginRef
}

// PluginRef names the installed plugin that owns a skill.
type PluginRef struct {
	ID      string
	Name    string
	Enabled bool
}

// Version is the immutable snapshot of a skill's model-facing text.
type Version struct {
	SkillID     string
	Version     int
	Body        string
	Description *string
	Message     *string
	CreatedAt   time.Time
}

// AgentRef is an agent that links a skill.
type AgentRef struct {
	ID      string
	Name    string
	Enabled bool
}

// NewSkill is the data of a skill to insert (as version 1).
type NewSkill struct {
	WorkspaceID string
	Name        string
	Description string
	Type        SkillType
	Source      Source
	SourceRef   *string
	Body        string
	Enabled     bool
}

// NewVersion is a snapshot to insert.
type NewVersion struct {
	SkillID     string
	Version     int
	Body        string
	Description string
	Message     string
}

// Patch is the validated body of a skill update: nil fields are not sent.
type Patch struct {
	Name        *string
	Description *string
	Type        *SkillType
	Body        *string
	Enabled     *bool
	BaseVersion *int
}

// Write is what the store changes on a skill row: nil fields keep their value. updated_at is
// always set by the store.
type Write struct {
	Name        *string
	Description *string
	Type        *SkillType
	Body        *string
	Enabled     *bool
	Version     *int
}

// EditMessage is the snapshot message of the patch applied to cur, or "" when the patch does not
// change the body or the description (sending the same text again is no change). Name, type and
// enabled never create a version.
func (p Patch) EditMessage(cur Skill) string {
	body := p.Body != nil && *p.Body != cur.Body
	description := p.Description != nil && *p.Description != cur.Description
	switch {
	case body && description:
		return "Edited body and description"
	case body:
		return "Edited body"
	case description:
		return "Edited description"
	default:
		return ""
	}
}

// CreatedMessage is the message of version 1: "Created", or "Imported from <ref>" for a skill
// that has a foreign source with a reference.
func CreatedMessage(source Source, sourceRef *string) string {
	if source == SourceManual || sourceRef == nil || *sourceRef == "" {
		return "Created"
	}
	return "Imported from " + *sourceRef
}

// ExtractedMessage is the message of version 1 of a skill merged from a repo's conventions.
func ExtractedMessage(repoFullName string) string {
	return "Extracted from conventions of " + repoFullName
}

// ExtractedSourceRef is the provenance of a skill merged from a repo's conventions.
func ExtractedSourceRef(repoFullName string) string { return "conventions:" + repoFullName }

// RestoredMessage is the message of a version written by restoring version v.
func RestoredMessage(v int) string { return fmt.Sprintf("Restored from v%d", v) }

// InitialEnabled applies the trust gate: a skill that did not come from the user's own hand is
// foreign instructions and is stored disabled, whatever was asked. A manual skill takes the
// requested value, true by default.
func InitialEnabled(source Source, requested *bool) bool {
	if source != SourceManual {
		return false
	}
	return requested == nil || *requested
}

// UTF16Len is the length of s in UTF-16 code units, which is how the contract counts characters.
func UTF16Len(s string) int { return utf16Len(s) }

// ---- statistics ---------------------------------------------------------------------------------

// StatsDefaultDays and StatsMaxDays bound the statistics window.
const (
	StatsDefaultDays = 30
	StatsMaxDays     = 365
)

// Counters are the raw statistics of one skill over a window.
type Counters struct {
	RunsAttached int
	RunsCited    int
	Findings     int
	Accepted     int
	Dismissed    int
}

// CountBy is a count per key.
type CountBy struct {
	Key   string
	Count int
}

// Breakdown groups the findings of a skill by category and by severity, each by count
// descending, then key ascending.
type Breakdown struct {
	ByCategory []CountBy
	BySeverity []CountBy
}

// Stats is the usage of one skill over a window.
type Stats struct {
	SkillID      string
	WindowDays   int
	RunsAttached int
	RunsCited    int
	PullRate     *float64
	Findings     int
	Accepted     int
	Dismissed    int
	AcceptRate   *float64
	ByCategory   []CountBy
	BySeverity   []CountBy
	UsedBy       []AgentRef
}

// StatsSummary is the line of a skill on the list cards.
type StatsSummary struct {
	SkillID    string
	PullRate   *float64
	AcceptRate *float64
	Findings   int
}

// Rate is num/den, or nil when nothing was measured (den is 0). It is not rounded.
func Rate(num, den int) *float64 {
	if den <= 0 {
		return nil
	}
	r := float64(num) / float64(den)
	return &r
}

// PullRate is the share of the runs that had the skill in the prompt and cited it.
func (c Counters) PullRate() *float64 { return Rate(c.RunsCited, c.RunsAttached) }

// AcceptRate is the share of the acted-on findings that were accepted.
func (c Counters) AcceptRate() *float64 { return Rate(c.Accepted, c.Accepted+c.Dismissed) }

// WindowStart is the start of a window of days × 24 h that ends at now.
func WindowStart(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}
