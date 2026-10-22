# Context pack: Go backend Phase 3 (repo-intel)
`DD/` = `/home/ypodop/IdeaProjects/dev-digest` (read-only, d1c9093), `N/` = `DD/server/src`; `R/`,`I/`,`P/` as in the plan.

## Lane contracts (fixed; wired in S20)
- [C0] S1's code is the contract: `R/domain/model.go` (rows, `Syntax`, `WalkResult`, `IndexState`, `SourceFS`, `TokenCounter`, §7.1 constants), `R/app/ports.go`; changes via orchestrator
- [C1] `(*postgres.Tx).Savepoint(ctx, fn func(context.Context) error) error`: SAVEPOINT in ctx's tx, stores reach it via `Conn`; error without a tx
- [C2] `(*jobs.Runner).Registered(kind string) (time.Duration, bool)`: effective attempt limit
- [C3] repointel `(*app.Jobs).TriggerIndex(ctx, ws, repoID, owner, name string) error`, `TriggerRefresh(ctx, ws, repoID string) error`; repos `JobsOptions.Index` (nil = none)
- [C4] `(*app.Reader).ConventionSamples(ctx, repoID string, n int) ([]string, error)`; conventions `JobsDeps.Ranker` (nil = fallback)
- [C5] `(*app.Service).IndexState(ctx, repoID string) domain.StateView` (never fails), `Resync(ctx, ws, repoID string) (string, error)`; kinds `repo-intel-{index,refresh,resync}`, `JobTimeout = 120 s`
- gitcli `(*Client).DiffNames(ctx, owner, name, base, head string) ([]string, error)`, `ErrOutputTooLarge`

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38` — "The Go API must stay wire-compatible with the UI" → §7.2 key order = struct order + `omitempty`
- [K2] `backend/AGENTS.md:35` — "Echo only in … `transport/http` packages and `cmd/`. GORM only in `adapters/postgres` packages"
- [K3] `backend/AGENTS.md:39-40` — "`Raw`/`Exec` only for … advisory locks, CTEs, DDL. Row structs are declared per adapter" → lock, FR3030 via Raw; own `repos` row
- [K5] `CLAUDE.md:43` + `backend/AGENTS.md:50` — Origin/Host guards; "Request bodies only through `httpx.ReadJSONBody`" → resync never reads its body
- [K6] `backend/AGENTS.md:48` — "Git only via `internal/platform/gitcli`"
- [K7] `backend/INSIGHTS.md:22` — startup work before listen "must not be fatal"
- [K8] `backend/INSIGHTS.md:47` — `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go …` in `backend/`
- [K9] `backend/INSIGHTS.md:24` — "redact provider keys before truncating to 500 chars" (`llm.Client.Redact`)
- [K10] `backend/INSIGHTS.md:23` — SQLState via `errors.As` on `interface{ SQLState() string }` → 23503 = repo gone
- [K11] `backend/INSIGHTS.md:16,35` — byte order = `COLLATE "C"`, never on a select alias
- [K12] `backend/INSIGHTS.md:15` — keep `ParameterizedQueries: true` (NFR3005)
- [K13] `backend/INSIGHTS.md:26` — stores join a tx only via `postgres.Conn(ctx, db)`
- [K14] `backend/INSIGHTS.md:32` — pin `go get` versions
- [K15] `CLAUDE.md:76` project `architecture-reviewer`; `:92` contracts untouched; `frontend/INSIGHTS.md:58` `pnpm exec vitest run <file>`

## Verified facts
- [F1] Go proxy dates: go-tree-sitter v0.25.0 2025-02-02, tree-sitter-typescript v0.23.2 2024-11-11, tree-sitter-javascript v0.25.0 2025-09-01, tiktoken-go/tokenizer v0.7.0 2025-08-08 (v0.8.2+ tagged 2026-10-21). crates.io ast-grep-language 0.43.0: tree-sitter ^0.26.3, typescript ^0.23.2, javascript ^0.25.0. AC3054 fallback: pkoukk/tiktoken-go v0.1.8 + tiktoken-go-loader v0.0.2
- [F2] Node decodes with U+FFFD per maximal subpart and keeps the BOM; `I/conventions/adapters/clonefiles/clonefiles.go:56` `[]rune` replaces per byte
- [F3] `N/adapters/codeindex/extract.ts:182-214`: JS regexes with `\2`, `\s`, `/i`
- [F4] go-tree-sitter v0.25.0: no finalizers → `Close` Parser/Tree/Cursor; `ParseWithOptions` + `ProgressCallback` (`parser.go:319`); one Parser per worker
- [F5] Goldens: `cd backend/internal/repointel/parity/testdata && TMPDIR=<scratch> DD/server/node_modules/.bin/tsx --tsconfig DD/server/tsconfig.json gen/generate.mts --dd DD --out goldens`. Imports N by file URL (astgrep, codeindex/extract, depgraph, rank, repo-map, walk); js-tiktoken via `createRequire(DD/server/package.json)`, `encode(t, [], [])`; `chdir` per graph fixture; asserts DD HEAD d1c9093 + clean status before/after (tsx 4.22.4, TS 5.9.3, depcruise 17.4.3 installed)
- [F6] `I/jobs/runner.go:357-369` handler ctx: DeadlineExceeded = timeout, Canceled = shutdown; `P/retry/retry.go:109` neither retried
- [F7] `P/gitcli/gitcli.go:38,362` 1 MiB stdout cap, silent
- [F8] `P/clonefs/walk.go:45` skips symlinks, unreadable dirs; `read.go:172`
- [F9] `I/repos/domain/repo.go:27` Repo has WorkspaceID (refresh payload lacks it)
- [F10] baseline: all 7 tables FK `ON DELETE CASCADE` (state :1777); symbols unique :1126
- [F11] `P/httpx/validate.go:85` `PathUUID` keeps the id as sent
- [F12] `I/conventions/app/job.go:176` sample; `cmd/server/app.go:138` reap, `:177` registerJobs
- [F13] `frontend/src/test/go-backend-contracts.test.ts:55` a contract per golden

## Mirrors
- `I/conventions/app`, `I/repos/`, `cmd/server/phase2_test.go`; Node `N/modules/repo-intel/`, `N/adapters/`

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| all | `backend/**/*.go` | go-architecture | §1 inward, consumer ports; §3 no globals; §6 owned goroutines; §7 tx |
| all | `*_test.go` | go-testing | §3 tables; §4 fakes; §5 synctest; §6 pgtest |
| S6,S10,S12,S16,S22 | paths, SQL, routes | engineering-paved-path:security | A01 confinement; A03 bind params; A09 logs |
| S11,S24 | `*.mts`, `*.test.ts` | — | [K15] |

## Verification sources
- `make backend-check` — `Makefile:111` · `docker compose config -q` — `Makefile:51`
- `docker compose build backend` — `Makefile:35` · vitest — [K15] · generator — [F5] (S11 README)

## Risks
- `-race` turns on checkptr for the cgo binding → S1 smoke; if it trips, stop and ask (never weaken the gate); musl: build = runtime Alpine 3.23
- NFR3001 60 s (P15 re-parses on incremental)
- regexp2 `\s` ≠ JS `\s` (U+0085, U+FEFF) → AC3054 goldens decide; else [F1] fallback
- Tokenizer adds ~15 MB vocab to the binary; shared tree: touch only your Files

## Notes for reviewers
- Base `8b6c896`; architecture [K15]; layers by grep
- Security: symlinks, tsconfig `extends`, no exec/network in `R/`, no source in logs, redaction
