# Research: HTTP API inventory of `server/` (Fastify) for Go migration
Mode: repo · Scope: `server/src/{app.ts,http,modules/**,platform,vendor/shared}`, `client/src/lib/**`, `mcp/src/api.ts`, `agent-runner/src`, `e2e` · Commit: d1c9093

## Answer
The server exposes **100 module routes + 2 health routes = 102**, registered in `server/src/modules/index.ts:23-43` (19 plugins). All routes are flat (no `/api` prefix), served on `API_PORT` (default 3001, host 127.0.0.1; `platform/config.ts:60-64`). Every module uses Zod contracts from `server/src/vendor/shared/contracts/*`. The vendor copy is identical to `client/src/vendor/shared` (`diff -rq` was empty). The client calls 84 of the routes. 13 server routes are never called by the client, listed in section 2. I found no client call without a matching server route.

Note: `server/README.md` has only a partial API map (the sections per module). The tables below come from the route files themselves.

## 0. Global behaviour (`server/src/app.ts`, `server/src/http/error-handler.ts`)
- **Body limit** is 1 MiB (`app.ts:40`). Per-route overrides:
  - `POST /skills/import/preview`: base64 limit + 64 KiB (`skills/http/routes.ts:54,87`).
  - Project-context file POST and PUT: 2 MiB (`project-context/http/schemas.ts` `FILE_BODY_LIMIT`; routes.ts:91,103).
- **Validation:** Zod type provider validates params, query and body. It serializes responses through the Zod response schema (`app.ts:48-49`). The response schema strips unknown keys and a mismatch becomes 500.
- **Helmet:** default `@fastify/helmet` 13.x, no options (`app.ts:99`).
- **CORS:** `origin: [http://localhost:WEB_PORT]` (WEB_PORT defaults to 3000), `credentials: true` (`app.ts:100`, `config.ts:185`). `@fastify/cors` 10.1 default methods are `GET,HEAD,PUT,PATCH,POST,DELETE` (`node_modules/@fastify/cors/index.js:11`). There are no custom allowed or exposed headers.
- **Global rate limit:** 120 requests per minute, keyed by IP. It is not registered when `NODE_ENV=test` (`app.ts:106-108`). The plugin sends `x-ratelimit-*` headers (`@fastify/rate-limit`). The client does not read them.
- **Per-route rate limits:**

| Route | Limit | Source |
|---|---|---|
| `POST /pulls/:id/review` | 10/min | `reviews/routes.ts:48` |
| `POST /settings/test-connection` | 20/min | `settings/routes.ts:50` |
| `POST /pulls/:id/intent/refresh` | 10/min (`REFRESH_RATE_LIMIT`, `intent/domain/constants.ts:47`) | `intent/http/routes.ts:31` |
| `POST /eval-runs/run-all` | 10/min, own bucket | `evals/http/routes.ts:24,207` |
| `POST /agents/:id/eval-runs` and `POST /skills/:id/eval-runs` | one shared 10/min per workspace bucket (keyGenerator = workspaceId); the global limiter is off for them | `evals/http/routes.ts:30,58-66,142-143,166-167` |
| `GET /runs/:id/events`, `/health`, `/health/ready` | `rateLimit: false` | `reviews/routes.ts:70`, `app.ts:112,117` |

- **Auth:** none. `getContext()` (`_shared/context.ts:14`) uses `LocalNoAuthProvider` (`adapters/auth/local.ts:14`). It always returns the single seeded system user and default workspace. No headers or cookies are read. Cross-workspace access returns 404, never 403.
- **Health:**
  - `GET /health` returns 200 `{status:"ok"}` (`app.ts:112`).
  - `GET /health/ready` returns 200 `{ready:true}` after `select 1`, otherwise 503 `{ready:false}` (`app.ts:117-125`). The failure body is not in the error envelope.
  - The MCP server refers to `/health/ready` only in a hint (`mcp/src/api.ts:~58`).
- **Graceful shutdown** (`app.ts:~131`, preClose hook):
  - Cancels live review runs.
  - Stops the job runner.
  - Ends SSE streams.
  - Aborts eval runs.
- **Boot reaping** (`app.ts:60-78`): runs and eval runs left `running` are marked failed or interrupted before the server listens.

