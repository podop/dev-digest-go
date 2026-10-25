package app

import (
	"encoding/json"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
)

// AutomaticReviewsKey is the settings key of the automatic-reviews value.
const AutomaticReviewsKey = "automatic_reviews"

// MaxAutoReviewAgents is the most agents the value can name.
const MaxAutoReviewAgents = 20

// enabledAtLayout is the wire form of enabled_at: ISO-8601 UTC with milliseconds.
const enabledAtLayout = "2006-01-02T15:04:05.000Z"

// AutomaticReviews is the stored value of the setting automatic_reviews (spec section 7.1).
// Its JSON is always the full object, key order as declared.
type AutomaticReviews struct {
	Enabled      bool     `json:"enabled"`
	AgentIDs     []string `json:"agent_ids"`
	OnNewPR      bool     `json:"on_new_pr"`
	OnNewCommits bool     `json:"on_new_commits"`
	// EnabledAt is set by the server when an update arms the setting; nil while not armed.
	EnabledAt *string `json:"enabled_at"`
}

// DefaultAutomaticReviews is the value of an install that never stored one: off, no agents,
// both triggers on.
func DefaultAutomaticReviews() AutomaticReviews {
	return AutomaticReviews{AgentIDs: []string{}, OnNewPR: true, OnNewCommits: true}
}

// Armed says whether automatic reviews can start runs: enabled, at least one agent and at least
// one trigger condition ticked.
func (a AutomaticReviews) Armed() bool {
	return a.Enabled && len(a.AgentIDs) > 0 && (a.OnNewPR || a.OnNewCommits)
}

// JSON returns the stored form of the value.
func (a AutomaticReviews) JSON() json.RawMessage {
	if a.AgentIDs == nil {
		a.AgentIDs = []string{}
	}
	out, err := json.Marshal(a)
	if err != nil { // a struct of bools and strings cannot fail to marshal
		panic("settings/app: marshal AutomaticReviews: " + err.Error())
	}
	return out
}

// AutomaticReviewsFromStored reads a stored value as the full object (FR1). A missing, null or
// unreadable value reads as the defaults with enabled false; a stored boolean b (the legacy form)
// reads as the defaults with enabled b and no enabled_at; in an object every absent or ill-typed
// field takes its default. It never fails: a stored value must not break GET /settings.
func AutomaticReviewsFromStored(raw json.RawMessage) AutomaticReviews {
	out := DefaultAutomaticReviews()
	switch jsonKind(raw) {
	case 't':
		out.Enabled = true
	case '{':
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return out
		}
		if b, ok := boolField(fields, "enabled"); ok {
			out.Enabled = b
		}
		if b, ok := boolField(fields, "on_new_pr"); ok {
			out.OnNewPR = b
		}
		if b, ok := boolField(fields, "on_new_commits"); ok {
			out.OnNewCommits = b
		}
		if ids, ok := stringsField(fields, "agent_ids"); ok {
			out.AgentIDs = uniqueIDs(ids)
		}
		var at string
		if json.Unmarshal(fields["enabled_at"], &at) == nil {
			if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
				formatted := t.UTC().Format(enabledAtLayout)
				out.EnabledAt = &formatted
			}
		}
	}
	if !out.Armed() {
		out.EnabledAt = nil // "null when not armed" (section 7.1)
	}
	return out
}

// automaticReviewsPatch is a PUT input: every field is optional. The transport layer validates
// it; the settings layer merges it into the stored value.
type automaticReviewsPatch struct {
	Enabled      *bool
	AgentIDs     []string // nil = keep
	OnNewPR      *bool
	OnNewCommits *bool
}

// parseAutomaticReviewsPatch reads a validated input: a boolean (= {enabled: b}) or an object
// with any subset of enabled, agent_ids, on_new_pr, on_new_commits. enabled_at and unknown
// fields are dropped. ok is false for any other shape or an ill-typed field.
func parseAutomaticReviewsPatch(raw json.RawMessage) (p automaticReviewsPatch, ok bool) {
	switch jsonKind(raw) {
	case 't', 'f':
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return p, false
		}
		p.Enabled = &b
		return p, true
	case '{':
	default:
		return p, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return p, false
	}
	for key, dst := range map[string]**bool{"enabled": &p.Enabled, "on_new_pr": &p.OnNewPR, "on_new_commits": &p.OnNewCommits} {
		if _, present := fields[key]; !present {
			continue
		}
		b, isBool := boolField(fields, key)
		if !isBool {
			return p, false
		}
		*dst = &b
	}
	if _, present := fields["agent_ids"]; present {
		ids, isList := stringsField(fields, "agent_ids")
		if !isList {
			return p, false
		}
		p.AgentIDs = uniqueIDs(ids)
		if len(p.AgentIDs) > MaxAutoReviewAgents {
			return p, false
		}
	}
	return p, true
}

// merge applies the patch to the stored value and sets enabled_at (FR2): now when the result is
// armed and the stored value was not, kept while it stays armed, nil when it is not armed. A
// stored armed value without a usable enabled_at counts as newly armed, so a hand-edited row can
// never reach back to PRs that existed before.
func (p automaticReviewsPatch) merge(stored AutomaticReviews, now time.Time) AutomaticReviews {
	out := stored
	if p.Enabled != nil {
		out.Enabled = *p.Enabled
	}
	if p.AgentIDs != nil {
		out.AgentIDs = p.AgentIDs
	}
	if p.OnNewPR != nil {
		out.OnNewPR = *p.OnNewPR
	}
	if p.OnNewCommits != nil {
		out.OnNewCommits = *p.OnNewCommits
	}
	switch {
	case !out.Armed():
		out.EnabledAt = nil
	case stored.Armed() && stored.EnabledAt != nil:
		out.EnabledAt = stored.EnabledAt
	default:
		at := now.UTC().Format(enabledAtLayout)
		out.EnabledAt = &at
	}
	return out
}

// mergeAutomaticReviews turns the validated PUT input of automatic_reviews into the full value to
// store, given the stored raw value (nil when none). Concurrent PUTs are last write wins.
func mergeAutomaticReviews(input, stored json.RawMessage, now time.Time) (json.RawMessage, error) {
	patch, ok := parseAutomaticReviewsPatch(input)
	if !ok {
		return nil, apperr.New(apperr.KindValidation, "automatic_reviews: invalid value")
	}
	return patch.merge(AutomaticReviewsFromStored(stored), now).JSON(), nil
}

// jsonKind is the first byte of the JSON value, with 't' for true and 'f' for false; 0 for empty.
func jsonKind(raw json.RawMessage) byte {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0
	}
	return s[0]
}

// boolField reads a JSON boolean; ok is false when the field is absent or not a boolean.
func boolField(fields map[string]json.RawMessage, key string) (value, ok bool) {
	switch jsonKind(fields[key]) {
	case 't':
		return true, true
	case 'f':
		return false, true
	default:
		return false, false
	}
}

// stringsField reads a JSON array of strings; ok is false when the field is absent or any
// element is not a string.
func stringsField(fields map[string]json.RawMessage, key string) ([]string, bool) {
	var ids []string
	if jsonKind(fields[key]) != '[' || json.Unmarshal(fields[key], &ids) != nil {
		return nil, false
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, true
}

// uniqueIDs lower-cases the ids and removes duplicates, keeping the first.
func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(id)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
