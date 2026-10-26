package postgres_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/customproviders/adapters/postgres"
	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm/custom"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	refs  *postgres.References
	tx    *pg.Tx
	ws    string
	other string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB), refs: postgres.NewReferences(d.DB), tx: pg.NewTx(d.DB)}
	f.ws = f.scan(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id::text`)
	f.other = f.scan(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id::text`)
	return f
}

func (f fixture) scan(t *testing.T, query string, args ...any) string {
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

func ptr[T any](v T) *T { return &v }

var ts = time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.UTC)

func provider(slug string) domain.Provider {
	return domain.Provider{
		ID: custom.ID(slug), Name: "Gateway " + slug, Protocol: custom.ProtocolOpenAI, BaseURL: "https://gw.example.com/v1",
		AuthMode: custom.AuthBearer, StructuredOutput: custom.ModeStrict, ExtraHeaderNames: []string{},
		CreatedAt: ts, UpdatedAt: ts,
	}
}

var cmpOpts = cmp.Options{cmpopts.EquateEmpty(), cmpopts.EquateApproxTime(time.Microsecond)}

func TestRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	want := provider("my-gw")
	want.Protocol = custom.ProtocolOpenAI
	want.AuthMode = custom.AuthHeader
	want.AuthHeader = "api-key"
	want.StructuredOutput = custom.ModeJSONObject
	want.ExtraHeaderNames = []string{"X-Team", "X-Env", "x-trace"}
	want.Models = []domain.Model{
		{ID: "zeta", Label: ptr("Zeta"), Pricing: &domain.Pricing{PromptPerM: 0.123457, CompletionPerM: 9999.999999}},
		{ID: "alpha"},
		{ID: "free", Pricing: &domain.Pricing{PromptPerM: 0, CompletionPerM: 0}},
		{ID: "mid", Label: ptr(""), Pricing: &domain.Pricing{PromptPerM: 2.5, CompletionPerM: 10000}},
	}
	if err := f.store.Insert(ctx, f.ws, want, domain.MaxProviders); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	got, err := f.store.Get(ctx, f.ws, want.ID)
	if err != nil {
		t.Fatalf("Get(%q) = %v", want.ID, err)
	}
	if diff := cmp.Diff(want, got, cmpOpts); diff != "" {
		t.Errorf("Get(%q) mismatch (-want +got):\n%s", want.ID, diff)
	}
	if got.Models[3].Label == nil {
		t.Errorf("Get(%q) lost the empty label of model mid", want.ID)
	}
	// Anthropic provider with bearer: no auth header.
	a := provider("claude-gw")
	a.Protocol = custom.ProtocolAnthropic
	a.StructuredOutput = custom.ModePromptOnly
	if err := f.store.Insert(ctx, f.ws, a, domain.MaxProviders); err != nil {
		t.Fatalf("Insert(anthropic) = %v", err)
	}
	gotA, err := f.store.Get(ctx, f.ws, a.ID)
	if err != nil {
		t.Fatalf("Get(%q) = %v", a.ID, err)
	}
	if diff := cmp.Diff(a, gotA, cmpOpts); diff != "" {
		t.Errorf("Get(%q) mismatch (-want +got):\n%s", a.ID, diff)
	}
}

