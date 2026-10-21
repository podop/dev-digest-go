# Backend migration: Node/Fastify → Go

Goal: the Go backend (`backend/`) serves the same HTTP API as the Node server of
[podop/dev-digest](https://github.com/podop/dev-digest) (`server/` + `reviewer-core/`, commit
`d1c9093`), so the unchanged Next.js UI (`frontend/`, copied from `client/`) works against it.

## Research (source of truth for the specs)

| File | Content |
|---|---|
| [research/01-http-api.md](research/01-http-api.md) | all 102 routes, error envelope, validation, rate limits, SSE, client ↔ route map, wire conventions, module dependency order |
| [research/02-data-layer.md](research/02-data-layer.md) | 21 Drizzle migrations, final schema (44 tables), access patterns, seeds, row ↔ contract mapping, GORM mapping |
| [research/03-engine-integrations.md](research/03-engine-integrations.md) | reviewer-core, LLM providers, GitHub, git, repo-intel, jobs/SSE/shutdown, CI export, Go library choices |

The wire contracts are the Zod schemas vendored in `frontend/src/vendor/shared/contracts/*`
(byte-identical to the Node server's copy).

## Decisions

| # | Decision | Date | Source |
|---|---|---|---|
| D1 | Three containers: `db` (pgvector/pg16), `backend` (Go), `frontend` (Next.js), one root `docker-compose.yml` | 2026-10-21 | user |
| D2 | HTTP framework **Echo v5**; ORM **GORM** (Postgres driver on pgx) | 2026-10-21 | user |
| D3 | Schema owned by versioned SQL migrations, each with an **up and a down**; no GORM AutoMigrate | 2026-10-21 | user |
| D4 | Migrations start from a **consolidated baseline** (final schema of Drizzle 0000–0020, plus `CREATE EXTENSION vector`); later changes are new up/down pairs | 2026-10-21 | user |
| D5 | **Fresh database only**: the Go stack does not adopt an existing Node database | 2026-10-21 | user |
| D6 | Wire compatibility with the existing UI is mandatory: same paths, status codes, JSON field names (snake_case + the documented camelCase exceptions), error envelope, SSE frames | 2026-10-21 | goal |
| D7 | Backend runtime image is Alpine with `git` (the backend shells out to git) | 2026-10-21 | research 03 |
| D8 | CI export runner (Node `agent-runner` bundle vs Go runner): **decided later**, in the CI phase | 2026-10-21 | user |

## Phases

Order follows the module dependencies in research 01 §6.

| Phase | Scope | Routes |
|---|---|---|
| 1 | Platform (error envelope, validation, body limits, rate limits, security headers, health, single-tenant context, migrations, seed) + settings, repos (git clone jobs), pulls (GitHub sync, diff), workspace, polling | 2 + 14 |
| 2 | agents, skills, conventions, project-context | 38 |
| 3 | repo-intel (indexing, rank, repo map) | 2 |
| 4 | reviews: reviewer-core engine, LLM providers, run bus + SSE, multi-agent | 15 |
| 5 | intent, smart-diff, blast, brief, onboarding, performance | 11 |
| 6 | evals, ci (decides D8) | 20 |

Each phase gets its own spec in `specs/` and plan in `docs/plans/`
(sdd-engineering conventions).
