# backend — Go API

Go 1.27 · Echo v5 · GORM (gorm.io/driver/postgres on pgx) · Postgres 16 + pgvector.
Module `dev-digest-go/backend`. Replaces the Node server of dev-digest; wire-compatible with the UI.

## Commands (from `backend/`)
From the repo root the `Makefile` wraps these with the right toolchain: `make backend-check`
(fmt + build + vet + full tests), `make backend-test-short`, `make backend-cover`, `make migrate-status`.
Prefix every Go command with `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH"` (see Gotchas).
- Build · vet: `go build ./... && go vet ./...`
- Tests (gate): `go test -race -shuffle=on -count=1 ./...` — integration tests start Postgres
  with testcontainers (Docker required)
- Fast loop (no Docker): `go test -short ./...`
- Run in the stack: `docker compose up -d --build backend` (from the repo root)
- DB (Phase 1, in progress): migrations + seed run on startup; `server migrate up|down|status`,
  `server seed`; `DB_AUTO_MIGRATE=false` / `DB_AUTO_SEED=false` switch the startup run off

## Read when
- Porting or changing a route → the module's row in `../docs/migration/research/01-http-api.md`
  (status codes, error codes, casing) and the contract in `../frontend/src/vendor/shared/contracts/`
- Touching the schema, a store, the seed → `../docs/migration/research/02-data-layer.md`
  (final schema, GORM mapping, Node access patterns)
- Touching git, GitHub, secrets, jobs, shutdown, LLM providers → `../docs/migration/research/03-engine-integrations.md`
- Implementing a phase → its spec in `../specs/` and plan + context pack in `../docs/plans/`
- Exact Node behavior in doubt → the read-only Node source `../../dev-digest/server/src/...`
- Adding/moving a package, use case, repository, handler or wiring → skill `go-architecture`;
  writing tests → skill `go-testing`
- Start of every task here → read `INSIGHTS.md` first; at the end → `engineering-insights` wrap-up

## Conventions (non-default)
- Layout: `cmd/server/` is the composition root (config, wiring, migrate → seed → jobs → listen,
  shutdown). Features live in `internal/<context>/{domain,app,adapters/<tech>,transport/http}`;
  shared plumbing in `internal/platform/<what>` (httpx, postgres, apperr, secrets, gitcli, github,
  retry, logging). Dependencies point inward only.
- Echo only in `internal/platform/httpx`, `transport/http` packages and `cmd/`. GORM only in
  `adapters/postgres` packages, `internal/platform/postgres` and `cmd/` (the composition root may
  hold and pass the `*gorm.DB` handle, never query with it). Domain and app import neither.
- Stores use GORM (`*gorm.DB`, `Transaction`, `clause.OnConflict`, `clause.Locking`); GORM
  `Raw`/`Exec` only for what the builder can't express (advisory locks, CTEs, DDL).
  Row structs are declared per adapter — no shared `models` package.
- Errors: use cases return `internal/platform/apperr` kinds; only `httpx` maps kinds to status and
  renders the envelope. Never return an HTTP status from app/domain code.
- Schema changes: a new version in `migrations/` with an `.up.sql` **and** a `.down.sql`;
  never GORM AutoMigrate, never edit a committed migration.
- JSON field names follow the contracts exactly (snake_case, plus the documented camelCase
  exceptions); timestamps are ISO-8601 UTC with milliseconds (`…000Z`).
- Secrets only via `internal/platform/secrets` (`$DEVDIGEST_DATA_DIR/secrets.json`, env fallback);
  never logged, never in errors. Git only via `internal/platform/gitcli`; the token goes through
  `GIT_CONFIG_*` env, never argv or `.git/config`.
- Request bodies only through `httpx.ReadJSONBody` (JSON parsed only for `application/json`).
  State-changing requests with a foreign `Origin` → 403; `Host` must be in `ALLOWED_HOSTS`
  (default `localhost,127.0.0.1,[::1],backend`). New routes get both for free via `httpx.New`.
- Integration tests use `internal/platform/postgres/pgtest` (one container per test binary, fresh
  DB per test) and skip under `-short`.

## Gotchas
- `go: no such tool "compile"` → an IDE-inherited `GOROOT` points at the Windows Go SDK; and
  `/usr/bin/go` is 1.25. Hence the `env -u GOROOT PATH=…` prefix.
- Echo v5 is not v4: handlers take `*echo.Context`; router 404/405 errors are unexported types
  (match `echo.HTTPStatusCoder`); CORS middleware panics without origins (`INSIGHTS.md`).
- The runtime image is Alpine with `git` (not distroless): the backend shells out to git.
- The schema already contains every table of later phases — empty ≠ unused.

## Do not touch
- Committed migrations in `migrations/` — add a new version instead.
- `../frontend/src/vendor/shared` — the wire contract; adapt the Go side to it.
