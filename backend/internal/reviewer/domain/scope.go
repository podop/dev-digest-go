package domain

// ScopeResult is the outcome of ApplyScopePolicy.
type ScopeResult struct {
	Findings []Finding
	// OutOfScope and OutOfScopeCritical count the findings flagged out of scope (zero without intent).
	OutOfScope, OutOfScopeCritical int
}

// ApplyScopePolicy normalises the model's own out-of-scope judgment; it never drops a finding and
// never changes a severity. Without a PR intent (the switch is off or the intent is unavailable)
// the model had nothing to judge against, so every flag is reset to nil. With an intent every
// flag becomes a plain boolean and the counts say how many findings are out of scope (spec 7.5).
// The input slice is not modified.
func ApplyScopePolicy(findings []Finding, hasIntent bool) ScopeResult {
	out := make([]Finding, len(findings))
	res := ScopeResult{}
	for i, f := range findings {
		if !hasIntent {
			f.OutOfScope = nil
		} else {
			flagged := f.OutOfScope != nil && *f.OutOfScope
			if flagged {
				res.OutOfScope++
				if f.Severity == SeverityCritical {
					res.OutOfScopeCritical++
				}
			}
			f.OutOfScope = &flagged
		}
		out[i] = f
	}
	res.Findings = out
	return res
}
