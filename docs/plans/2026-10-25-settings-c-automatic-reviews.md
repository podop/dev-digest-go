# Development Plan: Settings C — Automatic Reviews
Plan: Settings C · Status: done (2026-10-25) · Spec: specs/2026-10-23-settings-c-automatic-reviews.md · Base: feat/go-backend-phase-1@efaa348 + slice B (S0)
Packages: backend, frontend · Context pack: docs/plans/2026-10-25-settings-c-automatic-reviews.context.md ([A] aliases, [C1]–[C4], [D])
Execution: multi-agent, parallel lanes (default, A0)

## Goal
`automatic_reviews` becomes an object; after each background-poll cycle a trigger starts the chosen agents once per (PR, head SHA) as source `auto`, within the spend cap and 5/evaluation, 20/hour; status route; Settings › Automatic Reviews.

## Out of scope
Spec §3; slices B, D (except [C4] files); STATUS follow-ups N1–N6, spend ledger (D26); `B/internal/leaderboard/**`.

## Decisions
- P1 New context `AR/` (domain, app, adapters/postgres, transport/http); reaches settings, reviews, spend, secrets, pulls only via consumer-side ports bound in `S/autoreview.go` [K2][K3]
- P2 Settings owns the value: `ST/app` reads bool|object|missing as the full object (FR1), merges PUT patches + `enabled_at` with its clock (FR2); `validate.go` checks/normalises the patch [K5][F2]
- P3 Poller: optional `CycleObserver` port (nil = off), sync at the end of a cycle with ≥ 1 repo polled (A3) [F1]
- P4 Steps a–e are pure domain (`AR/domain.Decide`); app = loop, claim, start, log [C1]
- P5 At-most-once = unique (pr_id, head_sha) + claim `INSERT … ON CONFLICT DO NOTHING RETURNING` before Start, then update (A4) [C3][K8]
- P6 Start = `reviewsapp.Start`, `StartRequest.Source="auto"` → `agent_runs.source` and trace `config.source` [F3]
- P7 Budget: own port, op `auto_review_start`, once per evaluation; Start's 402 → `skipped_spend_cap` (A2) [K9]
- P8 Status precedence in domain; cap read without the Admit log (A9); `poll_disabled` = `!cfg.PRPollEnabled`
- P9 UI derives controls from the settings query + pending mutation variables; errors via the global toast [K14]
- P10 An evaluation recovers its own panic and logs it [K17]

## Steps
Done when = the step's own tests via [V1]/[V2] + build/typecheck; each UI folder has `*.test.tsx` with a pseudo-messages check (AC21) [K13]. (B) = shared with slice B.

