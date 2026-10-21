# Development Plan: Go backend Phase 1 — platform and core data
Plan: Go backend Phase 1 · Status: approved · Spec: specs/2026-10-21-go-backend-phase-1.md · Base: master@dfb9fc7 (branch feat/go-backend-phase-1)
Packages: backend (+1 frontend test) · Context pack: docs/plans/2026-10-21-go-backend-phase-1.context.md
Execution: multi-agent (user standing instruction)

## Goal
The unchanged UI runs against the Go backend: platform, migrations + seed at startup, secrets, clone jobs, shutdown and the 16 Phase-1 routes (spec §1).

## Out of scope
Spec §3 "Out of scope"; no ARCHITECTURE.md, lint config or CI.

## Decisions
- Own migration runner: one tx + advisory lock per version, `schema_migrations` created in it (goose creates it outside → AC31) — FR15
- Baseline = cleaned `pg_dump --schema-only` of Node 0000–0020; fallback verbatim concat — [F11][F14]
- Plain net/http for GitHub and LLM providers, no SDKs — [F5][F6][F8]
- Base URLs, git remote, timeouts: Config/Options fields, not env
- Git token only via `GIT_CONFIG_COUNT/KEY_0/VALUE_0` env, never argv — NFR1, [F7]
- Refresh branch: `git ls-remote --symref origin HEAD`, fallback `default_branch` — FR34
- Rate-limit IP = socket address (`echo.ExtractIPDirect`) — [F3]
- No shared `models` package; row structs per adapter — go-architecture §4, §11
- Job handler `func(ctx, json.RawMessage) error`; port `Enqueue(ctx, wsID, kind string, payload any) (string, error)`
- Only S3 and S16 edit go.mod/go.sum
- AC26 via Go goldens + one vitest file — [K9]

## Steps
`P/` = `backend/internal/platform/`, `I/` = `backend/internal/`. Go per [K8]. Integration tests use `pgtest`, skip under `-short`. Done when = `go test -count=1` on the step's packages passes.

### S1 — Errors, validation, wire time [W1-A]
- Files: A `P/apperr/apperr.go`; A `P/httpx/{errors,validate,wire}.go`; M `P/httpx/echo.go`; tests beside
- Change: apperr kinds→FR4 [F1]; unknown→500 hidden+logged; 422 details [F2]; `PathUUID`; `Time` `.000Z` UTC
- Rules: [K2][K7]; go-architecture §5 · Covers: AC1–AC5 · Tests: httptest tables

### S2 — Middleware [W1-A, after S1]
- Files: A `P/httpx/{body,headers,cors,ratelimit,reqlog}.go`; M `echo.go`, `I/health/http_test.go`; tests beside
- Change: `New(Options)`; §7.1 body, headers, own CORS in `Pre`, HEAD [F3], fixed windows (global + named buckets; skip health/preflight/unmatched), request log line
- Rules: [K6] · Covers: AC6–AC8, AC10–AC20, AC98 · Tests: tables; injected clock

### S3 — Migrations, pgtest [W1-B]
- Files: A `backend/migrations/{embed.go,0001_baseline.up.sql,.down.sql}`; A `P/postgres/migrate/` (+`testdata/drizzle/` copies); A `P/postgres/pgtest/`; M go.mod/sum
- Change: Up/Down/Status, NFR9 lines; down drops 44 tables + `vector`; one container per binary, fresh DB per test
- Rules: [K3]; go-testing §6 · Covers: AC27–AC31, AC101 · Tests: catalog diff vs Node; idempotent up; down→up; status; pre-existing `repos`

### S4 — Seed, tenancy [W1-B, after S3]
- Files: A `I/seed/{seed,patches}.go` + golden; A `I/seed/adapters/postgres/`; A `I/tenancy/tenancy.go`, `I/tenancy/adapters/postgres/`
- Change: FR19–FR20, §8, one tx [F10]; resolver `default`/`you@local`, missing → plain error (FR13)
- Covers: AC34–AC36, AC101 · Tests: values, rerun counts, CHECK-forced failure, patches == golden

### S5 — Config, secrets, retry, logging [W1-C]
- Files: M `I/config/config.go`; A `P/{secrets,retry,logging}/`; tests beside
- Change: §7.1 env; bools off only on `false`; secrets FR25 (read per call, atomic 0600); `retry.Do` + `IsTransient` [F5]; NFR3 redaction
- Covers: AC41, AC47, AC48 · Tests: tables, `t.TempDir`, synctest

### S6 — GitHub client [W1-C, after S5]
- Files: A `P/github/{client,pulls,comments}.go`; tests beside
- Change: token per call, `ErrNoToken`, `StatusError{Status,Message}`; NFR5 [F6]
- Covers: AC83, AC92, AC93 · Tests: httptest stubs (retry counts, caps, merge, hang)

### S7 — git adapter [W1-D]
- Files: A `P/gitcli/{gitcli,paths}.go`; tests beside
- Change: FR35 path guard; Clone/Sync [F7]; no argv/token in errors
- Covers: AC66, AC68–AC71, AC99 · Tests: `file://` bare remote [F13]; wrapper Binary logs argv/env

