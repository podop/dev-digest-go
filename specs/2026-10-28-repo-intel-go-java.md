# Repo intelligence for Go and Java, and an honest "no source files" state
Spec: repo-intel language support (Go, Java) and the UI state of a repo without indexable files · Status: draft · Date: 2026-10-28
Areas: index, graph, facts, db, ui
Design sources: the user's requests of 2026-10-28 ("do both, start with Go support. Add also Java support"), after
holocron (a Go GitLab project) was indexed with `filesIndexed: 0, reason: no_files` and could neither generate an
onboarding tour nor derive a blast radius; the brainstorming session of 2026-10-28 (sections 1–5 approved one by one:
tree-sitter for both languages, core index + endpoints/crons, same-package resolution in its own table, limits and
testing, frontend-only honest state). Code research at `main` @ `a0f6c1c` (plus the uncommitted clone-retry change of
2026-10-28). Language survey of the user's local checkouts (§2).
Related: `specs/2026-10-22-go-backend-phase-3.md` (Phase 3 — repo intelligence; this spec extends its walk rules
FR3013, facts FR3022, import graph FR3023, reference resolution FR3030 and sections 7.4/7.5 to two more languages and
leaves every TypeScript/JavaScript rule unchanged), `docs/migration/research/03-engine-integrations.md:193` (Node's
indexer is TS/JS only). Supersedes nothing.

ID convention: plain FR1…, EC1…, NFR1…, AC1…, D1…, Q1…. Area tags: `[index]` = walk, parser, symbols, references,
indexer runs; `[graph]` = import edges and reference resolution; `[facts]` = endpoint and cron detection; `[db]` =
schema and migrations; `[ui]` = Next.js app.

## 1. Goal
Repo intelligence (the index behind the onboarding tour, the blast radius, the repo map and the reviews' caller
context) understands only TypeScript and JavaScript, like the Node original. A Go repository such as holocron is
"indexed" with zero files: the tour cannot be generated (422 `index_not_ready`) and the blast radius is degraded with
no symbols. Java repositories (the dsf-* services, ucc) lose their whole backend: only their TS/JS front end is
indexed.

This spec makes Go and Java first-class indexed languages — symbols, references, import graph, same-package
resolution, ranking and repo map, HTTP endpoints and crons — so the onboarding tour, the blast radius (changed symbols,
callers, affected endpoints and crons) and the reviews' caller context work for Go, Java and mixed repositories. It
then makes the UI say plainly when a repository has no file in any supported language, instead of "indexed" and
"partial".

## 2. Context
- Language survey (local checkouts, `vendor/` and `node_modules/` excluded): holocron 118 `.go` (+ 20,801 vendored);
  citrus 251 `.go`; zork 481 `.go`; dcg-orchestrator 901 `.go`; dsf-fam-service 750 `.java` + 633 TS/JS (pom.xml);
  dsf-hub-api-service 178 `.java`; dsf-ciphertrust-api-service 279 `.java`; ucc 1,114 `.java` + 219 TS/JS
  (build.gradle); skywalker 11 `.py` + 5 `.js`.
- Frameworks seen: Go — `net/http`, gRPC (`google.golang.org/grpc`), Echo (`labstack/echo`), a little Gin; Java —
  Spring (`@RestController`, `@GetMapping`, `@PostMapping`, `@RequestMapping`, `@Scheduled`), sources under
  `src/main/java` and `src/test/java`.
- The parser is tree-sitter through the CGO binding `github.com/tree-sitter/go-tree-sitter` v0.25.0
  (`backend/internal/repointel/adapters/treesitter`). Grammar modules available: `tree-sitter-go` v0.25.0,
  `tree-sitter-java` v0.23.5.
- Reference resolution (`adapters/postgres/write.go` `ResolveReferences`, FR3030) links a reference to file F only
  when exactly one file F has an import edge from the reference's file and an exported symbol of that name. Go and Java
  code calls into its own package without an import, so this rule alone would miss most Go/Java callers.
- Blast radius lists the endpoints and crons of the **caller** files of the changed symbols (`app/blast.go`); a route
  is therefore attributed only when the file that registers it counts as a caller of the handler.
