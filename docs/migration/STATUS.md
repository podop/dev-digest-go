# Migration status — handoff

Last updated: 2026-10-25 19:20 (see ▶ Resume here) · Branch: `feat/go-backend-phase-1` (from `master` @ `dfb9fc7`) · not pushed, no PR

## Where we are
**All six phases are implemented and passed the review gate. The Node backend is fully ported (wire
parity with the UI).** Phase 6 (evals 16 routes, ci 4 routes, Go CI runner — README D8/D17) finished
on 2026-10-23. Still open (user, credentials needed): Phase 1 AC97 (GitHub token), Phase 2 AC301,
Phase 4 AC4077 real-key half, Phase 5 AC5070 and Phase 6 AC6062/AC6063 (real GitHub + OpenRouter key).

Phase 5: spec `specs/2026-10-22-go-backend-phase-5.md` (FR5001–FR5057, AC5001–AC5072, D5001–D5021);
plan `docs/plans/2026-10-22-go-backend-phase-5.md` (+ `.context.md`, S1–S38, W0–W4). Phase 6: spec
`specs/2026-10-23-go-backend-phase-6.md` (FR6001–FR6057, AC6001–AC6068, D6001–D6026); plan
`docs/plans/2026-10-23-go-backend-phase-6.md` (+ `.context.md`, S1–S38, W0–W4, 24 lanes; W4 split into
parallel lanes by the user). Backend gate after Phase 6: `make backend-check` exit 0, 128 packages;
frontend contract test 148/148.

