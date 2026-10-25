# Development Plan: Settings D — Integrations
Plan: Settings D · Status: approved (user 2026-10-25: CR1–CR3 accepted, spec D15–D18; migration 0008; starts after slice C) · Spec: specs/2026-10-23-settings-integrations.md · Base: feat/go-backend-phase-1@d653f2c + C W4 (S0)
Packages: backend, frontend · Context pack: docs/plans/2026-10-25-settings-d-integrations.context.md ([A], [C1]–[C5], [D])
Execution: multi-agent, parallel lanes (default, A0)

## Goal
Settings › Integrations: GitHub Action card + install dialog, plugin `.zip` export, previewed import (installed disabled), enable/disable/remove; ownership (0008) keeps a disabled plugin's agents and skills out of new runs.

## Out of scope
Spec §3; custom providers, GitLab A–C (extend D later, A14); C W4 files; `vendor/ui`. No LLM/GitHub call added (ledger guard untouched).

## Decisions
- P1 New context `PG/`; settings + workspace name via cmd ports in `S/plugins.go` [K2][K3]
- P2 Install/remove = one tx in `PG/adapters/postgres`, batched SQL over agents, skills, their v1 versions, links, eval_cases, memory (B reset precedent [K4]); v1 read back via their APIs (S24) [K5][K6]
- P3 Bundle types + archive reader in `PG/domain` (stdlib, mirrors `SK/domain/zipimport.go`, no other domain imported); copied limits get a drift test in `S/` [K7][F6]
- P4 Ownership = nullable `plugin_id` (FK NO ACTION) on agents, skills, memory; remove deletes children first [C2][K8]
- P5 Runnable = own `enabled` AND (no plugin OR plugin enabled): SQL in each run read, Go in CI peers [C3]
- P6 `httpx.Options.RawBodies` (route → media → limit): import zip ≤ 5 MiB, json ≤ 20 MiB; text/plain 415 there only [K9][F4]
- P7 Last export = Settings row `plugin_last_export` (cmd adapter, like `workspace_name`); refused by PUT /settings; NOT in `ExportableKeys` [F3][K10]
- P8 `plugin` read by correlated subquery (no FOR UPDATE over an outer join); on every agent/skill DTO; contract `.nullish()` (SCR2) [F5]
- P9 UI namespaces + hooks per [C4]; file transfer only via `api.ts` [K11]

## Steps
Done when = step tests via [V1]/[V2] + build/typecheck green; each UI folder has `*.test.tsx` with a pseudo-messages check [K12]. `+t` = with tests.

