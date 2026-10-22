package postgres_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/intent/adapters/postgres"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

func errorsIsNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }

// fixture is a migrated database with a workspace, a repo and a pull request (#482).
type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	ws    string
	repo  string
	pr    string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'payments-api', 'acme/payments-api') RETURNING id`, f.ws)
	f.pr = f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, body)
		VALUES (?, ?, 482, 'Add rate limiting', 'dev', 'feat/rate-limit', 'main', 'a1b2c3d4e5f6', 'Body text') RETURNING id`, f.ws, f.repo)
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

func (f fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func TestPull(t *testing.T) {
	f := newFixture(t)

	t.Run("found", func(t *testing.T) {
		got, err := f.store.Pull(t.Context(), f.ws, f.pr)
		want := domain.Pull{ID: f.pr, RepoID: f.repo, Number: 482, Title: "Add rate limiting", Body: "Body text", Branch: "feat/rate-limit", HeadSHA: "a1b2c3d4e5f6"}
		if err != nil || cmp.Diff(want, got) != "" {
			t.Errorf("Pull() = %+v, %v, want %+v", got, err, want)
		}
	})
	t.Run("NULL body reads as empty", func(t *testing.T) {
		f.exec(t, `UPDATE pull_requests SET body = NULL WHERE id = ?`, f.pr)
		got, err := f.store.Pull(t.Context(), f.ws, f.pr)
		if err != nil || got.Body != "" {
			t.Errorf("Pull() = %+v, %v, want an empty body", got, err)
		}
	})
	t.Run("another workspace is not found", func(t *testing.T) {
		other := f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
		if _, err := f.store.Pull(t.Context(), other, f.pr); !errorsIsNotFound(err) {
			t.Errorf("Pull(other workspace) error = %v, want domain.ErrNotFound", err)
		}
	})
	t.Run("unknown id is not found", func(t *testing.T) {
		if _, err := f.store.Pull(t.Context(), f.ws, "99999999-9999-4999-8999-999999999999"); !errorsIsNotFound(err) {
			t.Errorf("Pull(unknown) error = %v, want domain.ErrNotFound", err)
		}
	})
}

func TestRepo(t *testing.T) {
	f := newFixture(t)

	got, err := f.store.Repo(t.Context(), f.repo)
	if want := (domain.Repo{ID: f.repo, Owner: "acme", Name: "payments-api"}); err != nil || got != want {
		t.Errorf("Repo() = %+v, %v, want %+v", got, err, want)
	}
	if _, err := f.store.Repo(t.Context(), "99999999-9999-4999-8999-999999999999"); !errorsIsNotFound(err) {
		t.Errorf("Repo(unknown) error = %v, want domain.ErrNotFound", err)
	}
}

func TestCommits_ByCommittedAtNullLastThenID(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ id, sha, msg, at string }{
		{"00000000-0000-4000-8000-0000000000b2", "b2", "second, same time, higher id", "2026-01-02T00:00:00Z"},
		{"00000000-0000-4000-8000-0000000000a1", "a1", "second, same time, lower id", "2026-01-02T00:00:00Z"},
		{"00000000-0000-4000-8000-0000000000c3", "c3", "first", "2026-01-01T00:00:00Z"},
	} {
		f.exec(t, `INSERT INTO pr_commits (id, pr_id, sha, message, author, committed_at) VALUES (?, ?, ?, ?, 'dev', ?)`, c.id, f.pr, c.sha, c.msg, c.at)
	}
	f.exec(t, `INSERT INTO pr_commits (id, pr_id, sha, message, author, committed_at) VALUES ('00000000-0000-4000-8000-0000000000d4', ?, 'd4', 'undated', 'dev', NULL)`, f.pr)

	got, err := f.store.Commits(t.Context(), f.pr)

	want := []domain.Commit{{SHA: "c3", Message: "first"}, {SHA: "a1", Message: "second, same time, lower id"}, {SHA: "b2", Message: "second, same time, higher id"}, {SHA: "d4", Message: "undated"}}
	if err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("Commits() = %+v, %v, want %+v", got, err, want)
	}
	if none, err := f.store.Commits(t.Context(), "99999999-9999-4999-8999-999999999999"); err != nil || len(none) != 0 || none == nil {
		t.Errorf("Commits(no rows) = %#v, %v, want an empty slice", none, err)
	}
}

