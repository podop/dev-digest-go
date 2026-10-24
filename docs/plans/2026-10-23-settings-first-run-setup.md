# Development Plan: Settings A — shell, API Keys, GitHub, About, first-run setup
Plan: Settings A · Status: approved (user, 2026-10-24: A0–A14 accepted, SCR1 → spec D23, SCR2 measure first) · Spec: specs/2026-10-23-settings-first-run-setup.md · Base: feat/go-backend-phase-1@f7fd4a0
Packages: backend, frontend · Context pack: docs/plans/2026-10-23-settings-first-run-setup.context.md ([A] aliases, [C1]–[C3] lane contracts, [D] assumptions)
Execution: multi-agent, parallel lanes (default, A0)

## Goal
Seven-section Settings, richer connection test, `default_model`, monthly spend cap (402 at 8 LLM routes), `GET /settings/spend`, `GET /about`, 3-step `/setup` gated from `/`.

## Out of scope
Spec §3; slices B–D; `B/internal/leaderboard/**` (in flux, unused); migrations.

## Decisions
- P1 Read-only context `SP/` like performance [K2][K3]; cap via a port bound in `S/` like `settingsIntervals` [F1]
- P2 Money in int64 micros; one Raw query, 6 SUMs [C3][K4]
- P3 402 = `apperr.KindPaymentRequired`, mapped in httpx only [K7]; `Admit` fails closed
- P4 Cap check in each use case after 422/404, before any write/marker/enqueue; consumer-side `budget` port, nil = off; once per request [C1][F4]
- P5 Test connection: `ListModels`/`CurrentUser` ports, clock, logger in `ST/app`; per-provider DTOs, absent ≠ null [F2][F3]
- P6 About: transport-only; `-X main.version|buildID`, default `dev` [F6]
- P7 App-owned section list + messages; vendored `SETTINGS_SECTIONS` untouched [K12]; unknown → `redirect`
- P8 Any 402 → global MutationCache toast with link [F8]; shared `key-field`, `add-repo-form`, `lib/dialog-focus` [C2]
- P9 One message namespace per UI lane [C2][F9]; root-bundle constants zod-free [K15]

## Steps
Done when = step tests [V1]/[V2] + build/typecheck green; every UI folder has `*.test.tsx` with a pseudo-messages check (AC22) [K13].

