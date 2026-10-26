package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/gitlabconn/adapters/postgres"
	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

type fixture struct {
	db    *gorm.DB
	store *postgres.Store
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	return fixture{db: d.DB, store: postgres.NewStore(d.DB)}
}

func (f fixture) scan(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) workspace(t *testing.T, name string) string {
	t.Helper()
	return f.scan(t, `INSERT INTO workspaces (name) VALUES (?) RETURNING id::text`, name)
}

func (f fixture) gitlabRepo(t *testing.T, ws, host string, projectID int64, fullName string) {
	t.Helper()
	err := f.db.Exec(`INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?::uuid, 'acme', ?, ?, 'gitlab', ?, ?, ?)`,
		ws, fullName, fullName, host, projectID, "https://"+host+"/"+fullName).Error
	if err != nil {
		t.Fatalf("fixture gitlab repo %s/%s: %v", host, fullName, err)
	}
}

func (f fixture) githubRepo(t *testing.T, ws, fullName string) {
	t.Helper()
	err := f.db.Exec(`INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?::uuid, 'acme', ?, ?)`,
		ws, fullName, fullName).Error
	if err != nil {
		t.Fatalf("fixture github repo %s: %v", fullName, err)
	}
}

var ts = time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.UTC)

func conn(host string) domain.Connection {
	return domain.Connection{Name: "GitLab " + host, BaseURL: "https://" + host, Host: host, CreatedAt: ts, UpdatedAt: ts}
}

var cmpOpts = cmp.Options{cmpopts.EquateApproxTime(time.Microsecond), cmpopts.IgnoreFields(domain.Connection{}, "ID")}

func mustInsert(t *testing.T, f fixture, c domain.Connection) domain.Connection {
	t.Helper()
	if err := f.store.Insert(t.Context(), c, domain.Limit); err != nil {
		t.Fatalf("Insert(%s) = %v, want nil", c.Host, err)
	}
	got, err := f.store.GetByHost(t.Context(), c.Host)
	if err != nil {
		t.Fatalf("GetByHost(%s) after Insert = %v, want nil", c.Host, err)
	}
	return got
}

func TestRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	want := conn("git.example.com:8443")
	want.BaseURL = "https://git.example.com:8443/gitlab"

	got := mustInsert(t, f, want)
	if got.ID == "" {
		t.Fatalf("Insert() id is empty, want a generated uuid")
	}
	if diff := cmp.Diff(want, got, cmpOpts); diff != "" {
		t.Errorf("Insert() then GetByHost() mismatch (-want +got):\n%s", diff)
	}
	byID, err := f.store.Get(ctx, got.ID)
	if err != nil {
		t.Fatalf("Get(%s) = %v, want nil", got.ID, err)
	}
	if diff := cmp.Diff(got, byID, cmpOpts); diff != "" {
		t.Errorf("Get() mismatch (-want +got):\n%s", diff)
	}
	byHost, err := f.store.GetByHost(ctx, want.Host)
	if err != nil || byHost.ID != got.ID {
		t.Errorf("GetByHost(%q) = %+v, %v, want id %s", want.Host, byHost, err, got.ID)
	}
	list, err := f.store.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != got.ID {
		t.Errorf("List() = %+v, %v, want the one connection", list, err)
	}
}

func TestInsertKeepsAGivenIDAndDefaultsTimes(t *testing.T) {
	f := newFixture(t)
	const id = "0b8c9f5e-3a4d-4d6e-9a0e-4f1d2c3b4a5f"
	c := domain.Connection{ID: id, Name: "n", BaseURL: "https://a.example.com", Host: "a.example.com"}
	got := mustInsert(t, f, c)
	if got.ID != id || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("Insert(id %s, zero times) = %+v, want that id and times set by the database", id, got)
	}
}

func TestGetMissingAndMalformed(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"", "nope", "0b8c9f5e-3a4d-4d6e-9a0e-4f1d2c3b4a5f", "0b8c9f5e-3a4d-4d6e-9a0e-4f1d2c3b4a5f' OR 1=1"} {
		if _, err := f.store.Get(t.Context(), id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", id, err)
		}
		if _, err := f.store.GetForUpdate(t.Context(), id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetForUpdate(%q) = %v, want ErrNotFound", id, err)
		}
		if err := f.store.Replace(t.Context(), domain.Connection{ID: id, Name: "n", BaseURL: "https://a.example.com"}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Replace(%q) = %v, want ErrNotFound", id, err)
		}
		if err := f.store.Delete(t.Context(), id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Delete(%q) = %v, want ErrNotFound", id, err)
		}
	}
	if _, err := f.store.GetByHost(t.Context(), "none.example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetByHost(unknown) = %v, want ErrNotFound", err)
	}
}