func TestChangedFiles_InByteOrder(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"b.go", "B.go", "a/z.go", "a-b.go", "é.go"} {
		f.exec(t, `INSERT INTO pr_files (pr_id, path) VALUES (?, ?)`, f.pr, p)
	}

	got, err := f.store.ChangedFiles(t.Context(), f.pr)

	want := []string{"B.go", "a-b.go", "a/z.go", "b.go", "é.go"}
	if err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("ChangedFiles() = %v, %v, want %v", got, err, want)
	}
}

func record(prID string) domain.Record {
	return domain.Record{
		PRID: prID, Text: "Add a token-bucket rate limiter.", InScope: []string{"Limiter", "Config"}, OutOfScope: []string{"Auth"},
		ChangeType: ptr("feature"), Confidence: ptr("medium"), DerivedFrom: "explicit",
		Sources: []reviewer.IntentSource{
			{Kind: "title", Ref: "title", Status: "used"},
			{Kind: "ticket", Ref: "7", Status: "failed", Detail: ptr("Not Found")},
		},
		HeadSHA: "a1b2c3d4e5f6", InputHash: strings.Repeat("ab", 32), PromptVersion: 2, Provider: "openrouter", Model: "deepseek/deepseek-v4-flash",
		TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042), DerivedAt: time.Date(2026, 1, 1, 12, 0, 0, 123_000_000, time.UTC),
	}
}

func TestStore_UpsertAndGetRoundTrip(t *testing.T) {
	f := newFixture(t)
	want := record(f.pr)

	got, err := f.store.Upsert(t.Context(), want)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Upsert() mismatch (-want +got):\n%s", diff)
	}
	read, ok, err := f.store.Get(t.Context(), f.pr)
	if err != nil || !ok {
		t.Fatalf("Get() = %v, %v", ok, err)
	}
	if diff := cmp.Diff(want, read); diff != "" {
		t.Errorf("Get() mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_UpsertReplacesEveryColumn(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Upsert(t.Context(), record(f.pr)); err != nil {
		t.Fatal(err)
	}
	second := domain.Record{
		PRID: f.pr, Text: "Second.", InScope: []string{}, OutOfScope: []string{}, DerivedFrom: "inferred",
		Sources: []reviewer.IntentSource{}, InputHash: "h2", PromptVersion: 2, Provider: "openai", Model: "gpt-4.1",
		DerivedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	got, err := f.store.Upsert(t.Context(), second)

	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if diff := cmp.Diff(second, got); diff != "" {
		t.Errorf("second Upsert() mismatch (-want +got):\n%s", diff)
	}
	var rows int
	if err := f.db.Raw(`SELECT count(*) FROM pr_intent WHERE pr_id = ?`, f.pr).Scan(&rows).Error; err != nil || rows != 1 {
		t.Errorf("pr_intent rows = %d, %v, want 1", rows, err)
	}
}

func TestStore_CostIsRoundedToSixDecimalsByTheColumn(t *testing.T) {
	tests := []struct {
		name string
		cost *float64
		want *float64
	}{
		{"exact", ptr(0.00042), ptr(0.00042)},
		{"rounded", ptr(0.0004236789), ptr(0.000424)},
		{"below the column's scale", ptr(4e-07), ptr(0.0)},
		{"zero", ptr(0.0), ptr(0.0)},
		{"unpriced", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rec := record(f.pr)
			rec.CostUSD = tc.cost

			got, err := f.store.Upsert(t.Context(), rec)

			if err != nil || cmp.Diff(tc.want, got.CostUSD) != "" {
				t.Errorf("Upsert(cost %v).CostUSD = %v, %v, want %v", tc.cost, got.CostUSD, err, tc.want)
			}
		})
	}
}

