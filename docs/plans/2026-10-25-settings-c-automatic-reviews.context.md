# Context pack: Settings C
[A] `B/`=backend/; under B/internal: `AR/`=autoreview, `ST/`=settings, `PL/`=pulls, `RV/`=reviews, `PF/`=performance, `SP/`=spend; `S/`=B/cmd/server · `F/`=frontend/src · `SV/`=F/app/settings/[section]/_components/SettingsView · `AC/`=SV/_components/SettingsAutoReviews · `E/`=frontend/messages/en

## Lane contracts
- [C1] `AR/domain`: `Setting{Enabled, AgentIDs, OnNewPR, OnNewCommits, EnabledAt}.Armed()`; `Event`, `Outcome` (§8); `Candidate{PRID, Repo, Number, HeadSHA, LastReviewedSHA, HasAnyRecord, Running}`; `Decide(c, s, agents, budget, rateLeft) → Defer|AskBudget|Record(o)|Start`; `RateLeft(lastHour, thisEval)` (5, 20, 60 min); `State(Facts)` (FR12); `Status{State, LastPollAt, PRsTracked, PollIntervalMin}`. `AR/app` ports: `Settings`, `Interval` (clamped), `Store{Candidates, EnabledAgents, StartedSince, Claim→bool, Finish, StatusFacts}`, `Starter.StartAuto(…, ids) (runs, err)`, `Budget.Admit`, `Caps.CapReached`, `Tokens.HasGitHubToken`; Deps `PollEnabled, Now, Log`; API `AfterCycle(ctx, ws)`, `Status`. `PL/app.CycleObserver{AfterCycle}` = `PollerDeps.Observer`
- [C2] `AutomaticReviews` §7.1, `AutomaticReviewsInput` (bool | partial, used by `SettingsUpdate`), `AutomaticReviewsStatus` §7.2, `auto` in trace + RunHistoryItem, zod-free `AUTO_REVIEW_MAX_AGENTS=20`. Leaves take `{value, disabled, onChange}` (chips: `agents, selected`); AutoReviewStatus has its own hook + Retry
- [C3] `auto_review_records`: id uuid PK; workspace_id, pr_id NOT NULL FK CASCADE; head_sha, event, outcome text NOT NULL + CHECK; runs_started int ≥ 0; decided_at timestamptz (trigger clock); UNIQUE (pr_id, head_sha); INDEX (workspace_id, decided_at). Candidates in one statement (FR4 + has-record + running), ORDER BY updated_at NULLS LAST, id
- [C4] Shared with B: platform.ts, constants/settings.ts, lib/types.ts, hooks/{keys,core,settings.test}, validate{,_test}.go, ST/app/settings{,_test}.go, SettingsView{,.test}.tsx, settingsShell.json (read), S/app.go, B/AGENTS.md, go-backend-contracts.test.ts, migrations/

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38-46` "wire-compatible"; JSON only, foreign `Origin` 403, `ALLOWED_HOSTS`; `:90` no `down -v`
- [K2] `B/AGENTS.md:60-61` `internal/<context>/{domain,app,adapters/<tech>,transport/http}` · [K3] `:67-68` "only through consumer-side ports wired in `cmd/`"
- [K4] `:94-95` `Raw` only where the builder can't · [K5] `:97-98` "only `httpx` maps kinds to status" · [K6] `:99-100` up **and** down, never edit one
- [K8] `:111-113` `postgres.Conn(ctx, db)` inside `InTx` · [K9] `:81-82` "never a typed-nil pointer"
- [K10] `B/INSIGHTS.md:36` one row per PR/repo: "the cap under-counts repeated generations"
- [K11] `CLAUDE.md:92` contracts change "together with the matching backend change"
- [K13] `frontend/AGENTS.md:13-19,33` pages thin, hooks, `messages/en`, no vendor/ui edits; `F/INSIGHTS.md` :99 strayText, :64 vitest filter, :60 biome per dir, :61 full nested keys
- [K14] `F/INSIGHTS.md:47` global mutation toast; :29 `createAppQueryClient`; :22 toast = role=status
- [K15] `B/INSIGHTS.md:38` golden + frontend entry together; :51 `-update` after the package · [K17] :23 a goroutine panic "kills the whole server"
- [K16] `B/INSIGHTS.md:13` poller component setup; :108 addRepos in one tx, manual poll = wall clock; :37 fixed `spec.now`
- [K18] `F/INSIGHTS.md:12` fake timers for `refetchInterval`; :16 mockFetch; :102 factory responses
- [K19] `B/INSIGHTS.md:106` pin migration tests · [K20] :75 synctest: Sleep before Wait · [K21] :46 blocked unique writer in pg_locks; :62 SQLState 23505
- [K22] `F/INSIGHTS.md:40` Select + `pickOption`; :69 FormField gives no name · [K23] :21 after a parallel merge read PANELS item by item

## Verified facts
- [F1] `PL/app/poller.go:148-182`; Run on stopCtx `S/app.go:696-698`
- [F2] `ST/app/settings.go:153-172` no defaults; `validate.go:40,74-75` bool
- [F3] `RV/app/start.go:14-85` (Admit :35); `runs.go:32` local; `trace.go:179-187`; `tracedoc.go:452-457` local|ci; `executor.go:107-119` refuses after stop
- [F4] `PF/adapters/postgres/store.go:35`, `PF/domain/metrics.go:53`; CI list = `ci_runs`
- [F6] `0001_baseline.up.sql:61` CHECK; `:1177` running index; `:1756` cascade
- [F7] `settings_get.json` 4 keys, `go-backend-contracts.test.ts:606-613`
- [F8] `S/app.go:387-396` settings/spend; `:612-625` poller if enabled
- [F9] `SettingsView.tsx:18-23` PANELS; test `:41-46`
- [F10] vendored `Toggle`: no aria prop → wrap in `<label>`
- [F11] `RunHistory/styles.ts:21` local|ci; `E/agentPerformance.json:144-147`

## Mirrors
`S/{poll,spend,pr_poll_test,spend_cap_test}.go`, `TestMigration0003`, `SettingsGithub/**`

## Skill map (paved-path = `engineering-paved-path:<name>`)
| Step | Files | Skills | Rules |
|---|---|---|---|
| S3–S10,S16 | `B/**/*.go` | go-architecture | §1 ports, §5 errors, §6 ctx |
| S2–S19 | `*_test.go` | go-testing | tables, cmp.Diff, fakes, synctest |
| S2,S9 | SQL | postgresql-table-design | FK index, CHECK |
| S4,S8,S10 | input, logs | security | A09 logs, A10 fail closed |
| S1,S11–S15 | `F/**`, `E/*` | react-best-practices, typescript-expert | derive, no `any` |
| UI tests | `*.test.tsx` | react-testing-library | userEvent, roles |

## Assumptions [D]
A2 cap asked at the first candidate reaching (d) · A4 claim `started`/0 → runs n or `failed` · A8 "Expected object, received <t>", "Invalid uuid", "Array must contain at most 20 element(s)" · A9 no cap → not paused, else `Status().Reached()` · A14 `-autoreview.load` · SCR1 B exports no agent_ids/enabled_at · Cost: ≤ 20 starts/h × 20 agents; each head re-derives intent

## Verification sources
- [V1] `backend/`: `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9,11`)
- [V2] `frontend/`: `pnpm exec vitest run <fragment>` (`F/INSIGHTS.md:64,67`), `pnpm typecheck` (`frontend/AGENTS.md:4`)
- [V3] `Makefile:119` backend-check, `:144` frontend-check, `:149` check

## Risks, notes for reviewers
Spend overshoot (user); A5; A11; NFR1 inserts → batch skips. Arch: ports, observer. Security: NFR3 logs, no secret in status