## 1. Error shape (root handler `http/error-handler.ts:56-103`)
All errors use `{ "error": { "code": string, "message": string, "details"?: unknown } }`. The client parses this at `client/src/lib/api.ts:49-54`.

| Case | Status | code / message |
|---|---|---|
| Zod request validation (params, query, body, including a non-uuid `:id`) | **422** | `validation_error`, "Request validation failed", `details` = the `err.validation` array from fastify-type-provider-zod |
| Response serialization failure, or non-request ZodError (LLM, DB or trace parse) | 500 | `internal_error`, "Internal error" |
| `AppError(kind, code, msg, details)` | `STATUS_BY_KIND` (see below) | the error's own `code`, `message`, `details` |
| Other Fastify or plugin 4xx (bad JSON, 413, 415, **429 rate limit**) | its own status | code is always the literal `internal_error`, message is the Fastify message |
| Anything else | 500 | `internal_error`, "Internal error" (raw message never echoed) |
| Unknown route | **404** | `not_found`, `Route ${METHOD} ${path-without-query} not found` (`error-handler.ts:98-102`) |

`STATUS_BY_KIND` (`error-handler.ts:30-41`):

| kind | status |
|---|---|
| `invalid_input` | 400 |
| `unauthorized` | 401 |
| `forbidden` | 403 |
| `not_found` | 404 |
| `conflict` | 409 |
| `payload_too_large` | 413 |
| `validation` | 422 |
| `internal` | 500 |
| `config` | 500 |
| `external_service` | 502 |

An `AppError.statusCode` overrides the table. `AppError.details` can be `undefined`, so the JSON omits it.

Two quirks the Go server must reproduce:
- A 4xx from the Fastify layer, such as a 429, uses code `internal_error`.
- The domain error codes below (`stale_version`, `invalid_run_request` and so on) are mostly defined in the services, not the route files. They are listed in the README per module. I did not enumerate all `AppError` throw sites.

## 2. Routes by module (method, path, request, response, status)
`IdParams = {id: uuid}` (`_shared/schemas.js:11`). A non-uuid id gives 422. Exceptions are `/providers/:id` and the `:version` param. Responses are 200 unless stated.

### settings (4) — `modules/settings/routes.ts`
| Route | Line | Request / response |
|---|---|---|
| GET `/settings` | 27 | resp `record<string,unknown>` (raw stored key/value bag, not the `Settings` contract) |
| GET `/settings/secrets-status` | 32 | `SecretsStatus` |
| PUT `/settings` | 37 | body `SettingsUpdate` (= `Settings.partial()`), resp record |
| POST `/settings/test-connection` | 46 | body `ConnTestRequest {provider: openai\|anthropic\|openrouter\|github, key?}`, resp `ConnTestResult {provider, ok, message, detail?}`, 20/min |

### repos (4) — `modules/repos/routes.ts`
| Route | Line | Request / response |
|---|---|---|
| POST `/repos` | 23 | body `RepoInput {url}`, resp `Repo`; **201 if created, 200 if it already existed** |
| GET `/repos` | 34 | `Repo[]` |
| POST `/repos/:id/refresh` | 39 | `{status:"refreshing"}`; the clone runs as a background job |
| DELETE `/repos/:id` | 48 | `{deleted:<id>}` |

### pulls (4) — `modules/pulls/routes.ts`
| Route | Line | Request / response |
|---|---|---|
| GET `/repos/:id/pulls` | 20 | `PrMeta[]` (synced from GitHub, with score, cost, finding rollups) |
| GET `/pulls/:id` | 29 | `PrDetail` |
| GET `/pulls/:id/comments` | 34 | `PrReviewComment[]` (live proxy to GitHub) |
| POST `/pulls/:id/comments` | 43 | body `PrCommentInput`, resp `PrReviewComment` (200, not 201) |

### polling (1) — `modules/polling/routes.ts`
- POST `/repos/:id/poll` (line 18) returns `{synced:int, reviewTriggered:bool}` (camelCase).

### workspace (1) — `modules/workspace/routes.ts`
- GET `/workspace` (line 28) returns `{workspaceId, cloneDir, repos:[{id, full_name, clone_path, last_polled_at, cloned}]}`. Casing is mixed.

