# Phase 4 parity corpus and Node golden generator (S12)

`../corpus/` holds committed inputs (cases, fixtures, diffs). `../goldens/*.json` are the expected outputs,
produced by running the **Node** implementation of dev-digest (commit `d1c9093`) from the read-only checkout
`<dev-digest>` (here `/home/ypodop/IdeaProjects/dev-digest`): reviewer-core, the review run executor with
in-memory fakes and a scripted LLM, the multi-agent read model, and Fastify + `fastify-sse-v2` for the SSE bytes.
`../handwritten/*.json` are the expectations of the parts that cannot run without a database. Spec: NFR4005,
AC4041, AC4076, D4010, D4012; plan P16. Consumers: S23 (`parity/domain_test.go`), S25 (`parity/runs_test.go`),
S27 (SSE bodies), S7/S8/S11 (schema, mock, prompt tests may read the goldens too).

## Regenerate

From this repository, with `TMPDIR` a **short** scratch directory outside both checkouts (tsx writes its cache
there; a long path such as the Claude scratchpad breaks tsx's IPC socket with a spurious `EADDRINUSE`):

```sh
cd backend/internal/reviews/parity/testdata
DD=/home/ypodop/IdeaProjects/dev-digest
mkdir -p /tmp/p4gen
TMPDIR=/tmp/p4gen $DD/server/node_modules/.bin/tsx --tsconfig $DD/server/tsconfig.json \
  gen/generate.mts --dd $DD --out goldens
```

The generator refuses to run unless `<dev-digest>` is at `d1c9093` with a clean `git status`, checks that again
after the run, uses only read-only git commands (`--no-optional-locks`), and writes only below `--out` (which
must lie inside `testdata/`). It never installs anything: it needs the checkout's own `server/node_modules` and
`reviewer-core/node_modules` (tsx, fastify 5, fastify-sse-v2 4.2.2, fastify-type-provider-zod, drizzle-orm, zod,
openai 4, @ast-grep/napi). `goldens/meta.json` records the commit and the versions.

The output is deterministic: a second run reproduces the first byte for byte. Check it by comparing against a
copy of the first run (do **not** use `git add -N` + `git diff`: intent-to-add stages empty blobs and every file
then shows as new):

```sh
cp -r goldens /tmp/p4gen/run1 && <regenerate command> && diff -r /tmp/p4gen/run1 goldens && echo IDENTICAL
```

How determinism is achieved: `TZ=UTC`; the global `Date` is replaced by a logical clock that only the fake ports
move (so a library calling `Date.now()` cannot change a duration); there is no random data; the local HTTP
servers (provider fake, Fastify) only influence recorded bodies, never ports, timings or ordering; filesystem
walks are sorted.

## Conventions of the golden files

- JSON, 2-space layout of the Phase 3 goldens (short flat objects on one line). Every file has a `note`.
- Goldens carry their inputs (cases are self-contained), except the large synthetic ones below.
- **`$blob`**: a string longer than 16,384 UTF-16 units is written as
  `{"$blob": true, "len": <UTF-16 units>, "sha256": <hex of the UTF-8 bytes>, "head": <first 80 code points>, "tail": <last 80>}`.
  Compare such a value by `len` (UTF-16 units) and `sha256`. Only cases built from a `synth` spec contain blobs; their
  input is rebuilt from the spec (next item).
- **Synthetic diffs** (`synth` in corpus and in `engine.json` as `diffSynth`, `chunks.json` as `synth`): per file the lines
  `diff --git a/<p> b/<p>`, `--- a/<p>`, `+++ b/<p>`, then per hunk `{start, add, del, ctx, text}` the lines
  `@@ -<start>,<ctx+del> +<start>,<ctx+add> @@`, `ctx` lines ` <text> ctx<i>` (i = 1..ctx), `del` lines `-<text> old<i>`,
  `add` lines `+<text> new<i>`; all lines joined with `\n`, no trailing newline. `text` defaults to
  `const value = compute();`. `padTo: N` (chunks.json only) extends the **last line** with `x` until the raw text has exactly N
  UTF-16 units. A synthetic PR file patch (`pr_files.patch`) is the hunk lines only.
- **`{"$repeat": [text, n]}`** (`corpus` only, and `sse.json` `synthetic[].events`): the text repeated n times.
- Finding **shorthand** (`f s e sev cat t why fix c k skill oos tri evidence`) exists only in `corpus/`; goldens contain
  the expanded model JSON (schema key order: id severity category title file start_line end_line rationale
  [suggestion] confidence [kind] [trifecta_components] [evidence] [skill] [out_of_scope]; optional keys are present only
  when the case gave them, `null` stays `null`).
- Run ids and PR/agent ids in `runs.json` are fixed UUIDs; the persisted review id is always
  `11111111-1111-4111-8111-111111111111`, persisted finding ids `f0000000-0000-4000-8000-00000000000<n>`.
- **Timings are not parity data.** `(<n>ms)` in step messages, `duration_ms`, `durationMs`, `tool_calls[].ms` and the
  SSE `t` come from the logical clock: LLM call 1,500 ms, diff load 20, repo-intel call 10, project-context 30, skills 2,
  store write 5, provider resolve 1. `t` is `HH:MM:SS` of 2026-01-01T12:00:00Z plus the clock. Tests must normalise
  `\(\d+ms\)` and ignore `t` and the duration fields (AC4041: "all fields except `t` and timings").
- Costs are JS floats summed in chunk order (`0.1 + 0.2 + 0.3 = 0.6000000000000001`); Go sums the same way and may compare exactly.

## Goldens

| Golden | Node code | Corpus | What to compare |
|---|---|---|---|
| `diff.json` | `parseUnifiedDiff` (diff-parser.ts); `storedDiff` of pull.repo.ts on a fake drizzle handle | `corpus/diff/{parse,reconstruct}.json` | `parse[].diff.files` (path, additions, deletions, hunks with oldStart/oldLines/newStart/newLines/newLineNumbers). `reconstruct[]`: `rows` (path byte order) → `raw` text → `diff` |
| `chunks.json` | `sliceDiff`, `splitOversizeDiff` | `corpus/chunks/{slice,split}.json` | `slice[].slices[]` text per path (and `fileLabels` = parsed paths = map chunk labels); `split[]` parts and `truncated` for cap `max` (UTF-16 units) |
| `engine.json` | `reviewPullRequest` with a scripted LLM: single pass, map-reduce, oversize split, failures, cancel poll, mode selection (`mode_*` cases) | `corpus/engine/cases.json`, `corpus/prs/*` | `calls[].messages` (byte-identical prompts), `promptEvents` (section metadata per chunk), `events`/`eventGroups`, `usage`, `outcome` (review, grounding, dropped, mode, assembly, chunks, tokens, cost, raw) or `error` |
| `reduce.json` | `reduceReviews`, `scoreFromFindings`, `countBlockers`, `mapOrdered` | `corpus/reduce/cases.json` | `reduce[].merged`; `score[].score`; `blockers[].counts` per gate; `mapOrdered[]` (`initialStarted`, `steps[].startedAfter`, `results` or `error`) |
| `grounding.json` | `groundFindings`, `groundingSummary`, `applyScopePolicy` | `corpus/grounding/cases.json` | `ground[]`: `keptIndexes`, `dropped[{index, reason}]`, `summary`; `scope[]` |
| `prompt.json` | `assemblePrompt`, `wrapUntrusted`, `contextLabel`, `estimateTokens`, `taskLine`, `rankNote`, `hotFiles`, `callersDigest`, `renderSkillBlock`, `skillsLogLine`, `skillsUsed` | `corpus/prompt/cases.json` | `assemble[]`: `messages`, `assembly` (the trace's prompt_assembly), `sections` (name, source, role, trust, chars, tokens, items, truncated); the helpers by their `out`/`line`/`block` fields |
| `schema.json` | `toJsonSchema(Review, 'Review')`, `parseWithRepair`, `extractJson` | `corpus/schema/cases.json` | `strict` (compare as JSON values with `llm.StrictSchema`, P7); `parse[]` ok+data or error text + reprompt message; `extractJson[]` |
| `llm.json` | `OpenRouterProvider.completeStructured` over HTTP against a local server | `corpus/schema/llm.json` | `runs[].requests[].body` (session_id only for openrouter, `usage.include`, temperature rules, `response_format` shown as `"$strictReview"`), reprompt messages, `usage` per response, `result` or the final error text |
| `multiagent.json` | `orderColumns`, `runTotals`, `groupFindings`, `learnedMemoryContent`, `learnContext`, `MultiAgentService.get/list/estimates/learn` over a fake store, row→wire mappers | `corpus/multiagent/cases.json` | `columns[]` (ordered run ids, totals, conflicts), `learn`, `get[].response`, `list`, `learnUseCase[]` (response + the INSERT values), `findingRows`, `reviewRecords` |
| `callers.json` | `extractReferences`; `getCallerSignatures` over fixture clones | `corpus/callers/**` | `references[].lines`; `signatures[].rows` |
| `runs.json` | `ReviewRunExecutor.executeRuns` through fake ports (see below) | `corpus/runs/cases.json`, `corpus/prs/*` | per scenario: `events[runId]`, `calls`, `finalRuns`, `traces`, `llmCalls`, `promptLog` |
| `sse.json` | Fastify + fastify-sse-v2 + the real `reviewsRoutes` | `corpus/sse/cases.json` + scenarios | raw HTTP status/headers/body of `GET /runs/:id/events` |

### `engine.json` event comparison

Node publishes every chunk's `tool` event before any result when chunks run concurrently, Go cannot promise
that order. `eventGroups` splits the events into `pre` (before the first chunk), `chunks[]` (`label`, `tool`,
`result`) and `post`; with `concurrency > 1` compare the groups (pre as a sequence, each chunk's pair, post as a
sequence), with `concurrency = 1` the whole `events` list is ordered. The LLM script is indexed by chunk index
(`script.chunks["<i>"]`, else `script.default`); each step is `{review, raw, usage[]}` (the `usage` entries are reported
one by one to the usage observer, as the provider reports every attempt) or `{error, usage[]}`. `input.cancelAtPoll: n`
makes the cancellation check fire at its n-th call; `pollsMade` is the number of calls. `mode_*` cases only assert
`outcome.mode` and `outcome.chunks` (labels) for strategy/size combinations (the 400-line threshold counts
additions + deletions; the threshold itself stays single pass).

### `runs.json` scenarios

`input` has everything a Go test needs to replay a scenario: `pull`, `repo`, `prFiles` (rows `{path, patch}` in byte
order), `jobs` (runId + the agent), `skills`/`projectContext`/`repoIntel` per agent key (`sec`, `gen`, `mr`) as the
fakes return them, `llm` (`mode` `script` with resolved steps, `mock` with `delayMs`, or `http` with the provider
responses), `mapConcurrency`, `actions` (`{at: "llmCall:<k>" | "diffLoad", cancel: <runId>}`: the cancel route
`ReviewService.cancelRun` runs at that point), `persist.completeReturns: false` (a cancel lands during the persist),
`storedDiffError`, `skillsError`.

Output: `events[runId]` is the run bus buffer after the executor returned (shared pre-work is fanned out to every
run of the batch); `busComplete`; `calls` the port calls in order (`git.diff` always fails: Go reads `pr_files` only),
with store writes as recorded statements (`kind`, `table`, `values`/`set`: the real Node repository functions ran on a
recording handle; Go column names differ, the values are the contract: finding insert mapping, `outOfScope`
forced false, `kind` default `finding`, `skillId` resolved by exact name, `lastReviewedSha`, run completion stats);
`tx.commit`/`tx.rollback` mark the persist transaction; `finalRuns` the resulting agent_runs state
(**emulated** from `handwritten/run_status_transitions.json`, not read from a database); `traces[runId]` the saved
RunTrace (validated against the shared contract); `llmCalls` the provider requests (`chunkIndex`, messages,
`sessionId`); `promptLog` the prompt-log entries; `llmHttpRequests` for `http` mode (request bodies with
`session_id`).

Comparison rules for S25: per run, compare `events` after dropping `t` and normalising `\(\d+ms\)`; compare `data`
as JSON values (key order matters on the wire: `OversizeWarning`, `FileData`, skills data); compare the statements
and `finalRuns` ignoring `durationMs`; compare traces ignoring `stats.duration_ms`, `tool_calls[].ms` and `log[].t`.
Scenarios with several jobs run the agents one after another in Node (`runBounded` unset); Go runs them with
`REVIEW_AGENT_CONCURRENCY`, so compare per run, not across runs. The cancel scenarios (`cancel_*`) pin the
semantics (status `cancelled`, error `Cancelled by user`, usage recorded, final event `Run cancelled by user`); the
position of `Cancellation requested — stopping…` between other events depends on timing in Go.

### `sse.json`

`streams[]` replay (and, for `single_agent_done_with_every_context_section`, follow live) the runs of `runs.json`;
`synthetic[]` publish `events` on a bus (`connectFirst` = the client connects first and the run is `running` in the
database); `finalStatus[]` are runs the bus no longer holds (`dbStatus`/`dbError` in, the single synthetic event
out); `errors[]` are rejections before streaming (404 `Run not found`, 422 for a non-UUID id; the 422 `details`
shape is the Fastify zod provider's). `response.body` is the exact byte stream (UTF-8): `retry: 3000\n\n` then
`id: <seq>\nevent: <kind>\ndata: <json>\n\n` per event; `headers` exclude `date` (Go's headers also carry the
Phase 1 security/CORS headers, which this Node app does not register); SSE `t` values are `HH:MM:SS` of the logical
clock: compare after replacing every `"t":"..."` value. `escapingSensitive: true` marks the one case (U+2028/U+2029)
where Node emits the raw characters but Go's `encoding/json` always escapes them: the spec excepts JSON string
escaping from NFR4002, so compare that case after decoding the `data:` JSON.

## Hand-written expectations (not generated)

`../handwritten/*.json` (each has `"handWritten": true`, the spec items and the Node source it was read from):

| File | Why it is not generated |
|---|---|
| `prompt_surrogate.json` | EC4015: Node leaves a lone surrogate when the 4,000-unit cut splits a pair; Go drops it. The generator verifies the stated Node behaviour and the sizes against `assemblePrompt`. |
| `run_status_transitions.json` | The agent_runs updates are SQL (`CASE`, conditional `WHERE`): complete/fail/cancel/reap row states. |
| `estimates.json` | `agentRunEstimates` is a window-function query. |
| `multi_agent_columns.json` | `columnsForMultiAgentRun` assembles columns with SQL joins. |

## Parity traps the corpus pins (found while generating)

- JS `\s` and `String.prototype.trim()` are Unicode-aware (U+00A0, U+2003, U+2028, **U+FEFF**); Go's RE2 `\s` is ASCII and
  `strings.TrimSpace` does not trim U+FEFF. They decide `wrapUntrusted` tags (`prompt.json` `wrap`: `<` followed by U+00A0,
  U+2003 or U+FEFF before `untrusted`) and whether a PR description or callers/repo-map text is blank.
- RE2 `(?i)` folds `s`/`ſ` (U+017F); JS `/i` without `u` does not (`<untruſted>` stays unchanged in Node).
- `extractReferences` uses a backreference + lookahead string-blanking regex and a lookbehind; RE2 has neither (`callers.json`).
- `parseUnifiedDiff` quirks are part of the contract: a `+++ ` line inside a hunk (an added `++ x` line) re-reads the
  file path, `+++x` and `---x` lines count as context, `\ No newline at end of file` and blank lines advance the new-line
  cursor, a trailing newline adds one context line, quoted paths keep their quotes, a pure-deletion hunk grounds on its
  declared new range.
- `splitOversizeDiff` cuts at the last `\n` before `cap - len(note)`; a single huge added line therefore disappears
  entirely from a truncated unit (`large_one_unit_over_the_default_cap_is_truncated`).
- Float sums, `Math.round` half-up of the mean score, `localeCompare(…, 'en')` ordering (ICU) of agent names, and
  UTF-16 file-name order in conflict groups (an astral file name sorts before U+FF5E) differ from byte order.
- V8's `JSON.parse` error texts (`Unexpected token 'o', "oops" is not valid JSON`) are in `schema.json` and `llm.json`;
  Go cannot reproduce them, compare the `Output was not valid JSON: ` prefix and the reprompt tail.
- Node's `storedDiff` reads `pr_files` without `ORDER BY`; the corpus feeds rows in byte order (D4003). Node's
  callers scan walks `readdir` order; the generator sorts references by path bytes then line (D4004).

## Not covered here

The 409,600-byte read cap and the symlink rules of Go's `CallerSignatures` (spec 7.11) differ from Node, which reads
changed files without a cap and follows symlinks; S13's own tests cover them. Project-context budgeting (S14) is
tested there; the run scenarios take the resolver's documents as given. No recorded real-provider response exists
(D4012); the real-key walkthrough is AC4077 (manual).
