# Context pack: Custom model providers
[A] `B/`=backend, `S/`=B/cmd/server, `R/`=B/cmd/devdigest-runner; in B/internal: `LL/`=llm, `LC/`=llm/custom, `LA/`=llm/adapters, `CP/`=customproviders, `ST/`=settings, `AG/`=agents, `CI/`=ci, `RN/`=ci/runner, `PG/`=plugins, `EG/`=platform/egress, `HX/`=platform/httpx, `SE/`=platform/secrets · `F/`=frontend/src, `SH/`=F/vendor/shared, `SV/`=F/app/settings/[section]/_components/SettingsView/_components, `EW/`=…/CiTab/_components/ExportWizard, `E/`=frontend/messages/en

## Lane contracts
- [C1] `LC/` stdlib: `ParseID`, Protocol, AuthMode, Mode, `CheckBaseURL→(norm, plainHTTP, msg)`, `CheckHeaderName`, `ForbiddenAddr`. `LL/llm.go` types only: `Provider.IsCustom`, `Attempt.Mode`, `CustomCall{Adapter, Name, Mode, Price, Secrets}`, `CustomResolver.Resolve`, `ErrCustomNotConfigured`, `Options.Custom`
- [C2] `LA/{openaicompat,anthropic}.NewCustom(CustomOptions{ID, BaseURL, AuthMode, AuthHeader, Key, Headers, HTTPClient, Policy})`: FR4 auth, body by `Attempt.Mode`, cost only finite ≥ 0, errors keep `StatusCode()`
- [C3] `SE/` `Custom(ws,id)`, `SetCustom`, `DeleteCustom`, `CustomValues()`; key `custom_providers.<ws>/<id>`; no env fallback
- [C4] `EG.LoadExtraCA(path)`, `EG.Client{RootCAs, Guard}` (no proxy/redirects; dial refuses `ForbiddenAddr`: "Base URL resolves to a forbidden address"); `HX.PathParam` unescapes
- [C5] `CP/app.Service` List/Get/Create/Update/Delete/TestConnection/Models/Defined/Resolve; ports Store, Secrets, References, Lister; codes per spec §7
- [C6] `CI/domain` stdlib: `EnvName(slug)`, `SecretNames`, `RequiredSecrets/Variables`, `Runner`, manifest `custom_provider`/`pricing` after `version`, PR body step 1 "1. Add the repository Actions secret(s) `A` … and the variable `V` (Settings > Secrets and variables > Actions)."; `wfrules.CheckWith(text, names)`
- [C7] `F/lib/providers`: `isCustomId`, `providerOptions` (OpenRouter, OpenAI, Anthropic, custom by name), `providerName`, `slugFromName`, `hostOf`; hooks `useCustomProviders`, `useSaveCustomProvider`, `useDeleteCustomProvider`; `encodeURIComponent` ids

## Repo rules and notes
- [K1] `CLAUDE.md:38-45` wire compat + guards; `:92` contracts with backend; `:90` no `down -v`
- [K2] `B/AGENTS.md:60-73` layout, cmd-wired ports, no adapter→adapter
- [K3] `:84-105` `Request.Charge` on every call; Recorder per answered attempt
- [K4] `:142-164` plugin gating; Echo/GORM placement · [K5] `:165-176` apperr, up+down, `SE/`, `ReadJSONBody` · [K6] `:179-183` `postgres.Conn` in `InTx`; pgtest
- [K7] `B/INSIGHTS.md:34` no redirects; `:24` unwrap `url.Error`; `:131` 401 echoes masked keys; `:78` redact then cut
- [K8] `:63` runner Pricer non-nil; `:58` runner log; `:99` closure CGO=1 · [K9] `:130`, `:57` pin migration tests
- [K10] `:77` 23505; `:75` savepoint · [K11] `:43`, `:53`, `:52` goldens: `-run Contracts -update`, UI entry, fixed now · [K12] `:113`, `:95` synctest; `:126`, `:128` load
- [K13] `frontend/AGENTS.md:13-19`; `F/INSIGHTS.md` :16, :29, :44, :51, :57, :65, :67, :68, :75, :101, :107, :110, :112