// Every price with at most six decimals comes back as the same float64.
func TestPriceRoundTripIsExact(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	prices := []float64{0, 0.000001, 0.1, 0.2, 0.3, 1.1, 2.675, 3.14159, 0.999999, 1234.567891, 9999.999999, 10000}
	p := provider("prices")
	for i, v := range prices {
		p.Models = append(p.Models, domain.Model{ID: "m" + string(rune('a'+i)), Pricing: &domain.Pricing{PromptPerM: v, CompletionPerM: prices[len(prices)-1-i]}})
	}
	if err := f.store.Insert(ctx, f.ws, p, domain.MaxProviders); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	got, err := f.store.Get(ctx, f.ws, p.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	for i, m := range got.Models {
		if *m.Pricing != *p.Models[i].Pricing {
			t.Errorf("model %s pricing = %v, want %v (exact)", m.ID, *m.Pricing, *p.Models[i].Pricing)
		}
	}
}

func TestGetMissingAndMalformed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for _, id := range []string{"custom:ghost", "ghost", "custom:", "custom:UP", ""} {
		if _, err := f.store.Get(ctx, f.ws, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want ErrNotFound", id, err)
		}
		if _, err := f.store.GetForUpdate(ctx, f.ws, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetForUpdate(%q) error = %v, want ErrNotFound", id, err)
		}
		if ok, err := f.store.Exists(ctx, f.ws, id); ok || err != nil {
			t.Errorf("Exists(%q) = %v, %v, want false, nil", id, ok, err)
		}
		if ok, err := f.store.LockDefined(ctx, f.ws, id); ok || err != nil {
			t.Errorf("LockDefined(%q) = %v, %v, want false, nil", id, ok, err)
		}
		if err := f.store.Replace(ctx, f.ws, domain.Provider{ID: id}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Replace(%q) error = %v, want ErrNotFound", id, err)
		}
		if err := f.store.Delete(ctx, f.ws, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Delete(%q) error = %v, want ErrNotFound", id, err)
		}
	}
	if err := f.store.Insert(ctx, f.ws, domain.Provider{ID: "nope"}, 20); err == nil {
		t.Error("Insert(malformed id) = nil, want an error")
	}
}

func TestListOrdersByNameThenIDInByteOrder(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	// Byte order: uppercase before lowercase, digits before letters; ties on name go by id.
	for _, c := range []struct{ slug, name string }{
		{"b-one", "beta"}, {"a-two", "beta"}, {"z", "Zulu"}, {"c", "alpha"}, {"d", "Alpha"}, {"e", "10"},
	} {
		p := provider(c.slug)
		p.Name = c.name
		p.Models = []domain.Model{{ID: "m2"}, {ID: "m1"}}
		if err := f.store.Insert(ctx, f.ws, p, domain.MaxProviders); err != nil {
			t.Fatalf("Insert(%s) = %v", c.slug, err)
		}
	}
	got, err := f.store.List(ctx, f.ws)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
		if len(p.Models) != 2 || p.Models[0].ID != "m2" || p.Models[1].ID != "m1" {
			t.Errorf("List() models of %s = %v, want [m2 m1] in stored order", p.ID, p.Models)
		}
	}
	want := []string{"custom:e", "custom:d", "custom:z", "custom:c", "custom:a-two", "custom:b-one"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("List() ids mismatch (-want +got):\n%s", diff)
	}
	if empty, err := f.store.List(ctx, f.other); err != nil || len(empty) != 0 {
		t.Errorf("List(other workspace) = %v, %v, want empty", empty, err)
	}
}

func TestWorkspaceIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	p := provider("shared-slug")
	p.Models = []domain.Model{{ID: "m"}}
	if err := f.store.Insert(ctx, f.ws, p, 1); err != nil {
		t.Fatalf("Insert(ws) = %v", err)
	}
	// The same slug in another workspace is not a conflict, and the limit counts per workspace.
	q := provider("shared-slug")
	q.Name = "Other"
	if err := f.store.Insert(ctx, f.other, q, 1); err != nil {
		t.Fatalf("Insert(other) = %v", err)
	}
	got, err := f.store.Get(ctx, f.other, p.ID)
	if err != nil || got.Name != "Other" || len(got.Models) != 0 {
		t.Fatalf("Get(other) = %+v, %v, want the other workspace's own provider", got, err)
	}
	if ok, _ := f.store.Exists(ctx, f.other, "custom:only-ws"); ok {
		t.Error("Exists(other, only-ws) = true")
	}
	if err := f.store.Delete(ctx, f.other, p.ID); err != nil {
		t.Fatalf("Delete(other) = %v", err)
	}
	if _, err := f.store.Get(ctx, f.ws, p.ID); err != nil {
		t.Errorf("Get(ws) after deleting in other = %v, want the provider untouched", err)
	}
	if err := f.store.Replace(ctx, f.other, p); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Replace(other, deleted) = %v, want ErrNotFound", err)
	}
}

