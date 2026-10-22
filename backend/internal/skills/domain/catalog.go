package domain

import "strings"

// CatalogEntry is a community catalog entry: the card the list shows plus the skill text an
// import builds from.
type CatalogEntry struct {
	ID    string
	Name  string
	Repo  string
	Stars int
	Lang  string
	// Desc is the one-line card text.
	Desc string
	Type SkillType
	Tags []string
	// Description is the "when it applies" line that becomes the skill description.
	Description string
	Body        string
}

// CatalogFilter narrows the catalog. Empty fields are ignored.
type CatalogFilter struct {
	Q    string
	Tag  string
	Lang string
}

// FilterCatalog keeps the entries that match every non-empty filter, case-insensitively after
// trimming: Tag equals one of the tags, Lang equals the language, Q is a substring of the name,
// the card text or a tag. The order is kept.
func FilterCatalog(entries []CatalogEntry, f CatalogFilter) []CatalogEntry {
	q := strings.ToLower(strings.TrimSpace(f.Q))
	tag := strings.ToLower(strings.TrimSpace(f.Tag))
	lang := strings.ToLower(strings.TrimSpace(f.Lang))
	out := []CatalogEntry{}
	for _, e := range entries {
		if tag != "" && !anyLower(e.Tags, func(t string) bool { return t == tag }) {
			continue
		}
		if lang != "" && strings.ToLower(e.Lang) != lang {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Name), q) &&
			!strings.Contains(strings.ToLower(e.Desc), q) &&
			!anyLower(e.Tags, func(t string) bool { return strings.Contains(t, q) }) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func anyLower(items []string, pred func(string) bool) bool {
	for _, it := range items {
		if pred(strings.ToLower(it)) {
			return true
		}
	}
	return false
}
