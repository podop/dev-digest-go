# Sources

| Source | Used for |
|---|---|
| Google Go Style Guide — Decisions (tests): https://google.github.io/styleguide/go/decisions#tests | no assertion libraries, got/want messages, `cmp`, `t.Error` vs `t.Fatal`, helpers |
| Google Go Style Guide — Best practices (testing): https://google.github.io/styleguide/go/best-practices#testing | `t.Fatal` only from the test goroutine, `<pkg>test` doubles, real transports |
| Go wiki — TestComments: https://go.dev/wiki/TestComments | failure message format, compare full structures |
| Go wiki — TableDrivenTests: https://go.dev/wiki/TableDrivenTests | table layout, parallel subtests |
| Go blog — Subtests: https://go.dev/blog/subtests | `t.Run`, `-run` with `/` |
| Go blog — Testing time (synctest): https://go.dev/blog/testing-time | `synctest.Test`, `Wait`, fake clock |
| Go fuzzing docs: https://go.dev/doc/security/fuzz/ | fuzz targets, corpus in `testdata/fuzz` |
| Go blog — Coverage for integration tests: https://go.dev/blog/integration-test-coverage | `go build -cover`, `GOCOVERDIR` |
| `testing` package and `go help testflag` | `t.Context`, `t.Chdir`, `b.Loop`, `t.Attr`, `t.Output`, `t.ArtifactDir`, `-shuffle`, `-artifacts` |
| Go release notes 1.22–1.27: https://go.dev/doc/devel/release | version table in SKILL.md §8 |
| Three Dots Labs — Microservices test architecture: https://threedots.tech/post/microservices-test-architecture/ | test levels, don't test the mocks |
| Three Dots Labs — Database integration testing: https://threedots.tech/post/database-integration-testing/ | parallel, isolated, real DB, same suite for every repository implementation, sabotage |
| Dave Cheney — Prefer table driven tests: https://dave.cheney.net/2019/05/07/prefer-table-driven-tests | named cases, map-order independence |
| Mitchell Hashimoto — Advanced Testing with Go (talk) | golden files with `-update` |
| testcontainers-go Postgres module: https://golang.testcontainers.org/modules/postgres/ | `postgres.Run`, wait strategies, cleanup |
| go-cmp: https://pkg.go.dev/github.com/google/go-cmp/cmp · goleak: https://github.com/uber-go/goleak · go.uber.org/mock | comparison, leak checks, generated mocks |

All `testing`, `testing/synctest` and `net/http/httptest` APIs named in the skill were
checked with `go doc` on the Go 1.27 toolchain. The Ardan Labs Service test layout and the
full Hashimoto slides were not verified; the integration-gating default (`-short` over
build tags) is a recommendation, not a sourced rule.