### agents (11) — `modules/agents/routes.ts`
| Route | Line | Request / response |
|---|---|---|
| GET `/agents` | 61 | `Agent[]` |
| GET `/agents/:id` | 66 | `Agent`; 404 "Agent not found" |
| POST `/agents` | 73 | body `CreateAgentInput`, resp **201** `Agent` |
| PUT `/agents/:id` | 84 | body `CreateAgentInput.partial()`, resp `Agent` (versions the config) |
| DELETE `/agents/:id` | 95 | `{ok:true}` |
| GET `/agents/:id/versions` | 106 | `AgentVersion[]` |
| GET `/agents/:id/versions/:version` | 117 | `AgentVersion`; `version` is `z.coerce` positive int |
| GET `/agents/:id/skills` | 128 | `AgentSkillLink[]` |
| POST `/agents/:id/skills` | 139 | body is `{skill_ids?:uuid[], skill_id?:uuid, order?:int>=0}`. It needs one of `skill_ids` or `skill_id`, and `skill_ids` must have no duplicates. Resp `AgentSkillLink[]` |
| GET `/agents/:id/models` | 154 | `ModelInfo[]` |
| GET `/providers/:id/models` | 165 | `ModelInfo[]`; `:id` is the `Provider` enum, not a uuid |

### reviews (15) — `modules/reviews/routes.ts`, schemas in `reviews/http/schemas.ts`
| Route | Line | Request / response |
|---|---|---|
| POST `/pulls/:id/review` | 44 | Body `RunRequest` extended with `agentIds?`. A null or empty body is treated as `{}` (schemas.ts:22). Must be exactly one of `{agentId}`, `{all:true}`, `{agentIds}`, otherwise **400 `invalid_run_request`** (README:114). Unknown agent gives 404. Resp `ReviewRunResponse {pr_id, multi_agent_run_id?, runs:[{run_id, agent_id, agent_name}], reviews:ReviewRecord[]}`. 10/min |
| GET `/runs/:id/events` | 68 | **SSE**, see section 3 |
| GET `/pulls/:id/runs/active` | 82 | `ActiveRun[]` |
| GET `/pulls/:id/runs` | 91 | `RunSummary[]` |
| DELETE `/runs/:id` | 100 | `{ok:boolean}` (also deletes the review and trace) |
| POST `/runs/:id/cancel` | 105 | `{ok:true}` |
| GET `/runs/:id/trace` | 111 | `RunTrace`; 404 "Run trace not found"; 500 if the stored jsonb fails the schema |
| GET `/pulls/:id/reviews` | 120 | `ReviewRecord[]` |
| DELETE `/reviews/:id` | 129 | `{ok:true}` |
| POST `/findings/:id/accept` and `/findings/:id/dismiss` | 135-144 | Resp `{finding: FindingRecord}`. There is no body schema. The client sends `{reply}` (`client/src/lib/hooks/reviews.ts:187`), which the server ignores |
| GET `/multi-agent-runs/:id` | 146 | `MultiAgentRun`; 404 `multi_agent_run_not_found` |
| GET `/repos/:id/multi-agent-runs?limit=` | 155 | `MultiAgentRunSummary[]`; `limit` is coerced int, range 1-20 is checked by the service, **400 `invalid_limit`** |
| GET `/runs/estimates` | 164 | `AgentRunEstimate[]` |
| POST `/findings/:id/learn` | 169 | `{finding, memoryId}` (camelCase `memoryId`; idempotent) |

Route-order note: `/runs/estimates` is registered after `/runs/:id/*`. These do not collide, because `/runs/:id` only has DELETE and the other `:id` routes have a further segment.

### repo-intel (2) — `modules/repo-intel/routes.ts`
| Route | Line | Request / response |
|---|---|---|
| GET `/repos/:id/index-state` | 30 | `RepoIndexState`, with `updatedAt` as an ISO string (the facade returns a Date) |
| POST `/repos/:id/resync` | 42 | **202** `{status:"accepted", jobId}` or `{status:"accepted", degraded:true, reason:"no_handler"}`. It is 202 even if the enqueue fails |