func TestStore_GetReadsNullColumnsAsDefaults(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `INSERT INTO pr_intent (pr_id, intent, derived_at) VALUES (?, 'I', '2026-01-01T00:00:00Z')`, f.pr)

	got, ok, err := f.store.Get(t.Context(), f.pr)

	want := domain.Record{
		PRID: f.pr, Text: "I", InScope: []string{}, OutOfScope: []string{}, DerivedFrom: "inferred",
		Sources: []reviewer.IntentSource{}, DerivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err != nil || !ok || cmp.Diff(want, got) != "" {
		t.Errorf("Get() = %+v, %v, %v, want %+v", got, ok, err, want)
	}
}

func TestStore_GetNoRecord(t *testing.T) {
	f := newFixture(t)

	if _, ok, err := f.store.Get(t.Context(), f.pr); ok || err != nil {
		t.Errorf("Get(no row) = %v, %v, want no record", ok, err)
	}
	f.exec(t, `INSERT INTO pr_intent (pr_id, intent) VALUES (?, 'never derived')`, f.pr)
	if _, ok, err := f.store.Get(t.Context(), f.pr); ok || err != nil {
		t.Errorf("Get(row with NULL derived_at) = %v, %v, want no record", ok, err)
	}
}

func TestStore_ReadsSourcesWrittenByNode(t *testing.T) {
	f := newFixture(t)
	// Node writes detail: null explicitly; a source stored without the key reads as no detail.
	f.exec(t, `INSERT INTO pr_intent (pr_id, intent, in_scope, derived_at, sources) VALUES (?, 'I', '["a"]', '2026-01-01T00:00:00Z',
		'[{"kind":"title","ref":"title","status":"used","detail":null},{"kind":"doc","ref":"d.md","status":"skipped","detail":"doc total budget reached"},{"kind":"branch","ref":"branch","status":"used"}]')`, f.pr)

	got, _, err := f.store.Get(t.Context(), f.pr)

	want := []reviewer.IntentSource{
		{Kind: "title", Ref: "title", Status: "used"},
		{Kind: "doc", Ref: "d.md", Status: "skipped", Detail: ptr("doc total budget reached")},
		{Kind: "branch", Ref: "branch", Status: "used"},
	}
	if err != nil || cmp.Diff(want, got.Sources) != "" || cmp.Diff([]string{"a"}, got.InScope) != "" {
		t.Errorf("Get() sources = %+v, in_scope %v, %v, want %+v", got.Sources, got.InScope, err, want)
	}
}

func TestStore_WritesTheSourcesColumnWithDetailNull(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Upsert(t.Context(), record(f.pr)); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.db.Raw(`SELECT sources::text FROM pr_intent WHERE pr_id = ?`, f.pr).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"detail": null`) || !strings.Contains(raw, `"detail": "Not Found"`) {
		t.Errorf("sources column = %s, want detail present on every source (null when none)", raw)
	}
}

func TestStore_TextWithNULAndInvalidUTF8IsStored(t *testing.T) {
	f := newFixture(t)
	rec := record(f.pr)
	rec.Text = "a\x00b\xffc"
	rec.InScope = []string{"x\x00y"}
	rec.Sources = []reviewer.IntentSource{{Kind: "doc", Ref: "d\x00.md", Status: "skipped", Detail: ptr("unsafe path \x00")}}

	got, err := f.store.Upsert(t.Context(), rec)

	if err != nil {
		t.Fatalf("Upsert() error = %v, want the text made storable", err)
	}
	if got.Text != "a�b�c" || got.InScope[0] != "x�y" || got.Sources[0].Ref != "d�.md" || *got.Sources[0].Detail != "unsafe path �" {
		t.Errorf("stored record = %+v, want NUL and invalid bytes replaced by U+FFFD", got)
	}
}

func TestStore_RowsFollowThePullRequestOnDelete(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Upsert(t.Context(), record(f.pr)); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `DELETE FROM pull_requests WHERE id = ?`, f.pr)

	if _, ok, err := f.store.Get(t.Context(), f.pr); ok || err != nil {
		t.Errorf("Get() after the pull request was deleted = %v, %v, want no record", ok, err)
	}
}
