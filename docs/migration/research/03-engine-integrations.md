# Research: non-HTTP machinery to port from Node `server/` + `reviewer-core/` to Go, and which Go libraries to use (Echo + GORM)
Mode: both · Scope: `server/src`, `reviewer-core/src`, `agent-runner/`, `docs/` · Commit: d1c9093 · Date: 2026-10-21
All paths are relative to `<dev-digest>`. Part 2 versions come from pkg.go.dev and echo.labstack.com fetched today. I did not compile anything.

---
# PART 1 — Repo inventory

## Summary
Most of this is simple glue, and the review core is small and pure. Four items are hard to port.
- **The runner.** `agent-runner` is a Node bundle that the server ships as text. It must stay Node unless you rewrite it, and it consumes `reviewer-core` as TS source.
- **AST indexing.** Today it uses `@ast-grep/napi` (tree-sitter) on TS/JS only.
- **Import-graph resolution.** It uses `dependency-cruiser` with the repo's tsconfig, and nothing in Go replaces it.
- **Zod-to-JSON-Schema.** The strict structured-output schemas are derived from Zod.

## Port map (rating = hard-to-port)

| Area | Where | Rating | Why |
|---|---|---|---|
| Review pipeline (strategy select, chunking, reduce, score, grounding, scope) | `reviewer-core/src/review/{run,split,reduce,pool,scope}.ts`, `grounding.ts` | **low** | Pure functions, no I/O. |
| Prompt assembly | `reviewer-core/src/prompt.ts:257-367` | **low** | String concat, plus a regex that neutralises `<untrusted>` tags. |
| Structured output (Zod to JSON Schema, parse-with-repair) | `reviewer-core/src/llm/structured.ts:1-89`, `server/src/vendor/shared/contracts/findings.ts:47-100` | **med** | Zod is the source of truth and Go has no equivalent. |
| LLM providers (OpenAI, Anthropic, OpenRouter) | `server/src/adapters/llm/*.ts`, `reviewer-core/src/llm/openrouter.ts` | **med** | Three near-identical retry loops plus a call budget. |
| Cost, pricing, PriceBook | `adapters/llm/pricing.ts:16-59`, `platform/price-book.ts:80-132` | **low** | A static table and a TTL cache. |
| GitHub (octokit) | `adapters/github/octokit.ts` (593 lines) | **low-med** | Plain REST. |
| Git (simple-git) | `adapters/git/simple-git.ts` (336 lines) | **low** | Shell out to `git`. |
| repo-intel: ast-grep symbols | `adapters/astgrep/index.ts` (639 lines) | **high** | 639 lines of node-kind logic tied to tree-sitter TS/TSX/JS. |
| repo-intel: import graph | `adapters/depgraph/index.ts:55-106` | **high** | Resolution is done by dependency-cruiser. |
| repo-intel: PageRank, repo map, tokenizer | `domain/rank.ts`, `domain/repo-map.ts`, `adapters/tokenizer/index.ts` | **low-med** | PageRank has to match graphology numerically. |
| ripgrep | `adapters/codeindex/ripgrep.ts` | **low** | A pure-Node fallback already exists. |
| Jobs, SSE run bus, shutdown | `platform/{jobs,sse,run-logger}.ts`, `app.ts:76-135` | **med** | Idiomatic in Go (goroutines/channels). |
| Export to CI + agent-runner | `modules/ci/**`, `agent-runner/` | **med to high** | See section 6. |
| Secrets, settings, rate limit | `adapters/secrets/local.ts`, `platform/config.ts` | **low** | Rate limits are per route. |
| DB (drizzle, pgvector) | `db/schema/*.ts` (~58 tables, 26 jsonb columns), `db/migrations/0000..0020` | **med** | Needs GORM models plus the migration decision. |

## 1. reviewer-core pipeline

**Flow.** `reviewPullRequest` (`reviewer-core/src/review/run.ts:195-324`) runs these steps:
1. Pick a mode.
2. Build per-file chunks and apply the max-diff-chars guard.
3. Run the chunks through `mapOrdered`, which bounds concurrency and keeps results in item order.
4. Reduce the partial reviews.
5. Run the grounding gate.
6. Apply the scope policy.
7. Recompute the score from the surviving findings.

**Strategy and constants.**
- `selectMode` (`run.ts:187-193`):
  - `auto` picks map-reduce only when total changed lines exceed 400 AND there is more than one file.
  - `map-reduce` falls back to single-pass for a one-file diff.
  - A single-pass diff that gets split into several chunks is relabelled map-reduce (`run.ts:231`).
- Defaults (`run.ts:41-52`): threshold 400 lines, 2 schema-reprompt retries, 3 chunks in parallel, 200,000 characters per chunk, temperature 0.
- Port the 400-line threshold (`run.ts:41`) as-is. The server comment says it "matches FILE_MAP_THRESHOLD_LINES"; I did not locate that constant.

**Chunking** (`review/split.ts:55-83`).
- It splits at `diff --git` and `@@` boundaries.
- Each part re-carries its file header.
- One oversize hunk is truncated, and the truncation note is appended after the cut.
- `reduce.ts:64-87` parses `diff --git` headers, including git's quoted-path form.
- `sliceDiff` matches the header path exactly, not by substring.

**Scoring** (`reduce.ts:13-30`).
- Score = 100 minus a penalty per finding: CRITICAL 35, WARNING 12, SUGGESTION 3. It is clamped to 0..100.
- Reduce (`reduce.ts:43-55`) takes the worst verdict (request_changes > comment > approve), the rounded mean score, and the joined summaries.
- The final score is recomputed from the grounded findings (`run.ts:313`), not taken from the model.

**Grounding** (`grounding.ts:132-208`).
- A finding survives only if its file is in the diff and its `[start,end]` range intersects a hunk's new-side line set.
- The kinds `secret_leak`, `lethal_trifecta`, `phantom` and `hook` only need the file to be in the diff.
- It iterates the hunk lines, not the model-supplied range, so a huge `end_line` cannot cause a CPU hang. Keep that property.

**Token estimation.**
- `estimateTokens = ceil(chars/4)` (`llm/usage.ts:26`) is used everywhere: prompt section metadata, usage fallback and skill budgets.
- Real tiktoken (`cl100k_base`) is used in exactly one place: the repo-map budget (`adapters/tokenizer/index.ts:285-300`).
- For tiktoken-go, vendor the BPE ranks or accept a network download at runtime. I did not verify what tiktoken-go does about the BPE data.

