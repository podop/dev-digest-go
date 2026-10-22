# Migration status — handoff

Last updated: 2026-10-22 · Branch: `feat/go-backend-phase-1` (from `master` @ `dfb9fc7`) · not pushed, no PR

## Where we are
**Phases 1–4 are implemented and passed the review gate.** Phase 4 (reviews: reviewer-core engine, LLM
extensions, run bus + SSE, multi-agent — 15 routes) was finished on 2026-10-22. Phase 5 spec is being
written. Still open (user, credentials needed): Phase 1 AC97 (GitHub token), Phase 2 AC301 and Phase 4
AC4077 real-key half (OpenAI/OpenRouter key).

| Item | Phase 1 | Phase 2 | Phase 3 | Phase 4 |
|---|---|---|---|---|
| Spec | `specs/2026-10-21-go-backend-phase-1.md` | `specs/2026-10-22-go-backend-phase-2.md` — FR201–FR262, AC201–AC303, D201–D223 | `specs/2026-10-22-go-backend-phase-3.md` — AC3001–AC3071, D3001–D3019 | `specs/2026-10-22-go-backend-phase-4.md` — FR4001–FR4047, AC4001–AC4077, D4001–D4018 (D4001 intent deferred: user) |
| Plan | `docs/plans/2026-10-21-go-backend-phase-1.md` — S1–S19 | `docs/plans/2026-10-22-go-backend-phase-2.md` — S1–S25, 4 waves | `docs/plans/2026-10-22-go-backend-phase-3.md` — S1–S25, 5 waves | `docs/plans/2026-10-22-go-backend-phase-4.md` (+ `.context.md`) — S1–S29, 5 waves (W0–W4), P1–P17 |
| Backend gate | `make backend-check` green — 79 packages after Phase 4 | | | |
| Frontend | `pnpm exec vitest run src/test/go-backend-contracts.test.ts` 88/88 (Phase 1–4 goldens) | | | |
| Stack | `make up` → db, backend, frontend healthy on 127.0.0.1:5433/3101/3100; backend rebuilt 2026-10-22 with Phase 4 (seed added the PR #482 demo review: 1 review, 2 findings, 1 run) | | | |

## Phase 4 review gate (reports in `.sdd/review/2026-10-22-go-backend-phase-4/`, git-ignored)
| Reviewer | r1 (`4d1a2b3`) | r2 (fix delta `c7de1c0`) |
|---|---|---|
| Architecture | **PASS**, 2 MEDIUM (Raw EXISTS in seed; peers → llmcall adapter import) + 2 LOW | **PASS**, 0 findings (LOW 4 json-tagged event data in reviews/domain accepted) |
| Security | **FAIL**, 1 MEDIUM (caller scan ignored its 30 s bound inside a file: 31–89 s, cancel at 4 s → 51 s) + 2 LOW (queued-run cancel lost after bus eviction; SSE exemption unconditional, 105 KB per subscriber, no stream caps) | **PASS** — all closed (scan 2.6–3.2 s, cancel → 4.3 s; 2.3 KB per subscriber; caps 32/run, 256 total) |
| Plan verification | 156 PASS · 0 FAIL · 6 CANNOT_VERIFY (manual S29 rows) | **PASS** — 167 PASS · 0 FAIL · 0 CANNOT_VERIFY (DB evidence `db-tests-r2.log` 3,079 PASS / 0 SKIP; manual rows on accepted W4 evidence) |

Manual checks (S29, throwaway compose projects): AC4009 20 sequential 4-agent starts p95 46 ms (limit
500; rate limiter off — the 10/min bucket would 429 from the 11th); AC4023 `curl -N` stream open 660 s
with 34 events, not cut by Echo's ReadTimeout; AC4061 callers + repo map + rank on a 5,001-file clone
156–275 ms (limit 10 s), canaries outside the clone untouched; AC4077 mock half: headless Chrome
(`agent-browser` 0.27) 3 flows / 89 steps (single-agent review + accept/dismiss + trace; cancel + delete;
3-agent multi-agent with estimates + Learn) plus Node e2e flow 08.

Phase 4 decisions worth knowing (details in the spec/plan): reviews run without PR intent until Phase 5
(D14); SSE matches fastify-sse-v2 byte for byte (`retry: 3000`, no heartbeat, live streams end by
closing); Echo's 30 s ReadTimeout does not cut bodyless streaming GETs (W0 proof) — no server timeout
changes; runs execute in-process (no jobs rows), REVIEW_AGENT_CONCURRENCY per request, no global cap
(Q4001); shutdown: executor cancels runs, ≤ 5 s drain, completes buses, then Echo drains; startup reaps
`running` runs to failed (non-fatal); `reviews.run_id` has ON DELETE CASCADE (DeleteRun = one DELETE);
findings are read in physical order (`ctid`, as Node — may change after accept/dismiss); Node parity via
goldens from `backend/internal/reviews/parity/testdata/gen` (short TMPDIR); llm validator honours
`nullable: true`, Zod-equivalent messages; a provider panic fails only its run (pool re-raises on the
caller); queued runs re-check their row after bus eviction.

## Phase 3 review gate (reports in `.sdd/review/2026-10-22-go-backend-phase-3/`, git-ignored)
| Reviewer | r1 (`43c89c8`) | r2 (fix delta `5d7b081`) |
|---|---|---|
| Architecture (`architecture-reviewer`) | **PASS**, 2 MEDIUM (Raw for plain SELECTs; json tags on `domain.StateView`) | **PASS**, 0 findings — both fixed |
| Security | **FAIL**, 3 MEDIUM DoS from hostile clones (per-file limit not enforced after the parse; tsconfig `extends` fan-out; `paths` fan-out) + 4 LOW (NUL/invalid UTF-8 failed the index; tsconfig text in a log; go-tree-sitter per-parse leak; no row budget) | **PASS** — all closed; new LOW N1 (incremental index had no row budget) → **fixed in `12505fc`** with a regression test, no r3 run |
| Plan verification (`plan-verifier`) | 132 PASS · 2 FAIL (P3: unstable syntax-error fixtures skipped) · 21 CANNOT_VERIFY (DB tests skipped by the read-only verifier) | **PASS** — 0 FAIL · 0 CANNOT_VERIFY (DB evidence: `db-tests-r2.log`, 1,064 PASS, 0 SKIP) |

Manual checks (S25, throwaway compose project): AC3066 angular/angular depth-1 clone, 7,156 files bounded
to 5,000 — full index 92.4 s at first (limit 60 s; `ResolveReferences` ran 67 s on rows without planner
statistics), 28.7 s after the single-aggregate rewrite, status full, peak 191 MiB; AC3012 p95 3.2 ms
(limit 50 ms); AC3068 golden generator rerun byte-identical, `../dev-digest` clean; AC3069 image builds,
runs non-root, healthy ≈6 s after db.

Phase 3 decisions worth knowing (details in the spec/plan): Node parity is checked against goldens
generated by running the Node code (`backend/internal/repointel/parity/testdata/gen/README.md`; pagerank
bit-exact, repo map byte-identical, symbols/references/edges equal); deliberate graph deviations are
hand-written in `graph-deviations.json`; jobs `repo-intel-index` (after clone), `-refresh` (after the
Phase 1 refresh job), `-resync` (route), 120 s per attempt; startup backfill enqueues an index for cloned
repos without a current-version index (D3005); `REPO_INTEL_ENABLED` gates port reads only; hostile-clone
bounds: 2,000 ms per file through extraction, ≤ 32 tsconfig reads, 256 `paths` patterns × 16
substitutions, 500,000 held rows (full → partial `row_budget`, incremental → falls back to full); the
conventions sampler uses ranked files and falls back to the Phase 2 walk.

## Phase 2 review gate (reports in `.sdd/review/2026-10-22-go-backend-phase-2/`, git-ignored)
| Reviewer | r1 (`c35aba5`) | r2 (fix delta `26620b5`) |
|---|---|---|
| Architecture (`architecture-reviewer`) | **PASS**, 1 optional LOW (skills importer test uses the real in-memory catalog) | not re-run (fixes are local) |
| Security | **PASS**, 2 LOW: model output in the failed-scan log; LLM clients follow redirects with the key | **PASS** — both closed; 2 new pre-existing LOW (test-connection client follows redirects; GORM logger inlines SQL values) → **fixed in `60241b1`** with regression tests, no r3 run |
| Plan verification (`plan-verifier`) | 205 PASS · 3 FAIL · 14 CANNOT_VERIFY | **220 PASS · 0 FAIL · 2 CANNOT_VERIFY** (AC301 + S25 done-when, both the manual walkthrough) |

Manual checks done: AC295 (`GET /repos/:id/context` on a 5,000-file clone, 500 docs: 0.044–0.060 s,
limit 3 s; throwaway compose project), AC300 (grep review: Echo/GORM placement, migration 0001 only,
no AutoMigrate, all bodies via `ReadJSONBody`, single `httpx.New`).

Phase 2 decisions worth knowing (details in the spec/plan): seed agents/skills only into a new or
empty workspace (D213); `conventions.extract` 300 s via per-kind job timeouts, clone/refresh keep
120 s (D208); context "store files" are `context_files` rows, the clone is read-only (`platform/clonefs`);
the conventions→skill merge route lives in skills and runs one cross-context transaction (plan P1);
interrupted scans are failed before listen, non-fatally (Phase 1 AC33); `LLM_PROVIDER_OVERRIDE=mock`
swaps every provider for a deterministic mock with a startup warning (D214).

## Next steps (in order)
1. **Manual walkthroughs (user):**
   - Phase 2 **AC301** — with a real OpenAI/OpenRouter key and a cloned public repo: create an agent and
     pick a model; attach skills; import a skill from the catalog and from a GitHub blob URL; run a
     conventions scan, accept two rules, merge them into a skill linked to the agent; attach a repo doc
     to the agent; create/edit/rename/delete a store file; pick an OpenRouter model on Settings › Models.
     Without a key: `LLM_PROVIDER_OVERRIDE=mock` in `.env`, `docker compose up -d backend`.
   - Phase 1 **AC97** — five UI flows with a real `GITHUB_TOKEN`.
   - Phase 4 **AC4077 real-key half** — run a single-agent and a multi-agent review with a real OpenRouter
     key (the mock half passed in a headless browser).
   - Phase 3 (optional, same session as AC97): after a clone, the repo's index state turns `full`
     within about a minute (`GET /repos/:id/index-state`, or the UI's repo-intel status), and Resync works.
2. Follow-ups (README): pin base images by digest; `govulncheck` once CI exists; optional arch LOW
   (fake catalog in `skills/app` importer tests).
3. Phase 3 follow-ups (optional): the import-graph phase re-parses every walked file serially (≈8 s of
   the 28.7 s on 5,000 files) — reuse the parse phase's imports or parallelise; extraction guard ticks per
   node, so a few passes overshoot the 2,000 ms limit by ≤ 0.4 s (security r2 INFO); the 500,000-row budget
   may stop very large legitimate repos at `partial`; `Parser.SetCancellationFlag` is deprecated in
   go-tree-sitter (removed in 0.26) — revisit the leak-free cancellation in
   `repointel/adapters/treesitter/parser.go` when bumping the binding; the golden generator needs a short
   `TMPDIR` (tsx IPC socket path).
4. Phase 4 follow-ups (optional): global cap on concurrent review runs / chunks per run (Q4001 declined;
   map-reduce is one LLM call per changed file — cost risk under load); per-client fairness for the 256
   SSE slots; caller scan: one hostile line can still delay the ctx check ≈ 4.7 s (tick by whitespace
   trimmed); share one caller scan per batch across agents; findings order after accept/dismiss (needs an
   ordering column + migration); persist-path run notes keep Go prefixes (`insert review: …`).
5. **Phase 5** (intent, smart-diff, blast, brief, onboarding, performance — 11 routes, plus the review
   intent hook deferred by D14): spec in progress → plan → waves → review gate.

Recipe for a fresh-volume check without touching the real volumes: an override file that renames
`pgdata`/`backend_data` (e.g. `devdigest_go_ac_*`), `WEB_PORT=3200 API_PORT=3201 DB_PORT=5434`,
`docker compose -p devdigest-go-ac -f docker-compose.yml -f <override> …`; confirm with
`config | sed -n '/^volumes:/,$p'` before `down -v --rmi local`.

## Phase 1 record
Review gate r1/r2 PASS (`.sdd/review/2026-10-21-go-backend-phase-1/`); AC95 (backend healthy ≈6 s after
db, limit 60 s) and AC96 (key + clone survive a backend recreate) passed on a throwaway project; both
r2 security LOWs (Host port validation, `ALLOWED_HOSTS` fail-closed) fixed in `70f6f67`.

## How to resume
- Read `AGENTS.md`, `backend/AGENTS.md`, `backend/INSIGHTS.md`, `docs/migration/README.md`, this file.
- `make help` lists all tasks; Go commands need `env -u GOROOT` (IDE-inherited Windows GOROOT).
- Commits: Phase 1 `c54f630`…`70f6f67`; Phase 2 spec `430ec39`, plan `949acca`, waves `1205b67`,
  `2b24b9c`, `fab4f7b`, `c35aba5`, review fixes `26620b5`, `60241b1`.
- Phase 3: spec `8b6c896`, plan `83638d8`, waves `53ed885` (W0), `e61dcf6` (W1), `14fdafc` (W2),
  `14fe593` (W3), `43c89c8` (W4), review fixes `5d7b081`, `12505fc`.
- Phase 4: spec `f1f16bb`, plan `f63a41a`, waves `95e5146` (W0), `f0f5c39` (W1), `ba80ea6` (W2),
  `7c4609d` (W3), `4d1a2b3` (W4), review fixes `c7de1c0`.
