# Development Plan: Go backend Phase 2
Plan: Go backend Phase 2 · Status: done · Spec: specs/2026-10-22-go-backend-phase-2.md · Base: feat/go-backend-phase-1@430ec39
Packages: backend (+1 frontend test) · Context pack: docs/plans/2026-10-22-go-backend-phase-2.context.md
Execution: multi-agent (user)

## Goal
The 38 routes of spec §7 and the minimal LLM layer (D11) work with the unchanged UI; the seed adds 4 agents and 9 skills.

## Out of scope
Spec §3. No refactor of Phase 1 transports/settings.

## Planner decisions
- P1 FR250 use case + route in **skills** (app + transport) with primitive-typed ports to agents/conventions apps [C2][C3]; one tx through [C1] (D212)
- P2 NFR207 "130 s" conflicts with D208 (300 s): general bound = job limit + 10 s; AC299 kept (the 100 s request timeout ends it) — spec NFR207 updated accordingly (2026-10-22)
- P3 AC299 by synctest (in-memory stub, default limits), not a 150 s wall-clock test
- P4 A per-attempt cost from the transport (OpenRouter `usage.cost`, mock 0.00042) wins over the price book; else FR212 — needed for AC276
- P5 Seed prompts/skills and the catalog = JSON generated from Node [F9], `go:embed`, byte lengths asserted
- P6 Zip entry via `OpenRaw` + flate with its own 262,145 cap [F6]
- P7 Settings test-connection keeps its client (spec §12 "may"); compose passes the 3 new switches
- P8 LLM HTTP clients: no `http.Client.Timeout`; per-request ctx deadline [F4]
- P9 Only S11 and S22 edit go.mod/go.sum

## Steps
`P/`=`backend/internal/platform/`, `I/`=`backend/internal/`. Go per [K8]; tests per go-testing; integration via `pgtest`, skip under `-short`. Done when = `go test -race -count=1 <step pkgs>/...` passes. Contracts between lanes: [C1]–[C6] (context pack) are fixed.

### S1 — httpx: body limits, field validators [W1-A]
- Files: M `P/httpx/{body,echo,validate}.go`; A `P/httpx/fields.go`; tests
- Change: `Options.BodyLimits map[string]int64` keyed `"POST /route/path"`; Zod-style body/query helpers (UTF-16 lengths, refine `/`); `PathVersion`
- Rules: [K1][K6][K7] · Covers: AC204, AC206, AC207 (helpers) · Tests: tables

### S2 — Config switches, compose [W1-A]
- Files: M `I/config/config.go`(+test), `docker-compose.yml`, `.env.example`
- Change: §7.1 `LLM_PROVIDER_OVERRIDE`, `LLM_MOCK_DELAY_MS`, `PROJECT_CONTEXT_GLOBS`
- Covers: AC226 (`other` fails), AC280 (glob list) · Done when also `docker compose config -q`

### S3 — Tx in context [W1-B]
- Files: A `P/postgres/tx.go`+test · Change: [C1] · Covers: FR250 · Tests: pgtest rollback, nesting

### S4 — clonefs [W1-B]
- Files: A `P/clonefs/{walk,read}.go`+tests
- Change: [F5] safe read (no symlinked file, regular only, cap); BFS walk skipping symlinks, caller rules, caps
- Rules: security A01 · Covers: AC285, FR260, NFR205

### S5 — Per-kind job timeout [W1-B]
- Files: M `I/jobs/runner.go`, `runner_test.go` · Change: [C5]
- Covers: AC303 (runner) · Tests: synctest

### S6 — LLM core [W1-C]
- Files: A `I/llm/{llm,structured,extract,pricebook,prices,promptlog,featuremodel}.go`+tests
- Change: [C4]; FR205, FR209, FR210 budget, FR212, FR213 (owned goroutine, `Close`), FR215, FR217
- Rules: [K9][F3] · Covers: AC216, AC218, AC220–AC222, AC225, AC227 · Tests: fakes, synctest

### S7 — OpenAI + OpenRouter adapter [W1-C]
- Files: A `I/llm/adapters/openaicompat/`+tests
- Change: §7.2, FR206, FR208, FR210, FR214; key per call [K9]
- Covers: AC211, AC212, AC214–AC217, AC219, AC223, AC224 · Tests: synctest + `httptest.NewTestServer`

### S8 — Anthropic + mock adapters [W1-C]
- Files: A `I/llm/adapters/{anthropic,mock}/`+tests
- Change: FR207; mock §7.2
- Covers: AC213, AC223, AC226

### S9 — Conventions domain [W1-D]
- Files: A `I/conventions/domain/{sample,evidence,gate,prompt,schema}.go`+tests
- Change: §7.5 sample, evidence, gate, prompt [F8], schema + validator
- Covers: AC265, AC266, AC269, AC270

### S10 — Project-context domain [W1-D]
- Files: A `I/projectcontext/domain/{paths,globs,tokens}.go`+tests · Change: §7.6, D218
- Covers: AC280