- The incremental index rebuilds the whole import graph over the walked files on every run (`app/incremental.go`),
  but runs only when a changed path has a supported extension.
- The caller scan of `CallerSignatures` skips `.git node_modules dist build .next coverage` (`domain/callers.go`
  `IsCallerScanDir`) — not `vendor`.
- `RepoIndexState.reason` (frontend contract `vendor/shared/contracts/platform.ts`) is a free string and already
  carries `no_files` for a repo whose walk found no supported file.

## 3. Scope
### In scope
- Go (`.go`) and Java (`.java`) in the walk, the parser, symbols, references, import edges, same-package resolution,
  ranking, repo map, facts, the incremental index and the caller scan.
- Endpoint detection: Go `net/http` (`Handle`/`HandleFunc`, Go 1.22 patterns), Echo and Gin verb routes, gRPC service
  registration; Java Spring request mappings with a class-level prefix. Cron detection: Go robfig `AddFunc`/`AddJob`;
  Java `@Scheduled`.
- Migration 0013: table `file_packages`.
- `IndexerVersion` 2 → 3.
- Frontend: the "no supported source files" state in Settings repo rows, first-run setup, the onboarding tour and the
  blast radius card.

### Out of scope
- Other languages (Kotlin, Python, C#, …).
- The regex fallback extractor of the degraded blast radius (`domain/extract.go`, used when the index is missing):
  stays TypeScript/JavaScript only (user choice "core + endpoints/crons", 2026-10-28).
- chi (`r.Get`), gorilla/mux method chains, gocron, JAX-RS (`@Path`, `@GET`), Micronaut/Quarkus annotations.
- Route prefixes of Echo/Gin groups (`g := e.Group("/api")`) and Spring paths built from constants.
- Type-aware resolution (which `Get` a call means when several candidates exist): names are matched by text, as for
  TS/JS.
- Any change to the HTTP API or to a frontend contract.

## 4. Functional requirements

### Walk and parser
- FR1 [index] — `domain.SyntaxForPath` maps `.go` → `SyntaxGo` and `.java` → `SyntaxJava` (case-insensitive, like the
  six existing extensions); `IsSupported` follows. `Syntax.String()` names them `go` and `java`.
- FR2 [index] — `IsExcludedDir` also excludes `target` (Maven output). `vendor` stays excluded.
- FR3 [index] — Generated Go files are left out of the walk by name: a path ending in `.pb.go` (which covers
  `_grpc.pb.go`) or `.pb.gw.go` is not a candidate (not counted in `TotalCandidates`).
- FR4 [index] — The tree-sitter adapter parses Go with `tree-sitter-go` v0.25.0 and Java with `tree-sitter-java`
  v0.23.5. Every pass of the new languages ticks the existing `guard` (ctx checked every 1,024 nodes) and runs under
  the per-file `ParseTimeout`, like the TS/JS passes. The TS/JS passes are not changed.

### Symbols
- FR5 [index] — Go symbols: function declarations (kind `function`), method declarations (kind `method`; the
  signature shows the receiver, e.g. `func (s *Server) Start(ctx context.Context) error`), type declarations whose type
  is a struct (kind `class`), an interface (kind `interface`) or anything else (kind `type`). Package-level `var` and
  `const` are not symbols. `Exported` is true when the name starts with an upper-case letter (Unicode `IsUpper` of the
  first rune).
- FR6 [index] — Java symbols: class and record declarations (kind `class`), interface and annotation type
  declarations (kind `interface`), enum declarations (kind `enum`), method and constructor declarations (kind
  `method`), at any nesting depth (members of nested and inner types included). `Exported` is true for a declaration
  with the `public` modifier and for every member of an interface or annotation type.
- FR7 [index] — Signatures of both languages follow the existing rule: the declaration head up to the body, white
  space collapsed, at most `MaxSignatureUnits` UTF-16 units; names at most `MaxNameUnits`.

### References
- FR8 [index] — Go references: for every call expression, the called name — the identifier itself, or the field of a
  selector (`pkg.F()` → `F`, `x.M()` → `M`); every identifier or selector passed as an argument of a call (function
  values, e.g. `e.GET("/x", h.GetX)` → `GetX`, `mux.HandleFunc("/x", handleX)` → `handleX`); the type name of a
  composite literal (`T{}`, `pkg.T{}` → `T`).
- FR9 [index] — Java references: the name of every method invocation (`a.b()` → `b`), the type of every object
  creation (`new T()`, `new pkg.T<>()` → `T`), and the name of every annotation (`@Foo`, `@Foo(...)` → `Foo`).
- FR10 [index] — References keep the existing row shape (name, 1-based line) and dedupe rules.

### Imports and package keys
- FR11 [graph] — `app.Analyzer.Imports` returns the file's package key together with its specifiers
  (`domain.FileImports{Package string; Specs []string}`). TS/JS files return an empty package key and the same
  specifiers as today. A Go file's package key is `go:` + its directory (slash-separated, `.` for the root). A Java
  file's package key is `java:` + the name in its `package` declaration (`java:` alone for the default package).
- FR12 [graph] — Go specifiers are the import paths of the file's import declarations, aliased, dot and blank imports
  included (`import _ "x"` yields `x`).
- FR13 [graph] — Java specifiers are the imports of the file: `import a.b.C;` → `a.b.C`; `import a.b.*;` → `a.b.*`;
  `import static a.b.C.m;` → `static:a.b.C.m`; `import static a.b.C.*;` → `static:a.b.C.*`.

### Import edges
- FR14 [graph] — Go modules: the resolver looks for a `go.mod` in every directory that holds a walked file (and the
  clone root). It reads at most the first 64 KiB of each and takes the module path from its `module` directive; a
  `go.mod` without one is ignored. A Go import path P resolves to module M with the longest module path that equals P
  or is a prefix of P followed by `/`; the package directory is M's directory joined with the rest of P.
- FR15 [graph] — A resolved Go import yields one edge from the importing file to every walked `.go` file directly in
  the package directory whose name does not end in `_test.go`. An import that resolves to no in-repo module (standard
  library, external modules) or to a directory without such files yields no edge. An edge from a file to itself is
  never written.
- FR16 [graph] — Java: the resolver maps each package key to its walked `.java` files (from FR11). `a.b.C` resolves to
  the files named `C.java` in package `a.b`; when there is none, the last segment is dropped and the lookup repeated
  (`a.b.C.Inner` → `C.java` in `a.b`), at most until two segments remain. `a.b.*` resolves to every file of package
  `a.b`. `static:a.b.C.m` and `static:a.b.C.*` resolve as the class `a.b.C`. Unresolved specifiers (JDK, libraries)
  yield no edge.
- FR17 [graph] — Each specifier routes to the resolver of the importing file's syntax; TS/JS resolution is unchanged.
  Edges keep the existing `file_edges` shape and feed the ranking unchanged.

### Same-package resolution
- FR18 [db] — Migration 0013 creates `file_packages (repo_id uuid not null references repos(id) on delete cascade,
  file_path text not null, package_key text not null, primary key (repo_id, file_path))` with an index on
  `(repo_id, package_key)`; its down migration drops the table.
- FR19 [graph] — Every full and incremental index replaces the repo's `file_packages` rows with one row per walked
  file that has a non-empty package key, in the same transaction as the edges.
- FR20 [graph] — `ResolveReferences` keeps its import step unchanged and then runs a same-package step over the
  references whose `decl_file` is still NULL: a reference from file F gets `decl_file` G when G is the only file other
  than F that has F's package key and a symbol named like the reference (exported or not). Files without a package key
  (TS/JS) never take part. The step is one aggregate `UPDATE … FROM (SELECT … GROUP BY … HAVING count(DISTINCT …) = 1)`
  like the import step.

### Facts
- FR21 [facts] — `ExtractFacts` chooses the pattern set by the file's syntax: TS/JS files keep today's patterns
  exactly; Go and Java files use only their own (FR22–FR24). The output format is unchanged: endpoints `"METHOD /path"`,
  crons the expression, both de-duplicated in first-seen order.
- FR22 [facts] — Go endpoints, matched one line at a time with RE2:
  - `X.HandleFunc("<pattern>"` and `X.Handle("<pattern>"` (any receiver, `http` included): a Go 1.22 pattern
    `"METHOD [host]/path"` yields `METHOD /path` (host dropped); a pattern without a method yields `ANY /path`.
  - `X.GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD("/path"` (upper-case verbs only, Echo and Gin) yields `VERB /path`;
    `X.Any("/path"` yields `ANY /path`. The path must start with `/`.
  - `RegisterXxxServer(` (gRPC generated registration, any qualifier) yields `GRPC Xxx`.
- FR23 [facts] — Go crons: `X.AddFunc("<spec>"` and `X.AddJob("<spec>"` yield the spec as written (cron expressions
  and descriptors such as `@every 5m`).
- FR24 [facts] — Java, through an annotation scanner over the file text (not line-based): it reads `@Name` and, when
  followed by `(`, the argument list up to the balanced `)`, skipping string and character literals and comments, at
  most 4 KiB per annotation (a longer one is skipped).
  - `@GetMapping`, `@PostMapping`, `@PutMapping`, `@DeleteMapping`, `@PatchMapping`: each path yields `VERB path`.
    Paths come from the single unnamed argument, or from `value` / `path`, each a string literal or an array of string
    literals; no argument yields the empty path.
  - `@RequestMapping` on a method: its paths as above with `method = RequestMethod.X` (or an array of them, one
    endpoint per method); without `method`, `ANY`.
  - The first `@RequestMapping` placed before the first `class`/`interface` declaration of the file is the class
    prefix: its first path is joined to every method mapping of the file with exactly one `/` between them; an empty
    joined path is `/`.
  - An argument that is not a string literal (a constant, a concatenation) makes that path skipped; the other paths of
    the annotation are kept.
  - `@Scheduled(cron = "<expr>")` yields the expression as written (placeholders such as `${jobs.cron}` included);
    `fixedRate`, `fixedDelay`, `fixedRateString`, `fixedDelayString` yield `<attribute>=<value as written>`.

### Indexer runs
- FR25 [index] — `IndexerVersion` becomes 3, so the next index run of every repo is a full index (existing rule).
- FR26 [index] — An incremental index whose changed paths include a file named `go.mod` (any directory) runs a full
  index instead.
- FR27 [index] — The caller scan (`CallerSignatures`) also reads `.go` and `.java` files, and `IsCallerScanDir` also
  skips `vendor` and `target`.

### UI
- FR28 [ui] — `src/lib/hooks/repo-intel.ts` exports `hasNoSourceFiles(state)`: true when the state is indexed
  (`lastIndexedSha` not empty), `reason === "no_files"` and `filesIndexed === 0`.
- FR29 [ui] — Settings › GitHub and Settings › GitLab repository rows show "no supported source files" instead of
  "indexed … ago" for such a repo, with the supported-languages line as hint text.
- FR30 [ui] — First-run setup `IndexProgress` shows "{name} has no supported source files" and the
  supported-languages line instead of "{name} is indexed" and the status line.
- FR31 [ui] — The onboarding tour's empty state shows "This repository has no source files DevDigest can index, so a
  tour can't be generated." and the supported-languages line; Generate stays disabled. Other repos without indexed
  files keep today's "Index this repo first" hint.
- FR32 [ui] — The blast radius card reads the repo's index state (the shared `useRepoIntelStatus` query). When the
  answer is degraded and `hasNoSourceFiles` holds, the degraded line reads "This repository has no source files
  DevDigest can index, so callers can't be looked up." plus the supported-languages line, keeps "This is unknown, not
  “no impact”." and shows no Resync button. Every other degraded answer renders as today.
- FR33 [ui] — The supported-languages line ("Only TypeScript, JavaScript, Go and Java files are indexed.") is one
  message key, reused by FR29–FR32, in every locale file that the app ships.

## 5. Edge cases
- EC1 [graph] — A Go file whose `package` clause differs from its directory's other files (e.g. `package foo_test`
  next to `package foo`): the package key is still the directory, so external test files resolve same-package calls
  to the package under test (intended: tests count as callers).
