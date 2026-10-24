package domain

import (
	"cmp"
	"math"
	"slices"
)

// HotFiles orders the hot-file facts of the store (spec FR14) and caps them at HotFilesLimit. The
// store has already counted the snapshot findings of the current window's rows per file (a file
// is a (repository, path) pair; dismissed findings excluded, accepted ones still count; findings
// without a path ignored) and returns at most HotFilesLimit files; it must use the order below
// (SQL ORDER BY of the hot-files query in adapters/postgres, same constant), so that the files it
// cuts off are the ones this function would cut. Order: findings desc, critical desc, path asc
// (then repository, then the PR numbers, then the repository id). PRs are the row numbers with
// a finding in the file, ascending and distinct. The result is a copy and never nil.
func HotFiles(files []HotFile) []HotFile {
	out := make([]HotFile, len(files))
	for i, f := range files {
		f.PRs = slices.Sorted(slices.Values(f.PRs))
		out[i] = f
	}
	slices.SortFunc(out, func(a, b HotFile) int {
		return cmp.Or(
			cmp.Compare(b.Findings, a.Findings),
			cmp.Compare(b.Critical, a.Critical),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Repo, b.Repo),
			slices.Compare(a.PRs, b.PRs),
			cmp.Compare(a.RepoID, b.RepoID),
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
