# Development Plan: PR Leaderboard
Plan: PR Leaderboard · Status: approved · Spec: specs/2026-10-23-pr-leaderboard.md · Base: feat/go-backend-phase-1@eacb860
Packages: backend, frontend · Context pack: docs/plans/2026-10-23-pr-leaderboard.context.md
Execution: multi-agent, parallel lanes per wave (user)

## Goal
`GET /leaderboard` computes the page on read; every sync stores merge/close time and draft; a background poller keeps PRs fresh; `/leaderboard` renders it. Aliases: [A].

## Out of scope
Spec §3; PR-list DTOs; new indexes (AC1).

## Decisions
- P1 New context `L/` like performance [F1][K2]; own rows, SELECT only [K3]; status/risk re-implemented in `L/domain` + cross-check test vs `P/domain` [K5]
- P2 ≤ 8 set-based queries, snapshot = ROW_NUMBER CTE [K4][K12]; handles via `postgres.Conn` [K8]
- P3 `tz`: regex, ≤ 64 chars, not `Local`, then `LoadLocation` [F9]. Previous = same calendar days before `from`. `period.*` = local time + offset, ms (AC6; exception to [K9]); other times UTC `…Z`
- P4 Only keys period/tz/repo, one value each, else 422 `httpx.NewValidationError`; unknown repo → `apperr.NotFound("Repo not found")`
- P5 Poller = loop in `P/app`, not a job kind (no job rows); `listen` runs it on the stop ctx and waits [K10]; next cycle at max(start+interval, reset)
- P6 Poller has its own `github.Client`, `Retry: &retry.Options{}` (≤ 10 req/repo) [F7]; `StatusError.RateLimitReset`; limited = `RateLimited()` or 429 (ci untouched)
- P7 Pages 1..10 × 50, upsert each; stop at `updated_at` < cutoff or short page; cutoff = start of this process's previous successful background poll of the repo (in-memory, per repo) else now−62 d; manual poll / list never move it (spec FR4, R1 adopted); `MarkPolled(repo poll start)` still sets `last_polled_at` for display
- P8 `PR_POLL_INTERVAL_SECONDS` 0–86400, default 900, 0 off; test configs stay 0 [F10]
- P9 `0002_pr_merge_state`: `merged_at`, `closed_at timestamptz`, `draft boolean NOT NULL DEFAULT false`; Node-parity tests pinned to v1 [F6]
- P10 One NAV entry + Trophy in vendored `F/vendor/ui` (exception to [K14], precedent [K15]); palette `shell.json nav.leaderboard` [F12]
- P11 One message namespace per UI lane [F13]

## Recommendations not adopted
- R1 adopted after spec change (FR4, D17): own in-memory background-poll cursor
- R2 Merge state from `GET /pulls/:id`; R3 poller diff-stat backfill — beyond FR2/NFR3

## Steps
Done when = step tests [V1]/[V2]; every UI folder has a `*.test.tsx` [K16]; gates [V3].

