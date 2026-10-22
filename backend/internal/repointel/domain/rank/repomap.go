package rank

import (
	"strings"

	"dev-digest-go/backend/internal/repointel/domain"
)

// MapHeader is the first line of every repo map.
const MapHeader = "# Repo skeleton (top-ranked by import graph only, partial view)"

type mapItem struct {
	path      string
	signature string
}

// RenderMap renders the token-budgeted repo map of spec section 7.7 and returns its text and its
// token count. candidates must already be in the section's order (rank DESC, exported DESC, line
// ASC, name ASC, path ASC): the largest prefix of items that fits budget is kept. An empty
// signature and a (path, signature) pair already taken are skipped, which also folds the
// dual-emitted Class.method / method rows into the first. With no items, or when not even the
// header fits, the result is the header line.
func RenderMap(candidates []domain.MapCandidate, counter domain.TokenCounter, budget int) (text string, tokens int) {
	best := MapHeader + "\n"
	bestTokens := counter.Count(best)

	items := mapItems(candidates)
	if len(items) == 0 {
		return best, bestTokens
	}
	lo, hi := 0, len(items)
	for lo <= hi {
		mid := (lo + hi) / 2
		rendered := renderItems(items[:mid])
		if t := counter.Count(rendered); t <= budget {
			best, bestTokens = rendered, t
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, bestTokens
}

func mapItems(candidates []domain.MapCandidate) []mapItem {
	items := make([]mapItem, 0, len(candidates))
	seen := make(map[mapItem]struct{}, len(candidates))
	for _, c := range candidates {
		if c.Signature == "" {
			continue
		}
		it := mapItem{path: c.Path, signature: c.Signature}
		if _, dup := seen[it]; dup {
			continue
		}
		seen[it] = struct{}{}
		items = append(items, it)
	}
	return items
}

// renderItems groups items by file in first-appearance order under the header.
func renderItems(items []mapItem) string {
	var order []string
	byFile := make(map[string][]string)
	for _, it := range items {
		sigs, ok := byFile[it.path]
		if !ok {
			order = append(order, it.path)
		}
		byFile[it.path] = append(sigs, "  "+it.signature)
	}
	var b strings.Builder
	b.WriteString(MapHeader)
	b.WriteByte('\n')
	for _, p := range order {
		b.WriteString(p)
		b.WriteString(":\n")
		b.WriteString(strings.Join(byFile[p], "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}