### S11 — Skill import domain [W1-E]
- Files: A `I/skills/domain/{markdown,slug,zipimport,urlhost}.go`+tests; M go.mod/sum (`x/text`, `x/net/idna`)
- Change: §7.4 markdown, slug, zip [F6], host rules [F7]
- Rules: security A05/A10 · Covers: AC250, AC251, AC253–AC255 · Tests: tables, fuzz host

### S12 — URL fetcher, catalog [W1-E]
- Files: A `I/skills/adapters/urlfetch/`, `I/skills/adapters/catalog/{catalog.go,catalog.json}`+tests
- Change: FR239 (injected resolver/dialer, `Proxy:nil`, dial only validated IP); catalog [F9]
- Rules: security A01/A10 · Covers: AC248, AC249, AC256–AC258 · Tests: TLS httptest

### S13 — Agents core [W2-F]
- Files: A `I/agents/{domain,app,adapters/postgres}/`+tests
- Change: FR218–FR225, [C2]
- Rules: [K2][K3][K10] · Covers: AC229–AC235 · Tests: fakes, pgtest

### S14 — Agents HTTP [W2-F]
- Files: A `I/agents/transport/http/`+tests
- Change: 11 routes, FR226 · Covers: AC201, AC203, AC205, AC206, AC208, AC209, AC228, AC236

### S15 — Skills core [W2-G]
- Files: A `I/skills/domain/skill.go`, `I/skills/app/{service,importer,merge}.go`, `I/skills/adapters/postgres/{store,stats}.go`+tests
- Change: FR227–FR240, P1
- Covers: AC237–AC247, AC259, AC278, AC279 · Tests: fakes, pgtest

### S16 — Skills HTTP [W2-G]
- Files: A `I/skills/transport/http/`+tests (14 routes, `BodyLimits()`)
- Covers: AC202, AC204, AC207, AC209, AC237–AC259, AC278, AC279 · Tests: httptest+pgtest

### S17 — Conventions app, store, job [W2-H]
- Files: A `I/conventions/app/{service,job}.go`, `I/conventions/adapters/postgres/`+tests
- Change: FR241–FR249, [C3][C6]
- Rules: [K9][F3][F1] · Covers: AC260–AC263, AC267, AC271–AC275, AC277, AC299, AC302

### S18 — Conventions HTTP [W2-H]
- Files: A `I/conventions/transport/http/`+tests · Covers: AC205, AC208, AC209, AC260–AC263, AC277

### S19 — Project-context app, store [W2-I]
- Files: A `I/projectcontext/{app,adapters/postgres}/`+tests · Change: FR251–FR260 · Covers: AC281–AC294

### S20 — Project-context HTTP [W2-I]
- Files: A `I/projectcontext/transport/http/`+tests (10 routes, `BodyLimits()`) · Covers: AC204, AC281–AC294

### S21 — Seed [W3-J]
- Files: M `I/seed/seed.go`, `I/seed/adapters/postgres/store.go`; A `I/seed/knowledge.go`, `I/seed/data/{agents,skills}.json`+tests
- Change: FR261, FR262 · Covers: AC296, AC297

### S22 — Composition root [W3-K]
- Files: M `cmd/server/{app.go,flows_test.go}`, go.mod/sum (tidy)
- Change: wire [C1]–[C6], mock warning, body limits, reap before listen, price book `Close`; test options (stub URLs, job limit)
- Covers: AC201, AC226, AC264, AC303 · Done when: `go build ./... && go vet ./... && go test -race -count=1 ./cmd/server/...`

### S23 — Component tests [W4-L]
- Files: A `cmd/server/{phase2_test,phase2_llm_test}.go`, `testdata/contracts/*`
- Covers: AC201–AC203, AC205, AC209, AC210, AC219, AC225–AC228, AC233, AC245, AC264, AC268, AC272–AC276, AC278, AC279, AC297, AC298, AC303

### S24 — Frontend contract test [W4-L]
- Files: M `frontend/src/test/go-backend-contracts.test.ts` · Covers: AC210 · Done when: `cd frontend && pnpm exec vitest run src/test/go-backend-contracts.test.ts`

### S25 — Manual checks [W4-L]
- Files: none · Covers: AC295, AC300, AC301 · Done when: results reported (AC301 needs a real key)

## Execution
- Lanes of a wave have disjoint files; a lane runs its steps in order.
- W1: A S1→S2 ∥ B S3→S4→S5 ∥ C S6→S7→S8 ∥ D S9→S10 ∥ E S11→S12
- W2: F S13→S14 ∥ G S15→S16 ∥ H S17→S18 ∥ I S19→S20
- W3: J S21 ∥ K S22
- W4: L S23→S24→S25

## Contracts & migrations
Vendored contracts unchanged; no migration (D223).

## Verification
- Gate per wave: `make backend-check` — Makefile:111; W4 also the S24 command
- Manual: AC295, AC300, AC301 (S25)
- Review risk: high — SSRF, path traversal, keys, cross-context tx, concurrency

## Open questions / assumptions
- None open: D201–D223, Q201–Q207 accepted; P2 resolved in the spec (NFR207)