- EC2 [graph] — Nested modules (`go.mod` in `tools/`): the longest module path wins, so an import of
  `example.com/repo/tools/x` resolves inside `tools/` even though `example.com/repo` is also a module.
- EC3 [graph] — A `go.mod` larger than 64 KiB or without a `module` line contributes no module; its imports yield no
  edge (no error).
- EC4 [graph] — Two walked `C.java` files in the same package (e.g. duplicated across source sets): both get an edge;
  reference resolution then needs a unique candidate as always and may leave the reference unresolved.
- EC5 [graph] — A Java file with no `package` declaration has the key `java:` and shares it with the other
  default-package files.
- EC6 [graph] — A reference that the import step resolved is never changed by the same-package step.
- EC7 [graph] — A name declared in two other files of the same package (overloads across files, a method `String()`
  on several types): the reference stays unresolved.
- EC8 [index] — A file that fails to parse or times out contributes no symbols, references, specifiers or package key
  (existing degraded handling); its `file_packages` row is not written.
- EC9 [facts] — `http.Get("https://…")`, `client.Post("/x", …)` and `r.Get("/x", h)` (Title-case verbs) yield no
  endpoint.
- EC10 [facts] — An annotation argument list that never closes, or exceeds 4 KiB, is skipped; scanning continues after
  the `@Name` token.
