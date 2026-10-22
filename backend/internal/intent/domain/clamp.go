package domain

// Clamps of the model's own output (spec 7.4.5; Node intent/domain/intent.ts clampIntentText and
// clampScopeList): defence in depth, applied before the record is stored.

import reviewer "dev-digest-go/backend/internal/reviewer/domain"

// ClampIntentText trims the intent text and cuts it to IntentMaxUnits.
func ClampIntentText(text string) string { return reviewer.Cut(text, IntentMaxUnits) }

// ClampScopeList keeps the first ScopeMaxItems items, each trimmed and cut to ScopeItemMaxUnits,
// and drops the ones that are empty then. The result is never nil.
func ClampScopeList(items []string) []string {
	out := []string{}
	for i, item := range items {
		if i >= ScopeMaxItems {
			break
		}
		if c := reviewer.Cut(item, ScopeItemMaxUnits); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Clamp applies the clamps to a classification; ChangeType is kept as returned.
func Clamp(c Classification) Classification {
	return Classification{
		Intent:     ClampIntentText(c.Intent),
		InScope:    ClampScopeList(c.InScope),
		OutOfScope: ClampScopeList(c.OutOfScope),
		ChangeType: c.ChangeType,
	}
}

// BuildIntent assembles the intent of a derivation from the clamped classification and the code-set
// parts: derived_from, confidence and the record part of every source.
func BuildIntent(c Classification, derivedFrom, confidence string, sources []Source) reviewer.Intent {
	cl := Clamp(c)
	change := cl.ChangeType
	conf := confidence
	return reviewer.Intent{
		Text: cl.Intent, InScope: cl.InScope, OutOfScope: cl.OutOfScope,
		ChangeType: &change, Confidence: &conf, DerivedFrom: derivedFrom, Sources: IntentSources(sources),
	}
}
