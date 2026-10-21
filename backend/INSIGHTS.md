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

## Codebase Patterns
<!-- conventions and architectural decisions not obvious from the code -->

## Tool & Library Notes
<!-- dependency quirks, versions, flags -->
- 2026-10-21 — echo/v5@5.4.0 middleware.CORSWithConfig: panics at setup when AllowOrigins is empty (seen in tests building httpx.New(nil, …)) → register CORS only when origins are configured
- 2026-10-21 — echo/v5@5.4.0 internal/platform/httpx: ErrNotFound/ErrMethodNotAllowed are unexported *httpError types, so errors.As(err, &*echo.HTTPError) misses them and unknown routes rendered as 500 → match echo.HTTPStatusCoder (StatusCode()) and read the message from *echo.HTTPError only when present

## Recurring Errors & Fixes
<!-- error message → cause → fix -->
- 2026-10-21 — `go: no such tool "compile"` when running go build/test from an IDE terminal or a Claude session started from IntelliJ → cause: inherited GOROOT=/mnt/c/Users/<user>/go/go1.27.3 (Windows SDK) overrides the Linux toolchain → fix: unset GOROOT (or set the IDE Go SDK to ~/.local/go)

## Session Notes
<!-- YYYY-MM-DD — one-line summary of a meaningful session -->
- 2026-10-21 — backend skeleton: Echo v5 + GORM, error envelope/404/CORS/health matching the Node API, Alpine+git image, compose stack on 3100/3101/5433; left: Phase 1 spec → plan → implementation

## Open Questions
<!-- what is still unresolved -->
