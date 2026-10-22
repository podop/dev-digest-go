package domain

import (
	"slices"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Build groups files by role and attaches every file's finding lines. The groups are always the
// five of Roles(), in that order; the files of a group are sorted by path in English collation
// (JavaScript localeCompare) and each carries the distinct, ascending start lines of the findings
// on that path. A finding on a path that is not one of the files is ignored. TotalLines is the sum
// of additions and deletions over all files.
func Build(files []PullFile, findings []FindingRef) SmartDiff {
	linesByFile := make(map[string]map[int]struct{})
	for _, f := range findings {
		set := linesByFile[f.File]
		if set == nil {
			set = make(map[int]struct{})
			linesByFile[f.File] = set
		}
		set[f.StartLine] = struct{}{}
	}

	sorted := slices.Clone(files)
	reviewer.SortEnglishBy(sorted, func(f PullFile) string { return f.Path })

	roles := Roles()
	byRole := make(map[Role][]FileEntry, len(roles))
	total := 0
	for _, f := range sorted {
		lines := make([]int, 0, len(linesByFile[f.Path]))
		for l := range linesByFile[f.Path] {
			lines = append(lines, l)
		}
		slices.Sort(lines)
		role := Classify(f.Path)
		byRole[role] = append(byRole[role], FileEntry{
			Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, FindingLines: lines,
		})
		total += f.Additions + f.Deletions
	}

	groups := make([]Group, len(roles))
	for i, r := range roles {
		entries := byRole[r]
		if entries == nil {
			entries = []FileEntry{}
		}
		groups[i] = Group{Role: r, Files: entries}
	}
	return SmartDiff{Groups: groups, TotalLines: total}
}