**Prompts.**
- The shared injection guard (`prompt.ts:17-31`), the `INTENT_SCOPE_RULE` and `PROJECT_CONTEXT_RULE` are TS string constants. They can be copied verbatim into `.txt` or `go:embed` files. The regex in `prompt.ts:35` has `/gi` flags and `[^>]*`, which RE2 handles.
- Built-in agent system prompts are in `server/src/db/seed-prompts.ts` (390 lines, TS template strings with escaped backticks). They are mirrored in `docs/agent-prompts/*.md`.
  - The seed header says "The DB row is the source of truth at run time" (`seed-prompts.ts:6-8`). The `.md` files are the reusable verbatim copies for a Go seed.
  - The seed prompts mention Fastify, Drizzle and octokit in their stack context. Update that text if the Go service is also the reviewed stack.
- `server/src/prompts/onboarding.system.md` (44 lines) exists with `{{sections}}` templating, but nothing in the server TS code references it by name (grep found no match). The onboarding prompt is built in `modules/onboarding/domain/prompt.ts`. Treat the `.md` as possibly stale (unverified).
- Other prompt builders: `modules/brief/domain/prompt.ts`, `modules/onboarding/domain/prompt.ts`, `modules/reviews/domain/prompt.ts`, `modules/reviews/application/prompt-context.ts`.

**Structured-output schema (the hard bit).**
- `toJsonSchema` (`structured.ts:210-213`) calls OpenAI's `zodResponseFormat`, which produces a strict draft-07 schema.
- The same schema feeds both OpenAI `response_format` and Anthropic forced tool-use `input_schema`.
- The Go port needs hand-written or struct-generated JSON Schemas for every LLM-returned contract (Review, Intent, Brief, Onboarding, conventions and others), under strict-mode rules (`additionalProperties:false`, all properties required).
- `parseWithRepair` (`structured.ts:245-275`):
  1. Try `JSON.parse`.
  2. Fall back to fenced/brace extraction.
  3. Validate with Zod.
  4. On failure, build a reprompt message from the issues.
- The Zod validation must be re-implemented with Go validators.
- The Go port must reproduce the reprompt text, or output quality will shift.

## 2. LLM providers

**Usage.**
- **OpenAI** (`adapters/llm/openai.ts`):
  - Chat completions with `response_format:{type:'json_schema', strict:true}` (`:128-131`). No streaming; every call is non-streaming.
  - Reasoning models (`^(gpt-5|o1|o3|o4)`) get no `temperature` and use `max_completion_tokens` (`:31-47`).
  - `listModels` filters on gpt/o1/o3. Embeddings use `text-embedding-3-small`, 1536 dims (`:24,170-179`).
- **Anthropic** (`anthropic.ts`):
  - Forced tool use: a single tool whose `input_schema` is the JSON Schema, with `tool_choice:{type:'tool',name}` (`:138-145`).
  - The `tool_use.input` is stringified and then re-parsed.
  - `max_tokens` defaults to 4096, temperature 0. The system messages are joined (`:29-41`).
  - There are no embeddings (`:186`).
- **OpenRouter** (`reviewer-core/src/llm/openrouter.ts`):
  - It is the OpenAI SDK pointed at `https://openrouter.ai/api/v1`.
  - It sends `session_id` and `usage:{include:true}` as extra body fields (`:121-126`).
  - It reads the real cost from `usage.cost`, falling back to the injected estimator (`:201-203`).
  - It guards against HTTP 200 with no `choices` (`:136-152`).
  - `/models` is fetched raw, with per-token prices converted to USD per 1M. A negative price is treated as unknown (`:220-255`).
- **Reasoning-model detection** (the temperature rules) is duplicated in `llm/model-params.ts:1-16` and `openai.ts:31`. The two regexes differ slightly: the OpenRouter one strips the `vendor/` prefix.

**Retries, timeouts, budgets.**
- Per-request timeout 180 s (`openrouter.ts:32`).
- A wall-clock call budget of 360 s covers SDK retries AND schema reprompts (`:34`, `llm/budget.ts:145-175`). Translate this to a `context.WithTimeout` per structured call.
- Default `maxRetries` is 2 for both SDK transport and schema reprompts.
- `withRetry` (`platform/resilience.ts:227-246`) retries 429, 5xx and ECONNRESET/ETIMEDOUT/ENOTFOUND with base 250 ms, cap 8 s, plus jitter. It is used for listModels, embeddings, GitHub calls and jobs.
- A response with no `usage` has its tokens estimated and a warning raised, not booked as 0.
- `onUsage` is emitted per attempt before parsing, so spend is accounted even when the call finally fails.
- Cost is null-propagating: one unpriced attempt makes the run's total null (`usage.ts:22-24`).

**Cost.**
- A static table in `adapters/llm/pricing.ts` (USD per 1M tokens) lists OpenAI, Anthropic and OpenRouter models.
- The PriceBook (`platform/price-book.ts`) caches live OpenRouter prices for 6 hours, refreshes lazily in the background, and falls back to the static table.

**Embeddings and pgvector.**
- The columns are `vector(1536)` in `db/schema/context.ts:46` and `db/schema/knowledge.ts:36`.
- HNSW cosine indexes are created in `0011_integrity_indexes_checks.sql:25-26`.
- The `Embedder` is wired in `platform/container.ts:284-296`, but no `.embed(` call site exists in the server code I searched. Memory/RAG is gated off by default (`EMBEDDINGS_ENABLED`, `config.ts:20-23`).
- Embeddings are effectively dormant. Port the column type and extension only, and defer the retrieval logic.

**Concurrency limits.**
- `REVIEW_MAP_CONCURRENCY`: optional, 1..16, unset means 3 (`config.ts:33-36`). Chunks in flight per review.
- `REVIEW_AGENT_CONCURRENCY`: 1..20, default 4 (`config.ts:39-42`). It is applied via `new PQueue({concurrency}).addAll(tasks)` in `modules/reviews/composition.ts:31`.
- Other bounded fan-out: pulls backfill (`modules/pulls/composition.ts:17`), eval cases (`modules/evals/composition.ts:54`) and JobRunner (default 3, `platform/jobs.ts:55`).
- `mapOrdered` (`review/pool.ts:1-33`) semantics: results keep item order; after the first failure no new items start; in-flight items are awaited before the first error is re-thrown. `errgroup.SetLimit` gives the limit and first-error behaviour, but you must write the ordered result slots yourself.

**Mock provider.** `adapters/llm/mock.ts` (262 lines) is a deterministic provider enabled by `LLM_PROVIDER_OVERRIDE=mock`, with a delay set by `LLM_MOCK_DELAY_MS`. It is refused in production (`config.ts:163-165`). It is probably used by the e2e suite (`e2e/`), so the Go side likely needs the same flag. I did not check how e2e depends on it.

## 3. GitHub integration (`adapters/github/octokit.ts`)
Authentication is a single PAT passed to `new Octokit({auth})` (`:76-81`). The token comes from the secrets store.