func TestListSortsByNameThenHostInByteOrder(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ name, host string }{
		{"b", "z.example.com"}, {"B", "y.example.com"}, {"b", "a.example.com"}, {"a", "m.example.com"},
	} {
		x := conn(c.host)
		x.Name = c.name
		mustInsert(t, f, x)
	}
	got, err := f.store.List(t.Context())
	if err != nil {
		t.Fatalf("List() = %v, want nil", err)
	}
	var hosts []string
	for _, c := range got {
		hosts = append(hosts, c.Host)
	}
	// Byte order: "B" < "a" < "b".
	want := []string{"y.example.com", "m.example.com", "a.example.com", "z.example.com"}
	if diff := cmp.Diff(want, hosts); diff != "" {
		t.Errorf("List() hosts mismatch (-want +got):\n%s", diff)
	}
}

func TestListEmptyIsNotNil(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.List(t.Context())
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("List(empty) = %#v, %v, want an empty non-nil slice", got, err)
	}
}

func TestInsertDuplicateHost(t *testing.T) {
	f := newFixture(t)
	mustInsert(t, f, conn("dup.example.com"))
	other := conn("dup.example.com")
	other.Name = "Another"
	other.BaseURL = "https://dup.example.com/prefix"
	if err := f.store.Insert(t.Context(), other, domain.Limit); !errors.Is(err, domain.ErrExists) {
		t.Errorf("Insert(same host) = %v, want ErrExists", err)
	}
	list, _ := f.store.List(t.Context())
	if len(list) != 1 {
		t.Errorf("List() after a refused duplicate = %d connections, want 1", len(list))
	}
}

// A unique violation raised by the insert itself (a writer that skipped the lock inserted the host
// after the check) is ErrExists, and the savepoint leaves the surrounding transaction usable.
func TestInsertUniqueViolationIsExists(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	// A rival transaction inserts the host without taking the lock and stays open.
	rival := f.db.WithContext(ctx).Begin()
	if rival.Error != nil {
		t.Fatalf("begin rival: %v", rival.Error)
	}
	defer rival.Rollback()
	if err := rival.Exec(`INSERT INTO gitlab_connections (name, base_url, host) VALUES ('rival', 'https://dup.example.com', 'dup.example.com')`).Error; err != nil {
		t.Fatalf("rival insert: %v", err)
	}
	type result struct {
		dup, next error
		listed    int
	}
	done := make(chan result, 1)
	go func() {
		var r result
		err := f.store.InTx(ctx, func(ctx context.Context) error {
			// Passes the host check (the rival row is invisible), then waits for the rival's
			// commit on the unique index and fails with 23505.
			r.dup = f.store.Insert(ctx, conn("dup.example.com"), domain.Limit)
			r.next = f.store.Insert(ctx, conn("next.example.com"), domain.Limit)
			return r.next
		})
		if err != nil {
			r.next = err
		}
		done <- r
	}()
	// Wait until the insert is blocked on the rival's row.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var waiting int64
		if err := f.db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting).Error; err != nil {
			t.Fatalf("poll waiters: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the insert never waited on the rival row")
		}
		runtime.Gosched()
	}
	if err := rival.Commit().Error; err != nil {
		t.Fatalf("commit rival: %v", err)
	}
	r := <-done
	if !errors.Is(r.dup, domain.ErrExists) {
		t.Errorf("Insert(host inserted by a rival) = %v, want ErrExists", r.dup)
	}
	if r.next != nil {
		t.Errorf("Insert(next) in the same transaction = %v, want nil (savepoint kept it usable)", r.next)
	}
	if list, _ := f.store.List(ctx); len(list) != 2 {
		t.Errorf("List() = %d connections, want 2 (rival and next)", len(list))
	}
}

func TestInsertLimit(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		mustInsert(t, f, conn(fmt.Sprintf("h%d.example.com", i)))
	}
	if err := f.store.Insert(t.Context(), conn("h3.example.com"), 3); !errors.Is(err, domain.ErrLimit) {
		t.Errorf("Insert(4th, limit 3) = %v, want ErrLimit", err)
	}
	// A taken host reports ErrExists even when the table is full.
	if err := f.store.Insert(t.Context(), conn("h0.example.com"), 3); !errors.Is(err, domain.ErrExists) {
		t.Errorf("Insert(taken host, full) = %v, want ErrExists", err)
	}
}

func TestInsertTwentyOneRefused(t *testing.T) {
	f := newFixture(t)
	for i := range domain.Limit {
		mustInsert(t, f, conn(fmt.Sprintf("h%02d.example.com", i)))
	}
	if err := f.store.Insert(t.Context(), conn("one-more.example.com"), domain.Limit); !errors.Is(err, domain.ErrLimit) {
		t.Errorf("Insert(21st) = %v, want ErrLimit", err)
	}
}