- EC11 [facts] — A Java file with method mappings but no class-level `@RequestMapping` uses no prefix.
- EC12 [index] — A mixed repo with more than `MaxIndexedFiles` candidates keeps the first files in byte order across
  all languages (existing rule; no per-language quota).
- EC13 [ui] — A repo whose index failed or has not run yet is not "no supported source files": FR28 requires an
  indexed state with `reason === "no_files"`.

## 6. Non-functional requirements
- NFR1 [index] — A full index of dcg-orchestrator (901 `.go` files) and of ucc (1,114 `.java` + TS/JS) finishes within
  the existing `SoftBudget` (110 s) on this machine without hitting it.
- NFR2 [graph] — The same-package step of `ResolveReferences` takes at most as long as the import step on the same
  repo (measured on dcg-orchestrator and ucc).
- NFR3 [index] — Hostile input: a 400 KiB Go or Java file of deeply nested or repeated constructs ends within
  `ParseTimeout` plus one guard interval and honours cancellation; the annotation scanner is linear in the file size.
- NFR4 [index] — No TS/JS behaviour changes: the parity goldens (`repointel/parity`) and every existing repo-intel test
  pass unchanged.
- NFR5 [ui] — The new UI texts pass the i18n leak tests (pseudo messages, stray text).

## 7. Contracts
No HTTP route, status code, JSON field or frontend contract changes. `RepoIndexState.reason` already carries
`no_files`. Internal port change: `app.Analyzer.Imports` returns `domain.FileImports` (FR11).