### S0 Rebase on slice B [W0, orchestrator]
Once B is on the branch: re-read B's version of every [C4] file, set `<NNNN>` (0004 if B adds no migration, else 0005), re-check [F] refs, brief lanes. No code · Covers spec §12
### S1 Contracts + hooks [W1-a]
M `F/vendor/shared/contracts/{platform,trace,observability}.ts`, `F/vendor/shared/constants/settings.ts`, `F/lib/types.ts`, `F/lib/hooks/{keys,core}.ts`, `F/lib/hooks/settings.test.tsx` (B: all but trace/observability) [C2][K18] · Covers FR11, FR17, NFR6
### S2 Migration [W1-b]
A `B/migrations/<NNNN>_auto_review_records.{up,down}.sql`; M `B/internal/platform/postgres/migrate/migrate_test.go` [C3][K6][K19] · Covers FR10, AC4
### S3 Domain + app contract [W1-c]
A `AR/domain/{setting,decide,status}{,_test}.go`, `AR/app/{api,ports}.go` [C1] · Covers FR4–FR6, FR9, FR12, EC3, EC6, EC9
### S4 Settings value [W1-d]
A `ST/app/automatic_reviews{,_test}.go`; M `ST/app/settings{,_test}.go`, `ST/transport/http/validate{,_test}.go` (B) · Covers FR1, FR2, AC1–AC3, EC15, EC16
### S5 Source `auto` in reviews [W1-e]
M `RV/domain/{model,trace}.go`, `RV/app/{api,start}.go`, `RV/app/executor/{run,ending}.go`, `RV/adapters/postgres/{runs,tracedoc}.go` + tests [F3] · Covers FR7, FR11, AC12
### S6 Performance counts `auto` [W1-f]
M `PF/domain/{model,metrics}.go`, `PF/adapters/postgres/store.go` + tests [F4] · Covers FR11, AC12
### S7 Poller hook [W1-g]
M `PL/app/{poller,ports,poller_test}.go` · Covers FR3, EC5, NFR5, AC11
### S8 Use cases [W2-h]
A `AR/app/{service,trigger,status}.go` + `fakes_test.go`, `trigger_test.go`, `status_test.go`; M `SP/app/api.go` (op const) [K20] · Covers FR3–FR9, FR12, NFR2–NFR5, EC7–EC14, EC17, AC6–AC8, AC10, AC11, AC14
### S9 Store [W2-i]
A `AR/adapters/postgres/{store,rows,store_test}.go` [C3][K4][K8][K21] · Covers FR4, FR8, FR9, NFR1, NFR4, AC4, AC9
### S10 Status route [W2-j]
A `AR/transport/http/{routes,dto,routes_test}.go` · Covers FR12, NFR7, AC13, AC15
### S11 Run source label [W2-k]
M `F/app/agents/[id]/…/StatsTab/_components/RunHistory/{RunHistory.tsx,styles.ts}`, `E/agentPerformance.json`; A `RunHistory.test.tsx` [F11] · Covers FR19, AC21
### S12 Agent chips [W2-l]
A `AC/_components/AgentChips/**`, `E/autoReviewAgents.json` [C2] · Covers FR15, AC18, EC10, EC19
### S13 Status line [W2-m]
A `AC/_components/AutoReviewStatus/**`, `E/autoReviewStatus.json` [K18] · Covers FR17, AC20, EC18
### S14 Controls [W2-n]
A `AC/_components/{AutoReviewToggle,IntervalSelect,TriggerConditions}/**`, `E/autoReviewControls.json` [F10][K22] · Covers FR13, FR14, FR16, AC17, AC19
### S15 Section + shell [W3-o]
A `AC/{SettingsAutoReviews,helpers}{.tsx|.ts,.test.*}`, `AC/{constants,styles,index}.ts`, `E/automaticReviews.json`; M `SV/SettingsView{,.test}.tsx` (B) [F9][K23] · Covers FR13, FR18, FR20, AC17, AC19, AC21, NFR6
### S16 Composition root [W3-p]
A `S/autoreview{,_test}.go`; M `S/app.go` (B), `B/AGENTS.md` (B) [F8] · Covers FR3, FR7, FR12 · Done when `make backend-check`
### S17 Trigger component tests [W4-q]
A `S/autoreview_flow_test.go` [K16] · Covers AC5–AC8, AC10, AC11, AC14
### S18 Settings/status/source tests + contract [W4-r]
A `S/settings_c_test.go`, golden `S/testdata/contracts/automatic_reviews_status.json`; regenerate `settings_{get,put}.json`; then M `F/test/go-backend-contracts.test.ts` (B) [K15][F7] · Covers AC1–AC3, AC12, AC13, AC15
### S19 Two triggers, one DB [W4-s]
A `AR/adapters/postgres/trigger_integration_test.go` [K21] · Covers AC9, EC7, EC8, NFR4
### S20 Manual [W5-t]
AC16 (A14), AC22 on a throwaway stack [K1] · Done when reported

## Execution
- Mode: multi-agent (~70 files, 2 packages, contract + migration). Single-agent: waves in order
- W0: S0 · W1: a S1 ∥ b S2 ∥ c S3 ∥ d S4 ∥ e S5 ∥ f S6 ∥ g S7
- W2: h S8 ∥ i S9 ∥ j S10 ∥ k S11 ∥ l S12 ∥ m S13 ∥ n S14 · W3: o S15 ∥ p S16 · W4: q S17 ∥ r S18 ∥ s S19 · W5: t S20
- Lanes share no file; Go lanes use [C1]/[C3], UI lanes S1 + [C2]; S17/S18 share package `main`: prefix new test helpers (`ar…`/`sc…`)
- Commit W1–W3 as one change set [K11]; W4, W5 each

## Contracts & migrations
`platform.ts`, `trace.ts`, `observability.ts` S1 ↔ S4–S6, S10, S16; goldens S18. Migration `<NNNN>` (S2): new table; `agent_runs_source_chk` gains `auto`; down drops the table and turns `auto` runs into `local` (lossy).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `make check` → `architecture-reviewer` (project) ∥ security (read-only general-purpose agent + `engineering-paved-path:security`) ∥ `sdd-engineering:plan-verifier`
- Review risk: high — unattended LLM spend (cap at start only, under-count [K10]), at-most-once under concurrency, shutdown, `agent_runs` migration, contracts

## Open questions / assumptions (defaults planned; details [D])
- A0 multi-agent · A1 migration number (S0) · A2 one budget answer per evaluation; Start 402 → skipped_spend_cap
- A3 "polled" = pages + MarkPolled ok · A4 claim → start → update on a detached 3 s ctx · A5 serialised per process
- A6 NULL updated_at last · A7 deselect a disabled agent · A8 422 texts · A9 status cap read · A10 PUT last write wins
- A11 batch refused at shutdown → record `started`, runs reaped · A12 refetch while mounted · A13 "ago" buckets · A14 AC16 flag-gated test
- SCR1 B FR14 exports agent UUIDs · SCR2 FR6 d-before-e · SCR3 FR7 vs Start's 402 · Cost: see [D] (user)
