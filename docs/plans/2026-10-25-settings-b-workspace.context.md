# Context pack: Settings B
[A] `B/`=backend/ · `W/`=B/internal/workspace · `ST/`=B/internal/settings · `R/`=B/internal/repos · `S/`=B/cmd/server · `F/`=frontend/src · `SV/`=F/app/settings/[section]/_components/SettingsView · `WS/`=SV/_components/SettingsWorkspace · `E/`=frontend/messages/en

## Lane contracts
- [C1] `W/domain`: `NormalizeName` (trim, 1–80 runes, no U+0000–001F/007F); `Retention{Days; Forever}`, `ParseRetention(int)` (`ErrInvalidRetention`), absent 90, `Cutoff(now)` = now − Days×24h; `ExportFileName(name, now)`; `UniqueNames`; `ExportableKeys` (FR14); `Stats`; `Deleted` (§7 + `PRsUnmarked`); `BuildBundle(ExportData, Meta{Name, Version, Now})`. `W/app` ports: `NameStore`, `RepoOverview`, `SettingsSource`, `Reader{Stats; ExportData}`, `History{Reset(ctx, ws) (Deleted, active int, error)}`, `Workspaces`, `RetentionSource`, `TracePruner{PruneTraces(ctx, ws, cutoff) (int64, error)}`; `Service{Info, Rename, Stats, Export, Reset}`; `Pruner.Run(ctx)`; `postgres.Store` type (S3)
- [C2] SQL: stats one statement. Export drops `embedding`, `sources`; stable `ORDER BY` (`COLLATE "C"` for names, paths). Reset tx: advisory `workspace-history:<ws>`, `lock_timeout 10s`, `LOCK TABLE agent_runs, eval_suite_runs IN SHARE ROW EXCLUSIVE MODE`, count running, then children before parents (findings → … → memory), clear `last_reviewed_sha`. Prune: ≤ 5,000 traces per tx under the same advisory lock, ctx checked between batches
- [C3] UI namespaces: S10 `settingsWorkspace`, S11 `workspaceRetention`, S12 `workspaceStats`, S13 `workspaceDanger`; hooks `useWorkspace`, `useRenameWorkspace`, `useWorkspaceStats`, `useExportWorkspace`, `useResetHistory`; `workspaceKeys`
- [C4] AC23: compose project `devdigest-go-sb`, NFR3 data via SQL in `.sdd/review/2026-10-25-settings-b-workspace/`, raw timings kept, project + volumes removed after

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38-46` "wire-compatible with the UI"; JSON only, foreign Origin 403, ALLOWED_HOSTS; `:90` no `down -v`
- [K2] `B/AGENTS.md:60` "Features live in `internal/<context>/{domain,app,adapters/<tech>,transport/http}`"
- [K3] `:68` "consumer-side ports wired in `cmd/`"
- [K4] `:95` "`Raw`/`Exec` only for what the builder can't express (advisory locks, CTEs, DDL)"
- [K5] `:97` "only `httpx` maps kinds to status"
- [K6] `:112` inside `InTx` "takes its handle from `postgres.Conn(ctx, db)`"
- [K7] `:106` "Request bodies only through `httpx.ReadJSONBody`"
- [K8] `:37` poller "runs on the stop ctx, returns ≤ 5 s; `listen` waits"
- [K9] `:104` secrets "never logged, never in errors"
- [K10] `CLAUDE.md:92` contracts change "together with the matching backend change"
- [K11] `B/INSIGHTS.md:46,55` freeze tx + poll `pg_locks`, no sleeps
- [K12] `frontend/AGENTS.md:13-17` thin pages, hooks, `messages/en`; `:33` vendor/ui "compose, don't edit"; `F/INSIGHTS:53` keys from `keys.ts`
- [K13] `F/INSIGHTS:16` mockFetch; `:99` strayText; `:22` toast=status; `:29` createAppQueryClient; `:47` quietErrorCodes; `:104` clipboard; `:62` `type="button"`
- [K14] `B/INSIGHTS.md:75` synctest: Sleep before Wait
- [K15] `F/INSIGHTS:40` Select + `pickOption`; `:72` Escape `defaultPrevented`
- [K16] `B/INSIGHTS.md:38` golden + UI contract entry together; `:51` `-update` after package
- [K17] `B/INSIGHTS.md:72` time scan + encode; `:106` migrate tests pin `embeddedFS` (SCR3 only)

## Verified facts
- [F1] GET /workspace `R/transport/http/handler.go:51,93`; Overview `R/app/service.go:132`; tests `handler_test.go:513-564`
- [F2] lookup by "default" `tenancy.go:48`, `seed.go:178`
- [F3] settings `user_id` nullable (`0001:658`); List = all rows (`ST/adapters/postgres/store.go:67`); unknown keys stored (`validate.go:53`); adapters `S/poll.go`, `S/spend.go`
- [F4] reaps `S/app.go:239-260` before `listen`; poller `:696`; seam `pollTick :179`
- [F5] `0003` `agent_runs_ws_ran_idx`; cascades `0001:1420,1616,1665,1819,1833`; `ci_runs.agent_run_id` SET NULL `:1504` unindexed
- [F6] UI :3100 → API :3101; `httpx/cors.go` no `Access-Control-Expose-Headers`
- [F7] `RepoSwitcher.tsx:66` string subtitle, no ellipsis/title
- [F8] `Modal.tsx:26` no title id; `dialog-focus.ts:46` focuses the X first
- [F9] `SV/SettingsView.tsx:18` PANELS
- [F10] no memory query in `F/`; PR status from `last_reviewed_sha` (`pulls/domain/pulls.go:156`)
- [F11] `flows_test.go:612` workspace golden, `:523` dataDir normalizer; UI schema `go-backend-contracts.test.ts:416`

## Assumption details [D]
A11 proto §3.3 verbatim: sub "Where this workspace keeps its agents, skills, learnings, and run history."; danger "Resetting clears runs, traces and learnings for this workspace. Agents and skills are kept. This cannot be undone." · A12 AC14 markers only in run/review/finding rows

## Mirrors
`B/internal/spend/**`, `S/settings_a_test.go`, `RemoveRepoDialog`, `SpendCapField`

## Skill map
| Step | Files | Skills | Rules |
|---|---|---|---|
| S2–S9,S15 | `B/**/*.go` | go-architecture | consumer ports; errors at edge; goroutine waited |
| Go tests | `*_test.go` | go-testing | tables, cmp.Diff, fakes, pgtest, synctest |
| S5,S6 | SQL | postgresql-table-design | index FK lookups; set deletes |
| S6,S7,S9,S13,S18 | reset, export | security | A01 confirm+Origin; A09 counts only |
| S1,S10–S16 | `F/**` | react-best-practices, typescript-expert | derive; no `any` |
| tsx tests | | react-testing-library | userEvent, roles |

## Verification sources
- [V1] `backend/`: `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9,11`)
- [V2] `frontend/`: `pnpm exec vitest run <folder>` (`F/INSIGHTS:64`), `pnpm typecheck` (`frontend/AGENTS.md:4`)
- [V3] `Makefile:119` backend-check, `:144` frontend-check, `:149` check

## Risks
Executor writing after a run ends during reset · ci_runs FK scan (SCR3) · trace size vs NFR3

## Notes for reviewers
Arch: cross-context delete in `W/adapters/postgres`; GET /workspace moved · Security: export headers, reset guards, logs
