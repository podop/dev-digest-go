# Development Plan: Cost ledger
Plan: Cost ledger · Status: done (2026-10-25) · Spec: specs/2026-10-25-cost-ledger.md · Base: feat/go-backend-phase-1@2673765 + slice B (S0)
Packages: backend, frontend · Context pack: docs/plans/2026-10-25-cost-ledger.context.md ([A], [C1]–[C4], [D])
Execution: multi-agent, parallel lanes (default, A0)

## Goal
Each answered LLM attempt and new CI run with an artifact appends one immutable entry; a per-workspace UTC-month total, written in the same tx, feeds `GET /settings/spend` and the cap (≤ 50 ms p95). Upgrade backfill; `unpriced_count` + hint; B's dialog says the total is kept.

## Out of scope
Spec §3; slices C, D; CI runner; seed (A5); spec edits (SCR*).

## Decisions
- P1 Ledger in `SP/` (domain, app `Recorder`, adapters); stored totals replace the derived query [C1][K4]
- P2 One hook: `L/` `Options.Recorder`, called per answered attempt; call sites only set `Request.Charge`; cmd binds spend to it [C2][F1][F2][K2]
- P3 Append on the calling goroutine, detached ctx 3 s, so aborted units still record (FR5); failure = ERROR, go on (D8)
- P4 Totals striped in 8 slots per (ws, month) for NFR3; read sums ≤ 8 PK rows [C4]
- P5 CI entry in the CI run's tx; dedupe key (run id + attempt) also covers re-ingest after a B reset [C3][F6]
- P6 Backfill in the up migration; its summary row is logged once after migrate (FR15) [C4]
- P7 uuid entry ids (repo convention; AC9 greps export bytes for them)
- P8 `spendapp.API`, `Admit`, op constants unchanged (Settings C contract)

## Steps
Done when = step tests [V1]/[V2] + build/typecheck; UI keeps pseudo-messages checks [K10]. (B)/(C) = shared file.

### S0 Rebase on slice B [W0, orchestrator]
After B's commit: re-read every (B) file; `<N>` = 0004 if B added no migration, else 0005; check B reset/prune/export never name the new tables; re-check [F]/[K]; brief lanes. No code · Covers §12
### S1→S2 Contract, hint [W1-a]
M `platform.ts` (B,C): optional int ≥ 0 `unpriced_count`. M `SpendCapField{.tsx,.test.tsx}`, `E/settingsBudget.json`: ICU plural sentence, cap or not; 0/absent → none · Covers FR13, FR14, NFR8, AC16
### S3 Reset dialog copy [W1-b]
M `E/workspaceDanger.json` (B) `dialog.spendNote` → "This month's spend total is kept."; M `ResetHistoryDialog.test.tsx` (B): new text, B AC21 lists unchanged · Covers FR16, AC20
### S4 Spend domain [W1-c]
A `SP/domain/ledger{,_test}.go`; M `SP/domain/status{,_test}.go` [C1] · Covers FR2, FR3, EC11, EC21, NFR5
### S5 LLM usage hook [W1-d]
M `L/{llm,structured}.go`; A `L/recorder_test.go` [C2]: 1 per answer (re-prompt, no-choices), 0 before an answer, before validation, panic recovered, nil Charge WARN, `Answered` never retried away · Covers FR1, FR5, EC5, EC6
### S6 Migration [W1-e]
A `MG/<N>_cost_ledger.{up,down}.sql` [C4][K6]; M `migrate_test.go` (B,C) [K7]: up/down/up, same entries, no duplicates · Covers FR10, EC17, EC18, AC19 (test)
### S7 CI ingest port [W1-f]
M `CI/domain/{model,ingest,ingest_test}.go`, `CI/app/{ports,ingest,fakes_test,ingest_test}.go` [C3]: artifact 1, null cost, failed 0, duplicate 0, ledger error → not stored · Covers FR6, EC9
### S8 CI store [W1-g]
M `CI/adapters/postgres/{ingest,store_test}.go`: `RecordRun` returns the agent run id, joins a caller tx [K3] · Covers FR6
### S9 Spend app [W2-h]
M `SP/app/{ports,service,service_test}.go`; A `SP/app/recorder{,_test}.go` [C1]: Status from totals + Unpriced; Admit unchanged (order, fail closed, no-cap skip); FR15 logs · Covers FR7–FR9, FR15, D8, EC16, EC19, EC20, AC13 (log)
### S10 Spend store [W2-i]
M `SP/adapters/postgres/{store,store_test,export_test}.go`; A `reference_test.go` ([K8] query verbatim, test-only), `ledger_test.go`. Backfill: A AC9 fixture at `<N>−1` → up → totals = reference per (ws, month); sources, mirrors, NULL, malformed, bounds, CI dedupe. Append: atomic, dedupe, 50+10 concurrent, month split · Covers FR9, FR10, EC12, NFR5, AC10, AC13
### S11 Spend wire [W3-q, after S18: `-update` needs a building `main`]
M `SP/transport/http/{dto,handlers_test}.go`; regenerate `S/testdata/contracts/settings_spend.json`, then M `F/test/go-backend-contracts.test.ts` (B,C) [K9] · Covers FR13, NFR6, AC15
### S12 llmcall [W2-k]
M `reviewer/adapters/llmcall/llmcall{,_test}.go`: `Config.Charge` → request · Covers FR1
### S13 Reviews [W2-l]
M `RV/app/ports.go`, `RV/app/executor/run.go` (C), `RV/adapters/peers/providers{,_test}.go` · Covers FR1
### S14 Evals [W2-m]
M `EV/app/executor/{api,case}.go`, `EV/adapters/peers/providers{,_test}.go` · Covers FR1
### S15 Intent, conventions [W2-n]
M `IN/app/{flight,resolve_test}.go`, `CV/app/{job,job_test}.go` [F3] · Covers FR1
### S16 Brief, onboarding [W2-o]
M `BR/app/{service,service_test}.go`, `ON/app/{service,service_test}.go` · Covers FR1
### S17 Settings A tests on the ledger [W2-p]
M `S/{spend_cap_test,spend_wiring_test}.go`: `seedLedgerSpend` replaces agent_runs costs; A AC9 → feature rows don't move spend; read error = renamed totals [F8][K5] · Covers FR8, FR11, AC11
### S18 Composition root [W3-q]
A `S/ledger{,_test}.go` (recorder + CI adapters, kind map, backfill report); M `S/app.go` (B,C): recorder before `llm.New`, ci Ledger + Tx, report after migrate [K9]; `S/cli.go`, `B/AGENTS.md` (B,C) · Covers FR6, FR10, FR15 · Done when `make backend-check`
### S19 Call tests [W4-r]
A `S/ledger_calls_test.go` (`lc…`): AC1 (A2), AC2, AC3 (A4), AC14, AC18 · Covers FR1–FR4, EC1–EC3, NFR4
### S20 Failure tests [W4-s]
A `S/ledger_failures_test.go` (`lf…`): AC4, AC5, AC7 (scripted clock), AC11, AC12 · Covers FR5, FR15, EC4–EC7, EC11, EC16, EC20
### S21 Lifecycle tests [W4-t]
A `S/ledger_lifecycle_test.go` (`ll…`): AC6, AC8, AC9 · Covers FR4, FR6, FR12, EC8–EC10, EC13–EC15
### S22 Load harness [W4-u]
A `SP/adapters/postgres/load_test.go` (`-ledger.load`, `LEDGER_LOAD_DSN`): 50 concurrent appends, p95 · Covers NFR3
### S23 Manual [W5-v]
Throwaway stack [D]: AC19 upgrade from B's image; AC17 at NFR1 volume + S22 · Covers NFR1–NFR3, NFR7, AC17, AC19 · Done when reported

