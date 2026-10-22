# Migration status — handoff

Last updated: 2026-10-22 · Branch: `feat/go-backend-phase-1` (from `master` @ `dfb9fc7`) · not pushed, no PR

## Where we are
**Phase 1 (16 routes) and Phase 2 (agents, skills, conventions, project-context — 38 routes, plus a
minimal LLM layer pulled forward from Phase 4, README D11) are implemented and passed the review
gate.** Open before Phase 3: two manual UI walkthroughs that need the user's credentials — Phase 1
AC97 (GitHub token) and Phase 2 AC301 (OpenAI or OpenRouter key).

| Item | Phase 1 | Phase 2 |
|---|---|---|
| Spec | `specs/2026-10-21-go-backend-phase-1.md` | `specs/2026-10-22-go-backend-phase-2.md` — FR201–FR262, AC201–AC303, D201–D223 (D208 300 s job limit: user) |
| Plan | `docs/plans/2026-10-21-go-backend-phase-1.md` — S1–S19 done | `docs/plans/2026-10-22-go-backend-phase-2.md` (+ `.context.md`) — S1–S25 done, 4 waves |
| Backend gate | `make backend-check` green (gofmt, build, vet, `go test -race -shuffle=on -count=1 ./...` incl. testcontainers) — 57 packages | |
| Frontend | `pnpm exec vitest run src/test/go-backend-contracts.test.ts` 61/61 (22 Phase 1 + 38 Phase 2 goldens + mock-scan assertion) | |
| Stack | `make up` → db, backend, frontend healthy on 127.0.0.1:5433/3101/3100; rebuilt 2026-10-22 with Phase 2 (seed added 4 agents, 9 skills, 9 links to the existing workspace) | |

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
2. Follow-ups (README): pin base images by digest; `govulncheck` once CI exists; optional arch LOW
   (fake catalog in `skills/app` importer tests).
3. **Phase 3** (repo-intel: indexing, rank, repo map — 2 routes): spec `specs/2026-10-22-go-backend-phase-3.md`
   (FR3001…, AC3001–AC3071, D3001–D3019; user: D3002 CGO tree-sitter, D3019 fix Node import-graph bugs) and plan
   `docs/plans/2026-10-22-go-backend-phase-3.md` (+ `.context.md`; S1–S25, waves W0–W4, P1–P20) committed
   (`8b6c896`, `83638d8`). Next: run the waves, then the review gate. The conventions sampler switches to the ranker in S18.

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
