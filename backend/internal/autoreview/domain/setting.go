// Package domain holds the pure rules of automatic reviews: which stored pull requests are
// candidates, what happens to one (record a skip, defer, ask the budget, start), the rate limits and
// the state of the status line. It depends on nothing but the standard library.
package domain

import "time"

// MaxAgents is the most agents an automatic review can name (spec 7.1).
const MaxAgents = 20

// Setting is the stored `automatic_reviews` value as the trigger reads it. The settings context
// owns reading, validating and persisting it; the trigger only sees this view.
type Setting struct {
	Enabled bool
	// AgentIDs are the selected agents in run order, unique, at most MaxAgents.
	AgentIDs     []string
	OnNewPR      bool
	OnNewCommits bool
	// EnabledAt is when the setting became armed; nil while it is not armed.
	EnabledAt *time.Time
}

// Armed reports whether the trigger may run for the setting: enabled, at least one agent selected
// and at least one trigger condition ticked (spec glossary).
func (s Setting) Armed() bool {
	return s.Enabled && len(s.AgentIDs) > 0 && (s.OnNewPR || s.OnNewCommits)
}

// Ticked reports whether the setting's trigger condition for the event is ticked.
func (s Setting) Ticked(e Event) bool {
	switch e {
	case EventNewPR:
		return s.OnNewPR
	case EventNewCommits:
		return s.OnNewCommits
	default:
		return false
	}
}

// EffectiveAgents returns the selected agent ids, in setting order, that are in enabled (the ids of
// the existing, enabled agents). Duplicates in selected are dropped, keeping the first. The result
// is never nil.
func EffectiveAgents(selected, enabled []string) []string {
	ok := make(map[string]struct{}, len(enabled))
	for _, id := range enabled {
		ok[id] = struct{}{}
	}
	out := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if _, found := ok[id]; found {
			out = append(out, id)
		}
	}
	return out
}
