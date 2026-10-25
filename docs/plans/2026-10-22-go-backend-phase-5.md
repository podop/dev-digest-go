# Development Plan: Go backend Phase 5
Plan: Go backend Phase 5 · Status: done · Spec: specs/2026-10-22-go-backend-phase-5.md · Base: feat/go-backend-phase-1@54f2b7e
Packages: backend, frontend test · Context pack: docs/plans/2026-10-22-go-backend-phase-5.context.md
Execution: multi-agent (user)

## Goal
The 11 §7.2 routes work with the unchanged UI; reviews use the PR intent (switchable); mock or real providers.

## Out of scope
Spec §3: no migration, no go.mod module, frontend only the contract test.

## Planner decisions
- P1 W0 writes types, ports, API signatures as code [C0]
- P2 CRITICAL Drain never cancels request ctx [F4]: generations stop via `platform/inflight`; cmd shuts intent/brief/onboarding down with the executor, before Echo (S4)
- P3 CRITICAL `reviewer/domain` is the Phase 5 shared kernel (JS text, `Intent`, wrap) [K2]; AGENTS.md in S33
- P4 Six contexts, standard layout; parity test-only `Q/`
- P5 CRITICAL Switch off = `executor.Deps.Intent` nil; test config zero = off [K17]; Phase 4 goldens untouched
- P6 Brief reads intent, blast, roles, docs via ports (peers); own rows elsewhere [K3]
- P7 Peers map `github.ErrNoToken` → `GITHUB_TOKEN is not configured` [F5]
- P8 `gitcli.BlobSizeError{Size,Max}` (Is `ErrBlobTooLarge`) [F6]
- P9 Contents, merged PRs: no-redirect client copy [K6][F5]
- P10 Degraded blast reuses `WithCallerScan`, `ReferenceScanner` [K10]; hand-ported regexes [F11]
- P11 Onboarding reduces each blob to its TODO lines on read (NFR5006)
- P12 cmd seams `now`, short budgets replace component "fake clocks"; units synctest
- P13 Schemas as `V/domain/schema.go`; no llm change [C6]
- P14 Generators as Phase 4 [K16]
- P15 AC5066 via goroutine dump [F11]
- P16 AC5069 deletes PR rows by SQL [F10]; AC5072 brief allows `merge-base`/`rev-parse`
- D5001–D5021, Q5001–Q5005: spec defaults

## Steps
`I/`=`backend/internal/`, `V/`=`I/reviewer/`, `W/`=`I/reviews/`, `R/`=`I/repointel/`, `P/`=`I/platform/`, `Q/`=`I/parity/`, `C/`=`backend/cmd/server/`; `N/ S/ X/ B/ O/ F/` = `I/` intent, smartdiff, blast, brief, onboarding, performance. Tests per file; Go [K24]; DB tests pgtest. Done when = `go test -race -count=1 <step pkgs>/...`.