### S1 — Migration [W0-a]
- Files: A `M/0002_pr_merge_state.{up,down}.sql`; M `B/internal/platform/postgres/migrate/migrate_test.go`
- Change: P9 (fstest.MapFS with 0001 only for parity tests) · Rules: [K6][K27]; postgresql-table-design
- Covers: AC1 · Tests: Up → Down once → 3 columns gone, rest equal → Up
### S2 — UI contract [W0-b]
- A `F/vendor/shared/contracts/leaderboard.ts`; M `F/vendor/shared/index.ts`
- Zod per §7 + Q1 · Rules: [K11]; typescript-expert · Covers: FR6 · Done when: `pnpm typecheck`
### S3 — Skeleton [W0-c]
- A `L/domain/model.go`, `L/app/{ports,api}.go` exactly as [C1]
- Rules: go-architecture §1, §4 · Covers: FR8–FR16 · Done when: build + vet [V1]
### S4 — GitHub fields, paging [W1-d]
- M `G/{pulls,client,github_test}.go`
- Change: `PullSummary` + MergedAt, ClosedAt, Draft; `ListPullRequestsPage`; P6 · Rules: [F8]
- Covers: AC2, FR4, FR5 · Tests: httptest decode, query, reset
### S5 — Sync writes [W1-d]
- M `P/domain/pulls.go`, `P/app/ports.go`, `P/adapters/github/client{,_test}.go`, `P/adapters/postgres/store{,_test}.go`
- Change: `Listed` + 3 fields; upsert writes them with title, head_sha, status, updated_at
- Covers: AC2, FR2, EC10 · Tests: insert, update, idempotent repeat
### S6 — Query, windows [W1-e]
- A `L/domain/{query,window}{,_test}.go` · P3, P4
- Covers: AC6, FR7, EC4, EC5 · Tests: AC6 table, DST days, bounds; fuzz `ParseQuery`
### S7 — Rows, rank [W1-f]
- A `L/domain/{rows,rank}{,_test}.go` · Q2, Q9, Q10
- Covers: AC8, FR8–FR10, EC1, EC6, EC7, EC9 · Tests: hand-computed tables
### S8 — Aggregates [W1-g]
- A `L/domain/{kpis,awards,attention,files,daily}{,_test}.go` · Q5–Q8
- Covers: AC9–AC11, FR11–FR16, EC2, EC3 · Tests: medians, ties, cap 20, top 6, Σ daily
### S9 — Store [W1-h]
- A `L/adapters/postgres/{store,rows,queries,store_test}.go` · P2; `[from, to)`
- Covers: AC8, EC8, NFR1, NFR2 · Tests: pgtest, 2 workspaces, repo delete
### S10 — Sidebar, palette [W1-i]
- M `F/vendor/ui/{nav.ts,icons.tsx}`, `F/components/app-shell/helpers{,.test}.ts`, `.../hooks/useShellCommands.test.tsx`, `E/shell.json` · P10
- Covers: AC14, FR17
### S11 — Data, page, header [W1-j]
- A `F/lib/hooks/leaderboard.ts`, `F/app/leaderboard/page.tsx`, `V/{LeaderboardView.tsx,helpers{,.test}.ts,constants.ts,styles.ts,index.ts}`, `C/Header/*`, `E/leaderboard.json`; M `F/lib/hooks/{keys,index}.ts`
- Change: URL state (EC13), repo vs `useRepos` (EC14), Intl tz, FR18 label, sync note; view = header + states
- Rules: [K16]–[K21] · Covers: AC15, AC19, FR23, EC12
### S12 — KPIs, awards [W1-k]
- A `C/{KpiTiles,AwardCards}/*`, `E/leaderboardHighlights.json`; own delta, not `DeltaText` [F14]
- Covers: AC16, FR19, FR20
### S13 — Table [W1-l]
- A `C/LeaderboardTable/**`, `E/leaderboardTable.json`
- Covers: AC17, FR21, EC15, NFR4
### S14 — Panels [W1-m]
- A `C/{NeedsAttention,HotFiles,HealthBySize,DailyActivity}/*`, `E/leaderboardPanels.json` [F15]
- Covers: AC18, FR22
### S15 — Service, HTTP [W2-n]
- A `L/app/service{,_test}.go`, `L/transport/http/{routes,dto,handlers_test}.go` · P3, P4 · [K7]
- Covers: AC7, FR6, NFR2 · Tests: fake Reader; 422/404; DTO shape
### S16 — Poller [W2-o]
- A `P/app/poller{,_test}.go`, `P/domain/poll{,_test}.go`; M `P/app/ports.go`, `P/adapters/github/client{,_test}.go`, `P/adapters/postgres/store{,_test}.go`
- Change: P5–P7; `ListPollTargets` (all workspaces); one WARN/cycle without token; failed repo logged, cycle goes on
- Rules: go-architecture §6; go-testing §5 [K25] · Covers: AC3–AC5, FR3–FR5, EC11, NFR3
### S17 — View composition [W2-p]
- M `V/LeaderboardView.tsx`, `frontend/README.md` (route map); A `V/LeaderboardView.test.tsx`
- Covers: AC15, AC18, AC19 [K22]
### S18 — Composition root [W3-q]
- M `B/internal/config/config{,_test}.go`, `S/app.go`, `.env.example`, `docker-compose.yml`, `B/AGENTS.md`
- Change: P8; routes; poller in `listen`; AGENTS shutdown order
- Covers: FR3 · Done when: `make backend-check`
### S19 — Component: leaderboard [W4-r]
- A `S/leaderboard_test.go`, `S/testdata/contracts/leaderboard_{week,repo_30d,empty}.json` [C2][K26]
- Covers: AC7–AC11, AC13, EC8
### S20 — Component: poll [W4-r]
- A `S/pr_poll_test.go`: pages, 1 s interval, rate limit, 500, token, stop ≤ 5 s
- Covers: AC2–AC5
- Deviation (accepted, W4): test seam `options.pollTick` in `S/app.go` (+ `spec.pollTick` in `S/main_test.go`), default 0 = poller's 1-min tick; a synctest bubble cannot span the real server sockets, so poll tests shorten the tick and move `spec.now`. Poll tests start their clock at the real now (Retry-After uses the wall clock, security r2 N3)
### S21 — Contract test [W5-s]
- M `F/test/go-backend-contracts.test.ts` · Covers: AC13
### S22 — Manual [W5-t]
- Covers: AC12, AC20 [C3] · Done when: reported

## Execution
- Mode: multi-agent (user), ~60 files
- W0: a S1 ∥ b S2 ∥ c S3
- W1: d S4→S5 ∥ e S6 ∥ f S7 ∥ g S8 ∥ h S9 ∥ i S10 ∥ j S11 ∥ k S12 ∥ l S13 ∥ m S14
- W2: n S15 ∥ o S16 ∥ p S17 · W3: q S18 · W4: r S19→S20 · W5: s S21 ∥ t S22
- Lanes of a wave share no file; e/f/g build on S3 types

## Contracts & migrations
- `GET /leaderboard`: S2, S15, S19, S21 · `0002_pr_merge_state` up/down: S1

