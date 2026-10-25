# GitLab B — merge request comments and context peers
Spec: GitLab slice B (MR diff comments read and posted, GitLab-safe note bodies, GitLab context peers) · Status: ready-for-planning · Date: 2026-10-25
Date note: the file name keeps 2026-10-24, the day the GitLab slicing (D1) started, so the slice A/B/C files sort
together; the spec itself was written and approved on 2026-10-25. Planning waits for the Settings A review gate (D30).
Areas: api, ctx, db, ui
Design sources: the user's request of 2026-10-24 ("GitLab support with all of today's GitHub PR features") and the
user's decisions of 2026-10-24 (several GitLab instances, mixed workspaces, token types and scopes, copy, https and
token scoping, minimum version, reconciliation by deltas) plus the two slice A revision answers (deleting a used
connection leaves repositories "not connected"; sync follows the project id). The first spec-creator run's research
and slicing report (facts 1, 5, 6; research facts on discussions, positions, draft notes and limits), the slice A
spec and its hand-off notes. Code research at `feat/go-backend-phase-1` @ `4ce720b` (Settings A worktree
`../dev-digest-go-settings-a` @ `c9c4689`). GitLab documentation (docs.gitlab.com) and, where the docs are silent,
GitLab source at tag `v17.0.0` and `master` (research reports of 2026-10-25). No design frames or screenshots.
Related: `specs/2026-10-24-gitlab-a-connection-sync.md` (GitLab A — foundation; FR29 and FR45 there are superseded in
part, see §12), `specs/2026-10-21-go-backend-phase-1.md` (FR41, FR42 extended), `specs/2026-10-22-go-backend-phase-5.md`
(intent, smart diff, prior PRs, brief extended), `specs/2026-10-22-go-backend-phase-2.md` (skills URL import extended),
`specs/2026-10-23-settings-first-run-setup.md` (Settings A FR18 kept for GitHub). Sibling: GitLab slice C
`specs/<date>-gitlab-c-ci.md` (GitLab CI), independent of this spec. Supersedes nothing else.

ID convention: plain FR1…, EC1…, NFR1…, AC1…, US1…, D1…, Q1…. Slice A items are cited with an "A" prefix (for example "A FR29"). Area tags:
`[api]` = Go routes for PR comments, `[ctx]` = context peers (prior MRs, brief issue, intent tickets and docs, skills
URL import, smart-diff roles), `[db]` = schema and migrations, `[ui]` = Next.js app.

## 1. Goal
After slice A, GitLab merge requests sync and can be reviewed locally, but the result cannot go back to GitLab: the
comments area is empty, "Post to PR" is hidden, and the context peers that read GitHub (prior PRs, the brief's linked
issue, intent tickets and docs, skill import from a URL) answer nothing for GitLab repositories. This slice gives a
reviewer the same loop on GitLab as on GitHub: see the MR's diff threads in the diff, post a finding as a positioned
discussion ("Post to MR"), write a comment or a reply, and get GitLab issues, files and prior merge requests as review
context. Because GitLab runs quick actions (`/approve`, `/merge`, `/close`) written in note bodies as the token's
user, and finding text is LLM output an MR author can steer, every note body is neutralised on the server before it
reaches GitLab.

## 2. Context
- Comments today (GitHub only, never stored): `GET /pulls/:id/comments` proxies one page (≤ 100) of GitHub review
  comments and answers 200 `[]` without a token or on any failure (`pulls/app/service.go:238-249`,
  `platform/github/comments.go:67-83`; Phase 1 FR41). `PrReviewComment` = `{id:int, path, line:int|null,
  original_line:int|null, side:"LEFT"|"RIGHT", body, user, created_at, html_url, in_reply_to_id:int|null,
  is_outdated:bool}`, `is_outdated = line == null`, `side` `LEFT` only when GitHub says so (`comments.go:50-65`,
  `contracts/platform.ts:203-218`).
- `POST /pulls/:id/comments` with `PrCommentInput {path, line>0, side?, body, in_reply_to?:int}` creates a comment on
  the stored `head_sha` (`side` default `RIGHT`) or replies to `in_reply_to`; it is never retried; 200 with the created
  comment; no token → 400 `github_unavailable` "Connect a GitHub token to post comments."; any GitHub failure → 400
  `github_comment_failed` with GitHub's message and `details.cause` (`service.go:264-293`, `comments.go:91-121`,
  `github_test.go:758`; Phase 1 FR42, §7.4, AC88–AC91). `path`, `line` and `body` are required for a reply too
  (`platform.ts:220-228`). The body is forwarded verbatim; nothing is marked as posted on the server.
- UI posting: "Post to PR" exists on a finding whose `start_line` is a rendered add or context line, for an open PR
  (`DiffTab.tsx:84`, `PrDetailView.tsx:117`, `diff-viewer/findings.ts:42-65`); it posts `{path: file, line:
  start_line, side:"RIGHT"}` (`InlineFindingCard/helpers.ts:79-81`). The body is `**[<severity> · <category>]
  <title>**`, the rationale, an optional `**Suggested fix**` block, `<sub>DevDigest · <n>% confidence</sub>` and the
  marker `<!-- devdigest-finding:<id> -->`, the LLM fields escaped by `sanitizeForGitHub` (`<` → `&lt;`, `>` → `&gt;`,
  `@` + U+200B outside backtick code; code verbatim) (`helpers.ts:8-76`; `frontend/INSIGHTS.md:37`). "Posted on
  GitHub" is derived by finding the marker in the listed comments (`helpers.ts:13-19`); `visibleBody()` hides HTML
  comments before rendering (`diff-viewer/comments.ts:109-125`; `frontend/INSIGHTS.md:74`). The free-text composer
  and the reply composer send trimmed raw text (`InlineComposer.tsx:11-83`). A composer comment targets `{newNo,
  RIGHT}` on add or context lines and `{oldNo, LEFT}` on deleted lines (`comments.ts:77-82`). Copy is GitHub-worded
  (`prReview.json:15-17,239-241`, `shell.json` `diffViewer`).
- Stored diff: `pr_files.patch` holds the hunk text (`0001_baseline.up.sql:505-512`); A FR24 stores GitLab files with
  path = `new_path` (`old_path` for a deleted file) and no previous path for a renamed file. The TS `parsePatch`
  maps every diff line to `oldNo`/`newNo` (`diff-viewer/helpers.ts:12-38`); the Go diff parser tracks new-side lines
  only (`reviewer/domain/diff.go:60-125`).
- Slice A for GitLab PRs: comments `[]`, posting 400 `gitlab_unavailable`, prior PRs empty, brief issue omitted,
  intent tickets unresolved, intent docs from the clone only (A FR29); no posting UI and the text "Comments from
  GitLab are not shown yet." (A FR45); read-only toward GitLab (A NFR3, with the forward reference to quick actions).
  A not-connected repository (no connection for its host) sends no GitLab request and slices B and C answer
  `repo_host_not_connected` for it (A FR48). Every GitLab request addresses the project by numeric id (A FR17); links
  are built from `Repo.web_url` (A FR41); the token goes only to its connection's origin and path prefix, no redirects
  (A NFR2); the connection test reports `token_access` (`full` | `read_only`) and `login` (A FR10, FR11).
- Context peers today (one shared GitHub client; research report 2026-10-25):
  - prior PRs: `GET /repos/{o}/{n}/pulls?state=closed&sort=updated&direction=desc&per_page=30`, merged ones only,
    then one files page (100) per PR, ≤ 5 in flight, ≤ 31 requests; selection keeps overlapping PRs, skips the
    current one, sorts by merge time and keeps 10; any failure → `history: []` (`platform/github/merged.go:11-130`,
    `blast/domain/history.go:18-57`, `blast/app/service.go:84-103`; Phase 5 FR5033, §7.8, AC5061);
  - brief linked issue: first `#(\d+)` in the body, `GET /issues/{n}` within 10 s, failure → `missing_inputs`
    `{linked_issue, fetch_failed}` (`brief/domain/input.go:204-221,381-421`, `brief/app/facts.go:31-87`; Phase 5
    FR5034, EC5022);
  - intent tickets: GitHub issue URLs, `owner/name#N`, bare `#N`, closing keywords first, same repository only, ≤ 3,
    text ≤ 3,000 UTF-16 units, failures are `failed` sources with a reason (`intent/domain/links.go:27-189`,
    `intent/adapters/peers/tickets.go:50-78`; Phase 5 FR5014, FR5016, EC5003, AC5019, AC5024);
  - intent docs: links, same-repo GitHub `blob`/`tree` URLs (ref = one segment) and prose paths, ≤ 5; read from the
    clone at the head SHA (64 KiB cap, over it → `failed`, no API call), else the contents API; a 404, directory or
    over-cap file → "not found" (`links.go:50,237-325`, `intent/adapters/peers/docs.go:54-93`,
    `platform/github/contents.go:28-60`; Phase 5 FR5016, FR5054, EC5011);
  - skills URL import: any https host on port 443 with public addresses only, ≤ 3 re-validated redirects, 10 s,
    1 MiB, no token ever; `github.com/<o>/<r>/blob/<rest>` is rewritten to `raw.githubusercontent.com/<o>/<r>/<rest>`
    (query dropped); errors are 422 `invalid_import` (`skills/domain/urlhost.go:24-355`,
    `skills/adapters/urlfetch/fetcher.go:108-250`, `skills/app/importer.go:73-103`; Phase 2 FR239, EC219–EC222,
    NFR203, AC258); copy "An https:// link to a Markdown file or a .zip archive. GitHub blob links work too."
    (`skills.json:77-86`);
  - smart-diff roles: path globs, `wiring` includes `.github/**`, `*.config.*`, `.env*`; `.gitlab-ci.yml` and
    `.gitlab/**` fall to `core` (`smartdiff/domain/classify.go:16-108`; Phase 5 FR5028, §7.6); the brief reads the
    same roles (`brief/adapters/peers/peers.go:144-150`).
- Settings A (worktree): GitHub Integration's "Comment identity" reads "Comments are posted as @<login>." after a
  successful test, else "Comments are posted as the token's owner." (`TokenSection.tsx:51-55`,
  `githubIntegration.json:14-18`; Settings A FR18).
