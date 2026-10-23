# Design: Settings and First-run setup

Status: accepted (user, 2026-10-23) · Date: 2026-10-23 · Branch: `feat/go-backend-phase-1`
Sources: the user's Claude Design prototype "DevDigest — App prototype" (screenshots of 2026-10-23 and the decoded
bundle, `screen_settings`/`screen_onboarding`), code research of `backend/` and `frontend/`, the Node reference
`../dev-digest` (same scope as the Go port: API Keys + Feature Models only).
Follow-ups: one spec per slice in `specs/` (A–D below), each planned and implemented on its own.

## 1. Goal
Turn Settings from two sections (API Keys, Feature Models) into the full surface of the prototype — API Keys,
GitHub Integration, Workspace, Automatic Reviews, Integrations, About — and give a fresh install a three-step setup
(model key → GitHub token → first repository) instead of the bare "Add repository" page. Everything stays local:
keys never leave the machine, the API keeps its no-auth guards.

## 2. Where we are (research summary)
| Prototype element | Today | Gap |
|---|---|---|
| Settings shell, sub-nav | `/settings/[section]`: `api-keys`, `models`; other keys → "A6 finalizes…" empty state | 5 sections missing |
| Provider keys + Test connection | OpenAI, Anthropic, OpenRouter, GitHub rows; `POST /settings/test-connection` → "OK — N models available" / "Connected as @login" | no latency, no "model reachable", badge says "Configured" |
| Default model | per-feature models only (`feature_models`); every agent stores its own provider+model (NOT NULL) | new setting; used for new agents and features without an override |
| Monthly spend cap | none; `agent_runs.cost_usd` and 5 other cost columns exist | setting + month total + enforcement |
| GitHub PAT, scopes | token stored; scopes never read | read `X-OAuth-Scopes` (classic PAT only) |
| Comment identity | comments post as the PAT's user | a real bot needs a GitHub App |
| Connected repos | `/repos`, index state, add, remove | reuse |
| Workspace name / data dir | single tenant "default", not exposed; `DEVDIGEST_DATA_DIR` | rename route; read-only dir |
| Retention + prune on startup | none | setting + startup prune |
| Counts | list routes only; no learnings route | one stats route |
| Export workspace / Reset run history | none | two routes |
| Automatic Reviews | `automatic_reviews` bool and `polling_interval_min` stored but unused; no poller | leaderboard poller (in progress, FR3–FR5, D18) + trigger |
| GitHub Action card | per-agent `POST /agents/:id/export-ci`, `ci_installations`, `/ci/runs` | workspace-level view |
| Plugin export / import | Zod contracts in `contracts/productionize.ts`, table `installed_plugins`; no server code | full feature |
| About | `/health` = `{status:ok}`; `DEVDIGEST_BUILD_ID` only stamps the runner | `GET /about` |
| First-run wizard | `/` redirects to the first repo or shows an empty state → `/onboarding` (URL only) | wizard + home gate |

Facts that shape the design: secrets live in `$DEVDIGEST_DATA_DIR/secrets.json` (0600, env fallback), not in the DB
and not `~/.devdigest/credentials`; settings are rows `(workspace, user, key, jsonb)`; the CI runner is a Go binary
(`devdigest-runner`, runner version "2"), not `runner.mjs · node 22`; every key-carrying HTTP client refuses
redirects; mutating routes get the foreign-Origin 403 and Host allow-list from `httpx.New`.

## 3. Information architecture
- Settings sub-nav (prototype order, plus the existing Feature Models): API Keys · GitHub Integration · Workspace ·
  Automatic Reviews · Integrations · Feature Models · About. URL `/settings/<section>`; unknown section → API Keys.
- First-run setup at `/setup` (3 steps). Not a sidebar item: the prototype's "MORE SCREENS" group is a prototype
  index, not product navigation. Reached through the home gate and a "Run setup again" link in Settings → About.
- Home gate (`/`): no model key, no GitHub token or no repository → `/setup` at the first missing step; otherwise the
  current redirect to the first repo's PRs. `/onboarding` stays as a deep link and redirects to `/setup?step=repo`.

## 4. Design by slice

### Slice A — Settings shell, API Keys, GitHub Integration, About, First-run setup
**API Keys**
- Rows: OpenRouter (primary), OpenAI (optional: "Used only when an agent pins an OpenAI model"), Anthropic (kept —
  the app supports it; the prototype omits it). Masked value `•`×min(28, len−4) + last 4, eye toggle, "Where to get
  your key" links, status badge Connected / Not connected / Failed.