### skills (13) — `modules/skills/http/routes.ts`
All of these are 200 except where noted. `days` is `coerce int 1..STATS_MAX_DAYS`, default `STATS_DEFAULT_DAYS` (README says 30 and 1-365).

| Route | Line | Request / response |
|---|---|---|
| GET `/skills` | 62 | `Skill[]` |
| GET `/skills/stats?days=` | 67 | `SkillStatsSummary[]` |
| GET `/skills/community?q=&tag=&lang=` | 76 | `CommunitySkill[]` |
| POST `/skills/import/preview` | 85 | Body `SkillImportRequest` (discriminated by `kind`, e.g. `{kind:"url",url}`, `knowledge.ts:193`). The README says the upload is a **base64 `.md` or `.zip` inside the JSON body**, not multipart. Resp `SkillImportPreview`; 422 `invalid_import` |
| GET `/skills/:id` | 94 | `Skill` |
| POST `/skills` | 101 | body `CreateSkillInput`, **201** `Skill` |
| PUT `/skills/:id` | 108 | body `UpdateSkillInput`, resp `Skill`; 409 `stale_version` / `conflict` |
| DELETE `/skills/:id` | 119 | `{ok:true}` |
| GET `/skills/:id/versions` | 125 | `SkillVersion[]` |
| GET `/skills/:id/versions/:version` | 136 | `SkillVersion` |
| POST `/skills/:id/versions/:version/restore` | 147 | `Skill` (writes the old text as a new version) |
| GET `/skills/:id/agents` | 158 | `SkillAgentRef[]` |
| GET `/skills/:id/stats?days=` | 169 | `SkillStats` |

Route-order note: `/skills/stats`, `/skills/community` and `/skills/import/preview` are static segments that must win over `/skills/:id`.

### conventions (4) — `modules/conventions/http/routes.ts`
| Route | Line | Request / response |
|---|---|---|
| GET `/repos/:id/conventions` | 26 | `ConventionsState` |
| POST `/repos/:id/conventions/extract` | 35 | **202** `ConventionScan` (background job `conventions.extract`); 409 `scan_running`, 422 `not_cloned` |
| PATCH `/conventions/:id` | 45 | body `UpdateConventionInput`, resp `Convention` |
| POST `/repos/:id/conventions/skill` | 54 | body `CreateConventionSkillInput`, **201** `CreateConventionSkillResult {skill, linked_agents}` |

### evals (16) — `modules/evals/http/routes.ts` (param schemas are all `IdParams`)
| Route | Line | Request / response |
|---|---|---|
| POST `/findings/:id/eval-case` | 68 | **201** `AgentEvalCase`; 409 `eval_case_exists` (+`case_id`); 422 `finding_undecided` / `finding_no_agent` / `patch_unavailable` / `patch_too_large` / `eval_case_limit` |
| GET `/agents/:id/eval-cases` | 79 | `{cases}` (`AgentEvalCaseList`) |
| POST `/agents/:id/eval-cases` | 88 | body `EvalCaseCreateInput`, **201** `AgentEvalCase` |
| GET `/skills/:id/eval-cases` | 101 | `{cases}` |
| POST `/skills/:id/eval-cases` | 110 | body `EvalCaseCreateInput`, **201** |
| PATCH `/eval-cases/:id` | 123 | body `AgentEvalCaseUpdateInput`, resp `AgentEvalCase`; 400 `validation_error` when `end_line < start_line` |
| DELETE `/eval-cases/:id` | 132 | **204**, empty body |
| POST `/agents/:id/eval-runs` | 138 | **202** `EvalRunSummary`; 409 `eval_run_active`, 422 `no_eval_cases`, 429 |
| GET `/agents/:id/eval-runs?limit=&period=` | 153 | `{runs}` (`EvalRunListQuery`; limit 1-100, default 20; period `7d`\|`30d`\|`90d`\|`all`) |
| POST `/skills/:id/eval-runs` | 162 | body `EvalSkillRunStartInput {agent_id}`, **202** `EvalRunSummary` |
| GET `/skills/:id/eval-runs?limit=` | 177 | `{runs}` |
| GET `/eval-runs/compare?a=&b=` | 187 | `EvalCompare`; 422 `runs_not_comparable` (static segment before `/eval-runs/:id`) |
| GET `/eval-runs/:id` | 196 | `EvalRunDetail {run, results}` |
| POST `/eval-runs/run-all` | 205 | **202** `{started, skipped}` (`EvalRunAllResponse`) |
| GET `/eval/dashboard` | 216 | `EvalOverview` |
| GET `/agents/:id/eval/dashboard?period=` | 221 | `EvalAgentDashboard` |

