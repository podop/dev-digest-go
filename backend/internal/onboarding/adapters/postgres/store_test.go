package postgres_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/onboarding/adapters/postgres"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const goldenPath = "../../../parity/testdata/goldens/onboarding.json"

func ptr[T any](v T) *T { return &v }

func sampleTour(repoID string) domain.Tour {
	return domain.Tour{
		RepoID:        repoID,
		GeneratedAt:   time.Date(2026, 1, 1, 12, 0, 0, 123_000_000, time.UTC),
		IndexedSHA:    "abc123def456",
		FilesIndexed:  42,
		Provider:      "openrouter",
		Model:         "deepseek/deepseek-v4-flash",
		TokensIn:      5000,
		TokensOut:     900,
		CostUSD:       ptr(0.0123),
		PromptVersion: domain.PromptVersion,
		Language:      domain.Language,
		Architecture: domain.Architecture{
			Summary: "An HTTP `server` backed by a `db`.",
			Nodes:   []domain.Node{{ID: "server", Label: "Server", Kind: domain.KindEntry}, {ID: "db", Label: "Postgres", Kind: domain.KindStore}},
			Edges:   []domain.Edge{{From: "server", To: "db"}},
		},
		CriticalPaths: []domain.PathItem{{Path: "src/server.ts", Reason: "Entry point"}},
		RunSteps:      []domain.RunStep{{Command: "pnpm install"}, {Command: "pnpm dev", Comment: "Start the server"}},
		ReadingPath:   []domain.PathItem{{Path: "README.md", Reason: "Overview"}},
		FirstTasks:    []domain.FirstTask{{Title: "Wire the health check", Path: "src/server.ts", Complexity: domain.ComplexityLow}},
	}
}

type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	ws    string
	other string
	repo  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name, default_branch) VALUES (?, 'acme', 'shop', 'acme/shop', 'trunk') RETURNING id`, f.ws)
	return f
}

func (f fixture) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func TestFindRepo(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.FindRepo(t.Context(), f.ws, f.repo)
	if err != nil {
		t.Fatalf("FindRepo: %v", err)
	}
	want := domain.Repo{
		ID: f.repo, WorkspaceID: f.ws, Owner: "acme", Name: "shop", FullName: "acme/shop", DefaultBranch: "trunk",
		Provider: "github", Host: "github.com",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindRepo mismatch (-want +got):\n%s", diff)
	}
	if _, err := f.store.FindRepo(t.Context(), f.other, f.repo); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindRepo(other workspace) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.FindRepo(t.Context(), f.ws, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindRepo(unknown repo) error = %v, want ErrNotFound", err)
	}
}

func TestFindRepo_GitLabReturnsTheCloneKey(t *testing.T) {
	f := newFixture(t)
	gl := f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name, default_branch, provider, host, project_id, web_url)
		VALUES (?, 'grp/sub', 'web', 'grp/sub/web', 'main', 'gitlab', 'git.corp.example:8443', 4242, 'https://git.corp.example:8443/grp/sub/web') RETURNING id`, f.ws)
	got, err := f.store.FindRepo(t.Context(), f.ws, gl)
	if err != nil {
		t.Fatalf("FindRepo(gitlab): %v", err)
	}
	want := domain.Repo{
		ID: gl, WorkspaceID: f.ws, Owner: "@gitlab/git.corp.example_8443", Name: "4242", FullName: "grp/sub/web", DefaultBranch: "main",
		Provider: "gitlab", Host: "git.corp.example:8443", ProjectID: 4242,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindRepo(gitlab) mismatch (-want +got):\n%s", diff)
	}
}

func TestTour_NoRowIsAbsent(t *testing.T) {
	f := newFixture(t)
	_, ok, err := f.store.Tour(t.Context(), f.repo)
	if err != nil || ok {
		t.Errorf("Tour(no row) = ok %v, err %v, want absent", ok, err)
	}
}

