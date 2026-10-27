# backend — Go API

Go 1.27 · Echo v5 · GORM (gorm.io/driver/postgres on pgx) · Postgres 16 + pgvector.
Module `dev-digest-go/backend`. Replaces the Node server of dev-digest; wire-compatible with the UI.

## Commands (from `backend/`)
From the repo root the `Makefile` wraps these with the right toolchain: `make backend-check`
(fmt + build + vet + full tests), `make backend-test-short`, `make backend-cover`, `make migrate-status`.
Prefix every Go command with `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH"` (see Gotchas).
- Build · vet: `go build ./... && go vet ./...`
- Tests (gate): `go test -race -shuffle=on -count=1 ./...` — integration tests start Postgres
  with testcontainers (Docker required)
- Fast loop (no Docker): `go test -short ./...`
- Run in the stack: `docker compose up -d --build backend` (from the repo root)
- DB (Phase 1, in progress): migrations + seed run on startup; `server migrate up|down|status`,
  `server seed`; `DB_AUTO_MIGRATE=false` / `DB_AUTO_SEED=false` switch the startup run off

## Read when
- Porting or changing a route → the module's row in `../docs/migration/research/01-http-api.md`
  (status codes, error codes, casing) and the contract in `../frontend/src/vendor/shared/contracts/`
- Touching the schema, a store, the seed → `../docs/migration/research/02-data-layer.md`
  (final schema, GORM mapping, Node access patterns)
- Touching git, GitHub, secrets, jobs, shutdown, LLM providers → `../docs/migration/research/03-engine-integrations.md`
- Implementing a phase → its spec in `../specs/` and plan + context pack in `../docs/plans/`
- Exact Node behavior in doubt → the read-only Node source `../../dev-digest/server/src/...`
- Adding/moving a package, use case, repository, handler or wiring → skill `go-architecture`;
  writing tests → skill `go-testing`
- Start of every task here → read `INSIGHTS.md` first; at the end → `engineering-insights` wrap-up