## 8. Data model
- New table `file_packages` (FR18), owned by migration `0013_file_packages` (up and down). Written and replaced with
  the other per-repo index tables; deleted with the repo (cascade).
- Existing tables unchanged; `symbols.kind` keeps its value set (`function`, `class`, `method`, `interface`, `type`,
  `enum`).

## 9. Dependencies and rollout
- New Go module dependencies: `github.com/tree-sitter/tree-sitter-go` v0.25.0, `github.com/tree-sitter/tree-sitter-java`
  v0.23.5 (both CGO, built in the existing image; behind Zscaler use `BUILD_GOPROXY`).
- Rollout: deploy → every repo is fully re-indexed on its next index run (clone, refresh or Resync) because of FR25;
  holocron then indexes its 118 non-vendored Go files minus generated ones, dsf-fam-service and ucc add their Java.
- Phases (each leaves every gate green): **A** Go (FR1–FR5, FR7, FR8, FR10–FR12, FR14, FR15, FR17–FR23, FR25–FR27
  for Go), **B** Java (FR6, FR9, FR13, FR16, FR24, Java parts of FR1, FR2, FR27), **C** UI (FR28–FR33).

## 10. Acceptance criteria
- AC1 [index] — Given a fixture Go module with `main.go`, `internal/api/router.go`, `internal/api/handler.go`,
  `internal/svc/service.go`, `internal/svc/service_test.go` and `gen/x.pb.go`, when a full index runs, then every file
  but `gen/x.pb.go` is indexed and `TotalCandidates` does not count it.