### S8 — Job runner [W2-E]
- Files: A `I/jobs/runner.go`, `I/jobs/adapters/postgres/store.go`; tests beside
- Change: FR32, attempts [F4], Shutdown(10 s) keeps queued, NFR9
- Covers: AC63–AC65, AC94, AC101 · Tests: fake store + synctest; pgtest store

### S9 — Settings app [W2-F]
- Files: A `I/settings/app/`, `adapters/postgres/`, `adapters/providers/`
- Change: FR21–FR25, §7.2 messages, key→`[REDACTED]` [M1][F8]
- Covers: AC38–AC45, AC47 · Tests: fakes, pgtest, httptest

### S10 — Settings HTTP [W2-F, after S9]
- Files: A `I/settings/transport/http/`
- Change: 4 routes; §7.2 PUT schema; bucket 20
- Covers: AC6–AC8, AC10, AC11, AC25, AC37–AC45, AC47 · Tests: httptest + pgtest + stubs

### S11 — Repos core [W2-G]
- Files: A `I/repos/domain/`, `app/`, `adapters/postgres/`
- Change: URL [F9]; Add via ON CONFLICT DO NOTHING + reselect; clone/refresh handlers [M2]
- Covers: AC49–AC53, AC62, AC66–AC69 · Tests: AC53 table, fakes, concurrent insert, `file://` clone

### S12 — Repos HTTP [W2-G, after S11]
- Files: A `I/repos/transport/http/`
- Change: §7.3 routes incl. `/workspace`
- Covers: AC9, AC22–AC24, AC49–AC58 · Tests: httptest + pgtest + fake enqueuer

### S13 — Pulls domain, store [W2-H]
- Files: A `I/pulls/domain/`, `adapters/postgres/`
- Change: FR37, merge, FR39; upsert rules, FR36 order, FR40 rollups, detail tx [M3]
- Covers: AC73, AC74, AC77, AC78, AC80 · Tests: tables, pgtest fixtures

### S14 — Pulls use cases [W2-H, after S13]
- Files: A `I/pulls/app/`, `adapters/github/`
- Change: FR31, FR36, FR38–FR42
- Covers: AC59–AC61, AC72–AC77, AC79, AC81–AC84, AC86–AC90 · Tests: fakes; stub

### S15 — Pulls HTTP [W2-H, after S14]
- Files: A `I/pulls/transport/http/`
- Change: 5 routes, §7.4 null/omit rules
- Covers: AC3, AC56, AC59–AC61, AC72–AC93 · Tests: httptest + pgtest + stub

### S16 — Composition root [W3-J]
- Files: M `backend/cmd/server/main.go`; A `app.go`, `cli.go`; M go.mod/sum (tidy)
- Change: `run` = Load + `serve(ctx,cfg)`; CLI; migrate→seed→jobs→listen; drain 20 s ∥ jobs 10 s → close DB
- Covers: AC31–AC33, AC94 · Tests: S17 · Done when: `go build ./... && go vet ./...`

### S17 — Component tests [W3-J, after S16]
- Files: A `backend/cmd/server/{main,flows}_test.go`, `testdata/contracts/`
- Covers: AC1, AC2, AC12–AC16, AC20, AC21, AC26, AC31–AC33, AC46, AC49, AC50, AC55, AC62, AC63, AC70, AC94, AC98, AC101
- Tests: `serve` + pgtest + stubs · Done when: `go test -race -count=1 ./cmd/server/...`

### S18 — Frontend contract test [W3-J, after S17]
- Files: A `frontend/src/test/go-backend-contracts.test.ts`
- Covers: AC26 · Tests: Zod parse of each golden · Done when: `cd frontend && pnpm install --frozen-lockfile && pnpm test -- src/test/go-backend-contracts.test.ts`

### S19 — Compose [W3-K]
- Files: M `docker-compose.yml` (switches, `stop_grace_period: 30s` [F15]), `.env.example`
- Covers: AC95–AC97, AC100 · Tests: manual · Done when: `docker compose config -q`

## Execution
- Mode: multi-agent; lanes have disjoint files, steps in a lane run in order.
- W1: A S1→S2 ∥ B S3→S4 ∥ C S5→S6 ∥ D S7
- W2: E S8 ∥ F S9→S10 ∥ G S11→S12 ∥ H S13→S15
- W3: J S16→S18 ∥ K S19

## Contracts & migrations
Contracts unchanged. Migration v1 baseline (S3).

## Verification
- Gates: `cd backend && go build ./... && go vet ./...`; `go test -race -shuffle=on -count=1 ./...` — CLAUDE.md:20-21
- Manual: `docker compose up -d --build`, `docker compose ps`, `curl localhost:3101/health/ready` — CLAUDE.md:18-19; AC96, AC97, AC100
- Review risk: high — data, secrets, git subprocess, concurrency, contracts

## Open questions / assumptions
- `DEVDIGEST_DATA_DIR` unset → `$HOME/.devdigest` (Node parity)
- AC21 via `DROP DATABASE … WITH (FORCE)`; AC94 via ctx cancel (SIGTERM manual)
- Anthropic: first `/v1/models` page only (Node parity)