Eval runs execute in the background: 3 cases at a time, 120 s per case (README:186).

### ci (4) — `modules/ci/http/routes.ts` (schemas in `ci/http/schemas.ts`)
| Route | Line | Request / response |
|---|---|---|
| POST `/agents/:id/export-ci` | 22 | Body `CiExportBody` (extends `CiExportInput`, has `action: files\|open_pr`). Resp `CiExport`, **200 for `files`, 201 for `open_pr`**. Errors: 404 `agent_not_found` / `repo_not_found`, 409 `ci_slug_conflict`, 422 `workflow_insecure` (`details.rule`) / `github_permission_denied` / `unsupported_*` / `invalid_triggers` / `skill_slug_conflict`, 502 `github_unavailable`, 500 `runner_bundle_missing` |
| GET `/agents/:id/ci-installations` | 33 | `{installations}` |
| GET `/ci/runs?agent_id=&limit=` | 42 | `{runs}`; limit 1-200, default 100 |
| POST `/ci/refresh` | 51 | Body `{agent_id?:uuid}`; null body is treated as `{}`. Resp `CiRefreshResult` |

### Single-feature modules (read or generate)
| Module (count) | Route | Line | Request / response |
|---|---|---|---|
| brief (2) | GET `/pulls/:id/brief` | `brief/http/routes.ts:20` | `PrBriefResponse {brief\|null, stale}` |
| brief | POST `/pulls/:id/brief` | :29 | No body. One LLM call. Resp `{brief, stale:false}`; 409 `generation_in_progress`, 422 `no_changed_files` / `provider_not_configured`, 502 `generation_failed` |
| intent (2) | GET `/pulls/:id/intent` | `intent/http/routes.ts:18` | `PrIntentResponse {intent\|null, stale}` |
| intent | POST `/pulls/:id/intent/refresh` | :27 | 10/min, `PrIntentResponse` |
| smart-diff (1) | GET `/pulls/:id/smart-diff` | `smart-diff/http/routes.ts:16` | `SmartDiff` |
| blast (2) | GET `/pulls/:id/blast` | `blast/http/routes.ts:18` | `BlastRadius` (degraded flag) |
| blast | GET `/pulls/:id/history` | :27 | `PrHistory` (`[]` if GitHub is unavailable) |
| onboarding (2) | GET `/repos/:id/onboarding` | `onboarding/http/routes.ts:20` | `{status:"none"}` or `{status:"ready", stale, stale_reason?, tour}` |
| onboarding | POST `/repos/:id/onboarding` | :29 | Synchronous, up to 120 s. Resp `OnboardingTourReady`; 404 `repo_not_found`, 409 `generation_in_progress`, 422 `index_not_ready` / `provider_not_configured`, 502 `generation_failed` |
| performance (2) | GET `/agents/performance?period=&from=&to=` | `performance/http/routes.ts:19` | `AgentPerf`. `PeriodQuery` has period `1d`\|`30d`(default)\|`custom`; custom uses ISO instants with offset, `[from,to)`, up to 366 days; else 422 |
| performance | GET `/agents/:id/stats?period=&from=&to=` | :28 | `AgentStats`; 404 for an unknown agent |

Route-order note: `/agents/performance` is a static segment that must win over `/agents/:id`, and `/agents/:id/stats` sits beside it.

### project-context (10) — `modules/project-context/http/routes.ts`
Schemas are in `project-context/http/schemas.ts`.

