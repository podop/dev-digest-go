# Context pack: Go backend Phase 4 (reviews)
`DD/` = `/home/ypodop/IdeaProjects/dev-digest` (read-only, d1c9093), `N/` = `DD/server/src`; prefixes as in the plan.

## Lane contracts (fixed; wired in S24)
- [C0] S1's code is the contract: `V/domain/model.go` (diff, Chunk, Mode, Finding, Outcome, Assembly, §7.1 constants), `V/app/ports.go` (`LLM`, `Events`), `W/domain/model.go` (Run, status, Trace, multi-agent, Memory), `W/app/ports.go` (Store, Tx, Bus, Providers, RepoIntel, ProjectContext, Clock), `W/app/api.go` (Service methods + I/O types)
- [C1] `reviewerapp.Run(ctx, in Input, llm LLM, ev Events, opts Options) (Outcome, error)`; Options{MapConcurrency, Cancelled func() bool}
- [C2] runbus: `New(Options{Now, AfterFunc, TTL, Backlog})`; `Publish(runID, kind, msg string, data json.RawMessage) Event`; `Subscribe(runID) *Subscription` (`C()` replay+live; closed on complete/overflow); `Has`, `Buffer`, `Complete`, `Cancel`, `IsCancelled`, `OnCancel(runID, fn) (stop func())`, `CancelAll() []string`, `CompleteAll()`
- [C3] executor `New(Deps, Options)`, `Start(ctx, Batch)`, `Shutdown(timeout)`; transport uses only `W/app/api.go`
- [C4] llm: `Request.{SessionID string, Chunk *Chunk{Index,Total}, OnUsage func(AttemptUsage{TokensIn, TokensOut int; CostUSD *float64})}`, `PromptSection.{Role string, Truncated *bool}`, `Attempt.SessionID` (OpenRouter body `session_id`), `(*Client).Ready(p) error` (`<KEY> is not configured`), `StrictSchema(raw)`
- [C5] intel `(*Reader).CallerSignatures(ctx, repoID string, changed []string, limit int) ([]domain.CallerSignature, error)`; `(*pcapp.Service).RunContext(ctx, RunContextInput) (RunContext, error)`; gitcli `BaseCommit(ctx, owner, name, baseRef, headSHA) (string, error)` ("" = none), `BlobAt(ctx, owner, name, commit, path string, max int64) ([]byte, error)` (`ErrBlobTooLarge`, `ErrBlobMissing`); httpx `OpenStream(c) (*Stream, error)` (`Retry`, `Event`), `RateLimit.Exempt []Route`; config `ReviewAgentConcurrency`, `ReviewMapConcurrency`

