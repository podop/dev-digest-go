# Context pack: Cost ledger
[A] `B/`=backend/; B/internal/: `SP/` spend, `L/` llm, `CI/` ci, `RV/` reviews, `EV/` evals, `IN/` intent, `BR/` brief, `ON/` onboarding, `CV/` conventions · `S/`=B/cmd/server · `MG/`=B/migrations · `F/`=frontend/src · `E/`=frontend/messages/en · UI folders under F/app/settings/[section]/_components/SettingsView/_components/

## Lane contracts
- [C1] `SP/domain`: `Kind` (8: review_run, review_intent, intent_refresh, brief, onboarding, conventions_scan, eval_case, ci_run) + `ParseKind`; `Origin` live|ci_ingest|backfill; `CostFromUSD(*float64) *Micros` (nil/NaN/Inf/<0/≥1e12 → nil; 6 dp half away from 0); `Entry` = FR2 fields + `DedupeKey` (Cost *Micros nil = unknown); `CIDedupeKey` "ci:<run>:<attempt>"; `MonthTotal{Spent, Unpriced}`; `Status.Unpriced`. `SP/app`: `Totals.MonthTotal(ctx, ws, month)` replaces `Ledger` (`NewService` shape kept); `Entries.Append(ctx, Entry) (bool, error)`; `Recorder.RecordUsage(ctx, Usage)` no error, WithoutCancel + 3 s, failure = one FR15 ERROR; `RecordCIRun(ctx, CIRun) error` in caller's tx; `ReportBackfill` one FR15 INFO
- [C2] `L/`: `Options.Recorder` (nil = off) `.RecordUsage(ctx, RecordedUsage{Charge, Provider, Model, Tokens, CostUSD})`; `Request.Charge *Charge{WorkspaceID, Kind UsageKind, RefID}` (7 kinds, no ci_run); called per answered attempt even without `OnUsage`; Charge nil → WARN. RV `LLMBinding.WorkspaceID` → peers `{review_run, RunID}`; EV `LLMBinding.{WorkspaceID, CaseID}` → `{eval_case, CaseID}`; `llmcall.Config.Charge` (cmd copies `CallerConfig.Charge`); IN `{Force ? intent_refresh : review_intent, PR}`; BR `{brief, PR}`; ON `{onboarding, repo}`; CV `{conventions_scan, scan}`
- [C3] CI: `NewRun.FromArtifact`; `RecordRun → (Recorded{AgentRunID, Stored}, error)`; ports `Ledger.RecordCIRun`, `Tx.InTx` in `IngestDeps` (nil Ledger = off); both in one InTx when Stored ∧ FromArtifact; ledger error rolls back
- [C4] `cost_ledger`: id uuid PK; workspace_id FK (no cascade); occurred_at timestamptz; kind, origin + CHECK; provider, model (NULL only backfill); tokens_in/out, cost_micros bigint ≥ 0 NULL; ref_id uuid no FK; dedupe_key UNIQUE; INDEX (workspace_id, occurred_at). `spend_month_totals`: PK (workspace_id, month date, slot 0..7), spent_micros, unpriced_count, entry_count. `cost_ledger_backfill`: 1 summary row + logged_at. Append tx: insert ON CONFLICT (dedupe_key) DO NOTHING; inserted → `+=` random slot. Month = UTC (`date_trunc` on `t AT TIME ZONE 'UTC'`). Backfill: [K8] per record, known cost, [K6] guards; ci agent_runs → ci_run, key via `ci_runs.agent_run_id`; skipped = unusable cost or brief time; totals slot 0. Down drops the 3 tables

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38` "wire-compatible with the UI"; `:90` no `down -v` (main stack); `:92` contract "together with the matching backend change"
- [K2] `B/AGENTS.md:67-68` "consumer-side ports wired in `cmd/`"; `:81` nil port = off, "never a typed-nil pointer"
- [K3] `:104` Raw only "for what the builder can't express" · `:108` up + down · `:113` secrets "never logged" · `:121` InTx → `postgres.Conn(ctx, db)`
- [K4] `B/INSIGHTS.md:36` "don't 'fix' it by summing those tables differently"
- [K5] `:37` seed costs in the real month → fixed `spec.now`
- [K6] `:71` typeof + `pg_input_is_valid` inside CASE · `:11` detoast once
- [K7] `:106` pin migration tests (`embeddedFS`)
- [K8] `SP/adapters/postgres/store.go:29-98` = parity reference
- [K9] `:38,51` golden + UI entry together; `:61` startup non-fatal
- [K10] `frontend/AGENTS.md:13-19`; `F/INSIGHTS.md` :99 strayText, :64 filter, :60 biome, :61 nested keys

## Verified facts
- [F1] `L/structured.go:152-171` reports each answered attempt before `extractJSON`; `reportUsage` :276 exits without `OnUsage`; no workspace/unit in Request (`L/llm.go:103`)
- [F2] Sites: `reviewer/adapters/llmcall/llmcall.go:76`, `IN/app/flight.go:171`, `BR/app/service.go:271`, `ON/app/service.go:241`, `CV/app/job.go:255`; runner client `cmd/devdigest-runner/main.go:94`
- [F3] refresh = `Force:true` (`IN/app/service.go:126`); shared flights (`flight.go:52`)
- [F4] cost only if float64 (`openaicompat.go:213`); mock no OnboardingTour (`mock.go:27`); seam `llmAdapters` (`S/app.go:153`)
- [F5] `S/app.go:393` llm.New, :405 spend, :271 startupDB; `cli.go:41`
- [F6] `CI/adapters/postgres/ingest.go:44`; `CI/app/ingest.go:213`; B reset deletes ci_runs
- [F7] scans double (`0001:219`), brief/tour jsonb
- [F8] `S/spend_cap_test.go:170` agent_runs fixture, `:369` broken read
- [F9] `platform.ts:123`; `SpendCapField.tsx:44-48`; `workspaceDanger.json` `dialog.spendNote`; `ResetHistoryDialog.test.tsx:49`

## Assumption details [D]
Stack (S23): project `devdigest-go-cl`, ports 3240/3241/5440; `fix-r3-ac23-assets/seed-nfr4.sql` on B's image, then the new image (`duration_ms`, health); 500k + 2M entries, totals rebuilt; 200 req GET/402/404 (`measure.sh`); S22 on that DB; assets `.sdd/review/2026-10-25-cost-ledger/`; `down -v` of that project only. C-list: `spendapp.API{Status, Admit}`, op consts, `Status.Reached()`, FR8, auto = review_run, `seedLedgerSpend`, `<N>+1`

## Mirrors
`SP/**`, `S/spend_cap_test.go`, `TestMigration0003`, `fix-r3-ac23-assets/`

## Skill map
| Step | Files | Skills | Rules |
|---|---|---|---|
| S4–S18 | `B/**/*.go` | go-architecture | ports, log once, ctx, tx |
| S5–S22 | `*_test.go` | go-testing | cmp.Diff, fakes, pgtest, synctest |
| S6, S10 | SQL | postgresql-table-design | FK index, CHECK |
| S5, S9, S18–S20 | logs | security | A09, A10 |
| S1–S3, S11 | `F/**` | react-best-practices | derive |
| UI tests | `*.test.tsx` | react-testing-library | roles |

## Verification sources
- [V1] `backend/`: `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9,11`)
- [V2] `frontend/`: `pnpm exec vitest run <fragment>` (`F/INSIGHTS.md:64`), `pnpm typecheck` (`frontend/AGENTS.md:4`)
- [V3] `Makefile:119` backend-check, `:144` frontend-check, `:149` check

## Risks, notes for reviewers
NFR3 vs pool 20 (S23) · missing Charge (AC1) · corrupt costs (A6). Arch: `L/` hook, CI tx. Security: AC18