### S1 — Core contracts, config [W0-Z]
- A `V/domain/intent.go`, `N/domain/model.go`, `N/app/{ports,api}.go`, `R/domain/blast.go`; M `W/app/ports.go`, `W/domain/trace.go`, `I/config/config.go`, `.env.example`, `docker-compose.yml` · [C0]–[C2] · Covers: AC5036
### S2 — Feature contracts [W0-Y]
- A `{S,X,B,O,F}/domain/model.go`, `{S,X,B,O,F}/app/{ports,api}.go` [C5] · Covers: FR5003–FR5011
### S3 — JS-parity text [W0-X]
- A `V/domain/jstext.go` [C1][K11] · Covers: NFR5001
### S4 — In-flight registry [W0-W]
- A `P/inflight/{markers,flights}.go`; smoke on real Echo `StartConfig` [C3] · Covers: NFR5010 · Done when also: smoke outcome
### S5 — GitHub, git [W1-A]
- M `P/github/client.go`, `P/gitcli/gitcli.go`; A `P/github/{contents,merged}.go` · §7.13 [K9] · Covers: AC5060, AC5061
### S6 — Mock fixtures [W1-A]
- A `I/llm/adapters/mock/{intent,brief}.go`; M `mock.go` · Covers: AC5058
### S7 — Engine intent [W1-B]
- M `V/domain/{prompt,model,scope}.go`, `V/app/{engine,ports}.go` · §7.5 [F1] · Covers: AC5034, AC5037
### S8 — Trace, findings [W1-B]
- M `W/domain/trace.go`, `W/adapters/postgres/{tracedoc,persist}.go`, `W/transport/http/trace.go` [F2] · Covers: FR5025, FR5026
### S9 — Executor hook [W1-B]
- M `W/app/executor/{executor,batch,run,ending,mirror}.go` [C2] · Covers: AC5029, AC5035, FR5027
### S10 — Links, key [W1-C]
- A `N/domain/{links,key}.go` [K11] · Covers: AC5019–AC5021
### S11 — Sources, call, clamps [W1-C]
- A `N/domain/{sources,classify,clamp,schema}.go` · Covers: AC5022, AC5025
### S12 — Extractor, blast rules [W1-D]
- A `R/domain/{extract,blastpath}.go` [K10] · Covers: FR5030, FR5031
### S13 — `BlastRadius` [W1-D]
- A `R/app/blast.go`, `R/integration/blast_test.go`; M `R/adapters/postgres/read.go`, `R/app/doc.go` [K13][K20] · Covers: AC5038–AC5041
### S14 — Smart diff [W1-E]
- A `S/**` · Covers: AC5043, AC5006 (store)
### S15 — Blast domain [W1-E]
- A `X/domain/{radius,history}.go` · Covers: AC5042, FR5033
### S16 — Performance domain [W1-F]
- A `F/domain/{period,window,metrics,trends,order}.go` [K19] · Covers: AC5055, AC5017
### S17 — Performance rest [W1-F]
- A `F/{app,adapters/postgres,transport/http}/*` [K3] · Covers: AC5056
### S18 — Brief domain [W1-G]
- A `B/domain/{input,prompt,trim,normalize,schema}.go` · Covers: AC5044
### S19 — Redact, commands, inputs [W1-H]
- A `O/domain/{redact,command,input}.go` [K11] · Covers: AC5050, AC5051
### S20 — Onboarding prompt, tour [W1-H]
- A `O/domain/{prompt,trim,tour,stale,schema}.go` · Covers: AC5052
### S21 — Node goldens [W1-K]
- A `Q/testdata/{corpus,gen,goldens,handwritten}/**` · Covers: AC5063, FR5057 · Done when: rerun identical (`diff -r`), `../dev-digest` clean
### S22 — Reviews goldens [W1-L]
- A `W/parity/testdata/{gen/intent.mts,corpus/intent/**,goldens/intent.json}`; M `gen/{generate.mts,README.md}` · Done when: as S21 + Phase 4 goldens unchanged
### S23 — Intent service [W2-M]
- A `N/app/{service,flight,stale,events}.go` [C3] · Covers: AC5028, AC5027, FR5013, FR5020
### S24 — Intent rest [W2-M]
- A `N/adapters/{postgres,peers}/*`, `N/transport/http/*`, `W/adapters/peers/intent.go` · Covers: AC5023, AC5024
### S25 — Blast rest [W2-N]
- A `X/{app,adapters/{postgres,peers},transport/http}/*` · Covers: AC5007, AC5008
### S26 — Brief service [W2-O]
- A `B/app/{service,facts,logs}.go` · Covers: FR5034, FR5037, FR5039, FR5040
### S27 — Brief rest [W2-O]
- A `B/adapters/{postgres,peers}/*`, `B/transport/http/*` [K18] · Covers: AC5048
### S28 — Onboarding service [W2-P]
- A `O/app/{service,inputs}.go` [K9] · Covers: FR5041, FR5046
### S29 — Onboarding rest [W2-P]
- A `O/adapters/{postgres,peers}/*`, `O/transport/http/*` · Covers: AC5053
### S30 — Parity A [W2-Q]
- A `Q/{golden,intent,smartdiff,blast,llm}_test.go` · Covers: AC5019–AC5022, AC5025, AC5038, AC5039, AC5042, AC5043, AC5057
### S31 — Parity B [W2-R]
- A `Q/{brief,onboarding,performance}_test.go` · Covers: AC5044, AC5050–AC5052, AC5055
### S32 — Switch-on parity [W2-T]
- A `W/parity/intent_test.go`; M `W/parity/domain_test.go` [F12] · Covers: AC5030, AC5032
### S33 — Composition root [W3-U]
- M `C/{app,main}.go`, `backend/AGENTS.md` · P2, P5, P12 [K5] · Covers: FR5002 (bucket), FR5022
- Done when: `go build -o /dev/null ./... && go vet ./... && go test -race -count=1 ./cmd/server/...`
### S34 — Component: intent, hook [W4-V]
- A `C/phase5_intent_test.go`, contracts `intent_*`, `review_trace_with_intent` [K12] · Covers: AC5001–AC5005, AC5024, AC5026, AC5027, AC5029–AC5035, AC5037, AC5059
### S35 — Component: PR features [W4-V]
- A `C/phase5_pr_test.go`, contracts `smart_diff`, `blast_*`, `history*`, `brief_*` · Covers: AC5006–AC5011, AC5045–AC5047, AC5049, AC5071
### S36 — Component: repo, perf, ops [W4-V]
- A `C/phase5_repo_test.go`, contracts `onboarding_*`, `agents_performance`, `agent_stats` · P15, P16 · Covers: AC5012–AC5018, AC5054, AC5065, AC5066, AC5068, AC5069, AC5072
### S37 — Frontend contract test [W4-V]
- M `frontend/src/test/go-backend-contracts.test.ts` · Covers: AC5062 · Done when: `cd frontend && pnpm exec vitest run src/test/go-backend-contracts.test.ts`
### S38 — Manual [W4-V]
- Covers: AC5063, AC5064, AC5067, AC5070 (user-run) · Done when: results reported

## Execution
- W0: Z S1 ∥ Y S2 ∥ X S3 ∥ W S4
- W1: A S5→S6 ∥ B S7→S9 ∥ C S10→S11 ∥ D S12→S13 ∥ E S14→S15 ∥ F S16→S17 ∥ G S18 ∥ H S19→S20 ∥ K S21 ∥ L S22
- W2: M S23→S24 ∥ N S25 ∥ O S26→S27 ∥ P S28→S29 ∥ Q S30 ∥ R S31 ∥ T S32 · W3: U S33 · W4: V S34→S38

## Contracts & migrations
None.

## Verification
- Gate per wave: `make backend-check` (Makefile:111); W3 + `docker compose build backend` (Makefile:34); W4 + S37
- Manual: S38
- Review risk: high — shutdown, single flight, prompts, persisted rows, token, hostile clones

## Open questions / assumptions
None blocking; S4 confirms P2
