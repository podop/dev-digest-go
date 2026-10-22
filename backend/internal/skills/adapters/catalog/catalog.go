// Package catalog serves the community skill catalog that ships inside the binary. Listing and
// importing an entry make no network call.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"dev-digest-go/backend/internal/skills/domain"
)

//go:embed catalog.json
var catalogJSON []byte

// entryJSON is the shape of an entry in catalog.json.
type entryJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Repo        string   `json:"repo"`
	Stars       int      `json:"stars"`
	Lang        string   `json:"lang"`
	Type        string   `json:"type"`
	Tags        []string `json:"tags"`
	Desc        string   `json:"desc"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
}

// Catalog is the immutable list of shipped community skills, in display order.
type Catalog struct {
	entries []domain.CatalogEntry
}

// New parses the embedded catalog. It fails when the data is malformed or an entry is unusable.
func New() (*Catalog, error) {
	var raw []entryJSON
	if err := json.Unmarshal(catalogJSON, &raw); err != nil {
		return nil, fmt.Errorf("parse community catalog: %w", err)
	}
	entries := make([]domain.CatalogEntry, len(raw))
	seen := map[string]bool{}
	for i, r := range raw {
		typ, ok := domain.ParseSkillType(r.Type)
		if !ok {
			return nil, fmt.Errorf("community catalog entry %q: unknown type %q", r.ID, r.Type)
		}
		if r.ID == "" || seen[r.ID] {
			return nil, fmt.Errorf("community catalog entry %d: empty or duplicate id %q", i, r.ID)
		}
		seen[r.ID] = true
		entries[i] = domain.CatalogEntry{
			ID: r.ID, Name: r.Name, Repo: r.Repo, Stars: r.Stars, Lang: r.Lang, Desc: r.Desc,
			Type: typ, Tags: r.Tags, Description: r.Description, Body: r.Body,
		}
	}
	return &Catalog{entries: entries}, nil
}

// List returns the entries in catalog order. The result is a copy the caller may modify.
func (c *Catalog) List() []domain.CatalogEntry {
	out := make([]domain.CatalogEntry, len(c.entries))
	for i, e := range c.entries {
		out[i] = cloneEntry(e)
	}
	return out
}

func cloneEntry(e domain.CatalogEntry) domain.CatalogEntry {
	e.Tags = slices.Clone(e.Tags)
	return e
}

// Get returns the entry with the given id.
func (c *Catalog) Get(id string) (domain.CatalogEntry, bool) {
	for _, e := range c.entries {
		if e.ID == id {
			return cloneEntry(e), true
		}
	}
	return domain.CatalogEntry{}, false
}
