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
  shutdown). Startup reaps (conventions scans, review runs, eval runs) are non-fatal. Shutdown
  order: the review executor cancels live runs (≤ 5 s) and completes the run buses so SSE streams
  end, in parallel with the eval executor (aborts its runs, ≤ 5 s; `ErrNotDrained` = warning, the
  next start reaps them), the intent, brief and onboarding services cancelling their in-flight
  generations (≤ 5 s; Echo never cancels the context of a request in flight, so
  `platform/inflight` is stopped before Echo) and the job runner (10 s); only then does Echo (own
  start context) drain HTTP (20 s). The SSE route `GET /runs/:id/events` is exempt from rate
  limiting; `POST /pulls/:id/review` and `POST /pulls/:id/intent/refresh` have a 10/min bucket
  each; `POST /agents/:id/eval-runs` and `POST /skills/:id/eval-runs` share ONE bucket
  (`eval-run-start`, 10/min per workspace, `httpx.Bucket.Name` + `Key`), `POST /eval-runs/run-all`
  has its own per-IP bucket (`evalshttp.RateLimitBuckets()`). Phase 5 contexts: `intent`,
  `smartdiff`, `blast`, `brief`, `onboarding`, `performance` (brief and the review hook reach
  intent, blast, documents and roles through peers adapters). `REVIEW_INTENT_ENABLED` (default on,
  off only on `false`) decides whether `cmd` wires `executor.Deps.Intent`; off leaves it nil, which
  is Phase 4 behaviour exactly. Test seams in `cmd/server`: `options.now` (clock) and
  `intentBudget` / `briefBudget` / `onboardingBudget` / `evalCaseBudget`, `runner` (a fake
  `ciapp.RunnerBundle`) and `artifactClient` (TLS stub of the artifact storage leg). Phase 6
  contexts: `evals` (cases, runs, executor) and `ci` (export, ingest; `ci/runner` is the CI runner
  program, `cmd/devdigest-runner`). The CI runner is compiled by the image build (cgo off,
  reproducible) into `internal/ci/adapters/runnerbin/` and embedded in the server with
  `-tags devdigest_runner` (`runnerbin.Default()`); an untagged build (tests, `go build ./...`)
  has no runner and the export answers `500 runner_bundle_missing`. `DEVDIGEST_BUILD_ID` (12 hex
  of a clean HEAD, else `dev`; set by the `Makefile`, a compose build arg) stamps the runner.
  Features live in
  `internal/<context>/{domain,app,adapters/<tech>,transport/http}`;
  shared plumbing in `internal/platform/<what>` (httpx, postgres, apperr, secrets, gitcli, github,
  retry, logging). Dependencies point inward only.
- `reviewer` (the review engine) is a shared kernel: `reviews/domain` may import `reviewer/domain`
  and `reviews/app` may import `reviewer/app` types. `reviewer/domain` is also the kernel of the
  Phase 5 contexts (`intent`, `brief`, `onboarding`): the JS-parity text helpers, `Intent` /
  `RenderIntent` and the untrusted-text wrapping live there. Otherwise contexts reach each other only
  through consumer-side ports wired in `cmd/` (peers adapters may import another context's
  app/domain types); adapters never import other adapters (`cmd/` binds them, e.g. `newLLMCaller`,
  `newEvalLLMCaller`). Phase 6 adds two more engine consumers: `evals` (domain run input,
  `app/executor`, the providers peer) and `ci/runner` (domain, app) may import `reviewer/*` (the one
  engine `reviewer/app.Run` runs reviews, evals and the CI runner); `evals` and `ci` reach agents, skills, repos and GitHub only through
  consumer-side ports (`ci/adapters/peers` adapts the agents, skills and repos use cases).
- Echo only in `internal/platform/httpx`, `transport/http` packages and `cmd/` (one test-only
  exception: `platform/inflight/shutdown_test.go` drives a real Echo listener to prove that its
  graceful shutdown never cancels request contexts). GORM only in
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
- DB pool: `postgres.Open` caps it (`DB_MAX_OPEN_CONNS`, 1-90, default 20 of Postgres' 100; idle ≤ 10,
  idle time 5 min), so a burst waits for a connection instead of failing with 53300 "too many
  clients". Never hold a connection (an `InTx` body) while asking the same pool for a second one:
  every store method called inside `InTx` takes its handle from `postgres.Conn(ctx, db)`, never
  from its own `db`. With a saturated pool such a nested call deadlocks (all connections held by
  transactions that wait for another). Long holders: repo-intel indexing (a job, ≤ 3 at once).
- Integration tests use `internal/platform/postgres/pgtest` (one container per test binary, fresh
  DB per test) and skip under `-short`.

## Gotchas
- `go: no such tool "compile"` → an IDE-inherited `GOROOT` points at the Windows Go SDK; and
  `/usr/bin/go` is 1.25. Hence the `env -u GOROOT PATH=…` prefix.
- Echo v5 is not v4: handlers take `*echo.Context`; router 404/405 errors are unexported types
  (match `echo.HTTPStatusCoder`); CORS middleware panics without origins (`INSIGHTS.md`).
- The runtime image is Alpine with `git` (not distroless): the backend shells out to git.
- CGO is required for `internal/repointel/adapters/treesitter` (tree-sitter C runtime): host
  `go test -race` needs `gcc`, and `CGO_ENABLED=0` builds fail. The Dockerfile build stage adds
  `build-base`; the runtime image has no C toolchain (same musl: `alpine3.23` build, `alpine:3.23` runtime).
- The schema already contains every table of later phases — empty ≠ unused.

## Do not touch
- Committed migrations in `migrations/` — add a new version instead.
- `../frontend/src/vendor/shared` — the wire contract; adapt the Go side to it.
