# Migration status — handoff

Last updated: 2026-10-22 · Branch: `feat/go-backend-phase-1` (from `master` @ `dfb9fc7`) · not pushed, no PR

## Where we are
**Phase 1 (platform + settings, repos, pulls, polling, workspace — 16 routes) is implemented and
passed the review gate**, except for manual checks that need a fresh stack or a real GitHub token.

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

## Next steps (in order)
1. **Manual checks AC95/AC96** — run on a *separate* compose project so the real volumes stay intact:
   a compose override with other ports (e.g. 3200/3201/5434) and other volume names, `up -d --build`,
   time backend health (≤ 60 s), save a fake key in Settings, add a public repo and wait for the clone,
   then `up -d --build backend` and check the key status + clone survive; remove the temporary
   project and its volumes afterwards.
2. **Manual check AC97** — five UI flows with a real `GITHUB_TOKEN` (Settings, add repo, PR list, PR
   detail with diff, comments). Needs the user's token.
3. **Security LOW #1** — `internal/platform/httpx/guard.go` `hostName` cuts at the first `:` without
   checking the port is numeric (`Host: localhost:3100.evil.com` passes; browsers cannot send it) →
   `net.SplitHostPort`, require empty/numeric port.
4. **Security LOW #2** — an `ALLOWED_HOSTS` value with no valid entry (e.g. `","`) silently disables the
   Host check → reject it in `config.Load` or log a startup warning.
5. Follow-ups (README): pin base images by digest; `govulncheck` once CI exists.
6. **Phase 2** (agents, skills, conventions, project-context — 38 routes): spec → plan → waves → review
   gate, same flow (`spec-creator` → `implementation-planner` → `implementer` lanes → architecture +
   security + plan verification).

## How to resume
- Read `AGENTS.md`, `backend/AGENTS.md`, `backend/INSIGHTS.md`, `docs/migration/README.md`, this file.
- `make help` lists all tasks; Go commands need `env -u GOROOT` (IDE-inherited Windows GOROOT).
- Commits on the branch: baseline `dfb9fc7`, docs `cafa185`, Makefile `6be89f7`, wave 1 `c54f630`,
  waves 2–3 `5b778ef`, security fixes + review docs (latest).
