# Development Plan: Go backend Phase 6
Plan: Go backend Phase 6 · Status: approved · Spec: specs/2026-10-23-go-backend-phase-6.md · Base: feat/go-backend-phase-1@58226c0
Packages: backend, frontend test · Context pack: docs/plans/2026-10-23-go-backend-phase-6.context.md
Execution: multi-agent (user)

## Goal
The 20 §7.2 routes serve the unchanged UI; a Go CI runner is built in the image and exported; all 102 Node routes served.

## Out of scope
Spec §3 (no migration; go.mod promotes `yaml.v3` only; frontend: contract test only).

## Planner decisions
- P1 W0 writes types, ports, API signatures as code [C0]
- P2 CRITICAL Runner closure (AC6048): key names move to leaf `P/keynames`, llm drops `platform/secrets` [F1]; runner keys from env only, nil `Pricer` [C6]
- P3 CRITICAL Shipping (D6004): runner embedded via build tag, built first in the Dockerfile [C6]; untagged builds/tests have none
- P4 Layout [C2]; runner never imports Echo/GORM [K3]
- P5 CRITICAL One engine `V/app.Run` + llmcall (reviews, evals, runner); `E/app/executor`, `R/app` may import `V/` (S33 rule) [K2]; `SkillBlock` to `V/domain`; gate `CountBlockers` [F3]
- P6 `C/`, `R/` import `yaml.v3` directly [F4]
- P7 CRITICAL `checkWorkflow` on `yaml.Node` [C7] [K14]; corpus decides
- P8 Manifest text format [C7]
- P9 Shared limiter bucket [C3]
- P10 CRITICAL GitHub [C4]: no redirects with token; artifact storage leg without credentials [K6]
- P11 Refresh: each GitHub call ≤ 30 s, started before the deadline
- P12 CRITICAL Eval executor = reviews executor lifecycle (≤ 5 s drain), workers recover [K9]
- P13 Stores read peer tables [F5]; gated ACs: advisory lock [K16]
- P14 Evals LLM via peers + cmd `newLLMCaller`; error text Redact then cut [K10]
- P15 Phase 5 generator extended; its goldens unchanged [K19]
- P16 Runner endpoints fixed; tests call `run(…, endpoints)`
- P17 AC6052: one engine `Input`, runner vs server wiring
- P18 AC6025 via goroutine dump [F6]
- P19 S18 re-records shifted goldens once [K15]
- P20 cmd seams [C5]
- P21 Runner diff and blob rules [C7]
- P22 Artifact JSON JS-compatible [K11][K13]
- P23 S33 keeps the uncommitted AGENTS.md edit [F12]
- D6001–D6025, Q6001–Q6009: spec defaults

## Steps
Prefixes in [C0]. Tests per file; Go [K24]; DB tests pgtest. Done when = `go test -race -count=1 <step pkgs>/...`.