- Test connection returns, in addition to today's `{provider, ok, message}`: `latency_ms`, `model` (the default
  model or the provider's cheapest listed model) and `model_reachable`. Implementation: one authenticated model-list
  call through the existing no-redirect client, timed; reachable = the model id is listed. No completion call (no
  spend). UI: "Connection verified · <model> reachable · 142ms".
- Default model (new setting `default_model`, provider-qualified id, picked from `GET /providers/openrouter/models`):
  prefilled for new agents and the fallback for Feature Models without an override. Existing agents keep their model.
- Monthly spend cap (new setting `monthly_spend_cap_usd`, null = no cap) and "$X used this month": the month total is
  Σ cost of every LLM spend record (agent runs, intent, conventions, eval runs and suites, CI runs) since 00:00 UTC of
  the 1st. Enforcement: every route that starts LLM work (review start, multi-agent, eval start, conventions scan,
  intent/brief/onboarding generation, automatic reviews) checks the cap first and answers 402 `spend_cap_reached`
  (message names cap and total). Runs already running finish. One shared budget port, implemented once.
- Info box shows the real secrets path, from the backend.

**GitHub Integration**
- PAT row as above; Test connection → "Connected as @login" plus scopes: classic tokens report `X-OAuth-Scopes`
  (shown as "repo (read) · pull_requests (write)"-style text); fine-grained tokens report none → "Fine-grained token —
  scopes are not reported by GitHub". Missing `repo` on a classic token → warning, not a block.
