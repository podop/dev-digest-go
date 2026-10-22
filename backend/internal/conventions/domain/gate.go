package domain

import "math"

// Gate limits (FR246).
const (
	// MaxCandidates is the number of candidates the gate considers.
	MaxCandidates = 15
	// MaxEvidencePerCandidate is the number of evidence items considered per candidate.
	MaxEvidencePerCandidate = 3
	// RuleMaxChars is the length cap of a rule (UTF-16 code units).
	RuleMaxChars = 500
)

// Candidate is a convention as the model proposed it, after schema validation.
type Candidate struct {
	Category   string
	Rule       string
	Confidence float64
	Evidence   []ClaimedEvidence
}

// KeptConvention is a candidate that passed the gate.
type KeptConvention struct {
	Category   string
	Rule       string
	Evidence   []Evidence
	Confidence float64
}

// Dropped records a candidate the gate refused.
type Dropped struct {
	Rule   string
	Path   string
	Reason DropReason
}

// GateResult is the outcome of GateCandidates.
type GateResult struct {
	Kept    []KeptConvention
	Dropped []Dropped
}

// CitedPaths lists, in order of first citation, every safe path the considered
// candidates cite, so the caller reads each file once.
func CitedPaths(candidates []Candidate) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, c := range head(candidates, MaxCandidates) {
		for _, e := range head(c.Evidence, MaxEvidencePerCandidate) {
			p, ok := SafeRelativePath(e.Path)
			if !ok {
				continue
			}
			if _, dup := seen[p]; !dup {
				seen[p] = struct{}{}
				out = append(out, p)
			}
		}
	}
	return out
}

func head[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func clamp01(n float64) float64 {
	switch {
	case math.IsNaN(n) || math.IsInf(n, 0):
		return 0
	case n < 0:
		return 0
	case n > 1:
		return 1
	}
	return n
}

// GateCandidates verifies evidence and removes duplicates over files the caller
// already read. lines(path) returns the file's lines, or false for a missing,
// unsafe or unreadable file. knownKeys holds the RuleKey of rules the repository
// already decided on (accepted, rejected or edited); they are never re-proposed.
// A candidate is kept when at least one evidence item verifies; confidence is
// clamped to 0..1.
func GateCandidates(candidates []Candidate, lines func(path string) ([]string, bool), knownKeys map[string]struct{}) GateResult {
	var res GateResult
	seen := make(map[string]struct{}, len(knownKeys))
	for k := range knownKeys {
		seen[k] = struct{}{}
	}

	for _, c := range head(candidates, MaxCandidates) {
		rule := truncateUTF16(collapse(c.Rule), RuleMaxChars)
		firstPath := ""
		if len(c.Evidence) > 0 {
			firstPath = c.Evidence[0].Path
		}
		key := RuleKey(rule)
		if key == "" {
			res.Dropped = append(res.Dropped, Dropped{Rule: rule, Path: firstPath, Reason: ReasonInvalid})
			continue
		}
		if _, dup := seen[key]; dup {
			res.Dropped = append(res.Dropped, Dropped{Rule: rule, Path: firstPath, Reason: ReasonDuplicate})
			continue
		}

		var evidence []Evidence
		firstFailure := ReasonFileNotFound
		if len(c.Evidence) == 0 {
			firstFailure = ReasonInvalid
		}
		failed := false
		for _, claimed := range head(c.Evidence, MaxEvidencePerCandidate) {
			var ev Evidence
			reason := ReasonFileNotFound
			ok := false
			if path, safe := SafeRelativePath(claimed.Path); safe {
				if file, readable := lines(path); readable {
					claimed.Path = path
					ev, reason, ok = VerifyEvidence(file, claimed)
				}
			}
			switch {
			case ok:
				if !hasEvidenceAt(evidence, ev) {
					evidence = append(evidence, ev)
				}
			case !failed:
				firstFailure, failed = reason, true
			}
		}

		if len(evidence) == 0 {
			res.Dropped = append(res.Dropped, Dropped{Rule: rule, Path: firstPath, Reason: firstFailure})
			continue
		}
		seen[key] = struct{}{}
		res.Kept = append(res.Kept, KeptConvention{
			Category:   c.Category,
			Rule:       rule,
			Evidence:   evidence,
			Confidence: clamp01(c.Confidence),
		})
	}
	return res
}

func hasEvidenceAt(list []Evidence, e Evidence) bool {
	for _, x := range list {
		if x.Path == e.Path && x.StartLine == e.StartLine {
			return true
		}
	}
	return false
}
