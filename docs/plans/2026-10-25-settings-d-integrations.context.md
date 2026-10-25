# Context pack: Settings D
[A] `B/`=backend/; `PG/`=plugins, `AG/`=agents, `SK/`=skills, `RV/`=reviews, `EV/`=evals, `CI/`=ci, `AR/`=autoreview, `ST/`=settings, `HX/`=platform/httpx, `W/`=workspace; `S/`=B/cmd/server; others under B/internal · `F/`=frontend/src · `SV/`=F/app/settings/[section]/_components/SettingsView · `IN/`=SV/_components/SettingsIntegrations · `E/`=frontend/messages/en

## Lane contracts
- [C1] `PG/domain`: tagged bundle types (⊇ B's); `ParseUpload(media, body)`: ≤ 500 entries, all names checked (abs, `..`, `\`, drive), no encrypted, Store|Deflate, one `devdigest-plugin.json` at root or one top folder, OpenRaw+flate ≤ 20 MiB+1, utf8.Valid, BOM cut; `Validate` (§7, A2, A3, SCR4, no U+0000; ≤ 20 issues); `Preview`; `BuildBundle` + `UniqueNames` (W's rule); `ExportFileName`, `DefaultName`, `LastExport`, `ErrInvalid`, `ErrConflict`. `PG/app` ports `ExportReader`, `Names`, `Installer`, `Plugins{…, Remove(…, check func(name) error)}`, `LastExports`, `WorkspaceName`; `Service` = the six routes
- [C2] 0008: `installed_plugins` + `description`, `enabled` default false, `UNIQUE (workspace_id, name)`; `agents|skills|memory.plugin_id` NULL FK + partial index. Install tx: plugin row first, batched inserts, 23505 → re-read → 409. Remove tx: lock, check name, count ci_installations, delete eval_cases of owned owners, memory, agents, skills, plugin
- [C3] `x.enabled AND (x.plugin_id IS NULL OR EXISTS (SELECT 1 FROM installed_plugins p WHERE p.id = x.plugin_id AND p.enabled))`; named → conflict `plugin_disabled` naming the plugin
- [C4] S1: types `PluginRef`, preview, `PluginRow`, list, export request, `CiRepoInstallations`; hooks `usePlugins`, `use{Export,Preview,Install,Remove}Plugin`, `useSetPluginEnabled`, `useWorkspaceCiInstallations`, `useInstallCi`; `pluginKeys`; `isPluginDisabled`, `ciExportable`; `api.download(path, init)`, `api.upload`, `api.del(path, body?)`. Namespaces: the `E/` file named in each step
- [C5] Shared with C W4: contract test, `S/{app,flows_test}.go`, `AR/…/store.go`, `migrate_test.go`, `SV/SettingsView*`, `B/AGENTS.md`. S26: projects `devdigest-go-sd{,2}`, NFR4 SQL + timings in `.sdd/review/<plan>/`, removed after

## Repo rules (at d653f2c; S0 re-checks)
- [K1] `CLAUDE.md:38-46` wire + guards; `:90` no `down -v`
- [K2] `B/AGENTS.md:60-61` layout · [K3] `:67-69` ports wired in `cmd/`; adapters never import adapters
- [K4] `:115-116` reset writes other contexts' tables · [K5] `:143-144` Raw/Exec only if needed
- [K6] `:160-162` `postgres.Conn` inside `InTx` · [K7] `B/INSIGHTS.md:104` OpenRaw + LimitReader(cap+1)
- [K8] `B/INSIGHTS.md:12` index FKs · [K9] `B/AGENTS.md:155-157` `ReadJSONBody`; upload = design §Slice D exception
- [K10] `CLAUDE.md:92` contracts with backend; `W/domain/export.go:13-34` allow-list
- [K11] `frontend/AGENTS.md:13-19,33`; `F/INSIGHTS.md:54` keys, `:48` quietErrorCodes
- [K12] `F/INSIGHTS.md` :16, :100, :22, :30, :63, :34/:73 (Escape), :104 (uploads), :41, :44
- [K13] `B/INSIGHTS.md:115` pin migration tests; `B/AGENTS.md:148-149` up + down
- [K14] `B/INSIGHTS.md:66` 23505; :64 savepoint · [K15] :50 blocked twin; :59 trigger freeze/fail
- [K16] `B/INSIGHTS.md:39` download headers · [K17] :42, :55 goldens
- [K18] `B/INSIGHTS.md:31` ParameterizedQueries; :51/:61 U+0000; :32 COLLATE "C"; :124 429 code

