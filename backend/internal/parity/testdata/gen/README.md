# Parity corpus and Node golden generator (Phase 5 S21; Phase 6 S16/S17 extend it, see the last part)

`../corpus/` holds committed inputs (cases, fixtures). `../goldens/*.json` are the expected outputs, produced by
running the **Node** implementation of dev-digest (commit `d1c9093`) from the read-only checkout `<dev-digest>`
(here `/home/ypodop/IdeaProjects/dev-digest`): the intent, smart-diff, blast, brief, onboarding and performance
modules (domain functions, the real application services over in-memory fakes of their ports, a scripted LLM, the real
`LlmXxxModel` adapters over a fake provider, the real Fastify route plugins), the Node mock LLM, the schema
converter (`toJsonSchema`, `parseWithRepair`) and the git adapter over a throw-away repository.
`../handwritten/*.json` are the expectations of the parts that cannot run without a database. Spec: FR5057, NFR5001,
AC5063, D5017, D5019; plan S21/P14. Consumers: S30 and S31 (parity tests), the repository/transport lanes (S24,
S25, S27, S29, S17) and the component tests (S34–S36) for `routes.json`.

## Regenerate

From this repository, with `TMPDIR` a **short** scratch directory outside both checkouts (tsx writes its cache there;
a long path such as the Claude scratchpad breaks tsx's IPC socket with a spurious `EADDRINUSE`):

```sh
cd backend/internal/parity/testdata
DD=/home/ypodop/IdeaProjects/dev-digest
mkdir -p /tmp/p5gen
TMPDIR=/tmp/p5gen $DD/server/node_modules/.bin/tsx --tsconfig $DD/server/tsconfig.json \
  gen/generate.mts --dd $DD --out goldens
```

`--only intent,smartdiff,blast,brief,onboarding,performance,schemas,routes,git,evals,ci,wfrules,runner` regenerates a subset (no `meta.json`; the Phase 6 parts write `meta6.json`).
The generator refuses to run unless `<dev-digest>` is at `d1c9093` with a clean `git status`, checks that again after
the run, uses only read-only git commands (`--no-optional-locks`), writes only below `--out` (which must lie inside
`testdata/`; temp repositories and clones go under `$TMPDIR`), and never installs anything: it needs the checkout's own
`server/node_modules` and `reviewer-core/node_modules`. It also requires the English default collation locale
(`en-US`; the Node code uses `localeCompare` without a locale) and records locale, ICU and Node versions in
`goldens/meta.json` (a different Node/ICU can change that file and, in principle, collation-dependent goldens).

Reproducibility: check against a copy of the first run (do **not** use `git add -N` + `git diff`):

```sh
cp -r goldens /tmp/p5gen/run1 && <regenerate command> && diff -r /tmp/p5gen/run1 goldens && echo IDENTICAL
```

Determinism: `TZ=UTC`; the global `Date` is replaced by a logical clock in the brief/onboarding flows (1,500 ms per LLM
call; `durationMs` is not parity data); fake timers replace `setTimeout` where a 120 s budget or a 10 s deadline is
exercised; the only real timers are 40 ms budgets whose outcome is deterministic; the git fixture uses a fixed
identity and fixed dates, so its commit sha is reproducible; no random data; file walks are sorted.

## Corpus placeholders (corpus files only; goldens carry the expanded values)

`\u{HEX}` in a string = that code point (lone surrogates allowed) · `{"$repeat": [text, n]}` · `{"$concat": [a, b…]}` ·
`{"$each": [n, template]}` (`{i}`, `{i3}`, `{"$lin": [a, b]}` = a + b·i) · `{"$commits": [n, len]}` · `{"$files": n}` ·
`{"$items": n}` · `{"$seed": path}` / `{"$mock": NAME}` (Node seed patches of PR #482 and the Node mock LLM fixtures).

## Conventions of the golden files

- JSON, 2-space layout of the Phase 3/4 goldens (short flat objects on one line). Every file has a `note`.
- Goldens carry their inputs (cases are self-contained). Exceptions, all marked by a string value:
  `{"$blob": true, "len", "sha256", "head", "tail"}` = a string longer than 16,384 UTF-16 units (compare `len` in UTF-16
  units and the SHA-256 of the UTF-8 bytes); `"$response.brief"` / `"$response.tour"` (in `upserts[]` / `storedAfter`) =
  equal to the response's `brief` / `tour`; `"$same"` (brief `prompt[].trim.userMessage`) = equal to `userMessage`;
  `"$ONBOARDING_SYSTEM_PROMPT"` (onboarding `prompt[].messages[0].content`) = `constants.systemPrompt`.
- Lone surrogates appear as `\udXXX` escapes (Go's decoder turns them into U+FFFD; the cases that contain one say so).
- Texts that come from GitHub/Octokit in Node (ticket failure reasons, history failures) are scripted in the corpus (D5006).
- Errors are `{name, kind, code?, message, details?}` (Node `AppError` fields; the HTTP status is the Phase 1 table).
- Log lines are `{level, msg, obj}`; `Error` values inside are shown by their message.

## Goldens

| Golden | Node code | Top-level keys → what to compare (consumer) |
|---|---|---|
| `intent.json` | `intent/domain/{links,intent,classification,constants}`, `IntentService`, `GitDocSource`, `GitHubTicketSource`, `toPrIntentRecord`, `renderIntent` | `constants` (verbatim prompt texts, limits); `tickets`, `docs` (AC5019/20: refs + skipped, `{error: "URI malformed"}`); `keys` (AC5021: canonical JSON + `input_hash`); `text` (substantive body, `truncateText`, clamps AC5025, confidence, sources block, user message, `renderIntent`); `sources` (doc/ticket reads over fakes); `flows` (AC5022/5028: `resolve`/`get`/`refresh` → result/response/error, `events`, `llmCalls`, `promptLog`, `upserts`, `storedAfter`; world + `stored` baseline included); `mapper`; `scenarios` (single flight, cancellation, 120 s budget; S23/S30) |
| `smartdiff.json` | `smart-diff/domain`, `SmartDiffService` | `classify` (170 paths, AC5043), `build`, `service` (tables → body; the "latest review per agent" SQL is emulated) (S14, S30) |
| `blast.json` | `blast/domain`, `repo-intel getBlastRadius`, `BlastService`, `RipgrepCodeIndex` | `mapping` (AC5042), `history` (selection), `service` (logs, history failures), `persistent` (AC5038: tables → `port` + `blast`), `clone` (AC5039: fixture + `extraFiles` → `port` + `blast`) (S13, S15, S25, S30) |
| `brief.json` | `brief/domain`, `BriefService`, `LlmBriefModel`, `GitHubIssueSource` | `constants`; `input` (ranges, selections, blast, linked issue, missing inputs, truncate); `prompt` (user message, sections, estimate, trim); `normalize` + `stale`; `stored` (contract validation, key order); `flows` (AC5009–5011, 5044–5049: response/error, `logs`, `llmRequests`, `promptLog`, `specCalls`, `upserts`); `scenarios` (409 marker); `issueSource` (S18, S26, S27, S31) |
| `onboarding.json` | `onboarding/domain`, `OnboardingService`, `LlmOnboardingModel`, `GitRepoFiles` | `constants`; `redact` (AC5050); `command` (AC5051); `input` (excerpts, scripts, env, TODO, untested, test paths); `tour` + `stale` (AC5052); `prompt` (estimate, trim, messages); `stored`; `flows` (AC5013/5014/5054); `scenarios`; `repoFiles` (bounded reads) (S19, S20, S28, S29, S31) |
| `performance.json` | `performance/domain`, `PeriodQuery`, `PerformanceService` | `constants`; `period` (`queries` = issues, `datetimes`, `windows`); `folds` (metrics, summaries, cost segments, groups, trends, severity, usage); `datasets` + `service` (AC5015/5016/5055: tables → dashboard/stats body; the repository SQL is emulated) (S16, S17, S31) |
| `schemas.json` | `toJsonSchema`, `parseWithRepair`, `MockReviewLLMProvider`, `LlmIntentModel` | `schemas.<Name>.strict` (AC5057: provider-facing schema), `schemas.<Name>.parse` (validator issues / reprompt texts), `mock` (fixtures, AC5058), `calls`, `constants` (S6, S11, S18, S20, S30) |
| `routes.json` | the 11 real Fastify routes over the real services | `groups.<name>[]`: `{request, status, contentType, body}` raw responses incl. 422/404/409/502 envelopes (S24, S25, S27, S29, S17; component tests S34–S36) |
| `git.json` | `SimpleGitClient.readFileAt` over a fixture repo | `results[]`: content/size or the exact error text (cap 65,536 / 524,288 bytes, sha and path rules, decoding) (S5, intent doc source, onboarding reader) |

### How the services are run

`flows` build the real service with fake ports (`pulls`, `store`, `model`, `docs`, `tickets`, `index`, `files`, …).
Anything SQL is emulated in the generator and says so in the file's `note`: the intent store (numeric(12,6) cost
rounding), the smart-diff and brief "newest review per agent", the blast reader (symbols ordered by path, line, name in
byte order; callers by rank desc, from_path, line, to_symbol; D5009), the performance repository (counted-run filter,
window bounds, grouping). The goldens therefore pin the *logic*; the Go repositories must reproduce the emulated SQL
semantics (their `pgtest` tests seed the golden's tables).

## Hand-written expectations (not generated)

`../handwritten/*.json` (each has `"handWritten": true`, the spec items and the Node source it was read from):
`intent_store.json` (get/upsert/commit order/cost column), `brief_store.json` (file and review ordering, upsert, read
rule), `onboarding_store.json` (upsert, read rule, root file list).

## Parity traps and spec deviations found while generating (the golden decides)

- **Period query**: format errors (`invalid_string`, from `datetime`) are *non-fatal* in zod: the custom issues run too
  (`period=1d&from=2026-01-01` gives both the format error and "from is only allowed with period=custom"); only type
  errors and an invalid `period` enum stop the refinement (spec 7.11.1 says format errors come first and alone).
  A date-only `from` still counts for the span check (`Date.parse`). Zod also accepts offsets `+24:00`, `+99:99`,
  `+05:60`, which `Date` cannot parse: the span checks are skipped and Node then answers **500** (`RangeError`).
- **Onboarding budget**: Node only checks the budget *after the blob reads* ("Generation timed out"); an expiry
  *during the model call* aborts the provider call and is answered "The model call failed" (spec 7.10.4 says as brief).
- **Brief** logs: `durationMs`/`evt` shapes as in `logs[]`; a blast read failure is `{blast, degraded}` without detail.
- **Link extraction**: the blob URL regex `[^/]+` (ref segment) spans spaces and `)` across links; JS `\b`/`\w`/`\d` are
  ASCII; ticket numbers go through `Number` and print like JS (`1e+21`, `#1.2345678901234568e+29`).
- **`derived_from`**: a CRLF body whose headings end in `\r` keeps them (EC5001); `.` does not match `\r`, U+2028, U+2029.
- **Smart diff globs**: `**` is `.*` which does not cross `\n`, `\r`, U+2028/2029 (such paths fall to `core`).
- **Package scripts**: `JSON.parse` semantics (duplicate keys keep the first position and the last value, array-index
  keys first, `__proto__` is an own key, a BOM fails to parse).
- **Redaction / command safety**: JS `\s` is Unicode (NBSP, U+FEFF), `/i` never folds U+017F / U+212A (`baſh`, `mKfs`
  are not flagged); `sudo=1 make` is flagged.
- **Git adapter**: `git show` content is returned as is (trailing newline, BOM, CRLF kept; invalid UTF-8 gives one
  U+FFFD per maximal subpart); paths are normalised (`docs//a.md`, `./a`, `a/../b`); a directory path returns a tree
  listing; the cap counts bytes.
- **Intent**: a caller that joins with an already aborted signal leaves the flight's rejection unobserved in Node
  (`nodeUnhandledRejections`); Go has no such effect. A cache hit prints `confidence=null` for a NULL column.
- **HTTP**: a POST with a malformed JSON body is 400 `internal_error` in Node (Fastify parser); the Go routes ignore
  bodies (spec 7.1). A `+` in an unencoded query string is a space (422 for a datetime).
- **Blast clone scan**: dotted `Class.method` names are changed symbols there (the persistent path skips them); the
  walk is taken in full-path byte order (the Go scan order), not readdir order; hostile-clone bounds (100,000 files,
  1,000 names, 5,000 rows, 30 s) are Go-only and are not in the goldens.

## Not covered here

Real-provider responses (no recording exists), the Postgres repositories themselves (handwritten + emulated),
`REVIEW_INTENT_ENABLED` and the review hook/engine (S22's `W/parity` goldens), the hostile-input resource limits (S13,
S28 own tests), HTTP-level concerns of the Go server (Host/Origin/CORS/rate limits).


---

# Phase 6 additions (S16 evals / ci / wfrules, S17 runner)

Four new generator parts, all running the **Node** code of `<dev-digest>` at `d1c9093` read-only, under the rules above
(clean checkout before and after, `TZ=UTC`, no real clock, no random data, sorted output, short `TMPDIR`). The Phase 5
goldens and `meta.json` are not touched by them (P15): a full run rewrites them byte-identically, `meta6.json` carries the
Phase 6 provenance (yaml, fflate, p-queue, zod, openai versions). Spec: FR6056, NFR6001, AC6021, AC6036–AC6042,
AC6050, AC6051, AC6058, AC6066; plan S16, S17. Consumers (W2, S32): evals lane C, ci lane E, workflow rules lane F,
zip reader and runner lane G.

```sh
cd backend/internal/parity/testdata
DD=/home/ypodop/IdeaProjects/dev-digest
mkdir -p /tmp/p6gen
TMPDIR=/tmp/p6gen $DD/server/node_modules/.bin/tsx --tsconfig $DD/server/tsconfig.json \
  gen/generate.mts --dd $DD --out goldens            # everything (about 12 s)
# only Phase 6:  ... --out goldens --only evals,ci,wfrules,runner
```

AC6058: `cp -r goldens /tmp/p6gen/run1`, run again, `diff -r /tmp/p6gen/run1 goldens` → no output; and
`git -C $DD status --porcelain` stays empty. The runner part starts a local HTTP stub on `127.0.0.1:<random port>`
(the port never reaches a golden) and two scenarios make the OpenAI SDK retry on 500 (about 3 s of real backoff).

| File | What |
|---|---|
| `gen/evals.mts`, `gen/evals-flows.mts` | `evals.json`: domain rules, schemas, `EvalCasesService`, `EvalRunsService`, `EvalRunExecutor` |
| `gen/ci.mts` | `ci.json`: slug, workflow, PR body, manifest, artifact, GitHub error codes, ZIP, ingest, export |
| `gen/wfrules.mts` | `wfrules.json`: `checkWorkflow` over 428 workflow texts |
| `gen/runner.mts` | `runner.json` (pure functions) and `runner_runci.json` (`runCi` scenarios) |
| `gen/common6.mts` | small helpers (`attempt`, `merge`, `showParse`, recording logger) |
| `corpus/{evals,ci,wfrules,runner}/*.json` | inputs |

## Corpus-only placeholders (Phase 6; goldens carry the expanded values)

Those of Phase 5 (`\\u{HEX}` written as `\\\\u{HEX}` inside JSON, `$repeat`, `$concat`, `$each`) plus: `{"$diff": name, "eol"?}` (a diff of
`corpus/evals/content.json`), `{"$workflow": {slug, triggers, postAs}}` (the Node workflow text), `"$absent"` (delete the
key from a merged artifact / run), `"$unset"` (delete an environment variable), `"$grounded"` (a grounded finding plus a
phantom at line 999), `{"$runs": {agent, n, startAt, stepHours, ...}}` (n finished runs, newest first),
`"$skill1"`, `"$skill2"`, `"$skills"` (the executor skills). In the **goldens**: `{"$number": "Infinity"}` replaces a
non-finite number (JSON cannot carry it); strings over 16,384 units are `$blob` digests as before (rebuild from the
corpus when you need the text: `wfrules.json` texts, `evals.json` `diffs`, big bodies in `manual`/`fromFinding`).
Lone surrogates (`\udXXX` escapes, Go turns them into U+FFFD) appear in: `evals.json` `caseNames`/`fromFinding` (a name
cut inside an emoji) and `errorText` (cut at 500), `ci.json` `slugs` (inputs) and `artifacts` ("escaped lone surrogate"),
`runner.json` `artifacts` ("lone surrogate in a string": input and result).

## Goldens (Phase 6)

All cases are self-contained. `result` / `error` pairs are `attempt()` outcomes: `{result}` or `{error: {name, kind, code?,
message, details?}}` (Node `AppError`; the HTTP status comes from the Phase 1 kind table). `calls` lists the fake port
calls of a step, `logs` the `(level, msg, obj)` lines.

| Golden | Top-level keys → what to compare (consumer) |
|---|---|
| `evals.json` | `constants` (limits, thresholds, shared constants); `rounding` (`roundTo`: `Math.round(x·10^d)/10^d`, half toward +∞), `deltas` (`metricDelta`), `intersect` (either bound order); `caseNames` (name from title/location, cut at 120 UTF-16 units), `singleFile` (`diff --git a/p b/p`, `---`, `+++`, patch joined by `\n`), `normalize` (CRLF→LF; a header is a line starting `diff --git` or `+++ `); `fromFinding` (7.4.1 steps 3–4: `source` → `NewEvalCase` or the 422 code, check order decision → agent → patch blank → patch > 200,000 units); `diffs` (the named diffs of `content`/`create`/`edit`), `content` (`checkCaseContent` with the **server** diff parser, i.e. a trailing newline adds one context line: `{ok:true}` or the 422 with `details.files` ≤ 50 first-seen paths / `details.ranges` ≤ 50 merged runs); `manual` (the create pipeline without owner/limit/insert: schema `parse`, normalise, content check, `buildManualCase`), `edit` (`mergeCaseEdit`: `values` (only written fields, `markContentEdited`), `recheck`, `contentChanged`, `recheckResult`; 400 `validation_error` for merged end < start); `taskLines` (7.5.4 text), `errorText` (whitespace collapsed with JS `\s`, trimmed, cut at 500 units, `Unknown error` when empty), `usage` (case cost: null without a report or with an unpriced one; `rounded` = round(sum, 6)), `kept`, `skillBlocks` (`### name` + `\n_Applies when:_ d` when the description is non-blank, `\n\n`, trimmed body); `score`, `aggregate` (+ 40 seeded random runs), `verdict`; `lineDiff` (full results, or `summary` {count, ops RLE, sha256 of `op\tline\n` joined} for `compact` cases built from `gen`: `head-i`/`tail-i` shared, `a-i`/`b-i` different, `alternate` = `l-i` vs every other `m-i`), `compare` (`compareRuns`, A order then B-only, `absent`, order of the 422 checks: owner kind, agent/skill, running), `alerts` (`regressionAlert`, delta rounded first, ≤ −0.02), `series` (`sparklineOf` 10 oldest first, `trendOf` for several caps), `periods` (`periodStart`); `validation` (Zod issue lists of `EvalRunListQuery`, `EvalCompareQuery`, `EvalSkillRunStartInput`, `EvalAgentDashboardQuery`); `casesService`, `runsService` (steps over fakes: results, store `calls`, logs; the SQL of the dashboard reads is **emulated** in the generator: newest first as given, `since` = started_at ≥ since), `executor` (see below) (lane C: S7–S9, S32) |
| `ci.json` | `constants`; `slugs` (NFKD, strip U+0300–U+036F, JS lower-case, runs outside a-z0-9 → `-`, cut 40, strip `-`, fallback); `triggers` (`normalizeTriggers`); `workflows` (Node `buildWorkflow`: still has "Set up Node.js" and `node .devdigest/runner/index.js`, the Go text differs on purpose, D6007) incl. the invalid-argument errors; `prBodies` (no "### Runner" paragraph in Node, D6008); `manifests` (`buildManifest` values and the 422 `issues`) with Node's `yaml`; `yamlValues` (≈95 strings as name/system_prompt: Node's YAML text and whether Node and the runner parser read it back to the same values; the Go emitter must round-trip all of them); `artifacts` (`parseResultArtifact`: BOM stripped, duplicate keys last wins, ints up to 1e21 accepted, invalid UTF-8 refused), `verify` (`verifyArtifact`), `githubErrors` (`githubErrorCode`: header key exactly `x-ratelimit-remaining`, value `"0"` or `0`), `zips` (Node-built ZIPs, fixed mtime; `zipBase64` ≤ 12 KiB, else `zipSha256` and rebuild from `spec`; `entry` = the zip reader outcome, `result` = `readResultArtifact`; Node does not check CRCs and ignores other entries; `padToSize` zips are exactly 1 MiB / 1 MiB + 1); `requestSchemas`; `ingest` (26 `CiIngestService.refresh` scenarios: `result`, `recorded` (the `NewCiRun`s), `githubCalls`, `storeCalls`, `logs`; clock = latency per GitHub call, deadline 45 s); `export` (20 `CiExportService` scenarios: check order, files, publish calls, error codes) (lane E: S10–S12, lane G: S14; the Node export files list differs from Go's) |
| `wfrules.json` | `cases[]`: `{name, text, bytes, result: {ok:true} / {ok:false, rule}, stage, yamlCode?}`; `stage` = size \| parse \| shape \| rule \| ok (diagnosis only) (lane F: S13) |
| `runner.json` | `constants`; `manifestPaths` (`findManifestPath`: slug check, one manifest, error texts), `manifests` (`loadAgentManifest`: parsed value or `... failed validation: <path or (root)>: <message>; ...`), `skills` (`loadSkillBodies` + `reads`), `contexts` (`resolvePrContext`), `intOrNull`, `strips` (`stripIgnoredFiles`), `parses` (runner diff parser: files, additions, deletions, hunks with `newLineNumbers`; the trailing empty line is dropped), `gates` (`countBlockers`/`gateTriggered` per policy), `payloads` (`toReviewPayload`: event, body, inline comments anchored to the diff line closest to `end`), `artifacts` (`buildResultArtifact` + `json` = the exact file text), `fetches` (the three GitHub calls: URL, headers, bodies, error texts) (lane G: S15) |
| `runner_runci.json` | `scenarios[]` (59 `runCi` runs: `input`, `result`, `artifactFile`, `reads`, `writes`, `requests`, `llmRequests` with byte-exact messages, `logs`); `openrouter` (`reviewSchema` + 16 runs through the real `OpenRouterProvider` and a local stub: `httpRequests` with the request body, `result`; costs, re-prompts, retries) (lane G: S15, S32 runner differential, AC6051) |

### `executor` (evals.json)

Each scenario: `job`, `cases` (`null` = deleted), `script` (per case, per LLM call: `cost` (absent = nothing reported, `null` = unpriced),
`findings`, `error`, `costOnError`, `hang`), `steps` (default: wait for the final write; `settle`, `fire: 120000` = the case budget, `shutdown: 5000`),
and `result`: `loads`, `resolverCalls`, `llmRequests` (case, call index, model, `temperature` 0, schema name, `hasSignal`, **byte-exact messages**: task
line, `## PR description` only when non-empty, `## Skills / rules` blocks, `## Diff to review`), `records` (`CaseRecord`s, **sorted by case order**: Node
records in completion order), `recordAttempts`, `dropped`, `finals` (`RunFinal`), `shutdown`, `logs`. `durationMs` is always 0 (constant clock). Two scenarios
leave the "first error" ambiguity out (all errors have the same text); `strategyVariants` runs the same job with each strategy value (`""`, `fancy`,
`MAP-REDUCE` → auto). Timers are fake; the generator fires them.

## Parity traps and spec deviations found while generating Phase 6 (the golden decides)

- **Export `workflow: ""`** is not "non-empty else generated" (spec 7.6.1): Node uses `??`, so an empty string is checked as text and gives 422
  `workflow_insecure` / `invalid_yaml` (`ci.json export`, "a workflow edited in the preview"); `null` is generated.
- **Runner `GITHUB_REPOSITORY`** `a/b/c` → owner `a`, repo `b` (`split("/", 2)`), not "after the first /"; `a/` gives an empty repo. **`PR_NUMBER`** goes
  through `Number()`: `" 12 "`, `"+12"`, `"0x10"`, `"1e2"`, `"007"`, `"2.0"` are accepted; `"1.5"`, `"Infinity"`, `"abc"` refused; the event number is
  `String()`-ed first (a string `"9"` works). Node does not cut or redact GitHub error bodies and does not validate skill slugs (`../x` is read).
- **Workflow rules (JS regex / string semantics the Go port must reproduce):** `\s` is Unicode (NBSP, U+FEFF, U+2028, VT, FF count; U+200B does not) in the
  fork-guard collapse/trim; `toLowerCase` uses full mappings (`İ` → `i̇`, so `TİTLE` is not `title`); `/i` does not fold `ſ` (`${{ ſecrets.X }}` is no
  secrets reference); the pinned-action regex needs a look-ahead RE2 lacks (`owner/.@sha`, `owner/..@sha` refused, `owner/...@sha` and `owner/repo/../x@sha`
  allowed); `$` does not match before a trailing newline; steps that are not a list or not mappings are skipped (not checked); an `if` key anywhere (even
  under `env`) is read as an expression; expressions are only `${{ … }}` bodies of string scalars plus every `if` string, keys are not inspected.
- **YAML (Node `yaml` 2.9 vs yaml.v3, found with a throw-away yaml.v3 run over the corpus):** duplicate keys are a parse error in Node (yaml.v3 does not check on
  `yaml.Node`: check explicitly); alias budget = for each alias `count · aliasCount > 100` (`count` = times the anchor was used, `aliasCount` = 1 for a scalar
  or the largest nested `count · aliasCount` of a collection): 99 uses of a scalar anchor pass, 100 fail; a `%YAML 1.2` directive is accepted by Node and refused
  by yaml.v3; `%YAML 1.1` makes `on` a boolean in Node (`forbidden_trigger`); NUL/control characters in comments are accepted by Node and refused by yaml.v3; a
  CR-only document is a parse error in Node and fine in yaml.v3; unknown tags (`!secret`, `!!binary`, …) only warn in Node (not an error); merge keys are plain
  keys; a trailing `---` or `...` + `---` is a second document (error).
- **Eval cases:** `checkCaseContent` parses with the *server* parser (a deleted-file section has no path and vanishes: `diff_invalid`; a `+++ ` line inside a
  hunk is a header; a trailing newline adds one context line); lengths are UTF-16 units. `caseName` and `errorMessage` cut by units, so they can split an emoji.
  `mergeCaseEdit` returns `values.expectation` for a same-value edit (no `markContentEdited`); a patch `{}` still calls the store with `{}`. `runAll` fails
  whole with 404 when an agent vanishes mid-way (earlier runs stay launched). `compare` reads both sides before answering 404.
- **Ingest:** artifacts with equal `created_at` keep the first listed; an unparsable `created_at` listed first wins for good; `owner/name` without a slash is a
  `github_unavailable` error for that installation; the deadline is checked before every GitHub call (the generator advances the clock per call).
- **ZIP:** Node ignores CRCs and picks the named entry (exact name, no `./` or directory prefix); an entry declared over 65,536 bytes is `too_large` before inflating.
- **OpenRouter (real SDK):** a reply without `choices` fails with `OpenRouter returned no choices for Review: <message>`; schema-invalid or empty replies are
  re-prompted twice (messages `assistant` + `user` appended), usage of every attempt counts; the SDK retries 5xx/429 twice with backoff, not 4xx; an empty key is
  sent as an empty bearer; fenced JSON is repaired. Costs are summed per chunk in chunk order (`0.1 + 0.2 + 0.3 = 0.6000000000000001`).

## Not covered here (Phase 6)

The Postgres stores of evals and ci (handwritten expectations in the Go repository tests), real GitHub and real OpenRouter, the Go-only hardening
(redirect handling with credentials, the 64 MiB diff cap, 1 MiB manifest/skill caps, the build id, reproducible runner builds) and the HTTP layer.