## Verified facts
- [F1] Closed set: `LL/llm.go:26-74`, `structured.go:55-77`, `featuremodel.go:40-52` (skips unknown), `ST/…/validate.go:38,129`, `ST/app/settings.go:24-57`, `AG/domain/agent.go:29`, `AG/…/parse.go:53,90`, `handler.go:341` (unknown id → 422), `CI/domain/manifest.go:13`, `CI/app/export.go:83`, `RN/app/manifest.go:41`, `PG/domain/bundle.go:113`, `0001:107`
- [F2] Cost `LL/structured.go:285-349`; `openaicompat.go:213` cost only OpenRouter; `unpriced_count` + hint shipped (`SpendCapField.tsx:44`)
- [F3] `openrouter` fixed: `CI/app/ingest.go:273` (ledger), `CI/adapters/postgres/ingest.go:90`; upsert `export.go:32`
- [F4] Runner OpenRouter only `R/main.go:76-108`, even for openai manifests (`RN/app/run.go:145`); `redact` `:236`
- [F5] Echo routes `RawPath`, no param unescape (`router.go:896,1128`; `HX/echo.go:61`)
- [F6] Bundle settings never installed `PG/domain/preview.go:113`; export allow-list `workspace/domain/export.go:13`
- [F7] `Ready(p)`: `reviews/adapters/peers/providers.go:58`, `evals/…:61`
- [F8] UI: lists `CreateAgentModal/constants.ts:4`, `ConfigTab/constants.ts:4`, `DefaultModelField.tsx:46`; `SettingsModels.tsx:25` openrouter only; `EW/constants.ts:29`; `lib/plugins/index.ts:22`; raw id `RunRow.tsx:55`, `AgentEditorView.tsx:92`, `TraceBody.tsx:38`
- [F9] `parity/testdata/handwritten/ci_deviations.json`; `goldens/ci.json:6536`
- Mirrors: `skills/adapters/urlfetch`, `plugins/`, `0008`

## Details [D]
- SCR1 ledger shipped FR13/FR22; AC27 = its sentence · SCR2 FR15 settings import moot [F6] · SCR3 malformed id 422, undefined custom `[]` · SCR4 runner hint names the id · SCR5 ledger `ci_run` provider = stored · SCR6 AC24 order (A6) · SCR7 estimated usage never priced (custom, manifest)
- A3 custom calls skip HTTP(S)_PROXY · A4 mock override covers custom · A5 secrets per ws+id · A6 one picker order · A7 secrets in the tx, restored on failed commit · A8 delete FOR UPDATE vs agent write FOR SHARE; settings best effort · A9 runner checks env before GitHub · A10 runner scrubs all names, reads one key · A11 down 0009 refuses on custom agents; secrets stay · A12 reset/remove keep providers · A13 auto reviews unchanged

## Skill map (`pp:` = `engineering-paved-path:`)
| Steps | Skills | Rules |
|---|---|---|
| S2–S14, S21–S29 | go-architecture, go-testing | stdlib domain, consumer ports, apperr; fakes, cmp.Diff, pgtest, synctest, fuzz |
| S2, S21, S24 | pp:postgresql-table-design | NOT NULL + CHECK, numeric, PK covers FK |
| S4, S5, S7–S9, S14, S22, S25 | pp:security | SSRF, A09 logs, injection |
| S1, S15–S20, S30 | pp:react-best-practices, pp:react-testing-library, pp:typescript-expert | derive, hooks, roles, no `any` |

## Verification sources
[V1] `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9-11`) · [V2] `pnpm exec vitest run <fragment>`, `pnpm typecheck`, `pnpm exec biome lint --error-on-warnings <dir>` (`frontend/AGENTS.md:4`, `F/INSIGHTS.md:66,73`) · [V3] `make backend-check|frontend-check|check` (`Makefile:119,144,149`)