- GitLab facts (docs.gitlab.com and GitLab source `v17.0.0`/`master`; research reports of 2026-10-25):
  - Discussions: `POST /projects/:id/merge_requests/:iid/discussions` with `body` and `position[position_type]=text`,
    `base_sha`, `start_sha`, `head_sha` (all required), `old_path` and `new_path` (both required, before and after a
    rename), `new_line` only for an added line, `old_line` only for a removed line, both for an unchanged line; the
    server computes `line_code`; a position that matches no diff line → 400 `Note {:line_code=>["can't be blank",
    …]}`, and some wrong-SHA positions → 500 (issues #37518, #296829); 201 with `{id: <40-hex string>,
    individual_note, notes[]}`. Reply: `POST …/discussions/:discussion_id/notes {body}` → 201 note; a system note
    cannot be replied to (400). Notes carry no discussion id, so a note's discussion is found by listing discussions
    (docs.gitlab.com/api/discussions; `lib/api/discussions.rb`, `lib/api/entities/note.rb`).
  - `diff_refs` of a single MR is the latest diff version, empty right after creation and filled asynchronously; the
    first entry of `GET …/merge_requests/:iid/versions` (newest first) gives the same three SHAs
    (docs.gitlab.com/api/merge_requests; `app/models/merge_request_diff.rb`). A note whose position SHAs differ from
    the MR's current `diff_refs` is inactive (outdated); GitLab moves positions forward on push when the line is still
    traceable (`diff_positionable_note.rb`, `discussions/update_diff_position_service.rb`). There is no `outdated`,
    `original_position` or `web_url` field on a note; GitLab's own links use `<MR web URL>#note_<id>`.
  - Listing: `GET …/discussions` pages count discussions, `per_page` ≤ 100; system notes are included
    (`system: true`); note `type` is `DiffNote`, `DiscussionNote` or null; `position.position_type` is `text`, `image`
    or `file`.
  - Limits: a note ≤ 1,000,000 characters (`app/models/note.rb`); ≤ 5,000 comments per MR
    (administration/instance_limits); note creation on gitlab.com 60 per minute per user, self-managed default 300
    (`notes_create_limit`), 429 with `Retry-After` on current versions and without it on 17.0
    (`lib/api/helpers/rate_limiter.rb`). A `read_api` token gets 403 `insufficient_scope` on any POST
    (`lib/api/api.rb`, `api_guard.rb`). Posting needs a role that may comment (Reporter works on every version); a
    locked discussion refuses non-members with 403; merged or closed MRs accept notes.
  - Quick actions: a line runs a command only when it starts at column 0 with `/<known command>` inside a top-level
    paragraph; names are case-insensitive; code blocks, blockquotes, list items and HTML blocks are not searched; the
    discussions routes run them, edits re-run them, and draft notes run them at publish time
    (`lib/gitlab/quick_actions/extractor.rb`, `lib/banzai/filter/quick_action_filter.rb`,
    `app/services/notes/create_service.rb`; docs.gitlab.com/api/notes). Dangerous on MRs: `/approve`, `/merge`,
    `/close`, `/assign`, `/assign_reviewer`, `/label`, `/rebase`, `/submit_review`, `/draft`, `/ready`, `/title`,
    `/target_branch`, `/lock` and others (docs.gitlab.com/user/project/quick_actions).
  - Mentions: `@user` and `@group` notify; the mention pattern needs `[A-Za-z0-9_.]` right after `@`; text in code
    and links is ignored (`app/models/user.rb`, `reference_filter.rb`). `#12`, `!12`, `group/proj#12` and SHAs
    create "mentioned in" system notes on their targets (`mentionable.rb`). Raw HTML is sanitised but `<img>`, `<a>`,
    `<details>` survive; HTML comments are hidden when rendered and kept in the stored body; the API `body` is the
    Markdown source after normalisation (quick actions removed, `\r` removed, trailing whitespace stripped)
    (docs.gitlab.com/user/markdown, docs.gitlab.com/api/notes).
  - Files and issues: `GET /projects/:id/repository/files/:path/raw?ref=` with `PRIVATE-TOKEN` (the web `/-/raw/`
    route does not accept access tokens; `auth_finders.rb`); gitlab.com allows 500 files-API requests per minute per IP
    and path. `GET /projects/:id/issues/:iid` (`iid`, `title`, `description`, `state`); an invisible issue → 404.
    Issue URLs `/<path>/-/issues/<iid>` and `/-/work_items/<iid>` (legacy `/<path>/issues/<iid>` redirects); blob
    `/-/blob/<ref>/<path>`, raw `/-/raw/<ref>/<path>`; a ref may contain `/` and GitLab splits ref and path on the
    server against its ref list; short references `namespace/project#12` and `project#12` (same namespace).
  - No REST filter lists MRs by changed file; `state=merged` with `order_by=updated_at` lists merged MRs, and
    `…/merge_requests/:iid/diffs` (paginated) gives their paths.
  - Draft notes and `bulk_publish` exist on every tier since before 17.0; `bulk_publish` gains `reviewer_state` only
    in 19.2 (tag bisect; relevant to slice C only).

## 3. Scope
### In scope
- `GET /pulls/:id/comments` for GitLab PRs: diff threads read from GitLab discussions, mapped to `PrReviewComment`.
- `POST /pulls/:id/comments` for GitLab PRs: a new positioned discussion (finding or free text) and a reply into a
  discussion, with GitLab error mapping and no 500 leak.
- The GitLab note body rules, applied on the server to every note body, and on the UI side to finding text.
- UI: "Post to MR", composer and replies for GitLab PRs, "Posted on GitLab", "View on GitLab", the note identity
  line in GitLab Integration, not-connected handling.
- Context peers for GitLab repositories: prior merged MRs, the brief's linked issue, intent tickets (GitLab issue URL
  forms and cross-project references), intent docs with the files-API fallback, skills URL import of GitLab blob
  links, smart-diff roles of `.gitlab-ci.yml` and `.gitlab/**`.
- The stored diff head and previous paths of GitLab files that positions need (one migration with up and down).

