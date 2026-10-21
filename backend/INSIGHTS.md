# Insights — backend

Lessons learned in `backend/` that the code doesn't tell you. Written by the
`engineering-insights` skill via `.claude/skills/engineering-insights/scripts/append_insight.py`.
**Append only** — new bullets go on top of a section; existing lines are never changed by agents.
Format: `- YYYY-MM-DD — <where>: <fact> → <action>`.
Reviewed monthly: stale entries are removed in a dedicated commit.

## What Works
<!-- approaches and solutions that worked here -->

## What Doesn't Work
<!-- dead ends and anti-patterns — the most valuable section -->
- 2026-10-22 — internal/platform/httpx + transports: reading the request body as JSON regardless of Content-Type let any website send text/plain POST/PUT (no preflight) that overwrote saved secrets — security review r1, confirmed live → read bodies only via httpx.ReadJSONBody (JSON only for application/json; text/plain → 422 "Expected object, received string"), keep the foreign-Origin 403 and ALLOWED_HOSTS guards in httpx.New

## Codebase Patterns
<!-- conventions and architectural decisions not obvious from the code -->
- 2026-10-22 — platform/gitcli Sync: a depth-1 clone is single-branch, so `git fetch origin <branch>` would not update origin/<branch> for other branches → Sync fetches with the explicit refspec +refs/heads/<b>:refs/remotes/origin/<b> before reset --hard
- 2026-10-22 — migrations/0001_baseline: the Node schema after drizzle 0000–0020 has 46 tables, not the 44 written in research 02 and the Phase 1 spec (pg_tables on a migrated DB + meta/0020_snapshot.json) → trust TestUp_MatchesNodeSchema (diff vs the verbatim Node files in migrate/testdata/drizzle), not the docs

## Tool & Library Notes
<!-- dependency quirks, versions, flags -->
- 2026-10-22 — gorm@1.31 + pgx: a custom sql.Scanner is not called for SQL NULL — the field keeps its zero value — so a nullable jsonb read needs an explicit len==0/nil check (settings/adapters/postgres List failed on NULL until handled)
- 2026-10-22 — echo/v5@5.4.0 router: OPTIONS on a registered path answers 204 by default even without preflight headers → set RouterConfig.OptionsMethodHandler to return echo.ErrMethodNotAllowed so it becomes 404 not_found like Fastify; real preflights are answered by the CORS Pre middleware
- 2026-10-22 — echo/v5@5.4.0: HTTPErrorHandler runs in ServeHTTP after the Pre/Use chains return, so a Pre request-log middleware sees status 200 for failed requests → call c.Echo().HTTPErrorHandler(c, err) inside the logger (internal/platform/httpx/reqlog.go)
- 2026-10-21 — echo/v5@5.4.0 middleware.CORSWithConfig: panics at setup when AllowOrigins is empty (seen in tests building httpx.New(nil, …)) → register CORS only when origins are configured
- 2026-10-21 — echo/v5@5.4.0 internal/platform/httpx: ErrNotFound/ErrMethodNotAllowed are unexported *httpError types, so errors.As(err, &*echo.HTTPError) misses them and unknown routes rendered as 500 → match echo.HTTPStatusCoder (StatusCode()) and read the message from *echo.HTTPError only when present

## Recurring Errors & Fixes
<!-- error message → cause → fix -->
- 2026-10-22 — `\restrict <token>` / `\unrestrict` lines in a pg_dump 16.15 --schema-only output break any migration runner (psql meta-commands) → strip them (and SET/set_config/COMMENT ON EXTENSION) when regenerating a baseline from a dump
- 2026-10-21 — `go: no such tool "compile"` when running go build/test from an IDE terminal or a Claude session started from IntelliJ → cause: inherited GOROOT=/mnt/c/Users/<user>/go/go1.27.3 (Windows SDK) overrides the Linux toolchain → fix: unset GOROOT (or set the IDE Go SDK to ~/.local/go)

## Session Notes
<!-- YYYY-MM-DD — one-line summary of a meaningful session -->
- 2026-10-22 — Phase 1 implemented in 3 waves (S1–S19) + security fix round; review gate: arch PASS, security PASS (2 LOW open), plan-verify 185 PASS/0 FAIL/4 manual; left: AC95–AC97 manual checks, 2 LOW host/ALLOWED_HOSTS hardening — see docs/migration/STATUS.md
- 2026-10-21 — backend skeleton: Echo v5 + GORM, error envelope/404/CORS/health matching the Node API, Alpine+git image, compose stack on 3100/3101/5433; left: Phase 1 spec → plan → implementation

## Open Questions
<!-- what is still unresolved -->