## Conventions (non-default)
- Layout: `cmd/server/` is the composition root (config, wiring, migrate → seed → jobs → listen,
  shutdown). Startup reaps (conventions scans, review runs, eval runs) are non-fatal. Shutdown
  order: the review executor cancels live runs (≤ 5 s) and completes the run buses so SSE streams
  end, in parallel with the eval executor (aborts its runs, ≤ 5 s; `ErrNotDrained` = warning, the
  next start reaps them), the intent, brief and onboarding services cancelling their in-flight
  generations (≤ 5 s; Echo never cancels the context of a request in flight, so
  `platform/inflight` is stopped before Echo) and the job runner (10 s) and the PR poller (`PR_POLL_ENABLED`, runs on the stop ctx, returns ≤ 5 s;
  `listen` waits for it before `serve` closes the DB); only then does Echo (own
  start context) drain HTTP (20 s). The SSE route `GET /runs/:id/events` is exempt from rate
  limiting; `POST /pulls/:id/review` and `POST /pulls/:id/intent/refresh` have a 10/min bucket
  each; `POST /agents/:id/eval-runs` and `POST /skills/:id/eval-runs` share ONE bucket
  (`eval-run-start`, 10/min per workspace, `httpx.Bucket.Name` + `Key`), `POST /eval-runs/run-all`
  has its own per-IP bucket (`evalshttp.RateLimitBuckets()`). Phase 5 contexts: `intent`,
  `smartdiff`, `blast`, `brief`, `onboarding`, `performance` (brief and the review hook reach
  intent, blast, documents and roles through peers adapters). `PR_POLL_ENABLED` (default on, off only on `false`; the zero `config.Config` of the component tests is off)
  starts the background PR poll (`pulls/app.Poller`, its own non-retrying GitHub client; interval = each
  workspace's Settings `polling_interval_min`, read through `settingsIntervals` in `cmd/server/poll.go` on every tick: due = last cycle start + the current interval, so a change applies within one tick; a rate-limit resume time still wins).
  `REVIEW_INTENT_ENABLED` (default on,
  off only on `false`) decides whether `cmd` wires `executor.Deps.Intent`; off leaves it nil, which
  is Phase 4 behaviour exactly. Test seams in `cmd/server`: `options.now` (clock) and
  `intentBudget` / `briefBudget` / `onboardingBudget` / `evalCaseBudget`, `runner` (a fake
  `ciapp.RunnerBundle`), `artifactClient` (TLS stub of the artifact storage leg) and `pollTick`
  (the PR poller's due-check tick, 0 = 1 min; poll tests shorten it and move `now`). Phase 6
  contexts: `evals` (cases, runs, executor) and `ci` (export, ingest; `ci/runner` is the CI runner
  program, `cmd/devdigest-runner`). The CI runner is compiled by the image build (cgo off,
  reproducible) into `internal/ci/adapters/runnerbin/` and embedded in the server with
  `-tags devdigest_runner` (`runnerbin.Default()`); an untagged build (tests, `go build ./...`)
  has no runner and the export answers `500 runner_bundle_missing`. `DEVDIGEST_BUILD_ID` (12 hex
  of a clean HEAD, else `dev`; set by the `Makefile`, a compose build arg) stamps the runner.
  Features live in
  `internal/<context>/{domain,app,adapters/<tech>,transport/http}`;
  shared plumbing in `internal/platform/<what>` (httpx, postgres, apperr, secrets, gitcli, github,
  retry, logging). Dependencies point inward only.
- `reviewer` (the review engine) is a shared kernel: `reviews/domain` may import `reviewer/domain`
  and `reviews/app` may import `reviewer/app` types. `reviewer/domain` is also the kernel of the
  Phase 5 contexts (`intent`, `brief`, `onboarding`): the JS-parity text helpers, `Intent` /
  `RenderIntent` and the untrusted-text wrapping live there. Otherwise contexts reach each other only
  through consumer-side ports wired in `cmd/` (peers adapters may import another context's
  app/domain types); adapters never import other adapters (`cmd/` binds them, e.g. `newLLMCaller`,
  `newEvalLLMCaller`). Phase 6 adds two more engine consumers: `evals` (domain run input,
  `app/executor`, the providers peer) and `ci/runner` (domain, app) may import `reviewer/*` (the one
  engine `reviewer/app.Run` runs reviews, evals and the CI runner); `evals` and `ci` reach agents, skills, repos and GitHub only through
  consumer-side ports (`ci/adapters/peers` adapts the agents, skills and repos use cases).
- Spend cap (`spend` context): `GET /settings/spend`; the cap is the Settings key
  `monthly_spend_cap_usd` (USD number or null), read through `settingsCaps` in `cmd/server/spend.go`
  (exact `big.Rat` conversion to `spend/domain.Micros`; a bad value is an error, so a check fails
  closed). `spendapp.Service.Admit` is the one check; it reads the cap first and skips the spend query
  when no cap is set (D24; migration 0003 indexes the query); each entry context declares its own
  consumer-side port and `cmd` passes the service to all six (reviews `Deps.Budget`, evals
  `RunDeps.Budget`, conventions `ServiceOptions.Budget`, intent `Deps.Budget`, brief and onboarding
  `Deps.Spend`). A nil port means the check is off; pass a true nil interface, never a typed-nil
  pointer (prod always passes the service). The 402 `spend_cap_reached` comes after the 404/validation
  checks and before any work.
- Cost ledger (`spend` context, migration 0005): an append-only `cost_ledger` (one row per answered
  LLM attempt and per new CI run; never updated or deleted, not even by workspace reset) plus the
  stored `spend_month_totals` (8 slot rows per workspace and UTC month). The month spend, the unpriced
  count (`unpriced_count`, entries with an unknown cost) and the cap check read only the totals
  (`spendapp.Totals`); `Entries.Append` inserts the entry and bumps its slot in one statement.
  Eight kinds (`review_run`, `review_intent`, `intent_refresh`, `brief`, `onboarding`,
  `conventions_scan`, `eval_case`, `ci_run`): the seven LLM kinds are `llm.Charge.Kind`, set per
  request (`Request.Charge`, copied by `llmcall`, `newLLMCaller`/`newEvalLLMCaller` must pass
  `c.Charge`); `llm.Options.Recorder` (the `spendapp.Recorder`, built before `llm.New`, never nil in
  prod) is called once per answered attempt, detached from cancellation with a 3 s bound, and a
  failed append logs one ERROR and loses only that cost. CI ingest records `ci_run` through
  `cmd/server/ledger.go` (`ciLedger`) inside the transaction that stores the run (`IngestDeps.Ledger`
  needs `IngestDeps.Tx`); its dedupe key `ci:<github run id>:<attempt>` (GitLab runs:
  `ci:<host>:<job id>:<attempt>`, built by `spendapp.CIRun.Host`, never by hand) makes a re-ingest after a
  history reset a no-op. The migration backfills the pre-ledger costs (origin `backfill`, dedupe
  `backfill:<source>:<id>`) and leaves a summary row in `cost_ledger_backfill`;
  `reportLedgerBackfill` logs it once after migrating (server start and `server migrate up`) and sets
  `logged_at`.
- `GET /about` (`about` context) reports `version` and `build_id` from the link-time vars
  `main.version` / `main.buildID` (`cmd/server/main.go`, default `dev`), `runner.version` from
  `ci/runner/domain.RunnerVersion`, plus the data dir and secrets file. The Dockerfile stamps them
  from the build args `DEVDIGEST_VERSION` (default `dev`; only letters, digits and `. _ + -`, else
  `dev`; a compose build arg) and the sanitised `DEVDIGEST_BUILD_ID`.
- Workspace (`workspace` context, Settings B): `GET`/`PUT /workspace` (info, rename), `GET /workspace/stats`,
  `GET /workspace/export` (secret-free JSON download), `POST /workspace/reset-history` (typed name
  confirms; 409 `run_active`, 422 `confirmation_mismatch`). `GET /workspace` is no longer in `repos`.
  The display name is the Settings row `workspace_name` (never `workspaces.name`, the lookup key);
  `retention_days` ∈ {30,60,90,180,365,null} drives the trace pruner. `cmd/server/workspace.go`
  adapts settings and repos to the ports. The pruner (`workspace/app.Pruner`, always on) is started
  by `listen` after the startup reaps as `go Run(stopCtx)`, prunes at once and then 24 h after each
  pass ended, and `listen` waits for it (a prune in flight is cancelled) before `serve` closes the DB.
  Test seam `options.pruneInterval` (0 = 24 h).
  The workspace adapter deletes from other contexts' tables in the reset: a new table that references
  agent runs, eval runs, reviews or findings must be added to `resetSteps` (and to its test). The reset
  holds `SHARE ROW EXCLUSIVE` on `agent_runs` and `eval_suite_runs` for its duration (≤ ~30 s), which
  pauses run starts and CI ingest in all workspaces — acceptable for the local tool. A prune batch waits
  up to 60 s for the history lock (a reset waits 10 s), so a prune that meets a reset waits it out.
- Automatic reviews (`autoreview` context, Settings C, migrations 0006, 0007): the Settings key
  `automatic_reviews` is the object `{enabled, agent_ids, on_new_pr, on_new_commits, enabled_at}`
  (`settings/app.AutomaticReviewsFromStored`; `enabled_at` is stamped by the server when the value becomes
  armed, so PRs opened earlier are never reviewed). The hook: `autoreview/app.Service` is the poller's
  `PollerDeps.Observer`; `AfterCycle(ctx, ws)` runs in the poller goroutine after each workspace cycle that
  polled, one evaluation at a time per process, and starts reviews through `reviewsapp.AutoStarter`
  (`StartAuto`, runs with source `auto`). `cmd/server/autoreview.go` holds the adapters (settings, clamped
  interval, starter that turns a 402 into `ErrCapReached`, budget = spend `Admit` with `auto_review_start`,
  caps = spend `Status().Reached()` without logging, token facts = GitHub token + GitLab repos and connection tokens). At most once: the store claims
  `(pr_id, head_sha)` in `auto_review_records` (unique key, `INSERT … ON CONFLICT DO NOTHING`) BEFORE the
  start and settles it after, so two evaluations or processes never review one head twice; the table has no
  FK to `agent_runs` (B's reset deletes runs but keeps the records). Rate limits: ≤ 5 starts per evaluation
  and ≤ 20 per rolling hour per workspace (over the limit a PR is deferred, not recorded). The spend cap is
  asked once per evaluation; a reached cap records `skipped_spend_cap`, a failed check defers. Fork guard (D13):
  only PRs whose head branch is in the PR's own repository start; the poll stores `pull_requests.head_is_fork`
  (`head.repo.full_name` vs `base.repo.full_name`; a null or missing repository counts as a fork), a fork head is
  recorded `skipped_fork` before any budget or rate check, and a NULL value (a row from before 0007, not yet
  refreshed by the poll) defers without a record. The flag is not part of any API response. Shutdown: the
  poller runs on the stop ctx and returns ≤ 5 s; a claim/start/settle runs on a ctx detached from it
  (bounded to 3 s) so a started review is never left half-recorded. `GET /settings/automatic-reviews-status`
  (`autoreview/transport/http`) derives the state on read; the service is built even with
  `PR_POLL_ENABLED=false` (state `poll_disabled`).
- Plugins (`plugins` context, Settings D, migration 0008): the six `/plugins` routes (list, export, import
  with `?dry_run=true` as preview, enable/disable, remove) and `GET /ci/installations`. Ports in `cmd/`:
  `cmd/server/plugins.go` adapts settings to `LastExports` (the Settings row `plugin_last_export`, written
  through the settings use case because `PUT /settings` refuses the key; it is in `GET /settings` and not in
  the workspace export) and reuses `workspaceNames` for the default export name. Install and remove are
  each one transaction in `plugins/adapters/postgres`, which writes other contexts' tables (agents, skills,
  eval cases, memory), like B's reset: a new table that references agents or skills must be added to its
  remove steps. Ownership is `plugin_id` on agents, skills and memory (FK NO ACTION, so remove deletes the
  children first). Runnable = own enabled AND (no plugin OR plugin enabled): list reads (review `all:true`,
  auto-review agents, every skill list incl. the brief) filter it in SQL; named reads (review by id, eval
  start and run-all, CI export) answer 409 `plugin_disabled` naming the plugin (run-all skips such agents).
  `httpx.Options.RawBodies` (`"METHOD /path"` → media type → max bytes, 415 `unsupported_media_type`, 413
  `payload_too_large`, read with `httpx.ReadRawBody`) is the only way around JSON-only bodies, and the only
  route that uses it is `POST /plugins/import` (zip ≤ 5 MiB, json ≤ 20 MiB); the Origin and Host guards
  still apply to it.
- Custom model providers (`customproviders` context, migration 0009, spec D30): a workspace defines up to 20
  gateways `custom:<slug>` (OpenAI or Anthropic protocol, base URL, bearer or header auth, ≤ 5 extra headers,
  manual models with optional prices, `structured_output` strict | json_object | prompt_only) through
  `GET/POST /settings/custom-providers` and `PUT/DELETE /settings/custom-providers/:id`. The public id is
  `custom:<slug>`; the tables keep the bare slug (PK `(workspace_id, id)`). Every route that carries a provider id
  reads it with `httpx.PathParam`, which decodes `custom%3A…` once. The context is reached only through
  consumer-side ports bound in `cmd/server/customproviders.go` (settings `Deps.Custom`, agents
  `WithCustomProviders`, plugins `Deps.CustomProviders` — a nil port silently drops the dry-run warning —, ci
  `ExportDeps.Custom` and `Prices`) and as `llm.Options.Custom`. Pure rules (id, base URL, header names, modes,
  forbidden addresses) live in `internal/llm/custom` (stdlib only, shared with the CI runner); the CI names
  (`DEVDIGEST_<S>_BASE_URL`, `…_API_KEY`, `…_HEADER_<n>`) in `ci/domain/providers.go`.
  - LLM calls: `llm.Client` resolves the provider per call from `Charge.WorkspaceID` (`CustomResolver.Resolve`
    builds the adapter with the stored key and header values, so it is never shared); `Ready`/`ListModels` serve
    built-ins only, custom ids go through `ReadyIn`/`ListModelsIn(ctx, ws, p)` (the reviews and evals peers use
    them). Cost per attempt: a finite reported cost, else the manual model price on reported usage only, else
    NULL — the built-in and live price books are never consulted for a custom model and estimated tokens are never
    priced (SCR7); `GET /settings/spend` counts the NULLs as `unpriced_count`. The mode shapes the body (the
    adapters) and the schema text (the client); a 400/422 in strict mode gets the FR33 hint. Under
    `LLM_PROVIDER_OVERRIDE=mock` `mockCustomResolver` answers any well-formed custom id with the mock (A4).
  - Outbound: one `egress.Client` (no proxy, A3; no redirects; a connect-time guard `custom.ForbiddenAddr` refuses
    link-local and unspecified addresses, loopback and private ones are allowed) serves every custom adapter.
    `DEVDIGEST_EXTRA_CA_FILE` is read once by `serve` (`egress.LoadExtraCA`; a bad file → one ERROR naming the
    variable and the path, then exit) and added by `extraCAClient`/`egress.WithRootCAs` to the GitHub clients, the
    built-in LLM adapters, the providers listing, the skill URL import, the custom adapters and the GitLab client;
    git gets it through a bundle (see GitLab below). A client a test injects through `options` keeps its own TLS roots.
  - Secrets: `secrets.Store.Custom/SetCustom/DeleteCustom/RestoreCustom`, keyed by workspace and public id, no
    environment fallback, never in a response, error or log; written inside the DB transaction and restored when
    it does not commit (A7); `CustomCall.Secrets` feeds the redaction of every error and log line of the call.
  - Consistency: delete locks the provider row `FOR UPDATE` and then reads the references (agents,
    `feature_models`, `default_model` → 409 `provider_in_use`); agent writes lock it `FOR SHARE` (`LockDefined`,
    after the agent row, inside the same transaction); settings checks are best effort (A8). Workspace reset and
    plugin remove keep providers (A12). Down 0009 refuses while an agent uses `custom:%` (A11).
  - CI: `ci_installations.provider` (NOT NULL, no default) is the export-time provider; a CI run's `agent_runs` and
    ledger rows carry it (`app.Recorded.Provider`), not the agent's current one. openrouter exports are
    byte-identical to before; an openai/anthropic agent is exportable (`pricing` from the price book, via
    `llm.Client.PerM`), a custom one gets `custom_provider` and manual pricing in the manifest, and an undefined
    one answers 422 `provider_not_configured`. The runner reads `DEVDIGEST_LLM_*` from the workflow env.
  - References: `customproviders/adapters/postgres/references.go` `Find` reads the agents and settings tables directly; a new table that stores a provider id must be added there, or delete-in-use misses it.
- GitLab (GitLab A, `gitlabconn` context, migration 0010, spec D36-D37): read-only access to GitLab projects next to
  GitHub repos. Connections are installation-wide (no workspace, max 20): `GET/POST /settings/gitlab-connections`,
  `PUT/DELETE /settings/gitlab-connections/:id` (`gitlabconnhttp.AddRoutes`, no tenant resolver); a connection is
  `{id, name, base_url (https://host[:port][/prefix], no /api/v4), host key}`, the host key (`codehost.HostKey`: lower
  case, `:port` unless 443) is what repos bind to at READ time (no FK: repos/leaderboard SQL LEFT JOINs
  `gitlab_connections` on `host`, so deleting a connection disconnects its repos, it never deletes them). The token
  is `secrets.Store.GitLab/SetGitLab/DeleteGitLab/RestoreGitLab` (`gitlab_connections.<id>`, no environment fallback),
  written inside the DB transaction and restored when it does not commit. `repos` gains `provider`, `host`,
  `project_id`, `web_url` (uniques `(workspace, host, full_name)` and, for GitLab, `(workspace, host, project_id)`:
  the project id is the identity, the path is refreshed by `UpdatePath` on the poll and the sync).
  - Wiring is `cmd/server/gitlab.go`: `gitlabconnapp.Service` is bound to repos (`reposConnections`:
    `Connections.Resolve/HostKeysByName`, `JobsOptions.Connections/CAFile`, `WithGitLab`), pulls (`pullsResolver`,
    `pullsapp.WithGitLab`, and `PollerDeps.GitLab`, a `glClient.WithRetry(retry.Options{})` copy so a repo poll stays
    within its request bound), settings (`settingsGitLab`: connection test, `provider: "gitlab"` + `connection_id`),
    auto reviews (`autoTokens`: GitLab repos + the connection tokens decide `no_code_host_token`) and repo-intel
    (`intelGit`, below). Comments, posting and the context peers also work for GitLab (GitLab B, below), and the CI
    export and ingest too (GitLab C, below).
  - Outbound: `platform/gitlab.Client` over `egress.Client{Guard: custom.ForbiddenAddr, RootCAs}` (no proxy, SCR2; no
    redirects; the token goes only to the connection's own address). git (`gitcli.Options.AddrGuard =
    custom.ForbiddenAddr`) drops the proxy and SSL environment, resolves the host once, checks the address and pins
    it (`http.curloptResolve`), sends the token as a Basic `oauth2` header scoped to the connection prefix
    (`GIT_CONFIG_*`, never argv or `.git/config`) and never follows redirects.
  - Clones live under the clone key `codehost.CloneKey` = `@gitlab/<host key, ':'→'_'>/<project id>` (the path is not part
    of it, so a rename keeps the clone). EVERY reader of a clone passes the key (`repointel.Repo.CloneKey()`, the
    `ReviewRepo`/`FindRepo` peers); the GitHub methods refuse a GitLab key and the `*Remote` methods refuse a GitHub one.
    The clone key holds no project path, so `intelGit` (repo-intel's git port) reads it from the repository rows
    (`pullsStore.ListPollTargets`) when it resyncs: `DefaultBranchRemote`/`SyncRemote` with a `Remote` built from the
    connection; a host without a connection is a `*reposapp.NotConnectedError`.
  - `DEVDIGEST_EXTRA_CA_FILE` + git: git replaces its trust store with `http.sslCAInfo`, so `serve` (`prepareGitCA`)
    writes `<DataDir>/git-ca-bundle.pem` = system bundle (`SSL_CERT_FILE` or the usual distro paths) + the extra CA
    through `gitcli.WriteCABundle` and passes the path to the repos jobs and `intelGit`. No system bundle
    (`gitcli.ErrNoSystemBundle`) with the variable set refuses to start (SCR5); without the variable git keeps its
    default roots and no file is written.
  - Redaction: `llm.Options.ExtraRedactions` = custom values + `secrets.Store.GitLabValues` (`extraRedactions`), read at
    every call; gitcli scrubs the token (and its Basic encoding) from git stderr; the onboarding redactor knows the 12
    GitLab token prefixes. Auto reviews: a merge request from a fork (`source_project_id` ≠ `target_project_id`, or
    missing) is `skipped_fork` (SCR1). The poller keeps one rate-limit pause per host (`pulls/domain.HostPauses`).
- GitLab comments and context (GitLab B, migration 0011): GitLab merge requests get review comments (`GET`/`POST
  /pulls/:id/comments`: diff threads, new positioned discussion, reply into a discussion) and the same context as GitHub
  PRs. Wiring in `cmd/server`: ONE `bindingResolver(gcSvc)` (`gitlab.ResolveFunc`: host key -> `gitlab.Binding` with the
  token, read at every call, so a rotated or removed token applies to the next request) and the same egress `glClient`
  feed `pullsapp.WithGitLabComments(<pulls GitLab adapter>)` + `pullsapp.WithRedact(llmClient.Redact)` (masks every stored
  token and the custom secrets in GitLab's text before it reaches a response or log), blast `Options.GitLab`
  (`blastpeers.NewGitLab`, prior merged MRs), brief `Deps.GitLabIssues`, intent `NewDocs/NewTickets(...).WithGitLab` and
  skills `Importer.WithGitLabFiles(gitlabfiles.New(...))` (`/-/blob/<ref>/<file>` URLs of a connected host import with the
  token). Without the port/connection/token a feature answers empty or "not connected" and sends no request; no GitLab
  repository sends a GitHub request. Errors: `gitlab_comment_failed`, `gitlab_permission_denied`, `gitlab_rate_limited`
  (`details.reason`, `cause`, `retry_after_s`); writes are single attempts (no retry). `platform/gitlab/notebody` is a
  stdlib-only leaf (note body rules, shared with the CI runner of slice C). Migration 0011 adds
  `pull_requests.diff_head` (CHECK 40/64 lower hex; the position the comment is anchored to) and `pr_files.previous_path`,
  both refilled by the next detail fetch (its down does not refuse).
- GitLab CI (GitLab C, migration 0012): export target `gitlab_ci` (job file `.gitlab/devdigest/review-<slug>.yml`, root
  `.gitlab-ci.yml` include or a snippet, branch + commit + MR; no `repo_id`/`target` = chosen by the repo's host),
  checked by `wfrules.CheckGitLab`; the job runs the pinned public `domain.JobImage` (digest, not a tag). Ingest reads
  the MR pipelines' job artifacts through a credential-free storage hop (`platform/gitlab.JobArtifactFile`: the token
  never reaches the storage host, no redirect with it). Installations and runs are host-qualified (unique per host +
  repo/project id, `ci_runs` per host + job id + attempt); the ledger key is `ci:<host>:<job id>:<attempt>`. Wiring:
  `cmd/server/ci_gitlab.go` (`newCIGitLab`: `ExportDeps.GitLabConns/Clones/GitLab`, `IngestDeps.GitLab`, the repo
  directory's `WithGitLab`) over the same `bindingResolver` and egress `glClient` as GitLab B; `ciLedger` implements
  `RecordGitLabCIRun`; `options.artifactClient` also stands in for the GitLab storage hop. Runner GitLab mode
  (`DEVDIGEST_PLATFORM=gitlab_ci`, `ci/runner` + `adapters/glapi` + `adapters/gitdiff`): drafts + bulk publish, at most
  50 inline threads, B's `notebody` rules, and NO `::stop-commands::` wrapping of the log in GitLab mode.
- Echo only in `internal/platform/httpx`, `transport/http` packages and `cmd/` (one test-only
  exception: `platform/inflight/shutdown_test.go` drives a real Echo listener to prove that its
  graceful shutdown never cancels request contexts). GORM only in
  `adapters/postgres` packages, `internal/platform/postgres` and `cmd/` (the composition root may
  hold and pass the `*gorm.DB` handle, never query with it). Domain and app import neither.
- Stores use GORM (`*gorm.DB`, `Transaction`, `clause.OnConflict`, `clause.Locking`); GORM
  `Raw`/`Exec` only for what the builder can't express (advisory locks, CTEs, DDL).
  Row structs are declared per adapter — no shared `models` package.
- Errors: use cases return `internal/platform/apperr` kinds; only `httpx` maps kinds to status and
  renders the envelope. Never return an HTTP status from app/domain code.
- Schema changes: a new version in `migrations/` with an `.up.sql` **and** a `.down.sql`;
  never GORM AutoMigrate, never edit a committed migration.
- JSON field names follow the contracts exactly (snake_case, plus the documented camelCase
  exceptions); timestamps are ISO-8601 UTC with milliseconds (`…000Z`).
- Secrets only via `internal/platform/secrets` (`$DEVDIGEST_DATA_DIR/secrets.json`, env fallback);
  never logged, never in errors. Git only via `internal/platform/gitcli` (the one exception is the CI runner's `ci/runner/adapters/gitdiff`, a local
  `git diff` with no token, because the runner's import closure forbids gitcli); the token goes through
  `GIT_CONFIG_*` env, never argv or `.git/config`.
- Request bodies only through `httpx.ReadJSONBody` (JSON parsed only for `application/json`).
  State-changing requests with a foreign `Origin` → 403; `Host` must be in `ALLOWED_HOSTS`
  (default `localhost,127.0.0.1,[::1],backend`). New routes get both for free via `httpx.New`.
- DB pool: `postgres.Open` caps it (`DB_MAX_OPEN_CONNS`, 1-90, default 20 of Postgres' 100; idle ≤ 10,
  idle time 5 min), so a burst waits for a connection instead of failing with 53300 "too many
  clients". Never hold a connection (an `InTx` body) while asking the same pool for a second one:
  every store method called inside `InTx` takes its handle from `postgres.Conn(ctx, db)`, never
  from its own `db`. With a saturated pool such a nested call deadlocks (all connections held by
  transactions that wait for another). Long holders: repo-intel indexing (a job, ≤ 3 at once).
- Integration tests use `internal/platform/postgres/pgtest` (one container per test binary, fresh
  DB per test) and skip under `-short`.

## Gotchas
- `go: no such tool "compile"` → an IDE-inherited `GOROOT` points at the Windows Go SDK; and
  `/usr/bin/go` is 1.25. Hence the `env -u GOROOT PATH=…` prefix.
- Echo v5 is not v4: handlers take `*echo.Context`; router 404/405 errors are unexported types
  (match `echo.HTTPStatusCoder`); CORS middleware panics without origins (`INSIGHTS.md`).
- The runtime image is Alpine with `git` (not distroless): the backend shells out to git.
- CGO is required for `internal/repointel/adapters/treesitter` (tree-sitter C runtime): host
  `go test -race` needs `gcc`, and `CGO_ENABLED=0` builds fail. The Dockerfile build stage adds
  `build-base`; the runtime image has no C toolchain (same musl: `alpine3.23` build, `alpine:3.23` runtime).
- The schema already contains every table of later phases — empty ≠ unused.

## Do not touch
- Committed migrations in `migrations/` — add a new version instead.
- `../frontend/src/vendor/shared` — the wire contract; adapt the Go side to it.