| Route | Line | Request / response |
|---|---|---|
| GET `/repos/:id/context` | 35 | `ContextList` (`ContextListResponse`) |
| GET `/repos/:id/context/doc?path=` | 44 | `ContextDocPreview`. `path` is a string up to 4096 chars. 400 `invalid_path`, 404 `repo_not_found` / `doc_not_found`, 413 `doc_too_large` (over 256 KB) |
| GET `/agents/:id/context?repoId=` | 53 | `ContextAttachments`; `repoId` must be a uuid |
| PUT `/agents/:id/context` | 62 | body `{repo_id:uuid, ...ContextAttachmentsInput}`, resp `ContextAttachments` |
| GET `/skills/:id/context?repoId=` | 71 | same as the agent GET |
| PUT `/skills/:id/context` | 80 | same as the agent PUT |
| POST `/repos/:id/context/files` | 89 | Body `ContextFileCreateInput`, optional (null becomes `{}`), 2 MiB limit. **201** `ContextDocPreview`; 404, 409 `path_exists`, 413 `doc_too_large`, 422 `invalid_path` / `invalid_content` / `too_many_files` |
| PUT `/repos/:id/context/files?path=` | 100 | body `ContextFileSaveInput {content, base_version}`, 2 MiB limit, resp `ContextDocPreview`; 403 `read_only`, 404 `doc_not_found`, 409 `stale_version` (`details.current_version`), 413 |
| POST `/repos/:id/context/files/rename` | 112 | body `ContextFileRenameInput {path, new_path, base_version}`, resp `ContextDocPreview`; 403, 404, 409, 422 |
| DELETE `/repos/:id/context/files?path=` | 121 | **204**; 403 `read_only`, 404 |

### Count per module
settings 4 · repos 4 · pulls 4 · polling 1 · workspace 1 · agents 11 · reviews 15 · repo-intel 2 · skills 13 · conventions 4 · intent 2 · smart-diff 1 · blast 2 · project-context 10 · onboarding 2 · brief 2 · evals 16 · ci 4 · performance 2 = **100**, plus `/health` and `/health/ready` in `app.ts` = **102**.

## 3. Streaming (SSE): `GET /runs/:id/events` (`reviews/routes.ts:68-80`)
- Plugin: `fastify-sse-v2` 4.2.2. The handler calls `reply.sse(asyncIterator)`.
- Each message is `id: <seq>`, `event: <kind>`, `data: <JSON RunEvent>`.
  - `kind` is one of `info`, `tool`, `result`, `error` (`contracts/trace.ts:11`).
  - The `data` JSON is `{runId, seq:int, kind, msg, t:"HH:MM:SS" (local wall-clock), data?:unknown}` (`trace.ts:23`, `platform/sse.ts:~20`).
- The buffer is replayed first, then live events follow. The stream ends when the run completes (`bus.onDone`). The bus keeps a completed run's buffer for 5 minutes (`RUN_STATE_TTL_MS`).
- If the run is not on the bus and its status is not `running`, the stream is one synthetic event (`reviews/domain/run.ts:32`). It has `seq:1`, `kind` `result` if done (else `info`), and `data:{status,error}`.
- An unknown run, or one from another workspace, gets a **JSON 404 before the stream starts** (`review-service.ts:168-186`).
- The terminal marker the client relies on is `data.status` in `{done, failed, cancelled}` (`client/src/lib/hooks/run-events-store.ts:35,42`).
- The client closes on a terminal status or on `onerror`, so that EventSource does not reconnect (:124-126). The client listens to the named events `info`, `tool`, `result`, `error` (:37,123).
- Details not found in source:
  - Heartbeat or keepalive comments. The client tolerates unparseable frames (:109).
  - Exact `Content-Type` and no-cache headers set by the plugin.

No other streaming, uploads (multipart) or file-download endpoints exist.

## 4. Client mapping
The client wrapper is `client/src/lib/api.ts`.
- `API_BASE = NEXT_PUBLIC_API_BASE ?? http://localhost:3001`.
- `content-type: application/json` is sent only when a body exists (api.ts:30). Body-less POSTs have no content-type. The server must accept POST/PUT with no body and no content-type.
- A falsy body is sent as nothing (`body ? JSON.stringify : undefined`).
- 204 returns undefined.
- Error handling reads `body.error.{code,message,details}`.
- The network-failure error code `network_error` is synthesized in the client.

