package rank

import (
	"sort"
	"strings"

	"dev-digest-go/backend/internal/repointel/domain"
)

// Critical path parameters (spec section 7.3, CriticalPaths).
const (
	// CriticalPathRoots is the number of top-ranked files that start a chain.
	CriticalPathRoots = 5
	// CriticalPathDepth is the number of import hops a chain follows.
	CriticalPathDepth = 2
)

// junkPatterns are the path fragments of tests, configs, declarations, migrations and fixtures
// that rank-driven samples skip. They are matched against the lower-cased path.
var junkPatterns = [...]string{
	".test.",
	".spec.",
	".d.ts",
	"__tests__/",
	"__mocks__/",
	"/test/",
	"/tests/",
	"/migrations/",
	"/__fixtures__/",
	".config.",
	"vitest.",
	"jest.",
	"eslint",
	"prettier",
}

// IsJunkPath reports whether path is a test, config, declaration, migration or fixture path.
// Lower-casing follows JavaScript's toLowerCase for the one character where Go differs: U+0130
// becomes "i" plus a combining dot, not a plain "i".
func IsJunkPath(path string) bool {
	lower := strings.ToLower(strings.ReplaceAll(path, "\u0130", "i\u0307"))
	for _, p := range junkPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// TopFilesScan is the number of ranked rows to read for TopFiles(n): max(10n, 100), so the junk
// filter still leaves n paths where possible.
func TopFilesScan(n int) int {
	return max(n*10, 100)
}

// TopFiles returns the first n paths of ranked (rank DESC, path ASC) that are not junk and
// contain none of the exclude substrings (case-sensitive). n <= 0 gives an empty result.
func TopFiles(ranked []domain.RankedPath, n int, exclude []string) []string {
	out := []string{}
	if n <= 0 {
		return out
	}
	for _, r := range ranked {
		if IsJunkPath(r.Path) || containsAny(r.Path, exclude) {
			continue
		}
		out = append(out, r.Path)
		if len(out) >= n {
			break
		}
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// CriticalPaths returns the dependency chains from the CriticalPathRoots top-ranked files.
// ranked holds all ranked rows ordered by rank DESC, then path ASC. From each root the chain
// follows up to CriticalPathDepth hops, each time to the not-yet-visited import target with the
// highest rank (a missing rank counts as 0; ties go to the smaller path in byte order). Chains
// shorter than 2 and duplicate chains are dropped. No edges give an empty result.
func CriticalPaths(edges []domain.Edge, ranked []domain.RankedPath) [][]string {
	chains := [][]string{}
	if len(edges) == 0 {
		return chains
	}
	rankOf := make(map[string]float64, len(ranked))
	for _, r := range ranked {
		rankOf[r.Path] = r.Rank
	}
	targets := make(map[string][]string)
	for _, e := range edges {
		targets[e.From] = append(targets[e.From], e.To)
	}

	seen := make(map[string]struct{})
	for _, root := range ranked[:min(CriticalPathRoots, len(ranked))] {
		chain := []string{root.Path}
		visited := map[string]struct{}{root.Path: {}}
		cur := root.Path
		for hop := 0; hop < CriticalPathDepth; hop++ {
			next, ok := bestTarget(targets[cur], visited, rankOf)
			if !ok {
				break
			}
			chain = append(chain, next)
			visited[next] = struct{}{}
			cur = next
		}
		if len(chain) < 2 {
			continue
		}
		key := strings.Join(chain, "\x00")
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		chains = append(chains, chain)
	}
	return chains
}

// bestTarget picks the unvisited candidate with the highest rank, ties by path ascending.
func bestTarget(candidates []string, visited map[string]struct{}, rankOf map[string]float64) (string, bool) {
	var open []string
	for _, c := range candidates {
		if _, v := visited[c]; !v {
			open = append(open, c)
		}
	}
	if len(open) == 0 {
		return "", false
	}
	sort.Slice(open, func(a, b int) bool {
		ra, rb := rankOf[open[a]], rankOf[open[b]]
		if ra != rb {
			return ra > rb
		}
		return open[a] < open[b]
	})
	return open[0], true
}