## Verified facts
- [F1] `0001`: `installed_plugins :409-417`; skills unique `:1112`; eval_cases owner no FK `:265`; cascades `:1434-1497,1609,1623`
- [F2] `RV/app/start.go:106-140`, `RV/…/reads.go:54-100`, `EV/app/runs.go:158,181`, `EV/…/snapshots.go:37-74,97`, `AR/…/store.go:70-86`, `CI/app/export.go:63-77`, `CI/…/peers/directories.go:47-74`
- [F3] `ST/…/validate.go:45,91-94`; `S/workspace.go:21-53`
- [F4] `HX/body.go:32-79,98`; `HX/echo.go:15-32`; `S/app.go:581-596,620,654`
- [F5] one agent DTO `AG/…/handler.go:66-80`; lock `AG/…/store.go:163`; contract `.strict()` `:497-517,530`
- [F6] `SK/domain/{zipimport.go:16-24,skill.go:174}`; `EV/domain/model.go:42,46`
- [F7] `W/domain/bundle.go:30-136`; memory all rows `W/…/reads.go:303`; `productionize.ts:29-149` additive
- [F8] `F/lib/api.ts:24-36,94`; `ci-runs.ts` safeRunHref; `SV/SettingsView.tsx:20-27`
- [F9] `CI/…/lists.go:81-86,114` · [F10] pickers `useRunReviewPicker.ts`, `useConfigureRun.ts`, skills `EvalsTab.tsx`; mirror `components/skill-type-badge`

## Details [D]
- SCR1 AR skips them (no 409) · SCR2 all DTOs · SCR3 global 400; bad `filename` 422 · SCR4 name 1–120, ≤ 200/owner · SCR5 B rule kept · SCR6 after item 404, before PR 404/422/402 · SCR7 omitted · SCR8 FR16
- A2 enums, content 1–20,000, confidence null|0–1, no repo · A3 defaults when absent · A4 `plugin:<name>`, v1 "Installed from plugin <name>" · A5 1–255, no control chars · A6 MemStats · A7 ignored + listed · A8 newest ran_at; repos `COLLATE "C"` · A9 `devdigest-`+slug ≤ 64 · A10 section: queries + toggles/remove; dialogs: flows · A11 installations × agents · A12 null|object|array · A13 hidden · A14 custom providers FR15/FR24, GitLab C FR45 extend D: one provider list
- CR1 lossy down · CR2 route-scoped raw body · CR3 missing [C3] = trust-gate bypass (D6)
- Hotspots: bombs, U+0000, AC6, logs id/name/counts, server confirm, `safeRunHref`, no outbound call

## Skill map (`pp:` = `engineering-paved-path:`)
| Step | Skills | Rules |
|---|---|---|
| S2–S14,S21,S22,S24,S25 | go-architecture, go-testing, pp:postgresql-table-design | consumer ports; cmp.Diff, fakes, pgtest; FK index |
| S3,S4,S8–S10,S15,S17 | pp:security | A05, A08, A09 logs |
| S1,S15–S20,S23 | pp:react-best-practices, pp:typescript-expert, pp:react-testing-library | derive; no `any`; roles |

## Verification sources
[V1] `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9,11`) · [V2] `pnpm exec vitest run <fragment>` (`F/INSIGHTS.md:65,68`), `pnpm typecheck` (`frontend/AGENTS.md:4`) · [V3] `Makefile:119,144,149`

## Risks, reviewers
One install tx = one connection · shape drift (S24) · Arch: cross-context writes in `PG/adapters/postgres` · Security: CR2, CR3