func TestInsertExistsAndLimit(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for _, slug := range []string{"a", "b"} {
		if err := f.store.Insert(ctx, f.ws, provider(slug), 2); err != nil {
			t.Fatalf("Insert(%s) = %v", slug, err)
		}
	}
	// A taken id wins over a full workspace.
	if err := f.store.Insert(ctx, f.ws, provider("a"), 2); !errors.Is(err, domain.ErrExists) {
		t.Errorf("Insert(taken id, full) = %v, want ErrExists", err)
	}
	if err := f.store.Insert(ctx, f.ws, provider("c"), 2); !errors.Is(err, domain.ErrLimit) {
		t.Errorf("Insert(new id, full) = %v, want ErrLimit", err)
	}
	if err := f.store.Insert(ctx, f.ws, provider("c"), 3); err != nil {
		t.Errorf("Insert(new id, limit 3) = %v, want nil", err)
	}
	// A refused insert leaves nothing behind, models included.
	p := provider("d")
	p.Models = []domain.Model{{ID: "m"}}
	if err := f.store.Insert(ctx, f.ws, p, 3); !errors.Is(err, domain.ErrLimit) {
		t.Fatalf("Insert(over limit) = %v, want ErrLimit", err)
	}
	var n int64
	if err := f.db.Raw(`SELECT count(*) FROM custom_provider_models`).Scan(&n).Error; err != nil || n != 0 {
		t.Errorf("custom_provider_models rows = %d, %v, want 0", n, err)
	}
}

