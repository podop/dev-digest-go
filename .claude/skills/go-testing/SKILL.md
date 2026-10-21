---
name: go-testing
description: "Testing rules for Go services and libraries: what to test at each layer (pure domain unit tests, use cases with hand-written fakes, repositories against a real database via testcontainers, HTTP handlers with httptest), table-driven subtests, cmp.Diff and got/want failure messages, t.Error vs t.Fatal, helpers with t.Helper/t.Cleanup/t.Context, test doubles in <pkg>test packages, t.Parallel safety, deterministic time with testing/synctest, fuzzing, golden files, benchmarks with b.Loop, integration-test gating with -short, and CI flags (-race, -shuffle, -count=1, vet). Distilled from the Google Go Style Guide, the Go wiki and Three Dots Labs. Use when writing, fixing or reviewing *_test.go files, adding test helpers or fakes, setting up database or HTTP tests, or choosing go test commands. The repository's own testing conventions override this skill. Not for production package layout or layering (use go-architecture), not for TypeScript/React tests."
---

# Go testing

Tests prove behavior through the public API, fail with a message that explains itself,
and run fast, isolated and in any order.

**Repository rules win.** First read `CLAUDE.md` / `AGENTS.md`, `CONTRIBUTING.md`,
`TESTING.md`, the `Makefile` / CI test command and the existing tests next to the code
you touch. Match the assertion style already in use (stdlib + `cmp`, or testify) instead of
mixing styles. This skill fills the gaps.

Check the `go` directive in `go.mod` before using a version-gated feature (table in §8).

References (read only the one the task needs):
[references/patterns.md](references/patterns.md) — code for tables, helpers, fakes,
httptest, synctest, fuzz, golden files, benchmarks ·
[references/integration.md](references/integration.md) — real-database tests with
testcontainers, repository contract suites, component tests, coverage of binaries ·
[references/sources.md](references/sources.md) — where each rule comes from.

## 1. What to test where

| Layer (see go-architecture) | Test with | Doubles |
|---|---|---|
| **domain** | plain unit tests, table-driven; fuzz parsers/validators | none |
| **app** (use cases) | unit tests through the use-case API | hand-written **fakes** for ports (in-memory store, fixed clock) |
| **adapters** (repositories, clients) | integration tests against the real thing: Postgres in a container, `httptest.Server` for HTTP clients | none for what you own; one contract suite run against real and in-memory implementations |
| **transport** (handlers) | `httptest.NewRecorder` through the real `ServeMux` | fake or real use case |
| **service** (component) | start the service via `run(...)`, real DB, external services faked; happy path + key errors | external adapters only |
| **end-to-end** | a few critical paths | none — checks wiring, not logic |

If a use-case test needs many doubles to say anything, the logic belongs in `domain`.

## 2. Assertions and failure messages

- Default: standard library checks + `github.com/google/go-cmp/cmp`. No custom assertion
  helpers. If the repository already uses testify, use `require` for errors and
  preconditions, `assert` for values; no `suite` (it cannot run in parallel).
- Message format: `Func(input) = got, want want`. Name the function and the input; got
  before want.
- Structs, slices, maps: `if diff := cmp.Diff(want, got); diff != "" { t.Errorf("Func(%v) mismatch (-want +got):\n%s", in, diff) }`.
  Not `reflect.DeepEqual`. Unexported fields need `cmpopts.IgnoreUnexported` or
  `cmp.AllowUnexported`; errors compare with `cmpopts.EquateErrors()` or `errors.Is`.
- Compare semantics, not serialization: decode JSON and diff the values.
- `t.Error` for value mismatches (report everything); `t.Fatal` when continuing is
  meaningless (setup failed, unexpected error, nil result).
- Never `t.Fatal`/`t.FailNow`/`require.*` from a goroutine other than the test's: send the
  error back, or `t.Error` and return.
- Check every error in tests, including `Close`, `Rollback`, `Encode`.

## 3. Structure

- Table-driven tests with **named fields** and a `name` per case, run with `t.Run(tc.name, …)`.
  Keep the check logic in the test function, not in the table (no `check func` fields
  unless cases genuinely differ in what they assert).
- Black-box by default: `package foo_test`. Use an internal test file only for unexported
  logic that is hard to reach through the API.
- Helpers take `t *testing.T` (or `testing.TB`) first, call `t.Helper()`, register
  teardown with `t.Cleanup`, and do setup — not assertions.
- Use `t.Context()` instead of `context.Background()` (canceled before cleanups), and
  `t.TempDir()`, `t.Setenv()`, `t.Chdir()` instead of manual setup/teardown.
- One test, one behavior; test names describe it (`TestCancel_AlreadyShipped`).
- `Example` functions need an `// Output:` comment, or they are compiled but never run.

## 4. Test doubles

- Prefer a **fake** (working in-memory implementation of the port) over a mock. Put
  reusable fakes in a `<pkg>test` package (`orderstest.NewFakeStore()`), named by
  behavior (`AlwaysFails`, `FixedClock`).
- Generated mocks (`go.uber.org/mock`; `github.com/golang/mock` is archived) only when the
  interaction itself is the behavior (exactly one email sent). Don't assert call order
  that the code doesn't promise.
- Don't mock what you own at the database boundary — test the repository against a real
  database (references/integration.md).