func TestInsertInsideTxRollsBackWithTheCaller(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("boom")
	err := f.store.InTx(t.Context(), func(ctx context.Context) error {
		if err := f.store.Insert(ctx, conn("rolled.example.com"), domain.Limit); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx() = %v, want boom", err)
	}
	if list, _ := f.store.List(t.Context()); len(list) != 0 {
		t.Errorf("List() after rollback = %d connections, want 0", len(list))
	}
}

// Creates for one host race: exactly one wins, the others see ErrExists.
func TestConcurrentCreatesOfTheSameHost(t *testing.T) {
	f := newFixture(t)
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = f.store.InTx(t.Context(), func(ctx context.Context) error {
				return f.store.Insert(ctx, conn("race.example.com"), domain.Limit)
			})
		})
	}
	close(start)
	wg.Wait()
	ok, exists := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrExists):
			exists++
		default:
			t.Errorf("concurrent Insert() = %v, want nil or ErrExists", err)
		}
	}
	if ok != 1 || exists != n-1 {
		t.Errorf("concurrent Insert(same host): %d ok, %d ErrExists, want 1 and %d", ok, exists, n-1)
	}
}

// The limit holds when creates of different hosts race: the global lock serializes the count.
func TestConcurrentCreatesRespectTheLimit(t *testing.T) {
	f := newFixture(t)
	const n, limit = 10, 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = f.store.InTx(t.Context(), func(ctx context.Context) error {
				return f.store.Insert(ctx, conn(fmt.Sprintf("r%d.example.com", i)), limit)
			})
		})
	}
	close(start)
	wg.Wait()
	ok, full := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrLimit):
			full++
		default:
			t.Errorf("concurrent Insert() = %v, want nil or ErrLimit", err)
		}
	}
	if ok != limit || full != n-limit {
		t.Errorf("concurrent Insert(limit %d): %d ok, %d ErrLimit, want %d and %d", limit, ok, full, limit, n-limit)
	}
	if list, err := f.store.List(t.Context()); err != nil || len(list) != limit {
		t.Errorf("List() = %d connections, %v, want %d", len(list), err, limit)
	}
}

func TestReplaceKeepsHostAndCreatedAt(t *testing.T) {
	f := newFixture(t)
	created := mustInsert(t, f, conn("up.example.com"))
	upd := created
	upd.Name = "Renamed"
	upd.BaseURL = "https://up.example.com/new-prefix"
	upd.Host = "ignored.example.com"
	upd.CreatedAt = ts.Add(time.Hour)
	upd.UpdatedAt = ts.Add(2 * time.Hour)

	if err := f.store.Replace(t.Context(), upd); err != nil {
		t.Fatalf("Replace() = %v, want nil", err)
	}
	want := domain.Connection{
		ID: created.ID, Name: "Renamed", BaseURL: "https://up.example.com/new-prefix", Host: "up.example.com",
		CreatedAt: ts, UpdatedAt: ts.Add(2 * time.Hour),
	}
	stored, err := f.store.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get() after Replace = %v, want nil", err)
	}
	if diff := cmp.Diff(want, stored, cmpopts.EquateApproxTime(time.Microsecond)); diff != "" {
		t.Errorf("Get() after Replace mismatch (-want +got):\n%s", diff)
	}
}

func TestReplaceZeroUpdatedAtIsNow(t *testing.T) {
	f := newFixture(t)
	created := mustInsert(t, f, conn("up.example.com"))
	created.UpdatedAt = time.Time{}
	if err := f.store.Replace(t.Context(), created); err != nil {
		t.Fatalf("Replace() = %v, want nil", err)
	}
	got, _ := f.store.Get(t.Context(), created.ID)
	if !got.UpdatedAt.After(ts) {
		t.Errorf("Replace(zero updated_at).UpdatedAt = %v, want a time after %v", got.UpdatedAt, ts)
	}
}

func TestReplaceMissing(t *testing.T) {
	f := newFixture(t)
	c := conn("x.example.com")
	c.ID = "0b8c9f5e-3a4d-4d6e-9a0e-4f1d2c3b4a5f"
	if err := f.store.Replace(t.Context(), c); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Replace(missing) = %v, want ErrNotFound", err)
	}
}

