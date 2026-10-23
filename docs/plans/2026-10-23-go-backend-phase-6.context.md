# Context pack: Go backend Phase 6
`DD/` = `../dev-digest` (read-only, d1c9093). `I/`=`backend/internal/`; `E/ C/ R/ P/ V/ W/ Q/` = `I/` evals, ci, ci/runner, platform, reviewer, reviews, parity; `S/`=`backend/cmd/server/`, `N/`=`backend/cmd/devdigest-runner/`.

## Lane contracts (wired in S33)
- [C0] S1–S4 files are the contract
- [C1] evals `app.Cases`, `app.Runs` (one method per route + `FailInterrupted`; ctx, workspace first); ports `CaseStore`, `RunStore`, `Snapshots`, `Findings`, `Launcher`; executor `Deps{Store, Providers, Engine=reviewerapp.Run, Log}`, `Options{CaseBudget, Cases 3, Drain 5 s}`, `Shutdown`, `ErrNotDrained`
- [C2] Layout `E/{domain,app,app/executor,adapters/{postgres,peers},transport/http}`, `C/{domain,domain/wfrules,app,adapters/{postgres,peers,runnerbin,zipentry},transport/http}`, `R/{domain,app,adapters/{ghapi,env}}`, `N/`. ci `app.Exporter`, `app.Ingest`; ports `Store`, `GitHub` (§7.9; `*GitHubError{Status, RateLimited, Transport}`), `Runner() (RunnerArtifact{Encoded, SHA256, BuildID}, bool)`, `ArtifactReader`; `R/app.Run(ctx, Config, Deps) int`, ports `Env`, `Files`, `GitHub`, `LLM`, `Out`
- [C3] `V/domain.SkillBlock(name, description, body)`; `httpx.Bucket{…, Name, Key func(*echo.Context) string}`: same `Name` = one counter, nil `Key` = IP; run starts `eval-run-start`, constant key; run-all per IP
- [C4] github: one method per §7.9 row on the strict client; `StatusError.RateLimitRemaining`; writes one attempt (publish retried once whole); storage leg: own client, no redirects/credentials, https, cap max+1
- [C5] `options.evalCaseBudget`, `options.runner` (fake artifact), `options.artifactClient` (TLS stub); `now`, `githubBaseURL`, `llmURLs` as is
- [C6] Runner: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.buildID=$ID"` into `C/adapters/runnerbin/` + `build-id`; server `-tags devdigest_runner`; ARG `DEVDIGEST_BUILD_ID` (12 hex else `dev`; Makefile: clean HEAD); git/docker-ignored. LLM: env keys (`OPENROUTER_API_KEY`, `GITHUB_TOKEN` only), nil `Pricer`, stderr Warn
- [C7] YAML: one document, duplicate keys, alias budget 100, `<<` plain, only `!!str`. Manifest: block mapping, JSON-literal strings + `\uXXXX` for controls, U+0085, U+2028/9, U+FEFF. Runner diff: parse `TrimSuffix(raw,"\n")`, keep `Raw`. Blob = base64 of the encoded file bytes

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38-46` — "wire-compatible with the UI"; foreign `Origin` → 403
- [K2] `backend/AGENTS.md:47` — "`reviewer` … is a shared kernel" → P5
- [K3] `backend/AGENTS.md:53-60` — "Echo only in …"; "GORM only in …"; "Row structs are declared per adapter"
- [K4] `backend/AGENTS.md:70` — bodies only via `httpx.ReadJSONBody`
- [K5] `backend/AGENTS.md:31-38` — "Startup reaps … non-fatal. Shutdown order" → add evals
- [K6] `INSIGHTS.md:22` — clients "must not follow redirects"
- [K7] `backend/AGENTS.md:61-64` — apperr kinds; never AutoMigrate
- [K8] `backend/AGENTS.md:82-84` — CGO only for treesitter → runner cgo off
- [K9] `INSIGHTS.md:17` — worker panic kills the server
- [K10] `INSIGHTS.md:43` — "redact provider keys before truncating"
- [K11] `INSIGHTS.md:21,51` — JS float math; `ToFixed`
- [K12] `INSIGHTS.md:37,69` — jsonb: U+0000 rejected; NULL skips Scanner
- [K13] `INSIGHTS.md:55` — Go escapes U+2028/9, JSON.stringify never
- [K14] `INSIGHTS.md:60` — RE2 `(?i)` folds ſ/K
- [K15] `INSIGHTS.md:31` — seed shifts contract goldens: `-update` after the package
- [K16] `INSIGHTS.md:35,40,42` — advisory-lock trigger gate; `Savepoint`; `SQLState()` 23505/23503
- [K17] `INSIGHTS.md:68` — zip: `OpenRaw` + flate under `LimitReader(cap+1)`
- [K18] `INSIGHTS.md:24,66` — `COLLATE "C"` (agents order), not on an alias
- [K19] `INSIGHTS.md:80,83` — short `TMPDIR`; `diff -r`
- [K20] `INSIGHTS.md:41,77,78` — pre-listen non-fatal; bounded waits; 429 `internal_error`
- [K24] `backend/AGENTS.md:9` — `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go …`
- [K25] `CLAUDE.md:76-79` project `architecture-reviewer`, base master; `:92` contracts; `frontend/INSIGHTS.md:58` vitest

## Verified facts
- [F1] `I/llm/llm.go:19`, `structured.go:14,27` import `platform/secrets` for names only (`secrets.go:15-19`)
- [F2] `P/github/client.go:39-50` `StatusError` without headers; `:96-99` default client follows redirects
- [F3] `V/app/engine.go:131`; `V/domain/score.go:43`, `diff.go:39`; `W/domain/prompt.go:71`
- [F4] x/text in `V/domain/jstext.go:22`, `I/skills/domain/slug.go:7`
- [F5] `W/adapters/postgres/reads.go` reads agents
- [F6] go.mod: no goleak; `yaml.v3 v3.0.1` indirect
- [F7] `S/app.go:190-202` reaps, `:463` buckets, `:509` shutdown, `:116` options; `S/flows_test.go:53,725` `/eval/dashboard` 404 → a Compose path
- [F8] uniques `0001_baseline.up.sql:811-832,1231-1261`; FKs `:1434-1637`; `repos.default_branch`
- [F9] `openaicompat.go:31,33,160,302` 180 s, 2 retries, `usage.include`, no redirects
- [F12] `backend/AGENTS.md` has an uncommitted edit

## Mirrors
- `W/app/executor/**`, `W/adapters/peers/providers.go`, `Q/testdata/gen/`, `S/phase5_*_test.go`; Node `DD/server/src/{modules/{evals,ci},db/seed-eval-demo.ts}`, `DD/agent-runner/src`

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| all | `backend/**/*.go` | go-architecture | §1 inward, consumer ports; §5 errors at edge; §6 goroutine owner + wait |
| all | `*_test.go` | go-testing | §3 tables; §4 fakes, httptest; §5 synctest |
| S4,S5,S13–S15,S25–S31 | GitHub, YAML, ZIP, runner | engineering-paved-path:security | A05 input bounds; A08 checksum; A09 no secrets in logs |
| S16,S17,S37 | `*.mts`, `*.json`, `*.test.ts` | — | [K19][K25] |
| S4,S33 | Dockerfile, compose, Makefile, ignores, AGENTS.md | — | [C6] |

## Verification sources
- `Makefile:111`, `:34`; vitest [K25]; generator `Q/testdata/gen/README.md`

## Risks
- Runner over budget → S4 measures
- yaml.v3 vs Node `yaml` drift → corpus decides
- 8 MiB blob in 30 s → AC6062

## Notes for reviewers
- Base `58226c0`; arch [K25], P4, P5; security P10, YAML/ZIP, runner secrets