## Execution
- Mode: multi-agent (~55 files, 2 packages, migration, contract). Single-agent: waves in order
- W0: S0 · W1: a S1→S2 ∥ b S3 ∥ c S4 ∥ d S5 ∥ e S6 ∥ f S7 ∥ g S8
- W2: h S9 ∥ i S10 ∥ k S12 ∥ l S13 ∥ m S14 ∥ n S15 ∥ o S16 ∥ p S17 · W3: q S18→S11 · W4: r S19 ∥ s S20 ∥ t S21 ∥ u S22 · W5: v S23
- Lanes share no file; code to [C1]–[C4]; `main` test lanes prefix helpers
- Commit W1–W3 together [K1]; W4, W5 each

## Contracts & migrations
`platform.ts` S1 ↔ S11. `<N>_cost_ledger` (S6): 3 tables + backfill; down drops them only. C takes `<N>+1`.

## Settings C relies on
[D] C-list; (C) marks the shared files.

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `make check` → `architecture-reviewer` (project) ∥ security (read-only agent + `engineering-paved-path:security`) ∥ `sdd-engineering:plan-verifier`
- Review risk: high — backfill, concurrent totals, every LLM path, spend gate, contract

## Spec change requests (for spec-creator)
- SCR1 AC13 "equals" vs FR3 per-entry rounding: costs > 6 dp may differ ≤ 0.5 µ$/entry → allow 1 µ$ each (A1)
- SCR2 AC1 onboarding on the mock: no fixture [F4] → scripted adapter at $0.00042 (A2)
- SCR3 FR2 reference per kind undefined → A3
- SCR4 B FR19, §12, D16 decision (D14) when this lands — spec edit, no step
- SCR5 AC3 NaN/"0.1": JSON has no NaN; a string cost falls to the price book [F4] → A4

## Open questions / assumptions (defaults planned)
- A0 multi-agent · A1 per-entry round half away from zero · A2 scripted onboarding
- A3 ref: run, PR, PR, PR, repo, scan, eval case, CI agent run · A4 scripted −1/NaN; "0.1" via OpenRouter stub, unpriced model
- A5 seed demo costs get no entries (fresh DB: $0 seed month) · A6 backfill skips negative/NaN/≥ 1e12 as malformed
- A7 shared intent flight = one entry, starter's kind · A8 8 slots, S23 retunes · A9 nil CI Ledger = off; prod wires it (S18)
- A10 append timeout 3 s · A11 backfilled CI dedupe via `ci_runs.agent_run_id`