| Operation | Octokit call | Line |
|---|---|---|
| List PRs | `pulls.list` | 107 |
| Merged PRs plus their files | `pulls.list` and `pulls.listFiles` (batched, `MERGED_FILES_CONCURRENCY`) | 140, 158 |
| PR detail | `pulls.get`, `pulls.listFiles`, `pulls.listCommits` (capped pagination) | 186-208 |
| Linked issue | `issues.get` | 476 |
| Post review (with inline comments) | `pulls.createReview` | 267 |
| List/reply/create review comments | `pulls.listReviewComments`, `createReplyForReviewComment`, `createReviewComment` | 319-350 |
| Open PR | `pulls.create` | 371 |
| Commit files (CI export) | `git.getRef`, `getCommit`, `createTree`, `createCommit`, `updateRef`/`createRef` | 395-440 |
| Find open PR for a branch | `pulls.list` | 458 |
| Who am I | `users.getAuthenticated` | 490 |
| File content | `repos.getContent` | 506 |
| Workflow runs, artifacts | `actions.listWorkflowRuns`, `listWorkflowRunArtifacts`, `downloadArtifact` (zip) | 528, 556, 581 |

- Pagination is manual and capped: `paginateCapped` pages with `per_page` up to a maximum, and a short page stops the loop (`:88-99`).
- `mergeSamePath` (`:49-62`) folds same-path file entries (file to symlink type change) into one.
- Every call is wrapped in `withRetry(withTimeout(...))`. The timeout is `TIMEOUT`, with 90 s for detail pages (`DETAIL_TIMEOUT`, `:31`).
- `downloadArtifact` returns `null` when the zip exceeds `maxBytes` (`:574-592`). A 410 means the artifact expired.
- Required token scopes for export: `repo` + `workflow`, or fine-grained Contents/Workflows/Pull requests write (`export-service.ts:42-44`).
- Rating: low-med.

## 4. Git operations (`adapters/git/simple-git.ts`)
- **Layout.** `<DEVDIGEST_CLONE_DIR>/<owner>/<name>`, default `~/.devdigest/workspace` (`platform/config.ts:173-175`).
  - Segments must match `^[A-Za-z0-9_.-]+$` and stay exactly two levels deep under the root (`simple-git.ts:89-102`). `clone()` runs `rm -rf` on a stale destination, so keep this guard.
- **Clone.**
  - `CLONE_DEPTH = 1` (`modules/repos/constants.ts:9`).
  - If `.git` already exists, it fetches instead of cloning. A partial directory is removed first (`:141-161`).
- **Fetch/resync** (`:170-182`).
  - `fetch origin <branch> --depth 50`, then `reset --hard origin/<branch>`.
  - PR heads are fetched via `pull/<n>/head:pr-<n>` (`:163-168`).
- **Other commands.**
  - `diff base...head`, which is three-dot and parsed by `adapters/git/diff-parser.ts` (79 lines).
  - `diff --name-only base..head` (two-dot), for incremental indexing.
  - `blame --line-porcelain`, `log`, and `rev-parse HEAD`.
  - `merge-base` / `rev-parse --verify` for the base commit.
  - `cat-file -s` followed by `git show sha:path` with a 64 KiB cap.
- **No worktrees.** I found none (grep for "worktree" hit only comments). A shallow clone with `reset --hard` is the model.
- **Auth.** The token is injected per command via `-c http.https://github.com/.extraHeader=Authorization: Basic base64(x-access-token:TOKEN)` (`:49-52`).
  - It is never written to `.git/config`, and `scrubOrigin` strips credentials from older clones.
  - `GIT_TERMINAL_PROMPT=0` and `GCM_INTERACTIVE=never` are set.
- **Path safety.** `readFile` must stay inside the clone, with no `..` and no absolute path (`:259-278`). SHAs must match `^[0-9a-f]{7,40}$`. Ref names are restricted by `BASE_REF_RE`.
- The repository code is never executed ("we NEVER execute repo code").
- The Go image needs a `git` binary if you shell out.

## 5. repo-intel (`server/src/modules/repo-intel`, 2683 lines)
- **Pipeline** (`README.md`): walk files, then ast-grep symbols and references, then the import graph, then PageRank, then the repo map. Everything is persisted in Postgres in one transaction (`symbols`, `references`, `file_edges`, `file_rank`, `repo_map_cache`).
- **Languages.** `SUPPORTED_EXT = .ts .tsx .js .jsx .mjs .cjs` (`server/src/domain/source-scope.ts:9`). The mapping is `langForFile` (`adapters/astgrep/index.ts:57-76`). JSX goes through the TSX grammar. The indexer only understands TS/JS repos.
- **Extraction.** `adapters/astgrep/index.ts` uses ast-grep's `findAll({rule:{kind}})`. It handles these node kinds:
  - function, generator, class (methods are dual-emitted as `Class.method` and bare `method`), interface, type alias, enum, lexical/variable declarations, arrow functions;
  - call/new expressions (references), and import statements (default, namespace, named, type-only).
  - Signatures are trimmed to 120 characters (`MAX_SIGNATURE_CHARS`). `exported` is detected via `export_statement`.
- **Fallback.** `adapters/codeindex/extract.ts` (214 lines) is a regex extractor used by `RipgrepCodeIndex` (`symbols`, `references`). It is what blast-radius uses (`blast-radius.ts:161,183`).
- **Import graph.**
  - `adapters/depgraph/index.ts` runs `dependency-cruiser`'s `cruise()` with the repo's `tsconfig.json`, so path aliases resolve (`:61-71`).
  - It emits `{from,to}` for local, supported-extension, in-set files.
  - Any failure returns `[]`, and the index is stamped `partial`.
  - **`dependency-cruiser` is listed under `devDependencies` in `server/package.json` but imported at runtime.** That is a production-install hazard on the Node side.
  - A Go replacement means resolving relative imports yourself: extensions, `index.*`, and tsconfig `paths`/`baseUrl`. I would expect the quality to be lower than dependency-cruiser's.
- **Rank.**
  - `domain/rank.ts` builds a directed graph with importer pointing to imported, and no self-loops or multi-edges.
  - It runs `graphology-metrics`' `centrality.pagerank`, then computes a tie-aware percentile.
  - `hotness = 0` because the clone is shallow.
  - On failure it falls back to a uniform score.
  - Go has no drop-in graphology. You can implement PageRank directly (about 30 lines). Using `gonum` is optional.
  - To keep scores stable for existing data, match the graphology defaults (alpha 0.85, tolerance). I did not verify those defaults.
- **Repo map** (`domain/repo-map.ts:102-162`).
  - Candidates arrive ordered by rank DESC, exported DESC, line ASC.
  - They are deduplicated by `path signature`.
  - A binary search finds the largest prefix whose rendering fits `DEFAULT_REPO_MAP_TOKEN_BUDGET = 1500` tokens, counted with tiktoken `cl100k_base`.
  - The output is deterministic per (commit sha, budget).
