# Development Plan: Settings B — Workspace
Plan: Settings B · Status: done (2026-10-25) · Spec: specs/2026-10-23-settings-b-workspace.md · Base: feat/go-backend-phase-1@efaa348
Packages: backend, frontend · Context pack: docs/plans/2026-10-25-settings-b-workspace.context.md
Execution: multi-agent, parallel lanes (default, A0)

## Goal
Settings › Workspace: rename (shown in the switcher), read-only data dir, `retention_days` + trace prune at start and every 24 h, six counts, JSON export (`kind: workspace`), typed-confirm reset of run history (409 while a run is active).

## Out of scope
Spec §3; slices C, D; migrations (SCR3); `vendor/ui` edits.

## Decisions
- P1 New context `W/` owns every `/workspace*` route; GET moves out of `R/` (repos via a cmd port) [K2][K3][F1]
- P2 Display name = Settings row `workspace_name` (system user); `workspaces.name` stays `default` (D7) [F2][F3]
- P3 Reset = one tx per [C2]: table lock makes the run check and the deletes atomic (EC5); explicit deletes give the counts [K4][K6][F5]
- P4 Prune per [C2], goroutine in `listen` after the reaps, waited like the poller [K8][F4]
- P5 Export: `BuildBundle` pure; headers `Content-Disposition`, `Access-Control-Expose-Headers: Content-Disposition`, `Cache-Control: no-store` [F6]
- P6 Rules in `W/domain`; 422/409 as `apperr` kinds [K5]
- P7 UI: namespace per lane [C3]; hooks/keys only [K12]
- P8 New `contracts/workspace.ts` + zod-free `constants/workspace.ts`; `platform.ts` gets `retention_days`; bundle additive [K10]

## Steps
Done when = step tests [V1]/[V2] + build/typecheck green; each UI folder has a `*.test.tsx` with a pseudo-messages check [K13].

### S1 Contracts + hooks [W0-a]
A `F/vendor/shared/{contracts,constants}/workspace.ts`, `F/lib/hooks/workspace{,.test}.ts(x)`, `F/lib/download.ts`; M `platform.ts`, `productionize.ts`, `F/vendor/shared/index.ts`, `F/lib/{types,api}.ts`, `F/lib/hooks/{keys,index}.ts` · §7 shapes; `api.download` returns blob + server file name; an old bundle still parses · Covers FR21
### S2→S3 Go domain, ports [W0-b]
A `W/domain/{name,retention,export,bundle,model}{,_test}.go`, `W/app/ports.go`, `W/adapters/postgres/store.go` (type) per [C1] · Covers FR1, EC1, EC2, FR7, EC8, EC9, EC13, FR14, FR15
### S4 Settings keys [W0-c]
M `ST/transport/http/validate{,_test}.go`: `retention_days` 30|60|90|180|365|null; `workspace_name` → 422 (A1) · Covers FR6, AC6
### S5 Store reads [W1-d]
A `W/adapters/postgres/{reads,reads_test}.go`: Stats, ExportData, WorkspaceIDs [C2] · Covers FR11, AC11, FR13, EC12, EC14
### S6 Store writes [W1-e]
A `W/adapters/postgres/{history,prune}{,_test}.go` [C2]; pgtest: every FR16 kind, kept tables, 409 deletes nothing, second reset zeros, finding-built case reads, both barriers [K11] · Covers FR16, FR17, EC4–EC7, EC11, AC16, AC17, AC18, AC7
### S7 Service [W1-f]
A `W/app/service{,_test}.go`: Info, Rename, Stats, Export (file name, version, UTC ms), Reset (exact confirm else 422 `confirmation_mismatch`; 409 `run_active` with N; one INFO line, counts only) · Covers FR2, FR3, FR13–FR17, NFR7
### S8 Pruner [W1-g]
A `W/app/pruner{,_test}.go`: at Run, then 24 h later (A7); re-reads retention; logs per FR9; ≤ 5 s stop; synctest [K14] · Covers FR8, FR9, AC8
### S9 Transport [W1-h]
A `W/transport/http/{routes,dto,handlers_test}.go`; M `R/transport/http/handler{,_test}.go` (drop GET, move tests) · §7 bodies via [K7], P5 · Covers FR1, FR3, FR15, AC2, NFR2
### S10 Name + data dir [W1-i]
A `WS/_components/{WorkspaceNameField,DataDirField}/**`, `E/settingsWorkspace.json` · Enter/blur saves when changed + valid (runes, A10), "Saved", inline error; mono, copy, D18 hint · Covers FR4, AC4
### S11 Retention [W1-j]
A `WS/_components/RetentionField/**`, `E/workspaceRetention.json` · [K15]; unset 90; Keep forever → null; failure restores; §9 invalid note · Covers FR10, AC10
### S12 Stat tiles [W1-k]
A `WS/_components/WorkspaceStats/**`, `E/workspaceStats.json` · FR12 order, no digits loading, "—" + Retry · Covers FR12, AC12, EC15
### S13 Danger zone + dialog [W1-l]
A `WS/_components/{DangerZone,ResetHistoryDialog}/**`, `E/workspaceDanger.json` · export disabled in flight; dialog per FR19, focus to input, text reason when disabled, 409/422 inside, 200 → toast + refetch (A8, A9) · Covers FR18–FR20, AC20, AC21, NFR5
### S14 Switcher name [W1-m]
M `F/components/app-shell/{helpers,helpers.test}.ts`, `hooks/useShellContext.ts`; A `hooks/useShellContext.test.tsx` · `<label> · <name>`, A2 cut; name error → no name · Covers FR5, AC5, EC16
### S15 Composition root [W2-n]
A `S/workspace.go` (settings + repos adapters); M `S/app.go` (wire, routes, pruner in `listen`, seam `pruneInterval`), `B/AGENTS.md` · Covers FR8, NFR4 · Done when `make backend-check`
### S16 Section + panel [W2-o]
A `WS/{SettingsWorkspace.tsx,index.ts,styles.ts,SettingsWorkspace.test.tsx}`; M `SV/SettingsView{,.test}.tsx`, `E/settingsWorkspace.json` (A11) · Covers FR4, AC22
### S17 Component tests, goldens [W3-p]
A `S/settings_b_test.go`, `S/testdata/contracts/workspace_{rename,stats,export,reset,reset_run_active}.json`; M `workspace.json`, `S/flows_test.go` (dataDir [F11]); then `F/test/go-backend-contracts.test.ts` [K16] · Covers AC1–AC3, AC6, AC9, AC11, AC13, AC15, AC17, AC19
### S18 History component tests [W3-q]
A `S/workspace_history_test.go`: export leak (4 keys, unknown key, clone path, UUIDs; A12), reset + `secrets.json` byte-identical, startup prune, 30/null · Covers AC7, AC8, AC14, AC16, NFR1
### S19 Manual [W4-r]
Throwaway stack [C4][K1] · Covers AC23, AC24 · Done when reported

