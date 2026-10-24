# Context pack: Settings A
[A] `B/`=backend/ · `SP/`=B/internal/spend · `ST/`=B/internal/settings · `G/`=B/internal/platform/github · `S/`=B/cmd/server · `F/`=frontend/src · `SV/`=F/app/settings/[section]/_components/SettingsView · `SC/`=SV/_components · `E/`=frontend/messages/en

## Lane contracts
- [C1] `SP/domain`: `Micros int64`; `MonthStart(now)` UTC 1st 00:00; `Status{MonthStart; Spent; Cap *Micros}` `Reached()` (cap≠nil ∧ spent≥cap), `Remaining()` ≥0, `Message()` §7. `SP/app`: ports `Ledger.MonthSpend(ctx, ws, from, to) (Micros, error)`, `Caps.Cap(ctx, ws) (*Micros, error)`; `Status(ctx, ws)`; `Admit(ctx, ws, op) error` = nil | 402 `spend_cap_reached` details `{cap_usd, spent_usd, month_start}` + INFO log | wrapped err (500). Entry contexts: `budget{Admit(ctx, workspaceID, op string) error}` in Deps, nil = off; ops `review_start, eval_agent_start, eval_skill_start, eval_run_all, conventions_extract, intent_refresh, brief_generate, onboarding_generate`. `G.CurrentUser(ctx) (User{Login; Scopes []string; ScopesReported bool}, error)`; `providers.ListModels(ctx, p, key) (app.ModelList{IDs; Count}, error)`; `abouthttp.AddRoutes(e, Info{Version, BuildID, RunnerVersion, DataDir, SecretsFile})`. PUT: `default_model` = `featureModel()` | null; cap number|null 0..1e6, `big.Rat`×100 integral, Zod texts
- [C2] `F/components/key-field`: `<KeyField provider label hint docsHref configured optional? autoTest?/>`, `maskKey`, masked-edit helper; `F/components/add-repo-form`: `<AddRepoForm onAdded onCancel?/>`; `F/lib/dialog-focus.ts` (Wizard re-exports). Stubs `SC/{SettingsGithub,SettingsAbout}`; `SC/SettingsApiKeys/_components/{DefaultModelField,SpendCapField}`. Namespaces: S14 common, S15 keyField, S16 addRepo, S17 settingsShell, S18 settings, S19 settingsBudget, S21 apiKeys, S22 githubIntegration, S23 about, S24 setup, S25 home
- [C3] One query, `[month_start, next_month)`, per workspace, `COALESCE(SUM(x::numeric),0)`, `round(…,6)`: agent_runs/ran_at; eval_runs⋈eval_cases/ran_at; convention_scans/started_at; pr_intent⋈pull_requests/derived_at; pr_brief⋈pull_requests/`json->>'generated_at'`; onboarding⋈repos/generated_at; guarded jsonb casts skip bad docs; never ci_runs, eval_suite_runs

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38-46` — "wire-compatible with the UI"; JSON only, foreign `Origin` 403, `ALLOWED_HOSTS`; `:90` no `down -v`
- [K2] `B/AGENTS.md:60` — "Features live in `internal/<context>/{domain,app,adapters/<tech>,transport/http}`"
- [K3] `:67,74-79` — "contexts reach each other only through consumer-side ports wired in `cmd/`"
- [K4] `:80` — "`Raw`/`Exec` only for what the builder can't express" → comment why
- [K5] `:88` — secrets "never logged, never in errors"
- [K7] `:82` — "use cases return `apperr` kinds; only `httpx` maps kinds to status"
- [K8] `B/INSIGHTS.md:15,67` — aggregate in SQL, narrow columns
- [K9] `B/INSIGHTS.md:26` — key clients never follow redirects
- [K11] `CLAUDE.md:92` — contracts "change one only together with the matching backend change"
- [K12] `frontend/AGENTS.md:33` — vendor/ui: "compose, don't edit"
- [K13] `frontend/AGENTS.md:13,15,17` thin pages, hooks, messages; `F/INSIGHTS` :15 mockFetch, :94 strayText, :21 toast=status, :38 Select, :11 fake timers, :64 FormField, :48 router.replace
- [K14] `B/INSIGHTS.md:34` — new golden + its frontend contract entry in one change; `:47` `-update` after the package
- [K15] `F/INSIGHTS:80,83` — runtime constants from zod-free `constants/*`

## Verified facts
- [F1] `S/poll.go:24-47`; `S/app.go:325,367-372` settings wiring
- [F2] `ST/app/settings.go:150-180`; handler `:98` has no workspace
- [F3] `G/pulls.go:330` CurrentLogin follows redirects; `noRedirect` `G/client.go:161`
- [F4] checks: reviews `start.go:30-38`; evals `runs.go:124-175`, `runall.go:14`; conventions `service.go:117`; intent `:96-109`; brief `:86-94`; onboarding `:112-121` (422 `:139`)
- [F5] `llm/featuremodel.go:21` sole resolver; new default → intents stale
- [F6] `B/Dockerfile:26` server has no `-X`; build id is a build arg; RunnerVersion `ci/runner/domain/artifact.go:13`
- [F7] index state never 404s, synth `lastIndexedSha:""` (`repointel/app/service.go:34`)
- [F8] `F/lib/providers.tsx:44-48` global toast, no link; brief/onboarding quiet `*`
- [F9] `F/i18n/request.ts`: a namespace per `E/*.json`
- [F10] costs `B/migrations/0001_baseline.up.sql:41,207,288,473,483,518`; no `ran_at` index
- [F11] anthropic: first page only (`providers.go:131`); `test_connection` golden gains `latency_ms` → normalise

## Assumption details [D]
A3 identity error → reference model + WARN · A8 arg `DEVDIGEST_VERSION:-dev` · A10 openrouter.ai/keys, platform.openai.com/api-keys, console.anthropic.com/settings/keys, github.com/settings/tokens · A11 scopes joined " · "; warn when scopes≠null ∧ no `repo` · A13 message 2 decimals (`reviewer/domain.ToFixed`), JSON 6 · A14 failure → extras null; github omits model*, LLM omits login/scopes

## Mirrors
`B/internal/performance/**`, `S/leaderboard_test.go`

## Skill map
| Step | Files | Skills | Rules |
|---|---|---|---|
| S2–S13,S20,S26 | `B/**/*.go` | go-architecture | §1 ports; §5 errors; §8 config |
| Go tests | `*_test.go` | go-testing | tables, cmp.Diff, fakes, pgtest |
| S3,S4,S9,S15,S20,S27 | keys, cap | security | A09 no key in logs; A10 fail closed |
| S1,S14–S25,S29 | `F/**` | react-best-practices, typescript-expert | derive; no effect sync; no `any` |
| tsx tests | | react-testing-library | userEvent, roles |
| S25 | README | mermaid-diagram | `/setup` node |

## Verification sources
- [V1] from `backend/`: `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9,12`)
- [V2] `frontend/`: `pnpm exec vitest run <path fragment>` (`F/INSIGHTS:59,62`), `pnpm typecheck` (`frontend/AGENTS.md:4`)
- [V3] Makefile:119 `make backend-check`, :144 `frontend-check`, :149 `check`

## Risks
`pr_brief.json` detoast → AC23/SCR2 · nil budget → S28 covers 8 routes

## Notes for reviewers
Arch: `SP/`, budget ports · Security: redirects, keys, 402
