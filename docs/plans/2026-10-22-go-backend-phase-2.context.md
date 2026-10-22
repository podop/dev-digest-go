# Context pack: Go backend Phase 2
`N/` = `/home/ypodop/IdeaProjects/dev-digest/server/src` (read-only); `GO/` = GOROOT `src/`; `I/`,`P/` as in the plan.

## Lane contracts (fixed; wired in S22)
- [C1] `postgres.NewTx(db) *Tx`; `(*Tx).InTx(ctx, fn func(context.Context) error) error`; nested joins; stores use `postgres.Conn(ctx, db) *gorm.DB`
- [C2] agents app `(*Service).AppendSkill(ctx, workspaceID, agentID, skillID string) error` (link mode)
- [C3] conventions app `(*Service).NotAccepted(ctx, workspaceID, repoID string, ids []string) ([]string, error)`, `AttachSkill(ctx, workspaceID, repoID string, ids []string, skillID string) error`
- [C4] `llm.(*Client)`: `Structured(ctx, Request) (Result, error)`, `ListModels(ctx, Provider) ([]ModelInfo, error)`, `Redact(string) string`; `llm.ResolveFeatureModel(map[string]json.RawMessage, feature string) (Provider, string)`
- [C5] `jobs.(*Runner).Register(kind string, h Handler, opts ...RegisterOption)`, `jobs.WithTimeout(time.Duration)`
- [C6] conventions app: `KindExtract = "conventions.extract"`, `JobTimeout = 300 * time.Second`, `(*Jobs).Extract`, `(*Service).FailInterrupted(ctx) (int, error)`; feature-model port `Get(ctx, ws) (map[string]json.RawMessage, error)` = settings [F12]
- skills, projectcontext transports export `BodyLimits() map[string]int64`

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38` — "The Go API must stay wire-compatible with the UI" → DTOs per spec §7
- [K2] `backend/AGENTS.md:35` — "Echo only in `internal/platform/httpx`, `transport/http` packages and `cmd/`. GORM only in `adapters/postgres` packages"
- [K3] `backend/AGENTS.md:40` — "Row structs are declared per adapter — no shared `models` package." → own rows for read-only lookups
- [K4] `backend/AGENTS.md:41` — "use cases return `internal/platform/apperr` kinds" → `.WithCode(…)` for new codes
- [K6] `backend/AGENTS.md:50` — "Request bodies only through `httpx.ReadJSONBody`"
- [K7] `backend/INSIGHTS.md:28` — echo 404/405 are unexported → "match echo.HTTPStatusCoder"
- [K8] `backend/INSIGHTS.md:33` — run Go as `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go …` from `backend/`
- [K9] `backend/AGENTS.md:47` — "Secrets only via `internal/platform/secrets` … never logged, never in errors."
- [K10] `backend/INSIGHTS.md:24` — "a custom sql.Scanner is not called for SQL NULL" → nil-check nullable jsonb
- [K11] `CLAUDE.md:92` — vendored contracts: change one only together with the backend → S24 adds goldens, never edits contracts
- [K12] `frontend/INSIGHTS.md:57` — use `pnpm exec vitest run <file>`
- [K13] `CLAUDE.md:76` — "Use the project agent `architecture-reviewer`"
- [K14] `frontend/INSIGHTS.md:90` — mock override lists only `mock/reviewer` → AC301 without it

## Verified facts
- [F1] `I/jobs/runner.go:328-343` handler ctx: `DeadlineExceeded` = timeout, `Canceled` = shutdown; nil → job `done`; `:338` uses global `r.timeout`
- [F2] `P/httpx/body.go:15` fixed 1 MiB; `bodyRules` runs after routing → per-route lookup works
- [F3] `P/logging/logging.go:17-26,46` redacts by attribute **name** only → key values inside error texts must be scrubbed (D219)
- [F4] `P/retry/retry.go:108-126`: `net.Error` timeout = transient, ctx deadline not
- [F5] `GO/os/root.go:82,209` `OpenRoot`, `Root.Lstat`; `GO/net/http/httptest/server.go:177` `NewTestServer` (synctest network)
- [F6] `GO/archive/zip/reader.go:301` reading past the declared size → `ErrFormat`; use `File.OpenRaw` (`:260`) + `flate.NewReader` under `io.LimitReader(…, 262145)`
- [F7] AC255 needs WHATWG IPv6 text (`::ffff:a00:1`, not Go's `::ffff:10.0.0.1`) and IPv4 hex/octal/short forms
- [F8] prompt `N/modules/conventions/domain/extraction.ts:45-63`, descriptions `:14-42`; schema via `zodResponseFormat` (`reviewer-core/src/llm/structured.ts:19`)
- [F9] Data: `cd N && node --experimental-strip-types -e "import('./db/seed-prompts.ts').then(m=>process.stdout.write(JSON.stringify(m,null,2)))"`; same for `db/seed-skills.ts` (`SEED_SKILLS`, `SEED_AGENT_SKILLS`) and `modules/skills/infrastructure/community-catalog.ts` (`builtInCommunityCatalog.list()`); not run here; agent names/descriptions from spec §8
- [F10] `backend/migrations/0001_baseline.up.sql`: `skills_ws_name_uq` :1112 (23505 → 409), `convention_scans_one_running_uq` :1207, `agent_context_docs` PK (agent, repo, path) :769
- [F11] the seed is one tx, so `now()` gives all 4 agents the same `created_at` (AC228)
- [F12] `I/settings/app/settings.go:119` `Get(ctx, ws) (Bag, error)`, `Bag = map[string]json.RawMessage`
- [F13] `cmd/server/flows_test.go:46` asserts `GET /agents` 404 → S22 changes it; harness `main_test.go:115`
- [F14] `frontend/src/test/go-backend-contracts.test.ts:71-77`: every golden needs a contract entry

## Mirrors
- Node: `N/modules/{agents,skills,conventions,project-context}/`, `N/adapters/llm/`, `N/platform/{price-book,prompt-log}.ts`, `reviewer-core/src/llm/`
- Go: `I/repos/` (layers), `I/settings/transport/http/validate.go` (Zod texts), `cmd/server/flows_test.go:508` (goldens)

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| all | `backend/**/*.go` | go-architecture | §1 inward, ports at consumer; §3 no globals; §5 errors at edge; §6 goroutines owned; §7 tx (C1) |
| all | `*_test.go` | go-testing | §3 tables; §4 fakes, httptest; §5 synctest; §6 pgtest, `-short` |
| S4,S11,S12,S15–S20 | paths, URLs, zip | engineering-paved-path:security | A01 no traversal/SSRF; A05 no exec; A09 no keys in logs |
| S24 | `frontend/src/test/*.ts` | (vitest only) | [K11] |

## Verification sources
- `make backend-check` — `Makefile:111` (fmt, build, vet, race tests)
- `pnpm exec vitest run <file>` — [K12]

## Risks
- Shared tree → touch only your Files; no `go mod tidy` outside S11/S22
- SSRF via DNS/redirect/proxy → dial only validated IPs (NFR203)
- Scan end after ctx done → write on `context.WithoutCancel`

## Notes for reviewers
- Diff base `430ec39`; architecture → [K13]; no layer lint → grep
- Security: URL guard, clonefs confinement, key scrub (scan/job errors, logs), imports disabled
