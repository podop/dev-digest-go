# GitLab A — connections, repositories and merge request sync
Spec: GitLab slice A (GitLab connections, GitLab repositories next to GitHub ones, MR sync, host-aware links) · Status: ready-for-planning · Date: 2026-10-24
Areas: api, git, sync, db, ui
Design sources: the user's request of 2026-10-24 (translated): "Add support for GitLab, not just GitHub, with all its
PR sync features — the full spec of the functionality that currently exists for GitHub." The user's answers of
2026-10-24 to the blocking questions Q1–Q10 of the first spec-creator run, and that run's research report (three
research reports of 2026-10-24); the user's answers of 2026-10-24 to this spec's open questions Q1–Q9 (D25–D34).
Code research at `feat/go-backend-phase-1` @ `4ce720b`; Settings A contracts at the
worktree `../dev-digest-go-settings-a` (branch `feat/settings-a`) @ `c9c4689`; GitLab REST/GraphQL documentation
(docs.gitlab.com, research report of 2026-10-24). No design frames or screenshots were provided.
Related: `specs/2026-10-23-settings-first-run-setup.md` (Settings A — extended, see §12),
`specs/2026-10-23-settings-c-automatic-reviews.md` (Settings C — extended), `specs/2026-10-23-settings-integrations.md`
(Settings D — unchanged here, extended by slice C), `specs/2026-10-23-pr-leaderboard.md` (PR Leaderboard — extended),
`specs/2026-10-21-go-backend-phase-1.md` (Phase 1 — AC53 and the clone path rule superseded in part),
`specs/2026-10-23-go-backend-phase-6.md` (Phase 6 — one check added), `specs/2026-10-24-custom-model-providers.md`
(custom model providers — shares the extra CA file, the address rules and the test-connection route). Follow-ups that
depend on this spec: GitLab slice B `specs/<date>-gitlab-b-comments-context.md` (MR comments and context peers) and
GitLab slice C `specs/<date>-gitlab-c-ci.md` (GitLab CI), written after this one. Supersedes nothing else.

ID convention: plain FR1…, EC1…, NFR1…, AC1…, US1…, D1…, Q1…. Area tags: `[api]` = Go HTTP routes and use cases
(connections, connection test, repositories, host isolation), `[git]` = clone and refresh of repositories, `[sync]` =
PR/MR list, detail, manual poll, background poll and diff stats, `[db]` = schema and migrations, `[ui]` = Next.js app.

## 1. Goal
DevDigest works only with GitHub today. Teams on gitlab.com or on a self-hosted GitLab cannot add their projects.
This slice lets a user connect one or more GitLab instances (gitlab.com and any number of self-hosted instances, each
with its own access token) and add GitLab projects, including projects in nested groups, next to their GitHub
repositories in the same workspace. Merge requests are then synced the way GitHub pull requests are: the list,
detail with files, commits and linked issue, the manual poll and the background poll with diff stats. Local reviews,
smart diff, the PR Leaderboard and automatic reviews then work for GitLab merge requests unchanged, because they read
only the stored mirror. Links open the right host. Nothing is written to GitLab in this slice. Posting comments,
context peers (slice B) and GitLab CI (slice C) build on the foundations defined here: the connection model, the host
on repositories, the error codes, host-aware links and token redaction.

## 2. Context
- Repository URLs: only `https://github.com/<owner>/<repo>` (optional `.git`, trailing `/`) and
  `git@github.com:<owner>/<repo>` are accepted; segments match `^[A-Za-z0-9_.-]+$`, are not `.`, `..`, `.git` and do
  not start with `-` (`backend/internal/repos/domain/repo.go:58-88`). Any other URL → 422 `validation_error` with one
  issue at `["url"]`, message "Must be a GitHub repository URL, e.g. https://github.com/owner/repo"
  (`repos/transport/http/handler.go:108-111`, pinned by `handler_test.go:252-278`). `https://gitlab.com/o/r` and
  `git@gitlab.com:o/r` are explicit rejected cases (`repo_test.go:47,60`, `handler_test.go:256`; Phase 1 AC53). The
  same parser and message are in the shared contract (`frontend/src/vendor/shared/contracts/platform.ts:83-112`); the
  add-repo form does no client-side validation and shows the API message (`AddRepoView.tsx:33-42,103-119`).
- Add makes no GitHub call. It stores owner, name, `full_name` = `owner/name`, `default_branch` `main` (never updated
  later), answers 201 (created) or 200 (existing, nothing enqueued) and enqueues a clone job
  (`repos/app/service.go:71-87`, `repos/adapters/postgres/store.go:55-85`). Uniqueness is `(workspace_id, full_name)`,
  case-sensitive (`backend/migrations/0001_baseline.up.sql:1357`); repos have no host or provider column (`:613-624`).
  PRs are unique on `(repo_id, number)` (`:1333`).
- `Repo` on the wire: `id, workspace_id, owner, name, full_name, default_branch, clone_path, last_polled_at,
  created_by`, all keys always present (`repos/transport/http/handler.go:56-66`, `platform.ts:116-126`).
- Clones: `https://github.com/<owner>/<name>.git`, SSH input included (`repos/app/jobs.go:19`, `repo.go:52-56`), into
  exactly `<cloneRoot>/<owner>/<name>` — the guard requires two levels and refuses symlinks
  (`platform/gitcli/paths.go:25-34,52-100`; Phase 1 FR35). Git auth: env-only config
  `http.https://github.com/.extraHeader` = `Authorization: Basic base64("x-access-token:<token>")`; argv carries no
  secret; inherited `GIT_CONFIG_*` and askpass are dropped; stderr is scrubbed of the token, its Basic form and URL
  userinfo; without a token git runs anonymously (`gitcli.go:34-36,478-562`).
- PR sync goes through one GitHub REST client (`platform/github`), which never follows redirects
  (`client.go:150-165`) and keeps no rate-limit state (`client.go:101-110,250-305`):
  - list: `GET /repos/{o}/{n}/pulls?state=all&sort=updated&direction=desc`, 50 per page; mapped fields number, title,
    author (`user.login` or `unknown`), branch, base, head_sha, opened_at, updated_at, merged_at, closed_at, draft,
    status (merged → `merged`, closed → `closed`, else `open`) (`platform/github/pulls.go:69-151`); the upsert updates
    only title, head_sha, status, updated_at, merged_at, closed_at, draft (`pulls/adapters/postgres/store.go:196-227`;
    `backend/INSIGHTS.md:38`);
  - `GET /repos/:id/pulls` syncs one page when a token exists, back-fills diff stats of ≤ 10 PRs, and on no token or
    any failure serves stored rows with 200 (`pulls/app/service.go:50-130`);
  - detail `GET /pulls/:id`: the PR, ≤ 3,000 files (pages of 100), ≤ 250 commits, same-path files merged, written in
    one transaction; linked issue = first `#<digits>` in the body, fetched, never stored; stored detail on any failure
    (`pulls.go:193-327`, `service.go:168-236`; Phase 1 FR38, FR39);
  - manual poll `POST /repos/:id/poll`: no token → 500 `config_error` "GITHUB_TOKEN is not configured"; failure → 502
    `external_service` "GitHub request failed" (`service.go:141-163`);
  - background poll: every repo of every workspace, one at a time, ≤ 10 pages with a 62-day horizon, then ≤ 20
    diff-stat fetches (one `GET /pulls/{n}` each) — ≤ 30 requests per repo per cycle; no token → whole cycle skipped
    with one WARN; a rate limit pauses the whole poller (single `pausedUntil`, ≤ 2 h)
    (`pulls/app/poller.go:15-19,65-69,167-307`; `backend/INSIGHTS.md:36,69`; PR Leaderboard FR3–FR5, FR24, NFR3).
- The leaderboard reads `additions, deletions, files_count, status, draft, opened_at, merged_at, closed_at, author`
  from stored PRs and makes no GitHub call (`leaderboard/adapters/postgres/queries.go:31-60`). Its rows carry `repo_id`
  and `repo` (full name); `LeaderboardRepo` = `{id, full_name, last_polled_at}` (`contracts/leaderboard.ts:38-42,102`).
- Other GitHub consumers keyed by `owner/name`: blast-radius prior PRs, brief linked issue, intent tickets and the
  contents-API docs fallback, review comments (list, post, reply), CI export and ingest (research report, Q6 table;
  `pulls/app/ports.go:58-91`, `blast/app/ports.go:34-38`, `brief/app/ports.go:74-76`, `intent/app/ports.go:67,83`,
  `ci/app/ports.go:161-181`). CI export resolves `repo` by exact `full_name` (Phase 6 §7.6.1).
- Secrets: `<DEVDIGEST_DATA_DIR>/secrets.json` (0600, atomic write, env fallback, `GITHUB_TOKEN` → `GITHUB_PAT`), no
  delete route; `GET /settings/secrets-status` = `{openai, anthropic, openrouter, github}` (strict golden)
  (`platform/secrets/secrets.go:17-127`; Settings A worktree `settings/app/settings.go:62-68,174-179`).
- Test connection (Settings A, worktree @ `c9c4689`): `POST /settings/test-connection {provider, key?}`, provider
  `openai|anthropic|openrouter|github` (`gitlab` → 422 `invalid_enum_value`, `validate_test.go:234` there, `:199` on
  this branch); saves `key` first and keeps it on failure; always 200; GitHub returns `{provider, ok, message,
  latency_ms, login, scopes}` from `GET /user`; 20/min per IP; one INFO line per test without the key
  (`settings/app/settings.go:189-301`, `transport/http/handler.go:16-25,119-148`, `contracts/platform.ts:79-103`).
- Settings A UI (worktree): sub-nav `api-keys, github, workspace, automatic-reviews, integrations, models, about`
  (`SettingsView/constants.ts:2-10`); GitHub Integration = token row (auto-test on open), scopes, comment identity,
  "Connected repositories" from GET `/repos` (`SettingsGithub/**`); `/setup` steps `model, github, repo`
  (`SetupView/constants.ts:3`); home gate: no model key → model; no `github` → github; no repo → repo
  (`HomeView/helpers.ts:13-18`).
- Redaction: the onboarding pattern list knows `gh[pousr]_`, `github_pat_`, `sk-` and others but no GitLab prefix
  (`onboarding/domain/redact.go:206-214`); everything else redacts by exact stored value — the three LLM keys and
  `GITHUB_TOKEN` (`llm/structured.go:23-39`), git output (`gitcli.go:551-562`), GitHub client errors
  (`platform/github/client.go:248-274`); log attributes are redacted by name (`platform/logging/logging.go:12-61`).
- UI links: all external links are built client-side from `repo.full_name` against a hard-coded
  `https://github.com` — PR `/pull/<n>`, issue `/issues/<n>`, blob `/blob/<sha>/<path>#L<a>-L<b>` — "never from a
  server-echoed URL" (`frontend/src/lib/github-urls.ts:1-46`; callers `PrDetailView.tsx:86`, `PriorPrs.tsx:75`,
  `FindingCard.tsx:66`, `BlastRadiusCard/helpers.ts:49`, `IntentCard/helpers.ts:32,35`, `TourView/.../PathList/helpers.ts:5`,
  `lib/ci-runs.ts:15-18`). `#<n>` is rendered in about 11 components (PR row, detail header, prior PRs, leaderboard
  rows, awards, needs attention, hot files, CI runs, run history) and in message strings (`prReview.json:176,178`,
  `runs.json`, `leaderboardPanels.json:20`). Avatars are initials only (`vendor/ui/primitives/Avatar.tsx:3-31`).
- Finding text is LLM output a PR author can steer; GitHub posting runs it through `sanitizeForGitHub` (HTML and `@`)
  (`frontend/INSIGHTS.md:37`). GitLab additionally executes quick actions (`/approve`, `/merge`, `/close`, …) written
  in note bodies as the token's user (research report, fact 6) — relevant from slice B on.
