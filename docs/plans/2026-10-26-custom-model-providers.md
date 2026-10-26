# Development Plan: Custom model providers
Plan: Custom model providers · Status: done (2026-10-26; e10495f W1–W4, 67549cd W5, fix rounds 0f108b8 + f86f362; gate arch r2, security r2, plan-verify r2 PASS; spec D31–D37) · Earlier: approved (user pre-approval 2026-10-26: SCR1–SCR7, A1–A13, spec D30) · Spec: specs/2026-10-24-custom-model-providers.md (reconciled at aa7c5be) · Base: feat/go-backend-phase-1@aa7c5be
Packages: backend, frontend · Context pack: docs/plans/2026-10-26-custom-model-providers.context.md ([A], [C1]–[C7], [K], [F], [D])
Execution: multi-agent, parallel lanes (user default)

## Goal
`custom:<slug>` gateways (OpenAI/Anthropic protocol, base URL, auth, headers, manual models, mode) usable for agents, feature/default models, every LLM call, bundles and GitHub Actions CI; openai/anthropic agents become exportable. Keys stay local; costs reach the ledger.

## Out of scope
Spec §3; GitLab CI; `vendor/ui`.

## Decisions
- P1 New context `CP/`, reached via cmd ports [K2][C5]
- P2 Pure rules in `LC/` (server + runner); CI names in `CI/domain`, stdlib only [C1][C6]
- P3 `LL/` HTTP-free: `Options.Custom` gives a per-call adapter built in `S/`; the runner wires a fixed one
- P4 Custom ids need a workspace: `Ready`/`ListModelsIn(ctx, ws, p)`; calls use `Charge.WorkspaceID` [K3][F7]
- P5 FR12 per attempt in `LL/`; reported tokens only (SCR7); runner prices from the manifest [K8]
- P6 Mode: schema block + hint in `LL/`, bodies in adapters [C2]
- P7 Custom calls via `EG.Client` (guard, no proxy A3, no redirect); extra CA on every outbound client [C4][K7]
- P8 Secrets per ws+id in `SE/`, written inside the DB tx (A7) [C3][K5]
- P9 0009: 2 tables, agent CHECK widened, `ci_installations.provider`; down refuses custom agents (A11) [K9]
- P10 Delete = tx with FOR UPDATE; agent writes check FOR SHARE (A8) [K6]
- P11 `HX.PathParam` decodes `custom%3A…` on every provider route [F5]
- P12 openrouter CI output byte-identical; openai case → `ci_deviations.json` (D26) [F9]
- P13 FR13/FR22 shipped by the ledger: tests only (SCR1) [F2]
- P14 `F/lib/providers` feeds all pickers and names (A6) [C7][F8]

## Steps
Done when = the step's tests via [V1]/[V2] + build/typecheck green; UI steps add a pseudo-messages check [K13]. `+t` = with tests.

