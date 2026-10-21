# dev-digest-go

Mirror of [podop/dev-digest](https://github.com/podop/dev-digest) (local-first AI PR review) with
the Node/Fastify backend migrated to Go. Three containers via the root `docker-compose.yml`:

| Folder / service | Stack | Host port (loopback) |
|---|---|---|
| `backend/` → `backend` | Go 1.27 · Echo v5 · GORM (Postgres via pgx) | 3101 |
| `frontend/` → `frontend` | Next.js 15 UI copied from dev-digest `client/` (unchanged except `output: "standalone"`) | 3100 |
| `db` | pgvector/pgvector:pg16, volume `devdigest_go_pgdata` | 5433 |

Ports are configurable in `.env` (see `.env.example`); defaults avoid the Node stack on 3000/3001/5432.

## Commands

| Purpose | Command |
|---|---|
| Start / rebuild everything | `docker compose up -d --build` |
| Status / logs | `docker compose ps` · `docker compose logs -f backend` |
| Backend build · vet | `cd backend && go build ./... && go vet ./...` |
| Backend tests (gate) | `cd backend && go test -race -shuffle=on -count=1 ./...` |
| Backend fast loop | `cd backend && go test -short ./...` |
| Frontend | see `frontend/AGENTS.md` (pnpm: `test`, `typecheck`, `lint`) |

Never run `docker compose down -v` unless asked: it deletes the database and backend volumes.

## Backend migration

Roadmap, decisions (D1–D8) and research: `docs/migration/README.md`. Binding rules:
- The Go API must stay wire-compatible with the UI: same paths, status codes, JSON field names,
  error envelope `{"error":{"code","message","details?"}}`, SSE frames. Contracts:
  `frontend/src/vendor/shared/contracts/*` and `docs/migration/research/01-http-api.md`.
- Echo only in `internal/platform/httpx`, transport packages and `cmd/`; GORM only in adapters and
  `internal/platform/postgres`. Domain and app packages import neither.
- Schema changes are versioned SQL migrations with an up and a down; never GORM AutoMigrate.
- Specs: `specs/<YYYY-MM-DD>-<slug>.md` · plans: `docs/plans/<YYYY-MM-DD>-<slug>.md` (sdd-engineering).

## Engineering skills

Load these project skills with the Skill tool whenever a step touches Go code — every agent,
including `sdd-engineering:implementation-planner` and `sdd-engineering:implementer`, in addition
to their own skill tables:

| Files touched | Skill |
|---|---|
| Go packages, layout, `cmd/`, use cases, repositories, handlers, wiring, errors | `go-architecture` |
| `*_test.go`, test helpers, fakes, `testdata/`, test commands | `go-testing` |

Repository decisions above override the skills' defaults (Echo instead of `net/http` ServeMux,
GORM instead of plain SQL). The Node/TypeScript backend skills of `engineering-paved-path`
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
the same inputs (`base`, `round`, `report`, changed-file list).

## Architecture

No `backend/ARCHITECTURE.md` yet. Until one exists, the `go-architecture` skill describes the
default layering (`cmd/` → `internal/<context>/{domain,app,adapters,transport}`,
`internal/platform`); the reviewer treats it as generic guidance, not a binding rule.
