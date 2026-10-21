# Rubrics — what goes where, and what is worth writing

## Contents
- Sections (the 7 fixed rubrics)
- Qualifies / does not qualify
- Good vs bad entries
- Package routing edge cases

## Sections

| Section | Write here when… | Shape |
|---|---|---|
| **What Works** | an approach solved a real problem here after other attempts, and will apply again | `<where>: <approach> → use when <situation>` |
| **What Doesn't Work** | an approach failed or was abandoned — incl. what the agent keeps reaching for wrongly. Most valuable section: never skip it | `<where>: <approach> fails because <cause> → do <instead>` |
| **Codebase Patterns** | a convention/decision the code relies on but doesn't state (decided this session, or discovered) | `<where>: <rule> — <why>` |
| **Tool & Library Notes** | a dependency quirk: version, flag, limit, default | `<lib@version>: <quirk> → <workaround>` |
| **Recurring Errors & Fixes** | an error seen more than once, or certain to recur | `` `<error text>` in <where> → cause: <x> → fix: <y> `` |
| **Session Notes** | a session produced at least one other entry or a lasting decision | `<what changed>; left: <what's open>` |
| **Open Questions** | suspected but unverified, or deliberately left unresolved | `<question> — seen in <where>` |

## Qualifies (all must hold)
- **Counterfactual:** if this entry vanished, would the next agent — even after reading the code, tests, README and CLAUDE.md — likely repeat the mistake or redo the investigation? (Every `ce-compound`)
- **Recurs:** project-specific and will matter again; not a one-off typo. (evoleinik)
- **Worth it:** saves 5+ minutes or prevents a wrong turn. (evoleinik)
- **Verified:** observed this session — ran, failed, measured, fix confirmed working. Otherwise → Open Questions. (evoleinik, learning-loop)

## Does not qualify
- Generic programming knowledge ("await inside loops is slow").
- Anything already in `README.md`, `docs/`, `CLAUDE.md`/`AGENTS.md`, or obvious from the code.
- Effort, diff size or "task finished" by themselves. (Every `ce-compound`)
- Fixes whose code/test already makes the lesson obvious.
- Anything needing a paragraph — that's documentation: write `<package>/docs/<topic>.md`, then add a one-line entry pointing to it.

## Good vs bad

Bad → why:
- `- async is tricky in the executor` → no where, no what, no action.
- `- fixed the flaky test` → one-off; the fix is in the code.
- `- echo handles errors` → generic knowledge.

Good (shape of a real entry):
- `- 2026-10-21 — internal/platform/httpx: Echo v5's ErrNotFound/ErrMethodNotAllowed are unexported types, so errors.As(err, &*echo.HTTPError) misses them and unknown routes became 500 → match echo.HTTPStatusCoder`
- `- 2026-09-21 — src/vendor/shared: the UI's wire contract; a Go response field renamed to camelCase silently breaks a screen → keep the contract's snake_case json tags`

## Package routing
- `backend/**` → `backend` · `frontend/**` → `frontend`
- `docker-compose.yml`, `.env.example`, root scripts, `.github/` → the package whose workflow it serves
  (backend image/DB/migrations → `backend`; frontend image/build → `frontend`)
- `docs/migration/**`, `specs/**` → not packages: decisions live there, not in INSIGHTS.md
- Two packages interact (e.g. a contract mismatch) → the package where the next agent will hit the problem.
  Never copy one entry into two files.