## ▶ Resume here (2026-10-25 19:20)
Branch `feat/go-backend-phase-1`, HEAD `f1832de`. Not pushed, no PR (the user decides). The user's standing orders:
- run features autonomously in multi-agent waves and lanes;
- approve plans by default and escalate only CRITICAL items (spec contradictions that change behaviour, destructive or irreversible choices, security, an NFR failure that needs a spec change);
- the order is **slice C → slice D** (then custom model providers / GitLab A–C, specs from the user's other session, not yet planned).

| Feature | State |
|---|---|
| PR Leaderboard, Settings A, Settings B, Cost ledger | done and committed (specs `implemented`, plans `done`); the stand runs A+B (not yet the ledger/C) |
| **Settings C** (automatic reviews) | W1–W3 committed `2a7f422`; **W4 tests + fix round uncommitted in the tree** (see below) |
| **Settings D** (integrations / plugins) | plan approved `f1832de` (spec D15–D18, migration **0008**); implementation not started |

**Uncommitted work in the tree (all slice C), at the moment the session was saved:**
- W4 tests:
  - `cmd/server/{autoreview_flow_test,settings_c_test}.go`;
  - golden `testdata/contracts/automatic_reviews_status.json`;
  - `autoreview/adapters/postgres/trigger_integration_test.go`;
  - the frontend contract entry.
- Fix round, in flight:
  1. `TestAutoReviewFlow_NotArmedNothingStarts` fails deterministically (line 883: no record after re-arming). Its root cause is not found yet — it is either the test or a real re-arm/enabled_at bug.
  2. Security M1 → user decision **spec D13: auto-review only same-repo branches**:
     - migration `0007_pr_head_repo` stores the PR's head repo on poll;
     - the outcome is `skipped_fork`, and a NULL head repo counts as a fork;
     - new test `cmd/server/autoreview_fork_test.go`.
     The files are partly written (pulls/github mapping, pulls store, autoreview domain/store, migrate_test).
- Spec C edits (uncommitted): D13 and D14 (AC15: text/plain → 422).

**To resume slice C:**
1. `git status`, then finish the two fixes (re-run `go test -race -count=3 -run TestAutoReviewFlow ./cmd/server`), then `make check`.
2. Commit "C W4 + fixes r1" together with spec C.
3. Security delta on the M1 fix (fork filter, migration 0007, NULL head = fork).
4. W5 (plan S20: AC16 evaluation timing + the AC22 UI walk on a throwaway compose project, tear down only that project).
5. Plan-verifier.
6. INSIGHTS wrap-up, STATUS, close spec/plan C.
7. Rebuild the stand: `docker compose up -d --build backend frontend` — migrations 0005 (ledger backfill), 0006 and 0007 apply. Check `/settings/spend` and `/settings/automatic-reviews-status`.

**Then slice D:** plan `docs/plans/2026-10-25-settings-d-integrations.md` (+ `.context.md`).
- S0: re-read C's committed files; the migration is 0008.
- Then W1–W5 multi-agent, the gate, closing, the stand.

**Orchestration notes** (git-ignored): `.sdd/orchestration/`:
- lane rules `lane-common-{b,c,ledger}.md`;
- carry-over notes `{b,c,ledger}-carryover.md` with every lane's exported API, user decisions and the review findings.
Reports for each feature: `.sdd/review/<date>-<slug>/`.

Migration numbers: 0004 B · 0005 ledger · 0006 C · 0007 C (fork filter) · 0008 D.

## After the migration — PR Leaderboard (in progress) and Settings (specs only) · 2026-10-23 night
**PR Leaderboard** (new feature, not Node parity). Spec `specs/2026-10-23-pr-leaderboard.md` (+D18–D26), plan
`docs/plans/2026-10-23-pr-leaderboard.md` (+`.context.md`, A1, A2, declared deviations under S25 and
before S21). Every step S1–S26 is implemented and committed: spec `eacb860`, plan `7ee5012`, waves
`a519fc9` (W0), `9ef3155` (W1), `ce05e58` (W2–W3, A1/A2, S26), `1b0d73e` (W3b: S25 + r1 fixes),
`a497bfe` (W4), `2b7c5d1` (W5). Gate on `2b7c5d1`: `make check` exit 0 (132 backend packages, 954 frontend tests).

Review gate (reports in `.sdd/review/2026-10-23-pr-leaderboard/`, git-ignored):
| Reviewer | r1 (working tree W0–W3) | r2 |
|---|---|---|
| Architecture | PASS, 1 MEDIUM (Raw SQL) + 1 LOW | **PASS**; MEDIUM closed in `1b0d73e`; W4 delta PASS, its LOW (AGENTS seam list) fixed in `2b7c5d1` |
| Security | PASS, LOW F1 (secondary rate limit) | **PASS**; F1 closed; INFO N1–N3 (follow-ups below) |
| Plan verification | 105 PASS / 0 FAIL, rest pending | 106 PASS / 0 FAIL; 39 container rows settled by `make check` on `2b7c5d1`; delta for S21/S22 + fix r1 still to run |
| Manual S22 (`s22-manual.md`) | — | AC20 PASS (Playwright, 0 errors); AC12 no-writes PASS; **AC12 latency FAIL**: 30d/all p95 5.7 s (limit 500 ms) at NFR1 volume, because `qWindowFindings` scans ~104k findings into Go |

**Fix round r1 for AC12 (2026-10-24): done, gate closed.** User decision: SQL aggregation with no wire change.
Brief `fix-r1-ac12-brief.md`, report `fix-r1-report.md`, assets `s22-assets/`, all in `.sdd/review/2026-10-23-pr-leaderboard/`.
- Per-PR counts, the top-3 findings (P13, LATERAL top-N) and hot files are computed in SQL (`qFindingStats`, 4 repo
  shards read in parallel; sequential inside a transaction). The domain still owns the rules. Output byte-identical
  to HEAD on NFR1 data. Goldens untouched.
- p95 at NFR1 volume (200-request mix, load 3–4): 30d/all **532 ms** (was 5,692), week/all 197, last_week/all 270,
  one-repo variants 36–56 ms. **AC12 accepted with a deviation by the user (spec D27).**
- Plan P2: ≤ 9 statements per request instead of ≤ 8 (accepted deviation, recorded under P2).
- Gate r3: architecture **PASS** (2 LOW); security **PASS** (INFO N4–N6); plan verification: 32 PASS, 4 FAIL rows,
  all of them the two accepted deviations, and 41 CANNOT_VERIFY rows (DB tests), settled by `make check`:
  130 backend packages ok incl. `cmd/server`; the 2 known load-sensitive packages (`onboarding/domain`, `repointel/domain`)
  were green on re-run; frontend 954/954.
- The throwaway project `devdigest-go-s22` is removed (volumes included).

Leaderboard follow-ups (not fixed):
- AC12 latency (D27):
  - merge `windowReviews` into the shard statement (4 shards within ≤ 8 statements);
  - parallel JSON encoding (~15 ms);
  - fewer allocations in `BuildRows` (~10 ms);
  - re-measure the 4-shard build on a quiet host.
- Security r3:
  - N4: one request holds up to 8 of 20 pool connections, so cap the fan-out with a shared semaphore.
  - N5: the store goroutines have no `recover`.
  - N6: the hot-files `repos` join lacks `workspace_id` (as r1 INFO 4, `agents` join).
- Arch r3 LOW: no test of `WindowFacts` inside a transaction.
- Backfill slot use:
  - security N1 / lane x: a PR with real 0/0/0 stats, or one whose stats fetch keeps failing, takes one of the 20
    backfill slots every cycle. Fix: remember `(pr.ID, updated_at)` after a zero result or an error.
  - Stats are backfilled only after that repo's PR pages succeed.
- Code hygiene:
  - N2: add `workspace_id` to the `SetDiffStats` WHERE.
  - N3: `Retry-After` uses the wall clock, not the injected clock.
  - arch LOW: `leaderboard/domain/rows_test.go` imports `pulls/domain` (test-only drift guard).
- UI:
  - At 1600 px the table scrolls sideways and an expanded row hides the title column (S22).
  - Agent run errors show only "OpenRouter … returned 402" and drop OpenRouter's own message; check Node parity.
    The user hit this on 2026-10-23: the Security Reviewer on claude-sonnet-5.5 ran out of OpenRouter credit.
- Load-sensitive tests: `repointel` `TestExtractSymbols_Hostile` and `TestBuild_SubstitutionFanoutIsBounded`
  failed once at load average ≈ 45; they pass on a quiet host.

**Settings.** Design `docs/design/2026-10-23-settings-first-run.md` (accepted), specs A–D (`specs/2026-10-23-settings-*.md`,
commit `74c6ebf`). Order: A, then B ∥ C (C uses the leaderboard poller), then D.
**Slice A: done and merged** (`ac69763`, merging `feat/settings-a`). Plan `docs/plans/2026-10-23-settings-first-run-setup.md`
(+ `.context.md`, S1–S30, W0–W5; commit `bd926b0`).
- After the merge, `make check` on `feat/go-backend-phase-1` was green: 137 backend packages, frontend 1134 tests.
- Commits: `fd9f82b` W0–W3 · `ebcbbf1` W4 · `c9c4689` review fixes (S1 key fragments, S22 tests) · `9dc70ca` fix r3 (AC23).
- What it adds:
  - a 7-section Settings (API Keys, GitHub Integration, Models, About + 3 placeholders);
  - a richer test connection (latency, checked model, GitHub login/scopes), with no redirects and no key fragments;
  - `default_model`;
  - a monthly spend cap: `GET /settings/spend`, and 402 `spend_cap_reached` on 8 LLM routes, failing closed;
  - `GET /about`, with version and build id stamped at link time;
  - a 3-step `/setup` gated from `/`; `/onboarding` redirects to it;
  - migration `0003` (spend indexes).
- Gate (reports in `.sdd/review/2026-10-23-settings-first-run-setup/`, git-ignored):
  - architecture r1/r2 PASS;
  - security r1/r2 PASS;
  - plan verification r2: 91 PASS, 0 FAIL;
  - `make check` green, with known flakes re-run: `platform/github` Retry-After HTTP date and `EvalCasesSection` "200 cases".
- AC23: accepted deviation D26. GET /settings/spend p95 169 ms; the no-cap path is about the baseline; with a cap set the
  check adds about 165 ms p95.
- AC25: **PASS**.
  - The mock half passed on a throwaway stack.
  - The real-key half passed on the user's stand (2026-10-25, `.sdd/review/2026-10-23-settings-first-run-setup/ac25-real.md`, git-ignored):
    OpenRouter and GitHub keyless tests (login, scopes), the 402 on 3 routes plus the toast link, one real LLM call,
    the UI walk with 0 errors, and the secrets file at 0600.
- User decisions:
  - assumptions A0–A14;
  - spec D23–D26;
  - with the cap reached, 402 wins over 409 and over the 422s checked inside a generation;
  - with the cap reached, starts with no agents also answer 402;
  - the toast link text;
  - the KeyField `onResult` prop.
- Follow-ups:
  - security S2 / D25: show "N runs with unknown cost";
  - security I7: the shared llm wire error code scrubs only the exact key (masked 401 echo in `/providers/openai/models` and run
    errors), so give it the same fixed 401/403 message;
  - I8: cap the length of the test-connection key;
  - D26: a stored month rollup, so the check is ≤ 50 ms with a cap;
  - arch LOW: `settings/app` imports `platform/github` for `github.User`;
  - 4 views still link to `/onboarding` (Leaderboard, MultiAgentList, ConfigureRun, RepoNotFound);
  - AC14 stamped build id has no component test (no `spec.build` seam);
  - `/setup` can't tell a failed index from "no state";
  - the OpenRouter test passes for any key (public model list).
  - **spend under-count (AC25 real):** `pr_intent`, `pr_brief` and onboarding keep one row per PR or repo, so a regenerate in the
    same month REPLACES the earlier cost. The stand measured a $0.00375 refresh with a $0 delta, so the cap can be exceeded.
    It follows FR12 as written; a fix needs a cost ledger or month rollup, which revises D19 (fits the D26 follow-up). User decision.
  - setup step 3 says "Add your first repository" even when repos exist.
**Slice B: done** (Workspace: name, data directory, retention prune, stats, export, reset history).
Plan `docs/plans/2026-10-25-settings-b-workspace.md`; spec D22–D28. Commits: `43bb6d3` W0–W2 · `16a25d8` W3 + review fixes ·
`69c2ab5` W4 migration 0004 (`ci_runs_agent_run_idx`).
- New `internal/workspace` context, with routes `GET`/`PUT /workspace`, `/workspace/stats`, `/workspace/export` and
  `POST /workspace/reset-history`. `GET /workspace` moved out of `repos`.
- The reset runs in one transaction: an advisory lock, then SHARE ROW EXCLUSIVE on `agent_runs` and `eval_suite_runs`, then the deletes.
- A daily trace pruner runs in `listen`.
- The Settings › Workspace section and the name in the repo switcher.
- Gate (reports in `.sdd/review/2026-10-25-settings-b-workspace/`, git-ignored):
  - arch r1: PASS (MEDIUM fixed);
  - security r1: PASS (I3 fixed: prune lock 60 s);
  - plan verification r1: 71 PASS / 0 FAIL, and r2 covers W4;
  - S19 measured at NFR3 volume:
    - stats p95 31 ms;
    - export 0.2–0.7 s;
    - reset 23–25 s with migration 0004 (37–49 s without it; thin margin, D28);
    - the startup prune keeps `/health` and trace reads p95 ≤ 31 ms;
    - the AC24 UI walk passed 21/21.
- Follow-ups:
  - the export is built in memory (about 8–9× the bundle; security L1): stream it;
  - a failed reset logs the raw PG error (L3);
  - the reset margin depends on the run_traces delete volume;
  - one 3.5 s stall seen during the prune (NFR4 probe);
  - AC21 invalidations are only partly asserted;
  - `useUpdateSettings` / `useWorkspace` show both an inline error and a global toast;
  - `/workspace` 5xx toasts on every page.
- Migration numbers: B took `0004`, the cost ledger takes `0005`, slice C takes `0006`.

**Cost ledger: done.** Spec `specs/2026-10-25-cost-ledger.md` (D1–D22), plan `docs/plans/2026-10-25-cost-ledger.md`.
Commits: `2180e3a` W1–W3 · `448156f` W4 + review fixes · `7c04c15` NFR3 async commit.
- What it adds:
  - an append-only `cost_ledger` with one entry per answered LLM call, written at the single `internal/llm` Recorder hook,
    with a Charge label at every call site, guarded by an AST test;
  - month totals in 8 slot rows;
  - a backfill inside migration 0005;
  - `unpriced_count` on `GET /settings/spend`.
- The cap reads the totals: it is fast, regenerated work adds up, and reset and prune never lower the spend.
- Gate (reports in `.sdd/review/2026-10-25-cost-ledger/`):
  - architecture r1 PASS (M1/L1 fixed);
  - security r1 PASS (L1 CI cost ceiling D19, L2 saturating totals, L3 charge guard — all fixed);
  - plan verification r1: 53 PASS / 0 FAIL, and the 21 DB rows green on a clean 448156f.
- S23 measured:
  - upgrade 25–37 s at NFR4 volume;
  - parity exact (±1 µ$ D15);
  - `/settings/spend` p95 10–18 ms;
  - the cap check +5 ms;
  - NFR3: the append p95 was 51–145 ms on a durable PG, and is 40–47 ms with async commit (D22), still marginal at load 8+.
- Follow-ups:
  - NFR3 headroom (more slots need a migration; a batched recorder);
  - security I1–I4 (unanswered calls are not recorded; the recorder keeps ctx values; `migrate down` lowers spend; backfill time grows with legacy data);
  - the 2.5M-row backfill took 546–590 s (outside the spec's NFR4 volume).

**Slice C: in progress.** W1–W3 committed `2a7f422`; W4 tests in progress.
**Slice D:** planning started (`docs/plans/2026-10-25-settings-d-integrations.md`).


## Phase 6 review gate (reports in `.sdd/review/2026-10-23-go-backend-phase-6/`, git-ignored)
| Reviewer | r1 (`4735965`) | r2 (fix delta `95da9b9`) |
|---|---|---|
| Architecture | **PASS**, 0 findings | not re-run (fixes local to the runner, ci ingest, postgres pool and one evals query — Phase 5 precedent) |
| Security | **PASS**, 3 LOW: F1 runner FAILED line could print model text, uncapped; F2 `##[`/`::` workflow-command injection in the job log; F3 a fork-PR run with a forged artifact was ingested (Node does the same) + 14 INFO | **PASS** — F1–F3 closed (class-only, 1,200 B cap; `::stop-commands::<random>` + `BreakCommands`; runs whose head repository ≠ repository are never inspected); 6 new INFO, the spec wording one fixed in `db63264` |
| Plan verification | 137 PASS · **5 FAIL** (4 undeclared deviations → spec amendment D6026; AC6057 golden missing) · 10 CANNOT_VERIFY | r2: 152 PASS · 0 FAIL (AC6060 pending); r3 (`verify-r3.md`): **155 PASS · 0 FAIL** · 3 CANNOT_VERIFY (AC6062/AC6063 user, skills row = architecture) |

Manual checks (`s38-manual.md`, `ac6060-r2.md`): AC6047 runner static, cgo off, 7.9 MB, reproducible
SHA-256 — PASS; AC6058 golden generator byte-identical, `../dev-digest` clean — PASS; AC6061 Echo/GORM
placement, no migration — PASS; **AC6060** (throwaway stack, 50 agents / 10,000 cases / 2,000 runs /
400,000 results) r1 FAIL (overview dashboard p95 453 ms from a per-agent `SELECT *`; a 50-start burst hit
Postgres "too many clients" because the pool was uncapped) → fixed in `95da9b9` (`DB_MAX_OPEN_CONNS`,
default 20; one window query for the overview) → r2 PASS: GET worst p95 178 ms (overview 85/103 ms),
start p95 25 ms isolated / 380 ms in 5×50 bursts, second `files` 0.02 s, 0 connection failures in 470 runs.

Phase 6 decisions worth knowing (details in the spec, D6001–D6026): one engine (`reviewer/app.Run`) for
reviews, evals and the CI runner; the runner is embedded only with `-tags devdigest_runner` (image build),
untagged builds answer `500 runner_bundle_missing`; the eval-start bucket is shared by agent and skill
starts (10/min per workspace) and counts before validation; evals `head_sha` is `""` where Node had null;
the workflow rules differ from Node only on the yaml.v3 cases in `wfrules_deviations.json`; ingest refuses
fork-PR runs; the backend DB pool is capped (`DB_MAX_OPEN_CONNS`, 1–90, default 20) — inside an `InTx`
body every store call must use `postgres.Conn(ctx, db)`; the PR #483 eval demo is seeded.

## Phase 5 review gate (reports in `.sdd/review/2026-10-22-go-backend-phase-5/`, git-ignored)
| Reviewer | r1 (`3551565`) | r2 (fix delta `6f5028f`) |
|---|---|---|
| Architecture | **PASS**, 1 LOW (inflight smoke test imports Echo → exception recorded in `backend/AGENTS.md`) | **PASS**, 0 findings (build/vet re-run on Go 1.27 by the orchestrator) |
| Security | **FAIL**, 2 MEDIUM (degraded blast scan 906 MiB live heap, no scan cap; `RedactSecrets` quadratic on `${` runs, 59 s) + 1 LOW (onboarding logged model text) | **PASS** — all closed (23 MiB, ≤ 2 scans at once; 0.22 s; class-only cause); new LOW N1 (changed-file indexes kept, 480 MiB) → **fixed in `c668ede`** with a regression test, no r3 run |
| Plan verification | 146 PASS · 2 FAIL (AC5053 heap test missing) · 4 CANNOT_VERIFY | 150 PASS · 0 FAIL · 4 manual → AC5063 PASS (generator rerun byte-identical, `../dev-digest` clean); AC5064 see below; AC5070 user |

Manual checks: **AC5064** (throwaway project, 5,000-file index, 10,001 runs, 50,002 findings): intent p95
5 ms, smart-diff 19 ms, onboarding 6 ms, performance 191 ms, stats 225 ms — PASS; **blast p95 569–746 ms
(limit 500) and brief p95 108–176 ms (limit 100) FAIL** on a worst-case PR whose 300 files every other
file calls (11,998 callers, 1.17 MB response; leaf-file PR: 113 / 25 ms). A perf round was stopped by
the user to move on; its unverified patch is in `ac5064-perf-wip/` (see follow-ups).

Phase 5 decisions worth knowing (details in the spec/plan): `REVIEW_INTENT_ENABLED` default on (off =
Phase 4 behaviour exactly, `executor.Deps.Intent` nil); Echo's graceful shutdown never cancels request
contexts, so intent/brief/onboarding stop via `platform/inflight` before Echo; `reviewer/domain` is the
Phase 5 shared kernel (JS-parity text, `Intent`, untrusted wrapping); no migration (D5012); degraded blast
= Node's regex clone scan, 30 s, ≤ 2 scans per process, identical scans shared; only intent refresh has a
10/min bucket (D5014); no `OnboardingTour` mock fixture (502 under the mock, as Node).

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
5. Phase 5 follow-ups: **AC5064 blast/brief latency** on caller-heavy PRs (profile, then: read enclosing
   symbols only for kept callers, slimmer row scans, cheaper brief read validation; an index only via a
   new up/down migration — D5012 allows it now that NFR5007 failed); `blastFlights` not stopped at
   shutdown (security INFO 7); user walkthrough AC5070.
6. Phase 6 follow-ups (optional, security r1/r2 INFO): the runner follows symlinks when reading
   skill/manifest files (`O_NOFOLLOW` or `Lstat` + regular-file check; a committed
   `skills/x.md → /proc/self/environ` would send the job env to the model — repo writers only); no
   bucket on `POST /agents/:id/export-ci` (each `open_pr` uploads ~5.6 MB) and the publish runs on the
   request context (no stopper); global cap on concurrent eval cases (D6014 accepted); line cap on the
   compare diff and a `notes` max length; stored eval jsonb checked for shape, not enums; workflow rules
   accept `%YAML 1.1` (GitHub's reading, not a bypass); skipped CI runs are re-inspected and the fork-run
   WARN repeats on every refresh; optional `statement_timeout` / idle-in-transaction timeout on the DSN.
   User walkthroughs **AC6062** (export-ci `open_pr` + a real workflow run + refresh) and **AC6063**
   (evals with a real OpenRouter key). A private repo needs a token whose repository access includes it
   (a fine-grained token without it answers 403 "Write access to repository not granted").
7. Ship: push the branch and open the PR to `master` (not done — ask the user first).
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
- Phase 5: spec `54f2b7e`, plan `0ab8d5b`, waves `946d1e8` (W0), `dd31aae` (W1), `8ec87d0` (W2),
  `778605c` (W3), `3551565` (W4), review fixes `6f5028f`, `c668ede`.
- Phase 6: spec `58226c0`, plan `d2440e0`, waves `bdb0da4` (W0), `5c34dff` (W1), `9cc4efd` (W2),
  `c512bec` (W3), `4735965` (W4), review fixes `95da9b9`, `db63264`.