- Comment identity: read-only "Comments are posted as @<login>" (the token's user). A selectable bot identity needs a
  GitHub App and is out of scope (Q2).
- Connected repositories: existing `/repos` list with default branch, "indexed N ago" from index state, Remove (with
  confirm), Add repository (existing flow). Note about branch protection, as in the prototype.

**About** — `GET /about` → `{ version, build_id, runner: { kind: "go", version }, config_format, data_dir }`.
`version` comes from a build-time variable (default `dev`), `config_format` = the agent/plugin manifest version.

**First-run setup** (`/setup?step=model|github|repo`)
- Step 1 "Connect a model provider": provider choice (OpenRouter default, OpenAI, Anthropic), key, Test → verified
  banner; Continue enabled only after a successful test. Step 2 "GitHub token": PAT, Test → login + scopes. Step 3
  "Add a repository": existing add flow, shows clone/index progress, Finish → the repo's PRs. Back on every step;
  "Step n of 3 · You can change keys later in Settings". Already-done steps show as completed (state derived from
  `secrets-status` and `/repos`; nothing new is stored).

### Slice B — Workspace
- Name: `PUT /workspace {name}` (1–80 chars) on the existing `workspaces.name`; shown in the repo switcher subtitle.
- Data directory: read-only `DEVDIGEST_DATA_DIR` (changing it needs a restart and moving files; out of scope).
- Retention: setting `retention_days` ∈ {30, 60, 90, 180, 365, null = keep}; default 90. On startup (and daily) the
  server deletes run traces and their run-log events older than the retention; runs, reviews and findings stay (they
  feed the leaderboard and Agent Performance). Logged with counts.
- Counts: `GET /workspace/stats` → agents, skills, eval cases, learnings (memory rows), repos, runs.
- Export workspace: `GET /workspace/export` → a JSON bundle of agents, skills (with versions), eval cases, learnings,
  conventions, project-context store files and settings — never secrets, clones or run history. Same envelope as the
  plugin bundle (`devdigest-plugin/v1`) with `kind: "workspace"`, so it can be re-imported through Slice D.
- Reset run history: `POST /workspace/reset-history {confirm: "<workspace name>"}` deletes runs, traces, reviews,
  findings, multi-agent runs, eval runs and learnings; keeps agents, skills, eval cases, repos, settings, secrets.
  Refused while a run is active (409). UI: typed confirmation dialog.

### Slice C — Automatic Reviews
- Settings `automatic_reviews` becomes an object: `{ enabled, agent_ids[], on_new_pr, on_new_commits }` (the bool form
  is read as `{enabled}` for compatibility); polling interval = `polling_interval_min` (shared with the background
  poll, leaderboard D18).
- Detection reuses the background poller: each upsert reports per PR whether it was inserted or its head SHA changed.
  A consumer-side port in the poller hands those events to an auto-review use case, wired in `cmd/`.
- Trigger rules: enabled; event matches a ticked trigger; PR open and not draft; PR first seen after automatic
  reviews were enabled (no backfill flood); the spend cap allows it; no auto review yet for (PR, head SHA) — stored
  as an idempotency row so overlapping polls or restarts never double-run. Then the existing review start use case
  runs the chosen agents (source `auto`). Disabled agents are skipped; an empty agent list disables triggering.
- Status: "Active · last poll 2m ago · 7 PRs tracked" = enabled flag, newest `last_polled_at`, count of open PRs;
  "Paused — spend cap reached" / "No GitHub token" / "Background poll off" states.

### Slice D — Integrations
- GitHub Action card: repositories with at least one CI installation (from `ci_installations`), last CI run status
  and time ("succeeded 4m ago"), Manage → the agent's CI tab; "Install in a repo" → a dialog choosing repo + agents
  that calls the existing per-agent export with `open_pr` for each agent.
- Plugin export: `POST /plugins/export {skill_ids?, agent_ids?, include_learnings, include_eval_cases}` → the
  `devdigest-plugin/v1` bundle as a `.zip` download; last export shown from a setting.
- Plugin import: `.zip` upload (≤ 5 MiB compressed, ≤ 20 MiB inflated, ≤ 500 entries, no absolute or `..` paths,
  reuse the skills zip-import hardening), preview → install. Installed plugins listed from `installed_plugins` with
  Enable/Disable/Remove; disabling hides the plugin's skills and agents from runs; removing deletes what it installed.
  Imported skills and agents are untrusted text: they never run until enabled and they go through the existing
  prompt-wrapping. Upload is the one place a non-JSON body is accepted: one route, `application/zip`, size-capped,
  Origin-guarded (documented exception to the JSON-only rule).

## 5. Cross-cutting
- Contracts: new and changed schemas in `frontend/src/vendor/shared/contracts` together with the backend (repo rule);
  Go goldens + the frontend contract test for every new route.
- Migrations (up + down): auto-review idempotency table (C); plugin ownership columns for installed skills/agents (D).
  Settings values need none (jsonb rows). Retention, stats, export and reset need none.
- Security: no secret ever in a response, log or export; tests assert it. Destructive routes (reset, plugin remove,
  repo remove) are POST/DELETE (Origin guard) with explicit confirmation in the body where data is lost. Upload limits
  as above. Test connection keeps its 20/min bucket.
- i18n: all new copy in `messages/en`; sidebar labels stay vendored literals.

## 6. Order and dependencies
A first (spend cap and default model are used by C and the wizard), then B and C in parallel (C also needs the
leaderboard poller merged), then D (reuses B's export envelope). Each slice: spec → plan → waves → review gate.

## 7. Decisions (P1–P8 accepted with the slice scope, user 2026-10-23)
- P1 Keep Anthropic in API Keys and Feature Models in Settings although the prototype omits them.
- P2 Test connection never spends: model reachability = listed by the provider.
- P3 Spend cap enforced at every LLM entry point with 402 `spend_cap_reached`; month = calendar month in UTC.
- P4 Comment identity read-only (token user); GitHub App out of scope.
- P5 Retention prunes traces only, not runs/reviews/findings.
- P6 Workspace export = plugin bundle with `kind: workspace`, no secrets, no history.
- P7 Automatic reviews never backfill PRs seen before enabling; idempotent per (PR, head SHA).
- P8 `/setup` gated from `/`, not in the sidebar; `/onboarding` redirects to step 3.

## 8. Answers (user, 2026-10-23)
- Scope: all four slices A–D, one spec each.
- Q1 Wizard step 1 = "Connect a model provider", OpenRouter by default, OpenAI or Anthropic selectable.
- Q2 Comment identity read-only ("Comments are posted as @login"); GitHub App out of scope.
- Q3 Spend cap blocks new LLM work (402 `spend_cap_reached`); running work finishes; month = calendar month, UTC.
- Q4 Reset run history also deletes learnings (prototype copy) — taken as the default, confirm in spec B review.
- Q5 A plugin owns what it installed: Disable hides its skills/agents from runs, Remove deletes them (migration adds ownership).
- Poll interval: Settings `polling_interval_min` drives the background poll (leaderboard D18), shared with Automatic Reviews.
