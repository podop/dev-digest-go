# Context pack: Go backend Phase 1
`N/` = `/home/ypodop/IdeaProjects/dev-digest/server/src` (Node source of truth, read-only). Spec ids are in `specs/2026-10-21-go-backend-phase-1.md`.

## Repo rules and notes that apply
- [K1] `CLAUDE.md:30` — "The Go API must stay wire-compatible with the UI: same paths, status codes, JSON field names," → DTOs follow spec §7 exactly
- [K2] `CLAUDE.md:33` — "Echo only in `internal/platform/httpx`, transport packages and `cmd/`; GORM only in adapters and `internal/platform/postgres`." → app/domain import neither
- [K3] `CLAUDE.md:35` — "Schema changes are versioned SQL migrations with an up and a down; never GORM AutoMigrate."
- [K4] `CLAUDE.md:25` — "Never run `docker compose down -v` unless asked"
- [K5] `CLAUDE.md:49` — "Repository decisions above override the skills' defaults" (Echo, GORM)
- [K6] `backend/INSIGHTS.md:20` — CORSWithConfig "panics at setup when AllowOrigins is empty" → own CORS must accept an empty list
- [K7] `backend/INSIGHTS.md:21` — router 404/405 are unexported → "match echo.HTTPStatusCoder (StatusCode())"
- [K8] `backend/INSIGHTS.md:25` — Windows GOROOT breaks go → from `backend/` run `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go …` (`/usr/bin/go` is 1.25, go.mod needs 1.27)
- [K9] `frontend/AGENTS.md:22` — "`src/vendor/shared` is the wire contract the Go backend (`../backend`) must honor" → never edit it
- [K10] `CLAUDE.md:63` — "WRAP-UP: … append only new, verified, non-obvious insights" (engineering-insights skill)

## Verified facts
- [F1] `N/http/error-handler.ts:27-38` status table; `N/platform/errors.ts` default codes (`invalid_input`, `validation_error`, `not_found`, `conflict`, `payload_too_large`, `config_error`, `internal_error`); spec §7.1 uses `external_service`
- [F2] fastify-type-provider-zod `errors.js:33-42`: `{keyword: code, instancePath: "/"+path.join("/"), schemaPath: "#/"+path.join("/")+"/"+code, params:{issue}, message}`
- [F3] echo v5.4.0: `echo.NewRouter(echo.RouterConfig{AutoHandleHEAD:true})` (`echo_test.go:1493`), `echo.ExtractIPDirect()` (`ip.go:207`), `StartConfig.GracefulTimeout`/`OnShutdownError` (`server.go:51-56,185-196`)
- [F4] `N/platform/jobs.ts:64-125`: queued→running→done/failed+error; success sets attempts=1, retry n sets n; timeout not transient; no retry while shutting down
- [F5] `N/platform/resilience.ts:84-113`: transient = 429/≥500 or ECONNRESET/ETIMEDOUT/ENOTFOUND; delay min(8000, 250·2^n)+rand·250
- [F6] `N/adapters/github/octokit.ts:22-31,49-62,88-99,113,323`: 30 s, detail 90 s, files ≤3000, commits ≤250, pages of 100 stop on short page, list per_page 50, comments 100, same-path merge
- [F7] `N/adapters/git/simple-git.ts:49-52,89-102,141-182`: `Authorization: Basic base64("x-access-token:"+token)`; `.git` exists → fetch, else rm partial + clone; sync = fetch origin <b> --depth 50 + reset --hard origin/<b>
- [F8] `N/adapters/llm/openai.ts:76-83` id filter; `anthropic.ts:71-81` one page; flow `N/modules/settings/service.ts:58-80`
- [F9] `frontend/src/vendor/shared/contracts/platform.ts:89-103` URL regex + `isSafeRepoSegment`
- [F10] `N/db/seed.ts:44-146`; `N/db/seed-diff.ts:12-64` arrays `.join('\n')`, no escapes. Golden: `node --experimental-strip-types -e "import('<N>/db/seed-diff.ts').then(m=>process.stdout.write(JSON.stringify(m.SEED_PR_482_PATCHES,null,2)))"`
- [F11] `N/db/migrations/0000…0020.sql`; none creates `vector` (research 02 §1); DML in 0011/0013 no-ops on an empty DB
- [F13] git ignores `--depth` for plain-path local clones → tests use `file://`
- [F14] Baseline: scratch `pgvector/pgvector:pg16`, `CREATE EXTENSION vector`, `psql -v ON_ERROR_STOP=1` each Node file, `pg_dump --schema-only --no-owner --no-privileges`; strip `SET …` and `set_config('search_path'…)` (empties the runner session's search_path)
- [F15] `docker-compose.yml` sets no stop timeout; docker default 10 s < FR43 20 s
- [F16] go.mod `go 1.27`: synctest, `t.Context`, `wg.Go` available

## Mirrors
- [M1] `N/modules/settings/*`, `N/adapters/secrets/local.ts`
- [M2] `N/modules/repos/*`, `N/modules/workspace/service.ts`, `N/platform/jobs.ts`
- [M3] `N/modules/pulls/{service,domain,repository}.ts` (rollups `:218-283`), `N/modules/polling/service.ts`
- Go: `backend/internal/health/` (AddRoutes + fake port), `httpx/echo_test.go` (tables + cmp)

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| all | `backend/**/*.go` | go-architecture | §1 inward deps, ports at consumer; §3 ctor injection, no globals/init; §5 map errors only at edge; §6 ctx first, goroutines owned+waited; §7 tx in adapter; §8 env only in cmd |
| all | `*_test.go` | go-testing | §2 cmp.Diff, `Func(in) = got, want`; §3 tables, `_test` pkg, t.Helper/Cleanup/Context; §4 fakes, httptest; §5 no sleep, synctest, no t.Parallel with t.Setenv; §6 testcontainers + `-short` |
| S2,S5,S7,S9–S12 | input, secrets, git, CORS | engineering-paved-path:security | A05 exec without shell, `--` before url/path; A01 every query workspace-scoped; A09 redact; A02 exact origin list |
| S3 | SQL | postgresql-table-design | not loaded: schema copied, not designed |
| S18 | `frontend/src/test/*.ts` | (vitest only) | no React; no contract edits [K9] |

## Verification sources
- `go build ./... && go vet ./...`, `go test -race -shuffle=on -count=1 ./...`, `go test -short ./...` — `CLAUDE.md:20-22`
- `docker compose up -d --build`, `docker compose ps` — `CLAUDE.md:18-19`
- `pnpm test` — `frontend/package.json` scripts

## Risks
- Parallel lanes share one tree → touch only your step's Files; no `go mod tidy` outside S3/S16
- Baseline drift from Node → AC27 catalog diff
- Token leak via argv/errors/logs → env-only token, scrubbed stderr, AC70/AC98

## Notes for reviewers
- Changed files: `git diff --name-only master...`; layer check not configured → grep checks (`go-architecture` references/enforcement.md §4)
- Architecture: use project agent `architecture-reviewer` (`CLAUDE.md:70`)
- Security: clone path guard, no shell, workspace scoping, secrets file 0600, no key in any response