| Hook file (`client/src/lib/hooks/`) | Routes called |
|---|---|
| core.ts | GET and PUT `/settings`, GET `/settings/secrets-status`, POST `/settings/test-connection`, GET and POST `/repos`, POST `/repos/:id/refresh`, DELETE `/repos/:id`, GET `/repos/:id/pulls`, GET `/pulls/:id` |
| agents.ts | GET `/agents`, GET `/agents/:id`, POST `/agents`, PUT `/agents/:id`, DELETE `/agents/:id`, GET `/providers/:provider/models` |
| skills.ts | all 13 skills routes, plus GET and POST `/agents/:id/skills` (POST only with `skill_ids`) |
| reviews.ts | POST `/pulls/:id/review`, GET `/pulls/:id/runs/active`, `/runs`, `/reviews`, DELETE `/runs/:id`, POST `/runs/:id/cancel`, DELETE `/reviews/:id`, GET and POST `/pulls/:id/comments`, POST `/findings/:id/(accept\|dismiss)`, SSE `/runs/:id/events` |
| trace.ts | GET `/runs/:id/trace` |
| multi-agent.ts | GET `/multi-agent-runs/:id`, GET `/repos/:id/multi-agent-runs?limit=`, GET `/runs/estimates`, POST `/findings/:id/(accept\|dismiss\|learn)` |
| conventions.ts | the 4 conventions routes |
| evals.ts | all 16 evals routes |
| ci.ts | the 4 ci routes |
| intent.ts, brief.ts, smart-diff.ts, blast.ts, onboarding.ts, performance.ts, repo-intel.ts | their respective routes. `repo-intel.ts` and `blast.ts` both call POST `/repos/:id/resync`; `repo-intel.ts` also calls GET `/repos/:id/index-state` |
| project-context.ts | all 10 project-context routes |

**Server routes the client never calls (13):**
| Route | Other caller |
|---|---|
| GET `/health`, GET `/health/ready` | none in code (the MCP error hint mentions `/health/ready`); infra only |
| GET `/workspace` | none found |
| POST `/repos/:id/poll` | none found (README says it never triggers a review) |
| GET `/agents/:id/versions`, GET `/agents/:id/versions/:version` | none found |
| GET `/agents/:id/models` | none found (the client uses `/providers/:p/models`) |
| POST `/agents/:id/skills` with the single `skill_id` + `order` variant | none found. The path is called, but only with `skill_ids` |
| POST `/findings/:id/learn` | used only by the multi-agent page (`multi-agent.ts`) |
| MCP-only reads: GET `/pulls/:id/runs`, `/pulls/:id/reviews`, `/repos/:id/conventions`, `/pulls/:id/blast`, `/agents`, `/repos`, `/repos/:id/pulls`, POST `/pulls/:id/review` with `{agentId}`, POST `/runs/:id/cancel` | These are all also called by the client. They are listed because `mcp/src/api.ts:72-87` pins the wire shapes (it checks required keys of the response). |

The 13 count is the first seven rows (health ×2, workspace, poll, versions ×2, models) plus the unused single-skill link variant plus the learn route. Treat the last two as "partially used" rather than fully unused. The `learn` route is called, so strictly the never-called set is 7 routes plus the unused single-skill variant. `agent-runner` does not call this API (it only calls api.github.com, `agent-runner/src/github.ts:16`). `e2e` has no direct route literals under `e2e/*.ts` (browser flows; not exhaustively checked).

**Client calls with no server route:** none found.

## 5. Cross-cutting wire conventions (for the Go server)
- **JSON casing:** snake_case dominates (`workspace_id`, `full_name`, `head_sha`, `files_count`, `created_at`, and so on). Hand-written route schemas break the pattern with camelCase. Go struct tags must match each field exactly. The camelCase fields are:
  - `workspaceId`, `cloneDir` (`/workspace`)
  - `reviewTriggered` (`/poll`)
  - `memoryId` (`/learn`)
  - `jobId` (`/resync`)
  - `updatedAt` (`/index-state`)
  - Request bodies: `agentId`, `agentIds`, `all` on `/review`; query `repoId` on the context GETs.
  - SSE `runId`.
