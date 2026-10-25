# Development Plan: Go backend Phase 4 (reviews)
Plan: Go backend Phase 4 · Status: done · Spec: specs/2026-10-22-go-backend-phase-4.md · Base: feat/go-backend-phase-1@f1f16bb
Packages: backend (+1 frontend test) · Context pack: docs/plans/2026-10-22-go-backend-phase-4.context.md
Execution: multi-agent (user)

## Goal
The 15 §7.2 routes (incl. SSE) work with the unchanged UI: background runs, live log, cancel, reviews, traces, multi-agent, Learn; mock or real providers.

## Out of scope
Spec §3. No intent (D4001), no migration, no new dependency.

## Planner decisions
- P1 W0 writes types, ports, API signatures as code [C0]
- P2 **CRITICAL** SSE vs Echo `ReadTimeout` 30 s [F1]: S2 proves it; fix: `httpx.OpenStream` clears read/write deadlines via `http.ResponseController`; fallback (S24) `ReadTimeout=0`
- P3 **CRITICAL** Shutdown: Echo start ctx cancelled only after `Executor.Shutdown` (CancelAll → wait ≤ 5 s → CompleteAll); job runner 10 s in parallel
- P4 Packages `internal/reviewer` (engine), `internal/reviews` (executor in `app/executor`), `internal/platform/runbus`
- P5 Peers via consumer ports, mapped in `reviews/adapters/peers`; own rows for agents/skills/pulls [K3]
- P6 LLM additions [C4]; conventions goldens unchanged
- P7 `Review` schema Zod-style (optional ∉ required, nullish = anyOf null) so `llm.StrictSchema` = Node golden; else `Request.ProviderSchema`
- P8 SSE JSON `SetEscapeHTML(false)`, no trailing newline
- P9 Batch ctx `context.WithoutCancel(req ctx)`; run ctx ends on bus cancel; executor owns goroutines
- P10 `NewReader(src, enabled, opts...)` + `WithCallerScan`; walk `clonetree.ScanCallers`; 30 s ctx
- P11 `pcapp.(*Service).RunContext`; git port served by `*gitcli.Client`
- P12 Trace: tag-free domain; JSON in `reviews/adapters/postgres/tracedoc.go` (`EncodeTrace`, sanitised); GET decodes strictly
- P13 Column order `x/text/collate` English [F11] · P14 `time/tzdata` in main
- P15 cmd/server seams: `llmAdapters` override, `serverHook func(*http.Server)`
- P16 Generator = Phase 3 approach [F9]; non-callable Node parts hand-written
- P17 Raw only: learn lock, estimates window, jsonb `@>`
- Spec defaults: D4001–D4018, Q4001, Q4002; Q4003 real-key half user-run

## Steps
`V/`=`backend/internal/reviewer/`, `W/`=`backend/internal/reviews/`, `I/`=`backend/internal/`, `P/`=`I/platform/`, `C/`=`backend/cmd/server/`. Go per [K21]; DB tests pgtest, skip `-short`. Done when = `go test -race -count=1 <step pkgs>/...` passes.

### S1 — Contracts, config [W0-Z]
- A `V/domain/model.go`, `V/app/ports.go`, `W/domain/model.go`, `W/app/{ports,api}.go`; M `I/config/config.go`+test, `.env.example`, `docker-compose.yml` · [C0]–[C3]
- Covers: AC4070 (config), EC4038

### S2 — SSE smoke, stream writer [W0-Y]
- A `P/httpx/stream.go`+test: real listener, `ReadTimeout` 300 ms, 2 s stream with/without `OpenStream`; §7.4 headers/frames; Host 403, GET without Origin check, CORS, one log line [K12]
- Covers: NFR4002, NFR4003, NFR4009 · Done when also: ctx outcome reported

### S3 — Rate-limit exemption [W1-A]
- M `P/httpx/ratelimit.go`+test: `RateLimit.Exempt` · Covers: FR4045, AC4007 (unit)

### S4 — gitcli base, blob [W1-A]
- M `P/gitcli/gitcli.go`+test: `BaseCommit`, `BlobAt` (§7.12) · security A01/A05 · Covers: EC4037 (git)

### S5 — Seed demo review [W1-A]
- M `I/seed/{seed.go,adapters/postgres/store.go}`; A `I/seed/review.go`+tests · §7.13 · Covers: AC4071 (seed), EC4035

### S6 — Run bus [W1-B]
- A `P/runbus/{bus,state}.go`+tests · [C2] [K11] · Covers: AC4020, AC4021 (bus), AC4024, NFR4011

### S7 — LLM extensions [W1-C]
- M `I/llm/{llm,structured,promptlog,schema}.go`, `adapters/openaicompat/openaicompat.go`+tests · [C4] [K6][K8] · Covers: AC4056 (body), AC4057, AC4058 (log)

### S8 — Mock Review [W1-C]
- A `I/llm/adapters/mock/review.go`; M `mock.go`+test · §7.10 · Covers: AC4059

### S9 — Diff, chunks, split, mode [W1-D]
- A `V/domain/{diff,chunks,split,mode}.go`+tests · Covers: AC4042, AC4043, EC4008, EC4009

### S10 — Reduce, grounding, scope, score [W1-D]
- A `V/domain/{reduce,grounding,scope,score}.go`+tests · Covers: AC4044–AC4047