- AC2 [index] — Given the AC1 fixture, then `symbols` hold the functions, methods (receiver in the signature), struct
  (`class`), interface and other types with the right lines and `exported` per the first letter, and no `var`/`const`.
- AC3 [index] — Given the AC1 fixture, then `references` hold the called names, selector fields, function values
  passed as arguments (`h.GetX` → `GetX`) and composite-literal types.
- AC4 [graph] — Given the AC1 fixture, where `handler.go` imports `<module>/internal/svc`, `net/http` and
  `github.com/labstack/echo/v4`, then `file_edges` hold `internal/api/handler.go → internal/svc/service.go`, no edge to
  `internal/svc/service_test.go`, and no edge for the standard-library or external imports.
- AC5 [graph] — Given a fixture with a root `go.mod` (`example.com/r`) and `tools/go.mod` (`example.com/r/tools`), when
  a file imports `example.com/r/tools/x`, then its edge ends in `tools/x/`.
- AC6 [graph] — Given the AC1 fixture, when references are resolved, then a call in `handler.go` to an unexported
  function declared once in another file of `internal/api` has that file as `decl_file`, and a call from
  `service_test.go` to a function of `service.go` has `service.go`.
- AC7 [graph] — Given a name declared in two other files of the caller's package, then the reference's `decl_file`
  stays NULL; given a reference already resolved through an import edge, then the same-package step leaves it as it is.
- AC8 [facts] — Given Go lines `mux.HandleFunc("GET /users/{id}", h)`, `http.HandleFunc("/health", h)`,
  `e.POST("/items", h.Create)`, `pb.RegisterBillingServiceServer(s, srv)`, `c.AddFunc("@every 5m", f)`, then the facts
  are `GET /users/{id}`, `ANY /health`, `POST /items`, `GRPC BillingService` and the cron `@every 5m`; given
  `http.Get("https://x")` and `r.Get("/x", h)`, then nothing.
- AC9 [graph] — Given the AC1 fixture (`router.go` registers `e.GET("/x", h.GetX)`; `GetX` in `handler.go` calls the
  service), when the blast radius is read for a PR that changes a function of `internal/svc/service.go`, then it is not
  degraded, lists the changed symbol and lists `handler.go` and `service_test.go` as callers; for a PR that changes
  `GetX` in `handler.go`, then `router.go` is a caller with the endpoint `GET /x`.
- AC10 [index] — Given a fixture Spring project with `src/main/java/com/acme/web/OrderController.java` (class-level
  `@RequestMapping("/api/orders")`, a nested static class, `@GetMapping("/{id}")`, multi-line
  `@RequestMapping(value = "/search",\n method = RequestMethod.POST)`), `src/main/java/com/acme/svc/OrderService.java`
  and `src/test/java/com/acme/svc/OrderServiceTest.java`, when a full index runs, then all three are indexed with
  classes, the nested class, methods and constructors (`exported` from `public`/interface membership).
- AC11 [graph] — Given the AC10 fixture, then `OrderController.java → OrderService.java` is an edge (single-type
  import), a wildcard import of `com.acme.svc.*` yields edges to every file of the package, a static import resolves to
  its class file, and `java.util.List` yields none.
- AC12 [graph] — Given the AC10 fixture, then a call in `OrderServiceTest.java` to a package-private method of
  `OrderService` (same package `com.acme.svc`, other source root) resolves to `OrderService.java`.
- AC13 [facts] — Given the AC10 controller, then its facts are `GET /api/orders/{id}` and `POST /api/orders/search`;
  given `@Scheduled(cron = "0 */5 * * * *")` and `@Scheduled(fixedRate = 5000)`, then the crons are `0 */5 * * * *` and
  `fixedRate=5000`; given `@GetMapping(BASE + "/x")`, then no endpoint.