- **Limits** (`constants.ts:179-208`):
  - `MAX_INDEXED_FILES` 5000 and `MAX_FILE_SIZE` 400 KB.
  - `MAX_PARSE_MS_PER_FILE` 2000 and a soft indexing budget of 110 s, against the JobRunner's hard 120 s.
  - Excluded dirs: node_modules, dist, build, coverage, .next, out, vendor, .git.
  - `INDEXER_VERSION = 2`. Bump it whenever extraction output changes, to force a reindex.
- **Facade**: `getRepoMap`, `getFileRank`, `getCallerSignatures` (cap 20 per symbol), `getBlastRadius` (BFS depth 2), `getUnresolvedReferences` and `getConventionSamples`.
- Rating: **high** for AST extraction and import resolution, low for rank/repo-map.

**ripgrep.**
- `@vscode/ripgrep` is optionally dynamic-imported (`ripgrep.ts:28-41`) and spawned with `--line-number --no-heading --color=never`. A recursive Node-regex walk is the fallback.
- The walk skips `.git`, node_modules, dist, build, .next and coverage, and files at or above 2 MB (`:128-146`).
- `RipgrepCodeIndex.grep()` has no caller in the code I searched. Only `symbols()` and `references()` have production callers (`source-queries.ts:65`, `blast-radius.ts:161,183`), and both are pure regex and file walks with no ripgrep.
- **So the Go port does not need a ripgrep binary.** `filepath.WalkDir` with the same ignore set and Go regexp is enough. The seed prompt text mentions ripgrep, but the code path is dormant.
- Rating: low.

## 6. Export to CI and agent-runner
- **The server does not build or zip a bundle.**
  - `FileRunnerBundle.read()` just reads the pre-built text file `agent-runner/dist/index.js` (1,636,462 bytes) from the repo checkout (`modules/ci/infrastructure/runner-bundle.ts:10-23`). The path is five levels up from the file.
  - It returns `null` on ENOENT, which becomes `500 runner_bundle_missing` (`export-service.ts:98-101`).
- **Files committed** (`export-service.ts:103-109`):
  - `.devdigest/agents/<slug>.yaml`
  - `.devdigest/skills/<skill>.md`
  - `.devdigest/memory.jsonl` (empty)
  - `.devdigest/runner/index.js`
  - `.github/workflows/devdigest-review-<slug>.yml`
  - Branch `devdigest/ci`, one commit via the Git Data API, then reuse or open a PR (`:136-169`).
  - Only `target:gha` and agents on the `openrouter` provider can be exported (`:67-72`).
- **The bundle is built by `ncc build src/index.ts -o dist`** (`agent-runner/package.json` scripts). It bundles `reviewer-core` and the shared Zod contracts from TS source via tsconfig path aliases (`agent-runner/README.md`). The runner uses native `fetch`, with no octokit.
- **fflate is used for unzipping** the downloaded result artifact, with a size check against the central directory before inflating (`modules/ci/infrastructure/zip-entry-reader.ts`). It is not used to produce anything. The Go port uses `archive/zip` (central directory sizes via `File.UncompressedSize64`). Limits: zip 1 MiB, JSON 64 KiB (`ci/domain/constants.ts:57-59`).
- **YAML**: `yaml.parse` and `yaml.stringify({lineWidth:0})` (`ci/infrastructure/yaml-codec.ts`). It is used in the manifest generator, `checkWorkflow` (security rules: only `pull_request` triggers, strict `permissions`, fork guard) and `workflow.ts` (113 lines). Check that the Go YAML emitter's key order, quoting and line folding produce workflow files that still pass `checkWorkflow` and match any golden tests. I did not look at those tests.
- **Workflow shape**: the workflow runs `node .devdigest/runner/index.js` on `ubuntu-24.04` with Node 22 (`ci/domain/constants.ts:17,50-51`). It pins action SHAs (`ACTION_PINS`, `:54-58`).
- **Does the Go server need Node at runtime? No.** It only needs the bytes of `index.js`.
  - Embed the file with `go:embed`, or copy it into the image at build.
  - A Node build stage is needed to produce it.
  - The runner still runs `reviewer-core` in TS inside GitHub Actions. If the TS `reviewer-core` is retired, the CI path loses its engine.
  - Decision needed: keep `agent-runner` + a TS `reviewer-core` for CI only, or ship a Go runner binary and change the workflow generator (`RUNNER_RUN_COMMAND`, Node setup).
  - If both a Go and a TS engine exist, their prompts, grounding and scoring must stay in lock-step.
- **Ingest** (`ingest-service.ts`): a manual "Refresh" lists completed runs, looks up the artifact named `devdigest-result-<slug>`, downloads it, verifies it against the run, and stores `agent_runs` + `ci_runs`. Budget `REFRESH_BUDGET_MS = 45_000`, 30 runs per installation.
- Rating: med for the server side, **high** if the runner is to be ported.

## 7. Background and long-running work
- **Polling is not a timer.** `PollingService.poll` is a manual refresh that syncs the PR list and never triggers a review (`modules/polling/service.ts:23-37`). It uses no `setInterval` anywhere in `server/src` (grep found none).
- **JobRunner** (`platform/jobs.ts:43-153`). Jobs are mirrored in the `jobs` table with these steps:
  1. Insert a row with status `queued`.
  2. Run on a queue (concurrency 3).
  3. Mark `running`.
  4. Run the handler under `withRetry(withTimeout(...))`: timeout 120 s, 2 retries, transient errors only.
  5. Write `done` or `failed` and the error text.
  - The handler receives an `AbortSignal` that fires on timeout or shutdown.
  - `shutdown()` clears the queue (rows stay `queued`), aborts running handlers and waits up to 10 s.
  - Job kinds in repo-intel: `repo-intel-index`, `-refresh`, `-resync` (`constants.ts:169-172`). No durable queue; this is in-process only.
- **RunBus** (`platform/sse.ts:35-213`) is an in-memory pub/sub per run id.
  - Each event has a `seq` and a `HH:MM:SS` wall-clock string.
  - The buffer is replayed to late subscribers, `complete()` sends `done`, and state is evicted after 5 minutes.
  - It also carries sticky cancel flags and listeners, `cancelAll()` and `whenIdle(timeoutMs)`.
  - The full buffer is persisted as one document in `run_traces` at the end of a run.
  - The Go equivalent is a mutex-guarded struct with per-subscriber channels plus `context.CancelFunc`s.
- **SSE route**: `GET /runs/:id/events` (`modules/reviews/routes.ts:67-80`) sends `id=seq`, `event=kind`, `data=JSON(RunEvent)`. It replays the buffer first, ends on done, and has the rate limit disabled. The Next.js client contract depends on this frame format.
- **Startup tasks** (`app.ts:62-79`):
  - `reapStaleRuns` and `evals.runs.reapInterrupted` mark `running` rows as failed or interrupted. They are awaited BEFORE listening and only valid for a single instance.
  - Job handlers are registered per module.
  - `server.ts:35-49` logs warnings for mock-LLM and prompt-log-verbose modes.
