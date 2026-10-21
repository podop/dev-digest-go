# Package layout

## Contents
1. Default tree
2. Small service (before splitting)
3. Mapping the source models
4. Package naming
5. When to split or merge

## 1. Default tree

```
go.mod
cmd/
  <service>/main.go            composition root: main → run(ctx, args, getenv, stdout)
internal/
  <context>/                   one bounded context (orders, billing, …)
    domain/                    entities, value objects, invariants, errors — stdlib only
    app/                       use cases, ports (consumer-side interfaces)
    adapters/
      postgres/                repository implementing app ports; row structs; tx
      <client>/                outbound HTTP/gRPC/SDK adapters
    transport/
      http/                    handlers, DTOs, routes.go, error → status
  platform/                    generic infra used by several contexts
    postgres/                  pool setup, migrations runner
    httpx/                     middleware, server defaults
    logging/                   slog handler setup
migrations/                    SQL migrations (if the repo uses them)
```

- `internal/` is enforced by the compiler: nothing outside the module tree rooted at
  the parent of `internal/` can import it. Put all server code there.
- `cmd/<service>` holds only startup: config, wiring, signals. It may be one file.
- No `pkg/`: it is not part of the official layout guide; code meant for others is a
  separate module.

## 2. Small service (before splitting)

A context with little logic may start as a single package:

```
internal/orders/
  order.go          type Order, invariants, ErrNotFound
  service.go        use cases + ports
  postgres.go       repository
  http.go           handlers
```

Inside one package Go cannot enforce layers, so split into `domain/app/adapters/transport`
as soon as one of these holds: there are business rules worth unit-testing in isolation,
there is a second adapter (or a fake), or the package passes ~1 000 lines.

## 3. Mapping the source models

| This skill | Three Dots Labs (Wild Workouts) | Ardan Labs Service | go-cleanarch dirs |
|---|---|---|---|
| domain | `domain` | `business/types` + core of `business/domain/<x>bus` | `domain` |
| app | `app` (`app/command`, `app/query`) | `business/domain/<x>bus` (use cases) + `app/domain/<x>app` orchestration | `app` |
| adapters | `adapters` | `business/domain/<x>bus/stores/<x>db` | `infrastructure` / `adapters` |
| transport | `ports` (HTTP/gRPC handlers) | `app/domain/<x>app` handlers, `api/` | `interfaces` |
| platform | — | `foundation/` (no business imports, does not log) | — |
| composition root | `main.go` | `api/services/<svc>` / `cmd` | — |

Three Dots calls inbound handlers "ports"; this skill uses **port** only for the
consumer-side interface and **transport** for handlers, to keep one meaning per word.
Whatever names the repository uses, keep them consistent and encode them in its
arch-lint config.

## 4. Package naming

- Short, lowercase, one word, named for what it **provides**: `postgres`, `orders`,
  `money`. Not `util`, `common`, `helpers`, `misc`, `base`, `types`, `models`,
  `interfaces`.
- No stutter: `orders.Service`, not `orders.OrderService`.
- Test-double packages: `<pkg>test` (`orderstest`) — see go-testing.

## 5. When to split or merge

- Split a package when two parts change for different reasons or have different
  dependencies (a part needs a driver, the other does not).
- Two sibling packages importing each other's internals → merge them, or move the
  shared part into the inner layer they both may import.
- A `platform` package that starts to know a business term belongs in a `<context>`.
- A context that needs another context's data calls its `app` API (in-process) or
  consumes its events; it never reads the other context's tables directly.