- GitLab facts used here (GitLab docs and, where the docs are silent, GitLab source at tag `v17.0.0`; research
  reports of 2026-10-24):
  - REST root `<instance>/api/v4` (a relative URL root prefixes every path), header `PRIVATE-TOKEN`; project `:id` =
    numeric id or URL-encoded full path; an old path of a renamed or moved project answers 301 to
    `/api/v4/projects/<id>` (source `lib/api/helpers.rb`, not in the docs); subgroups nest up to 20 levels
    (docs.gitlab.com/api/projects, /api/rest).
  - MR list `GET /projects/:id/merge_requests` (`state`, `order_by=updated_at`, `sort`, `updated_after`; offset
    pagination, `per_page` ≤ 100, `Link`/`x-next-page`) carries `sha`, `merged_at`, `closed_at`, `draft`,
    `source_project_id`, but no additions/deletions; the single MR adds `changes_count`, a string capped at `"1000+"`;
    `/merge_requests/:iid/diffs` is paginated and returns limited results above the diff limits (`collapsed`,
    `too_large` flags only from 18.4); `/commits`; issues `GET /projects/:id/issues/:iid`
    (docs.gitlab.com/api/merge_requests).
  - GraphQL `MergeRequest.diffStatsSummary {additions, deletions, changes, fileCount}` exists from before 15.0 (source
    `app/graphql/types/diff_stats_summary_type.rb` at `v15.0.0`/`v17.0.0`), is batch-loaded, needs only `read_api`
    (mutations need `api`); limits: page size ≤ 100, query complexity ≤ 250 for authenticated calls, 30 s timeout
    (docs.gitlab.com/api/graphql). A missing summary is returned as zeros.
  - Scopes: `api` grants Git over HTTP as well; `read_repository` grants Git over HTTP and the repository files API;
    `read_api` is read-only API (the code also grants git download, the docs do not say so)
    (docs.gitlab.com/security/tokens/access_token_scopes; `lib/gitlab/auth.rb` at `v17.0.0`).
  - Git over HTTPS takes HTTP Basic only (no Bearer, no `PRIVATE-TOKEN`); for access tokens the user name is any
    non-empty string (docs.gitlab.com/user/profile/personal_access_tokens; issue #409822).
  - `GET /user` (scope `api`, `read_api` or `read_user`; a project or group token answers its bot user);
    `GET /personal_access_tokens/self` (15.5+, any scope; returns `scopes`, `expires_at`, `active`; may answer 400 for
    token types it does not treat as personal — unverified for project and group tokens); `GET /metadata` (`version`,
    `revision`, `enterprise`; `/version` is its deprecated alias; authentication required). Expired, revoked and
    unknown tokens all answer 401 (docs.gitlab.com/security/tokens/token_troubleshooting). Since 16.0 every access
    token has an expiry date, at most 365 days by default.
  - gitlab.com allows 2,000 authenticated API requests per minute; `RateLimit-Remaining`, `RateLimit-Reset` (epoch
    seconds) and, on 429, `Retry-After`; self-managed limits are off by default or configured by the administrator
    (docs.gitlab.com/user/gitlab_com/rate_limits).

## 3. Scope
### In scope
- A GitLab connection entity, installation-wide: base URL (relative URL root allowed), display name, access token;
  one per host, up to 20; its CRUD routes, its connection test and its token in the secrets store.
- GitLab repositories next to GitHub ones: URL forms with nested groups and SSH form, host binding, project lookup at
  add, host-qualified uniqueness, clone and refresh with the connection's token, the stored path following a renamed
  or moved project, and the "not connected" state of repositories whose host has no connection.
- MR sync: list, detail (files, commits, linked issue), manual poll, background poll per host, diff stats within the
  leaderboard request budget.
- Host isolation for the GitHub-only consumers (comments, context peers, CI export) until slices B and C.
- Settings → GitLab Integration, the setup step "Code host", the home gate, the add-repository form, host-aware
  links, `!<n>` references and host labels.
- GitLab token redaction; the deploy-level extra CA file applied to GitLab API and git calls.
- The schema change (connections, repo host, project id) with an up and a down migration.

### Out of scope
- No request with a method other than GET is sent to any GitLab instance, except POST of read-only GraphQL queries
  (no mutation) for diff stats; no note, discussion, approval, status, commit or branch is created on GitLab.
- No MR comment is listed or posted, no GitLab issue is used as an intent ticket or brief issue, and no GitLab file is
  read through the API (slice B).
- No GitLab CI export, runner, ingest or Integrations card entry (slice C).
- No OAuth login and no GitLab application; only personal, project and group access tokens are accepted.
- No webhook receiver; GitLab data is pulled only, as GitHub data is.
- No environment variable defines a GitLab connection, base URL or token.
- No connection is scoped to a workspace; every workspace sees and uses the same connections.
- No more than one connection per host; no several tokens per host.
- No base URL with the `http` scheme is accepted, and no option disables TLS certificate verification.
- No redirect is followed by the GitLab API client or by git for a GitLab remote.
- No GitHub host other than `github.com` (GitHub Enterprise) is supported.
- No GitLab instance older than 17.0 is refused; it only gets a warning.
- No route returns a GitLab token, a part of one or a token hint.
- The UI does not relabel "PR" / "pull request" in generic chrome (navigation, list and page titles, leaderboard
  headings) for GitLab repositories.
- No clone directory is moved or renamed when a GitLab project's path changes; only the stored path and web URL
  follow it (FR47).
- Deleting a GitLab connection deletes no repository, pull request, file, commit, review, finding or clone.
- No GitLab request is sent for a repository whose host has no connection.
- The `GET /settings/secrets-status` response keeps exactly its four keys.
- GitHub behaviour is unchanged: accepted GitHub URL forms, clone locations, git auth, sync requests, status codes and
  JSON fields stay as they are; additions are new optional or always-present fields and new routes only.

## 4. User scenarios
- US1 — As a developer on gitlab.com, I add a GitLab connection with my personal access token, test it and see my
  username, the token scopes, its expiry date and the GitLab version.
- US2 — As a developer at a company with a self-hosted GitLab at `https://git.corp.example/gitlab` behind a
  corporate CA, I connect it next to gitlab.com, and DevDigest trusts the CA file mounted by the administrator.
- US3 — As a user, I add `https://gitlab.com/acme/platform/payments` (nested group) to a workspace that already has
  GitHub repositories, and its merge requests appear in the PR list, the leaderboard and automatic reviews.
- US4 — As a reviewer, I open a GitLab merge request, see its files, commits, diff and linked issue, run a local
  review, and open it on GitLab with "View on GitLab"; it is referenced as `!42`.
- US5 — As a user whose token expires soon, I see a warning 14 days ahead and rotate the token without touching my
  repositories.
- US6 — As a new user whose team uses only GitLab, I complete setup with a GitLab connection instead of a GitHub token.
- US7 — As an administrator, I delete a connection that repositories still use, am told how many repositories become
  "not connected", keep reading their reviews, and when I add a connection for that host again they sync again.
- US8 — As a user whose GitLab project was renamed or moved to another group, I see its new path and links after the
  next sync, without adding it again.

## 5. Functional requirements
GitLab connections
- FR1 [api] — A GitLab connection shall have:
  - `id` — server-generated UUID, immutable;
  - `name` — display name, 1–80 characters after trimming, no control characters; empty or omitted on create → the
    connection's host key;
  - `base_url` — the instance URL (FR2), and the derived `host` key (FR2);
  - a token — write-only (FR4);
  - `created_at`, `updated_at`.

  Connections are installation-wide: they belong to no workspace, and every workspace uses them. At most 20
  connections exist; at most one per host key (D4, D13).
- FR2 [api] — `base_url` rules:
  - scheme `https` only; a host is present; an optional port; an optional path prefix (relative URL root) of
    segments; no userinfo, query or fragment; ≤ 2,048 characters;
  - stored normalised: scheme and host lower-cased, port 443 removed, trailing `/` removed;
  - a path ending in `/api/v4` → 422 "Use the GitLab instance URL, not its API URL";
  - host `github.com`, `www.github.com` or `api.github.com` → 422;
  - loopback and private addresses (RFC 1918, IPv6 ULA) are allowed; link-local (169.254.0.0/16, fe80::/10) and
    unspecified (`0.0.0.0`, `::`) addresses are refused as a literal host (422) and as the address a host name
    resolves to when a call connects (the call fails, nothing is sent) — the rule of custom providers FR3 without its
    plain-http allowance (D9).

  Every broken rule → 422 `validation_error` at path `["base_url"]`. The **host key** is the lower-cased host name,
  plus `:<port>` when the port is not 443. Derived addresses: REST root `<base_url>/api/v4`, GraphQL
  `<base_url>/api/graphql`, git `<base_url>/<full_name>.git`, web `<base_url>/<full_name>`.
- FR3 [api] — Routes (§7): GET `/settings/gitlab-connections` (list, sorted by `name` then `host`, byte order), POST
  (create → 201), PUT `/settings/gitlab-connections/:id` (→ 200), DELETE `/settings/gitlab-connections/:id` (→ 200).
  A connection for an existing host key → 409 `gitlab_connection_exists`; an unknown id → 404
  `gitlab_connection_not_found`; a 21st connection → 422 `gitlab_connection_limit`. Create, update and delete make
  no request to GitLab.
- FR4 [api] — The token shall be stored only in the secrets store (mode 0600), keyed by the connection id, and never
  returned; responses carry `token_configured`. Token input: 1–4,096 characters, no whitespace or control
  characters. On PUT: `token` omitted → kept, `null` → removed, string → replaced. Deleting a connection removes its
  token. A definition change and its token change are applied together or not at all.
- FR5 [api] — PUT may change `name` and `base_url`, but `base_url` only when its host key stays the same; a different
  host key → 422 `validation_error` at `["base_url"]` "The host of a connection cannot change; add a new connection
  instead" (repositories are bound by host, D15). A changed path prefix applies to every bound repository's derived
  addresses from the next call on.
- FR6 [api] — Token rotation (PUT with a new token, or a test with `key`) takes effect for the next GitLab API or git
  call of every route, job and poll without a restart; a call already in flight finishes with the token it started
  with.
- FR7 [api] — DELETE of a connection shall always be allowed, also while repositories in any workspace are bound to
  its host key: the connection and its token are deleted together (FR4) and the response is 200 {`deleted`: id,
  `disconnected_repos`: the number of repositories that were bound to it}. Its repositories are not deleted; they
  become "not connected" (FR48). There is no `gitlab_connection_in_use` error (D25).
- FR8 [api] — Each connection in a response shall carry `repo_count`: the number of repositories in all workspaces
  bound to its host key.

Connection test
- FR9 [api] — `POST /settings/test-connection` shall accept `provider: "gitlab"` with a required `connection_id`
  (missing → 422 at `["connection_id"]`; unknown → 404 `gitlab_connection_not_found`, nothing saved). With `key`, the
  key is saved as the connection's token first and kept when the test fails (as for GitHub). Without a stored token
  → `ok:false` "GitLab connection <name> has no token", no request. `connection_id` is ignored for other providers.
- FR10 [api] — A GitLab test shall make, in this order and to the connection's REST root only, `GET /user`,
  `GET /personal_access_tokens/self` and `GET /metadata`, with the token in the `PRIVATE-TOKEN` header, never
  following a redirect:
  - `/user` 2xx with a `username` → continue; 401 → `ok:false` "GitLab rejected the token (401): it is wrong,
    expired or revoked"; 403 →
    `ok:false` "The token cannot read its user (403)"; 3xx → `ok:false` "GitLab answered with a redirect — check the
    base URL"; a non-JSON or username-less 2xx → `ok:false` "No GitLab API found at <base_url>"; timeout, 5xx or a
    connection error → `ok:false` "GitLab is not reachable at <base_url>";
  - `/personal_access_tokens/self` 2xx → `scopes` and `expires_at`; any failure → both null and warning
    `scopes_unknown`;
  - `/metadata` 2xx → `instance_version` = its `version`; any failure → null and warning `version_unknown`.

  Success message: "Connected as @<username>".
- FR11 [api] — The test shall evaluate the token (D6):
  - `token_access` `full` when `scopes` contain `api`; `read_only` when they contain `read_api` but not `api` →
    warning `read_only_token` "Read-only token: sync works; posting to merge requests and GitLab CI export will
    fail"; `null` when `scopes` is null;
  - `scopes` containing none of `api`, `read_repository` and `write_repository` → warning `no_repository_scope`
    "Without read_repository, private projects may not be cloned" (`api` already grants Git over HTTP);
  - `scopes` with neither `api` nor `read_api` → `ok:false` "The token needs the api or read_api scope";
  - `expires_at` within 14 days of today (UTC dates, both ends included) → warning `token_expiring` "Token expires on
    <date> (in <n> days)";
  - `instance_version` whose major.minor is below 17.0 → warning `version_unsupported` "GitLab <version> is older
    than the supported 17.0; some features may fail" (D8).

  Warnings are listed in the order `read_only_token`, `no_repository_scope`, `token_expiring`, `version_unsupported`,
  `scopes_unknown`, `version_unknown`. `login` carries the username.

GitLab repositories
- FR12 [api] — `POST /repos` shall accept, after trimming, the GitHub forms of Phase 1 §7.3 unchanged and these
  forms for any other host:
  - `https://<host>[:<port>]/<path>` with an optional `.git` and an optional trailing `/`;
  - `git@<host>:<path>` with an optional `.git`;

  where `<path>` is 2–21 `/`-separated segments (after the connection's path prefix, FR13), each following the
  Phase 1 FR35 segment rule. Any other input (`http://`, `ssh://`, userinfo, query, fragment, a `-` segment such as
  GitLab's `/-/merge_requests/…` deep links, more than 21 segments) → 422 `validation_error` at `["url"]` with the
  message "Must be a GitHub or GitLab repository URL, e.g. https://github.com/owner/repo or
  https://gitlab.com/group/project".
- FR13 [api] — Host binding:
  - host `github.com` → GitHub, as today (no API call);
  - otherwise the connection whose host key equals the URL's host key (HTTPS: host name plus non-443 port; SSH: host
    name only); for HTTPS the path shall start with the connection's path prefix, which is removed; a path outside it
    → 422 `validation_error` at `["url"]` "This URL is outside <base_url>";
  - an SSH host name shared by connections on different ports → 422 `validation_error` at `["url"]` "Several GitLab
    connections use <host>; paste the HTTPS URL";
  - no matching connection → 422 `repo_host_not_connected` "No GitLab connection for <host>. Add one in Settings →
    GitLab Integration." with `details` {`host`} (D22).
- FR14 [api] — Adding a GitLab repository shall make one `GET /projects/<URL-encoded path>` with the connection's
  token (anonymous when it has none) and, on 2xx, take the canonical `path_with_namespace`, the namespace full path,
  the project path, the numeric project id and `default_branch` (null → `main`). Errors, with nothing stored and no
  job: 404 → 422 `gitlab_project_not_found` "Project <path> was not found on <host>, or the token cannot see
  it"; 3xx (a renamed or moved project) → 422 `gitlab_project_not_found` "GitLab reports that <path> has moved; paste
  the project's current URL" (the redirect is not followed); 401/403 → 422 `gitlab_permission_denied` "GitLab rejected the token of connection <name>"; 429 → 502
  `gitlab_rate_limited`; timeout, 5xx, a non-JSON body or a connection error → 502 `gitlab_unavailable` "GitLab is
  unavailable, try again later".
- FR15 [api] — A GitLab repository shall be stored with provider `gitlab`, `host` = the host key, `owner` = the
  namespace full path (for example `acme/platform`), `name` = the project path, `full_name` = the canonical
  `path_with_namespace`, `default_branch` from FR14, the project id and the web URL `<base_url>/<full_name>`
  (FR47). When the workspace already has a repository with the same host and project id → 200 with it (its path
  updated per FR47) and nothing enqueued. When it has a repository with the same host and `full_name` but another
  project id (a stale path of a renamed or moved project) → 409 `gitlab_path_conflict` "<full_name> in this workspace
  is a different GitLab project that was renamed or moved; sync it first" with `details` {`repo_id`}, nothing stored
  and no job. Otherwise 201 and a clone job. GitHub repositories are stored with provider `github` and host
  `github.com`, and keep the `(host, full_name)` rule of today (200 with the existing one).
- FR16 [api] — Every `Repo` response shall add `provider` (`github`|`gitlab`), `host`, `web_url` and
  `connection_id`. `web_url` is `https://github.com/<full_name>` for GitHub; for a connected GitLab repository it is
  `<base_url>/<full_name>` of the current connection, and for a not-connected one (FR48) the web URL stored at its
  last successful project fetch. `connection_id` is the id of the connection whose host key equals the repository's
  host at the time of the request, null for GitHub and null for a not-connected GitLab repository.
- FR17 [api] — Every GitLab API request for a stored repository shall address the project by its numeric id, so a
  project renamed or moved on GitLab keeps syncing.

Clone and refresh
- FR18 [git] — The clone and refresh jobs (Phase 1 FR33, FR34) shall, for a GitLab repository, use
  `<base_url>/<full_name>.git` over HTTPS (SSH input included) with the same depth and refresh rules. Its clone
  location is derived only from the host key and the numeric project id — never from the project path — at a fixed
  depth under the clone root, inside a top-level directory whose name cannot be a GitHub owner name. The path guard
  shall accept exactly the GitHub form `<cloneRoot>/<owner>/<name>` and this GitLab form, and keep refusing
  traversal and symlinks. GitHub clone locations do not change. Every feature that reads a clone (repo index,
  conventions, onboarding tour, intent docs, project context, smart diff) shall find a GitLab clone.
- FR19 [git] — When the connection has a token, git shall send it only as a per-command HTTP Basic authorization
  header with user name `oauth2` (GitLab accepts any non-empty user name for access tokens; D32), scoped to the URL
  prefix `<base_url>/`; never in argv, the remote URL, the clone's
  git config or any output. Without a token git runs anonymously. Git shall not follow HTTP redirects for a GitLab
  remote.
- FR20 [api] — The deploy setting `DEVDIGEST_EXTRA_CA_FILE` (custom providers FR14: optional PEM bundle added to the
  system trust store; unreadable or certificate-less → the backend does not start, one ERROR line naming the variable
  and the path) shall apply to every GitLab REST, GraphQL and git call. Whichever of the two specs ships first
  introduces the setting; the other reuses it.

MR sync
- FR21 [sync] — For a GitLab repository, every list sync (`GET /repos/:id/pulls`, `POST /repos/:id/poll`, background
  poll) shall read `GET /projects/:id/merge_requests` with `state=all`, `order_by=updated_at`, `sort=desc`, 50 per
  page — one page for the list request and the manual poll; for the background poll the pages, cutoff and horizon of
  PR Leaderboard FR4 — and upsert by (repo, number) with the same inserted and updated fields as GitHub (Phase 1
  FR36, PR Leaderboard FR2).
- FR22 [sync] — MR mapping:

  | Stored field | From the GitLab MR |
  |---|---|
  | number | `iid` |
  | title | `title` |
  | author | `author.username`, else `unknown` |
  | branch / base | `source_branch` / `target_branch` |
  | head_sha | `sha` |
  | opened_at / updated_at | `created_at` / `updated_at` |
  | merged_at / closed_at | `merged_at` / `closed_at` |
  | draft | `draft` |
  | status | `merged` → `merged`; `closed` or `locked` → `closed`; `opened` → `open` |

  MRs from forks are synced like any other. An MR without `sha` is skipped in that sync and counted nowhere.
- FR23 [sync] — Failure behaviour matches GitHub's: `GET /repos/:id/pulls` logs a missing token or any GitLab failure
  and serves the stored rows with 200; `POST /repos/:id/poll` answers 500 `config_error` "GitLab connection <name>
  has no token" without a token and 502 `external_service` "GitLab request failed" on a GitLab failure after retries.
- FR24 [sync] — `GET /pulls/:id` for a GitLab MR shall fetch the MR, its diffs (up to 3,000 files) and its commits
  (up to 250) and, on success, replace the stored files and commits and update `body`, `additions`, `deletions` and
  `files_count` in one transaction (Phase 1 FR38):
  - file path = `new_path` (`old_path` for a deleted file); additions and deletions are counted from the diff's `+`
    and `-` lines, hunk headers excluded; the patch keeps GitLab's hunk text; a collapsed, too-large or empty diff →
    no patch and 0/0; same-path files are merged as for GitHub;
  - `body` = `description`; commits: `sha` = `id`, `message`, `author` = `author_name` else `unknown`,
    `committed_at` = `committed_date`;
  - `additions`, `deletions`, `files_count` come from the diff-stats source (FR26), else from the sums of the files
    and their count;
  - on any failure or without a token the stored detail is served (FR38 rules).
- FR25 [sync] — `linked_issue` of a GitLab MR shall follow Phase 1 FR39 (first `#<digits>`, optional
  closes/fixes/resolves), fetched with `GET /projects/:id/issues/:iid` and mapped `number` = `iid`, `title`, `body` =
  `description`, `state` `opened` → `open`, `closed` → `closed`; on no match or any failure it is omitted, never
  stored.
- FR26 [sync] — Diff stats (additions, deletions, files) of GitLab MRs shall come from one read-only GraphQL query per
  repository and batch of up to 20 MRs (by iid), reading each MR's diff-stats summary; a summary of all zeros counts
  as no summary (as 0/0/0 does for GitHub today). It serves the list back-fill
  (Phase 1 FR36 step 2), the background-poll stats fill (PR Leaderboard FR24, up to 20 MRs) and the detail fetch.
  When the query fails or returns no summary for an MR, its stats stay unset, the failure is logged once per
  repository and batch, and the next cycle tries again; there is no fallback that reads diff pages for stats, so an
  MR without stats is skipped by the size metrics as an unfilled GitHub PR is today (D19, D29).
- FR27 [sync] — The background poll shall work per host: each repository is polled with its host's client and token;
  no GitHub token → GitHub repositories skipped (one WARN per cycle); a GitLab connection without a token → its
  repositories skipped (one WARN per connection per cycle); a not-connected GitLab repository (FR48) → skipped with
  no request, one WARN per host key per cycle, and its host is not paused; a rate limit reported by a host (429 with
  `Retry-After`,
  else `RateLimit-Reset`, capped at 2 h) pauses only that host's repositories; the other hosts' repositories are
  polled in the same cycle. A failed repository is logged and the cycle goes on (PR Leaderboard FR5 otherwise
  unchanged).
- FR28 [sync] — Automatic reviews (Settings C) shall treat GitLab MRs like GitHub PRs: the "opened time" of FR4 there
  is the MR's `created_at`. The status state shall add `no_code_host_token`, placed where `no_github_token` is in the
  precedence: when the workspace has at least one GitLab repository, `no_github_token` is never answered and
  `no_code_host_token` is answered when no repository of the workspace has a token for its host (a not-connected
  repository has none); a workspace without GitLab repositories behaves as today.

Host isolation (until slices B and C)
- FR29 [api] — A GitLab repository shall never cause a GitHub request, a GitHub repository never a GitLab request, and
  a repository bound to one connection never a request to another. GitHub-only features degrade for GitLab PRs as
  they do without a token, with no request:
  - `GET /pulls/:id/comments` → 200 `[]`;
  - `POST /pulls/:id/comments` → 400 `gitlab_unavailable` "Posting to GitLab merge requests is not available yet.";
  - blast-radius prior PRs → empty history; brief linked issue → omitted; intent tickets → unresolved; intent docs →
    the clone only, without the API fallback;
  - `POST /agents/:id/export-ci`: `repo` is matched against the workspace's GitHub repositories only; a `full_name`
    that matches only a GitLab repository → 422 `unsupported_host` "GitLab CI export is not available yet", checked
    in place of `repo_not_found` (Phase 6 §7.6.1).

Data
- FR30 [db] — One versioned SQL migration with an up and a down shall add the connection entity (installation-wide),
  the repository provider (`github`|`gitlab`, existing rows `github`), host (existing rows `github.com`), project id
  and stored web URL (both required for GitLab, empty for GitHub), and replace the repository uniqueness `(workspace, full_name)` with
  `(workspace, host, full_name)` plus `(workspace, host, project id)` for GitLab. The down migration refuses with a
  clear message while any GitLab repository or connection exists, and otherwise restores the previous schema.

Redaction
- FR31 [api] — Every stored GitLab token shall be redacted by exact value wherever stored keys and the GitHub token are
  redacted today (LLM-bound text, error messages, git output, test messages, client errors). The onboarding pattern
  redaction shall add GitLab token prefixes: `glpat-`, `gloas-`, `gldt-`, `glrt-`, `glrtr-`, `glcbt-`, `glptt-`,
  `glft-`, `glimt-`, `glagent-`, `glsoat-`, `glffct-`, each followed by at least 20 token characters.

UI — Settings, setup and add repository
- FR32 [ui] — The Settings sub-nav shall insert "GitLab Integration" (`gitlab`) after "GitHub Integration"; the other
  sections keep their order (Settings A FR1).
- FR33 [ui] — GitLab Integration shall show the intro "Connect gitlab.com or a self-hosted GitLab. Each instance has
  its own token; repositories use the connection of their host.", one row per connection — name, base URL, a badge
  "Token saved" / "No token", "<n> repositories", and Test, Edit and Delete — and "Add GitLab connection". Empty state:
  "No GitLab connections yet". On opening the section, the UI tests the first 5 connections with a saved token, one
  at a time, in list order (D28).
- FR34 [ui] — A test result row shall show "Connected as @<username>", the scopes ("Scopes: api · read_repository",
  or "Scopes not reported"), "Expires <date>" or "No expiry date", "GitLab <version>" (or "Version unknown"), and one
  warning banner per returned warning with the copy of FR11. A failed test shows the error banner with the message;
  badge states follow Settings A FR4.
- FR35 [ui] — The add/edit dialog shall have: name (placeholder = the base URL's host); base URL (default
  `https://gitlab.com`, helper "Instance URL, including a relative URL root such as https://git.example.com/gitlab");
  the token field with Settings A FR3 masking, the placeholder "Token saved — paste a new token to replace it" when
  one is saved, and "Remove token" on edit; the link "Create a token" to the instance's personal access token page
  `<base_url>/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository` (new tab, D30); the hint "Scopes: api and
  read_repository. A read_api + read_repository token can sync but cannot post." Save sends POST or PUT; "Save and
  test" saves, then runs FR9. 422 field errors show at their field; 409 `gitlab_connection_exists` shows at the base
  URL field.
- FR36 [ui] — Delete shall ask for confirmation naming the connection and, from its `repo_count`, how many
  repositories become not connected: with `repo_count` ≥ 1 "Delete <name>? <n> repositories use <host> and become not
  connected: they stay listed with their reviews, but are not synced until you add a GitLab connection for <host>
  again. The token is removed."; with 0 "Delete <name>? Its token is removed." Confirm sends DELETE; a failure shows
  the error banner and keeps the row.
- FR37 [ui] — "Connected repositories" (Settings A FR19) shall list only GitHub repositories in GitHub Integration;
  GitLab Integration shall list the workspace's GitLab repositories with full name, host, default branch, index
  state and, for a not-connected one, the badge "Not connected", with Remove (dialog text "Nothing changes on
  GitLab.") and "Add repository".
- FR38 [ui] — Setup step 2 shall become "Code host" (stepper label; step id `code-host`, `github` accepted as an alias)
  with a choice GitHub (default) / GitLab. GitHub keeps Settings A FR24. GitLab shows base URL (default
  `https://gitlab.com`), the token field and "Save and test", which creates the connection (name = host key) or
  updates the one for that host, then runs FR9 and shows FR34. Continue is enabled after a successful test, or when
  a GitHub token or a GitLab connection with a token already exists ("Already connected"). The step is done when a
  GitHub token or any GitLab connection with a token exists.
- FR39 [ui] — Home gate (Settings A FR27): the second condition becomes "no GitHub token and no GitLab connection with
  a token" → `/setup?step=code-host`. The gate also loads GET `/settings/gitlab-connections`; when it fails, the
  error state with Retry shows and nothing redirects.
- FR40 [ui] — The add-repository form (setup step "Repository", Settings, `/onboarding` target) shall say "Paste a
  GitHub or GitLab repository URL", with the example "e.g. https://github.com/acme/payments-api or
  https://gitlab.com/acme/platform/payments". A 422 `repo_host_not_connected` shows its message with the link "Add a
  GitLab connection" to `/settings/gitlab`. An add error is shown once, in the form (no extra toast).

UI — links, references and labels
- FR41 [ui] — Outbound links shall be built from the repository's `provider` and `web_url` only, never from a URL in
  a GitLab or GitHub response:

  | Link | GitHub (unchanged) | GitLab |
  |---|---|---|
  | PR / MR | `<web_url>/pull/<n>` | `<web_url>/-/merge_requests/<n>` |
  | Issue | `<web_url>/issues/<n>` | `<web_url>/-/issues/<n>` |
  | File at a commit | `<web_url>/blob/<sha>/<path>#L<a>-L<b>` | `<web_url>/-/blob/<sha>/<path>#L<a>-<b>` |

  A single line is `#L<a>` on both hosts. Paths keep the current per-segment encoding.
- FR42 [ui] — Host-specific copy shall be used where an action or link touches the host: "View on GitLab" (PR detail
  header), "Open <path> on GitLab" (onboarding tour), "Nothing is deleted on GitLab" (remove repository), "Refresh to
  sync from GitLab" (empty PR list). Generic chrome keeps "PR" (D7).
- FR43 [ui] — A GitLab PR shall be referenced as `!<n>` instead of `#<n>` wherever a number is shown with or for its
  repository: PR list row, PR detail header, page title and breadcrumb, leaderboard rows, awards, needs attention and
  hot files, and agent run history.
- FR44 [ui] — Wherever a repository is named by its full name (repository switcher, Settings repository rows,
  leaderboard repository filter), a GitLab repository shall also show its host (for example "acme/platform/payments ·
  gitlab.com"). GitHub repositories are shown as today (D31).
- FR45 [ui] — For a GitLab PR, the UI shall not offer posting: no "Post to PR" action on findings and no inline
  composer in the diff; the comments area shows "Comments from GitLab are not shown yet."
- FR46 [api] — `GET /leaderboard` shall add `provider`, `host` and `web_url` to each `LeaderboardRepo`, so the page can
  apply FR41–FR44 by `repo_id`.

Renamed projects and not-connected repositories (D25–D27)
- FR47 [sync] — Every successful fetch of a GitLab project shall update the stored `full_name` (=
  `path_with_namespace`), `owner` (namespace full path), `name` (project path) and web URL (`<base_url>/<full_name>`,
  built from the connection, never taken from a GitLab response, FR41) to the project's current values. Project
  fetches happen at add (FR14, also when the add answers 200 with an existing repository) and as the first request of
  every list sync (`GET /repos/:id/pulls`, `POST /repos/:id/poll`, background poll) with `GET /projects/<project id>`;
  a failed project fetch is a failed sync (FR23, FR27) and no MR list request follows. `default_branch` is not updated
  (as for GitHub). The clone location (host key + project id, FR18) does not change; the next clone or refresh uses
  the updated path in its remote URL. When the new `full_name` equals the stored `full_name` of another repository of
  the same workspace and host, the path update is skipped for that fetch (the repository keeps its stored path and
  web URL), one WARN line names both repository ids, the sync goes on by project id, and the next fetch tries again.
- FR48 [api] — A GitLab repository is **not connected** while no connection's host key equals its host. Binding is
  resolved by host key at every GitLab call; repositories store no connection reference, so deleting a connection
  needs no change to repositories and creating a connection for the same host key re-binds them automatically,
  with no data migration (D26). For a not-connected repository:
  - all local data stays readable: GET `/repos`, `GET /repos/:id/pulls` (stored rows, 200), `GET /pulls/:id`
    (stored detail, `linked_issue` omitted), reviews, findings, leaderboard, smart diff and local review runs on the
    existing clone;
  - no GitLab request is sent by any route, job or poll: `POST /repos/:id/poll` → 422 `repo_host_not_connected` "No
    GitLab connection for <host>. Add one in Settings → GitLab Integration." with `details` {`host`, `repo_id`};
    adding its URL again → 422 `repo_host_not_connected` (FR13); a clone or refresh job ends `failed` with the same
    message, runs no git command and is not retried; the background poll skips it (FR27); GitHub-only features
    degrade per FR29;
  - calls already in flight when the connection is deleted finish and their results are stored; no new call starts.

  Slices B and C answer `repo_host_not_connected` for their GitLab calls (comments, context peers, CI) of a
  not-connected repository.
- FR49 [ui] — A not-connected GitLab repository shall show, on its PR list and PR detail, the banner "Not connected —
  add a GitLab connection for <host>" with the link "GitLab Integration" to `/settings/gitlab`, and the PR list's
  refresh/poll action is disabled; stored PRs, detail, reviews and findings are shown as usual. A repository is
  shown as not connected when its `provider` is `gitlab` and its `connection_id` is null.

## 6. Workflow and communication
```mermaid
sequenceDiagram
    participant U as Browser UI
    participant API as Go API
    participant S as Secrets store
    participant DB as Database
    participant GL as GitLab instance
    U->>API: POST /settings/gitlab-connections (name, base_url, token)
    API->>DB: store connection (host key unique)
    API->>S: store token (0600)
    API-->>U: 201 GitLabConnection (token_configured)
    U->>API: POST /settings/test-connection provider gitlab, connection_id
    API->>GL: GET /api/v4/user, /personal_access_tokens/self, /metadata (PRIVATE-TOKEN, no redirect)
    GL-->>API: username, scopes, expires_at, version
    API-->>U: ok, login, scopes, expires_at, instance_version, warnings
    U->>API: POST /repos url https://gitlab.com/acme/platform/payments
    API->>DB: find connection by host key
    API->>GL: GET /api/v4/projects/acme%2Fplatform%2Fpayments
    GL-->>API: id, path_with_namespace, default_branch
    API->>DB: insert repo (provider gitlab, host, project id), enqueue clone
    API-->>U: 201 Repo (provider, host, web_url, connection_id)
```
```mermaid
sequenceDiagram
    participant P as Background poller
    participant DB as Database
    participant GH as GitHub
    participant GL as GitLab instance
    participant J as Clone job and git
    J->>GL: git clone base_url/full_name.git (Basic oauth2 header scoped to base_url, no redirect)
    loop every repo, one at a time
        alt GitHub repo
            P->>GH: list pulls pages, then stats
        else GitLab repo, no connection for its host
            P->>P: skip, one WARN per host per cycle, no request, no pause
        else GitLab repo, host not paused, token present
            P->>GL: GET /projects/:id (current path)
            P->>DB: update full_name, owner, name, web URL unless another repo holds that path
            P->>GL: GET /projects/:id/merge_requests pages (cutoff, horizon)
            P->>GL: POST /api/graphql read-only diff stats for up to 20 iids
        end
        P->>DB: upsert PRs, set stats, last-polled time
    end
    Note over P,GL: 429 from one host pauses only that host's repos
```
Failure behaviour:

| Interaction (caller → callee) | Trigger | On failure |
|---|---|---|
| API → secrets store | connection create, update, delete, test with `key` | 500 envelope; the change is not applied (all or nothing) |
| API → GitLab, test calls | Test connection | 200 `ok:false` + scrubbed message (FR10); `/personal_access_tokens/self` or `/metadata` failure → null field + warning |
| API → GitLab, project lookup | POST `/repos` | 422/502 per FR14; nothing stored, no job; path held by another project → 409 `gitlab_path_conflict` |
| API/poller → GitLab, project fetch before a list sync | GET `/repos/:id/pulls`, POST poll, background cycle | failed sync (rows below); path collision → path kept, WARN, sync goes on (FR47) |
| API/poller/job → connection lookup by host key | any GitLab call of a repository | no connection → no request: stored data served, poll 422 `repo_host_not_connected`, job `failed`, poller skips (FR48) |
| API → GitLab, MR list | GET `/repos/:id/pulls` | logged; stored rows, 200 |
| API → GitLab, MR list | POST `/repos/:id/poll` | 500 `config_error` (no token) or 502 `external_service` |
| API → GitLab, detail, issue, stats | GET `/pulls/:id` | stored detail; issue omitted; stats from file sums |
| Poller → GitLab | background cycle | repo skipped and logged; 429 → that host paused; no token → host skipped with WARN |
| Clone job → git → GitLab | add, refresh | job `failed` with scrubbed git error (Phase 1 FR32 retries); redirect not followed |
| API/git → DNS, TLS | any GitLab call | link-local/unspecified address or untrusted certificate → call fails, nothing sent |
| UI → any route | — | error state with Retry; field errors at their field |

## 7. Contracts
All new routes keep the guards of every route: Host allow-list; POST/PUT/DELETE with a foreign `Origin` → 403; JSON
bodies only as `application/json` (415 otherwise); error envelope `{"error":{"code","message","details?"}}`; 422
`validation_error` with Zod-style `details` and `path`.

`GitLabConnection` (response):
```
{ id: string,                       // UUID
  name: string, base_url: string,   // normalised (FR2)
  host: string,                     // host key, e.g. "gitlab.com", "git.corp.example:8443"
  token_configured: boolean,
  repo_count: integer,              // repos bound in all workspaces
  created_at: string, updated_at: string }   // ISO 8601 UTC
```
`GitLabConnectionInput` (POST; PUT takes the same fields, all optional):
```
{ name?: string,            // 1–80 after trim; empty/omitted on POST → host key
  base_url: string,         // FR2; PUT: same host key only (FR5)
  token?: string|null }     // write-only, 1–4096, no whitespace; PUT: omitted = keep, null = remove
```

| Route | Success | Errors |
|---|---|---|
| GET `/settings/gitlab-connections` | 200 `GitLabConnection[]` | 500 |
| POST `/settings/gitlab-connections` | 201 `GitLabConnection` | 422 `validation_error`; 422 `gitlab_connection_limit` "At most 20 GitLab connections"; 409 `gitlab_connection_exists` "A GitLab connection for <host> already exists"; 403; 415 |
| PUT `/settings/gitlab-connections/:id` | 200 `GitLabConnection` | 404 `gitlab_connection_not_found`; 422 `validation_error` (incl. host change); 403; 415 |
| DELETE `/settings/gitlab-connections/:id` | 200 `{ deleted: string, disconnected_repos: integer }` — allowed while repositories are bound; they become not connected (FR7, FR48) | 404 `gitlab_connection_not_found`; 403 |

Test connection (`contracts/platform.ts`):
```
ConnTestProvider  += "gitlab"
ConnTestRequest   += connection_id?: string        // required when provider = "gitlab"
ConnTestResult for provider "gitlab" (all keys present; null when ok = false):
{ provider: "gitlab", ok, message, latency_ms,
  login: string|null,                          // GitLab username
  scopes: string[]|null,                       // null = not reported
  expires_at: string|null,                     // "YYYY-MM-DD"; null = no expiry or not reported
  instance_version: string|null,               // e.g. "17.5.1-ee"
  token_access: "full"|"read_only"|null,
  warnings: [{ code: "read_only_token"|"no_repository_scope"|"token_expiring"|"version_unsupported"
                    |"scopes_unknown"|"version_unknown", message: string }] }   // [] when none
```
Errors added: 404 `gitlab_connection_not_found`; 422 at `["connection_id"]`. The bucket (20/min per IP) is unchanged.
The custom providers spec's single `warning` field is not used for `gitlab`.

`Repo` gains (always present): `provider: "github"|"gitlab"`, `host: string`, `web_url: string`,
`connection_id: string|null` (null for GitHub and for a not-connected GitLab repository; for GitLab, null means
"not connected", FR48). `owner` is the namespace full path for GitLab (may contain `/`); `owner`, `name`,
`full_name` and `web_url` of a GitLab repository follow the project's current path (FR47). `RepoInput.url` accepts
FR12 forms; its message becomes the FR12 text.

| Route | Change |
|---|---|
| POST `/repos` | 201/200 as today; new errors: 422 `repo_host_not_connected` `details` {`host`}; 422 `gitlab_project_not_found`; 422 `gitlab_permission_denied`; 409 `gitlab_path_conflict` `details` {`repo_id`} (FR15); 502 `gitlab_rate_limited`; 502 `gitlab_unavailable`; 422 `validation_error` with the FR12/FR13 messages |
| GET `/repos` | each `Repo` with the new fields |
| GET `/repos/:id/pulls`, GET `/pulls/:id` | shapes unchanged; `number` = MR iid for GitLab |
| POST `/repos/:id/poll` | GitLab: 500 `config_error` "GitLab connection <name> has no token"; 502 `external_service` "GitLab request failed"; not connected → 422 `repo_host_not_connected` `details` {`host`, `repo_id`} (FR48) |
| GET `/pulls/:id/comments` | GitLab PR → 200 `[]` |
| POST `/pulls/:id/comments` | GitLab PR → 400 `gitlab_unavailable` "Posting to GitLab merge requests is not available yet." |
| POST `/agents/:id/export-ci` | 422 `unsupported_host` "GitLab CI export is not available yet" (FR29) |
| GET `/leaderboard` | `LeaderboardRepo` += `provider`, `host`, `web_url` |
| GET `/settings/automatic-reviews-status` | `state` += `"no_code_host_token"` (FR28) |
| GET `/workspace`, GET `/settings/secrets-status` | unchanged |

Error codes reserved for GitLab, mirroring the GitHub ones (slices B and C reuse them): `gitlab_unavailable`,
`gitlab_permission_denied`, `gitlab_rate_limited`, `gitlab_comment_failed` (B), plus `gitlab_connection_exists`,
`gitlab_connection_not_found`, `gitlab_connection_limit`, `gitlab_project_not_found`, `gitlab_path_conflict`,
`repo_host_not_connected` (add-repo without a connection, and every GitLab call of a not-connected repository),
`unsupported_host`. `gitlab_connection_in_use` is not used (D25). GitHub codes are unchanged.

GitLab endpoints called (external contract; token in `PRIVATE-TOKEN` for REST and GraphQL, Basic `oauth2` for git):
`GET /api/v4/user`, `/personal_access_tokens/self`, `/metadata`, `/projects/:path-or-id` (path at add, id before
every list sync),
`/projects/:id/merge_requests`, `/projects/:id/merge_requests/:iid`, `/…/:iid/diffs`, `/…/:iid/commits`,
`/projects/:id/issues/:iid`; `POST /api/graphql` (query only); git smart HTTP under `<base_url>/<full_name>.git`.

Deploy configuration: `DEVDIGEST_EXTRA_CA_FILE` (optional, FR20), shared with custom providers.

Shared-contract consumers: `platform.ts` (`ConnTestProvider`, `ConnTestRequest`, `ConnTestResult`, `Repo`,
`RepoInput`, `parseGitHubRepoUrl` replaced by a host-neutral parser, new `GitLabConnection*`), `leaderboard.ts`
(`LeaderboardRepo`), the Settings C status contract. Each ships with the matching backend change; every new or
changed shape gets a Go golden checked by `frontend/src/test/go-backend-contracts.test.ts`; `SecretsStatus` and its
strict golden are unchanged.

## 8. Data model (logical)
- GitLab connection (new, persisted, installation-wide, no workspace): id, name, normalised base URL, host key
  (unique), created/updated time. Lives until deleted; deletion is allowed while repositories are bound (FR7).
- Connection token (secrets store, not the database): one per connection id; deleted with the connection.
- Repository (existing, per workspace) gains provider, host, and for GitLab a project id and a stored web URL.
  Unique per workspace on (host, full name), and for GitLab on (host, project id). For GitLab, `full_name`, `owner`,
  `name` and the stored web URL are a cache of the project's current path, refreshed on every successful project
  fetch (FR47); the project id is the identity. A repository is bound to the connection whose host key equals its
  host — resolved at every call, not a stored reference (D26); with no such connection it is not connected (FR48)
  and keeps all its data.
- Pull request, files, commits (existing): unchanged; for GitLab, `number` holds the MR iid.
- Clone (disk): GitHub `<cloneRoot>/<owner>/<name>` unchanged; GitLab under a host-scoped location keyed by project id
  (FR18). Deleted with the repository as today.
- Derived, never stored: `connection_id`, `repo_count`, the not-connected state, the GitHub `web_url` and a
  connected GitLab repository's response `web_url`, test results, token access and warnings.

## 9. States and UX
| Screen / element | Loading | Empty | Error | Degraded | Success | Notes |
|---|---|---|---|---|---|---|
| GitLab connection list | row skeletons | "No GitLab connections yet" + Add | list error + Retry | connection without token → "No token" badge | rows, badge, repo count | first 5 auto-tested |
| Connection test result | "Testing…", disabled | not tested in this view → no result | "Failed" + message | warnings banners (read-only, no repository scope, expiring, old version, unknown scopes/version) | "Connected as @user", scopes, expiry, version | view-local |
| Add/edit dialog | "Saving…"/"Testing…" | new: base URL `https://gitlab.com`, Save disabled until valid | field errors; 409 at base URL | — | closes on Save; result after Save and test | focus trap |
| Delete connection | "Deleting…" | — | error banner, row kept | — | row removed; its repositories show "Not connected" | confirm dialog names how many repositories become not connected (FR36) |
| GitLab repositories (Settings) | row skeletons | "No GitLab repositories yet" + Add | list error + Retry | "not indexed yet"; "Not connected" badge | rows with host | Remove confirms |
| Setup "Code host" | "Testing…" | GitHub selected | error banner, Continue disabled | warnings shown, Continue enabled | verified, Continue enabled | alias `github` |
| Home gate | skeleton | — (redirects) | error + Retry, no redirect | — | redirect | loads connections too |
| Add-repository form | "Cloning…" | empty field | message; host not connected → link to Settings | — | navigates to PRs | one error display |
| PR list / detail (GitLab repo) | as today | as today ("Refresh to sync from GitLab") | as today (stored rows) | no token → stored rows; not connected → banner "Not connected — add a GitLab connection for <host>" + link, refresh disabled, stored rows | `!<n>`, "View on GitLab" | no posting |
| Diff comments area (GitLab PR) | — | "Comments from GitLab are not shown yet." | — | — | — | no composer |
| Leaderboard | as today | as today | as today | as today | GitLab rows with `!<n>` and host | |

## 10. Edge cases
- EC1 [api] — Base URL `http://gitlab.example`, `https://u:p@gitlab.example`, `https://gitlab.example/?a=1`,
  `https://gitlab.example/#x`, `https://gitlab.com/api/v4`, `https://github.com`, `https://169.254.169.254`,
  `https://[fe80::1]`, `https://0.0.0.0` or 2,049 characters → 422 at `["base_url"]` (FR2).
- EC2 [api] — `https://GitLab.Corp.Example:443/gitlab/` is stored as `https://gitlab.corp.example/gitlab` with host
  `gitlab.corp.example`; `https://gitlab.corp.example:8443` gets host `gitlab.corp.example:8443` and is a different
  host (FR2).
- EC3 [api] — A second connection for an existing host key with another path prefix → 409 `gitlab_connection_exists`;
  two concurrent creates for one host → exactly one 201 (FR1, FR3).
- EC4 [api] — PUT changing only the path prefix → 200 and bound repositories' `web_url` follow; PUT changing the host
  key → 422, nothing changed (FR5).
- EC5 [api] — DELETE while 2 repositories (in two workspaces) are bound → 200 with `disconnected_repos: 2`; the
  token is gone from the secrets store, the list no longer shows the connection, and both repositories are still
  listed with `connection_id: null` and their PRs (FR7, FR48).
- EC6 [api] — Test against an instance answering `/user` with 302, an HTML page, 401, or a delay past the timeout →
  `ok:false` with the FR10 message; the `Location` host receives no request; nothing contains the token (FR10).
- EC7 [api] — `expires_at` null → no expiry warning; today + 14 days → `token_expiring`; today + 15 days → none
  (FR11).
- EC8 [api] — Version `16.11.3-ee` → `version_unsupported`; `17.0.0` → none; `/metadata` 403 → `instance_version` null
  and `version_unknown` (FR10, FR11).
- EC9 [api] — Scopes `[read_api, read_repository]` → `ok:true`, `read_only`, `read_only_token`; `[api]` → `full`, no
  warning; `[read_api]` → `read_only` plus `no_repository_scope`; `[read_user]` → `ok:false`;
  `/personal_access_tokens/self` 400 or 404 (for example a token type it does not report) → `scopes:null`,
  `token_access:null`, `scopes_unknown`, `ok:true` (FR10, FR11).
- EC10 [api] — The token is rotated while a background poll runs → requests already sent finish; the next request of
  that repository uses the new token (FR6).
- EC11 [api] — `https://git.corp.example/other/group/proj` with connection base `https://git.corp.example/gitlab` →
  422 "This URL is outside https://git.corp.example/gitlab"; `git@git.corp.example:group/proj.git` with connections on
  ports 443 and 8443 → 422 "Several GitLab connections use git.corp.example" (FR13).
- EC12 [api] — `https://gitlab.com/acme/web` without a gitlab.com connection → 422 `repo_host_not_connected`
  `details.host` `gitlab.com`; `https://gitlab.com/acme/web/-/merge_requests/1` → 422 `validation_error`; a path of
  22 segments → 422 `validation_error` (FR12, FR13).
- EC13 [api] — The same project added as `https://gitlab.com/Acme/Web` (GitLab resolves paths case-insensitively) and
  as `git@gitlab.com:acme/web.git` → 200 with the existing repository, no second clone job; an old path of a moved
  project → 422 with the "has moved" message (FR14, FR15).
- EC14 [api] — A GitHub `acme/web` and a GitLab `acme/web` in one workspace → both are stored, each with its own clone
  and PRs; neither add answers 200 with the other (FR15, FR30).
- EC15 [sync] — A connection without a token: adding a public project works anonymously; `GET /repos/:id/pulls`
  serves stored rows; `POST /repos/:id/poll` → 500 `config_error`; the background poll skips its repositories with
  one WARN per cycle (FR14, FR23, FR27).
- EC16 [sync] — `git.corp.example` answers 429 with `Retry-After: 120` → its repositories are not polled for 120 s;
  gitlab.com and GitHub repositories are polled in the same cycle (FR27).
- EC17 [sync] — MR states `locked` → `closed`, `opened` with `draft: true` → `open` and draft; an MR from a fork is
  stored; an MR without `sha` is skipped and stored on a later sync (FR22).
- EC18 [sync] — A detail with a too-large file, a collapsed file and a deleted file → all three paths present; the
  first two without patch and 0/0, the deleted one under `old_path` with its `-` count; more than 3,000 files → the
  first 3,000 (FR24).
- EC19 [sync] — The GraphQL query answers an error or the instance disables GraphQL → stats stay unset, one log line
  per repository and batch, the cycle goes on, and the next cycle retries (FR26).
- EC20 [api] — A project renamed or moved on GitLab (same id, new path `acme/core/payments`) → the next add, list sync
  or poll goes on by project id and stores `full_name` `acme/core/payments`, `owner` `acme/core`, `name` `payments`
  and the new `web_url`; links use the new path; the clone directory stays where it is and the next refresh fetches
  from the new remote URL (FR17, FR47).
- EC21 [git] — Clone with no token → anonymous; a private project fails with a scrubbed git error; the remote answers
  a redirect → not followed and the job fails (FR19).
- EC22 [git] — A GitHub repository owned by a user named `gitlab` and a GitLab project on `gitlab.com` → their clone
  directories never overlap (FR18).
- EC23 [ui] — A 422 `repo_host_not_connected` in the add form → the message and the link "Add a GitLab connection"
  show once, with no toast (FR40).
- EC24 [api] — For a GitLab PR, `GET /pulls/:id/comments` → `[]`; `POST /pulls/:id/comments` → 400
  `gitlab_unavailable`; neither sends a request to GitHub or GitLab (FR29).
- EC25 [api] — CI export with `repo: "acme/web"` when the workspace has only a GitLab `acme/web` → 422
  `unsupported_host`; with both a GitHub and a GitLab `acme/web` → the GitHub repository is used (FR29).
- EC26 [api] — The secrets store write fails while creating a connection → 500 and no connection exists afterwards
  (FR4).
- EC27 [ui] — The base URL field gets `https://gitlab.com/api/v4` → the FR2 message shows at the field and nothing is
  saved (FR35).
- EC28 [ui] — Connections with saved tokens: 7 → only the first 5 are tested on opening; the others show Test
  (FR33).
- EC29 [db] — The down migration runs while a GitLab repository exists → it fails with a message naming the GitLab
  data and changes nothing; with only GitHub data it restores the old uniqueness (FR30).
- EC30 [api] — A connection is deleted while a list sync of a bound repository waits on GitLab → that request
  finishes and its rows are stored; the repository's next sync sends no request (FR48).
- EC31 [api] — For a not-connected repository: `GET /repos/:id/pulls` → 200 stored rows; `GET /pulls/:id` → stored
  detail without `linked_issue`; `POST /repos/:id/poll` → 422 `repo_host_not_connected` with `details.host` and
  `details.repo_id`; adding its URL again → 422 `repo_host_not_connected`; a refresh job → `failed` with the message,
  no git command, no retry; the leaderboard and a local review of a stored PR work; no request reaches any GitLab
  or GitHub stub (FR48).
- EC32 [sync] — A cycle with not-connected repositories on `git.corp.example` and connected ones on gitlab.com → the
  not-connected ones are skipped with one WARN for `git.corp.example`, the host is not paused, gitlab.com and GitHub
  repositories are polled (FR27, FR48).
- EC33 [api] — After the gitlab.com connection was deleted, a new connection for `https://gitlab.com` is created →
  its repositories show the new `connection_id`, the next sync uses the new token, the existing clone is reused at
  the same location and no clone job is enqueued; a new connection for `https://gitlab.com:8443` (another host key)
  does not bind them (FR48).
- EC34 [sync] — Repository X (id 1, stored `acme/old`) is renamed on GitLab to `acme/new` while repository Y (id 2) of
  the same workspace and host is still stored as `acme/new` → X's sync keeps `acme/old`, logs one WARN with both
  repository ids, and upserts its MRs; after Y's sync stores Y's new path, X's next sync stores `acme/new` (FR47).
- EC35 [api] — Adding `https://gitlab.com/acme/new` (project id 3) while repository Y (id 2) is still stored as
  `acme/new` → 409 `gitlab_path_conflict` with `details.repo_id` of Y, nothing stored, no job; after Y's sync
  updates its path, the same add → 201 (FR15).
- EC36 [ui] — The delete dialog of a connection with `repo_count` 3 says "3 repositories use <host> and become not
  connected"; with `repo_count` 0 it says only that the token is removed (FR36).

## 11. Non-functional requirements
- NFR1 [api] — No GitLab token, or part of one, appears in any response, log line, error message, git argv, remote
  URL, clone git config, LLM-bound text, workspace export or contract golden. Redaction happens before any truncation
  (`backend/INSIGHTS.md:60`). Tests assert it with a recognisable `glpat-` value of ≥ 20 characters.
- NFR2 [api] — Outbound safety: a GitLab token is sent only to its connection's scheme, host, port and path prefix
  (REST root, GraphQL, git); neither the API client nor git follows a redirect; TLS verification is always on with
  the system store plus `DEVDIGEST_EXTRA_CA_FILE`; link-local and unspecified addresses are refused at connect time.
- NFR3 [api] — Read-only toward GitLab: no route, job or poll of this slice sends any GitLab request other than GET,
  and POST to `/api/graphql` with a query that has no mutation. Forward reference for slice B: GitLab runs quick
  actions (lines starting with `/`, for example `/approve`, `/merge`, `/close`) in note bodies as the token's user;
  every note body slice B posts (LLM finding text, replies) shall neutralise them, along with HTML and `@` mentions.
- NFR4 [sync] — Request budget: the background poll sends at most 1 concurrent code-host request; per GitLab
  repository and cycle at most 13 GitLab requests (1 project fetch + ≤ 10 list pages + ≤ 2 stats queries), within PR
  Leaderboard NFR3; `GET /repos/:id/pulls` and `POST /repos/:id/poll` add at most 3 GitLab requests (1 project fetch
  + 1 list page + 1 stats query); a GitLab detail fetch makes at most 1 + 30 + 3 + 1 + 1 requests (MR, diff pages,
  commit pages, issue, stats); a not-connected repository causes 0 requests.
- NFR5 [api] — Timeouts and retries match the GitHub client: 30 s per REST or GraphQL call, 90 s per detail fetch, up
  to 3 retries of transient errors in the foreground and none in the background poll; each test call 15 s.
- NFR6 [api] — Every new route keeps the Host allow-list, the foreign-Origin 403 on POST/PUT/DELETE, JSON-only bodies
  and the error envelope.
- NFR7 [api] — Compatibility: with no GitLab connection and only GitHub repositories, every existing golden and test
  passes unchanged apart from the added `Repo` and `LeaderboardRepo` fields, the FR12 message, and the
  `repo_host_not_connected` code for well-formed non-GitHub URLs; GitHub URLs, clone locations, git auth headers and
  GitHub requests are byte-identical to today.
- NFR8 [api] — GET `/settings/gitlab-connections` p95 ≤ 100 ms with 20 connections and 500 repositories.
- NFR9 [api] — Observability: each GitLab call logs one line with connection host, operation, HTTP status (or error
  class), attempt and latency; each skipped host logs one WARN per cycle; no line contains a token, a title, a
  description or a diff.
- NFR10 [api] — Untrusted GitLab text (titles, descriptions, commit messages, diffs, user names, file paths) is
  stored and shown under the same rules as GitHub text: rendered as text or Markdown without raw HTML; file paths
  from GitLab never become clone-location segments.
- NFR11 [ui] — Accessibility: dialog fields have labels; dialogs trap focus and return it on close; field errors are
  linked with `aria-describedby`; banners use `role="status"` / `role="alert"`; badges, warnings and host labels carry
  text, not colour only.
- NFR12 [ui] — All new copy is in `messages/en/*.json`; the pseudo-messages test finds no hard-coded copy.

## 12. Dependencies and rollout
- Shared-contract change: yes — `platform.ts` (`ConnTestProvider`, `ConnTestRequest.connection_id`, `ConnTestResult`
  GitLab fields, `Repo` fields with `connection_id` null meaning "not connected" for GitLab, `RepoInput` and the URL
  parser, new `GitLabConnection*` and the DELETE response `{deleted, disconnected_repos}`), `leaderboard.ts`
  (`LeaderboardRepo`), the Settings C status enum, the new error codes `gitlab_path_conflict` and
  `repo_host_not_connected` on POST `/repos/:id/poll`. Shipped with the backend in one change set; `SecretsStatus`
  unchanged.
- Persisted data change: yes — FR30 migration (up and down) including the GitLab project id and stored web URL; the
  stored GitLab path fields are rewritten by syncs (FR47); secrets store gains one token entry per connection and
  loses it on delete.
- No feature flag: without a GitLab connection nothing changes for users (NFR7).
- Deploy: `DEVDIGEST_EXTRA_CA_FILE` documented in `.env.example`; the PEM file is mounted into the backend container.
- Order: planning starts only after Settings A passes its review gate (D3); this spec builds on Settings A's final
  contracts at `c9c4689`. Slices B and C depend on this slice and are independent of each other.
- Conflicts with related specs, stated explicitly (those specs are not edited, D10):

  | Spec | Item | Relation |
  |---|---|---|
  | Settings A | FR1 sub-nav | extended by FR32 (`gitlab` after `github`) |
  | Settings A | FR15 info box "see GitHub Integration" | kept (CI stays GitHub-only until slice C) |
  | Settings A | FR16, §7 `ConnTestProvider`/`ConnTestRequest`; worktree `validate_test.go:234` (`gitlab` → invalid enum) | extended by FR9–FR11; that test case is superseded |
  | Settings A | FR17, FR18 (GitHub token, comment identity) | kept for GitHub; GitLab has no comment identity until slice B |
  | Settings A | FR19 connected repositories | narrowed to GitHub repositories by FR37 |
  | Settings A | FR22 stepper "GitHub token", FR24 | superseded by FR38 ("Code host", id `code-host`, alias `github`) |
  | Settings A | FR25 "(GitHub URL)" | superseded by FR40 |
  | Settings A | FR27 home gate, EC14 | extended by FR39 |
  | Settings A | AC16, AC18–AC20 | kept for GitHub; GitLab behaviour in AC19–AC23 here |
  | Custom providers | FR14 `DEVDIGEST_EXTRA_CA_FILE` | reused and extended to git (FR20) |
  | Custom providers | FR3 address rules | reused without plain http (FR2) — intentional difference |
  | Custom providers | 409 `provider_in_use` on delete | intentionally different: a GitLab connection can be deleted while used; its repositories become not connected (FR7, FR48, D25) |
  | Custom providers | FR21 home gate, `ConnTestProvider` widening, `warning` | compatible: the gate loads both lists; `gitlab` uses `warnings[]` |
  | Settings C | FR4 "GitHub opened time" | extended by FR28 (MR `created_at`) |
  | Settings C | FR12, §7.2 state enum `no_github_token` | extended by FR28 (`no_code_host_token`) |
  | Settings C | FR13 subtitle "Poll GitHub for new PRs…" | copy becomes "Poll your code hosts for new PRs…" when a GitLab repository exists |
  | Settings D | Out of scope `target_type = gha`, FR1/FR2 GitHub Action card | unchanged here (slice C) |
  | PR Leaderboard | area `[sync]` "GitHub PR sync"; FR1/FR2 "GitHub merge time" | extended to GitLab by FR21, FR22 |
  | PR Leaderboard | FR5 skip-all without token, whole-cycle rate-limit pause | superseded by FR27 (per host) |
  | PR Leaderboard | FR24 stats source, NFR3 budget | FR26; NFR3 kept (NFR4 here) |
  | PR Leaderboard | NFR2 "no GitHub calls" | read as "no code-host calls" |
  | PR Leaderboard | §7 `LeaderboardRepo` | extended by FR46 |
  | Phase 1 | FR26, §7.3 URL forms and message, AC53 | extended by FR12–FR15; AC53's `https://gitlab.com/o/r` case now answers 422 `repo_host_not_connected` (or is valid with a gitlab.com connection); its other cases are unchanged |
  | Phase 1 | FR35 clone paths exactly `<cloneRoot>/<owner>/<name>`, AC99 | extended by FR18 (GitLab form); GitHub unchanged |
  | Phase 1 | FR33 clone URL, EC11/Q7 SSH over HTTPS | extended by FR18 |
  | Phase 1 | FR31, FR36, FR38, FR39 sync, detail, issue | extended by FR21–FR25 |
  | Phase 1 | §8 Repo unique (workspace, full_name) | superseded by FR30 |
  | Phase 6 | §7.6.1 check order (`repo_not_found`) | extended by FR29 (`unsupported_host`) |
- What slices B and C must know (forward references, not specified here):
  - Binding is by host key, resolved at each call (D26): a GitLab repository whose host has no connection is not
    connected (`Repo.connection_id` null); every GitLab call of B and C for it shall send no request and answer 422
    `repo_host_not_connected` with `details` {`host`, `repo_id`} (FR48). The stored `full_name` of a GitLab
    repository can change on any sync (FR47); B and C address projects by project id and match `repo` inputs
    (for example CI export) against the current stored `full_name`.
  - B posts MR notes as positioned discussions, replies into discussions and reads diff notes; it needs `api` scope
    (`read_only` tokens fail with `gitlab_permission_denied`) and must meet NFR3's quick-action rule; it adds the
    comment identity "Notes are posted as @<username>" to GitLab Integration and replaces FR29's comment and
    context-peer degradations and FR45.
  - C (user decisions of 2026-10-24, D11): the runner posts a summary note plus inline threads via draft notes and
    bulk publish, never approves or requests changes, and reads the token from the masked variable
    `DEVDIGEST_GITLAB_TOKEN`; config at `.gitlab/devdigest/review-<slug>.yml` with a root `include` (an existing
    `.gitlab-ci.yml` is never modified; the snippet is shown); the runner is the committed blob on a digest-pinned
    public image, sha256-verified, Linux amd64. C replaces FR29's `unsupported_host`.

## 13. Acceptance criteria
- AC1 [api] — Given no connection, when POST `/settings/gitlab-connections` `{base_url:"https://GitLab.Corp.Example:443/gitlab/", token:"glpat-…"}`
  is sent, then 201 with `name` and `host` `gitlab.corp.example`, `base_url` `https://gitlab.corp.example/gitlab`,
  `token_configured:true`, `repo_count:0`; GET lists it; the secrets store holds the token with mode 0600; no request
  reached any stub GitLab. `https://gitlab.corp.example:8443` creates a second connection with host
  `gitlab.corp.example:8443`.
  Traces: FR1, FR2, FR3, FR4, EC2 · Verify: integration
- AC2 [api] — Given each EC1 base URL, then POST answers 422 `validation_error` at `["base_url"]` and stores nothing.
  Given a host name that resolves to 169.254.169.254, then a test answers `ok:false` and no request is sent.
  Traces: FR2, EC1, NFR2 · Verify: integration
- AC3 [api] — Given a connection for `gitlab.corp.example`, when another POST uses `https://gitlab.corp.example/other`,
  then 409 `gitlab_connection_exists`; two concurrent POSTs for a new host give exactly one 201; a 21st connection
  gives 422 `gitlab_connection_limit`; PUT or DELETE of an unknown id gives 404 `gitlab_connection_not_found`.
  Traces: FR1, FR3, EC3 · Verify: integration
- AC4 [api] — Given a connection, when PUT changes the path prefix, then 200 and a bound repository's `web_url` uses the
  new prefix; PUT with another host → 422 and nothing changes; PUT without `token` keeps it, `token:null` removes it
  (`token_configured:false`), a new token is used by the next stub request. Given a secrets write failure on POST,
  then 500 and GET shows no new connection.
  Traces: FR4, FR5, FR6, EC4, EC26 · Verify: integration
- AC5 [api] — Given a connection whose host has 2 repositories in two workspaces (GET shows `repo_count:2`), when
  DELETE is sent, then 200 `{deleted:<id>, disconnected_repos:2}`, the token entry is gone from the secrets store,
  GET no longer lists the connection, and GET `/repos` of each workspace still lists its repository with
  `connection_id:null` and the stored `web_url`, with its PRs, reviews and findings unchanged. Given a secrets
  write failure on DELETE, then 500 and the connection and token both remain.
  Traces: FR7, FR8, FR16, EC5 · Verify: integration
- AC6 [api] — Given a stub GitLab with user `ann`, token scopes `[api, read_repository]`, `expires_at` today + 30 days
  and version `17.5.1-ee`, when test-connection `{provider:"gitlab", connection_id}` runs, then `ok:true`, message
  "Connected as @ann", `login:"ann"`, `scopes`, `expires_at`, `instance_version`, `token_access:"full"`,
  `warnings:[]`, `latency_ms ≥ 0`; the stub saw exactly GET `/api/v4/user`, `/personal_access_tokens/self`,
  `/metadata` with `PRIVATE-TOKEN` and nothing else. Without `connection_id` → 422 at `["connection_id"]`; an unknown id
  → 404 and nothing saved; with `key`, the key is saved before the test and kept after a 401.
  Traces: FR9, FR10, FR11 · Verify: integration
- AC7 [api] — Given stub variants, then: scopes `[read_api, read_repository]` → `read_only` + `read_only_token`;
  `[api]` → `full`, no warning; `[read_api]` → `read_only_token` then `no_repository_scope`; `[read_user]` →
  `ok:false`; self endpoint 400 → `scopes:null`, `scopes_unknown`; `expires_at` null / today + 14 / today + 15 → none
  / `token_expiring` / none; version `16.11.3-ee` → `version_unsupported`; `17.0.0` → none; `/metadata` 403 →
  `version_unknown`; warnings in the FR11 order.
  Traces: FR11, EC7, EC8, EC9 · Verify: unit
- AC8 [api] — Given `/user` answering 302 to another host, an HTML 200, 401, and a delay past 15 s, then each test
  answers 200 `ok:false` with the FR10 message, the other host received nothing, and neither the response nor the
  logs contain the token; each test logged one INFO line with provider, host, ok and latency.
  Traces: FR10, EC6, NFR1, NFR2, NFR9 · Verify: integration
- AC9 [api] — Given a gitlab.com connection and a stub project `acme/platform/payments` (id 42, default branch
  `develop`), when POST `/repos` `{url:"https://gitlab.com/acme/platform/payments"}`, then 201 with `provider:"gitlab"`,
  `host:"gitlab.com"`, `owner:"acme/platform"`, `name:"payments"`, `full_name:"acme/platform/payments"`,
  `default_branch:"develop"`, `web_url:"https://gitlab.com/acme/platform/payments"`, `connection_id` of the connection,
  and a clone job; the stub saw one GET `/api/v4/projects/acme%2Fplatform%2Fpayments`. Posting
  `git@gitlab.com:Acme/Platform/Payments.git` (resolving to id 42) → 200 with the same repository and no job.
  Traces: FR13, FR14, FR15, FR16, EC13 · Verify: integration
- AC10 [api] — Given the EC11 and EC12 inputs, then each answers the stated 422 code and message, and no repository or
  job is created; `details.host` is `gitlab.com` for `repo_host_not_connected`. Given the Phase 1 AC53 GitHub cases,
  then they still answer 422 `validation_error`, with the FR12 message.
  Traces: FR12, FR13, EC11, EC12, NFR7 · Verify: component
- AC11 [api] — Given the project stub answering 404, 301 (to `/api/v4/projects/42`), 401, 429 and 500 in turn, then
  POST `/repos` answers 422 `gitlab_project_not_found`, 422 `gitlab_project_not_found` with the "has moved" message,
  422 `gitlab_permission_denied`, 502
  `gitlab_rate_limited` and 502 `gitlab_unavailable`, nothing is stored, and no redirect target is contacted. Given a
  connection without a token and a public project, then the add succeeds and the stub request had no token header.
  Traces: FR14, EC15 · Verify: integration
- AC12 [db] — Given a migrated database, when a GitHub `acme/web` and a GitLab `acme/web` are added to one workspace,
  then both exist with their own rows and PRs; existing rows have provider `github` and host `github.com`. Given a
  GitLab repository, the down migration fails with a message naming GitLab data and changes nothing; with GitHub data
  only, down then up round-trips.
  Traces: FR30, EC14, EC29 · Verify: integration
- AC13 [git] — Given a local HTTPS git stub for a GitLab project (id 42, path `acme/platform/payments`) behind a TLS
  certificate signed by the CA in `DEVDIGEST_EXTRA_CA_FILE`, when the clone job runs, then the clone lands at the
  host-and-id location, the stub saw the Basic `oauth2` header only for URLs under `<base_url>/`, argv, the remote URL
  and the clone's git config contain no token, and the index job finds the clone. A GitHub clone of `gitlab/gitlab.com`
  lands at `<cloneRoot>/gitlab/gitlab.com` with no overlap. A git stub answering a redirect → job `failed`, redirect
  target not contacted. Without a token → no auth header.
  Traces: FR18, FR19, FR20, EC21, EC22, NFR2 · Verify: integration
- AC14 [git] — Given a clone job payload or stored values crafted with `..`, a symlinked directory or a path from the
  GitLab project name, when clone or refresh runs, then nothing is created or removed outside the two accepted
  location forms.
  Traces: FR18, NFR10 · Verify: integration
- AC15 [sync] — Given a stub GitLab with MRs in states opened (one draft, one from a fork, one without `sha`), merged,
  closed and locked, when `GET /repos/:id/pulls` runs, then the stored rows follow the FR22 table (iid as number,
  username as author, `locked` → `closed`), the `sha`-less MR is absent, a second sync with changed values updates
  title, head_sha, status, updated_at, merged_at, closed_at and draft, and the stub saw one list request with
  `state=all&order_by=updated_at&sort=desc&per_page=50` after one `GET /projects/<id>`, plus at most one GraphQL
  request.
  Traces: FR21, FR22, EC17, NFR4 · Verify: integration
- AC16 [sync] — Given a connection without a token, then `GET /repos/:id/pulls` serves stored rows with 200 and
  `POST /repos/:id/poll` → 500 `config_error` "GitLab connection <name> has no token"; given the stub answering 500,
  then the poll → 502 `external_service` "GitLab request failed" after retries.
  Traces: FR23, EC15 · Verify: integration
- AC17 [sync] — Given a stub MR with 3 diff pages (a too-large file, a collapsed file, a deleted file, a renamed file,
  two entries of one path), 2 commit pages and a description "Fixes #7" with issue 7, when `GET /pulls/:id` runs,
  then files follow FR24 (paths, counted lines, missing patches, merged entries), commits and `linked_issue`
  `{number:7, state:"open"}` are returned, the stored files and commits are replaced in one transaction, and
  `additions`/`deletions`/`files_count` equal the GraphQL summary; with an all-zero summary they equal the file sums. With the stub down, the stored detail is served.
  Traces: FR24, FR25, EC18 · Verify: integration
- AC18 [sync] — Given 25 GitLab MRs without stats and a stub that answers GraphQL, when one background cycle runs,
  then all PRs selected by PR Leaderboard FR24 get stats from one GraphQL request, the repository used ≤ 13 GitLab
  requests, and every GraphQL body is a query without `mutation`. With GraphQL answering an error, the stats stay
  unset, one log line is written, no diff page is requested for stats, and the next cycle retries.
  Traces: FR26, EC19, NFR3, NFR4 · Verify: integration
- AC19 [sync] — Given repositories on GitHub, gitlab.com and `git.corp.example` with a fake clock, when
  `git.corp.example` answers 429 `Retry-After: 120`, then its repositories are not polled for 120 s while the others
  are polled in the same cycle; with the gitlab.com connection's token removed, its repositories are skipped with one
  WARN per cycle while GitHub repositories are polled; with no GitHub token only GitHub repositories are skipped; at
  most one code-host request is in flight at any time; shutdown completes within 5 s.
  Traces: FR27, EC16, NFR4 · Verify: integration
- AC20 [sync] — Given a token rotated through PUT while a cycle runs, then requests already sent complete and the next
  request of that repository carries the new token. Given a project renamed on the stub (same id, new path), then
  the next sync addresses it by id and still upserts its MRs (path update: AC40).
  Traces: FR6, FR17, EC10 · Verify: integration
- AC21 [api] — Given automatic reviews armed in a workspace with a GitLab repository whose MR (created after
  `enabled_at`, open, not draft) appears in a poll cycle, then a review starts for it with source `auto`. Given no
  token for any repository's host, then the status `state` is `no_code_host_token`; given a GitHub-only workspace
  without a token, then it is `no_github_token`.
  Traces: FR28 · Verify: integration
- AC22 [api] — Given a GitLab PR, then `GET /pulls/:id/comments` → 200 `[]`, `POST /pulls/:id/comments` → 400
  `gitlab_unavailable`, a brief has no linked issue, blast radius has no prior PRs and intent tickets are unresolved,
  and the GitHub and GitLab stubs received no request for it; CI export cases of EC25 answer as stated.
  Traces: FR29, EC24, EC25, NFR3 · Verify: integration
- AC23 [api] — Given a connection token `glpat-AbCdEfGhIjKlMnOpQrSt` and an onboarding README containing a different
  `glpat-…` and a `gldt-…` value, then the README excerpt sent to the fake LLM has both replaced, and after forcing
  errors in test, add-repo, sync, clone and an LLM call, no response, log line, git argv or stored error contains the
  stored token.
  Traces: FR31, NFR1 · Verify: integration
- AC24 [api] — Given only GitHub repositories and no connection, then every existing golden and contract test passes
  with only the added `Repo`/`LeaderboardRepo` fields; GitHub stub requests, clone locations and git headers are
  unchanged; `GET /settings/secrets-status` has exactly four keys. New routes refuse a foreign Origin (403), a
  non-JSON body (415) and a foreign Host. New shapes have goldens checked by the UI contract test.
  Traces: NFR6, NFR7, FR16, FR46 · Verify: component
- AC25 [api] — Given 20 connections and 500 repositories in a throwaway stack, when 200 GET
  `/settings/gitlab-connections` requests run, then p95 ≤ 100 ms.
  Traces: NFR8 · Verify: manual
- AC26 [api] — Given GitLab calls during a test, add, sync and poll, then each call logged one line with host,
  operation, status, attempt and latency, no line contains a token, MR title, description or diff, and stored MR
  titles with HTML render as text in the PR list.
  Traces: NFR9, NFR10, NFR5 · Verify: integration
- AC27 [ui] — Given `/settings/gitlab`, then the sub-nav shows GitLab Integration after GitHub Integration; given 7
  connections with tokens (mocked), then rows show name, base URL, badge and repo count, exactly 5 test requests are
  sent one after another, and a result `{login:"ann", scopes:["read_api","read_repository"], expires_at, warnings:[read_only_token, token_expiring]}`
  shows "Connected as @ann", the scopes, "Expires <date>", and two warning banners. No connection → "No GitLab
  connections yet".
  Traces: FR32, FR33, FR34, EC28 · Verify: component
- AC28 [ui] — Given the add dialog, then the base URL defaults to `https://gitlab.com`, the token masks per Settings A,
  "Create a token" opens the instance token page in a new tab, Save sends POST, "Save and test" sends POST then a
  test; a 422 at `["base_url"]` (for example `https://gitlab.com/api/v4`) shows at the field; a 409 exists shows at
  the base URL; on edit "Remove token" sends `token:null`; the "Create a token" link is
  `<base_url>/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository`.
  Traces: FR35, EC27, NFR11 · Verify: component
- AC29 [ui] — Given a GitHub and a GitLab repository, then GitHub Integration lists only the GitHub one and GitLab
  Integration lists the GitLab one with its host and the GitLab remove copy.
  Traces: FR37 · Verify: component
- AC30 [ui] — Given `/setup?step=github`, then the URL becomes `/setup?step=code-host` and the stepper reads "Code
  host"; choosing GitLab and "Save and test" sends POST `/settings/gitlab-connections` then a test, and a successful
  result enables Continue; an existing GitLab connection with a token shows "Already connected". Given secrets-status
  without GitHub and one GitLab connection with a token, then `/` does not stop at the code-host step; given the
  connections request failing, then `/` shows the error with Retry and does not redirect.
  Traces: FR38, FR39 · Verify: component
- AC31 [ui] — Given the add form, then the copy names GitHub and GitLab; a mocked 422 `repo_host_not_connected` shows
  the message and the link to `/settings/gitlab` once, with no toast.
  Traces: FR40, EC23 · Verify: component
- AC32 [ui] — Given a GitLab repository `acme/platform/payments` with `web_url` `https://git.corp.example/gitlab/acme/platform/payments`,
  then the PR detail header shows `!12` and "View on GitLab" linking to `<web_url>/-/merge_requests/12`; a finding on
  lines 10–20 links to `<web_url>/-/blob/<sha>/src/a%20b.ts#L10-20`; the onboarding tour says "Open <path> on
  GitLab"; the PR list row shows `!12`; a GitHub repository renders exactly as today.
  Traces: FR41, FR42, FR43 · Verify: component
- AC33 [ui] — Given a leaderboard response with a GitLab `LeaderboardRepo` and rows, awards, needs-attention items and
  hot files of it, then each shows `!<n>` and the repository with its host; the repository switcher and the
  leaderboard filter show "acme/platform/payments · gitlab.com".
  Traces: FR43, FR44, FR46 · Verify: component
- AC34 [ui] — Given a GitLab PR, then findings show no "Post to PR" action, the diff has no inline composer, and the
  comments area shows "Comments from GitLab are not shown yet."
  Traces: FR45 · Verify: component
- AC35 [ui] — Given the new components rendered with pseudo messages, then no hard-coded copy is found; badges,
  warnings and host labels have text equivalents; dialog errors are linked with `aria-describedby`.
  Traces: NFR11, NFR12 · Verify: component
- AC36 [ui] — Given a fresh stack, a real gitlab.com token (and, when available, a self-hosted instance with a
  corporate CA mounted), when a user completes setup with GitLab, adds a nested-group project, opens an MR and runs a
  review, then the private project clones with the Basic `oauth2` header, the test shows the real scopes and expiry
  for a personal and a project access token, the PR list, detail, leaderboard and links work without console
  errors, and the GitLab audit log of the token shows only read requests.
  Traces: FR38, FR14, FR21, FR24, FR41, NFR3 · Verify: manual
- AC37 [api] — Given a GitLab repository with stored PRs, a stored detail, a review and findings, and its connection
  deleted, then `GET /repos/:id/pulls` → 200 with the stored rows, `GET /pulls/:id` → the stored detail without
  `linked_issue`, the review and findings read as before, `POST /repos/:id/poll` → 422 `repo_host_not_connected`
  with `details` {`host`, `repo_id`}, POST `/repos` with its URL → 422 `repo_host_not_connected`, an enqueued refresh
  job ends `failed` with the message, ran no git command and was not retried, and no GitLab or GitHub stub received
  a request. Given a list sync waiting on a delayed stub when the connection is deleted, then that sync completes
  and stores its rows, and the next sync sends nothing.
  Traces: FR48, EC30, EC31 · Verify: integration
- AC38 [sync] — Given not-connected repositories on `git.corp.example`, connected repositories on gitlab.com and
  GitHub repositories with a GitHub token, when one background cycle runs, then the not-connected ones receive no
  request, exactly one WARN names `git.corp.example`, no pause is recorded for any host, and the gitlab.com and
  GitHub repositories are polled in the same cycle.
  Traces: FR27, FR48, EC32, NFR4 · Verify: integration
- AC39 [api] — Given not-connected gitlab.com repositories with a clone, when POST `/settings/gitlab-connections`
  creates a connection for `https://gitlab.com` with a new token, then GET `/repos` shows its id as their
  `connection_id` with no database change to the repositories, the next sync's stub requests carry the new token,
  the clone stays at its location and no clone job is enqueued; a connection for `https://gitlab.com:8443` instead
  leaves them not connected.
  Traces: FR48, EC33 · Verify: integration
- AC40 [sync] — Given a stored GitLab repository (id 42, `acme/platform/payments`) and the stub renaming it to
  `acme/core/payments`, when each of POST `/repos` (by the new URL), `GET /repos/:id/pulls`, `POST /repos/:id/poll`
  and a background cycle runs, then afterwards `full_name` is `acme/core/payments`, `owner` `acme/core`, `name`
  `payments`, `web_url` `<base_url>/acme/core/payments`, MRs are upserted by id, the clone directory is unchanged,
  `default_branch` is unchanged, and the next refresh job's remote URL is `<base_url>/acme/core/payments.git`. A
  `web_url` in the stub's project body pointing to another host is not used.
  Traces: FR47, FR15, EC20 · Verify: integration
- AC41 [api] — Given the EC34 repositories X and Y, when X syncs, then X keeps `acme/old`, one WARN names both ids and
  X's MRs are upserted; after Y syncs to its new path, X's next sync stores `acme/new`. Given the EC35 add, then 409
  `gitlab_path_conflict` with `details.repo_id` of Y, nothing stored and no job; after Y's sync, the same add → 201.
  Traces: FR15, FR47, EC34, EC35 · Verify: integration
- AC42 [ui] — Given a connection with `repo_count:3`, when Delete is clicked, then the dialog names the connection and
  says "3 repositories use <host> and become not connected"; with `repo_count:0` it says only that the token is
  removed; confirming sends DELETE and removes the row; a mocked 500 shows the error banner and keeps the row. Given
  a GitLab repository with `connection_id:null`, then its PR list and PR detail show "Not connected — add a GitLab
  connection for <host>" with a link to `/settings/gitlab`, the refresh action is disabled, stored PRs are listed,
  and GitLab Integration shows its row with the "Not connected" badge.
  Traces: FR36, FR37, FR49, EC36, NFR11 · Verify: component

## 14. Traceability
| Source | Area | FR | EC / NFR | AC |
|---|---|---|---|---|
| Request; first-run Q1 (several instances, global, one per host) | api, db | FR1–FR8 | EC1–EC5, EC26, NFR6, NFR8 | AC1–AC5, AC24, AC25 |
| First-run Q3 tokens, scopes, expiry; Q8 version | api | FR9–FR11 | EC6–EC9, NFR1, NFR2, NFR9 | AC6–AC8 |
| First-run Q2 mixed repos, host binding, uniqueness | api, db | FR12–FR17, FR30 | EC11–EC15, EC20, EC29, NFR7 | AC9–AC12, AC20 |
| First-run Q9 TLS, CA, redirects, token scoping | git, api | FR18–FR20 | EC21, EC22, NFR2, NFR10 | AC13, AC14 |
| Request: PR sync parity | sync | FR21–FR26 | EC15–EC19, NFR4, NFR5 | AC15–AC18, AC26 |
| Leaderboard / auto reviews parity | sync, api | FR26–FR28, FR46 | EC16, NFR4 | AC18, AC19, AC21, AC33 |
| Slicing A → B ∥ C (isolation) | api, ui | FR29, FR45 | EC24, EC25, NFR3 | AC22, AC34 |
| Research fact 6 (redaction, quick actions) | api | FR31 | NFR1, NFR3 | AC23 |
| Settings A deltas | ui | FR32–FR40 | EC23, EC27, EC28, NFR11, NFR12 | AC27–AC31, AC35 |
| First-run Q4 copy and references | ui | FR41–FR44 | NFR10 | AC32, AC33 |
| Rollout | all | FR14, FR21, FR24, FR38, FR41 | NFR3 | AC36 |
| D25/D26 delete with repositories, not connected, re-bind by host key | api, sync, ui | FR7, FR16, FR27, FR36, FR37, FR48, FR49 | EC5, EC30–EC33, EC36, NFR4 | AC5, AC37–AC39, AC42 |
| D27 renamed or moved projects follow the project id | sync, api | FR15, FR17, FR47 | EC20, EC34, EC35 | AC20, AC40, AC41 |

## 15. Design review
| # | Gap / proposal | Source | Area | Decision |
|---|---|---|---|---|
| G1 | Delete of a connection still used by repositories | Q1 | api | accepted: delete allowed, repositories become not connected and re-bind by host key (D25, D26) |
| G2 | Env bootstrap of a GitLab connection | report assumption | api | rejected: no env bootstrap (D14, D33) |
| G3 | Host change on edit would orphan repositories | analysis | api | host key immutable (D15) |
| G4 | `SecretsStatus` strict golden vs a `gitlab` flag | contract test | api, ui | unchanged; gate loads the connection list (D18) |
| G5 | Diff stats without a per-MR request | research, leaderboard NFR3 | sync | accepted: GraphQL batch of 20, no diff-pages fallback (D19, D29) |
| G6 | Renamed or moved projects | analysis | api | accepted: sync by id; stored path and web URL follow the project, clone stays (D27) |
| G7 | Add-repo with a non-GitHub host and no connection | Q1 | api, ui | 422 `repo_host_not_connected` + link (D22) |
| G8 | `acme/web` on two hosts looks identical in lists | Q2 | ui | accepted: host label always for GitLab repos (FR44, D31) |
| G9 | Posting UI would fail for GitLab PRs | slicing | ui | hidden in A (D21) |
| G10 | Auto-test of up to 20 connections would exhaust the 20/min bucket | Settings A NFR3 | ui | accepted: first 5 only (D28) |
| G11 | Clone directory collision between a GitHub owner `gitlab` and GitLab clones | analysis | git | reserved top-level name (FR18) |
| G12 | git follows redirects and could replay the auth header | Q9 | git | redirects off for GitLab remotes (FR19) |
| G13 | A token pasted as an API URL base | UX | ui | field error (EC27) |
| G14 | Expired-soon tokens go silent | Q3 | ui | 14-day warning on auto-test (FR11, FR33) |
| G15 | A synced rename can collide with another repository's stale stored path | analysis of D27 | sync, api | accepted: path update skipped with WARN, retried next sync; add answers 409 `gitlab_path_conflict` (FR15, FR47) |
| G16 | Repositories of a deleted connection would fail without a visible cause | analysis of D25 | ui | accepted: "Not connected" banner and badge with a link to GitLab Integration; delete dialog names the count (FR36, FR49) |

## 16. Decisions
- D1 — Hosts: gitlab.com and self-hosted instances (user, 2026-10-24).
- D2 — Scope: full parity including CI, split into slices A → B ∥ C; this spec is slice A (user, 2026-10-24).
- D3 — Order: all specs now; planning only after Settings slice A passes its review gate (user, 2026-10-24).
- D4 — Q1: several GitLab instances can be connected at once; connections are installation-wide (like the secrets
  store), each with base URL (relative URL root allowed), token and display name; one connection per host; a
  repository binds to the connection by the host of its URL (user, 2026-10-24).
- D5 — Q2: GitHub and GitLab repositories side by side in one workspace, bound to their host, unique on host + path;
  PR list, leaderboard and automatic reviews mix them (user, 2026-10-24).
- D6 — Q3: personal, project and group access tokens only, no OAuth; required scopes `api` + `read_repository`; a
  `read_api` + `read_repository` token is accepted with a warning; the test shows username, scopes, `expires_at` and
  version and warns within 14 days of expiry (user, 2026-10-24).
- D7 — Q4: "PR" stays in generic chrome; host-specific copy where an action or link touches the host ("View on
  GitLab", "Post to MR" in B, `!123`) (user, 2026-10-24).
- D8 — Q8: minimum self-hosted version 17.0; older → warning, not a block (user, 2026-10-24).
- D9 — Q9: base URL https only; corporate CA via one deployment-level PEM file for API and git; never follow
  redirects; token only to the configured scheme/host/port/path prefix; private IPs allowed; no skip-verify (user,
  2026-10-24).
- D10 — Q10: reconcile through deltas named in this spec (§12); other specs are not edited; build on Settings A's
  final contracts (user, 2026-10-24).
- D11 — CI answers for slice C, recorded as context only: summary note + inline threads via draft notes and bulk
  publish, never approve or request changes, token from masked `DEVDIGEST_GITLAB_TOKEN`; config at
  `.gitlab/devdigest/review-<slug>.yml` with a root include, an existing `.gitlab-ci.yml` never modified (snippet
  shown); runner = committed blob on a digest-pinned public image, sha256-verified, Linux amd64 (user, 2026-10-24).
- D12 — The first run's non-blocking assumptions are accepted unless they contradict D4–D9; the single
  `GITLAB_TOKEN`/`GITLAB_BASE_URL` storage is replaced by the connection model (user, 2026-10-24).
- ~~D13 — Deleting a connection with bound repositories → 409 `gitlab_connection_in_use` (spec author default, see
  Q1).~~ Superseded by D25.
- D14 — No environment bootstrap for connections: several connections have no natural single-variable form, and an
  env-defined connection could be neither edited nor deleted in the UI (two sources of truth) (spec author default,
  see Q2).
- D15 — A connection's host key is immutable; its path prefix and name can change (spec author, 2026-10-24).
- D16 — Connection ids are server-generated UUIDs; the host key is the uniqueness key (spec author, 2026-10-24).
- D17 — The GitLab test uses the shared test-connection route with `connection_id` and returns machine-readable
  `warnings[]` (spec author, 2026-10-24).
- D18 — `SecretsStatus` stays at four keys; the home gate and setup read GET `/settings/gitlab-connections`, as custom
  providers do (spec author, 2026-10-24).
- D19 — GitLab diff stats come from batched read-only GraphQL queries (spec author default, see Q5).
- D20 — MR state `locked` maps to `closed` (first-run assumption, accepted with D12).
- D21 — Posting is hidden for GitLab PRs in this slice; the API answers 400 `gitlab_unavailable` (spec author,
  2026-10-24).
- D22 — A well-formed URL on a host without a connection answers 422 `repo_host_not_connected` instead of
  `validation_error` (spec author, 2026-10-24).
- D23 — A GitLab clone location is keyed by host and project id, not by path (spec author, 2026-10-24).
- D24 — `api` alone counts as full access and gets no repository-scope warning, because GitLab documents that `api`
  also grants Git over HTTP; the dialog and setup still ask for `api` + `read_repository` (D6). The version is read
  from `GET /metadata` (`/version` is its deprecated alias) (spec author, 2026-10-24, from the GitLab docs research).
- D25 — Q1: deleting a GitLab connection that repositories still use is allowed (no 409); its token is removed; the
  bound repositories become "not connected": listed with their local data readable, every GitLab call for them
  skipped or answered with 422 `repo_host_not_connected`, skipped by the poller without pausing the host, shown with a
  "Not connected" state linking to GitLab Integration; the delete dialog says how many repositories become not
  connected (FR7, FR36, FR48, FR49) (user, 2026-10-24).
- D26 — Q1 follow-up: repositories bind by host key resolved at every call; no connection id is stored on a
  repository. Reason: re-creating a connection for the same host key re-binds its repositories with no data
  migration and no repair step; a delete leaves no dangling reference to clear; it matches the immutable host key
  (D15); the lookup is over at most 20 connections. `Repo.connection_id` stays as a derived field (null = not
  connected) (user asked to prefer this, 2026-10-24; justification by the spec author).
- D27 — Q3: sync follows the project by numeric id; every successful project fetch (add, list sync, manual poll,
  background poll) updates the stored `full_name`, `owner`, `name` and web URL to the project's current path; the
  clone location (host + project id) does not move. The stored web URL is built as `<base_url>/<full_name>` from the
  current path rather than copied from GitLab's `web_url` field, to keep FR41's rule that links never come from a
  server-echoed URL (user, 2026-10-24; web URL construction by the spec author). A path collision with another
  stored repository skips the update and retries (FR47); an add whose path is held by another project → 409
  `gitlab_path_conflict` (FR15) (spec author, 2026-10-24, as asked by the user).
- D28 — Q4: the first 5 connections with a saved token are auto-tested when GitLab Integration opens (user,
  2026-10-24).
- D29 — Q5: no diff-pages fallback when GraphQL is unavailable; stats stay unknown and size metrics skip the MR, as
  for unfilled GitHub stats; batch size 20; the planner measures the query's complexity on a 17.0 instance (user,
  2026-10-24).
- D30 — Q6: "Create a token" links to
  `<base_url>/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository` (user, 2026-10-24).
- D31 — Q7: GitLab repositories always show their host label (user, 2026-10-24).
- D32 — Q8: git auth is a per-command `extraHeader` with Basic `oauth2:<token>`; AC13 checks it against a stub and
  AC36 against gitlab.com (user, 2026-10-24).
- D33 — Q2: no environment variable defines a GitLab connection (D14 confirmed) (user, 2026-10-24).
- D34 — Q9: when `GET /personal_access_tokens/self` fails (for example 400 for a project or group token), `scopes`
  is null with the warning `scopes_unknown` and the test still passes; no other introspection endpoint is called
  (user, 2026-10-24).
- D35 — The user approved this spec with D25–D34 and it is ready for planning; planning still starts only after
  Settings A passes its review gate (D3) (user, 2026-10-24).

## 17. Open questions
None open. Q1–Q9 of the draft were answered by the user on 2026-10-24 and are recorded as D25–D34 (Q1 → D25, D26;
Q2 → D33; Q3 → D27; Q4 → D28; Q5 → D29; Q6 → D30; Q7 → D31; Q8 → D32; Q9 → D34).

## Glossary
- Connection — an installation-wide GitLab instance definition (base URL, name, token) (FR1).
- Host key — lower-cased host name plus a non-443 port; binds repositories to connections (FR2, FR13).
- Base URL / path prefix — the instance URL, including a relative URL root such as `/gitlab` (FR2).
- MR iid — the per-project merge request number; stored and shown as the PR number (FR22).
- Not connected — the state of a GitLab repository whose host key matches no connection: data readable, no GitLab
  request, `connection_id` null (FR48).
- Token access — `full` (the token has `api`) or `read_only` (`read_api` without `api`); the UI recommends
  `api` + `read_repository` and `read_api` + `read_repository` respectively (FR11).