func TestSaveTour_RoundTripsAndKeepsGeneratedAtInSync(t *testing.T) {
	f := newFixture(t)
	want := sampleTour(f.repo)
	if err := f.store.SaveTour(t.Context(), want); err != nil {
		t.Fatalf("SaveTour: %v", err)
	}
	got, ok, err := f.store.Tour(t.Context(), f.repo)
	if err != nil || !ok {
		t.Fatalf("Tour = ok %v, err %v, want the saved tour", ok, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Tour mismatch (-want +got):\n%s", diff)
	}
	var col time.Time
	if err := f.db.Raw(`SELECT generated_at FROM onboarding WHERE repo_id = ?`, f.repo).Scan(&col).Error; err != nil {
		t.Fatalf("select generated_at: %v", err)
	}
	if !col.Equal(want.GeneratedAt) {
		t.Errorf("onboarding.generated_at = %v, want the tour's %v", col, want.GeneratedAt)
	}
	var stamp string
	if err := f.db.Raw(`SELECT json->>'generated_at' FROM onboarding WHERE repo_id = ?`, f.repo).Scan(&stamp).Error; err != nil {
		t.Fatalf("select json generated_at: %v", err)
	}
	if stamp != "2026-01-01T12:00:00.123Z" {
		t.Errorf("stored generated_at = %q, want the ISO form with milliseconds", stamp)
	}
}

func TestSaveTour_UpsertsAndStoresNullCost(t *testing.T) {
	f := newFixture(t)
	first := sampleTour(f.repo)
	if err := f.store.SaveTour(t.Context(), first); err != nil {
		t.Fatalf("SaveTour(first): %v", err)
	}
	second := sampleTour(f.repo)
	second.GeneratedAt = first.GeneratedAt.Add(time.Hour)
	second.CostUSD = nil
	second.Architecture.Summary = "second"
	if err := f.store.SaveTour(t.Context(), second); err != nil {
		t.Fatalf("SaveTour(second): %v", err)
	}
	got, ok, err := f.store.Tour(t.Context(), f.repo)
	if err != nil || !ok {
		t.Fatalf("Tour = ok %v, err %v", ok, err)
	}
	if diff := cmp.Diff(second, got); diff != "" {
		t.Errorf("Tour after the upsert mismatch (-want +got):\n%s", diff)
	}
	var rows int
	if err := f.db.Raw(`SELECT count(*) FROM onboarding WHERE repo_id = ?`, f.repo).Scan(&rows).Error; err != nil || rows != 1 {
		t.Errorf("onboarding rows = %d, %v, want 1", rows, err)
	}
	var costType string
	if err := f.db.Raw(`SELECT jsonb_typeof(json->'cost_usd') FROM onboarding WHERE repo_id = ?`, f.repo).Scan(&costType).Error; err != nil || costType != "null" {
		t.Errorf("stored cost_usd type = %q, %v, want a present JSON null", costType, err)
	}
}

func TestSaveTour_NulCharactersAreMadeStorable(t *testing.T) {
	f := newFixture(t)
	tour := sampleTour(f.repo)
	tour.Architecture.Summary = "a\x00b"
	tour.ReadingPath = []domain.PathItem{{Path: "src/\x00.ts", Reason: "bad \xff byte"}}
	if err := f.store.SaveTour(t.Context(), tour); err != nil {
		t.Fatalf("SaveTour with U+0000: %v", err)
	}
	got, ok, err := f.store.Tour(t.Context(), f.repo)
	if err != nil || !ok {
		t.Fatalf("Tour = ok %v, err %v", ok, err)
	}
	if got.Architecture.Summary != "a�b" || got.ReadingPath[0].Path != "src/�.ts" || got.ReadingPath[0].Reason != "bad � byte" {
		t.Errorf("stored text = %q / %q / %q, want U+FFFD for U+0000 and invalid bytes", got.Architecture.Summary, got.ReadingPath[0].Path, got.ReadingPath[0].Reason)
	}
}

func TestSaveTour_ForAMissingRepoIsNotFound(t *testing.T) {
	f := newFixture(t)
	err := f.store.SaveTour(t.Context(), sampleTour("00000000-0000-4000-8000-000000000000"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SaveTour(missing repo) error = %v, want ErrNotFound", err)
	}
}

func TestTour_ADocumentThatFailsTheContractReadsAsAbsent(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]string{
		"summary over 800":    `jsonb_set(json, '{architecture,summary}', to_jsonb(repeat('x', 801)))`,
		"language not en":     `jsonb_set(json, '{language}', '"de"')`,
		"missing indexed_sha": `json - 'indexed_sha'`,
		"fractional version":  `jsonb_set(json, '{prompt_version}', '1.5')`,
		"unknown node kind":   `jsonb_set(json, '{architecture,nodes,0,kind}', '"queue"')`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.store.SaveTour(t.Context(), sampleTour(f.repo)); err != nil {
				t.Fatalf("SaveTour: %v", err)
			}
			if err := f.db.Exec(`UPDATE onboarding SET json = `+mutate+` WHERE repo_id = ?`, f.repo).Error; err != nil {
				t.Fatalf("corrupt the document: %v", err)
			}
			if _, ok, err := f.store.Tour(t.Context(), f.repo); err != nil || ok {
				t.Errorf("Tour(%s) = ok %v, err %v, want absent without an error", name, ok, err)
			}
		})
	}
}