## Execution
- Mode: multi-agent (~70 files, 2 packages, contract)
- W0: a S1 ∥ b S2→S3 ∥ c S4
- W1: d S5 ∥ e S6 ∥ f S7 ∥ g S8 ∥ h S9 ∥ i S10 ∥ j S11 ∥ k S12 ∥ l S13 ∥ m S14
- W2: n S15 ∥ o S16 · W3: p S17 ∥ q S18 · W4: r S19
- Lanes share no file; commit W0–W2 together [K10], then W3, W4
- Shared with slice C (rebases on B): `ST/transport/http/validate{,_test}.go`, `platform.ts`, `F/vendor/shared/index.ts`, `F/lib/{types.ts,hooks/keys.ts,hooks/index.ts}`, `SV/SettingsView{,.test}.tsx`, `S/app.go`, `B/AGENTS.md`, `S/flows_test.go`, `F/test/go-backend-contracts.test.ts`. For C: reset deletes all `agent_runs` sources (`auto` too), so no RESTRICT FK to `agent_runs`; new exportable keys join `ExportableKeys`

## Contracts & migrations
S1 ↔ S9, S15; goldens S17 + UI contract test. No migration (SCR3).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `make check` → `architecture-reviewer` (project) ∥ security (read-only agent + `engineering-paved-path:security`) ∥ `sdd-engineering:plan-verifier`
- Review risk: high — destructive tx + table lock, background goroutine, export leaks, contracts

## Spec change requests
- SCR1 D7 allows "column or setting" but §3 forbids migrations → setting (A1)
- SCR2 FR5/EC16/AC5 tooltip impossible: vendored subtitle takes a string, no ellipsis/title [F7] → A2
- SCR3 NFR3 reset: unindexed `ci_runs.agent_run_id` FK [F5] → measure in S19; over 30 s → migration 0004 + spec change (C → 0005)
- SCR4 AC14 "no finding text" vs FR13 eval cases built from findings → A12
- SCR5 FR20 "Memory data": no memory query in the UI [F10]

## Open questions / assumptions (defaults planned)
- A0 multi-agent · A1 `workspace_name` row; PUT /settings 422 on that key; bad stored value → `default`
- A2 name cut at 32 code points + "…", no tooltip · A3 CI runs = ws installations ∪ linked to ws runs
- A4 run starts wait during a reset; lock timeout 10 s → 500, nothing deleted · A5 409 text literal
- A6 export skips eval cases with no owner; collisions take the next free " (n)"
- A7 next prune 24 h after the previous one ends; INFO adds `workspace_id`
- A8 dialog sets `aria-labelledby` on Modal's role=dialog [F8] · A9 reset invalidates workspace, pulls, repos, runs, leaderboard, performance, evals, multi-agent, ci, spend
- A10 1–80 code points on both sides · A11 prototype copy [D] · A12 AC14 fixture
- Volume: prune/stats use `agent_runs_ws_ran_idx` (0003); likely index need: SCR3
