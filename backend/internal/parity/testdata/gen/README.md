# Phase 5 parity corpus and Node golden generator (S21)

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

`--only intent,smartdiff,blast,brief,onboarding,performance,schemas,routes,git` regenerates a subset (no `meta.json`).
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