// TestDecodeTour_MatchesNodeContract runs the stored-document cases of the Node golden
// (OnboardingTour.safeParse). The golden's generated_at is the string "g", which the contract
// accepts but no real document holds; it is replaced by an instant in both document and result.
func TestDecodeTour_MatchesNodeContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(goldenPath))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g struct {
		Stored []struct {
			Name   string          `json:"name"`
			Doc    json.RawMessage `json:"doc"`
			OK     bool            `json:"ok"`
			Parsed json.RawMessage `json:"parsed"`
		} `json:"stored"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(g.Stored) == 0 {
		t.Fatal("golden has no stored cases")
	}
	const instant = "2026-01-01T00:00:00.000Z"
	fix := func(doc json.RawMessage) []byte {
		var m map[string]any
		if json.Unmarshal(doc, &m) != nil || m["generated_at"] != "g" {
			return doc
		}
		m["generated_at"] = instant
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return out
	}
	for _, c := range g.Stored {
		t.Run(c.Name, func(t *testing.T) {
			tour, ok := postgres.DecodeTour(fix(c.Doc))
			if ok != c.OK {
				t.Fatalf("DecodeTour ok = %v, want %v", ok, c.OK)
			}
			if !ok {
				return
			}
			enc, err := postgres.EncodeTour(tour)
			if err != nil {
				t.Fatalf("EncodeTour: %v", err)
			}
			var want, got any
			if err := json.Unmarshal(fix(c.Parsed), &want); err != nil {
				t.Fatalf("decode parsed: %v", err)
			}
			if err := json.Unmarshal(enc, &got); err != nil {
				t.Fatalf("decode encoded: %v", err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeTour_Details(t *testing.T) {
	base := func() map[string]any {
		raw, err := json.Marshal(mustEncode(t, sampleTour("r")))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	tests := []struct {
		name   string
		mutate func(m map[string]any)
		wantOK bool
	}{
		{"unchanged", func(map[string]any) {}, true},
		{"unknown keys are ignored", func(m map[string]any) { m["extra"] = 1 }, true},
		{"integer written as 5000.0", func(m map[string]any) { m["tokens_in"] = json.Number("5000.0") }, true},
		{"cost_usd absent", func(m map[string]any) { delete(m, "cost_usd") }, false},
		{"cost_usd a string", func(m map[string]any) { m["cost_usd"] = "1" }, false},
		{"a list that is null", func(m map[string]any) { m["run_steps"] = nil }, false},
		{"six critical paths", func(m map[string]any) {
			m["critical_paths"] = []any{p(), p(), p(), p(), p(), p()}
		}, true},
		{"seven critical paths", func(m map[string]any) {
			m["critical_paths"] = []any{p(), p(), p(), p(), p(), p(), p()}
		}, false},
		{"comment over 120 units", func(m map[string]any) {
			m["run_steps"] = []any{map[string]any{"command": "x", "comment": strings.Repeat("c", 121)}}
		}, false},
		{"a label of 31 astral runes is 62 units", func(m map[string]any) {
			m["architecture"] = map[string]any{"summary": "s", "edges": []any{}, "nodes": []any{map[string]any{"id": "a", "label": strings.Repeat("\U0001F600", 31), "kind": "entry"}}}
		}, false},
		{"empty command", func(m map[string]any) { m["run_steps"] = []any{map[string]any{"command": ""}} }, false},
		{"generated_at not an instant", func(m map[string]any) { m["generated_at"] = "yesterday" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			doc, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := postgres.DecodeTour(doc); ok != tc.wantOK {
				t.Errorf("DecodeTour(%s) ok = %v, want %v", tc.name, ok, tc.wantOK)
			}
		})
	}
	for _, doc := range []string{`[]`, `null`, `"x"`, ``, `{`} {
		if _, ok := postgres.DecodeTour([]byte(doc)); ok {
			t.Errorf("DecodeTour(%q) ok = true, want false", doc)
		}
	}
}

func p() any { return map[string]any{"path": "a.ts", "reason": "r"} }

func mustEncode(t *testing.T, tour domain.Tour) json.RawMessage {
	t.Helper()
	b, err := postgres.EncodeTour(tour)
	if err != nil {
		t.Fatalf("EncodeTour: %v", err)
	}
	return b
}
