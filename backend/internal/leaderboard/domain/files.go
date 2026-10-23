package domain

import (
	"cmp"
	"math"
	"slices"
)

// HotFiles returns the files with the most snapshot findings over the rows (spec FR14), dismissed
// findings excluded (accepted ones still count). A file is a (repository, path) pair; findings
// without a path are ignored. Ordered by findings desc, critical desc, path asc (then
// repository), at most HotFilesLimit entries. PRs are the row numbers with a finding in the file,
// ascending and distinct.
func HotFiles(rows []Row) []HotFile {
	type key struct{ repoID, repo, path string }
	type acc struct {
		file HotFile
		prs  map[int]struct{}
	}
	byFile := make(map[key]*acc)
	for _, r := range rows {
		for _, rv := range r.Reviews {
			for _, it := range rv.Items {
				if it.DismissedAt != nil || it.File == "" {
					continue
				}
				k := key{r.RepoID, r.Repo, it.File}
				a, ok := byFile[k]
				if !ok {
					a = &acc{file: HotFile{Path: it.File, RepoID: r.RepoID, Repo: r.Repo}, prs: make(map[int]struct{})}
					byFile[k] = a
				}
				a.file.Findings++
				if it.Severity == SeverityCritical {
					a.file.Critical++
				}
				a.prs[r.Number] = struct{}{}
			}
		}
	}

	out := make([]HotFile, 0, len(byFile))
	for _, a := range byFile {
		a.file.PRs = make([]int, 0, len(a.prs))
		for n := range a.prs {
			a.file.PRs = append(a.file.PRs, n)
		}
		slices.Sort(a.file.PRs)
		out = append(out, a.file)
	}
	slices.SortFunc(out, func(a, b HotFile) int {
		return cmp.Or(
			cmp.Compare(b.Findings, a.Findings),
			cmp.Compare(b.Critical, a.Critical),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Repo, b.Repo),
			slices.Compare(a.PRs, b.PRs),
		)
	})
	return out[:min(len(out), HotFilesLimit)]
}

// HealthBySize returns the five size buckets XS, S, M, L, XL in order (spec FR15). Rows of unknown
// size (EC9) are left out. AvgHealth is the rounded mean over the bucket's rows that have a
// health; AvgFindings is the mean number of snapshot findings per PR with one decimal. Both are
// nil when there is nothing to average (no rows; for health also no row with a health).
func HealthBySize(rows []Row) []SizeBucket {
	type acc struct{ prs, healthN, healthSum, findings int }
	accs := make(map[Size]*acc, len(Sizes))
	for _, s := range Sizes {
		accs[s] = &acc{}
	}
	for _, r := range rows {
		a, ok := accs[r.Size]
		if !ok {
			continue
		}
		a.prs++
		a.findings += r.Findings.Critical + r.Findings.Warning + r.Findings.Suggestion
		if r.Health != nil {
			a.healthN++
			a.healthSum += *r.Health
		}
	}
	out := make([]SizeBucket, 0, len(Sizes))
	for _, s := range Sizes {
		a := accs[s]
		b := SizeBucket{Size: s, PRs: a.prs}
		if a.healthN > 0 {
			h := int(math.Round(float64(a.healthSum) / float64(a.healthN)))
			b.AvgHealth = &h
		}
		if a.prs > 0 {
			f := aggRound(float64(a.findings)/float64(a.prs), 1)
			b.AvgFindings = &f
		}
		out = append(out, b)
	}
	return out
}