## Repo rules and notes that apply
- [K1] `CLAUDE.md:38` — "must stay wire-compatible with the UI"
- [K2] `backend/AGENTS.md:35` — "Echo only in … `transport/http` packages and `cmd/`. GORM only in `adapters/postgres`"
- [K3] `backend/AGENTS.md:39-40` — "`Raw`/`Exec` only for what the builder can't express … Row structs are declared per adapter" → P17
- [K4] `CLAUDE.md:43-45`, `backend/AGENTS.md:50` — "Request bodies only through `httpx.ReadJSONBody`" → action routes never read the body
- [K6] `INSIGHTS.md:18` — LLM clients "must not follow redirects"
- [K7] `INSIGHTS.md:35` — "redact provider keys before truncating"
- [K8] `INSIGHTS.md:51` — retry.Value: "check which context is done after the error" → cancel vs failure
- [K9] `INSIGHTS.md:19` — "keep ParameterizedQueries: true"
- [K10] `INSIGHTS.md:37` — stores join a tx only via `postgres.Conn(ctx, db)`
- [K11] `INSIGHTS.md:50` — `httptest.NewTestServer` in synctest; URL after `Client()`
- [K12] `INSIGHTS.md:57` — log middleware renders errors itself → SSE errors only before headers
- [K13] `INSIGHTS.md:20,52` — byte order = `COLLATE "C"`, never on a select alias
- [K16] `INSIGHTS.md:64,67` — tsx needs a short `TMPDIR`; compare against a copied first run, not `git add -N`
- [K17] `INSIGHTS.md:27` — freeze a tx: trigger + advisory lock, poll `pg_locks`
- [K18] `INSIGHTS.md:33` — startup work before listen "must not be fatal" (reap)
- [K19] `INSIGHTS.md:34` — SQLState via `errors.As` → 23503 = run deleted
- [K20] `INSIGHTS.md:26` — test config: `RepoIntelEnabled` false; `:28` S13 adds no stats key
- [K21] `backend/AGENTS.md:9` — `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go …`; `INSIGHTS.md:63` `go build -o /dev/null`
- [K22] `CLAUDE.md:76` project `architecture-reviewer`; `:92` contracts untouched; `frontend/INSIGHTS.md:58` vitest exec
- [K23] `INSIGHTS.md:66` — write BOM/emoji escapes in tests via python (EC4015)
- [K24] `INSIGHTS.md:16,29,46` — clone bytes via `DecodeText`; sanitise U+0000/bad UTF-8; RE2 `(?i)` ≠ JS `/i`

## Verified facts
- [F1] `echo/v5@v5.4.0/server.go:113` `ReadTimeout: 30s`, no WriteTimeout; `C/app.go:386-390` sets only header/idle timeouts; `response.go:82,106` Flush, Unwrap
- [F2] `I/llm/promptlog.go:52-55` role system only for `system`; `schema.go:126` unexported; `structured.go:143-150` usage hook
- [F3] `mock.go:74` only `ConventionExtraction`; `openaicompat.go:159` `usage.include` (OpenRouter)
- [F4] `C/app.go:148-159` non-fatal startup; `:361-392` Echo start ctx = runCtx
- [F5] `P/httpx/ratelimit.go:58` only `/health*` exempt
- [F6] `I/repointel/app/reader.go:31` `NewReader(src, enabled)`; `domain/model.go:21` 409,600 B
- [F7] `I/seed/seed.go:196-209` PR #482 head `a1b2c3d4e5f6`
- [F9] `N/modules/reviews/application/run-executor.ts:31-55` deps are ports; tsconfig paths resolve reviewer-core; command as `I/repointel/parity/testdata/gen/README.md`
- [F10] `N/modules/reviews/domain/multi-agent.ts:41` `localeCompare(…, 'en')`, then run id
- [F11] `I/skills/domain` already imports `golang.org/x/text`

## Mirrors
- `I/repointel/`, `C/phase3_test.go`; Node `N/modules/reviews/**`, `DD/reviewer-core/src`, `N/platform/sse.ts`

## Skill map
| Step | Files | Skills | Key rules |
|---|---|---|---|
| all | `backend/**/*.go` | go-architecture | §1 inward, ports; §5 errors; §6 goroutines; §7 tx |
| all | `*_test.go` | go-testing | §3 tables; §4 fakes; §5 synctest; §6 pgtest |
| S2,S4,S13,S14,S21,S22 | stream, git, clone, logs | engineering-paved-path:security | A01 paths; A02 Host/CORS; A09 logs |
| S12,S28 | `*.mts`, `*.test.ts` | — | [K16][K22] |

## Verification sources
- `make backend-check` — `Makefile:111` · `docker compose build backend` — `Makefile:35` · vitest [K22] · generator [F9]

## Risks
- P2 unproven until S2 (S22/S24 depend)
- jsonb rejects `\u0000` → sanitise in `EncodeTrace`
- ICU vs x/text collation, JSON escaping → goldens decide
- AC4069 adapter outlives serve → release in `t.Cleanup`

## Notes for reviewers
- Base `f1f16bb`; arch [K22]; security: SSE guards, keys/logs, blob paths, clone scan
