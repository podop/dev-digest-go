# Development Plan: Go backend Phase 3 (repo-intel)
Plan: Go backend Phase 3 · Status: done · Spec: specs/2026-10-22-go-backend-phase-3.md · Base: feat/go-backend-phase-1@8b6c896
Packages: backend (+1 frontend test) · Context pack: docs/plans/2026-10-22-go-backend-phase-3.context.md
Execution: multi-agent (user)

## Goal
Clones are indexed (D3002) into the 7 repo-intel tables on clone/refresh/resync/startup; the §7.2 routes work with the unchanged UI; the §7.3 port feeds the conventions sampler.

## Out of scope
Spec §3. No migration (D3013); Phase 1/2 bodies unchanged.

## Planner decisions
- P1 W0 (S1) owns go.mod and writes shared types + app ports as code [C0]
- P2 Pins [F1]: go-tree-sitter v0.25.0, tree-sitter-typescript v0.23.2, tree-sitter-javascript v0.25.0, tiktoken-go/tokenizer v0.7.0
- P3 **CRITICAL** ast-grep runs tree-sitter 0.26, Go's binding stops at 0.25: differing error-recovered trees (EC3005) get hand-written expectations
- P4 Invalid UTF-8: WHATWG decoder, not `[]rune` [F2]
- P5 Facts regexes for RE2: quote alternation for `\2`, ECMAScript `\s` class [F3]
- P6 `git diff --name-only --no-renames -z <base> <head> --`; SHAs must be hex; > 1 MiB → `ErrOutputTooLarge`
- P7 `(*postgres.Tx).Savepoint` (InTx only joins) [C1]
- P8 Faults only from tests: SQL triggers that raise or wait on a test-held advisory lock
- P9 AC3067: import test (no `net*`/`os/exec` in `R/`) + fake git
- P10 Parser takes ctx; 2,000 ms via `ProgressCallback`; failed file → no specifiers
- P11 Graph builder = app component behind a port; panic/unexpected error → `graphFailed`
- P12 tsconfig: own JSON5 subset (comments, trailing commas, single quotes, bare keys)
- P13 Re-dedupe symbols by clamped (name, kind, line) before insert (unique key)
- P14 Insert chunks of 500 rows
- P15 Incremental re-parses unchanged files for imports only
- P16 repos → `indexTrigger` port [C3]; conventions → optional `Ranker` [C4]
- P17 Golden generator = checkout's own tsx, writes only here [F5]
- P18 Build `golang:1.27-alpine3.23` + `build-base`, `CGO_ENABLED=1`; runtime unchanged
- P19 Walk = `clonefs.Walk` (uncapped) + rules → byte sort → 5,000 bound
- P20 `(*jobs.Runner).Registered` for AC3013 [C2]
- Spec defaults accepted: D3001–D3019, Q3002–Q3005

## Steps
`R/`=`backend/internal/repointel/`, `I/`=`backend/internal/`, `P/`=`I/platform/`. Go per [K8]; DB tests via pgtest, skip `-short`. Done when = `go test -race -count=1 <step pkgs>/...` passes.

### S1 — Skeleton, deps, CGO smoke [W0-Z]
- Files: M go.mod/sum; A `R/domain/{model,utf16}.go`, `R/app/ports.go`, `R/adapters/treesitter/languages.go`, `R/adapters/tokenizer/`+tests
- Change: [C0]; FR3027 (count specials as text; fallback + warning) · Covers: AC3054 (special), FR3031 · Tests: parse each grammar under `-race`

### S2 — gitcli DiffNames [W1-A]
- Files: M `P/gitcli/gitcli.go`+test · P6 · Covers: FR3016, EC3018

### S3 — Savepoint, Registered [W1-A]
- Files: M `P/postgres/tx.go`, `I/jobs/runner.go`+tests · P7, P20 · Covers: AC3013 (runner)

### S4 — REPO_INTEL_ENABLED [W1-A]
- Files: M `I/config/config.go`+test, `.env.example`, `docker-compose.yml` · Covers: AC3065 · Done when also `docker compose config -q`

### S5 — Domain: text, walk rules, facts, state [W1-B]
- Files: A `R/domain/{text,walkrules,facts,state}.go`+tests
- Change: P4, SHA-1, clamp; FR3013 rules; §7.4 facts; §7.2 view + synthesised; status rules
- Covers: AC3002, AC3045, AC3047

### S6 — Clone tree [W1-B]
- Files: A `R/adapters/clonetree/`+tests · P19; read cap; `SourceFS` · Rules: security A01 · Covers: AC3024, NFR3004

### S7 — Symbols [W1-C]
- Files: A `R/adapters/treesitter/{parser,symbols,signature}.go`+tests · §7.4, P10, [F4] · Covers: AC3042, AC3044

### S8 — References, imports [W1-C]
- Files: A `R/adapters/treesitter/{references,imports}.go`+tests · §7.4 refs, §7.5 specifiers (TS elision) · Covers: AC3043, AC3049

### S9 — Rank, map, read rules [W1-D]
- Files: A `R/domain/rank/{pagerank,repomap,reads}.go`+tests · §7.6, §7.7, §7.3 junk filter, CriticalPaths · Covers: AC3051–AC3053

