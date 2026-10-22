# Migration status — handoff

Last updated: 2026-10-22 · Branch: `feat/go-backend-phase-1` (from `master` @ `dfb9fc7`) · not pushed, no PR

## Where we are
**Phase 1 (platform + settings, repos, pulls, polling, workspace — 16 routes) is implemented and
passed the review gate.** Manual checks AC95/AC96 passed on 2026-10-22 and both security LOWs are
fixed; only AC97 (needs the user's GitHub token) is open before Phase 2.

| Item | State |
|---|---|
| Spec | `specs/2026-10-21-go-backend-phase-1.md` — ready-for-planning, decisions D1–D12 |
| Plan | `docs/plans/2026-10-21-go-backend-phase-1.md` — approved, 3 waves, S1–S19 all done |
| Backend gate | `make backend-check` green (gofmt, build, vet, `go test -race -shuffle=on -count=1 ./...` incl. testcontainers) |
| Frontend | contract test `pnpm exec vitest run src/test/go-backend-contracts.test.ts` 22/22; `biome lint` passes |
| Stack | `make up` → db, backend, frontend healthy on 127.0.0.1:5433/3101/3100; migrate + seed run at startup |

## Review gate (reports in `.sdd/review/2026-10-21-go-backend-phase-1/`, git-ignored, local only)
| Reviewer | r1 | r2 (fix delta) |
|---|---|---|
| Architecture (`architecture-reviewer`) | PASS, 1 MEDIUM (GORM in `cmd/`) | **PASS**, 0 findings — rule amended (README D10) |
| Security | **BLOCK**: HIGH CSRF via `text/plain`, MEDIUM DNS rebinding, 2 LOW | **PASS** — HIGH/MEDIUM closed (README D9), 2 new LOW (below) |
| Plan verification (`plan-verifier`) | 185 PASS · 0 FAIL · 4 CANNOT_VERIFY | 185 PASS · 0 FAIL · same 4 CANNOT_VERIFY |

Manual checks and LOW fixes after r2 (2026-10-22):
| Item | Result |
|---|---|
| AC95 fresh volumes | **PASS** — throwaway project `devdigest-go-ac` (ports 3200/3201/5434, own volumes): backend container started right after db healthy (06:28:24.8Z), first healthy check 06:28:30.5Z (≈6 s, limit 60 s); `/health/ready` 200; headless Chromium on `/` ends on "Pull requests · DevDigest" for `acme/payments-api` |
| AC96 persistence | **PASS** — fake OpenAI key saved via test-connection, `octocat/Hello-World` cloned; after `up -d --build --force-recreate backend` (new container id) secrets-status `openai:true`, `/workspace` `cloned:true`, clone HEAD intact. Project, volumes and images removed afterwards; real `devdigest_go_*` volumes untouched |
| S19.tests | covered by AC95/AC96 above; AC97 part still open |
| Security LOW #1 | **Fixed** — `httpx.hostName` requires an empty or 1–5-digit port (`localhost:3100.evil.com` → 403, live-checked) |
| Security LOW #2 | **Fixed** — `config.Load` rejects an `ALLOWED_HOSTS` with no entry (`","`); `hostCheck` fails closed (403 for all) when no entry is well-formed and logs each malformed entry. Only an empty list (tests) switches it off |

## Next steps (in order)
1. **Manual check AC97** — five UI flows with a real `GITHUB_TOKEN` (Settings, add repo, PR list, PR
   detail with diff, comments). Needs the user's token.
2. Follow-ups (README): pin base images by digest; `govulncheck` once CI exists.
3. **Phase 2** (agents, skills, conventions, project-context — 38 routes): spec → plan → waves → review
   gate, same flow (`spec-creator` → `implementation-planner` → `implementer` lanes → architecture +
   security + plan verification).

Recipe for a fresh-volume check (AC95/AC96) without touching the real volumes: an override file that
renames `pgdata`/`backend_data` (e.g. `devdigest_go_ac_*`), `WEB_PORT=3200 API_PORT=3201 DB_PORT=5434`,
`docker compose -p devdigest-go-ac -f docker-compose.yml -f <override> …`; confirm with
`config | sed -n '/^volumes:/,$p'` before `down -v --rmi local`.

## How to resume
- Read `AGENTS.md`, `backend/AGENTS.md`, `backend/INSIGHTS.md`, `docs/migration/README.md`, this file.
- `make help` lists all tasks; Go commands need `env -u GOROOT` (IDE-inherited Windows GOROOT).
- Commits on the branch: baseline `dfb9fc7`, docs `cafa185`, Makefile `6be89f7`, wave 1 `c54f630`,
  waves 2–3 `5b778ef`, security fixes + review docs `5334ec0`, LOW fixes + AC95/AC96 (latest).