### S0 Rebase on C W4 [W0, orchestrator]
After C W4 commits: re-read [C5] files, confirm `0008` free, re-check [F], brief lanes · Covers spec §12
### S1 Contracts + hooks [W1-a]
M `F/vendor/shared/contracts/{productionize,knowledge,eval-ci}.ts`, `F/vendor/shared/index.ts`, `F/lib/{api,types}.ts`, `F/lib/hooks/{ci,keys,index}.ts`; A `F/vendor/shared/constants/plugins.ts`, `F/lib/plugins`+t, `F/lib/hooks/plugins`+t [C4] · Covers FR1, FR4–FR6, FR10–FR12, FR14
### S2 Migration 0008 [W1-b]
A `B/migrations/0008_plugin_ownership.{up,down}.sql`; M `migrate/migrate_test.go` [C2][K13] · Covers NFR6, AC24
### S3 Plugin domain + app contract [W1-c]
A `PG/domain/{bundle,archive,validate,preview,export,names}`+t, `PG/app/{api,ports}.go` [C1] · Covers FR4, FR6, FR8, FR9, EC1–EC4, EC10, NFR1, AC5, AC9, AC11
### S4 Raw bodies [W1-d]
M `HX/{body,echo}.go`; A `HX/rawbody_test.go` · P6, `ReadRawBody`; other routes unchanged · Covers NFR3, AC10
### S5 Settings key [W1-e]
M `ST/transport/http/validate`+t: `plugin_last_export` → 422 · Covers FR5
### S6 Agents owner [W2-f]
M `AG/domain/agent.go`, `AG/adapters/postgres/store`+t, `AG/transport/http/handler`+t · P8 · Covers FR14, AC17
### S7 Skills owner [W2-g]
M `SK/domain/skill.go`, `SK/adapters/postgres/store`+t, `SK/transport/http/handler`+t · P8 · Covers FR14, AC17
### S8 Plugin store [W2-h]
A `PG/adapters/postgres/{store,rows,install,remove,reads}`+t [C2][K14][K15] · Covers FR7, FR8, FR10–FR12, EC5, EC6, EC11, AC8, AC12, AC16
### S9 Plugin service [W2-i]
A `PG/app/{service,export,install}.go`, `{fakes,service}_test.go` · selection, 404 ids, `nothing_to_export`, 20 MiB, file name, last export, exact confirm; logs id/name/counts only · Covers FR4–FR7, FR9–FR12, NFR2, AC4, AC7, AC15
### S10 Plugin transport [W2-j]
A `PG/transport/http/{routes,dto,routes_test}.go` · download headers [K16]; buckets 10/min · Covers FR4, FR6, FR10–FR12, NFR3, AC10, AC15
### S11 CI installations by repo [W2-k]
A `CI/domain/repos`+t, `CI/app/installations`+t; M `CI/app/api.go`, `CI/adapters/postgres/{lists,store_test}.go`, `CI/transport/http/{routes,dto,routes_test}.go` (A8) · Covers FR1, EC12, AC1
### S12 Review gating [W2-l]
M `RV/domain/model.go`, `RV/app/start.go`, `RV/adapters/postgres/reads.go` +t [C3] (SCR6) · Covers FR13, EC8, EC9
### S13 Eval gating [W2-m]
M `EV/domain` snapshot, `EV/app/runs.go`, `EV/adapters/postgres/snapshots.go` +t (SCR7) · Covers FR13, EC8
### S14 Auto-review filter [W2-n]
M `AR/adapters/postgres/store`+t EnabledAgents (SCR1) · Covers FR13
### S15 GitHub Action card + install dialog [W2-o]
A `IN/_components/{GithubActionCard,InstallActionDialog}/**`, `E/integrationsGithubAction.json` · one call at a time, Retry failed only, `safeRunHref` [F8] (A13) · Covers FR2, FR3, EC13, EC15, AC2, AC3
### S16 Export card + dialog [W2-p]
A `IN/_components/{PluginExportCard,PluginExportDialog}/**`, `E/integrationsExport.json` · Covers FR15, EC15, AC18
### S17 Import card + preview [W2-q]
A `IN/_components/{PluginImportCard,PluginImportPreview}/**`, `E/integrationsImport.json` · type check before any request · Covers FR16, EC14, AC19
### S18 Installed plugins + remove [W2-r]
A `IN/_components/{InstalledPlugins,RemovePluginDialog}/**`, `E/integrationsPlugins.json` (A11) · Covers FR17, EC15, AC20
### S19 Plugin labels [W2-s]
A `F/components/plugin-badge/**`, `E/pluginBadge.json`; M AgentCard, SkillCard, AgentEditor, SkillEditor +t [F10] · Covers FR18, AC21
### S20 Picker filters [W2-t]
M `useRunReviewPicker.ts`, `useConfigureRun.ts`, skills `EvalsTab.tsx` +t via `isPluginDisabled` [F10] · Covers FR18, AC21
### S21 CI export gating [W3-u]
M `CI/domain/model.go`, `CI/app/export`+t, `CI/adapters/peers/directories`+t · 409 right after the agent 404 · Covers FR13
### S22 Composition root [W3-v]
A `S/plugins.go`; M `S/app.go` (routes, RawBodies, buckets), `B/AGENTS.md` (P1, P2, P5, P6) · Covers FR5, NFR3 · Done when `make backend-check`
### S23 Section [W3-w]
A `IN/{SettingsIntegrations.tsx,index.ts,styles.ts}`+t, `E/settingsIntegrations.json`; M `SV/SettingsView`+t (A10) · Covers FR19, NFR5, AC22
### S24 Plugin component tests + goldens [W4-x]
A `S/settings_d_test.go`, goldens `plugins_{list,import_preview,import_installed,patch,delete}`, `ci_installations_by_repo`, `plugin_disabled`; regenerate agent/skill goldens [K17]; then M `F/test/go-backend-contracts.test.ts` · AC6 leak, P2 read-back, P3 drift, P7 · Covers AC1, AC4–AC7, AC9–AC11, AC15, AC17
### S25 Gating component tests [W4-y]
A `S/plugin_gating_test.go` · Covers AC13, AC14, AC16, EC7, EC8
### S26 Manual [W5-z]
Throwaway stacks [C5][K1] · Covers AC23, AC25 · Done when reported

## Execution
- Mode: multi-agent (~110 files, 2 packages, contract, migration).
- W0: S0 · W1: a ∥ b ∥ c ∥ d ∥ e · W2: f … t (15 lanes) · W3: u ∥ v ∥ w · W4: x ∥ y · W5: z
- Lanes share no file; Go lanes use [C1]–[C3], UI lanes S1 + [C4]; S24/S25 share `main`: helper prefixes `sd…`/`pg…`
- Commit W1–W3 as one change set [K10]; W4, W5 each

## Contracts & migrations
S1 ↔ S6, S7, S10, S11, S12, S22; goldens S24. Migration 0008 [C2]; down drops ownership + description (lossy, CR1).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `make check` → `architecture-reviewer` (project) ∥ security (read-only agent + `engineering-paved-path:security`) ∥ `sdd-engineering:plan-verifier`; then S26
- Review risk: high — see [D] hotspots

## Spec change requests (defaults planned; details [D])
SCR1 auto reviews · SCR2 DTO field · SCR3 400/filename · SCR4 eval bounds · SCR5 reset learnings · SCR6 409 order · SCR7 run-all · SCR8 §9 vs FR16

## Open questions / assumptions (defaults planned; details [D])
A0 multi-agent · A1 CR1–CR3 need a yes · A2–A14 per [D] (learnings, agent fields, provenance, filename, heap test, unknown keys, repo last run, export name, UI split, remove count, eval JSON, install list, other specs)
