# Context pack: PR Leaderboard
[A] `B/`=backend/ · `L/`=B/internal/leaderboard · `P/`=B/internal/pulls · `G/`=B/internal/platform/github · `S/`=B/cmd/server · `M/`=B/migrations · `F/`=frontend/src · `V/`=F/app/leaderboard/_components/LeaderboardView · `C/`=V/_components · `E/`=frontend/messages/en

## Lane contracts (S3 writes them as code)
- [C1] `L/domain`: `ParseQuery(url.Values) (Query, []Issue)`; `ResolveWindow(key, *time.Location, now) Window{From,To,PrevFrom,PrevTo,Days}` + `DayIndex(t)`; `BuildRows(win, Facts, now) (cur, prev []Row)`; `Rank([]Row)`; `Kpis`, `Awards`, `Attention(rows, []OpenPull, now) (total, items)`, `HotFiles`, `HealthBySize`, `Daily`. `L/app` port `Reader{RepoScope(ctx, ws, repo) ([]RepoRef, error)`; `WindowFacts(ctx, ws, repoIDs, from, to) (Facts, error)`; `OpenState(ctx, ws, repoIDs) ([]OpenPull, error)}`; `API.Leaderboard(ctx, ws, Query)`. `P/app.NewPoller(deps, PollerOptions{Interval, Horizon 62d, MaxPages 10, PageSize 50}).Run(ctx)`
- [C2] Fixture: 3 repos, 2 agents + 1 deleted, re-reviews, triaged findings, runs (+1 CI), merges in/out, merged unreviewed, merged without `merged_at`, review at `to`, zero stats, null `opened_at`; then delete a repo
- [C3] AC12: throwaway compose project [K1], NFR1 volume, 200 req p95, `log_statement=all`: no writes

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38-46` — "wire-compatible with the UI"; foreign `Origin` 403, `ALLOWED_HOSTS`; `:90` never `docker compose down -v`
- [K2] `B/AGENTS.md:55` — "Features live in `internal/<context>/{domain,app,adapters/<tech>,transport/http}`"
- [K3] `:69-76` — "Echo only in …", "GORM only in …", "Row structs are declared per adapter"
- [K4] `:75` — "`Raw`/`Exec` only for what the builder can't express (advisory locks, CTEs, DDL)"
- [K5] `:62` — "contexts reach each other only through consumer-side ports wired in `cmd/`"
- [K6] `:79` — "a new version … with an `.up.sql` **and** a `.down.sql`"; never edit 0001
- [K7] `:77` — "use cases return `apperr` kinds; only `httpx` maps kinds to status"
- [K8] `:89` — "every store method called inside `InTx` takes its handle from `postgres.Conn(ctx, db)`"
- [K9] `:81` — "timestamps are ISO-8601 UTC with milliseconds (`…000Z`)"
- [K10] `:32` — shutdown order; add the poller (≤ 5 s)
- [K11] `CLAUDE.md:92` — contracts "change … only together with the matching backend change"
- [K12] `B/INSIGHTS.md:14` — no N+1: one ROW_NUMBER query, DTO columns only
- [K14] `frontend/AGENTS.md:33` — "`src/vendor/ui` … compose, don't edit"
- [K15] `frontend/INSIGHTS.md:19,29` — sidebar items were added to vendored `nav.ts`; labels are literals, `shell.json nav.*` feeds the palette
- [K16] `frontend/AGENTS.md:13,15,17` — thin pages; data only via `src/lib/hooks`; strings in `messages/en`
- [K17] `frontend/INSIGHTS.md:47` — page awaits `searchParams`, view changes URL via `router.replace`
- [K18] `:50` — keys only from `keys.ts`
- [K19] `:28` — query via `api.requests('GET /x')[i].search`
- [K20] `:37` — dropdowns: `@/components/select`, tests `pickOption`
- [K21] `:15,20` — `renderWithProviders` + `mockFetch`; toast is also `role=status`
- [K22] `:91` — `strayText` allow-lists data patterns
- [K25] `B/INSIGHTS.md:62` — synctest: `time.Sleep` before `Wait()` moves the clock
- [K26] `:41` — goldens: `go test ./cmd/server -run … -update` (flag after the package)
- [K27] `:57` — trust `TestUp_MatchesNodeSchema`

## Verified facts
- [F1] template `B/internal/performance/**`, wired `S/app.go:557`
- [F3] `P/domain/rollup.go:44` CurrentReviews (spec cites :273; file has 91 lines); `reviews.agent_id` ON DELETE SET NULL; `reviews.score` nullable (`M/0001…up.sql:630-642`)
- [F4] `agent_runs` has `ran_at`, no `started_at`; `cost_usd numeric(12,6)` nullable (`:41-63`)
- [F5] stored status ∈ needs_review|reviewed|stale|open|closed|merged (`:545-564`); `DeriveStatus` `P/domain/pulls.go:153`
- [F6] `migrate_test.go:144-310` assume one version (n == 1, Down → v1, Status len 1)
- [F7] `retry.Options{}` = no retry (`platform/retry/retry.go:22-26`); `getJSON` retries 429/5xx
- [F8] `G/client.go:40-52` `StatusError` has `RateLimitRemaining`, no reset; token never in errors (`:4-6`)
- [F9] `S/main.go:21` embeds `time/tzdata`
- [F10] `S/main_test.go:123-138` test `Config` literal; `config_test.go` compares whole structs
- [F12] `useShellCommands.ts:24-31` (app-shell) builds the palette from `NAV`
- [F13] `F/i18n/request.ts:9-11` one namespace per `messages/en/*.json`
- [F14] `DeltaText` (agent-metrics) hides zero change, sign only
- [F15] `frontend/INSIGHTS.md:90` charts: `data-series` tags

## Mirrors
- [F1]; `S/phase5_repo_test.go:560-600`; `F/app/agent-performance/**`, `F/lib/hooks/performance.ts`; `StatCard`, `BarRow`, `Avatar`, `CostText`

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| S3–S9,S15,S16,S18 | `B/**/*.go` | go-architecture | §1 inward, consumer ports; §5 errors at edge; §6 goroutine owner + wait |
| all Go tests | `*_test.go` | go-testing | tables + cmp.Diff; fakes; pgtest; synctest; fuzz parsers |
| S4,S6,S15,S16 | GitHub, query input | security | A05 validate `tz`/`repo`; A09 no token in logs |
| S1,S9 | SQL | postgresql-table-design | timestamptz, NOT NULL DEFAULT |
| S2,S10–S14,S17,S21 | `F/**` | react-best-practices, typescript-expert | derive don't store; URL state; no `any` |
| tsx tests | | react-testing-library | userEvent, roles; [K21] |
| S17 | README | mermaid-diagram | one node per route |

## Verification sources
- [V1] from `backend/`: `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9,12`)
- [V2] from `frontend/`: `pnpm exec vitest run <file-name substring>` (`frontend/INSIGHTS.md:59,81`)
- [V3] `make backend-check` (Makefile:119), `make frontend-check` (:144), `make check` (:149)

## Risks
- Poller in other tests → interval 0 [F10] · 30d p95 → S22 · cutoff moved by manual poll (R1)

## Notes for reviewers
- Architecture: project `architecture-reviewer` (`CLAUDE.md:76`); P1, P5, P10
- Security: `tz`/`repo` input, token, read-only route