### S1 — Evals contracts [W0-Z]
- A `E/domain/model.go`, `E/app/{ports,api}.go`, `E/app/executor/api.go` [C1] · Covers: FR6018
### S2 — CI contracts [W0-Y]
- A `C/domain/model.go`, `C/app/{ports,api}.go`, `R/app/{ports,api}.go` [C2] · Covers: FR6048
### S3 — Kernel, seams [W0-X]
- A `V/domain/skillblock.go`, `P/github/{gitdata,actions}.go` (stubs); M `W/domain/prompt.go`, `P/httpx/ratelimit.go` (fields) [C3][C4] · Covers: NFR6014
### S4 — Runner build smoke [W0-W]
- A `P/keynames/keynames.go`, `N/{main,closure_test}.go`, `C/adapters/runnerbin/{runnerbin,embed,none}.go`; M `I/llm/{llm,structured}.go`, `P/secrets/secrets.go`, `backend/{Dockerfile,.dockerignore,go.mod}`, `.gitignore` · P2, P3 · Covers: AC6048 · Done when also: [C6] sizes, `docker compose build backend`
### S5 — GitHub ops [W1-A]
- M `P/github/{client,gitdata,actions}.go`; A `P/github/artifact.go` · P10 · Covers: AC6056
### S6 — Shared bucket [W1-B]
- M `P/httpx/ratelimit.go` · P9 · Covers: FR6013
### S7 — Case rules [W1-C]
- A `E/domain/{casebuild,caseinput,edit,errtext}.go` [K10][K12] · Covers: FR6014–FR6016
### S8 — Scoring, run input [W1-C]
- A `E/domain/{scoring,runinput}.go` [K11] · Covers: FR6022
### S9 — Compare, dashboards [W1-C]
- A `E/domain/{compare,linediff,dashboard,period}.go` · Covers: FR6028
### S10 — Slug, manifest [W1-E]
- A `C/domain/{slug,manifest}.go` · P8 · Covers: FR6034
### S11 — Workflow, PR body, encoding [W1-E]
- A `C/domain/{workflow,prbody,runner}.go` · Covers: FR6035, FR6036
### S12 — Artifact, codes, run view [W1-E]
- A `C/domain/{artifact,ghcodes,ingest}.go` · P22 · Covers: FR6043, FR6045
### S13 — Workflow rules [W1-F]
- A `C/domain/wfrules/{load,rules}.go` · P7 · Covers: FR6037, NFR6003
### S14 — ZIP entry [W1-G]
- A `C/adapters/zipentry/zipentry.go` [K17] · Covers: FR6042
### S15 — Runner domain [W1-G]
- A `R/domain/{context,diff,gate,payload,artifact,errors}.go` · P21, P22 · Covers: FR6050, FR6052
### S16 — Goldens evals, ci [W1-K]
- A `Q/testdata/{gen,corpus,goldens}/{evals,ci,wfrules}*`; M `gen/{generate.mts,README.md}` · P15 · Covers: FR6056 · Done when: rerun identical, `../dev-digest` clean [K19]
### S17 — Goldens runner [W1-K]
- A `Q/testdata/{gen,corpus,goldens}/runner*` · Covers: FR6056 · Done when: as S16
### S18 — Eval demo seed [W1-L]
- A `I/seed/evaldemo.go`; M `I/seed/seed.go`, `I/seed/adapters/postgres/store.go`, `S/testdata/contracts/*` · P19 · Covers: AC6068 · Done when also: `go test ./cmd/server/...`
### S19 — Case service [W2-M]
- A `E/app/cases.go` · Covers: FR6001–FR6005, FR6017
### S20 — Run service [W2-M]
- A `E/app/{runs,runall,views}.go` · Covers: FR6006–FR6012, FR6019, FR6024, FR6027
### S21 — Executor [W2-N]
- A `E/app/executor/{executor,case,record,finish,shutdown}.go` · P12 · Covers: AC6019, FR6020, FR6021, FR6023, FR6025, FR6026
### S22 — Cases store [W2-O]
- A `E/adapters/postgres/{rows,cases,sources,snapshots}.go` [K12][K18] · Covers: AC6008
### S23 — Runs store [W2-O]
- A `E/adapters/postgres/{runs,queries}.go` · Covers: AC6022
### S24 — Evals HTTP [W2-P]
- A `E/adapters/peers/providers.go`, `E/transport/http/{routes,dto}.go` · P14 · Covers: FR6002, EC6052
### S25 — Export service [W2-Q]
- A `C/app/export.go` · Covers: FR6033, FR6038, FR6039, FR6047
### S26 — Ingest service [W2-Q]
- A `C/app/ingest.go` · P11 · Covers: FR6040, FR6041, FR6044
### S27 — CI store [W2-R]
- A `C/adapters/postgres/{rows,export,ingest,lists}.go` [K16] · Covers: AC6045, FR6030, FR6031
### S28 — GitHub peer, runner cache [W2-S]
- A `C/adapters/peers/github.go`; M `C/adapters/runnerbin/runnerbin.go` · Covers: FR6054
### S29 — CI HTTP [W2-S]
- A `C/transport/http/{routes,dto}.go` · Covers: FR6029, FR6032
### S30 — Runner app [W2-T]
- A `R/app/{run,manifest,skills}.go`, `R/adapters/{ghapi,env}/*.go` · P2 · Covers: FR6049, FR6051, FR6053
### S31 — Runner main, parity [W2-T]
- M `N/main.go`; A `N/{main,parity,secrets}_test.go` · P16 · Covers: AC6050, AC6051, AC6053, AC6054
### S32 — Domain parity [W2-U]
- A `Q/{evals,ci,wfrules}_test.go` · Covers: AC6021, AC6036–AC6039, AC6046, AC6066
### S33 — Composition root [W3-V]
- M `S/{app,main,flows_test}.go`, `docker-compose.yml`, `Makefile`, `backend/AGENTS.md` · P3, P9, P12, P20, P23 [K5] · Covers: FR6013, FR6024, FR6025
- Done when: `make backend-check` + `docker compose build backend`
### S34 — Component: evals [W4-X]
- A `S/phase6_evals_test.go`, contracts `eval_*` · Covers: AC6001–AC6007, AC6009–AC6020, AC6023–AC6027, AC6065
### S35 — Component: CI [W4-X]
- A `S/phase6_ci_test.go`, contracts `ci_*` · Covers: AC6028–AC6035, AC6040–AC6044
### S36 — Component: runner, security, ops [W4-X]
- A `S/phase6_ops_test.go` · Covers: AC6049, AC6052, AC6055, AC6059, AC6064, AC6067
### S37 — Frontend contract test [W4-X]
- M `frontend/src/test/go-backend-contracts.test.ts` · Covers: AC6057 · Done when: `cd frontend && pnpm exec vitest run src/test/go-backend-contracts.test.ts`
### S38 — Manual [W4-X]
- Covers: AC6047 (`--no-cache` ×2), AC6058, AC6060–AC6063 (AC6062/3 user) · Done when: reported

## Execution
- W0: Z S1 ∥ Y S2 ∥ X S3 ∥ W S4
- W1: A S5 ∥ B S6 ∥ C S7→S9 ∥ E S10→S12 ∥ F S13 ∥ G S14→S15 ∥ K S16→S17 ∥ L S18
- W2: M S19→S20 ∥ N S21 ∥ O S22→S23 ∥ P S24 ∥ Q S25→S26 ∥ R S27 ∥ S S28→S29 ∥ T S30→S31 ∥ U S32 · W3: V S33 · W4: X S34→S38

## Contracts & migrations
None (go.mod: `yaml.v3` direct).

## Verification
- Gate per wave: `make backend-check` (Makefile:111); W3 + `docker compose build backend` (Makefile:34); W4 + S37
- Manual: S38
- Review risk: high — embedded binary, token, hostile YAML/ZIP, shutdown, rows, cost

## Open questions / assumptions
None blocking; S4 proves P2, P3, sizes
