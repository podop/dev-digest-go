# Context pack: Go backend Phase 5
`DD/` = `../dev-digest` (read-only, d1c9093), `M/` = `DD/server/src/modules`; prefixes as in the plan.

## Lane contracts (fixed; wired in S33)
- [C0] S1/S2 files are the contract
- [C1] `domain.Intent{Text; InScope, OutOfScope []string; ChangeType, Confidence *string; DerivedFrom string; Sources []IntentSource{Kind,Ref,Status; Detail *string}}`, `RenderIntent`, `IntentScopeRule`; `PromptParts.Intent`, `reviewerapp.Input.Intent *Intent`; jstext `Cut`, `LessUnits`, `SortEnglish`, `Canonical` (JSON.stringify, sorted keys), `DecodeURIComponent`, `JSNumber`, `ToFixed`
- [C2] `IntentResolver.Resolve(ctx, IntentRequest, emit func(kind, msg string, data json.RawMessage)) IntentResult{Intent *reviewer.Intent; Trace *IntentTrace}`, never errors; ctx ends when every run is cancelled; `executor.Deps.Intent` nil = off
- [C3] intent `Get`, `Refresh(ctx, ws, prID) (Response, error)`, `Resolve(ctx, ResolveInput, sink) Result`, `Stored(ctx, prID) (*reviewer.Intent, error)`, `Shutdown(ctx) error`; inflight `Markers.Begin(ctx, key) (ctx, release, ok)`, `Flights[T,E].Do(ctx, key, sink, fn)` (fn ctx ends on budget, all cancellable callers gone, `Shutdown`), `Len()`
- [C4] `(*Reader).BlastRadius(ctx, repoID, changed) (BlastResult, error)`; github `GetContent(ctx, owner, name, path, ref, max) ([]byte, bool, error)`, `ListMergedPulls(ctx, owner, name, limit)`
- [C5] blast `Radius`, `RadiusFor(repoID, changed)` (logs), `History`; brief/onboarding `Get`, `Generate`, `Shutdown`; performance `Dashboard`, `Stats`; smartdiff `Get`; cmd `now`, `budgets`
- [C6] llm as is: `Structured` with `Sections`, `OnUsage`; `apperr` Config = missing key → `provider_not_configured`

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38` — "must stay wire-compatible with the UI"
- [K2] `backend/AGENTS.md:39` — "`reviewer` (the review engine) is a shared kernel" → extended (P3)
- [K3] `backend/AGENTS.md:43-48` — "Echo only in …"; "`Raw`/`Exec` only for what the builder can't express"; "Row structs are declared per adapter"
- [K4] `backend/AGENTS.md:58` — "Request bodies only through `httpx.ReadJSONBody`" → the 3 POSTs never read the body
- [K5] `backend/AGENTS.md:32-35` — "Shutdown order: the review executor cancels live runs" → add the P2 services; refresh bucket 10/min
- [K6] `INSIGHTS.md:21` — clients "must not follow redirects"
- [K7] `INSIGHTS.md:42` — "redact provider keys before truncating" (intent warning, detail)
- [K8] `INSIGHTS.md:32` — validator mirrors Zod: `nullable`, issues in property order
- [K9] `INSIGHTS.md:16` — worker panics kill the server → recover, re-raise on the caller
- [K10] `INSIGHTS.md:15,54` — hostile scans: one pass per file, ctx tick every ~32K units
- [K11] `INSIGHTS.md:50,52,57` — collate English per sort, `JSTrim`; Go escapes U+2028/9 (canonical JSON must not); RE2 `(?i)` folds ſ/K
- [K12] `INSIGHTS.md:30` — contract goldens: `-update` after the package
- [K13] `INSIGHTS.md:35` — stats keys pinned → read `bounded`, `degradedReason`; add none
- [K14] `INSIGHTS.md:75` — `go build -o /dev/null ./...`
- [K15] `INSIGHTS.md:51` — Echo runs GET handlers for HEAD → GETs stay read-only
- [K16] `INSIGHTS.md:76,79` — tsx short `TMPDIR`; reproducibility via `diff -r`
- [K17] `INSIGHTS.md:33` — test `newConfig` zero values (`RepoIntelEnabled` false)
- [K18] `INSIGHTS.md:36` — jsonb rejects U+0000 → `SanitizeText` model/PR text before upsert
- [K19] `INSIGHTS.md:20` — JS float math in runtime float64 vars
- [K20] `INSIGHTS.md:23,63` — byte order = `COLLATE "C"`, never on an alias
- [K21] `INSIGHTS.md:66` — nullable jsonb: Scanner skipped on NULL
- [K22] `INSIGHTS.md:19` — git/GitHub bytes via `DecodeText`
- [K23] `INSIGHTS.md:62` — after a retry error check which ctx ended (timeout vs failed)
- [K24] `backend/AGENTS.md:9` — `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go …`
- [K25] `CLAUDE.md:76` project `architecture-reviewer`; `:92` contracts untouched; `frontend/INSIGHTS.md:58` `pnpm exec vitest run`

## Verified facts
- [F1] `V/app/engine.go:116` `ApplyScopePolicy(…, false)`; `V/domain/prompt.go:88` no intent; stale comments `model.go:189,233,284`, `scope.go:11`
- [F2] `W/adapters/postgres/persist.go:79` `OutOfScope: false`; `tracedoc.go:52,117,288` intent null/status only; `W/transport/http/trace.go:18,49`
- [F4] `C/app.go:423-438` executor, jobs shutdown, then `stopEcho`; `:446` grace 20 s; `:384` buckets
- [F5] `P/github/client.go:37` `ErrNoToken` "github token is not configured"; `:96` client follows redirects; `pulls.go:274` `GetIssue`
- [F6] `P/gitcli/gitcli.go:353` too-large error has no size
- [F7] `I/llm/structured.go:113` prompt log before key check; `mock.go:77` fixture switch
- [F8] `R/app/callers.go:46` `WithCallerScan`; `service.go:34` `IndexState` never fails
- [F9] `I/seed/seed.go:233-236` PR #482 files = brief fixture paths
- [F10] baseline FKs cascade (`0001_baseline.up.sql:1721,1728,1749`); no PR delete route
- [F11] `go.mod`: no goleak; `regexp2` only indirect (D5010: unused)
- [F12] `W/parity/domain_test.go:339` skips intent scope rows

## Mirrors
- `I/reviews/**`, `V/domain/schema.go`, `W/parity/testdata/gen/`, `C/phase4_test.go`; Node `M/**`

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| all | `backend/**/*.go` | go-architecture | §1 inward, ports; §5 errors; §6 goroutines |
| all | `*_test.go` | go-testing | §3 tables; §4 fakes; §5 synctest; §6 pgtest |
| S5,S13,S24,S27,S29,S33 | GitHub, git, prompts, logs | engineering-paved-path:security | A01 paths; A09 logs; token |
| S21,S22,S37 | `*.mts`, `*.json`, `*.test.ts` | — | [K16][K25] |
| S1,S33 | env, compose, AGENTS.md | — | [K5] |

## Verification sources
- `Makefile:111`, `:34`; vitest [K25]; generator `W/parity/testdata/gen/README.md`

## Risks
- P2 unproven until S4 (S23, S26, S28, S33 depend)
- Node SQL without order → D5009 fixed orders; goldens decide
- Onboarding heap, hostile blast scan → P10, P11 tests

## Notes for reviewers
- Base `54f2b7e`; arch [K25], P3; security: token, prompts, doc paths, redaction