### S10 — Import resolution [W1-D]
- Files: A `R/domain/importgraph/{resolve,tsconfig,json5,builtins,edges}.go`+tests · §7.5, D3019, P12 · Covers: AC3049, AC3050

### S11 — Parity corpus, generator [W1-E]
- Files: A `R/parity/testdata/{corpus/**,gen/*,goldens/*.json,graph-deviations.json}` · [F5]
- Covers: AC3068 · Done when: generator run twice, then `git diff --exit-code -- backend/internal/repointel/parity/testdata` after `git add -N`

### S12 — Write store [W2-F]
- Files: A `R/adapters/postgres/{rows,write}.go`+tests · FR3028–FR3032, P13, P14 · Rules: [K2][K3][K10][K12]
- Covers: AC3048, AC3057, AC3058, AC3059

### S13 — Read store [W2-F]
- Files: A `R/adapters/postgres/read.go`+test · state, repo, backfill list, §7.3 queries [K11], §7.7 candidates · Covers: AC3016, AC3061

### S14 — Indexer [W2-G]
- Files: A `R/app/{indexer,full,incremental,parse,graph,failure}.go`, `fakes_test.go`+tests
- Change: FR3011–FR3018, FR3021, FR3036, FR3037, P10, P11, P15; workers max(1,CPU−1); injected clock · Rules: [K9][K13]
- Covers: AC3022, AC3026, AC3028, AC3031–AC3037, AC3039, AC3040, AC3046

### S15 — Jobs, service, reader, backfill [W2-G]
- Files: A `R/app/{jobs,service,reader,backfill,doc}.go`+tests · FR3005–FR3010, FR3033, FR3034, FR3039, [C3]–[C5]
- Covers: AC3016, AC3018, AC3021, AC3062, AC3063

### S16 — HTTP [W2-H]
- Files: A `R/transport/http/`+tests · §7.2; no body read · Rules: [K1][K5] · Covers: AC3001–AC3006, AC3008

### S17 — Repos triggers [W2-I]
- Files: M `I/repos/app/{jobs,jobs_test,fakes_test}.go` · FR3006, FR3007 · Covers: AC3014, AC3015

### S18 — Ranked samples [W2-I]
- Files: M `I/conventions/app/{job,ports,job_test,fakes_test}.go` · FR3035 · Covers: AC3064

### S19 — Parity tests [W2-J]
- Files: A `R/parity/parity_test.go` · every S11 golden · Covers: AC3042, AC3043, AC3045, AC3047, AC3049, AC3051, AC3053, AC3054, AC3068

### S20 — Composition root [W3-K]
- Files: M `backend/cmd/server/{app,flows_test}.go`, go.mod/sum (tidy)
- Change: wire; 3 kinds at 120 s; backfill before listen, non-fatal [K7]; test options
- Covers: AC3013, AC3017 · Done when: `go build ./... && go vet ./... && go test -race -count=1 ./cmd/server/...`

### S21 — Image [W3-L]
- Files: M `backend/Dockerfile`, `backend/AGENTS.md` (CGO gotcha) · P18 · Covers: NFR3011 · Done when: `docker compose build backend`

### S22 — Pipeline integration [W3-N]
- Files: A `R/integration/`(tests + testdata) · real adapters, pgtest, fake git, P8, P9
- Covers: AC3019, AC3020, AC3022, AC3023, AC3025, AC3027, AC3029–AC3041, AC3055–AC3061, AC3067, AC3070, AC3071

### S23 — Component tests, goldens [W4-M]
- Files: A `backend/cmd/server/phase3_test.go`, 5 `testdata/contracts/repo_{index_state_full,index_state_degraded,index_state_synth,resync_accepted,resync_degraded}.json`
- Covers: AC3001, AC3003, AC3005–AC3007, AC3009–AC3011, AC3014–AC3017, AC3019, AC3064

### S24 — Frontend contract test [W4-M]
- Files: M `frontend/src/test/go-backend-contracts.test.ts` · Covers: AC3011 · Done when: `cd frontend && pnpm exec vitest run src/test/go-backend-contracts.test.ts`

### S25 — Manual [W4-M]
- Covers: AC3012, AC3063, AC3066, AC3068 (rerun), AC3069 · Done when: results reported

## Execution
- Lanes: disjoint files, steps in order; only S1, S20 touch go.mod/sum.
- W0: Z S1 · W1: A S2→S3→S4 ∥ B S5→S6 ∥ C S7→S8 ∥ D S9→S10 ∥ E S11
- W2: F S12→S13 ∥ G S14→S15 ∥ H S16 ∥ I S17→S18 ∥ J S19 · W3: K S20 ∥ L S21 ∥ N S22 · W4: M S23→S24→S25

## Contracts & migrations
Vendored contracts unchanged; no migration.

## Verification
- Gate per wave: `make backend-check` (Makefile:111); W3 + `docker compose build backend`; W4 + S24 command
- Manual: S25 (AC3066 in a throwaway compose project)
- Review risk: high — CGO/musl, `-race`/checkptr on cgo, cancellation, lock/savepoint, persisted data, untrusted clones

## Open questions / assumptions
- None blocking (P3 may be revisited)
