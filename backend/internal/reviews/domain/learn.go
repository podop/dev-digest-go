package domain

// LearnContextPrefix starts the sources context that identifies a learned finding.
const LearnContextPrefix = "finding:"

// LearnedMemoryContent is the memory text Learn stores for a finding: the title, a blank line,
// the rationale and, when there is a non-empty suggestion, a blank line and "Suggested fix:
// <suggestion>".
func LearnedMemoryContent(f FindingRecord) string {
	content := f.Title + "\n\n" + f.Rationale
	if f.Suggestion != nil && *f.Suggestion != "" {
		content += "\n\nSuggested fix: " + *f.Suggestion
	}
	return content
}

// LearnContext is the sources context of a learned finding ("finding:<id>"), the idempotency key
// of Learn.
func LearnContext(findingID string) string { return LearnContextPrefix + findingID }

// LearnLockKey is the key of the transaction-scoped advisory lock that serialises Learn calls on
// one finding.
func LearnLockKey(findingID string) string { return "learn:" + findingID }

// NewLearnMemory is the memory entry Learn stores for a target: the finding's text and
// confidence, in the PR's repo, from the PR the finding was reported on.
func NewLearnMemory(workspaceID string, t LearnTarget) NewMemory {
	return NewMemory{
		WorkspaceID: workspaceID,
		RepoID:      t.RepoID,
		FindingID:   t.Finding.ID,
		PRNumber:    t.PRNumber,
		Content:     LearnedMemoryContent(t.Finding),
		Confidence:  t.Finding.Confidence,
	}
}