- **Graceful shutdown** (`server.ts:27-31`, `app.ts:121-135`, `container.ts:163-171`):
  - The grace period is 20 s.
  - `preClose` cancels live runs (5 s drain), shuts down jobs (10 s), completes all run buses so SSE streams end, then aborts eval runs.
  - `onClose` closes the DB pool.
  - Uncaught exceptions and unhandled rejections also go through the shutdown path.
- **Health**: `/health` (no DB) and `/health/ready` (`select 1`, 503 on failure) (`app.ts:94-108`).

## 8. Other
- **Secrets are not in the DB.**
  - API keys live in a JSON file at `~/.devdigest/secrets.json`, mode 0600, read lazily and cached (`adapters/secrets/local.ts:12-50`). It is plaintext, with no encryption.
  - Resolution order: file value first, then environment variable. `GITHUB_TOKEN` falls back to `GITHUB_PAT`.
  - Settings keep only non-secret preferences in the DB (`db/schema/core.ts:38`). The status endpoint returns booleans only (`modules/settings/routes.ts:32`, `service.ts:45-51`).
  - Container secret caches are invalidated after a write (`settings/service.ts:64-65`).
  - For a container deployment, the file path under `$HOME` must be a mounted volume.
- **Auth.** `LocalNoAuthProvider` returns the seeded system user and default workspace (`adapters/auth/local.ts`). Every request is workspace-scoped.
- **Bind address.** `API_HOST` defaults to 127.0.0.1 (`config.ts:64`). The API has no auth, so a container needs `0.0.0.0` set explicitly.
- **Rate limiting.**
  - Global limit: 120 per minute, off when `NODE_ENV=test` (`app.ts:90-92`).
  - Per route: 20/min on settings connection test (`settings/routes.ts:50`), 10/min on review start (`reviews/routes.ts:48`) and on eval run-all (`evals/http/routes.ts:24`), plus intent refresh (`intent/http/routes.ts:31`). SSE and `/health*` are exempt.
  - Evals use a shared bucket for run-start routes (`evals/http/routes.ts:27-30,58`).
- **CORS**: single origin `http://localhost:${WEB_PORT}`, credentials true (`app.ts:85`). **helmet** defaults. **Body limit** 1 MiB (`app.ts:42`).
- **Config** (`platform/config.ts`) is a Zod-validated env. Notable parsing: empty strings count as unset; `REPO_INTEL_ENABLED` and `REVIEW_INTENT_ENABLED` are ON unless the string is `'false'`; `EMBEDDINGS_ENABLED` is OFF unless `'true'`. `PROJECT_CONTEXT_GLOBS` is split on commas outside `{...}` (`parseGlobList`, `:90-107`).
- **Glob library.** Project-context globs are `**/{specs,docs,insights}/**/*.md` by default. Go's `filepath.Glob` does not support `**` or `{a,b}`. Use a library such as `doublestar`. [general knowledge, not verified]
- **DB.**
  - Schema is Drizzle: 15 files, about 58 tables, 26 jsonb columns.
  - Migrations are 21 SQL files, `0000..0020`, with `--> statement-breakpoint` separators (88 in `0000_init.sql`).
  - `runMigrations` first runs `CREATE EXTENSION IF NOT EXISTS vector` (`db/migrate.ts:20-27`).
  - Seeds: `db/seed.ts`, `seed-prompts.ts`, `seed-skills.ts`, `seed-eval-demo.ts`, `seed-diff.ts`.
- **Logging**: pino, with a structured `prompt_assembled` line that carries section metadata and never prompt text (`platform/prompt-log.ts`). Keep that privacy property.

## Hard-to-port ranking
1. **AST symbol extraction** (639 lines of ast-grep logic): rewrite against a Go tree-sitter binding. The same node kinds apply, but the API differs.
2. **TS import resolution** (dependency-cruiser): Go has no equivalent.
3. **CI runner** (Node/TS bundle that embeds `reviewer-core`): decide between keeping it and rewriting it.
4. **Zod-derived strict JSON schemas and validation** for every LLM contract.
5. **RunBus + cancellation semantics** and the **SSE frame contract** with the client.
6. Everything else is low or medium.

---
# PART 2 — Go libraries (as of 2026-10-21; versions from pkg.go.dev)

**Go toolchain note.** Go 1.27 was due in August 2026. The draft release notes list generic methods and `go mod tidy` merging duplicate require blocks. I could not confirm the published notes. go.dev/doc/go1.27 was only seen as a search hit.