- AC14 [graph] — Given the AC10 fixture and a PR changing `OrderService`, then the blast radius lists
  `OrderController.java` as a caller with its two endpoints.
- AC15 [index] — Given a mixed fixture (Java + TS), then both languages are indexed in one state and TS resolution and
  facts are those of today (existing TS assertions reused).
- AC16 [index] — Given an indexed Go fixture, when only `go.mod` changes, then the next incremental run is a full index
  (state `stats` show the full-index keys).
- AC17 [index] — Given a hostile 400 KiB Go file and a hostile 400 KiB Java file, when extracted with a cancelled
  context or past `ParseTimeout`, then the call returns the context error within one guard interval.
- AC18 [facts] — The Java annotation scanner fuzz target runs 20 s (`-fuzzminimizetime 1x`) without a panic, and every
  output path is one of the input's string literals or their join with the class prefix.
- AC19 [db] — Migration 0013 up then down then up succeeds; the down migration leaves no `file_packages` table.
- AC20 [index] — The parity tests and every existing repo-intel test pass unchanged.
- AC21 [index] — `CallerSignatures` on a Go fixture with a `vendor/` directory returns callers from non-vendored files
  only.
- AC22 [ui] — Given `GET /repos/:id/index-state` answering `{status: partial, filesIndexed: 0, reason: no_files,
  lastIndexedSha: "abc"}`, then the Settings GitHub and GitLab rows, first-run setup, the onboarding empty state and the
  degraded blast card show the FR29–FR32 texts and the blast card has no Resync button; given `{status: partial,
  filesIndexed: 12, reason: "budget"}` (or any other reason), then each surface renders as today.
- AC23 [ui] — The i18n leak tests pass with the new keys.
- AC24 [index] — Live (after deploy, on this machine): holocron re-indexes with `filesIndexed > 0` and status `full`,
  an onboarding tour is generated, and the blast radius of one of its MRs is not degraded; dsf-fam-service indexes its
  Java files; dcg-orchestrator and ucc meet NFR1.

## 11. Traceability
| FR | AC |
|---|---|
| FR1–FR4 | AC1, AC10, AC15, AC17 |
| FR5, FR7 | AC2 |
| FR6 | AC10 |
| FR8, FR10 | AC3 |
| FR9 | AC12, AC14 |
| FR11–FR15, FR17 | AC4, AC5 |
| FR13, FR16 | AC11 |
| FR18–FR20 | AC6, AC7, AC12, AC19 |
| FR21–FR23 | AC8, AC9 |
| FR24 | AC13, AC14, AC18 |
| FR25, FR26 | AC16, AC24 |
| FR27 | AC21 |
| FR28–FR33 | AC22, AC23 |
| NFR1–NFR5 | AC24, AC17, AC20, AC23 |

## 12. Decisions
- D1 — tree-sitter for Go and Java (not `go/parser`, not regex): one parser path with the existing timeout, guard,
  error recovery and limits (user, 2026-10-28).
- D2 — Depth: core index plus endpoints/crons for the frameworks in the user's repos; the regex fallback extractor stays
  TS/JS only (user, 2026-10-28).
- D3 — Same-package links live in `file_packages` and a second resolution step, not in `file_edges`: pairwise package
  edges would add up to n² rows per package and skew the ranking (approved 2026-10-28).
- D4 — Go package key is the directory (external `_test` packages join their package); Java package key is the declared
  package (main and test source sets join).
- D5 — A Go import edges to every non-test file of the package directory (the package is the unit of import), not to
  the files that declare the used names.
- D6 — Test files are indexed (as TS/JS tests are today), so tests appear as callers in the blast radius.
- D7 — Generated Go files are excluded by name (`*.pb.go`, `*.pb.gw.go`), not by reading the "Code generated" header,
  so the walk stays content-free.
- D8 — Go function values passed as call arguments count as references, so a router counts as a caller of its handlers
  (FR8).
- D9 — The UI derives "no supported source files" from the existing `reason: "no_files"`; no contract or backend change
  for the UI phase.
- D10 — Phases: Go, then Java, then UI (user, 2026-10-28).

## 13. Open questions
None.