### Out of scope
- No GitLab request other than GET (and slice A's read-only GraphQL query) is sent, except
  `POST …/merge_requests/:iid/discussions` and `POST …/discussions/:discussion_id/notes`, and only from
  `POST /pulls/:id/comments`.
- No approval, unapproval, merge, label, assignment, resolve, edit, delete or award is made on GitLab by this slice.
- No draft note is created and `bulk_publish` is never called (slice C's runner may use them).
- No multi-line (`line_range`) position, image position or file-level position is sent; a finding is posted at its
  first line, as on GitHub.
- No comment is stored in the database; comments are read live, as on GitHub.
- No fallback to a general (unpositioned) MR note when GitLab refuses a position (D24).
- No retry of a POST to GitLab; no queueing of comments behind the notes rate limit.
- No webhook, no live update of comments; comments refresh when the comments query refetches, as today.
- GitHub behaviour is unchanged: the GitHub comment routes, bodies, errors and requests, the GitHub ticket, doc,
  prior-PR and skill-import rules, and the smart-diff roles of GitHub repositories stay byte-identical.
- No shared contract in `frontend/src/vendor/shared/contracts/*` changes; new behaviour uses the existing
  `PrReviewComment`, `PrCommentInput`, history, brief, intent and skill-import shapes and new error codes.
- No GitLab issue, file or prior MR is stored; context peers stay live reads as on GitHub.
- No GitLab token is sent to a host, port or path outside its connection's base URL, including for skill imports.
- No GitLab CI behaviour (slice C).

## 4. User scenarios
- US1 — As a reviewer of a GitLab MR, I open the diff and see the MR's existing diff threads on their lines, and the
  ones on older revisions in the outdated section.
- US2 — As a reviewer, I click "Post to MR" on a finding and a discussion appears on that line in GitLab; the finding
  then shows "Posted on GitLab".
- US3 — As a reviewer, I write a comment on a line or reply to a thread from DevDigest, and it appears in GitLab.
- US4 — As a team lead, I trust that a finding steered by the MR author ("/approve", "/merge", "@all", an image
  pixel) cannot approve, merge, notify or track anyone through my token.
- US5 — As a reviewer, my brief and intent use the GitLab issues the MR references, including `group/proj#12` forms,
  and docs linked from the MR; the blast card shows prior merged MRs that touched the same files.
- US6 — As a user, I import a skill from a GitLab blob link of a private project on my self-hosted instance, and from
  a public gitlab.com link.
- US7 — As a user with a read-only GitLab token, I am told clearly why posting fails.

## 5. Functional requirements
Comments — reading
- FR1 [api] — `GET /pulls/:id/comments` for a PR of a GitLab repository shall read GitLab discussions only when the
  repository is connected (A FR48) and its connection has a token; otherwise it answers 200 `[]` with no request. Any
  GitLab failure (status, timeout, connection error, malformed body) → logged, 200 `[]`. A 404 for an unknown PR is
  unchanged.
- FR2 [api] — The read shall request `GET /projects/<project id>/merge_requests/<number>/discussions?per_page=100`,
  following `x-next-page` for at most 3 pages, and stop once 100 comments are collected. A discussion is kept when its
  first note is a non-system `DiffNote` whose `position.position_type` is `text`; every non-system note of a kept
  discussion becomes one comment. Order: GitLab's discussion order, then note order inside a discussion; at most 100
  comments (the first ones). Caps per D27.
- FR3 [api] — Mapping of a GitLab note to `PrReviewComment` (the root is the discussion's first note):

  | Field | Value |
  |---|---|
  | `id` | note `id` |
  | `path` | the root's `position.new_path`, else `position.old_path` |
  | `side` | `RIGHT` when the root's `position.new_line` is set, else `LEFT` |
  | `original_line` | the root's `new_line` when set, else its `old_line` |
  | `line` | `original_line`, or null when the comment is outdated (FR4) |
  | `is_outdated` | per FR4 |
  | `body` | note `body`, verbatim |
  | `user` | `author.username`, else `unknown` |
  | `created_at` | note `created_at` in the API's time format |
  | `html_url` | `<repository web_url>/-/merge_requests/<number>#note_<id>`, built from the stored repository web URL (A FR16, FR47), never from a GitLab response |
  | `in_reply_to_id` | null for the root; the root's note `id` for every other note |

  Replies carry the root's `path`, `side`, `line`, `original_line` and `is_outdated`.
- FR4 [api] — A comment is outdated when its root's `position.head_sha` differs from the PR's **diff head** (FR35):
  the head SHA of the diff version the stored files came from, or the stored `head_sha` when no diff head is stored.

Comments — posting
- FR5 [api] — `POST /pulls/:id/comments` for a GitLab PR shall check, in this order, with no GitLab request before
  step 5: (1) the body against `PrCommentInput` (422 `validation_error`, unchanged); (2) the PR (404, unchanged);
  (3) not connected → 422 `repo_host_not_connected` "No GitLab connection for <host>. Add one in Settings → GitLab
  Integration." `details` {`host`, `repo_id`} (A FR48); (4) no token → 400 `gitlab_unavailable` "Connect a token for
  <host> in Settings → GitLab Integration to post comments."; (5) the body rules (FR13–FR17), then a sanitised body
  over 1,000,000 characters → 400 `gitlab_comment_failed` reason `body_too_long`; then FR6–FR9.
- FR6 [api] — A new comment (no `in_reply_to`) shall be positioned from the stored file whose `path` equals the
  request `path` and its stored patch, before any GitLab request:

  | Request | Diff line found in the stored patch | Position lines |
  |---|---|---|
  | `side` `RIGHT` (default), `line` L | added line with new number L | `new_line` L |
  | `side` `RIGHT`, `line` L | unchanged line with new number L, old number O | `old_line` O, `new_line` L |
  | `side` `LEFT`, `line` L | removed line with old number L | `old_line` L |
  | `side` `LEFT`, `line` L | unchanged line with old number L, new number N | `old_line` L, `new_line` N |

  Hunk headers are not lines. No stored file with that path → 400 `gitlab_comment_failed` reason `file_not_in_diff`
  "<path> is not part of this merge request's diff."; a file without a stored patch, or no matching line → reason
  `line_not_in_diff` "Line <L> (<side>) is not part of the diff of <path>." No request is sent in either case.
- FR7 [api] — The diff refs shall come from `GET /projects/<project id>/merge_requests/<number>`: its `diff_refs`
  when `base_sha`, `start_sha` and `head_sha` are all non-empty; otherwise the first entry of
  `GET …/merge_requests/<number>/versions` (`base_commit_sha`, `start_commit_sha`, `head_commit_sha`). Neither
  available → 400 `gitlab_comment_failed` reason `diff_not_ready` "GitLab is still preparing this merge request's
  diff. Try again in a minute." The head SHA found ≠ the PR's stored diff head, or no diff head stored → 400
  `gitlab_comment_failed` reason `diff_changed` "The merge request changed since this diff was loaded. Reload the PR
  and post again." No discussion is created in these cases (D15, D26).
- FR8 [api] — The new comment shall be created with `POST …/merge_requests/<number>/discussions` and `body` = the
  sanitised body, `position[position_type]=text`, `position[base_sha]`, `position[start_sha]`, `position[head_sha]`
  from FR7, `position[new_path]` = the stored path, `position[old_path]` = the stored previous path (FR35), else the
  stored path, and the line fields of FR6. No `line_code`, `line_range` or `commit_id` is sent. A 201 → 200 with
  `PrReviewComment` mapped per FR3 from the created discussion's first note, with `is_outdated` false and `line`,
  `side` as requested.
- FR9 [api] — A reply (`in_reply_to` = a note id) shall find the discussion that contains a non-system note with that
  id by reading `GET …/merge_requests/<number>/discussions?per_page=100`, at most 10 pages, stopping at the first
  match; not found → 400 `gitlab_comment_failed` reason `reply_target_not_found` "The comment you are replying to
  was not found on GitLab." with no POST. Found → `POST …/discussions/<discussion id>/notes` with the sanitised body
  → 200 with the created note mapped per FR3 (its `in_reply_to_id` = that discussion's first note id). The request's
  `path`, `line` and `side` are validated (FR5) and not used. The stored patch and diff refs are not consulted. The
  10-page cap is D27.
- FR10 [api] — GitLab answers inside the post flow shall map to these errors (every one HTTP 400, as the GitHub
  comment route; `details` = {`reason`, `cause`}, `cause` per NFR4):

  | GitLab answer | Code | `reason` | Message |
  |---|---|---|---|
  | 401 | `gitlab_permission_denied` | `token_rejected` | "GitLab rejected the token of connection <name> (401)." |
  | 403 with `insufficient_scope` | `gitlab_permission_denied` | `read_only_token` | "The token of connection <name> cannot post: it needs the api scope." |
  | other 403 | `gitlab_permission_denied` | `forbidden` | "GitLab refused the comment (403): the token's user may lack the Reporter role, or the discussion is locked." |
  | 404 | `gitlab_comment_failed` | `not_found` | "GitLab did not find this merge request, or the token cannot see it." |
  | 400, 422 or 500 to the discussion create | `gitlab_comment_failed` | `position_rejected` | "GitLab could not place the comment on <path> line <L>. Reload the PR and try again." |
  | other 4xx | `gitlab_comment_failed` | `rejected` | GitLab's `message` (redacted, ≤ 300 characters), else "GitLab refused the comment (<status>)." |
  | 429 | `gitlab_rate_limited` | `rate_limited` | "GitLab limits how many comments can be posted per minute. Try again in <n> s." with `details.retry_after_s` = `Retry-After` seconds, else 60 |
  | 3xx | `gitlab_comment_failed` | `redirect` | "GitLab answered with a redirect — check the connection's base URL." (not followed) |
  | other 5xx, timeout, connection or TLS error, non-JSON 2xx | `gitlab_comment_failed` | `unavailable` | "GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again." |

  A rejected `diff_refs`/`versions`/discussion-list read uses the same table (with `position_rejected` only for the
  create call).
- FR11 [api] — The two POSTs shall be sent once and never retried; the GETs of the post flow follow A NFR5
  (foreground: up to 3 retries of transient errors, 30 s each). One post sends at most 3 GitLab requests (MR,
  versions, create); one reply at most 11 (10 discussion pages, create).
- FR12 [api] — GitHub PRs keep Phase 1 FR41 and FR42 unchanged: no body rule is applied, no request or error changes.

GitLab note body rules (authoritative on the server). FR13–FR17 are one self-contained rule list: another spec
(for example GitLab slice C) adopts them by citing "B FR13–FR17" and needs nothing else from this spec.
- FR13 [api] — Scope: a **note body** is the Markdown text of any GitLab request DevDigest sends that creates or
  changes a note — a new discussion, a reply note, a draft note (create or update) and an edit of an existing note's
  body. Every note body — finding text, reviewer-typed text and generated text alike — shall pass FR14, FR15 and
  FR16, in that order, applied by the server to the complete body as it will be sent (after any template is
  assembled), immediately before the request that carries it; no body is exempt, because the server cannot tell typed
  text from LLM text (D21), and no code path sends a note body around the rules. GitLab runs quick actions when a note
  is created, when a note is edited and when a draft note is published (Context), so: (a) a new discussion or reply
  sends only the rule output; (b) a draft note's body passes the rules when the draft is created and every time it is
  updated, and publishing (single publish or `bulk_publish`) sends no body of its own, so a draft DevDigest publishes
  carries only rule output; (c) an edit sends only the rule output as the new body, never a stored or GitLab-returned
  body unchanged. Any length limit (GitLab's 1,000,000 characters, FR5) is checked on the output. This slice sends only
  (a) (FR8, FR9); (b) and (c) bind every spec that adopts FR13–FR17.
- FR14 [api] — Normalisation and code regions: CR LF and a lone CR become LF before any rule; a line is the text
  between two LFs (or the body's start or end). A **code region** is a run of N backticks and everything up to the
  next run of exactly N backticks (inline code and fenced blocks); a run with no closing run is not code (the
  `sanitizeForGitHub` rule, `frontend/INSIGHTS.md:37`). Tilde fences and indented code are not code regions.
- FR15 [api] — Quick actions (applied everywhere, inside code regions too): for every line, skip a leading prefix made
  of any mix of spaces, tabs, U+FEFF, U+00A0, `>` and list markers (`-`, `*` or `+` followed by a space or tab, or 1–9
  digits followed by `.` or `)` and a space or tab); when the next character is `/`, insert U+200B (zero-width space)
  directly before that `/`. Every other character stays. Examples (`{ZWSP}` stands for U+200B): `/approve` →
  `{ZWSP}/approve`; `  - > 1. /merge now` → `  - > 1. {ZWSP}/merge now`; `see /docs/x` (not at line start)
  unchanged. Inserted character (D29): U+200B, verified live by AC34 on gitlab.com; if AC34 shows that a quick action
  still runs after U+200B, this rule is revised by a new D-entry to insert a backslash `\` instead of U+200B outside
  code regions (U+200B stays inside code regions).
- FR16 [api] — Outside code regions: (a) mentions — every `@` not already followed by U+200B gets U+200B inserted
  after it; (b) HTML — `<` becomes `&lt;` and `>` becomes `&gt;`, except on exactly two allowed line forms, kept as
  they are: a line `<sub>DevDigest · <1–3 digits>% confidence</sub>`, and the body's last non-empty line when it is
  `<!-- devdigest-finding:<id> -->` with `<id>` of 1–64 characters `[A-Za-z0-9_-]`; the list is closed, and a spec
  that needs another allowed line form names it as an addition to FR16(b); (c) images — `![` becomes `!{ZWSP}[`
  (U+200B between, so Markdown images render as text and no image is fetched; D23). `&`, links, `#12`, `!12`,
  `group/proj#12` references and commit SHAs are not changed (D22).
- FR17 [api] — The rules depend only on the body (same input, same output on every route), are idempotent (applying
  them to their own output changes nothing) and change the rendered text only by invisible U+200B characters and by
  showing escaped HTML and image syntax as text.
- FR18 [ui] — For a GitLab PR the UI shall build a finding's body with the GitHub template and marker (Context), pass
  the LLM fields (title, rationale, suggestion) through `sanitizeForGitHub` and then the FR15 and FR16(c) rules, and
  send free text and replies as typed (the server applies FR13). For a GitHub PR the body is built exactly as today.

UI — comments for GitLab PRs (replaces A FR45)
- FR19 [ui] — For a GitLab PR of a connected repository, the diff shall show the listed threads and offer posting under
  the GitHub conditions (open PR; a finding placed on a rendered add or context line), with GitLab copy:

  | Element | GitHub (unchanged) | GitLab |
  |---|---|---|
  | finding action | "Post to PR" | "Post to MR" |
  | its hint | "Post this finding as an inline review comment on GitHub" | "Post this finding as a discussion on the GitLab merge request" |
  | posted link | "Posted on GitHub" | "Posted on GitLab" |
  | comment card link | "View on GitHub" | "View on GitLab" |
  | composer note | "Posts to GitHub immediately" | "Posts to GitLab immediately" |
  | post failure fallback | "Couldn’t post the comment to GitHub." | "Couldn’t post the comment to GitLab." |

  "Posted on GitLab" is derived as on GitHub: a listed comment whose body contains the finding's marker; it links to
  that comment's `html_url`.
- FR20 [ui] — A failed post shall show the server's message once (one toast, no second notification) and keep the
  composer draft; for `details.reason` `diff_changed` or `diff_not_ready` the UI also refetches the PR detail and the
  comments, so the diff reloads.
- FR21 [ui] — For a not-connected GitLab repository (A FR49) the diff shall show no posting action, composer or reply
  button, and the comments area shows "Comments are not available while <host> is not connected." For a connected
  repository whose connection has no token, posting stays offered and fails with the FR5 message (GitHub parity).
- FR22 [ui] — GitLab Integration (A FR33, FR34) shall show per connection the read-only line "Notes are posted as
  @<login>." after a successful test, "Notes are posted as the token's user." before one, and "This token is read-only
  and cannot post notes." when the test returned `token_access` `read_only`.

Context peers
- FR23 [ctx] — Every context peer of a GitLab repository shall use the repository's connection (A FR48) and its token;
  not connected or no token → the feature's degraded result with no request (FR24–FR30); a GitLab repository never
  causes a GitHub request (A FR29 kept for that rule).
- FR24 [ctx] — Prior PRs (`GET /pulls/:id/history`, Phase 5 §7.8) for a GitLab PR shall read
  `GET /projects/<project id>/merge_requests?state=merged&order_by=updated_at&sort=desc&per_page=30` (one page),
  then for each listed MR the first page of `GET …/merge_requests/<iid>/diffs?per_page=100`, at most 5 in flight,
  taking each entry's `new_path` (`old_path` for a deleted file). Selection, the 10-item cap and the wire shape are
  Phase 5's; `pr_number` = `iid`, `author` = `author.username` else `unknown`, `merged_at` = GitLab's value, `notes`
  `""`. Any failure → `history: []` (200), logged.
- FR25 [ctx] — The brief's linked issue (Phase 5 FR5034) for a GitLab PR shall be the first `#<digits>` of the MR
  description fetched with `GET /projects/<project id>/issues/<iid>` within 10 s; `title`, `description` as body
  (3,000-unit prompt cap unchanged). Not linked → `not_linked`; failure, timeout, no token or not connected →
  `fetch_failed`, the brief is still generated.
- FR26 [ctx] — Intent ticket references (Phase 5 FR5014) of a GitLab repository shall be extracted from the same texts
  with the same closing-keyword priority, de-duplication and cap of 3, using these forms instead of the GitHub URL
  and `owner/name#N` forms:
  - an issue URL `https://<host>[:<port>]/<path>/-/issues/<n>`, `…/-/work_items/<n>` or legacy `…/<path>/issues/<n>`
    → a ticket `#<n>` when its host key equals the repository's host and `<path>` equals the connection's path prefix
    plus the repository's `full_name` (case-insensitive); another path on the same host → skipped "issue reference
    from a different project (<path>)"; another host → skipped "issue reference from another host (<host>)";
  - a reference `<p>#<n>` where `<p>` is 1–21 `/`-separated segments of `[A-Za-z0-9_.-]`, not preceded by a word
    character, `/` or `.`: one segment means `<repository namespace>/<p>`; the result equal to the repository's
    `full_name` (case-insensitive) → ticket `#<n>`, else skipped "issue reference from a different project (<p>)";
  - a bare `#<n>` as today; a GitHub issue URL or a Jira-style key → skipped with today's reasons.
- FR27 [ctx] — Intent tickets of a GitLab repository shall be read with `GET /projects/<project id>/issues/<n>`, one
  at a time; text `title + "\n\n" + description`, the 3,000-unit cap and statuses unchanged. Failure reasons: no token
  → "GitLab connection <name> has no token"; not connected → "No GitLab connection for <host>"; otherwise GitLab's
  `message` (for a 404 "404 Not found") or the error text, redacted and cut to 1,000 bytes.
- FR28 [ctx] — Intent doc references of a GitLab repository shall accept, besides Markdown links and prose paths, the
  URLs `https://<host>[:<port>]/<path>/-/blob/<ref>/<file>` and `…/-/tree/<ref>/<file>` (ref = one segment, as for
  GitHub): same project (FR26 rule) → the decoded `<file>`; another project → skipped "doc link points outside this
  project (<path>)"; any other http(s) URL → skipped "not a blob or tree URL of this GitLab project (no arbitrary URL
  fetch)". Path safety, extensions, ordering and the cap of 5 are unchanged.
- FR29 [ctx] — A doc not read from the clone (Phase 5 FR5016 order unchanged: an over-cap clone blob still fails with
  no API call) shall be read with `GET /projects/<project id>/repository/files/<URL-encoded path>/raw?ref=<head SHA>`,
  reading at most 65,537 bytes: more than 65,536 bytes, 404, or any failure → `failed` "not found" (GitHub parity);
  no token or not connected → `failed` "not found" with no request.
- FR30 [ctx] — Skills URL import (`POST /skills/import/preview`, kind `url`) shall treat a URL whose host key and path
  prefix match a GitLab connection (A FR2, FR13) as a connection file:
  - accepted forms `<base_url>/<project path>/-/blob/<ref…>/<file>` and `…/-/raw/<ref…>/<file>`, project path 2–21
    segments; any other path on that host → 422 `invalid_import` "Only GitLab blob or raw file links can be imported
    from <host>";
  - fetched with `GET <REST root>/projects/<URL-encoded project path>/repository/files/<URL-encoded file>/raw?ref=<ref>`
    with the connection's token (anonymous without one), trying ref = the first k segments for k = 1, 2, … up to 5
    (and fewer than all segments), stopping at the first answer that is not 404 (D28);
  - errors (422 `invalid_import`): all 404 → "File not found on <host>, or the token cannot see it"; 401/403 →
    "GitLab rejected the token of connection <name>"; 3xx → "GitLab reports that this project has moved; use its
    current URL" (not followed); 429 → "GitLab rate limit reached; try again later"; other → today's messages;
  - the connection's address rules (A FR2, NFR2: private addresses allowed, link-local refused, no redirect, extra CA)
    apply instead of the public-address rule; the 10 s, 1 MiB, zip and UTF-8 rules apply unchanged; `source_ref` is
    the URL as typed.
- FR31 [ctx] — A URL on any host without a connection whose path contains `/-/blob/` shall be rewritten to the same
  URL with that segment replaced by `/-/raw/` and the query dropped, then fetched by today's public fetcher (public
  addresses, ≤ 3 re-validated redirects, no token). GitHub blob rewriting and every other URL are unchanged.
- FR32 [ctx] — Smart-diff roles (Phase 5 FR5028) of files of a GitLab repository shall add to `wiring` the patterns
  `.gitlab-ci.yml` (base name, any depth) and `.gitlab/**` (root-anchored), with today's order and glob semantics;
  the brief's role facts follow. GitHub repositories classify exactly as today (D25).
- FR33 [ui] — For a GitLab repository, prior-PR rows shall show `!<n>` and link to `<web_url>/-/merge_requests/<n>`
  (A FR41, FR43); intent ticket sources link to `<web_url>/-/issues/<n>` and doc sources to the GitLab blob link
  (A FR41). The skills URL import subtitle becomes "An https:// link to a Markdown file or a .zip archive. GitHub and
  GitLab blob links work too."

Data
- FR34 [db] — One versioned SQL migration with an up and a down shall add, for stored PRs, the diff head (text,
  optional) and, for stored PR files, the previous path (text, optional). GitHub rows keep both empty. The down
  migration drops them (the values are refilled by the next detail fetch).
- FR35 [api] — The GitLab detail fetch (A FR24) shall store, in the transaction that replaces the files, the diff head
  = the MR's `diff_refs.head_sha` from the detail response, else its `sha`, and for every file its `old_path` as the
  previous path. Neither is on the wire. The GitHub detail fetch is unchanged.

## 6. Workflow and communication
```mermaid
sequenceDiagram
    participant U as Browser UI
    participant API as Go API
    participant DB as Database
    participant GL as GitLab (connection of the repo host)
    U->>API: POST /pulls/:id/comments path, line, side, body
    API->>DB: PR, repository, stored file patch, diff head
    alt not connected or no token
        API-->>U: 422 repo_host_not_connected or 400 gitlab_unavailable
    else line not in stored diff
        API-->>U: 400 gitlab_comment_failed line_not_in_diff (no request)
    else
        API->>API: apply note body rules
        API->>GL: GET merge_requests/:iid (diff_refs)
        opt diff_refs empty
            API->>GL: GET merge_requests/:iid/versions
        end
        alt head differs from stored diff head or not ready
            API-->>U: 400 gitlab_comment_failed diff_changed or diff_not_ready
        else
            API->>GL: POST discussions body, position (sent once)
            GL-->>API: 201 discussion
            API-->>U: 200 PrReviewComment
        end
    end
    U->>API: GET /pulls/:id/comments
    API->>GL: GET discussions (up to 3 pages)
    API-->>U: 200 diff threads (or [] on any failure)
```
```mermaid
sequenceDiagram
    participant API as Go API
    participant GL as GitLab
    participant F as Public fetcher
    Note over API: reply with in_reply_to = note id
    API->>GL: GET discussions pages (up to 10) until the note is found
    API->>GL: POST discussions/:discussion_id/notes body
    Note over API: skills import of a URL
    alt URL under a connection base URL
        API->>GL: GET files/:file/raw?ref (token, no redirect, up to 5 ref splits)
    else other host with /-/blob/
        API->>F: GET same URL with /-/raw/ (no token, public addresses only)
    end
```
Failure behaviour:

| Interaction (caller → callee) | Trigger | On failure |
|---|---|---|
| API → GitLab discussions read | GET comments | logged; 200 `[]` |
| API → GitLab MR / versions | POST comment | FR10 table; `diff_not_ready` / `diff_changed` without POST |
| API → GitLab discussion create / reply | POST comment | FR10 table; never retried; 5xx or timeout → `unavailable` ("not confirmed") |
| API → GitLab discussions search | reply | not found → `reply_target_not_found`; errors per FR10 |
| Prior PRs → GitLab MRs and diffs | history | `history: []` |
| Brief → GitLab issue | brief generation | `fetch_failed`, generation continues |
| Intent → GitLab issue / files raw | intent derivation | `failed` source with reason |
| Skills import → GitLab files raw or public fetcher | import preview | 422 `invalid_import` with the FR30/FR31 message |
| Any → connection lookup | any GitLab call | not connected → no request (FR5, FR21, FR23) |
| UI → POST comment | Post / Comment / Reply | one toast with the message; reload on `diff_changed`/`diff_not_ready` |

## 7. Contracts
All routes keep the guards (Host allow-list, foreign `Origin` → 403 on POST, JSON bodies only, error envelope
`{"error":{"code","message","details?"}}`). No shared-contract file changes; the shapes below are today's.

`GET /pulls/:id/comments` — 200 `PrReviewComment[]` (unchanged shape). GitLab: ≤ 100 items, mapped per FR3; `[]`
without token, when not connected, or on any GitLab failure; 404 `Pull request not found` unchanged.

`POST /pulls/:id/comments` — request `PrCommentInput {path: string ≥1, line: int ≥1, side?: "LEFT"|"RIGHT", body:
string ≥1, in_reply_to?: int}` (unchanged; `in_reply_to` is a GitLab note id for GitLab PRs); 200 `PrReviewComment`.

| Status | Code | When (GitLab PR) | `details` |
|---|---|---|---|
| 422 | `validation_error` | body does not match `PrCommentInput` | Zod-style issues |
| 404 | `not_found` | unknown PR / repository | — |
| 422 | `repo_host_not_connected` | repository not connected (A FR48) | `{host, repo_id}` |
| 400 | `gitlab_unavailable` | connection has no token | — |
| 400 | `gitlab_permission_denied` | GitLab 401 / 403 (FR10) | `{reason: token_rejected\|read_only_token\|forbidden, cause}` |
| 400 | `gitlab_rate_limited` | GitLab 429 | `{reason: rate_limited, cause, retry_after_s: integer}` |
| 400 | `gitlab_comment_failed` | every other failure (FR5–FR10) | `{reason: body_too_long\|file_not_in_diff\|line_not_in_diff\|diff_not_ready\|diff_changed\|reply_target_not_found\|not_found\|position_rejected\|rejected\|redirect\|unavailable, cause}` |
| 413 | `payload_too_large` | request body over 1 MiB (unchanged) | — |

`cause` is a string: GitLab's `message` field redacted and cut to 1,000 bytes, else "GitLab answered HTTP <status>",
else the error class; for checks made without a request it is `""`. GitHub PRs: unchanged (Phase 1 §7.4).

GitLab note body rules: §5 FR13–FR17, one self-contained list (scope incl. new discussions, replies, draft notes
and edits; normalisation and code regions; quick actions; mentions, HTML and images; idempotency), applied by the
server to every GitLab note body. GitHub bodies are not changed by the server.

Context-peer routes, shapes unchanged:
- `GET /pulls/:id/history` → `{history:[{pr_number, title, merged_at, author, files_overlap[], notes}]}`; GitLab per
  FR24; `[]` on failure, without token or not connected.
- Brief `missing_inputs` `{input:"linked_issue", reason:"not_linked"|"fetch_failed"}` (FR25).
- Intent sources `{kind:"ticket"|"doc", ref, status, detail}`; the new skip and failure reasons of FR26–FR29 are
  `detail` strings.
- `POST /skills/import/preview {kind:"url", url}` → `SkillImportPreview`; 422 `invalid_import` with the FR30/FR31
  messages.

GitLab endpoints called by this slice (token in `PRIVATE-TOKEN`, REST root `<base_url>/api/v4`, project by numeric id
except the skill import, which addresses the URL's project by URL-encoded path; no redirect followed):
`GET /projects/:id/merge_requests/:iid/discussions`, `GET /projects/:id/merge_requests/:iid`,
`GET …/merge_requests/:iid/versions`, `POST …/merge_requests/:iid/discussions`,
`POST …/discussions/:discussion_id/notes`, `GET /projects/:id/merge_requests?state=merged…`,
`GET …/merge_requests/:iid/diffs`, `GET /projects/:id/issues/:iid`, `GET /projects/:id/repository/files/:path/raw`.

## 8. Data model (logical)
- Pull request (existing) gains its **diff head**: the head SHA of the GitLab diff version its stored files came from;
  set by the GitLab detail fetch together with the files; empty for GitHub and for GitLab PRs fetched before this
  slice (FR34, FR35).
- PR file (existing) gains its **previous path** (`old_path` from GitLab; equal to the path unless renamed); empty
  for GitHub.
- Derived, never stored: GitLab comments and their outdated state, discussion ids, sanitised bodies, issues, files,
  prior MRs, skill import downloads.

## 9. States and UX
| Screen / element | Loading | Empty | Error | Degraded | Success | Notes |
|---|---|---|---|---|---|---|
| Diff threads (GitLab PR) | as today | no threads | as today (GET failure → no threads) | outdated threads in the outdated section; not connected → "Comments are not available while <host> is not connected." | threads on their lines, "View on GitLab" | |
| "Post to MR" (finding) | "Posting…", disabled | — | one toast with the message; `diff_changed`/`diff_not_ready` → detail and comments refetch | hidden when not connected, for closed/merged MRs and unmatched findings | "Posted on GitLab" link | |
| Inline composer / reply | "Posting…", disabled | empty text → Comment disabled | toast, draft kept | hidden when not connected | thread updated after refetch | "Posts to GitLab immediately" |
| GitLab Integration identity line | "Testing…" | "Notes are posted as the token's user." | failed test → same as before a test | read-only token line | "Notes are posted as @<login>." | per connection |
| Prior PRs (GitLab) | as today | hidden (as today) | hidden | — | rows with `!<n>` | |
| Intent card sources | as today | as today | `failed` source with reason | skipped references with reasons | GitLab issue / blob links | |
| Skills "Import from URL" | as today | as today | inline message | — | preview | new subtitle |

## 10. Edge cases
- EC1 [api] — The discussions contain system notes, a general (non-diff) discussion, an image-position and a
  file-position diff note, and a diff discussion with 2 replies → only the text diff discussion's 3 notes are listed,
  replies with `in_reply_to_id` = the root id and the root's path, side and line (FR2, FR3).
- EC2 [api] — 350 discussions with 150 diff notes → at most 3 discussion pages are read and exactly the first 100
  comments are returned (FR2).
- EC3 [api] — A thread whose root position has an older `head_sha` than the diff head → `is_outdated: true`,
  `line: null`, `original_line` set; when no diff head is stored, the stored `head_sha` is compared (FR4).
- EC4 [api] — Post on an unchanged line at new 12 / old 10 (RIGHT) → position `old_line` 10, `new_line` 12; a removed
  line old 7 (LEFT) → `old_line` 7 only; an added line new 3 → `new_line` 3 only (FR6).
- EC5 [api] — Post on a line between hunks, on a file whose patch is not stored (too large or collapsed), or on a path
  not in the diff → 400 `line_not_in_diff` / `file_not_in_diff`, no GitLab request (FR6).
- EC6 [api] — `diff_refs` empty right after MR creation and `/versions` empty → 400 `diff_not_ready`, no POST;
  `diff_refs` empty but `/versions` has an entry matching the diff head → posted with its SHAs (FR7).
- EC7 [api] — A push happened after the diff was loaded (GitLab head ≠ diff head), or the PR was fetched before this
  slice (no diff head) → 400 `diff_changed`, no POST; after the UI reloads the detail, the same post succeeds (FR7,
  FR20, FR35).
- EC8 [api] — A renamed file `old/a.ts` → `new/a.ts` → position `old_path` `old/a.ts`, `new_path` `new/a.ts` (FR8,
  FR35).
- EC9 [api] — GitLab answers the create with 400 "line_code can't be blank" or with a 500 HTML page → 400
  `gitlab_comment_failed` `position_rejected`; the response and logs contain no HTML from GitLab and no token (FR10,
  NFR4).
- EC10 [api] — A `read_api` token → 403 `insufficient_scope` → 400 `gitlab_permission_denied` `read_only_token`; a
  locked discussion for a non-member → `forbidden`; an expired token → `token_rejected` (FR10, D4).
- EC11 [api] — gitlab.com answers 429 with `Retry-After: 37` → 400 `gitlab_rate_limited` `retry_after_s` 37; a 429
  without `Retry-After` (GitLab 17.0) → 60; nothing is retried (FR10, FR11).
- EC12 [api] — The create call times out → 400 `unavailable` "the comment was not confirmed"; no second POST is sent
  (FR10, FR11).
- EC13 [api] — A reply to a note on page 4 of the discussions → 4 list requests and one POST to that discussion; a
  reply to a deleted note → `reply_target_not_found` after at most 10 list pages, no POST; a reply to a system note id →
  not found (FR9).
- EC14 [api] — A body of 999,990 characters that grows past 1,000,000 with inserted U+200B → 400 `body_too_long`, no
  request (FR5, FR15, FR16).
- EC15 [api] — Body corpus: `/approve`, `/MERGE`, `  /close`, `> /close`, `- /assign @me`, `1) /label ~x`, a
  ```` ``` ```` fence containing `/merge`, an unclosed backtick run before `/close`, `\r\n/close`, a lone CR before
  `/close`, U+FEFF + `/close`, `@all`, `` `@user` ``, `email@example.com`, `<img src=x>`, `<!-- hide`, `![p](https://t/x.png)`,
  the `<sub>` line and the marker as the last line, a forged marker in the middle → each line per FR14–FR16; applying
  the rules twice gives the same text (FR17).
- EC16 [api] — The repository's connection is deleted between loading the diff and posting → 422
  `repo_host_not_connected`, no request; GET comments → `[]` (FR1, FR5, A FR48).
- EC17 [api] — A merged or closed GitLab MR posted to through the API → accepted by GitLab and returned (the UI does
  not offer it) (FR8, FR19).
- EC18 [ui] — Double click on "Post to MR" → one request (the button is disabled while posting); two tabs posting the
  same finding → two discussions, both detected as posted (parity) (FR19).
- EC19 [ctx] — Prior MRs: 30 merged MRs listed, 12 overlap, one is the current MR, one has an unparsable
  `merged_at` → the Phase 5 selection gives 10 items without the current MR; one diffs request fails → `[]` (FR24).
- EC20 [ctx] — Ticket corpus for repository `acme/platform/payments` on `git.corp.example/gitlab`:
  `Closes https://git.corp.example/gitlab/acme/platform/payments/-/issues/7`, `acme/platform/payments#8`,
  `payments#9`, `other#3`, `acme/other#4`, `https://gitlab.com/acme/platform/payments/-/issues/5`,
  `…/-/work_items/10`, `#11`, `!12`, `JIRA-1` → tickets 7, 8, 9 (cap 3; 10 and 11 skipped "ticket limit reached"),
  skipped `other` and `acme/other` (different project), `gitlab.com` (another host) and `JIRA-1`; `!12` ignored
  (FR26).
- EC21 [ctx] — A ticket that is a confidential issue the token cannot see → `failed` "404 Not found"; no token →
  `failed` "GitLab connection <name> has no token" with no request (FR27).
- EC22 [ctx] — Docs: a same-project blob URL of a file missing in the clone → read through the files API; a 70,000-byte
  file only on GitLab → `failed` "not found"; a blob URL of another project → skipped (FR28, FR29).
- EC23 [ctx] — Skill import from `https://git.corp.example/gitlab/acme/skills/-/blob/feature/x/skills/a.md` (private,
  host on 10.0.0.5) → ref `feature` answers 404, ref `feature/x` answers 200 → imported with the connection's token;
  the same URL with no connection → refused as a non-public address by today's rule (FR30, FR31).
- EC24 [ctx] — Skill import from `https://gitlab.com/acme/skills/-/blob/main/a.md?ref_type=heads` without a gitlab.com
  connection → fetched from `https://gitlab.com/acme/skills/-/raw/main/a.md` with no token header; with a gitlab.com
  connection → through the files API with its token (FR30, FR31).
- EC25 [ctx] — A GitLab connection URL of the form `…/-/tree/main/skills` or `…/-/snippets/1` → 422 "Only GitLab blob
  or raw file links can be imported from <host>"; a 302 from the files API → "has moved", the target not contacted
  (FR30).
- EC26 [ctx] — Smart diff of a GitLab MR touching `.gitlab-ci.yml`, `ci/.gitlab-ci.yml`, `.gitlab/merge_request_templates/a.md`
  and `src/gitlab/x.ts` → the first three `wiring`, the last `core`; a GitHub PR touching `.gitlab-ci.yml` → `core` as
  today (FR32).
- EC27 [ui] — A GitLab connection test that returned `token_access: read_only` → the identity line says the token
  cannot post; posting still sends the request and shows the `read_only_token` message (FR22, FR10).

## 11. Non-functional requirements
- NFR1 [api] — Write surface toward GitLab: apart from slice A's read-only GraphQL query (A NFR3), the only non-GET
  GitLab requests of the whole backend after this slice (slice C excluded) are the two POSTs of FR8 and FR9, each sent once per user action, never retried, never from a
  background job or poll.
- NFR2 [api] — Every note body that leaves the backend for GitLab has passed FR13–FR17; no code path posts to GitLab
  around the rules. A body that GitLab would treat as a quick action, a mention, raw HTML or an image (per the
  Context facts) never reaches GitLab from DevDigest.
- NFR3 [api] — Token scoping: a GitLab token is sent only to the REST root of its own connection (A NFR2); a skill
  import sends a token only when the URL's host key and path prefix match that connection, and then only to the
  connection's REST root, never to the URL as typed; the public fetcher never sends a token or an `Authorization`
  header.
- NFR4 [api] — No GitLab response text reaches a response or log line unredacted: `cause` and messages are redacted
  for every stored token (A FR31) before being cut to their limits; non-JSON bodies (HTML error pages) are never
  echoed; no log line contains a note body, a token, an issue description or a file's content.
- NFR5 [api] — Request budgets: GET comments ≤ 3 GitLab requests; a post ≤ 3; a reply ≤ 11; prior PRs ≤ 31 with ≤ 5
  in flight; a brief ≤ 1 issue request; intent ≤ 3 issue and ≤ 5 file requests; a skill import ≤ 5 requests. All
  reads use A NFR5 timeouts (30 s per call; the brief issue 10 s; the skill import 10 s total).
- NFR6 [api] — Observability: each GitLab call logs one line per A NFR9 (host, operation, status, attempt, latency);
  a post or reply logs one INFO line with PR id, operation, outcome code and `reason`, without the body.
- NFR7 [api] — Compatibility: with only GitHub repositories, every existing golden and test passes unchanged; GitHub
  comment requests, bodies and errors, GitHub ticket/doc/prior-PR rules, GitHub skill-import rewriting and GitHub
  smart-diff roles are byte-identical; no file under `frontend/src/vendor/shared/contracts` changes.
- NFR8 [api] — Latency: `POST /pulls/:id/comments` for a GitLab PR adds at most 50 ms of DevDigest processing (body
  rules, patch mapping) to the GitLab round trips for a 1 MiB body and a 3,000-line patch; the body rules are linear in
  the body length.
- NFR9 [ui] — Accessibility: "Post to MR", composer and reply controls keep the GitHub ones' labels, focus order and
  disabled states; the identity and read-only lines are text, not colour only.
- NFR10 [ui] — All new copy is in `messages/en/*.json`; the pseudo-messages test finds no hard-coded copy.

## 12. Dependencies and rollout
- Depends on slice A (connections, host binding, not-connected state, repository web URL, GitLab detail fetch, error
  codes) being implemented first; independent of slice C.
- Shared-contract change: no. New error codes (`gitlab_comment_failed`, `gitlab_permission_denied`,
  `gitlab_rate_limited`, `gitlab_unavailable` — reserved by A §7) and `details.reason` values are additions inside the
  existing envelope; new message keys only.
- Persisted data change: yes — FR34 (two optional attributes, up and down migration); filled by the next GitLab
  detail fetch, so posting needs one PR reload after the upgrade (EC7).
- No feature flag: GitLab PRs get the behaviour once the slice ships; GitHub is unaffected (NFR7).
- Release: backend and frontend ship together (the UI shows posting for GitLab PRs only with a backend that accepts
  it).
- Conflicts with related specs (those specs are not edited, D8):

  | Spec | Item | Relation |
  |---|---|---|
  | GitLab A | FR29 bullets "comments → `[]`", "posting → 400 `gitlab_unavailable` not available yet", "prior PRs empty", "brief issue omitted", "intent tickets unresolved", "intent docs clone only" | superseded by FR1–FR11, FR24–FR29; FR29's host-isolation rule and CI bullet are kept (CI: slice C) |
  | GitLab A | FR45 (no posting UI, "Comments from GitLab are not shown yet.") | superseded by FR19–FR21 |
  | GitLab A | NFR3 read-only toward GitLab | superseded for the two POSTs of NFR1; its quick-action forward reference is met by FR13–FR17 |
  | GitLab A | Out of scope "No MR comment is listed or posted, no GitLab issue … no GitLab file is read through the API" | superseded by this slice's scope |
  | GitLab A | §7 rows `GET`/`POST /pulls/:id/comments` | superseded by §7 here |
  | GitLab A | FR24 detail fetch | extended by FR35 (diff head, previous path) |
  | GitLab A | FR42 host copy, FR43 `!<n>` places | extended by FR19, FR33 (comment copy, prior PRs) |
  | GitLab A | §9 row "Diff comments area (GitLab PR)" | superseded by §9 here |
  | GitLab A | FR48 "B answers `repo_host_not_connected`" | met by FR5 (POST); reads degrade to `[]` / failed sources without a request (FR1, FR23) |
  | Phase 1 | FR41, FR42, §7.4 comment routes, EC24 | extended to GitLab by FR1–FR11; GitHub unchanged (FR12) |
  | Phase 5 | FR5033, §7.8 prior PRs | extended by FR24 |
  | Phase 5 | FR5034 brief linked issue | extended by FR25 |
  | Phase 5 | FR5014, §7.4.2 ticket rules; FR5016, FR5054 docs fallback; EC5003 reasons | extended by FR26–FR29 for GitLab repositories |
  | Phase 5 | FR5028, §7.6 roles | extended by FR32 for GitLab repositories |
  | Phase 2 | FR239, EC222, NFR203 (public addresses only, no token) | extended by FR30, FR31: connection URLs use the connection's address rules and token (intentional exception, NFR3) |
  | Settings A | FR18 comment identity | kept for GitHub; GitLab identity by FR22 |
- What slice C must know (forward references, not specified here): slice C adopts the note body rules by citing
  "B FR13–FR17"; they already cover every body the runner sends (summary note, inline threads, draft notes when
  created or updated — GitLab runs quick actions when a draft is published — and note edits, FR13(b), (c)); a new
  allowed line form (for example a summary marker) is named as an addition to FR16(b); `bulk_publish` exists on 17.0 (204 on success) but `reviewer_state` only from about 19.2, so a
  17.0 baseline cannot set a review state; positions follow FR6–FR8 (both paths, line fields per side, SHAs from
  `diff_refs` or the first `/versions` entry); 429 handling per FR10; a `read_api` token cannot post (403
  `insufficient_scope`).

## 13. Acceptance criteria
- AC1 [api] — Given a connected GitLab repository with a token and a stub whose discussions hold system notes, a
  general discussion, an image and a file position, and a text diff discussion (root + 2 replies, root position
  `new_line` 5 at the diff head), when `GET /pulls/:id/comments` is called, then the response is 3 `PrReviewComment`
  items: the root with `side` `RIGHT`, `line` 5, `original_line` 5, `is_outdated` false, `in_reply_to_id` null,
  `user` = the username, `html_url` `<web_url>/-/merge_requests/<n>#note_<id>`, and both replies with
  `in_reply_to_id` = the root id and the root's path, side and line.
  Traces: FR1, FR2, FR3, EC1 · Verify: integration
- AC2 [api] — Given 350 discussions with 150 text diff notes, when comments are read, then at most 3 discussion
  requests (`per_page=100`) are sent and exactly the first 100 comments are returned in GitLab order.
  Traces: FR2, EC2, NFR5 · Verify: integration
- AC3 [api] — Given a root position with `head_sha` older than the stored diff head, then the comment has
  `is_outdated` true, `line` null and `original_line` set; given a LEFT-only root (`old_line` 7), then `side` `LEFT`
  and `line` 7; given no stored diff head, then the stored `head_sha` is compared.
  Traces: FR3, FR4, EC3 · Verify: unit
- AC4 [api] — Given no token, a not-connected repository, and a stub answering 500, an HTML 200 and a timeout, then
  each GET comments answers 200 `[]`, and the first two sent no GitLab request.
  Traces: FR1, EC16 · Verify: integration
- AC5 [api] — Given a stored patch and a stub MR with complete `diff_refs` equal to the diff head, when posting RIGHT
  line 12 (unchanged, old 10), LEFT line 7 (removed) and RIGHT line 3 (added), then each create request carries
  `position_type` `text`, the three SHAs, `new_path`, `old_path` and exactly `old_line` 10 + `new_line` 12, `old_line`
  7, `new_line` 3, no `line_code`/`line_range`, and each answers 200 `PrReviewComment` with `is_outdated` false; the
  stub saw 2 requests per post (MR, create).
  Traces: FR6, FR7, FR8, EC4, NFR5 · Verify: integration
- AC6 [api] — Given a renamed file stored by a detail fetch from `old/a.ts` to `new/a.ts`, when a comment is posted on
  it, then the position has `old_path` `old/a.ts` and `new_path` `new/a.ts`; the detail fetch stored the diff head
  and previous paths and `GET /pulls/:id` shows neither.
  Traces: FR8, FR34, FR35, EC8 · Verify: integration
- AC7 [api] — Given a line between hunks, a file without stored patch and a path not in the diff, then each post
  answers 400 `gitlab_comment_failed` with `reason` `line_not_in_diff`, `line_not_in_diff` and `file_not_in_diff`, and
  the stub received no request.
  Traces: FR6, EC5 · Verify: integration
- AC8 [api] — Given `diff_refs` empty and `/versions` empty, then 400 `diff_not_ready` and no POST; given `diff_refs`
  empty and `/versions[0]` matching the diff head, then the create uses its three SHAs; given a GitLab head different
  from the diff head, or no stored diff head, then 400 `diff_changed` and no POST.
  Traces: FR7, EC6, EC7 · Verify: integration
- AC9 [api] — Given a reply with `in_reply_to` = a note on discussion page 4, then 4 list requests and one POST to
  `discussions/<that id>/notes` with only the sanitised body, and the 200 response has `in_reply_to_id` = that
  discussion's first note id; given a deleted note id or a system note id, then 400 `reply_target_not_found` after ≤ 10
  list requests and no POST.
  Traces: FR9, EC13, NFR5 · Verify: integration
- AC10 [api] — Given the stub answering the create with 401, 403 `insufficient_scope`, 403 other, 404, 400
  "line_code can't be blank", 500 HTML, 409, 429 `Retry-After: 37`, 429 without `Retry-After`, 302, 503 and a
  timeout, then the responses are 400 with the FR10 code, `reason` and message (`retry_after_s` 37 and 60), the
  redirect target received nothing, the create was sent exactly once each time, and no response or log contains HTML
  from the stub or the token.
  Traces: FR10, FR11, EC9, EC10, EC11, EC12, NFR1, NFR4 · Verify: integration
- AC11 [api] — Given a not-connected repository, then POST answers 422 `repo_host_not_connected` with `{host,
  repo_id}`; given a connection without token, then 400 `gitlab_unavailable` with the FR5 message; neither sends a
  request; an invalid body still answers 422 `validation_error` first.
  Traces: FR5, EC16 · Verify: integration
- AC12 [api] — Given the EC15 corpus, when the body rules run, then every output equals its golden: U+200B before each
  `/` that follows only the FR15 prefix, also inside the fence; U+200B after each `@` outside code; `&lt;`/`&gt;`
  outside code except the `<sub>` line and the final marker line; `!{ZWSP}[` for images; the forged middle marker escaped;
  and a second application returns the same text.
  Traces: FR13, FR14, FR15, FR16, FR17, EC15, NFR2 · Verify: unit
- AC13 [api] — Given posts of a finding body, a free-text comment `/approve\n@ann see <b>x</b>` and a reply
  `- /merge`, then the bodies the stub received are the FR13–FR16 outputs; given a GitHub PR with the same inputs,
  then the GitHub stub receives the bodies unchanged.
  Traces: FR12, FR13, NFR2, NFR7 · Verify: integration
- AC14 [api] — Given a 999,990-character body that exceeds 1,000,000 characters after the rules, then 400
  `body_too_long` and no request; given a 1 MiB body and a 3,000-line patch, then DevDigest processing (stub latency
  excluded) is ≤ 50 ms at p95 over 50 runs.
  Traces: FR5, EC14, NFR8 · Verify: integration
- AC15 [api] — Given a merged GitLab MR, when a valid comment is posted through the API, then the create is sent and 200
  is returned.
  Traces: EC17, FR8 · Verify: integration
- AC16 [api] — Given posts and replies with a stub, then one INFO line per action has PR id, operation, code and
  reason and no body; the post and reply flows sent no request other than those of FR7–FR9, and no background job or
  poll ever sent a POST.
  Traces: NFR1, NFR6 · Verify: integration
- AC17 [ui] — Given an open GitLab PR of a connected repository with a line-placed finding (mocked), then it shows
  "Post to MR" with the GitLab hint; clicking sends POST `{path, line: start_line, side:"RIGHT", body}` where the body
  has the template, the marker and the LLM fields escaped per FR18 (a title `/close @x <i>` → `{ZWSP}/close @{ZWSP}x &lt;i&gt;`);
  while pending the button is disabled; after the comments refetch returns a body with the marker, "Posted on GitLab"
  links to its `html_url`. A GitHub PR renders and posts exactly as today.
  Traces: FR18, FR19, EC18, NFR9 · Verify: component
- AC18 [ui] — Given a GitLab PR, then the composer note reads "Posts to GitLab immediately", comment cards link "View on
  GitLab", a reply sends `in_reply_to` = the thread root id, and a mocked 400 `diff_changed` shows one toast with the
  message, keeps the draft and refetches the PR detail and comments.
  Traces: FR19, FR20 · Verify: component
- AC19 [ui] — Given a GitLab PR whose repository has `connection_id` null, then no "Post to MR", composer or reply
  button is rendered and the comments area reads "Comments are not available while <host> is not connected."; given a
  connection without token, then "Post to MR" is shown and a mocked 400 `gitlab_unavailable` shows its message.
  Traces: FR21 · Verify: component
- AC20 [ui] — Given GitLab Integration with a connection tested `ok` as `ann`, one untested and one tested
  `token_access:"read_only"`, then the rows read "Notes are posted as @ann.", "Notes are posted as the token's user."
  and "This token is read-only and cannot post notes."
  Traces: FR22, EC27 · Verify: component
- AC21 [ctx] — Given a stub with 30 merged MRs (12 overlapping, one = the current MR, one with an unparsable
  `merged_at`), when `GET /pulls/:id/history` is called for a GitLab PR, then the request list is one
  `state=merged&order_by=updated_at&sort=desc&per_page=30` list and ≤ 30 `diffs?per_page=100` requests with ≤ 5 in
  flight, and the body follows Phase 5 selection (10 items, no current MR, `author` usernames); with one diffs request
  failing, then `history: []`; without token or not connected, then `[]` and no request; GitHub stubs saw nothing.
  Traces: FR23, FR24, EC19, NFR5 · Verify: integration
- AC22 [ctx] — Given an MR description "Fixes #7" and an issue stub, then the brief prompt has the GitLab issue's
  title and description; given the stub delaying 11 s, a 404, no token or not connected, then `missing_inputs` has
  `{linked_issue, fetch_failed}` and the brief is stored.
  Traces: FR25 · Verify: integration
- AC23 [ctx] — Given the EC20 corpus, when ticket references are extracted for the GitLab repository, then refs and
  skipped entries equal the golden (tickets 7, 8, 9; reasons per FR26); given the same corpus for a GitHub repository,
  then the result equals today's golden.
  Traces: FR26, EC20, NFR7 · Verify: unit
- AC24 [ctx] — Given an issue stub, then tickets read `GET /projects/<id>/issues/<n>` one at a time and become `used`
  (or `truncated` above 3,000 units); a 404 → `failed` "404 Not found"; no token → `failed` "GitLab connection <name>
  has no token"; not connected → `failed` "No GitLab connection for <host>"; the last two sent no request.
  Traces: FR27, EC21 · Verify: integration
- AC25 [ctx] — Given a clone fixture and a files stub, when docs are read for a same-project blob URL missing in the
  clone, a 70,000-byte GitLab-only file, another project's blob URL, an `https://example.com/a.md` link and a 70,000-byte
  clone file, then the results are `used`, `failed` "not found", skipped "doc link points outside this project",
  skipped "not a blob or tree URL of this GitLab project", and `failed` with the size message with no API call.
  Traces: FR28, FR29, EC22 · Verify: integration
- AC26 [ctx] — Given a connection `https://git.corp.example/gitlab` whose host resolves to a private address and a
  files stub that answers 404 for ref `feature` and 200 for ref `feature/x`, when the EC23 URL is previewed, then the
  stub saw two `…/projects/acme%2Fskills/repository/files/skills%2Fa.md/raw?ref=…` requests with `PRIVATE-TOKEN`, and
  the preview has `source` `imported_url` and `source_ref` = the typed URL; the EC25 URLs answer 422 with their
  messages and a 302 target receives nothing.
  Traces: FR30, EC23, EC25, NFR3 · Verify: integration
- AC27 [ctx] — Given no gitlab.com connection, when `https://gitlab.com/acme/skills/-/blob/main/a.md?ref_type=heads`
  is previewed against a stub resolver, then the fetched URL is `https://gitlab.com/acme/skills/-/raw/main/a.md` with
  no `Authorization` or `PRIVATE-TOKEN` header; with a gitlab.com connection, then the files API with its token is
  used; Phase 2 AC255 and AC258 still pass.
  Traces: FR31, EC24, NFR3, NFR7 · Verify: integration
- AC28 [ctx] — Given the EC26 paths, then the GitLab repository's roles are `wiring`, `wiring`, `wiring`, `core`, the
  GitHub repository's `.gitlab-ci.yml` is `core`, and Phase 5 AC5043 still passes.
  Traces: FR32, EC26, NFR7 · Verify: unit
- AC29 [ui] — Given a GitLab repository, then prior-PR rows show `!<n>` linking to `<web_url>/-/merge_requests/<n>`,
  intent ticket sources link to `<web_url>/-/issues/<n>`, and the skills URL import subtitle names GitHub and GitLab.
  Traces: FR33 · Verify: component
- AC30 [db] — Given a migrated database, then PRs and PR files have the two optional attributes empty for existing
  rows; down then up round-trips with GitHub and GitLab data; after the down migration a GitLab post answers
  `diff_changed` until a detail fetch refills the diff head.
  Traces: FR34, FR35 · Verify: integration
- AC31 [api] — Given only GitHub repositories, then every existing golden and contract test passes unchanged and no
  file under `frontend/src/vendor/shared/contracts` differs from the base branch.
  Traces: NFR7 · Verify: component
- AC32 [api] — Given forced errors in every GitLab flow of this slice with a stored token `glpat-AbCdEfGhIjKlMnOpQrSt`
  echoed in the stub's error messages, then no response, log line or intent `detail` contains it, and each GitLab call
  logged one A NFR9 line.
  Traces: NFR4, NFR6 · Verify: integration
- AC33 [ui] — Given the new components rendered with pseudo messages, then no hard-coded copy is found.
  Traces: NFR10 · Verify: component
- AC34 [ui] — Given a throwaway gitlab.com project and MR, a real `api` token and a `read_api` token, when a tester
  posts a finding whose rationale contains `/approve`, `/close`, `  /merge`, `> /label ~x`, `- /assign @me`, `@all`,
  `<img src=https://example.com/p.png>` and `![p](https://example.com/p.png)`, a free-text comment and a reply, then
  the MR is not approved, closed, merged, labelled or assigned, no one is notified, no image loads, the notes show the
  text visibly (U+200B invisible), the marker is hidden and DevDigest shows "Posted on GitLab"; posting on a context
  line, a removed line and a renamed file lands on the right lines; the `read_api` token gets the `read_only_token`
  message; the GitLab audit shows only the POSTs of NFR1.
  Traces: NFR1, NFR2, FR8, FR9, FR15, FR16, FR19, EC10 · Verify: manual

## 14. Traceability
| Source | Area | FR | EC / NFR | AC |
|---|---|---|---|---|
| Request: comments parity (list) | api | FR1–FR4 | EC1–EC3, EC16, NFR5 | AC1–AC4 |
| Request: post finding / comment as positioned discussion | api | FR5–FR8, FR10–FR12 | EC4–EC12, EC14, EC17, NFR1, NFR4, NFR8 | AC5–AC8, AC10, AC11, AC14–AC16 |
| Request: reply via numeric `in_reply_to` | api | FR9 | EC13 | AC9 |
| Research fact 6, A NFR3: quick actions, mentions, HTML | api, ui | FR13–FR18 | EC15, NFR2 | AC12, AC13, AC17, AC34 |
| D5 copy ("Post to MR", "Posted on GitLab") | ui | FR19–FR21, FR33 | EC18, NFR9, NFR10 | AC17–AC19, AC29, AC33 |
| A hand-off: notes identity, read-only token (D4) | ui | FR22 | EC10, EC27 | AC20 |
| Request: context peers | ctx | FR23–FR29 | EC19–EC22, NFR5 | AC21–AC25 |
| Request: skills URL import, D6 token scoping | ctx | FR30, FR31 | EC23–EC25, NFR3 | AC26, AC27 |
| Request: smart diff `.gitlab/**` | ctx | FR32 | EC26 | AC28 |
| Positions need diff head and previous path | db, api | FR34, FR35 | EC7, EC8 | AC6, AC30 |
| Binding: GitHub unchanged, contracts unchanged | all | FR12 | NFR7, NFR4, NFR6 | AC13, AC31, AC32 |

## 15. Design review
| # | Gap / proposal | Source | Area | Decision |
|---|---|---|---|---|
| G1 | Where the authoritative sanitisation lives | request | api, ui | server, every GitLab body; UI escapes LLM fields too (D11) |
| G2 | Quick actions inside lists, quotes, code are not run by GitLab today, but Markdown structure is attacker-shaped and edits re-run them | research | api | neutralise every `/`-line after the FR15 prefix, inside code too (D12); U+200B checked live, backslash fallback (D29) |
| G3 | Reviewer-typed text loses `@` mentions and HTML | analysis | api | accepted: same rules for all bodies (D21) |
| G4 | `#12`/`!12` references create "mentioned in" notes on other objects | research | api | rejected: not neutralised, GitHub parity (D22) |
| G5 | Markdown images load from attacker hosts (GitLab does not proxy by default) | research | api | accepted: neutralised (D23) |
| G6 | Positions against an older diff version behave unpredictably (accepted, 400 or 500) | research | api | accepted: refuse with `diff_changed` and reload (D15, D26) |
| G7 | Renamed files need `old_path`; A stores only the new path | analysis | db | store previous path and diff head (D16) |
| G8 | GitLab 500 HTML on bad positions would leak | research | api | mapped to `position_rejected`, no HTML echoed (FR10, NFR4) |
| G9 | Fallback to an unpositioned note on rejection | research advice | api | rejected: no fallback (D24) |
| G10 | Notes rate limit (60/min on gitlab.com) | research | api | surfaced as `gitlab_rate_limited` with seconds; no queue (D18) |
| G11 | A POST timeout leaves the outcome unknown | analysis | api | not retried; message tells the user to check GitLab (FR10) |
| G12 | Skill import from self-hosted private addresses vs the public-only rule | D6 | ctx | connection URLs use the connection's rules and token (D19) |
| G13 | Ref names with `/` in blob URLs | research | ctx | accepted: up to 5 ref splits (D19, D28) |
| G14 | `.gitlab-ci.yml` roles for GitHub repositories | binding | ctx | accepted: GitLab repositories only (D25) |
| G15 | Double toast on comment failure in the DiffTab path | code research | ui | GitLab path shows one toast (FR20); GitHub path unchanged |
| G16 | Duplicate posts from two tabs | analysis | api | no dedup (GitHub parity) (EC18) |
| G17 | Listing and reply-search caps on large MRs | analysis | api | accepted: 3 pages / 100 comments, reply search 10 pages (D27) |

## 16. Decisions
- D1 — GitLab parity including CI, split into slices A → B ∥ C; this spec is slice B (user, 2026-10-24).
- D2 — Several GitLab instances, installation-wide connections, a repository bound to its connection by host (user,
  2026-10-24).
- D3 — GitHub and GitLab repositories side by side in one workspace (user, 2026-10-24).
- D4 — Personal, project and group access tokens with `api` + `read_repository`; a read-only token can sync, and
  posting fails with `gitlab_permission_denied` (user, 2026-10-24).
- D5 — "PR" stays in generic chrome; host-specific copy where an action touches GitLab: "Post to MR", "Posted on
  GitLab", `!123` (user, 2026-10-24).
- D6 — https only, no redirects, a token only to its connection's origin and path prefix, a corporate CA file (user,
  2026-10-24).
- D7 — Minimum GitLab version 17.0; older instances get a warning (user, 2026-10-24).
- D8 — Reconcile with other specs through deltas named in this spec; no other spec is edited (user, 2026-10-24).
- D9 — Deleting a used connection is allowed; its repositories become "not connected" (local data readable, every
  GitLab call skipped or answered with a clear error) and re-bind when a connection for the same host is created
  (user, 2026-10-25, slice A revision D25/D26).
- D10 — Sync follows the project id and updates the stored path and web URL on every project fetch (user, 2026-10-25,
  slice A revision D27); this slice's links and `html_url` use that stored web URL.
- D11 — The server applies the note body rules to every GitLab note body and is authoritative; the UI also escapes
  LLM fields so its request already matches (spec author, 2026-10-25).
- D12 — Quick-action neutralisation inserts U+200B before the `/` (invisible, documented by no GitLab page but needing
  only the `^\/` rule of the extractor) rather than a backslash, because it reads the same inside and outside code
  (spec author, 2026-10-25; checked live by AC34).
- D13 — Comment-post failures are HTTP 400 with GitLab codes and `details.reason`, matching the GitHub route's 400
  convention; not connected stays 422 as slice A defines (spec author, 2026-10-25).
- D14 — `html_url` is built by the server from the stored repository web URL and `#note_<id>` (GitLab has no note
  URL field; A FR41 forbids response URLs) (spec author, 2026-10-25).
- D15 — A post refuses to create a discussion when GitLab's latest diff head differs from the diff the user saw
  (`diff_changed`), instead of posting against an older version as GitHub does (spec author, 2026-10-25; confirmed by the user, D26).
- D16 — The GitLab detail fetch stores the diff head and previous paths (spec author, 2026-10-25).
- D17 — Draft notes are not used for single comments; immediate discussions match GitHub's immediate comments and
  avoid the publish step (spec author, 2026-10-25).
- D18 — No client-side throttle for the notes limit; one user action is one note (spec author, 2026-10-25).
- D19 — Skill imports under a connection's base URL go through the files API with that connection's token and
  address rules; other GitLab blob links are rewritten to `/-/raw/` and fetched publicly without a token (spec author,
  2026-10-25).
- D20 — Prior MRs use the merged-MR list (30) plus one diffs page each, mirroring GitHub's 1 + 30 requests (spec
  author, 2026-10-25).
- D21 — (Q1) Comments and replies typed by the reviewer get the same body rules as finding text (`@` mentions and
  HTML are neutralised too), because the server cannot tell typed text from pasted LLM text (FR13) (user, 2026-10-25).
- D22 — (Q2) `#12`, `!12`, `group/proj#12` and commit SHAs in note bodies are not neutralised (GitHub parity; the
  links stay useful) (FR16) (user, 2026-10-25).
- D23 — (Q3) Markdown images (`![…](…)`) in note bodies are neutralised, because GitLab does not proxy images by
  default and a pixel would reveal readers' addresses (FR16(c)) (user, 2026-10-25).
- D24 — (Q4) When GitLab refuses a position, DevDigest does not fall back to a general MR note; the error asks for a
  reload (FR10, §3 Out of scope) (user, 2026-10-25).
- D25 — (Q5) `.gitlab-ci.yml` and `.gitlab/**` are `wiring` for GitLab repositories only; GitHub output is unchanged
  (FR32) (user, 2026-10-25).
- D26 — (Q6) When the MR changed since the diff was loaded, the post is refused with `diff_changed` and the UI
  reloads, instead of posting against the stored diff's version (FR7, FR20; confirms D15) (user, 2026-10-25).
- D27 — (Q7) Listing caps: GET comments reads at most 3 discussion pages and returns at most 100 comments; the reply
  search reads at most 10 pages (FR2, FR9, NFR5) (user, 2026-10-25).
- D28 — (Q8) Skill import ref splits: at most 5 attempts, shortest ref first (FR30, NFR5) (user, 2026-10-25).
- D29 — (Q9) The neutralising character is U+200B (before `/`, after `@`, inside `![`), verified live by AC34 on
  gitlab.com; if AC34 fails for the `/` rule, FR15 is revised to a leading backslash outside code regions (user,
  2026-10-25).
- D30 — The user approved the spec with every default as written and set it ready for planning; planning starts
  only after the Settings A review gate has passed (user, 2026-10-25).

## 17. Open questions
None. Q1–Q9 were answered on 2026-10-25 and are recorded as D21–D29.

## Glossary
- `{ZWSP}` — written in this spec's examples for the invisible U+200B zero-width space.
- Diff head — the head SHA of the GitLab diff version whose files are stored for a PR (FR35).
- Note body rules — the server-side rules FR13–FR17 applied to every note body sent to GitLab.
- Root note — the first note of a GitLab discussion; replies point to it with `in_reply_to_id`.
- Not connected — slice A's state of a GitLab repository whose host has no connection (A FR48).