## Verification
- Gates: `make backend-check`, `make frontend-check`, end `make check` [V3] · Manual: S22
- Review risk: high — migration, goroutine + shutdown, GitHub token/rate limit, SQL p95, contract

## Open questions / assumptions (defaults planned)
- Q1 SCR1/2: Row `health`, `size` nullable
- Q2 Review = `kind='review'`; deleted agents = one snapshot agent [F3]
- Q3 Run time = `ran_at`, any status; null cost = 0 [F4]
- Q4 Open = stored status not merged/closed [F5]
- Q5 Mean health integer, findings 1 decimal; even median = mid-two mean, whole s
- Q6 Hottest/priciest/monster need value > 0
- Q7 Hot file = (repo, path); daily opened = every PR of the filter
- Q8 Attention: (c) never reviewed; (d) reviewed sha ≠ head; (e) updated ≤ now−7 d
- Q9 Size null when additions, deletions, files all 0 · Q10 new commits = reviewed sha set ≠ head
- Q11 Rate limit ends the cycle · Q12 env name (P8)

## Amendment A1 — spec D18–D23 (user, 2026-10-23)
Prototype alignment and the Settings poll interval. Supersedes the listed parts of S7, S8, S9, S15, S16, S17, S18.
- P8′ (replaces P8) Poll interval = Settings `polling_interval_min` of each workspace (1–1,440 min, default 5), read before each cycle through a consumer-side port in `P/app` implemented over the settings store; env `PR_POLL_ENABLED` (default true) is the only switch; test configs set it false.
- P12 Health/size/rank/attention/activity per spec FR10, FR13, FR15, FR16, FR21 (D20–D23); `previous_summary` and `period.previous_*` null for 30d or a previous week without rows (FR7, FR11).
- P13 Top findings: snapshot findings ordered CRITICAL > WARNING > SUGGESTION, then open > accepted > dismissed, then file, start_line; ≤ 3 per row; facts gain finding id, title, start_line and the review's agent name.
- P14 Attention population = window PRs (opened, reviewed or merged in it), current state; the store returns current-review open critical for those PRs (not only open ones).

### S23 — Backend rework [W2b-u] (after S15)
- M `L/domain/{model,rows,rank,kpis,awards,attention,files,daily}{,_test}.go`, `L/adapters/postgres/**`, `L/app/service{,_test}.go`, `L/transport/http/{dto,handlers_test}.go`
- Change: P12–P14; `Daily` → activity with granularity day|week; DTO per spec §7 (amended)
- Covers: AC6, AC8–AC11 (amended) · Done when: `go test -race ./internal/leaderboard/...`
### S24 — Frontend rework [W2b-v] (after S17)
- M `F/vendor/shared/contracts/leaderboard.ts` (§7 amended), `C/{LeaderboardTable,KpiTiles,AwardCards,NeedsAttention,HealthBySize,DailyActivity}/**`, `V/LeaderboardView.tsx`, `V/_components/Header/*`, message files
- Change: FR18, FR19, FR21, FR22 (amended): no comparison for 30d, "no change" < 0.05, 85/60 health bands + info tooltip, sort cycle + "Reset to rank", row click → PR, top findings, XS bucket, weekly activity labels, empty texts
- Covers: AC15–AC18 (amended)
### S16′ — Poller [W2b-w] (replaces S16)
- As S16 plus P8′: `IntervalSource` port (`Interval(ctx, workspaceID) (time.Duration, error)`), per-workspace due time, a 1-minute tick, interval change takes effect on the next cycle; the leaderboard spec AC3 (amended) with synctest
### S18′ — Composition root
- As S18 with `PR_POLL_ENABLED` instead of `PR_POLL_INTERVAL_SECONDS`; wires the settings-backed `IntervalSource`

## Amendment A2 — spec FR24–FR26, D24–D26 (user, 2026-10-23), after W3
### S25 — Poller diff-stat backfill [W3b-x]
- M `P/app/poller{,_test}.go`, `P/app/ports.go` (stats fetch port; reuse the PR-list backfill's GitHub call), `P/adapters/postgres/store{,_test}.go` (PRs without stats, updated ≥ now−62 d, newest first, limit 20), `S/app.go` wiring if the port needs it
- Covers: FR24, NFR3, AC21
- Deviation (accepted, W3b): stats come from a new one-request `platform/github.Client.GetPullStats` (same additions/deletions/changed_files) instead of `GetPullRequest` (≤ 34 requests/PR, would break NFR3); also edits `platform/github/{client,pulls}.go` and `S/leaderboard_wiring_test.go` (fixed clock, stats assertion)
- Also in W3b: security r1 F1 (secondary rate limit: 403/429 with Retry-After or message → pause, `StatusError.LimitReached`, `RetryAfter`) and arch r1 finding 1 (GORM builder for the four simple leaderboard store queries)
### S26 — Header compare + Hot files links [W3b-y]
- M `V/helpers{,.test}.ts` / `C/Header/*` (FR25), `C/HotFiles/*` + `E/leaderboardPanels.json` (FR26, rich tag)
- Covers: FR25, FR26, AC22