### S11 — Prompt, schema [W1-D]
- A `V/domain/{prompt,wrap,schema}.go`+tests · §7.8, P7 [K23] · Covers: AC4048, AC4049

### S12 — Parity corpus, generator [W1-E]
- A `W/parity/testdata/{corpus/**,gen/*,goldens/*.json}` · P16 [K16] · Covers: AC4041, AC4076 (Node side)
- Done when: two runs identical (`diff -r` copy), `../dev-digest` clean

### S13 — CallerSignatures [W1-F]
- A `I/repointel/{domain/callers.go,adapters/clonetree/scan.go,app/callers.go,integration/callers_test.go}`+tests; M `app/{reader,doc}.go` · §7.11 [K24] · Covers: AC4060, NFR4010

### S14 — Run project context [W1-G]
- A `I/projectcontext/{domain,app}/runcontext.go`+tests; M `app/ports.go` · §7.12 · Covers: AC4054, FR4033

### S15 — Reviews domain [W1-H]
- A `W/domain/{request,status,events,prompt,trace,multiagent,learn,usage}.go`+tests · §7.2–7.6 · Covers: AC4063, EC4001–EC4007, FR4013

### S16 — Engine [W2-I]
- A `V/app/{engine,pool}.go`+tests · [C1] · Covers: AC4031, AC4033 (engine), AC4070 (map), EC4019

### S17 — LLM call, peers [W2-I]
- A `V/adapters/llmcall/`, `W/adapters/peers/`+tests · FR4034, FR4038 · Covers: AC4056, AC4058

### S18 — Store writes [W2-J]
- A `W/adapters/postgres/{rows,runs,persist,tracedoc}.go`+tests · [K9][K10][K19] · Covers: AC4011, AC4034, AC4035, FR4041

### S19 — Store reads [W2-J]
- A `W/adapters/postgres/{reads,multiagent,learn}.go`+tests · D4013 [K13] · Covers: AC4010, AC4013, AC4062, AC4064–AC4066

### S20 — Service [W2-K]
- A `W/app/{service,start,events,actions,multiagent}.go`, `fakes_test.go`+tests · [C3] · Covers: AC4001–AC4006, AC4018, AC4037

### S21 — Executor [W2-L]
- A `W/app/executor/{executor,batch,run,ending,mirror,shutdown}.go`, `fakes_test.go`+tests · D4017 [K7][K8]
- Covers: AC4025–AC4033, AC4035, AC4036, AC4039, AC4050, AC4052, AC4055, AC4073

### S22 — Transport [W2-M]
- A `W/transport/http/{routes,dto,handlers,sse,trace,limits}.go`+tests · [K1][K4] · Covers: AC4004, AC4012, AC4017, AC4019, AC4022

### S23 — Domain parity [W2-N]
- A `W/parity/domain_test.go` · Covers: AC4041 (all but runs)

### S24 — Composition root [W3-O]
- M `C/{app,main}.go`, `backend/AGENTS.md` · reap before listen [K18], P2, P3, P14, P15 · Covers: AC4067, AC4068 (wiring)
- Done when: `go build -o /dev/null ./... && go vet ./... && go test -race -count=1 ./cmd/server/...`

### S25 — Run parity [W3-P]
- A `W/parity/runs_test.go` · Covers: AC4041 (events, traces), NFR4005

### S26 — Component: routes, goldens [W4-R]
- A `C/phase4_test.go`, `C/testdata/contracts/review_*.json` [K20]
- Covers: AC4001–AC4008, AC4010–AC4016, AC4028, AC4051–AC4053, AC4062, AC4064–AC4066, AC4071, AC4075

### S27 — Component: runs, SSE, shutdown [W4-R]
- A `C/phase4_runs_test.go` · P15, [K17]
- Covers: AC4017–AC4019, AC4021, AC4022, AC4025–AC4027, AC4029, AC4030, AC4032, AC4034, AC4036–AC4040, AC4050, AC4055, AC4067–AC4069, AC4072, AC4073, AC4076

### S28 — Frontend contract test [W4-R]
- M `frontend/src/test/go-backend-contracts.test.ts` · Covers: AC4016 · Done when: `cd frontend && pnpm exec vitest run src/test/go-backend-contracts.test.ts`

### S29 — Manual [W4-R]
- Covers: AC4009, AC4023, AC4061, AC4074, AC4077 (real-key half user-run, not a gate) · Done when: results reported

## Execution
- Lanes: disjoint files, steps in order.
- W0: Z S1 ∥ Y S2 · W1: A S3→S4→S5 ∥ B S6 ∥ C S7→S8 ∥ D S9→S10→S11 ∥ E S12 ∥ F S13 ∥ G S14 ∥ H S15
- W2: I S16→S17 ∥ J S18→S19 ∥ K S20 ∥ L S21 ∥ M S22 ∥ N S23 · W3: O S24 ∥ P S25 · W4: R S26→S29

## Contracts & migrations
Contracts unchanged; no migration.

## Verification
- Gate per wave: `make backend-check` (Makefile:111); W3 + `docker compose build backend`; W4 + S28 command
- Manual: S29 (AC4023 `curl -N` ≥ 660 s)
- Review risk: high — SSE timeouts, cancel/shutdown order, concurrency, persisted data, keys/log privacy

## Open questions / assumptions
- None blocking; S2's outcome picks P2's fix