### S0 Base check [W0, orchestrator]
Confirm `0009` free, re-check [F], brief lanes · Covers spec §12
### S1 Contracts + hooks [W1-a]
M `SH/contracts/{knowledge,platform,eval-ci,productionize}.ts`, `SH/adapters.ts`, `F/lib/{api,types}.ts`, `F/lib/hooks/{core,keys,index}.ts`; A `F/lib/providers`+t [C7] · Covers FR7, FR19, FR20
### S2 Migration 0009 [W1-b]
A `B/migrations/0009_custom_providers.{up,down}.sql`; M `migrate/migrate_test.go` (P9, CHECKs mirror FR1) · Covers FR1, FR7, FR37, EC35, AC48
### S3 Pure rules [W1-c]
A `LC/*`+t (fuzz base URL); M `LL/llm.go` types [C1] · Covers FR1, FR3, FR4, EC2, EC3, EC11, EC24, AC3
### S4 Egress [W1-d]
A `EG/*`+t; M `HX/fields.go`+t [C4] · Covers FR3, FR14, EC3, EC14, EC27, NFR2
### S5 Secrets [W1-e]
M `SE/secrets.go`+t [C3] · Covers FR5, D22, NFR1
### S6 Workflow rules [W1-f]
M `CI/domain/wfrules/rules.go`+t (`CheckWith`; parity verdicts unchanged) · Covers FR28, EC20
### S7 CI domain [W1-g]
A `CI/domain/providers.go`+t; M `CI/domain/{manifest,workflow,prbody,model}.go`+t [C6] · Covers FR25–FR27, FR29, FR35, EC25, EC26, NFR8, AC30, AC31, AC44, AC45
### S8 LLM client [W2-h]
M `LL/{structured,models,featuremodel}.go`+t, `reviews/…/peers/providers.go`+t, `evals/…/peers/providers.go`+t · resolve, keyless, FR12, Redact, mode, hint, NFR6 · Covers FR8, FR9, FR12, FR33, EC9, EC15, EC31, NFR4, NFR6, AC12, AC17, AC18, AC40, AC41
### S9 LLM adapters [W2-i]
M `LA/openaicompat`, `LA/anthropic` +t (httptest) [C2] · Covers FR4, FR8, FR33, EC4, NFR4, AC4, AC17, AC39, AC40
### S10 CP domain + app [W2-j]
A `CP/domain/*`+t, `CP/app/{service,ports,fakes_test,service_test}.go` · Covers FR1, FR2, FR5, FR6, FR10, FR11, EC1, EC5–EC7, EC12, EC13, AC6, AC10, AC11
### S11 Settings [W2-k]
M `ST/transport/http/{validate,handler}.go`+t, `ST/app/settings.go`+t · Covers FR7, FR11, AC8
### S12 Agents [W2-l]
M `AG/transport/http/{parse,handler}.go`+t, `AG/app/service.go`+t · SCR3 · Covers FR6, FR7, FR10, EC14, AC8, AC10
### S13 Plugins [W2-m]
M `PG/domain/{validate,preview}.go`+t, `PG/app/*` port · SCR2 · Covers FR15, EC16, AC15
### S14 Runner app [W2-n]
M `RN/app/{manifest,run,ports,modelerr}.go`+t · A9, A10, SCR4 · Covers FR30, FR36, EC18, NFR7, AC35
### S15 Custom providers UI [W2-o]
A `SV/SettingsApiKeys/_components/{CustomProviders,CustomProviderDialog,ManualModels}/**`, `E/customProviders.json`; M `SettingsApiKeys.tsx`+t · Covers FR16–FR18, FR23, FR34, NFR9, NFR10, AC21–AC23, AC28, AC29, AC43
### S16 Pickers [W2-p]
A `F/components/provider-select/**`; M `CreateAgentModal`, `ConfigTab`, `DefaultModelField`, `SettingsModels` +t · Covers FR19, EC23, AC24
### S17 Provider names [W2-q]
A `F/components/provider-name/**`; M `RunRow`, `AgentEditorView`, `TraceBody` +t · Covers FR20, EC22, AC25
### S18 Home gate + setup [W2-r]
M `HomeView`, `SetupView/_components/ModelStep` +t, `E/{home,setup}.json`; re-run SpendCapField tests · Covers FR21, FR22, AC26, AC27
### S19 Export wizard [W2-s]
M `EW/**` (Runner, notes, secrets from response) +t, `E/ci.json` · Covers FR31, EC21, AC36
### S20 Install dialog [W2-t]
M `SV/SettingsIntegrations/_components/InstallActionDialog/**`, `F/lib/plugins/index.ts` +t · Covers FR32, EC34, AC37
### S21 CP store [W3-u]
A `CP/adapters/postgres/*`+t [K6][K10] · Covers FR1, FR2, EC1, AC1
### S22 CP routes [W3-v]
A `CP/transport/http/{routes,dto,validate}`+t · Covers FR2, EC2, EC13, EC30, NFR3, AC2, AC16, AC42
### S23 CI app + routes [W3-w]
M `CI/app/{export,ingest,api,ports}.go`+t, `CI/transport/http/*`+t, `parity/…/ci_deviations.json` · SCR5 · Covers FR24, FR29, FR35, FR37, EC17, EC36, EC37, AC32, AC45, AC49
### S24 CI store [W3-x]
M `CI/adapters/postgres/{export,ingest,lists}.go`+t · Covers FR37, EC33, AC47
### S25 Runner binary [W3-y]
M `R/main.go`+t, `R/{secrets,parity,closure}_test.go` · [K8] · Covers FR30, FR36, EC19, EC29, NFR7, AC34, AC44, AC46, AC49
### S26 Composition root [W4-z]
A `S/customproviders.go`; M `S/app.go`, `config/config.go`+t, `.env.example`, `B/AGENTS.md` · Covers FR14, EC27, AC14 · Done when `make backend-check`
### S27 CP component tests [W5-1]
A `S/custom_providers_test.go`, goldens `custom_providers_*` · Covers AC1, AC2, AC6–AC8, AC10, AC11, AC14, AC16, AC42
### S28 Call component tests [W5-2]
A `S/custom_calls_test.go` (stub gateway, recorder) · Covers FR13, AC4, AC5, AC9, AC12, AC13, AC15, AC17, AC18, AC39–AC41
### S29 CI component tests [W5-3]
A `S/custom_ci_test.go`, CI goldens · Covers AC30–AC33, AC45, AC47–AC50
### S30 Contract test [W6-4]
M `F/test/go-backend-contracts.test.ts`; review the golden diff [K11] · Covers NFR11, AC19
### S31 Manual [W6-5]
Throwaway stack (`B/INSIGHTS.md:42`) · Covers AC20, AC38 · Done when reported

## Execution
- Mode: multi-agent (~120 files, contracts, migration)
- W0 S0 · W1 a–g · W2 h–t (13 lanes) · W3 u–y · W4 z · W5 1 ∥ 2 ∥ 3 · W6 4 ∥ 5
- Lanes share no file; Go lanes code against [C1]–[C6], UI lanes against S1 [C7]; W5 lanes use helper prefixes `cp…`/`cc…`/`ci…`
- Commit W1–W4 as one change set [K1]; W5, W6 each

## Contracts & migrations
S1 ↔ S11, S12, S22, S23; goldens S27–S29, checked by S30. Migration 0009 (P9).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3] (`cmd/server` may need `-timeout 25m` [K12])
- Review gate: `architecture-reviewer` (project) ∥ security (`engineering-paved-path:security`) ∥ `sdd-engineering:plan-verifier`; then S31
- Review risk: high — outbound secrets, SSRF guard, workflow injection, shared contracts, migration

## Spec change requests (defaults planned; [D])
SCR1 FR13/FR22/AC27 shipped · SCR2 FR15 settings · SCR3 FR10 422 · SCR4 runner hint · SCR5 ledger provider · SCR6 AC24 order · SCR7 estimated tokens

## Open questions / assumptions (defaults planned; [D])
A1 Q13 top-level `pricing` for openai/anthropic only · A2 Q14 CiInstallation unchanged · A3–A13 per [D]