- **IDs:** strings. Path ids are validated as uuid (`z.string().uuid()`); a bad one gives 422 `validation_error`. The contracts type most ids as plain `z.string()`.
- **Dates:** ISO-8601 strings produced with `toISOString()` (UTC, `Z`), for example `created_at`, `accepted_at` (`reviews/repository/mappers.ts:27,49`). Contracts use `z.string()` rather than `z.date()`. Nullable dates are `null`. The SSE `t` is local time `HH:MM:SS`. The performance `from` and `to` need an ISO instant with offset.
- **Pagination:** none (no cursors, offsets or envelopes). Only `?limit=`:
  - multi-agent runs: 1-20
  - eval runs: 1-100
  - ci runs: 1-200
  - A few lists are capped inside the response, for example `truncated` on `ContextList` at 500 files.
- **Response envelopes:** mostly bare arrays or objects. The wrapped ones are `{cases}`, `{runs}`, `{installations}`, `{finding}`. `{ok:true}` is used for deletes of agents, skills, runs and reviews. Repos delete returns `{deleted:id}`. Eval-case and context-file deletes are 204.
- **Nullable semantics:** the contracts use `.nullish()` widely (for example `PrMeta.id`, `score`, `opened_at`), meaning absent or null. The Go side should emit null or omit as the contract allows.
- **Status codes quirks:** POST `/repos` is 200 or 201, `export-ci` is 200 or 201, and the 202 routes are `resync`, `conventions/extract`, `eval-runs` ×2 and `run-all`. `POST /pulls/:id/comments` and `/review` return 200.
- **Settings:** GET and PUT `/settings` return the raw stored key/value bag with no defaults injected (`settings/routes.ts:8-13`).
- **Request body handling:** an empty body on a route with an optional body is treated as `{}`. This applies to `/review`, `/ci/refresh` and `POST /repos/:id/context/files`. Other POST routes declare no body schema and ignore any body.

## 6. Suggested migration order (by dependency)
1. **Platform:**
   - Router, error envelope, 404 handler, 422 validation, body limits.
   - helmet-equivalent headers, CORS, rate limiter (120/min plus per-route), `/health`, `/health/ready`.
   - Local single-tenant context.
2. **settings (4) → repos (4) → polling (1), workspace (1), pulls (4):** the base entities. `repos` needs a git clone job and the GitHub adapter. `pulls` needs the GitHub sync.
3. **agents (11) → skills (13)** (agents link skills, skills list `used_by`). Add **conventions (4)** (creates skills and links agents) and **project-context (10)** (attachments for agents and skills, depends on repos).
4. **repo-intel (2)** (index and parse). It is needed by blast, onboarding and review prompt context.
5. **reviews (15)**, the core. It depends on settings (keys), repos, pulls, agents, skills, project-context and intent pre-work. It also needs the SSE bus, run executor, job runner, LLM adapters, usage metering and findings grounding. The multi-agent routes live here.
6. **intent (2), smart-diff (1), blast (2), brief (2), onboarding (2):** read or LLM features on top of pulls, reviews and repo-intel.
7. **performance (2):** read-only aggregation over runs, after reviews has data.
8. **evals (16)** and **ci (4):** these depend on agents, skills and review runs, and they need the LLM runner plus GitHub PR creation.

## Not found / unclear
- The exact `details` body for 422: it is `err.validation` from `fastify-type-provider-zod`. I did not inspect the package to document its item fields (`keyword`, `instancePath`, `params`, `message`).
- The full set of `AppError` codes thrown from services: I only collected the ones in the route comments and in the README.
- Field-level schemas of each contract (`Agent`, `Skill`, `RunTrace`, and so on): contracts are in `server/src/vendor/shared/contracts/{platform,knowledge,review-api,findings,trace,observability,brief,eval-ci,eval-pipeline,productionize,project-context,onboarding,why}.ts` and were only inspected for `Repo`, `PrMeta`, `RunEvent`, `ReviewRunResponse` and `ConnTestRequest`.
- Whether `fastify-sse-v2` sends keepalive pings and the SSE response headers.
- The server's behaviour if `reply` is sent to the finding-accept routes: not applicable beyond "no body schema". I did not check whether the service reads the raw body.
- No direct route usage was found in `e2e/`. The folder contains browser flows, so literal API calls may exist in non-TS files, which I did not scan.
- `docs/` and `specs/` of the server were not read for extra HTTP contracts; the README table and the route files were taken as authoritative.
