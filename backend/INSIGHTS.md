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
- 2026-10-22 — net/http clients that call LLM providers must not follow redirects: Go strips Authorization on a cross-host redirect but keeps custom headers like Anthropic's x-api-key, and resends the POST body on 307/308 (security r1/r2) → copy the client and set CheckRedirect to return http.ErrUseLastResponse (llm adapters, settings/adapters/providers)
- 2026-10-22 — platform/postgres GORM logger without ParameterizedQueries inlines all SQL values into failed (ERROR) and slow (WARN) query lines — a 409 duplicate skill wrote the whole skill body to the log (security r2 N2) → keep ParameterizedQueries: true in postgres.Open (TestSkills_FailedInsertDoesNotLogTheBody)
- 2026-10-22 — ordering names in tests or queries by the default collation: it puts no-then-chains before n-plus-one-gate, while the API (GET /agents, D203) orders by name COLLATE "C" → use COLLATE "C" wherever byte order is expected (seed TestSeed_WritesAgentsSkillsAndLinks failed on exactly this)
- 2026-10-22 — internal/platform/httpx hostName: Go net/http accepts a Host header whose port part is not numeric (Host: localhost:3100.evil.com reached handlers, security r2, live-checked) — cutting at the first ':' turned it into an allowed host → only an empty or 1–5-digit port counts as a match; a non-empty ALLOWED_HOSTS without a valid entry must fail closed, never disable the check
- 2026-10-22 — internal/platform/httpx + transports: reading the request body as JSON regardless of Content-Type let any website send text/plain POST/PUT (no preflight) that overwrote saved secrets — security review r1, confirmed live → read bodies only via httpx.ReadJSONBody (JSON only for application/json; text/plain → 422 "Expected object, received string"), keep the foreign-Origin 403 and ALLOWED_HOSTS guards in httpx.New

## Codebase Patterns
<!-- conventions and architectural decisions not obvious from the code -->
- 2026-10-22 — cmd/server startup: work that runs before listen against Phase 2 tables (conventions FailInterrupted) must not be fatal — DB_AUTO_MIGRATE=false on an empty DB still has to serve /health (Phase 1 AC33; TestStartup_AutoMigrateOff failed with 'relation convention_scans does not exist') → log the error and keep starting
- 2026-10-22 — unique-violation detection without importing pgx/pgconn (only an indirect dependency): errors.As(err, &interface{ SQLState() string }) and compare "23505" (skills, conventions, projectcontext postgres adapters)
- 2026-10-22 — conventions scan errors: redact provider keys before truncating to 500 chars — truncating first can leave half a key unredacted (TestExtract_RedactsKeysBeforeCutting, checked by mutation)
- 2026-10-22 — projectcontext: "store" context files are rows in the context_files table (path, content, version) — Node never writes to the clone, .devdigest/specs/ is only a path prefix; the clone stays read-only via platform/clonefs → don't add filesystem writes for context files
- 2026-10-22 — cross-context transactions (platform/postgres Tx, Phase 2 P1): a store joins the caller's transaction only if it takes its handle from postgres.Conn(ctx, db); a store using its *gorm.DB directly silently escapes the rollback → every store method used by agents AppendSkill and conventions NotAccepted/AttachSkill goes through Conn (proved by skills transport integration_test rollback cases)
- 2026-10-22 — platform/gitcli Sync: a depth-1 clone is single-branch, so `git fetch origin <branch>` would not update origin/<branch> for other branches → Sync fetches with the explicit refspec +refs/heads/<b>:refs/remotes/origin/<b> before reset --hard
- 2026-10-22 — migrations/0001_baseline: the Node schema after drizzle 0000–0020 has 46 tables, not the 44 written in research 02 and the Phase 1 spec (pg_tables on a migrated DB + meta/0020_snapshot.json) → trust TestUp_MatchesNodeSchema (diff vs the verbatim Node files in migrate/testdata/drizzle), not the docs

## Tool & Library Notes
<!-- dependency quirks, versions, flags -->
- 2026-10-22 — go get: an unpinned go get golang.org/x/text golang.org/x/net also bumps x/crypto, x/sync, x/sys → pin explicit versions (@v0.40.0, @v0.57.0) to keep the go.mod diff to the intended modules
- 2026-10-22 — go1.27 httptest.NewTestServer: srv.URL stays empty until srv.Client() (or Start) is called; handlers blocking on r.Context().Done() work inside synctest.Test over its in-memory network → build adapter base URLs after Client(); request-timeout tests then run instantly (llm/adapters/openaicompat tests)
- 2026-10-22 — platform/retry: retry.Value returns errors.Join(ctx.Err(), err) when ctx ends during a backoff, so the error shape does not tell budget expiry from caller cancellation → check which context is done after the error (internal/llm/structured.go fail)
- 2026-10-22 — postgres: ORDER BY <select alias> COLLATE "C" is invalid — only a bare alias is allowed in ORDER BY → order by the underlying expression, or by the bare alias when the keys are fixed ASCII (skills/adapters/postgres/stats.go)
- 2026-10-22 — gorm@1.31: Scan into a struct that embeds an unexported struct type leaves every field zero without an error → flatten the row struct, or add the extra column on the one row type with gorm:"column:x;->" (skills/adapters/postgres store_test)
- 2026-10-22 — Go archive/zip: File.Open verifies against the declared size and returns ErrFormat when an entry inflates past it, so it cannot cap real inflated bytes or give Node's error text → read the entry via File.OpenRaw + flate.NewReader under io.LimitReader(cap+1) (skills/domain/zipimport.go, TestReadArchive_BombIsBounded)
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
- 2026-10-22 — Phase 2 (agents, skills, conventions, project-context + minimal LLM layer) implemented in 4 waves of parallel implementer lanes; review gate arch PASS, security PASS (4 LOW fixed), plan-verify 220 PASS/0 FAIL; left: AC301 + Phase 1 AC97 UI walkthroughs — see docs/migration/STATUS.md
- 2026-10-22 — security LOW #1/#2 fixed (Host port validation, ALLOWED_HOSTS fail-closed) and AC95/AC96 passed on a throwaway compose project; only AC97 (needs GitHub token) left before Phase 2 — see docs/migration/STATUS.md
- 2026-10-22 — Phase 1 implemented in 3 waves (S1–S19) + security fix round; review gate: arch PASS, security PASS (2 LOW open), plan-verify 185 PASS/0 FAIL/4 manual; left: AC95–AC97 manual checks, 2 LOW host/ALLOWED_HOSTS hardening — see docs/migration/STATUS.md
- 2026-10-21 — backend skeleton: Echo v5 + GORM, error envelope/404/CORS/health matching the Node API, Alpine+git image, compose stack on 3100/3101/5433; left: Phase 1 spec → plan → implementation

## Open Questions
<!-- what is still unresolved -->
