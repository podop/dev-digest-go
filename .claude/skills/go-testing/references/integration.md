# Integration and component tests

## Contents
1. Principles
2. Postgres with testcontainers-go
3. Repository contract suite
4. Component test of the whole service
5. Coverage of a running binary

## 1. Principles

- **Real dependency, isolated data.** Each test creates its own rows (unique IDs, e.g.
  `uuid` or `t.Name()`-derived) instead of truncating shared tables; tests can then run
  with `t.Parallel()`.
- **Fast.** One container per test package (started in `TestMain` or behind `sync.Once`),
  not per test. Use migrations from the repository, the same ones production runs.
- **Deterministic.** No `time.Sleep`, no retry loops, no fixed host ports: the container's
  wait strategy decides readiness, and the mapped port comes from the container.
- **Gated.** `if testing.Short() { t.Skip(...) }`; the fast loop is `go test -short ./...`.
- **Cover the hard parts.** Transactions and rollback, unique/foreign-key violations
  translated to domain errors, concurrent updates (`SELECT … FOR UPDATE`), not-found.
- **Sabotage once.** Break the query and check the test fails.

## 2. Postgres with testcontainers-go

Helper in a test-support package (`internal/platform/postgres/pgtest`):

```go
package pgtest

var (
	once sync.Once
	dsn  string
	err  error
)

// DSN starts one Postgres container per test binary and returns its connection string.
func DSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	once.Do(func() {
		ctx := context.Background()
		var ctr *postgres.PostgresContainer
		ctr, err = postgres.Run(ctx, "postgres:17-alpine",
			postgres.WithDatabase("app_test"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			return
		}
		dsn, err = ctr.ConnectionString(ctx, "sslmode=disable")
		// The container is removed by the testcontainers reaper when the test binary exits.
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	return dsn
}
```

Per-test setup opens a pool on that DSN, runs migrations once (`sync.Once` again, or
`TestMain`), and registers `t.Cleanup(pool.Close)`. When a single test owns a container,
use `testcontainers.CleanupContainer(t, ctr)` right after `Run`.

Alternatives when the repository already has them: a `docker compose` service started by
the Makefile, or a DSN from an env var (`TEST_DATABASE_URL`) with `t.Skip` when unset.

## 3. Repository contract suite

One suite, run against every implementation of the port, keeps fakes honest:

```go
func runOrderStoreContract(t *testing.T, newStore func(t *testing.T) app.OrderStore) {
	t.Run("update not found", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		err := s.UpdateByID(t.Context(), domain.NewOrderID(), func(*domain.Order) error { return nil })
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("UpdateByID(unknown) error = %v, want ErrNotFound", err)
		}
	})
	t.Run("failed update is rolled back", func(t *testing.T) { /* … */ })
}

func TestPostgresOrderStore(t *testing.T) {
	runOrderStoreContract(t, func(t *testing.T) app.OrderStore { return pgorders.NewOrderRepo(openDB(t)) })
}

func TestFakeOrderStore(t *testing.T) {
	runOrderStoreContract(t, func(t *testing.T) app.OrderStore { return orderstest.NewFakeStore() })
}
```

This needs an exported port (or a test-only interface in the `_test` package with the same
method set).

## 4. Component test of the whole service

Start the real service in-process through `run(...)` (see go-architecture
references/patterns.md), with the containerized DB and fakes for external services:

```go
func TestService_PlaceAndCancel(t *testing.T) {
	addr := freeAddr(t) // net.Listen(":0"), read the port, close
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if err := run(ctx, []string{"shop", "-addr", addr}, envFor(t), io.Discard); err != nil {
			t.Error(err)
		}
	}()
	waitReady(t, "http://"+addr+"/healthz") // polls the health endpoint with a deadline
	// … exercise the HTTP API with http.Client and assert responses
}
```

Keep these to happy paths and a few key errors; logic is covered lower in the pyramid.

## 5. Coverage of a running binary

```bash
go build -cover -o bin/shop ./cmd/shop
GOCOVERDIR=covdata bin/shop &      # run the black-box / e2e suite against it, then stop it
go tool covdata percent -i=covdata
go tool covdata textfmt -i=covdata -o cover.txt
```