### S1 Contract + hooks [W0-a]
M `F/vendor/shared/contracts/platform.ts`, `F/lib/types.ts`, `F/lib/hooks/{keys,core}.ts`; A `F/vendor/shared/constants/settings.ts`, `F/lib/hooks/settings.test.tsx`. Spec §7 shapes; `useSpend`, `useAbout`. [K11][K15] · Covers NFR9
### S2 Go skeleton + 402 [W0-b]
A `SP/domain/model.go`, `SP/app/{ports,api}.go` as [C1]; M `B/internal/platform/{apperr/apperr,httpx/errors}.go` + tests · Covers FR13 (402)
### S3 GitHub user [W0-c]
M `G/{pulls,client,github_test}.go`: `CurrentUser` on `noRedirect()`, scopes header · Covers FR16, EC3, NFR2
### S4 Provider list [W0-d]
M `ST/adapters/providers/providers{,_test}.go`: `ListModels` (A1) · Covers FR5, EC1, NFR2 (one GET, 302 not followed, key never in errors)
### S5 Feature-model fallback [W0-e]
M `B/internal/llm/featuremodel{,_test}.go`: choice > default_model > registry · Covers FR9, EC10
### S6 Spend domain [W1-f]
A `SP/domain/{month,status}{,_test}.go` · Covers EC5, EC7, FR11, FR13 message (A13)
### S7 Spend store [W1-g]
A `SP/adapters/postgres/{store,store_test}.go` [C3][K8] · Covers AC9, EC4, FR12 (pgtest: 6 sources, mirrors, 2 workspaces)
### S8→S9 Settings validation, test connection [W1-h]
M `ST/transport/http/{validate,validate_test,handler}.go`, `ST/app/settings{,_test}.go`: FR7/FR11 issues [C1]; FR5/FR16 fields, latency, checked model (A3, A9, A14); NFR6 log · Covers AC4–AC7, EC1–EC3, NFR1
### S10 About route [W1-i]
A `B/internal/about/transport/http/{routes,routes_test}.go` · Covers FR20, NFR5
### S11 Budget: reviews, evals [W1-j]
M `B/internal/reviews/app/{start,service,start_test}.go`, `B/internal/evals/app/{runs,runall,service}.go` + tests · Covers FR13, EC6, EC9
### S12 Budget: conventions, intent [W1-k]
M `B/internal/{conventions,intent}/app/service{,_test}.go` · Covers FR13
### S13 Budget: brief, onboarding [W1-l]
M `B/internal/{brief,onboarding}/app/service{,_test}.go` · Covers FR13, EC9, A7
### S14 402 surfacing [W1-m]
M `F/lib/{toast,providers}.tsx`, `F/lib/query-meta.ts`, `E/common.json`; A `F/lib/mutation-errors{,.test}.tsx` · Covers FR14 (402), AC12 (A6)
### S15 Key field [W1-n]
A `F/components/key-field/**`, `E/keyField.json` · Covers FR3, FR4, FR6, EC17, NFR7, AC2, AC3 (SCR1)
### S16 Add-repo form, dialog focus [W1-o]
A `F/components/add-repo-form/**`, `F/lib/dialog-focus.ts`; M ExportWizard `useExportWizard.ts`, `helpers.ts` · Covers FR19, FR25, EC15
### S17 Settings shell [W1-p]
M `F/app/settings/[section]/page.tsx`, `SV/{SettingsView.tsx,constants.ts}`; A tests, stubs `SC/{SettingsGithub,SettingsAbout}/index.ts`, `E/settingsShell.json` · Covers AC1, FR1
### S18 Default model: agents, Feature Models [W1-q]
M `CreateAgentModal{,.test}.tsx`, `SC/SettingsModels/*` (+ test), `E/settings.json` · Covers FR10, AC13
### S19 Default model + spend cap fields [W1-r]
A `SC/SettingsApiKeys/_components/{DefaultModelField,SpendCapField}/**`, `E/settingsBudget.json` · Covers FR8, FR14, EC18, AC12, AC13
### S20 Spend service + route [W2-s]
A `SP/app/service{,_test}.go`, `SP/transport/http/{routes,dto,handlers_test}.go` · Covers FR12, FR13, NFR6, D16
### S21 API Keys section [W2-t]
M `SC/SettingsApiKeys/{SettingsApiKeys.tsx,constants.ts}`; A test, `E/apiKeys.json` · Covers FR2, FR15, AC2, AC3, AC15
### S22 GitHub Integration [W2-u]
`SC/SettingsGithub/**`, `E/githubIntegration.json` · Covers FR17–FR19, AC16, AC17, EC13 (A4, A11)
### S23 About section [W2-v]
`SC/SettingsAbout/**`, `E/about.json` · Covers FR21, AC15
### S24 Setup [W2-w]
A `F/app/setup/page.tsx`, `F/app/setup/_components/SetupView/**`, `E/setup.json`; M `F/lib/hooks/repo-intel.ts` · Covers FR22–FR26, AC19, AC20, EC11, EC12, EC14–EC16
### S25 Home gate, /onboarding [W2-x]
M `F/app/_components/HomeView/*`, `F/app/onboarding/page.tsx`, `E/home.json`, `frontend/README.md`; D `F/app/onboarding/_components/AddRepoView/**` · Covers FR27, FR28, AC18, AC21
### S26 Composition root, build [W3-y]
M `S/{app,main}.go`, `B/Dockerfile`, `docker-compose.yml`, `B/AGENTS.md`; A `S/spend.go` (A8) · Done when `make backend-check`
### S27→S29 Settings/about component tests, UI contract [W4-z]
A `S/settings_a_test.go`, goldens `S/testdata/contracts/{about,settings_spend,test_connection_llm,spend_cap_reached}.json`; M `S/flows_test.go`; then M `F/test/go-backend-contracts.test.ts` [K14] · Covers AC4–AC7, AC14, AC24, NFR1, NFR3, NFR5, NFR9
### S28 Spend/cap/default-model component tests [W4-aa]
A `S/spend_cap_test.go` · Covers AC8–AC11, NFR6
### S30 Manual [W5-ab]
AC23, AC25, throwaway stack [K1] · Done when reported

## Execution
- Mode: multi-agent (recommended: ~75 files, 2 packages, contract). Single-agent: waves in order
- W0: a S1 ∥ b S2 ∥ c S3 ∥ d S4 ∥ e S5
- W1: f S6 ∥ g S7 ∥ h S8→S9 ∥ i S10 ∥ j S11 ∥ k S12 ∥ l S13 ∥ m S14 ∥ n S15 ∥ o S16 ∥ p S17 ∥ q S18 ∥ r S19
- W2: s S20 ∥ t S21 ∥ u S22 ∥ v S23 ∥ w S24 ∥ x S25 · W3: y S26 · W4: z S27→S29 ∥ aa S28 · W5: ab S30
- Lanes share no file; Go lanes use [C1], UI lanes S1 + [C2]
- Commit W0–W3 as one change set (contract with its backend) [K11]; W4, W5 each

## Contracts & migrations
`platform.ts` S1 ↔ S9, S10, S20, S26; goldens S27; UI test S29. No migrations.

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `make check` → `architecture-reviewer` (project) ∥ security (read-only agent + `engineering-paved-path:security`) ∥ `sdd-engineering:plan-verifier`
- Review risk: high — spend gate on 8 routes (fail closed), keys, contract, unindexed SQL (NFR4)

## Open questions / assumptions (defaults planned; details [D])
- A0 multi-agent · SCR1 AC2 says 16 dots, FR3 gives 17 (21-char key) → FR3 wins; spec AC2 fixed (D23)
- SCR2 no index (NFR4): if AC23 fails, an index migration needs a spec change
- A1 Anthropic one `GET /models?limit=1000` · A2 spend per request workspace · A3 default_model best-effort
- A4 not indexed ⇔ `lastIndexedSha == ""` · A5 NFR6 route = op label · A6 402 toast despite quiet `*`
- A7 402 before onboarding's 409 marker, so it wins over `index_not_ready` · A8 build arg `DEVDIGEST_VERSION`
- A9 empty anthropic list → model null · A10 key URLs · A11 scopes text · A12 no clear-default UI · A13 money · A14 null/absent
- Start after the leaderboard gate (STATUS); its patch may break gates