- For HTTP clients, use the real client against `httptest.Server`, not a mocked
  `http.Client`/`RoundTripper`.
- Don't export an interface from production code only to mock it.

## 5. Time, concurrency, isolation

- No `time.Sleep` to wait for something. Use channels/`WaitGroup`, or `testing/synctest`
  (`synctest.Test` + `synctest.Wait`) for timers, timeouts, tickers and retries — the
  bubble's fake clock makes them instant and deterministic.
- Inject time (`func() time.Time` or a clock port) where business rules depend on it.
- `t.Parallel()` only when the test owns all its state: no package globals, no shared
  rows, no `t.Setenv`/`t.Chdir` (they panic in parallel tests). Database tests create
  unique data per test instead of truncating shared tables.
- Tests must pass in any order (`-shuffle=on`) and repeatedly (`-count=N`).
- Goroutine leaks: `goleak.VerifyTestMain(m)` in `TestMain` for packages that start
  goroutines.

## 6. Integration tests

- Real dependencies in containers (testcontainers-go), one container per package in
  `TestMain` or a `sync.Once` helper, isolated data per test.
- Gate with `if testing.Short() { t.Skip("integration: needs Docker") }` so
  `go test -short ./...` is the fast loop. Use a build tag only if the repository already
  does or CI must exclude them entirely.
- No fixed ports, no retry/sleep loops for readiness — use the container's wait strategy.
- Details and code: [references/integration.md](references/integration.md).

## 7. Special tests

- **Fuzz** (`FuzzXxx(f *testing.F)`) parsers, decoders, validators. Targets are fast,
  deterministic, without global state; commit failing inputs in `testdata/fuzz/FuzzXxx/`
  (plain `go test` replays them).
- **Golden files** for large outputs: `testdata/<name>.golden`, regenerated with a
  package-level `-update` flag, reviewed in the diff.
- **Benchmarks**: `for b.Loop() { … }`, not `for i := 0; i < b.N; i++`.
- **Artifacts** a failing test should keep (dumps, payloads): write to `t.ArtifactDir()`.

## 8. Version-gated features

| Feature | Since | Instead of |
|---|---|---|
| Per-iteration loop variables | 1.22 | `tc := tc` before `t.Parallel()` (remove it) |
| `ServeMux` patterns, `req.SetPathValue` | 1.22 | third-party router in handler tests |
| `t.Context()`, `t.Chdir()`, `b.Loop()` | 1.24 | `context.Background()`, manual `os.Chdir`, `b.N` loops |
| `testing/synctest` (`Test`, `Wait`) | 1.25 | `time.Sleep`, injected fake clocks for timers |
| `t.Attr()`, `t.Output()` | 1.25 | ad-hoc log lines for metadata |
| `t.ArtifactDir()` + `go test -artifacts` | 1.26 | writing into `os.TempDir()` by hand |
| `synctest.Sleep`, `httptest.NewTestServer` (in-memory network) | 1.27 | real sockets inside a synctest bubble |

## 9. Workflow: tests for a change

```
- [ ] 1. Read the repository's test conventions and neighbouring tests
- [ ] 2. Pick the layer (§1); write the failing test first where practical
- [ ] 3. Table + subtests; cmp.Diff; got/want message with function and input
- [ ] 4. Doubles: fake for ports, real DB/httptest for adapters
- [ ] 5. No sleeps; t.Parallel only with isolated state
- [ ] 6. Break the code once and watch the test fail
- [ ] 7. Run the repository's test command; otherwise: go vet ./... && go test -race -shuffle=on -count=1 ./...
```

## 10. Review checklist

- [ ] `t.Fatal`/`FailNow`/`require` not called from spawned goroutines
- [ ] Helpers call `t.Helper()`; teardown via `t.Cleanup`; no ignored errors
- [ ] Failure messages name function + input, got before want, `(-want +got)` with `cmp.Diff(want, got)`
- [ ] Table cases named; no index-only identification; no shared mutable state between cases
- [ ] `t.Parallel` tests don't use `t.Setenv`/`t.Chdir`/globals/shared rows
- [ ] No `time.Sleep` for synchronization; time-dependent code uses synctest or an injected clock
- [ ] Doubles are fakes of consumer-side ports; no mocks of the database boundary; no mocked `http.Client`
- [ ] Integration tests skip under `-short`, use wait strategies, no fixed ports
- [ ] Benchmarks use `b.Loop`; fuzz corpora committed under `testdata/fuzz/`
- [ ] Test-only code lives in `_test.go` files or `<pkg>test` packages, never in production packages
- [ ] CI runs `go vet`, `-race`, `-shuffle=on`, `-count=1` (or the repository's equivalent)

## 11. Commands

| Purpose | Command |
|---|---|
| CI default (if the repository defines none) | `go vet ./... && go test -race -shuffle=on -count=1 ./...` |
| Fast loop | `go test -short ./...` |
| One case | `go test -run '^TestCancel$/already_shipped' -count=1 -v ./internal/orders/app` |
| Coverage | `go test -race -coverprofile=cover.out ./... && go tool cover -func=cover.out` |
| Fuzz one target | `go test -fuzz=FuzzParse -fuzztime=30s ./internal/orders/domain` |
| Keep artifacts | `go test -artifacts -outputdir=out ./...` |

No test command anywhere and no Go toolchain → report it; don't invent a different runner.