func TestDeleteLeavesRepositories(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	ws1, ws2 := f.workspace(t, "one"), f.workspace(t, "two")
	c := mustInsert(t, f, conn("gl.example.com"))
	keep := mustInsert(t, f, conn("other.example.com"))
	f.gitlabRepo(t, ws1, "gl.example.com", 1, "grp/a")
	f.gitlabRepo(t, ws1, "gl.example.com", 2, "grp/b")
	f.gitlabRepo(t, ws2, "gl.example.com", 1, "grp/a")
	f.gitlabRepo(t, ws1, "other.example.com", 3, "grp/c")
	f.githubRepo(t, ws1, "acme/web")

	// The use case counts and deletes in one transaction (the count is what the response reports).
	var disconnected map[string]int
	err := f.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		if disconnected, err = f.store.CountBound(ctx, []string{c.Host}); err != nil {
			return err
		}
		return f.store.Delete(ctx, c.ID)
	})
	if err != nil {
		t.Fatalf("CountBound + Delete in one transaction = %v, want nil", err)
	}
	if disconnected[c.Host] != 3 {
		t.Errorf("CountBound(%q) before Delete = %d, want 3 (the repositories of all workspaces)", c.Host, disconnected[c.Host])
	}
	if _, err := f.store.Get(ctx, c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(deleted) = %v, want ErrNotFound", err)
	}
	if _, err := f.store.Get(ctx, keep.ID); err != nil {
		t.Errorf("Get(other connection) = %v, want it untouched", err)
	}
	var repos int64
	if err := f.db.Table("repos").Count(&repos).Error; err != nil || repos != 5 {
		t.Errorf("repos after Delete = %d, %v, want all 5 kept", repos, err)
	}
	counts, err := f.store.CountBound(ctx, []string{c.Host, keep.Host})
	if err != nil {
		t.Fatalf("CountBound() = %v, want nil", err)
	}
	// The repositories stay and keep counting for their host key (a re-created connection binds them again).
	if diff := cmp.Diff(map[string]int{"gl.example.com": 3, "other.example.com": 1}, counts); diff != "" {
		t.Errorf("CountBound() after Delete mismatch (-want +got):\n%s", diff)
	}
	if err := f.store.Delete(ctx, c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete(again) = %v, want ErrNotFound", err)
	}
}

func TestCountBound(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	ws1, ws2 := f.workspace(t, "one"), f.workspace(t, "two")
	f.gitlabRepo(t, ws1, "gl.example.com", 1, "grp/a")
	f.gitlabRepo(t, ws2, "gl.example.com", 1, "grp/a")
	f.gitlabRepo(t, ws2, "gl.example.com", 2, "grp/b")
	f.gitlabRepo(t, ws1, "other.example.com", 9, "grp/z")
	f.githubRepo(t, ws1, "acme/web")
	f.githubRepo(t, ws2, "acme/api")

	tests := []struct {
		name  string
		hosts []string
		want  map[string]int
	}{
		{"counts all workspaces", []string{"gl.example.com"}, map[string]int{"gl.example.com": 3}},
		{"only the asked hosts", []string{"gl.example.com", "other.example.com"}, map[string]int{"gl.example.com": 3, "other.example.com": 1}},
		{"host without repositories is absent", []string{"empty.example.com"}, map[string]int{}},
		{"github is never counted", []string{"github.com"}, map[string]int{}},
		{"no hosts", nil, map[string]int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.store.CountBound(ctx, tc.hosts)
			if err != nil {
				t.Fatalf("CountBound(%v) = %v, want nil", tc.hosts, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("CountBound(%v) mismatch (-want +got):\n%s", tc.hosts, diff)
			}
		})
	}
}

// Counts are bound by the host key alone: a host that differs in case or port is another host.
func TestCountBoundMatchesTheHostKeyExactly(t *testing.T) {
	f := newFixture(t)
	ws := f.workspace(t, "one")
	f.gitlabRepo(t, ws, "gl.example.com:8443", 1, "grp/a")
	got, err := f.store.CountBound(t.Context(), []string{"gl.example.com", "gl.example.com:8443"})
	if err != nil {
		t.Fatalf("CountBound() = %v, want nil", err)
	}
	if diff := cmp.Diff(map[string]int{"gl.example.com:8443": 1}, got); diff != "" {
		t.Errorf("CountBound() mismatch (-want +got):\n%s", diff)
	}
}

// The database refuses what the application should have caught (defence in depth for FR1/FR2).
func TestSchemaRefusesBadRows(t *testing.T) {
	f := newFixture(t)
	for name, c := range map[string]domain.Connection{
		"http base url":  {Name: "n", BaseURL: "http://a.example.com", Host: "a.example.com"},
		"trailing slash": {Name: "n", BaseURL: "https://a.example.com/", Host: "a.example.com"},
		"empty name":     {Name: " ", BaseURL: "https://a.example.com", Host: "a.example.com"},
		"upper host":     {Name: "n", BaseURL: "https://a.example.com", Host: "A.example.com"},
	} {
		if err := f.store.Insert(t.Context(), c, domain.Limit); err == nil || errors.Is(err, domain.ErrExists) || errors.Is(err, domain.ErrLimit) {
			t.Errorf("Insert(%s) = %v, want a constraint error", name, err)
		}
	}
}