## Echo (HTTP framework)
- **Echo v5 is GA and current.**
  - Import path `github.com/labstack/echo/v5` ([pkg.go.dev](https://pkg.go.dev/github.com/labstack/echo/v5)).
  - v5.0.0 was released 2026-01-18, and the latest is **v5.4.0 on 2026-09-27** ([versions](https://pkg.go.dev/github.com/labstack/echo/v5?tab=versions)).
  - The releases page marks v5.4.0 as "Multiple security fixes with behavior changes" ([GitHub releases](https://github.com/labstack/echo/releases)).
- **v4 is in maintenance.**
  - v4.16.0 (also 27 Sep) carries the same security fixes as v5.4.0, and the page says most v4 patches are backports.
  - The README says "v4 receives security updates and bug fixes until 2026-12-31" ([GitHub README](https://github.com/labstack/echo)).
  - **Start new code on v5.** It is incompatible with v4 and with v4-era examples: handlers take `*echo.Context` (a struct pointer), not the old `echo.Context` interface ([SSE recipe](https://echo.labstack.com/cookbook/sse/)). The README points to `API_CHANGES_V5.md` for the migration notes; I did not read it.
- **Go version policy.** The README says "three most recent Go releases"; the pkg.go.dev page says "last four" (the two conflict). Go 1.27 is fine either way.
- **v5.4.0 behaviour changes worth knowing** (from the releases page):
  - `Context.Scheme()` / `X-Forwarded-Proto` is trusted only from loopback, private, link-local or unix-socket peers.
  - Behind a public-IP proxy or CDN, set the trusted ranges explicitly, or HTTPS redirects can loop.
  - `RealIP()` backs the rate-limit identifier. In v4, `RealIP()` trusts `X-Forwarded-For` from any client unless `IPExtractor` is set. This matters for per-IP rate limiting.
  - Static files: double slashes and dot segments return 404.

| Need | Echo v5 approach | Source |
|---|---|---|
| SSE | Set `Content-Type: text/event-stream`, `Cache-Control: no-cache` and `Connection: keep-alive`. Loop with `select` on `c.Request().Context().Done()`, write frames, and flush with `http.NewResponseController(w).Flush()`. `Response` also has its own `Flush()`. **Disable the server write timeout** by setting `WriteTimeout = 0` in `StartConfig.BeforeServeFunc`. No built-in SSE type, so write `id:`/`event:`/`data:` frames by hand to match the Node format (`id=seq`, `event=kind`, `data=JSON`). | [SSE recipe](https://echo.labstack.com/cookbook/sse/) |
| CORS | `middleware.CORSWithConfig(middleware.CORSConfig{AllowOrigins: []string{"http://localhost:3000"}, AllowCredentials: true})`. Wildcard origin plus credentials is refused: `CORS`/`CORSWithConfig` panic and `ToMiddleware()` returns an error. `UnsafeAllowOriginFunc` overrides `AllowOrigins` when set. | [CORS docs](https://echo.labstack.com/middleware/cors/) |
| Validation | No built-in validator. Set `e.Validator` to a type with `Validate(i any) error` (usually go-playground/validator, `validate:"required,email"` tags), then call `c.Bind(&req)` and `c.Validate(&req)`. There are also typed helpers `echo.QueryParam[T]`, `echo.PathParam[T]` and `echo.FormValue[T]`. Error envelopes must be mapped in a custom `HTTPErrorHandler` to match the existing JSON error shape (`http/error-handler.ts`). | [Request guide](https://echo.labstack.com/guide/request/) |
| Rate limiting | `middleware.RateLimiterWithConfig` with `NewRateLimiterMemoryStoreWithConfig({Rate, Burst, ExpiresIn})`. It also has `Skipper`, `IdentifierExtractor` (defaults to `c.RealIP()`), `ErrorHandler` and `DenyHandler` (defaults to 429 "rate limit exceeded"). **It is a token bucket (Rate per second + Burst), not a fixed "N per minute" window** like `@fastify/rate-limit`. The 10/min and 20/min limits map approximately (Rate ≈ N/60, Burst = N); this is my inference. Apply per route group via `Group.Use` or `Route.Use`, and exempt SSE and `/health*` via `Skipper`. The built-in store is in-process, which matches the single-instance deployment. | [Rate limiter docs](https://echo.labstack.com/middleware/rate-limiter/) |
| Graceful shutdown | `echo.StartConfig{Address, GracefulTimeout}` and `sc.Start(ctx, e)` with `ctx` from `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`. On cancel it stops accepting connections and gives active requests up to `GracefulTimeout`. A custom `http.Server` plus `Shutdown` also works. The Node shutdown order (cancel runs, jobs, complete SSE, evals, close DB) must run before or alongside `Start` returning. **Open SSE connections stall shutdown** unless the run buses complete first, as the Node code does. Set `GracefulTimeout` of at least 20 s to mirror `GRACE_MS` (`server.ts:15`). v5.0.4's notes mention a graceful shutdown fix ([newreleases.io](https://newreleases.io/project/github/labstack/echo/release/v5.0.4)). | [Graceful shutdown recipe](https://echo.labstack.com/cookbook/graceful-shutdown/) |

Other Echo items: security headers (the helmet equivalent) are available as middleware, and the 1 MiB body cap is available as middleware. I did not verify exact v5 names for either. [general knowledge, not verified]

## GORM + pgvector + datatypes
| Package | Latest | Date | Notes |
|---|---|---|---|
| `gorm.io/gorm` | v1.31.2 | 2026-06-22 | Previous: v1.31.1 (2025-11-02). Source: [versions](https://pkg.go.dev/gorm.io/gorm?tab=versions) |
| `gorm.io/driver/postgres` | v1.6.3 | 2026-09-14 | Sits on pgx. Source: [versions](https://pkg.go.dev/gorm.io/driver/postgres?tab=versions) |
| `gorm.io/datatypes` | v1.2.7 | 2025-09-19 | Oldest of the set (about 13 months old). Source: [versions](https://pkg.go.dev/gorm.io/datatypes?tab=versions) |
| `github.com/pgvector/pgvector-go` | v0.4.1 | 2026-07-30 | Previous: v0.4.0 (2026-05-18). Source: [versions](https://pkg.go.dev/github.com/pgvector/pgvector-go?tab=versions) |
| `github.com/jackc/pgx/v5` | v5.11.0 | 2026-09-07 | [versions](https://pkg.go.dev/github.com/jackc/pgx/v5?tab=versions) |

- **JSONB with datatypes.**
  - `datatypes.JSONType[T]` (added v1.1.0) gives typed JSON columns, and `NewJSONType` (v1.2.0) builds one. `JSONMap` covers untyped objects.
  - The datatypes page does not use the word "JSONB". Use `gorm:"type:jsonb"` on the field, and verify the driver round-trips it.
  - The repo has 26 jsonb columns, so this will be used a lot.
- **pgvector with GORM.**
  - `pgvector.Vector` with `gorm:"type:vector(1536)"`, `db.Create`, and nearest-neighbour via `clause.OrderBy{Expression: clause.Expr{SQL: "embedding <-> ?", Vars: []interface{}{pgvector.NewVector(...)}}}`.
  - The README lists GORM among supported drivers ([pgvector-go README](https://github.com/pgvector/pgvector-go)).
  - Cosine distance (`<=>`, `vector_cosine_ops`) matches the existing HNSW indexes. The README snippet shows the `<->` operator for L2.
  - With raw pgx, register types via `pgxvec.RegisterTypes` in `config.AfterConnect`. With GORM's pgx driver, I'd expect a plain `Valuer`/`Scanner` to be enough, but I didn't verify.
- **GORM caveats for this schema** [general knowledge, not verified]:
  - Auto-migrating over the existing Drizzle-generated tables is risky.
  - Partial and expression indexes and CHECK constraints (`0011_integrity_indexes_checks.sql`) are easier to keep in SQL migrations.
  - Use `db.Exec("CREATE EXTENSION IF NOT EXISTS vector")` before any vector table, as `db/migrate.ts:20-27` does.

## Other libraries
| Need | Recommendation | Latest (date) | CGO / extra binary | Notes and sources |
|---|---|---|---|---|
| Anthropic | `github.com/anthropics/anthropic-sdk-go` | **v1.84.0 (2026-10-20)** on the v1 path ([versions](https://pkg.go.dev/github.com/anthropics/anthropic-sdk-go?tab=versions)); **v2.0.0 (2026-10-21)** on `/v2` ([versions](https://pkg.go.dev/github.com/anthropics/anthropic-sdk-go/v2?tab=versions)) | none | **Major-version trap.** v2 was released today and sends API version `2026-10-21`; the docs say each new API version comes with a new major (the [Go SDK doc](https://platform.claude.com/docs/en/api/sdks/go) lists v1 import paths and "Go 1.24+"; the [GitHub README](https://github.com/anthropics/anthropic-sdk-go) shows `/v2` and "Go 1.25+"). **Recommend v1.84.x now** and plan a deliberate v2 move. Features the pipeline needs: forced tool use is supported (`ToolParam` with `InputSchema`; I did not see `tool_choice` in the docs excerpt), `Messages.NewStreaming`, `option.WithMaxRetries` (default 2), `option.WithRequestTimeout` (per attempt), and a 10-minute default for non-streaming. |
| OpenAI | `github.com/openai/openai-go/v3` | **v3.85.0 (2026-10-21)** ([versions](https://pkg.go.dev/github.com/openai/openai-go/v3?tab=versions)) | none | Requires Go 1.26+ ([README](https://github.com/openai/openai-go)). Defaults: 2 retries on connection errors, 408, 409, 429 and 5xx; no request timeout; `WithRequestTimeout` per attempt. Structured output is documented for the **Responses API** (`ResponseFormatTextJSONSchemaConfigParam`, `Strict`) with schemas from `invopop/jsonschema`. The current code uses **chat completions** `response_format`, and I did not confirm the chat-completions param types in v3. `OPENAI_BASE_URL` / `option.WithBaseURL` are supported. |
| OpenRouter | Use openai-go with `option.WithBaseURL("https://openrouter.ai/api/v1")` | n/a | none | OpenRouter is OpenAI-compatible, which the repo already relies on (`openrouter.ts:23`). `session_id` and `usage:{include:true}` are non-standard body fields. The Anthropic SDK has `option.WithJSONSet`; I believe openai-go has it too, but I didn't confirm that. `/models` should be fetched with plain `net/http` (it carries pricing). `usage.cost` is read from the raw JSON (the Anthropic doc shows `result.JSON.Foo.Raw()` / `ExtraFields` for undocumented fields; I assume the same pattern in openai-go). |
| GitHub API | `github.com/google/go-github/v93` | **v93.0.0 (2026-10-15)**; v89.0.0 was 2026-07-06 | none | go-github majors move roughly weekly to fortnightly (pkg.go.dev lists v18 to v93), so pin one and bump on purpose. The pkg.go.dev page said v93 was "not the latest"; a v94 URL returned 404, so v93.x patch releases may exist. I did not confirm the exact method names (`Actions.DownloadArtifact`, `PullRequests.CreateReview`, `Git.CreateTree`, `Actions.ListWorkflowRuns`). Use `ListOptions` pagination with your own cap. Artifact download is a redirect to a blob URL. |
| git | **Shell out to `git`** | go-git v5.19.3 (2026-10-04) ([versions](https://pkg.go.dev/github.com/go-git/go-git/v5?tab=versions)) | `git` binary in the image | See the trade-off below. |
| tree-sitter | **No pure-Go-and-mature option; choose between CGO and a young pure-Go runtime** | `github.com/tree-sitter/go-tree-sitter` v0.25.0 (2025-02-02) ([versions](https://pkg.go.dev/github.com/tree-sitter/go-tree-sitter?tab=versions)); `github.com/smacker/go-tree-sitter` (401 commits, no archive banner, last-commit date not visible); `github.com/odvcencio/gotreesitter` v0.56.0 (2026-10-13) ([versions](https://pkg.go.dev/github.com/odvcencio/gotreesitter?tab=versions)) | **CGO for the first two** (they vendor C sources; I inferred this from the C files; no page states it); **no CGO for gotreesitter** | See the section below. |
| ripgrep | **Skip it**; use `filepath.WalkDir` + `regexp` | n/a | none | `grep()` has no caller and `symbols`/`references` don't use rg (Part 1 section 5). Go's RE2 is also closer to ripgrep's regex semantics than JS RegExp. [general knowledge, not verified] |
| tokenizer | `github.com/pkoukk/tiktoken-go` | v0.1.8 (2025-09-10) ([versions](https://pkg.go.dev/github.com/pkoukk/tiktoken-go?tab=versions)) | none | Only the repo-map budget needs `cl100k_base`, so a few percent of drift is tolerable. Everything else uses chars/4. Its BPE-data loading (runtime download vs embedded) was not verified; the sibling `tiktoken-go-loader` package is the usual offline answer. [general knowledge, not verified] |
| graph metrics | Write PageRank by hand, or `gonum.org/v1/gonum` | gonum v0.17.0 (2025-12-29) ([versions](https://pkg.go.dev/gonum.org/v1/gonum?tab=versions)) | none | `gonum` has `network.PageRank`. [general knowledge, not verified] A hand-rolled PageRank is about 30 lines and removes a heavy dependency. |
| migrations | **goose** | v3.29.0 (2026-10-11) ([versions](https://pkg.go.dev/github.com/pressly/goose/v3?tab=versions)) | none | golang-migrate v4.20.1 (2026-09-09) is the alternative ([versions](https://pkg.go.dev/github.com/golang-migrate/migrate/v4?tab=versions)). Both can `go:embed` SQL. goose supports Go migrations, which suit seed logic. [general knowledge, not verified] The existing `0000..0020` Drizzle files can be reused as the baseline only after stripping `--> statement-breakpoint` and splitting statements, because both tools run one file as a unit or by their own markers. |
| YAML | `go.yaml.in/yaml/v3` | v3.0.5 (2026-07-26) ([versions](https://pkg.go.dev/go.yaml.in/yaml/v3?tab=versions)) | none | A v4 module also exists; I did not look at its status. The old `gopkg.in/yaml.v3` was archived [general knowledge, not verified]. |
| zip | stdlib `archive/zip` | n/a | none | Replaces fflate's read side. Check `UncompressedSize64` before opening the entry and cap the read with `io.LimitReader`. |
| SSE | `net/http` flushing via Echo (above); no library required | n/a | none | Keep the manual `id/event/data` frame format. |
| concurrency | `golang.org/x/sync/errgroup` (`SetLimit`) or `semaphore` | x/sync v0.24.0 (2026-09-23) ([versions](https://pkg.go.dev/golang.org/x/sync?tab=versions)) | none | `errgroup.SetLimit` covers the per-review and per-batch caps. Order-preserving results and "no new items after first failure" need a small wrapper (see `pool.ts`). |

### git: shell out vs go-git
- **Shell out (recommended).**
  - The code needs these operations:
    - shallow clone and `fetch --depth 50`;
    - `reset --hard`;
    - three-dot and two-dot `diff`;
    - `blame --line-porcelain`;
    - `merge-base`;
    - `cat-file -s`;
    - `show sha:path`.
  - The existing code already speaks CLI porcelain, and the parsers (`diff-parser.ts`, blame) can be ported 1:1.
  - The auth model (`-c http.https://github.com/.extraHeader=...`) maps directly onto `exec.CommandContext`, and context cancellation kills the child process, as `abort: signal` did.
  - Cost: the image needs `git` (and `ca-certificates`), so it cannot be a bare static or distroless image. The `-base` distroless variants do not ship git.
  - A small Alpine or Debian-slim runtime with `git` is the simple answer.
- **go-git** is pure Go (no CGO, no binary), but:
  - `blame`, `merge-base`, shallow `fetch --depth` and `diff` with three-dot semantics need extra work or are unsupported. I did not verify support for each.
  - v5.19.1 carries two advisories (GO-2026-6213 symlink following in worktree operations, GO-2026-6214 path traversal via crafted reference names), per the pkg.go.dev page.
  - Those advisories matter because this service clones untrusted repositories.
- **Worktrees:** not used by the Node code, so there is nothing to port.

### tree-sitter and the CGO question
| Option | CGO | Maturity | Notes |
|---|---|---|---|
| `tree-sitter/go-tree-sitter` | yes (C sources vendored, inferred) | official org, last tag 2025-02-02 | The README says grammars are NOT bundled: `go get` a separate module for each (it shows tree-sitter-javascript's `bindings/go`; the TypeScript module path was not confirmed). You must call `Close()` on parsers, trees and queries because finalizers are unreliable with CGO ([README](https://github.com/tree-sitter/go-tree-sitter)). It also describes a purego loading option for shared-library grammars. |
| `smacker/go-tree-sitter` | yes (C sources vendored, inferred) | 401 commits; no archive banner on the page | Bundles about 30 grammars, including TypeScript, JavaScript and others ([repo](https://github.com/smacker/go-tree-sitter)). The folder listing does not obviously show a separate TSX grammar; check before relying on it. The README's own wording on cgo was not visible in what I read. |
| `odvcencio/gotreesitter` | **no** (pure Go) | v0.56.0, releases every few days, MIT, 576 stars | Reads the same parse tables as the C runtime. The README claims 206 grammars, with JavaScript, TypeScript and TSX named in connection with declaration/signature facts, but I could not confirm exact grammar support ([repo](https://github.com/odvcencio/gotreesitter)). It reports full parses about 2.4x slower than native C. It has no API stability statement. |

- **Consequence for the image.**
  - Any CGO option needs a C toolchain at build time and either a libc runtime or a fully static musl link.
  - A `CGO_ENABLED=0` fully static distroless image is only possible with gotreesitter.
  - With CGO, use `gcr.io/distroless/base` (glibc) or a musl static link. Cross-compiling for macOS or arm64 from Linux needs per-target C toolchains. [general knowledge, not verified]
  - Since the image needs `git` anyway, a Debian-slim or Alpine runtime with CGO is the pragmatic choice, and a "distroless-static" goal is already off the table.
- **Pragmatic suggestion.**
  - Start by porting the existing regex fallback `adapters/codeindex/extract.ts` (214 lines). It is already the always-available path.
  - Add tree-sitter later, behind the `INDEXER_VERSION` bump.
  - A prototype against gotreesitter (CGO-free) versus smacker (battle-tested, CGO) decides the matter. The needed node kinds are listed in Part 1 section 5.
  - ast-grep's `findAll({rule:{kind}})` becomes a tree walk or an S-expression query. The `field('body')` calls become `ChildByFieldName`.

## Flags: what forces CGO or extra binaries
| Item | Forces |
|---|---|
| `git` shell-out | the `git` binary and `ca-certificates` in the image (not distroless-static) |
| `tree-sitter/go-tree-sitter` and `smacker/go-tree-sitter` | **CGO** and a C toolchain at build time |
| `gotreesitter` | nothing, but young |
| `agent-runner/dist/index.js` (1.6 MB) | a Node build step at image build time (Node is not needed at runtime), unless the runner is rewritten |
| ripgrep | nothing if skipped, as recommended |
| everything else above (SDKs, pgx, GORM, goose, tiktoken-go, YAML, errgroup, archive/zip) | no CGO |

## Conflicting information
- **Echo supported Go versions.** The README says "three most recent"; pkg.go.dev says "last four". Not material for Go 1.27.
- **Anthropic SDK path.** The platform doc page shows the v1 import path and "Go 1.24+"; the GitHub README shows `/v2` and "Go 1.25+". This is consistent with a v2 release made today; the doc page appears not yet updated. I treated the GitHub README plus pkg.go.dev as more current.
- **Anthropic v1 versions.** One pkg.go.dev fetch showed v1.81.0 to v1.84.0 on Oct 14 to 20, 2026. That is a very fast cadence, but I could only see the first four entries.

---
## Not found / unclear
- **Where the Node server reads `server/src/prompts/onboarding.system.md`.** No TS reference was found, so it may be dead.
- **A `FILE_MAP_THRESHOLD_LINES` constant in the server.** `run.ts:40` says the default "matches" it. I did not locate it.
- **Zod schemas for every LLM output contract.** I only read the `Review`/`Finding` locations (`vendor/shared/contracts/findings.ts:47-100`). The brief, onboarding, intent, conventions and eval-judge schemas were not inventoried. I also did not check how e2e or evals use the mock LLM (`adapters/llm/mock.ts`).
- **The default parameters of graphology's `pagerank`** (alpha, tolerance, iteration count), which determine whether a Go PageRank reproduces existing `file_rank` values.
- **The tests for `workflow.ts`** (golden outputs), which matter for the choice of Go YAML emitter.
- **Run executor internals.** I did not read `modules/reviews/application/run-executor.ts`, `review-service.ts` or `multi-agent-service.ts` in depth, nor `modules/blast`, `brief`, `intent`, `conventions`, `evals`, `onboarding` or `project-context`.
- **A final Go 1.27 release page.** Only draft notes were visible in search; the release date and final contents are unconfirmed.
- **Echo helmet/body-limit equivalents.** Exact v5 middleware names were not checked. I also did not read Echo's `API_CHANGES_V5.md`.
- **openai-go v3 chat-completions JSON-schema param types, `option.WithJSONSet` support, and reading `usage.cost` from the raw response.** Only the Responses API structured-output example was seen.
- **Anthropic SDK `tool_choice` forced-tool field names and structured outputs.** The doc excerpt did not show them.
- **go-github method names** (the four above).
- **go-git v6 status**, and which of blame, merge-base and shallow fetch go-git v5 supports.
- **tiktoken-go's BPE loading behaviour** (embedded vs downloaded).
- **tree-sitter module paths for TypeScript/TSX** under `tree-sitter/go-tree-sitter`, TSX presence in smacker's set, gotreesitter grammar coverage for TS/TSX/JS, smacker's last-commit date, and whether any of them is actively maintained as of Oct 2026.
- **`golang-migrate` vs `goose` behaviour against Drizzle's `--> statement-breakpoint` format**, and `gonum`'s `network.PageRank` API (general knowledge only).
- **`go.yaml.in/yaml/v4` status.**
