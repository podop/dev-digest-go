package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// exportableKeys is the allow-list of settings an export may contain (spec FR14), including the
// keys Slices A and C define as exportable. Every other stored key, known or not, stays out.
var exportableKeys = [...]string{
	"automatic_reviews",
	"default_model",
	"density",
	"feature_models",
	"monthly_spend_cap_usd",
	"polling_interval_min",
	"retention_days",
	"sync_to_folder",
	"theme",
}

// automaticReviewsKey is the setting whose value carries row ids and instance state that an
// export drops (spec D26).
const automaticReviewsKey = "automatic_reviews"

// automaticReviewsDropped are the keys removed from the automatic_reviews object: agent row ids
// and the instant automatic reviews were switched on.
var automaticReviewsDropped = [...]string{"agent_ids", "enabled_at"}

// ExportableKeys returns the allow-listed settings keys, sorted.
func ExportableKeys() []string { return slices.Clone(exportableKeys[:]) }

// ExportSettings returns the allow-listed part of the stored settings, never nil. Keys outside
// the allow-list are dropped; automatic_reviews loses agent_ids and enabled_at, and a value of
// it that is neither null nor a JSON object is dropped, since its content is unknown.
func ExportSettings(stored map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(exportableKeys))
	for _, key := range exportableKeys {
		raw, ok := stored[key]
		if !ok {
			continue
		}
		if key == automaticReviewsKey {
			filtered, keep := filterAutomaticReviews(raw)
			if !keep {
				continue
			}
			raw = filtered
		}
		out[key] = raw
	}
	return out
}

// filterAutomaticReviews drops agent_ids and enabled_at from an object value.
func filterAutomaticReviews(raw json.RawMessage) (json.RawMessage, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return json.RawMessage("null"), true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	for _, k := range automaticReviewsDropped {
		delete(obj, k)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}

// maxSlugLen is the longest name slug in an export file name.
const maxSlugLen = 40

// ExportFileName returns "devdigest-workspace-<slug>-<YYYY-MM-DD>.json". The slug is the name
// lower-cased with every run of characters outside a-z and 0-9 turned into one "-", trimmed of
// "-", cut to 40 characters, and "workspace" when nothing is left; the date is now in UTC.
func ExportFileName(name string, now time.Time) string {
	return fmt.Sprintf("devdigest-workspace-%s-%s.json", slugify(name), now.UTC().Format("2006-01-02"))
}

func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}
	if slug == "" {
		return "workspace"
	}
	return slug
}

// UniqueNames returns names made unique, in order: the first holder of a name keeps it and a
// later one becomes "<name> (2)", "<name> (3)", taking the next suffix not yet used by any
// earlier result (spec FR13, EC13, A6). The input is in creation order and is not changed.
func UniqueNames(names []string) []string {
	used := make(map[string]struct{}, len(names))
	out := make([]string, len(names))
	for i, name := range names {
		candidate := name
		for n := 2; ; n++ {
			if _, taken := used[candidate]; !taken {
				break
			}
			candidate = fmt.Sprintf("%s (%d)", name, n)
		}
		used[candidate] = struct{}{}
		out[i] = candidate
	}
	return out
}
