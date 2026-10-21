# dev-digest-go — agent map

Mirror of [podop/dev-digest](https://github.com/podop/dev-digest) (local-first AI PR review) with
the Node/Fastify backend migrated to Go. Three containers via the root `docker-compose.yml`:

| Package / service | Stack | Host port (loopback) |
|---|---|---|
| `backend/` → `backend` | Go 1.27 · Echo v5 · GORM (Postgres via pgx) · up/down SQL migrations | 3101 |
| `frontend/` → `frontend` | Next.js 15 UI copied from dev-digest `client/` at `d1c9093` (only change: `output: "standalone"`) | 3100 |
| `db` | pgvector/pgvector:pg16, volume `devdigest_go_pgdata` | 5433 |

Ports are configurable in `.env` (see `.env.example`); defaults avoid the Node stack on 3000/3001/5432.

## Commands
- `make help` lists every task (root `Makefile`): `make up` · `make ps` · `make logs SERVICE=backend` ·
  `make health` · `make db-shell` · `make migrate-status` · `make check` (all gates)
- Start / rebuild everything: `docker compose up -d --build` · status: `docker compose ps` ·
  logs: `docker compose logs -f backend`
- Test / build / lint: run inside the package — see `backend/AGENTS.md`, `frontend/AGENTS.md`.

## Read when
- Every task, right after the user's request and before planning/editing → read the
  `INSIGHTS.md` of each package it concerns (skill `engineering-insights`, step READ, below)
- Working on the backend migration (any phase) → read `docs/migration/README.md`
  (goal, decisions D1–D8, phases) and the research it links
- Implementing a planned feature → its spec in `specs/`, its plan + context pack in `docs/plans/`
- Touching `backend/` → `backend/AGENTS.md` · touching `frontend/` → `frontend/AGENTS.md`

## Per-package layout (convention)
Every package has the same knowledge slots:
- `AGENTS.md` (+ `CLAUDE.md` symlink) — commands, read-when, conventions, gotchas.
- `INSIGHTS.md` — lessons learned, fixed sections, append-only (skill `engineering-insights`).
- Feature specs live in the root `specs/` (one file per feature/phase); plans in `docs/plans/`
  (sdd-engineering: `specs/<YYYY-MM-DD>-<slug>.md`, `docs/plans/<YYYY-MM-DD>-<slug>.md`).

## Cross-package rules (binding)
- The Go API must stay wire-compatible with the UI: same paths, status codes, JSON field names,
  error envelope `{"error":{"code","message","details?"}}`, SSE frames. Contracts:
  `frontend/src/vendor/shared/contracts/*` and `docs/migration/research/01-http-api.md`.
- Stack decisions (user, 2026-10-21): Echo v5 for HTTP, GORM as ORM, schema owned by versioned SQL
  migrations with an up and a down (never GORM AutoMigrate). Details: `backend/AGENTS.md`.

## Engineering skills
Load these project skills with the Skill tool whenever a step touches Go code — every agent,
including `sdd-engineering:implementation-planner` and `sdd-engineering:implementer`, in addition
to their own skill tables:

| Files touched | Skill |
|---|---|
| Go packages, layout, `cmd/`, use cases, repositories, handlers, wiring, errors | `go-architecture` |
| `*_test.go`, test helpers, fakes, `testdata/`, test commands | `go-testing` |

Repository decisions override the skills' defaults (Echo instead of `net/http` ServeMux, GORM
instead of plain SQL). The Node/TypeScript backend skills of `engineering-paved-path`
(`fastify-best-practices`, `onion-architecture`, `drizzle-orm-patterns`, `zod`) do not apply to
`backend/`; `react-best-practices`, `react-testing-library`, `typescript-expert` apply to
`frontend/`; `security`, `postgresql-table-design`, `mermaid-diagram` apply everywhere.

## Engineering insights (every task)
Each package has an append-only `INSIGHTS.md` (`backend/INSIGHTS.md`, `frontend/INSIGHTS.md`) —
skill `engineering-insights`, enforced by `.claude/hooks/engineering-insights.py`:
- **READ:** right after the user's request and before planning or editing, read the `INSIGHTS.md`
  of each package the task concerns; treat it as high-confidence guidance unless told otherwise
  and say in one line which entries apply. Subagents working from a plan read its context pack
  instead of the whole files.
- **WRAP-UP:** when a task changed `backend/` or `frontend/`, re-read that `INSIGHTS.md` and append
  only new, verified, non-obvious insights via the skill's `append_insight.py` script, then run its
  `verify`. If nothing qualifies, write nothing. Never edit existing `INSIGHTS.md` lines. Do not
  skip this step — the Stop hook asks for it once per change set.

## Architecture review
Use the project agent `architecture-reviewer` (`.claude/agents/architecture-reviewer.md`), not the
plugin agent `architecture-review:architecture-reviewer`. When a workflow such as
`sdd-engineering:run-plan` asks for the plugin reviewer, delegate to `architecture-reviewer` with
the same inputs (`base`, `round`, `report`, changed-file list). Base for feature branches: `master`.

## Gotchas
- Images are built with `build.network: host`: on this WSL2 machine parallel registry downloads
  get reset on the default bridge network (`frontend/INSIGHTS.md`). Keep it.
- `NEXT_PUBLIC_API_BASE` is baked into the frontend image at build time; changing `API_PORT`
  needs a frontend rebuild.
- The Node stack may be running at the same time (ports 3000/3001/5432, volume `devdigest_pgdata`)
  — never point this stack at its database (decision D5: fresh database only).

## Do not touch
- Never `docker compose down -v` unless asked: it deletes `devdigest_go_pgdata` and
  `devdigest_go_backend_data` (all repos, clones and local secrets).
- `frontend/src/vendor/shared` contracts — change one only together with the matching backend change.
- The original dev-digest checkout (`../dev-digest`) is read-only reference material.

## On finishing a task
Run `engineering-insights` (WRAP-UP) for each package you changed; append only new, verified,
non-obvious insights via its script; if nothing qualifies, write nothing.
