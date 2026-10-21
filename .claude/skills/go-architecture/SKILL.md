---
name: go-architecture
description: "Architecture and design rules for Go backend services: package layout (cmd/, internal/, bounded-context packages), inward-only layering (domain, app, adapters, transport, composition root), consumer-side interfaces (ports), constructor injection without globals or init(), separate DTO/row/domain models, error wrapping and mapping at the HTTP edge, context and goroutine lifecycle, config and logging, transactions with the UpdateFn pattern, and enforcement with go-arch-lint or golangci-lint depguard. Distilled from Three Dots Labs, the Google Go Style Guide and Ardan Labs Service. Use when adding or moving Go packages, use cases, repositories, handlers or adapters, asking where Go code should go, wiring main, or reviewing a Go diff for layering and package boundaries. The repository's own architecture docs override this skill. Not for writing or reviewing tests (use go-testing), not for TypeScript backends, not for style/lint nits or performance tuning."
---

# Go service architecture

Dependencies point **inward only**. The domain compiles and is testable with nothing but
the standard library; the database, HTTP framework and SDKs live at the edges and are
wired together in `main`.

**Repository rules win.** First read the repository's own sources and follow them where
they differ: `ARCHITECTURE.md`, `docs/architecture/**`, ADRs, `CLAUDE.md` / `AGENTS.md`,
`.go-arch-lint.yml`, depguard rules in `.golangci.yml`. This skill fills the gaps.

Before using a newer standard-library API, check the `go` directive in `go.mod`.

References (read only the one the task needs):
[references/layout.md](references/layout.md) — package layout, naming, how the three source
models map onto each other, when to split ·
[references/patterns.md](references/patterns.md) — code for ports, constructors, UpdateFn
transactions, errors at the edge, `run()` with graceful shutdown, handlers ·
[references/enforcement.md](references/enforcement.md) — go-arch-lint / depguard configs and
grep checks for a diff ·
[references/sources.md](references/sources.md) — where each rule comes from.

## 1. Layers

| Layer | Default location | Owns | May import (from this module) |
|---|---|---|---|
| **domain** | `internal/<context>/domain` | entities, value objects, invariants, pure rules, domain errors | nothing — standard library only, and not `database/sql`, `net/http`, `os` |
| **app** | `internal/<context>/app` | use cases, **ports** (interfaces the use cases need), transaction boundary | `domain`, `internal/platform` interfaces |
| **adapters** | `internal/<context>/adapters/<tech>` | Postgres repositories, HTTP/gRPC clients, queues; row ↔ domain mapping | `app` (ports it implements), `domain`, `platform`, drivers/SDKs |
| **transport** | `internal/<context>/transport/http` (or `httpapi`) | handlers, request/response DTOs, auth middleware, error → status | `app`, `domain`, `platform` |
| **platform** | `internal/platform/<tech>` | generic infra: DB pool, logger setup, middleware, clock | standard library, vendors — never a `<context>` package |
| **composition root** | `cmd/<service>/main.go` | config from flags/env, wiring, server start, signals, exit code | everything |

Rules:
- An outer layer may import any inner one; never the reverse.
- `adapters` and `transport` never import each other; contexts reach each other only
  through `app` APIs (or events), never through another context's `adapters` or `domain`
  internals.
- Nothing imports `cmd/...`; binaries never import each other.
- Code for the server goes under `internal/`. No `pkg/` directory: share code by making it
  a separate module.
- A **port** is a small interface declared in the package that *uses* it (`app`), shaped
  by the use case (`UpdateByID(ctx, id, fn)`), not by tables. Never declare it in the
  adapter that implements it.

## 2. Where does it go?

| You have… | Goes to |
|---|---|
| Business rule, state transition, invariant, calculation | `domain` — method on the entity or a pure function |
| "Load A, apply rule, save A, publish event" | `app` — a use-case method/handler |
| Interface the use case needs from the outside | `app` — unexported port next to its consumer |
| SQL, row struct, `SELECT … FOR UPDATE`, transaction | `adapters/postgres` |
| Call to another service or SDK | `adapters/<name>` implementing a port; SDK types never cross it |
| Route, JSON DTO, status code, request validation of shape | `transport/http` |
| Domain error → HTTP status | one mapping function in `transport/http` |
| Env var, flag, secret | `cmd/<service>` → typed `Config` passed down |
| Logger, metrics, clock | injected dependency (`*slog.Logger`, `func() time.Time`) |
| Generic helper with no business meaning | `internal/platform/<what-it-provides>` — never `util`, `common`, `helpers` |

## 3. Dependencies and wiring

- Constructors take explicit dependencies and return **concrete types**:
  `func NewService(orders orderStore, log *slog.Logger) *Service`. Use an options/config
  struct once there are more than ~4 parameters.
- A use case receives only the ports it uses — never a container or service locator.
- No mutable package-level variables and no `init()` with side effects. Allowed globals:
  `Err*` sentinels, `regexp.MustCompile`, compile-time checks `var _ port = (*Impl)(nil)`.
- Wiring is plain code in `main`/`run`. A DI framework needs a written reason.
- Request-scoped values (user, tenant) are arguments, not fields on the service.