// A failed Insert inside the caller's transaction is reported as ErrExists and the caller decides.
func TestInsertInsideTxRollsBackWithTheCaller(t *testing.T) {
	f := newFixture(t)
	errAbort := errors.New("abort")
	err := f.store.InTx(t.Context(), func(ctx context.Context) error {
		if err := f.store.Insert(ctx, f.ws, provider("tx"), 20); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("InTx() = %v, want errAbort", err)
	}
	if ok, err := f.store.Exists(t.Context(), f.ws, "custom:tx"); ok || err != nil {
		t.Errorf("Exists after rollback = %v, %v, want false", ok, err)
	}
}

func TestConcurrentCreateOfTheSameID(t *testing.T) {
	f := newFixture(t)
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			p := provider("race")
			p.Models = []domain.Model{{ID: "m"}}
			errs[i] = f.store.InTx(t.Context(), func(ctx context.Context) error {
				return f.store.Insert(ctx, f.ws, p, domain.MaxProviders)
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
		t.Errorf("concurrent Insert(same id): %d ok, %d ErrExists, want 1 and %d", ok, exists, n-1)
	}
}

// The limit holds when creates of different ids race: the lock serializes the count.
func TestConcurrentCreatesRespectTheLimit(t *testing.T) {
	f := newFixture(t)
	const n, limit = 8, 3
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = f.store.InTx(t.Context(), func(ctx context.Context) error {
				return f.store.Insert(ctx, f.ws, provider("p"+string(rune('a'+i))), limit)
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
	got, err := f.store.List(t.Context(), f.ws)
	if err != nil || len(got) != limit {
		t.Errorf("List() = %d providers, %v, want %d", len(got), err, limit)
	}
}

// Creates in different workspaces do not wait for each other's lock.
func TestInsertLockIsPerWorkspace(t *testing.T) {
	f := newFixture(t)
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.store.InTx(t.Context(), func(ctx context.Context) error {
			if err := f.store.Insert(ctx, f.ws, provider("held"), 20); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.store.Insert(ctx, f.other, provider("free"), 20); err != nil {
		t.Errorf("Insert(other workspace) while ws holds its lock = %v, want nil", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("held InTx = %v", err)
	}
}

func TestReplace(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	orig := provider("rep")
	orig.AuthMode = custom.AuthHeader
	orig.AuthHeader = "api-key"
	orig.ExtraHeaderNames = []string{"X-A"}
	orig.Models = []domain.Model{
		{ID: "a", Pricing: &domain.Pricing{PromptPerM: 1, CompletionPerM: 2}},
		{ID: "b"}, {ID: "c", Label: ptr("C")},
	}
	if err := f.store.Insert(ctx, f.ws, orig, 20); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	later := ts.Add(time.Hour)
	next := domain.Provider{
		ID: orig.ID, Name: "Renamed", Protocol: custom.ProtocolAnthropic, BaseURL: "http://localhost:8080",
		AuthMode: custom.AuthBearer, StructuredOutput: custom.ModePromptOnly, ExtraHeaderNames: nil,
		CreatedAt: later.Add(time.Hour), // ignored: created_at is kept
		UpdatedAt: later,
		Models:    []domain.Model{{ID: "c"}, {ID: "d", Label: ptr("D")}, {ID: "a", Pricing: &domain.Pricing{PromptPerM: 0.5, CompletionPerM: 0.25}}},
	}
	if err := f.store.Replace(ctx, f.ws, next); err != nil {
		t.Fatalf("Replace() = %v", err)
	}
	got, err := f.store.Get(ctx, f.ws, orig.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	want := next
	want.CreatedAt = ts
	if diff := cmp.Diff(want, got, cmpOpts); diff != "" {
		t.Errorf("Get() after Replace mismatch (-want +got):\n%s", diff)
	}
	var order []string
	for _, m := range got.Models {
		order = append(order, m.ID)
	}
	if !slices.Equal(order, []string{"c", "d", "a"}) {
		t.Errorf("model order after Replace = %v, want [c d a]", order)
	}
	// Replace with no models empties them.
	next.Models = nil
	if err := f.store.Replace(ctx, f.ws, next); err != nil {
		t.Fatalf("Replace(no models) = %v", err)
	}
	if got, _ := f.store.Get(ctx, f.ws, orig.ID); len(got.Models) != 0 {
		t.Errorf("models after Replace(no models) = %v, want none", got.Models)
	}
	if err := f.store.Replace(ctx, f.ws, provider("missing")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Replace(missing) = %v, want ErrNotFound", err)
	}
}

func TestReplaceFailureKeepsTheOldModels(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	p := provider("keep")
	p.Models = []domain.Model{{ID: "a"}, {ID: "b"}}
	if err := f.store.Insert(ctx, f.ws, p, 20); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	bad := p
	bad.Models = []domain.Model{{ID: "x"}, {ID: "x"}} // duplicate model id violates the primary key
	if err := f.store.Replace(ctx, f.ws, bad); err == nil {
		t.Fatal("Replace(duplicate model ids) = nil, want an error")
	}
	got, err := f.store.Get(ctx, f.ws, p.ID)
	if err != nil || len(got.Models) != 2 || got.Models[0].ID != "a" {
		t.Errorf("Get() after a failed Replace = %+v, %v, want the old models", got.Models, err)
	}
}

func TestDeleteRemovesModels(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	p := provider("gone")
	p.Models = []domain.Model{{ID: "a"}, {ID: "b"}}
	if err := f.store.Insert(ctx, f.ws, p, 20); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	if err := f.store.Delete(ctx, f.ws, p.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if err := f.store.Delete(ctx, f.ws, p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete(again) = %v, want ErrNotFound", err)
	}
	var n int64
	if err := f.db.Raw(`SELECT count(*) FROM custom_provider_models`).Scan(&n).Error; err != nil || n != 0 {
		t.Errorf("custom_provider_models rows after Delete = %d, %v, want 0", n, err)
	}
}

func TestExistsAndLockDefined(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.store.Insert(ctx, f.ws, provider("here"), 20); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	for name, fn := range map[string]func(context.Context, string, string) (bool, error){
		"Exists": f.store.Exists, "LockDefined": f.store.LockDefined,
	} {
		if ok, err := fn(ctx, f.ws, "custom:here"); !ok || err != nil {
			t.Errorf("%s(defined) = %v, %v, want true", name, ok, err)
		}
		if ok, err := fn(ctx, f.ws, "custom:absent"); ok || err != nil {
			t.Errorf("%s(absent) = %v, %v, want false", name, ok, err)
		}
		if ok, err := fn(ctx, f.other, "custom:here"); ok || err != nil {
			t.Errorf("%s(other workspace) = %v, %v, want false", name, ok, err)
		}
	}
}

// lockTimeoutState is the SQLState of a statement that gave up waiting for a lock.
const lockTimeoutState = "55P03"

func isLockTimeout(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == lockTimeoutState
}

// blocked runs try in a second transaction (another connection) with a short lock_timeout and
// reports whether it had to give up waiting for a lock. Any other error fails the test.
func (f fixture) blocked(t *testing.T, try func(context.Context) error) bool {
	t.Helper()
	err := f.tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := pg.Conn(ctx, f.db).Exec(`SET LOCAL lock_timeout = '300ms'`).Error; err != nil {
			t.Fatalf("set lock_timeout: %v", err)
		}
		return try(ctx)
	})
	if err != nil && !isLockTimeout(err) {
		t.Fatalf("second transaction failed with %v, want success or a lock timeout", err)
	}
	return err != nil
}

// The two row locks of the delete/agent-write protocol (A8), with two connections: FOR SHARE
// holders coexist and exclude FOR UPDATE; FOR UPDATE excludes both.
func TestLockSemantics(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Insert(t.Context(), f.ws, provider("locked"), 20); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	const id = "custom:locked"
	lockDefined := func(ctx context.Context) error {
		ok, err := f.store.LockDefined(ctx, f.ws, id)
		if err == nil && !ok {
			t.Error("LockDefined() = false for a defined provider")
		}
		return err
	}
	getForUpdate := func(ctx context.Context) error { _, err := f.store.GetForUpdate(ctx, f.ws, id); return err }
	get := func(ctx context.Context) error { _, err := f.store.Get(ctx, f.ws, id); return err }
	exists := func(ctx context.Context) error { _, err := f.store.Exists(ctx, f.ws, id); return err }

	holdWith := func(t *testing.T, hold func(context.Context) error, probes map[string]bool) {
		t.Helper()
		held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- f.tx.InTx(t.Context(), func(ctx context.Context) error {
				if err := hold(ctx); err != nil {
					return err
				}
				close(held)
				<-release
				return nil
			})
		}()
		select {
		case <-held:
		case err := <-done:
			t.Fatalf("holder ended early: %v", err)
		}
		probeFns := map[string]func(context.Context) error{
			"LockDefined": lockDefined, "GetForUpdate": getForUpdate, "Get": get, "Exists": exists,
		}
		for name, wantBlocked := range probes {
			if got := f.blocked(t, probeFns[name]); got != wantBlocked {
				t.Errorf("%s blocked = %v while the lock is held, want %v", name, got, wantBlocked)
			}
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("holder: %v", err)
		}
		// The lock is gone with the transaction.
		for name, fn := range probeFns {
			if f.blocked(t, fn) {
				t.Errorf("%s still blocked after the holder committed", name)
			}
		}
	}

	t.Run("FOR SHARE holder", func(t *testing.T) {
		holdWith(t, lockDefined, map[string]bool{"LockDefined": false, "GetForUpdate": true, "Get": false, "Exists": false})
	})
	t.Run("FOR UPDATE holder", func(t *testing.T) {
		holdWith(t, getForUpdate, map[string]bool{"LockDefined": true, "GetForUpdate": true, "Get": false, "Exists": false})
	})
}

// A delete waiting for the share lock of an agent write proceeds once that write commits, and then
// the agent write's provider is gone (the order the protocol relies on).
func TestDeleteWaitsForAnAgentWrite(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Insert(t.Context(), f.ws, provider("busy"), 20); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	held, release, writer := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		writer <- f.tx.InTx(t.Context(), func(ctx context.Context) error {
			if ok, err := f.store.LockDefined(ctx, f.ws, "custom:busy"); err != nil || !ok {
				return errors.Join(err, errors.New("provider not defined"))
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	deleted := make(chan error, 1)
	go func() {
		deleted <- f.store.InTx(t.Context(), func(ctx context.Context) error {
			if _, err := f.store.GetForUpdate(ctx, f.ws, "custom:busy"); err != nil {
				return err
			}
			return f.store.Delete(ctx, f.ws, "custom:busy")
		})
	}()
	select {
	case err := <-deleted:
		t.Fatalf("delete finished while an agent write holds the share lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-writer; err != nil {
		t.Fatalf("writer: %v", err)
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Errorf("delete after the writer committed = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delete still waiting after the writer committed")
	}
}
