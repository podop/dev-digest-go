# Sources

Primary sources (the three this skill is built on):

| Source | Used for |
|---|---|
| Three Dots Labs — Introducing Clean Architecture: https://threedots.tech/post/introducing-clean-architecture/ | inward-only dependencies, layer names, wiring in `main` |
| Three Dots Labs — Repository pattern in Go: https://threedots.tech/post/repository-pattern-in-go/ | domain-shaped repositories, separate row models, UpdateFn |
| Three Dots Labs — Database transactions in Go: https://threedots.tech/post/database-transactions-in-go/ | transaction inside the repository, cross-aggregate tx "sparingly" |
| Three Dots Labs — Common anti-patterns in Go web applications: https://threedots.tech/post/common-anti-patterns-in-go-web-applications/ | one model per responsibility, don't over-structure early |
| Three Dots Labs — DDD Lite / Basic CQRS: https://threedots.tech/post/ddd-lite-in-go-introduction/ · https://threedots.tech/post/basic-cqrs-in-go/ | domain free of DB concerns; CQRS only when it pays off |
| Google Go Style Guide — Decisions: https://google.github.io/styleguide/go/decisions | consumer-side interfaces, package names, context first, no panics, globals |
| Google Go Style Guide — Best practices: https://google.github.io/styleguide/go/best-practices | error wrapping and matching, log-or-return, option structs |
| Ardan Labs Service: https://github.com/ardanlabs/service (+ wiki "project structure") | layers import downward only, foundation does not log, explicit converters at boundaries |
| Ardan Labs — Package Oriented Design (2017): https://www.ardanlabs.com/blog/2017/02/package-oriented-design.html | `cmd/` owns startup; sibling packages don't import each other |

Cross-checks:

| Source | Used for |
|---|---|
| Organizing a Go module: https://go.dev/doc/modules/layout | `cmd/`, `internal/`, no `pkg/` |
| Go Code Review Comments: https://go.dev/wiki/CodeReviewComments | interfaces, contexts, goroutine lifetimes, error strings |
| Uber Go Style Guide: https://github.com/uber-go/guide/blob/master/style.md | no mutable globals / `init()`, exit once in `main`, error naming |
| Mat Ryer — How I write HTTP services in Go after 13 years: https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/ | `run(ctx, args, getenv, stdout)`, handler closures, `addRoutes` |
| Peter Bourgon — Go best practices (2016): https://peter.bourgon.org/go-best-practices-2016/ | explicit dependencies, loggers are dependencies |
| golang-standards/project-layout + golang/go#45861 | why `pkg/` and that layout are not official |
| go-arch-lint: https://github.com/fe3dback/go-arch-lint · depguard: https://golangci-lint.run/docs/linters/configuration/#depguard | enforcement configs |

Standard-library APIs mentioned in the skill (`errors.AsType`, `sync.WaitGroup.Go`,
`context.WithoutCancel`, `ServeMux` patterns, `slog`) were checked with `go doc` on the
Go 1.27 toolchain.