## 4. Models

- Separate models per layer: transport DTO, domain type, DB row. Convert with explicit,
  named functions (`toDomain`, `fromDomain`, `toResponse`); never assign across a layer
  boundary directly.
- Domain types carry no `json:`, `db:` or ORM tags and are not encoded to responses
  directly.
- Proportionality: a CRUD endpoint with no invariants may skip the domain type and go
  handler → app → repository with a single struct. Add the domain type when there is a
  rule to protect.

## 5. Errors

- `domain`/`app` return sentinel (`var ErrNotFound = errors.New("order not found")`) or
  typed errors (`*ValidationError`). Only `transport` maps them to status codes.
- Wrap with `%w` when the caller may need the cause; add only new context, at the front:
  `fmt.Errorf("load order %s: %w", id, err)`. Lowercase, no punctuation, no "failed to".
- Compare with `errors.Is` / `errors.As` (or `errors.AsType` when `go.mod` allows) —
  never with `err.Error()` strings.
- Adapters translate driver errors (no rows, unique violation) into domain errors.
- Handle each error once: log **or** return, not both. Log at the edge.
- No `panic` for expected failures; `Must*` and nil-dependency panics only at startup.
  Only `main` calls `os.Exit` / `log.Fatal`, once.

## 6. Context, goroutines, lifecycle

- `ctx context.Context` is the first parameter of anything that does I/O or may block;
  never stored in a struct; `context.Background()` only in `main` and tests.
- Every goroutine has an owner, a stop condition and a wait (`errgroup`,
  `sync.WaitGroup.Go`, ctx cancellation). No fire-and-forget.
- `main` → `run(ctx, args, getenv, stdout) error`: `signal.NotifyContext` for SIGINT/SIGTERM,
  `http.Server` with timeouts, `Shutdown` with a deadline on a `context.WithoutCancel` ctx.

## 7. Transactions

- The repository opens the transaction around an `updateFn` closure supplied by `app`;
  business logic runs inside the closure, the repository holds none.
- `*sql.Tx`, `pgx.Tx` and `*sql.DB` never appear in `app` or `domain` signatures.
- Cross-aggregate transactions (a transaction provider passed to the use case) only
  when a single aggregate cannot hold the invariant — use sparingly.

## 8. Config and logging

- Flags, `os.Getenv`, `os.Args` are read only in `cmd/`; packages receive a typed config.
- Loggers are injected; `domain` and `platform` libraries do not log. No global
  `log.Printf` / `slog.Default()` in `internal/`.
- Prefer the standard library: `net/http` `ServeMux` method patterns
  (`"POST /orders/{id}"`, `r.PathValue`), `log/slog`, `errors`. A router or logging
  framework needs a reason.

## 9. Proportionality

- Start with one package per bounded context; split into `domain`/`app`/`adapters`/
  `transport` when the context has real rules or more than one adapter.
- Add a port when there is a swap point (test fake, second implementation, SDK).
- CQRS (separate command/query handlers) only when the use cases grow; not by default.
- Never create a package "for symmetry"; an empty layer is noise.

## 10. Workflow: new use case / module

```
- [ ] 1. Read the repository's architecture docs and the spec/plan; check the go directive
- [ ] 2. domain: types, invariants, errors + unit tests (go-testing)
- [ ] 3. app: use case, the ports it needs (consumer side), UpdateFn transaction boundary
- [ ] 4. adapters: repository/client implementing the port, row mapping, error translation
- [ ] 5. transport: route, DTOs, handler → use case → response, error → status
- [ ] 6. cmd: config field, wiring, nothing else
- [ ] 7. go build ./... · go vet ./... · the repository's layer check (references/enforcement.md)
```

## 11. Review checklist

Mechanical checks with commands are in [references/enforcement.md](references/enforcement.md).

- [ ] No inward-to-outward import (`domain` → anything in module; `app` → `adapters`/`transport`/`database/sql`/`net/http`)
- [ ] `adapters` ↔ `transport` do not import each other; no import of `cmd/`
- [ ] Ports declared at the consumer; constructors return concrete types
- [ ] No new mutable globals, `init()`, `os.Getenv`/`flag` outside `cmd/`, `os.Exit`/`log.Fatal` outside `main`
- [ ] `ctx` first, not stored; no `context.Background()` mid-chain
- [ ] No `*sql.Tx`/`*sql.DB` in `app`/`domain`; no SQL or business rules in handlers
- [ ] Domain types have no `json:`/`db:` tags; DTOs mapped explicitly
- [ ] Errors wrapped with `%w`, matched with `errors.Is/As`, mapped to status only in transport
- [ ] Every new goroutine has a stop condition and a waiter
- [ ] No new `util`/`common`/`helpers`/`models`/`types` package, no `pkg/`

## 12. Enforcement

If the repository has `.go-arch-lint.yml` or depguard rules in `.golangci.yml`, run them
with the repository's own command (Makefile, CI, `CLAUDE.md`). A new violation → fix the
code, never widen the config in the same change. No config → report
"layer check: not configured" and use the grep checks in
[references/enforcement.md](references/enforcement.md); do not invent a config in a review.
