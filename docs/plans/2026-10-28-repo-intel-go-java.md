# Repo intelligence for Go and Java — Implementation Plan
Plan: repo-intel Go + Java + honest "no source files" UI · Status: done · Spec: specs/2026-10-28-repo-intel-go-java.md
(approved by the user 2026-10-28) · Base: main @ a0f6c1c (+ uncommitted clone-retry change of 2026-10-28)
Packages: backend, frontend · Context pack: docs/plans/2026-10-28-repo-intel-go-java.context.md

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
> Load `go-architecture` + `go-testing` for every Go task (CLAUDE.md). Read the context pack, not the whole INSIGHTS files.

**Goal:** Index Go and Java (symbols, references, import graph, same-package resolution, endpoints, crons) so the
onboarding tour and blast radius work for Go/Java/mixed repos, then make the UI say when a repo has no indexable files.

**Architecture:** Two more tree-sitter grammars behind the existing `Analyzer` port, each language in its own file of
`adapters/treesitter`; per-language import resolvers in `domain/importgraph` behind one `Build` that now returns edges
plus package keys; a `file_packages` table feeding a second, same-package step of `ResolveReferences`; per-language fact
extractors chosen by file syntax. TS/JS paths stay byte-identical. The UI reads the existing `reason: "no_files"`.

**Tech Stack:** Go 1.27, go-tree-sitter v0.25.0 + tree-sitter-go v0.25.0 + tree-sitter-java v0.23.5 (CGO), GORM/pgx,
SQL migrations; Next.js 15, next-intl, vitest + RTL.

## Global Constraints
- No HTTP route, status code, JSON field or frontend contract changes (spec §7).
- TS/JS behaviour unchanged: parity goldens and every existing repo-intel test pass (NFR4, AC20).
- Limits unchanged: `MaxIndexedFiles` 5000, `MaxFileBytes` 409,600, `ParseTimeout` 2 s, `SoftBudget` 110 s,
  `MaxSignatureUnits` 120, `MaxNameUnits` 255; new: `go.mod` read ≤ 64 KiB, annotation ≤ 4 KiB.
- Every new extraction pass ticks the `guard`; every new scan is linear in its input (NFR3).
- `domain` packages import the standard library only ([K2]).
- Migration 0013 has an up and a down; never AutoMigrate ([K1]).
- Supported-languages line, exact copy: "Only TypeScript, JavaScript, Go and Java files are indexed." (FR33).

## Review Focus
1. Go `import . "x"` / blank / aliased imports and a file importing its own module root package → edges still go to
   the package's non-test files, never to itself (Task 3: `TestGoResolver_AliasDotBlankAndRoot`).
2. A Java file inside `src/main/java` whose `package` declaration does not match its directory → resolution uses the
   declaration, not the path (Task 10: `TestJavaResolver_PackageFromDeclarationNotPath`).
3. Annotation text with `)` or `@` inside string literals, text blocks (`"""…"""`) and comments → scanner neither
   splits nor invents annotations (Task 11: `TestJavaAnnotations_LiteralsAndComments`).
4. Incremental run after a repo switches from TS-only to adding Go files (indexer version unchanged, new `.go` files in
   the diff) → Go files indexed and `file_packages` written (Task 8: `TestGo_IncrementalAddsGoFiles`).
5. Same-package step on a repo where a name is declared in the caller's own file and once elsewhere in the package →
   the other file wins (F excluded), not "ambiguous" (Task 4: `TestResolveReferences_SamePackageExcludesOwnFile`).

## Decisions (plan-level; spec decisions D1–D10 apply)
- P1 Port name stays `Imports` and returns `domain.FileImports` (spec FR11); all callers updated in Task 2 ([F8]).
- P2 `GraphBuilder.BuildEdges` becomes `BuildGraph` returning `domain.Graph`; `importgraph.Build` returns `domain.Graph`.
  `importgraph.BuildEdges` (TS resolver only) keeps its signature for its tests; `Build` reuses its loop through an
  unexported `collectEdges(ctx, files, resolve func(from, spec string) []string, imports map[string]domain.FileImports)`.
- P3 `Build` reads each file's imports once into a map before resolving (the Java resolver needs every package key
  first); memory is bounded by the walk (≤ 5000 files).
- P4 Java static specifiers carry the `static:` prefix (FR13); TS/Go specifiers are unchanged.
- P5 Generated-Go exclusion lives in `SelectFiles` via `isGeneratedGoPath` (FR3), not in `IsSupported`, so a changed
  `.pb.go` in an incremental diff is still a supported path but never walked (it is simply absent from the walk).
- P6 Goldens with `indexerVersion: 2` are regenerated with `go test ./cmd/server -run Contracts -update`, then
  reproduced with a second run and a clean `git status` ([F9], [K3]).
- P7 Phases A (Tasks 1–8), B (9–12), C (13), close (14); each task commits on its own and leaves [V1] green.

---

## Phase A — Go

### Task 1: Domain — Go syntax, package keys, generated-file exclusion
**Files:** Modify `RI/domain/model.go`, `RI/domain/walkrules.go` · Test: create `RI/domain/model_test.go`, modify `RI/domain/walkrules_test.go`
**Interfaces:** Produces [C1] (`SyntaxGo`, `FileImports`, `FilePackage`, `Graph`, `GoPackageKey`; `SyntaxJava`/
`JavaPackageKey` come in Task 9).
- [ ] **Step 1: failing tests** — `TestSyntaxForPath_Go` (table: `a.go`, `A.GO` → `SyntaxGo`, `String()=="go"`;
  `a.go.txt` → not ok); `TestGoPackageKey` (`main.go` → `go:.`, `internal/api/h.go` → `go:internal/api`);
  `TestSelectFiles_SkipsGeneratedGo` (entries `x.go`, `gen/x.pb.go`, `gen/y_grpc.pb.go`, `gw/z.pb.gw.go` → `Files ==
  [x.go]`, `TotalCandidates == 1`).
- [ ] **Step 2:** `go test ./internal/repointel/domain/ -run 'Go|Generated'` → FAIL (undefined `SyntaxGo`).
- [ ] **Step 3:** add `SyntaxGo` after `SyntaxJavaScript`, `.go` in `SyntaxForPath` (update its doc: "supported
  extensions"), the three types and `GoPackageKey` ([C1]); `isGeneratedGoPath(p) bool` (suffix `.pb.go` or `.pb.gw.go`)
  checked in `SelectFiles` before counting a candidate.
- [ ] **Step 4:** [V1] `./internal/repointel/domain/...` → PASS (existing walk tests unchanged).
- [ ] **Step 5:** commit `repo-intel: Go syntax, package keys, skip generated Go (FR1, FR3)`.

### Task 2: Analyzer port returns `FileImports`; Go grammar, symbols, references, imports
**Files:** Modify `B/go.mod`, `B/go.sum`, `TS/languages.go`, `TS/parser.go`, `RI/app/ports.go`, `RI/app/graph.go`
(specifiers return `FileImports`), `RI/parity/parity_test.go`, `TS/{hostile,symbols,imports}_test.go` (`.Specs`) ·
Create `TS/golang.go` · Test `TS/golang_test.go`, `TS/hostile_test.go`
**Interfaces:** Consumes [C1]. Produces [C2] `Analyzer.Imports` → `domain.FileImports`; [C5] Go passes.
- [ ] **Step 1: add the grammar** — `go get github.com/tree-sitter/tree-sitter-go@v0.25.0` (Zscaler: [K3] GOPROXY);
  `TS/languages.go` maps `SyntaxGo` → `sitter.NewLanguage(golang.Language())`. `TestLanguage_ParsesEachGrammar` gains a Go row.
- [ ] **Step 2: failing tests** in `TS/golang_test.go` (table, `cmp.Diff`, black-box through `treesitter.New()`):
  - `TestExtractGo_Symbols`: source with `func Run()`, `func helper()`, `func (s *Server) Start(ctx context.Context) error {`,
    `type Server struct{…}`, `type Store interface{…}`, `type ID string`, `var X = 1`, `const Y = 2` → symbols
    `Run function exported`, `helper function !exported`, `Start method exported` with signature
    `func (s *Server) Start(ctx context.Context) error`, `Server class`, `Store interface`, `ID type`; lines/end lines
    exact; no `X`/`Y`.
  - `TestExtractGo_References`: `svc.Do(x)`, `run()`, `e.GET("/x", h.GetX)`, `mux.HandleFunc("/y", handleY)`,
    `_ = pkg.Config{}`, `T{}` → names `Do, run, GET, GetX, HandleFunc, handleY, Config, T` with their lines.
  - `TestImportsGo`: file `internal/api/h.go` with `import ( "fmt"; x "example.com/r/internal/svc"; . "example.com/r/dot"; _ "example.com/r/side" )`
    → `FileImports{Package: "go:internal/api", Specs: ["fmt","example.com/r/internal/svc","example.com/r/dot","example.com/r/side"]}`.
  - `TestImports_TypeScriptUnchanged`: an existing TS case returns `Package: ""` and the same specs.
  - `TestHostileFiles_StopAtTheDeadline` gains a 400 KiB Go row (deeply nested `func(){` + 50k calls) → error wraps
    `context.DeadlineExceeded` within the existing bound (AC17).
- [ ] **Step 3:** run `go test ./internal/repointel/adapters/treesitter/` → FAIL.
- [ ] **Step 4: implement** — port change [C2] and callers ([F8]); `Extract`/`Imports` switch on syntax: TS/JS paths
  untouched, Go → `extractGo` / `importsGo` ([C5]) in `TS/golang.go`. Node kinds: `function_declaration`,
  `method_declaration` (receiver from field `receiver`), `type_spec` with `type` field `struct_type` / `interface_type`
  / other; references from `call_expression` (`function` field: `identifier` or `selector_expression.field`), the
  `identifier`/`selector_expression` children of each call's `argument_list`, `composite_literal` `type`
  (`type_identifier` or `qualified_type.name`); imports from `import_spec.path` (unquote with `strconv.Unquote`, drop on
  error). Exported = `unicode.IsUpper(first rune)`. Signatures through the existing signature helper (head up to the
  body, collapsed, ≤ 120 units). Every walk ticks the guard.
- [ ] **Step 5:** [V1] `./internal/repointel/...` → PASS (parity included).
- [ ] **Step 6:** commit `repo-intel: tree-sitter Go symbols, references, imports; Imports returns FileImports (FR4, FR5, FR7, FR8, FR11, FR12)`.

### Task 3: Import graph — Go resolver and `Build` → `domain.Graph`
**Files:** Create `IG/gomod.go` · Modify `IG/edges.go`, `RI/app/graph.go` (`BuildGraph`), `RI/app/ports.go`
(`GraphBuilder`), `RI/app/fakes_test.go`, `RI/app/graph_test.go`, `IG/{edges,limits}_test.go`,
`RI/adapters/clonetree/clonetree_test.go`, `RI/parity/parity_test.go` · Test `IG/gomod_test.go`
**Interfaces:** Consumes [C1], [C2] `Imports`. Produces [C2] `BuildGraph`, [C3] `Build`, `SourceImports`,
`NewGoResolver`/`ResolveAll`.
- [ ] **Step 1: failing tests** (`IG/gomod_test.go`, fake `Tree` from the existing tests):
  - `TestGoResolver_SingleModule`: `go.mod` `module example.com/r`; files `internal/svc/{a.go,b.go,a_test.go}` →
    `ResolveAll("example.com/r/internal/svc") == [internal/svc/a.go internal/svc/b.go]`; `"fmt"`, `"github.com/x/y"` → nil.
  - `TestGoResolver_NestedModuleLongestPrefix` (AC5): root `example.com/r`, `tools/go.mod` `example.com/r/tools` →
    `example.com/r/tools/x` → files under `tools/x/`.
  - `TestGoResolver_GoModWithoutModuleOrTooLarge` (EC3): no `module` line / 65 KiB file → nil, no panic.
  - `TestGoResolver_AliasDotBlankAndRoot` (Review Focus 1): root-package import `example.com/r` from `main.go` →
    other root `.go` files, never `main.go` itself (via `Build`).
  - `TestBuild_GoGraph`: through `Build` with `SourceImports` stubs → `Graph.Edges` as above, `Graph.Packages` one row per
    Go file with `GoPackageKey`, TS files have no package row; edges deduped.
  - existing `Build`/`BuildEdges` tests adapted to the new signature with unchanged expectations.
- [ ] **Step 2:** `go test ./internal/repointel/domain/importgraph/` → FAIL.
- [ ] **Step 3: implement** — `GoResolver`: modules from `go.mod` in every directory holding a walked file plus `.`
  (read ≤ 64 KiB, first `module` directive, quoted or bare); `filesByDir` of walked non-`_test.go` `.go` files;
  longest matching module path (equal, or prefix + `/`). `Build` (P2, P3): load TS aliases as today, read every file's
  `FileImports` once, route each spec by importer syntax (TS → `Resolver.Resolve` as a 0/1-element slice, Go →
  `GoResolver.ResolveAll`), `collectEdges` keeps the dedupe/self/unwalked/ctx rules of [F3]; packages from non-empty
  keys. App: `ImportGraph.BuildGraph` + `buildGraph` return `domain.Graph`; `fakeGraph` follows.
- [ ] **Step 4:** [V1] `./internal/repointel/...` → PASS.
- [ ] **Step 5:** commit `repo-intel: Go import resolution, graph with package keys (FR14, FR15, FR17)`.

### Task 4: Migration 0013 and same-package resolution
**Files:** Create `B/migrations/0013_file_packages.{up,down}.sql`, `MG/migrate_0013_test.go` · Modify `PG/write.go`,
`PG/rows.go`, `RI/app/ports.go` (`ReplaceFilePackages`), `RI/app/fakes_test.go` · Test `PG/write_test.go`
**Interfaces:** Consumes [C1] `FilePackage`. Produces [C2] `ReplaceFilePackages`; extended `ResolveReferences`.
- [ ] **Step 1: failing tests**
  - `TestMigration0013_FilePackages` (AC19, pattern [F10]): up to 12 → up 13 (table + index
    `file_packages_repo_key_idx` exist, cascade on repo delete) → down (table gone) → up again.
  - `PG/write_test.go`: `TestReplaceFilePackages` (replace semantics, `SanitizeText` on paths);
    `TestResolveReferences_SamePackage` (AC6: `a.go` refs `helper` declared unexported once in `b.go`, same key →
    `decl_file = b.go`); `TestResolveReferences_SamePackageAmbiguous` (EC7, declared in `b.go` and `c.go` → NULL);
    `TestResolveReferences_ImportStepWins` (EC6: an import-resolved row keeps its file);
    `TestResolveReferences_SamePackageExcludesOwnFile` (Review Focus 5); `TestResolveReferences_TSUnaffected` (TS rows
    without keys keep today's result); `reset=true` clears and recomputes both steps.
- [ ] **Step 2:** run `go test ./internal/platform/postgres/migrate/ -run 0013` and `./internal/repointel/adapters/postgres/` → FAIL.
- [ ] **Step 3: implement** — DDL per FR18 (`file_packages_repo_id_file_path_pk`, index on `(repo_id, package_key)`);
  down drops it. `ReplaceFilePackages` = delete + `insertChunks`. Second statement in `ResolveReferences` after the
  import step, same shape as [F5]:
  ```sql
  UPDATE "references" r SET decl_file = c.decl
  FROM (
    SELECT r.id AS ref_id, min(s.path) AS decl
    FROM "references" r
    JOIN file_packages fp ON fp.repo_id = r.repo_id AND fp.file_path = r.from_path
    JOIN file_packages op ON op.repo_id = r.repo_id AND op.package_key = fp.package_key AND op.file_path <> r.from_path
    JOIN symbols s ON s.repo_id = r.repo_id AND s.path = op.file_path AND s.name = r.to_symbol
    WHERE r.repo_id = ? AND r.decl_file IS NULL
    GROUP BY r.id
    HAVING count(DISTINCT s.path) = 1
  ) c
  WHERE r.id = c.ref_id
  ```
- [ ] **Step 4:** both packages PASS.
- [ ] **Step 5:** commit `repo-intel: file_packages (0013) and same-package reference resolution (FR18, FR20)`.

### Task 5: Indexer wiring — packages written, `go.mod` triggers full, version 3
**Files:** Modify `RI/app/full.go`, `RI/app/incremental.go`, `RI/domain/model.go` (`IndexerVersion = 3`), tests with
literal `2` ([F9]), `S/testdata/contracts/repo_index_state_*.json` (P6) · Test `RI/app/full_test.go`, `RI/app/incremental_test.go`
**Interfaces:** Consumes [C2] `BuildGraph`, `ReplaceFilePackages`.
- [ ] **Step 1: failing tests** — `TestFull_WritesFilePackages` (fake store records `ReplaceFilePackages` with the
  graph's packages, before `ResolveReferences`; skipped when budget-stopped like edges); `TestIncremental_WritesFilePackages`
  (inside the savepoint); `TestIncremental_GoModChangeRunsFull` (AC16: `DiffNames` → `["go.mod"]` → result is a full
  index); `TestIncremental_NestedGoModRunsFull` (`tools/go.mod`).
- [ ] **Step 2:** `go test ./internal/repointel/app/` → FAIL.
- [ ] **Step 3: implement** — write packages next to `ReplaceEdges` in both paths ([F4]); in `incremental` before
  `supportedPaths`: `if hasGoMod(changedAll) { return ix.full(ctx, repoID) }` (`path.Base(p) == "go.mod"`); bump
  `IndexerVersion`; replace literal `2` in tests by `domain.IndexerVersion` where the test means "current version";
  regenerate the three goldens (P6).
- [ ] **Step 4:** [V1] `./internal/repointel/...` and `go test ./cmd/server -run 'Contracts|Index'` → PASS; golden diff
  shows only `indexerVersion`.
- [ ] **Step 5:** commit `repo-intel: write package keys, go.mod forces a full index, indexer version 3 (FR19, FR25, FR26)`.

### Task 6: Go endpoints and crons
**Files:** Create `RI/domain/facts_go.go` · Modify `RI/domain/facts.go` (`ExtractFacts` dispatch) · Test `RI/domain/facts_go_test.go`
**Interfaces:** Produces [C4] dispatch (Java branch added in Task 11).
- [ ] **Step 1: failing tests** — `TestExtractFacts_Go` (AC8 table): `mux.HandleFunc("GET /users/{id}", h)` →
  `GET /users/{id}`; `mux.Handle("POST example.com/items", h)` → `POST /items`; `http.HandleFunc("/health", h)` →
  `ANY /health`; `e.POST("/items", h.Create)` → `POST /items`; `g.Any("/w", h)` → `ANY /w`;
  `pb.RegisterBillingServiceServer(s, srv)` → `GRPC BillingService`; `c.AddFunc("@every 5m", f)` and
  `c.AddJob("*/5 * * * *", j)` → crons; negatives (EC9) `http.Get("https://x")`, `client.Post("/x", b)`,
  `r.Get("/x", h)`, `e.GET(path, h)` → nothing. `TestExtractFacts_GoIgnoresTSPatterns` (`app.get('/x'` in a `.go`
  file → nothing) and `TestExtractFacts_TSUnchanged` (existing TS table reused through a `.ts` path).
- [ ] **Step 2:** `go test ./internal/repointel/domain/ -run Facts` → FAIL.
- [ ] **Step 3: implement** — `extractGoFacts(text string) (endpoints, crons []string)` with RE2 patterns per FR22/FR23
  (one line at a time, `newOrderedSet`); `ExtractFacts` switches on `SyntaxForPath(path)`: Go → Go set, TS/JS → today's
  functions, other → none.
- [ ] **Step 4:** PASS. **Step 5:** commit `repo-intel: Go endpoints and crons (FR21–FR23)`.

### Task 7: Caller scan reads Go and Java, skips vendor and target
**Files:** Modify `RI/domain/callers.go` · Test `RI/domain/callers_test.go`
- [ ] **Step 1: failing tests** — `TestIsCallerScanFile` gains `.go`, `.java` (true) and `.GO` (false, case-sensitive
  like today); `TestIsCallerScanDir` gains `vendor`, `target` (true).
- [ ] **Step 2:** FAIL. **Step 3:** extend both switches. **Step 4:** PASS (the TS caller tests unchanged).
- [ ] **Step 5:** commit `repo-intel: caller scan reads .go/.java, skips vendor and target (FR27)`.

### Task 8: Go integration fixture (end to end on a real database)
**Files:** Create `IT/golang_test.go`
**Interfaces:** Consumes everything above; helpers [F11].
- [ ] **Step 1: tests** with `newEnv(t, withFiles(goFixture()))`, where `goFixture()` is the AC1 module
  (`go.mod` `module example.com/r`; `main.go`; `internal/api/router.go` registering `e.GET("/x", h.GetX)`;
  `internal/api/handler.go` with `GetX` calling `svc.Do` and an unexported helper declared once in `internal/api/util.go`;
  `internal/svc/service.go` (`Do`), `internal/svc/service_test.go` calling `Do`; `gen/x.pb.go`):
  - `TestGo_FullIndex` (AC1–AC4): state `full`, `filesIndexed == 6`, symbols/edges/`file_packages` counts and the
    specific rows of AC2–AC4.
  - `TestGo_ReferenceResolution` (AC6, AC7): `decl_file` of the helper call = `internal/api/util.go`, of the test's
    `Do` = `internal/svc/service.go`, of `GetX` from `router.go` = `handler.go`.
  - `TestGo_BlastRadius` (AC9): `blast(r, "internal/svc/service.go")` not degraded, callers `handler.go`,
    `service_test.go`; `blast(r, "internal/api/handler.go")` has caller `router.go` with endpoint `GET /x`.
  - `TestGo_IncrementalAddsGoFiles` (Review Focus 4): start from `baseFiles()` (TS), commit the Go fixture, incremental
    → Go symbols and package rows present.
  - `TestGo_GoModChangeRunsFull` (AC16) and `TestGo_CallerScanSkipsVendor` (AC21: a `vendor/x/x.go` calling `Do` is not
    returned by `CallerSignatures`).
- [ ] **Step 2:** `go test -count=1 ./internal/repointel/integration/ -run TestGo_` → PASS (fix forward in the owning
  task's code if one fails).
- [ ] **Step 3:** commit `repo-intel: Go integration tests (AC1–AC9, AC16, AC21)`.

## Phase B — Java

### Task 9: Java syntax, grammar, symbols, references, imports
**Files:** Modify `RI/domain/model.go` (`SyntaxJava`, `.java`, `JavaPackageKey`), `RI/domain/walkrules.go` (`target`),
`B/go.mod`, `TS/languages.go`, `TS/parser.go` · Create `TS/java.go` · Test: create `TS/java_test.go`; modify `RI/domain/{model,walkrules}_test.go`, `TS/hostile_test.go`
- [ ] **Step 1: failing tests** — domain: `.java` → `SyntaxJava` (`String()=="java"`), `JavaPackageKey("a.b") == "java:a.b"`,
  `IsExcludedDir("target")`. Adapter (`go get github.com/tree-sitter/tree-sitter-java@v0.23.5` first):
  - `TestExtractJava_Symbols` (AC10): `public class OrderController` with a `static class Page`, a `record R(int a)`,
    `interface Repo { List<X> all(); }`, `enum Kind { A }`, `@interface Audit {}`, a public constructor, a
    package-private method → kinds `class, class, class, interface, method, enum, interface, method, method`;
    `exported` true for public and for `all` (interface member), false for the package-private method; signatures
    collapsed and ≤ 120 units.
  - `TestExtractJava_References`: `service.find(id)`, `new Order()`, `new java.util.ArrayList<>()`, `@Transactional`,
    `@GetMapping("/x")` → `find, Order, ArrayList, Transactional, GetMapping`.
  - `TestImportsJava` (FR13): `package com.acme.web;` + single, wildcard, static, static-wildcard imports →
    `Package "java:com.acme.web"`, specs `com.acme.svc.OrderService`, `com.acme.svc.*`, `static:com.acme.util.Ids.next`,
    `static:com.acme.util.Ids.*`; no package declaration → `java:`.
  - hostile Java row (400 KiB nested classes/annotations) in `TestHostileFiles_StopAtTheDeadline` (AC17).
- [ ] **Step 2:** FAIL. **Step 3: implement** `extractJava`/`importsJava` ([C5]): node kinds
  `class_declaration`, `record_declaration`, `interface_declaration`, `annotation_type_declaration`,
  `enum_declaration`, `method_declaration`, `constructor_declaration` at any depth (walk the class bodies);
  `public` from the `modifiers` child; interface/annotation members exported; references from
  `method_invocation.name`, `object_creation_expression.type` (last identifier of a scoped/generic type),
  `annotation`/`marker_annotation` name; imports from `import_declaration` (`static` child → prefix, `asterisk` → `.*`),
  package from `package_declaration`.
- [ ] **Step 4:** [V1] `./internal/repointel/...` → PASS. **Step 5:** commit `repo-intel: tree-sitter Java (FR1, FR2, FR4, FR6, FR9, FR13)`.

### Task 10: Java import resolution
**Files:** Create `IG/javapkg.go` · Modify `IG/edges.go` (route Java specs) · Test `IG/javapkg_test.go`
- [ ] **Step 1: failing tests** — `TestJavaResolver` table (AC11): `com.acme.svc.OrderService` → its file;
  `com.acme.svc.OrderService.Page` → same file; `com.acme.svc.*` → every file of the package;
  `static:com.acme.util.Ids.next` and `static:com.acme.util.Ids.*` → `Ids.java`; `java.util.List` → nil;
  duplicated `C.java` in two source roots of one package (EC4) → both; `TestJavaResolver_PackageFromDeclarationNotPath`
  (Review Focus 2); `TestBuild_JavaGraph` via `Build` (packages `java:<pkg>` per file, main and test share a key).
- [ ] **Step 2:** FAIL. **Step 3: implement** `NewJavaResolver(pkgFiles)` / `ResolveAll` per FR16 (strip trailing
  segments while ≥ 2 remain; class file match by `path.Base == Name + ".java"`); `Build` builds `pkgFiles` from the
  imports map and routes Java importers to it.
- [ ] **Step 4:** PASS. **Step 5:** commit `repo-intel: Java import resolution (FR16)`.

### Task 11: Java annotation scanner, Spring endpoints, `@Scheduled`
**Files:** Create `RI/domain/facts_java.go` · Modify `RI/domain/facts.go` (dispatch) · Test
`RI/domain/facts_java_test.go`, `RI/domain/facts_java_fuzz_test.go`
**Interfaces:** Produces [C4] `JavaAnnotations`, `Annotation`, `MaxAnnotationBytes`.
- [ ] **Step 1: failing tests**
  - `TestJavaAnnotations`: names, args across lines, `Line`; `TestJavaAnnotations_LiteralsAndComments` (Review Focus 3:
    `")"`, `'('`, `"@x"`, `"""…)…"""`, `// @Fake(`, `/* @Fake( */`); `TestJavaAnnotations_UnclosedOrTooLong` (EC10:
    skipped, scanning resumes, following annotation found).
  - `TestExtractFacts_Spring` (AC13): class prefix `@RequestMapping("/api/orders")` + `@GetMapping("/{id}")` →
    `GET /api/orders/{id}`; multi-line `@RequestMapping(value = "/search",\n method = RequestMethod.POST)` →
    `POST /api/orders/search`; `@PutMapping(path = {"/a", "/b"})` → `PUT /api/orders/a`, `PUT /api/orders/b`;
    `@RequestMapping(value="/m", method={RequestMethod.GET, RequestMethod.HEAD})` → two endpoints; `@DeleteMapping` →
    `DELETE /api/orders`; no class prefix (EC11) → plain paths; prefix only + no-arg mapping with empty join → `/`;
    `@GetMapping(BASE + "/x")` → nothing; crons `@Scheduled(cron = "0 */5 * * * *")` → `0 */5 * * * *`,
    `cron = "${jobs.cron}"` → `${jobs.cron}`, `fixedRate = 5000` → `fixedRate=5000`, `fixedDelayString = "PT5M"` →
    `fixedDelayString=PT5M`.
  - `FuzzJavaFacts` (AC18): seeds from the table; property: no panic, and every endpoint path is a string literal of
    the input or `join(prefixLiteral, literal)` of two literals of the input (check by collecting the input's literals
    with the same scanner).
- [ ] **Step 2:** FAIL. **Step 3: implement** — `JavaAnnotations` as a single forward pass with a small state machine
  (code / line comment / block comment / string / char / text block), balanced-paren argument capture capped at
  `MaxAnnotationBytes`; argument parsing of `value`/`path`/`method`/`cron`/`fixed*` over the captured `Args` (string
  literals and `{…}` arrays only; anything else marks that path skipped); class prefix = first `@RequestMapping` before
  the first `class`/`interface` keyword outside comments/strings; `join` with exactly one `/`.
- [ ] **Step 4:** PASS; [V3] fuzz 20 s clean. **Step 5:** commit `repo-intel: Spring endpoints and @Scheduled crons (FR24)`.

### Task 12: Java and mixed integration fixtures
**Files:** Create `IT/java_test.go`
- [ ] **Step 1: tests** — `javaFixture()` per AC10 (controller in `src/main/java/com/acme/web`, service in
  `src/main/java/com/acme/svc`, test in `src/test/java/com/acme/svc` calling a package-private method, a `target/`
  copy that must not be walked):
  - `TestJava_FullIndex` (AC10, AC11): files, symbol kinds, edges (single, wildcard, static), no `target/` rows.
  - `TestJava_SamePackageAcrossSourceRoots` (AC12).
  - `TestJava_FactsAndBlast` (AC13, AC14): controller facts; `blast(r, service)` lists the controller with both endpoints.
  - `TestMixed_JavaAndTS` (AC15): `javaFixture()` ∪ `baseFiles()` → one `full` state; the TS edges/facts equal those of
    the TS-only run of the existing `full_test.go` assertions.
- [ ] **Step 2:** `go test -count=1 ./internal/repointel/integration/ -run 'TestJava_|TestMixed_'` → PASS.
- [ ] **Step 3:** commit `repo-intel: Java and mixed integration tests (AC10–AC15)`.

## Phase C — UI

### Task 13: "No supported source files" state
**Files:** Modify `F/lib/hooks/repo-intel.ts`, `E/common.json`, `E/{githubIntegration,gitlabIntegration,setup,onboarding,blast}.json`,
`RepoRow.tsx`, `GitLabRepoRow.tsx`, `IndexProgress.tsx`, `TourView.tsx`, `EmptyTour.tsx`, `BlastRadiusCard.tsx` ([F13]) ·
Test: create `F/lib/hooks/repo-intel.test.ts`, `GitLabRepoRow.test.tsx`; modify `RepoRow.test.tsx`, `IndexProgress.test.tsx`,
`TourView.test.tsx`, `BlastRadiusCard.test.tsx`
**Interfaces:** Produces `hasNoSourceFiles(state: RepoIndexState | undefined): boolean`; message key
`common.repoIntel.supportedLanguages`.
- [ ] **Step 1: failing tests** (`renderWithProviders` + `mockFetch` [K4]):
  - `hasNoSourceFiles`: true for `{lastIndexedSha:"abc", reason:"no_files", filesIndexed:0}`; false for empty sha
    (EC13), other reasons, `filesIndexed > 0`, `undefined`.
  - each surface with `GET /repos/:id/index-state` → `{status:"partial", filesIndexed:0, reason:"no_files", lastIndexedSha:"abc", …}`
    shows its FR29–FR32 text and "Only TypeScript, JavaScript, Go and Java files are indexed."; the blast card (degraded
    `index_partial` answer) has no button named Resync and still shows "This is unknown, not “no impact”.";
    with `{filesIndexed:12, reason:"budget"}` each surface renders exactly as before (AC22).
- [ ] **Step 2:** `pnpm exec vitest run <files>` → FAIL.
- [ ] **Step 3: implement** — helper next to `isRepoIndexed`; texts: Settings rows `"no supported source files"`
  (both integration namespaces), setup `"{name} has no supported source files"`, onboarding
  `"This repository has no source files DevDigest can index, so a tour can't be generated."`, blast
  `"This repository has no source files DevDigest can index, so callers can't be looked up."`; the languages line from
  `common.repoIntel.supportedLanguages`. The blast card calls `useRepoIntelStatus(repoId)` (shared cache) and switches
  only when `data.degraded && hasNoSourceFiles(state)`.
- [ ] **Step 4:** `make frontend-check` → PASS (typecheck, Biome, vitest incl. pseudo-message/stray-text tests, AC23).
- [ ] **Step 5:** commit `UI: honest "no supported source files" state (FR28–FR33)`.

## Close

### Task 14: Gates, live acceptance, docs
- [x] **Step 1:** `make check` in an `autocrlf=false` worktree ([V2]); all green (load-sensitive timing tests re-run
  alone per [K3]).
- [x] **Step 2:** review gate — project `architecture-reviewer` (base `main`) ∥ `security` skill review (hostile clone
  input: grammars, annotation scanner, `go.mod` read, SQL) ∥ plan verification against the spec; fix rounds as needed.
- [x] **Step 3:** rebuild the stack (`docker compose up -d --build backend frontend`), Resync holocron, dsf-fam-service,
  dcg-orchestrator, ucc. Record for AC24/NFR1/NFR2: holocron `full` with `filesIndexed > 0`, tour generated, blast of
  one MR not degraded; dsf-fam-service Java files indexed; dcg-orchestrator and ucc `durationMs` < 110,000 and the
  same-package step ≤ the import step (log/`EXPLAIN ANALYZE` on the live db).
  Decision (user, 2026-10-28): dcg-orchestrator and ucc are not registered in this stack, so AC24 is closed without
  them; NFR1/NFR2 were measured on holocron and dsf-fam-service only (results in the spec's AC24 note).
- [x] **Step 4:** spec status → implemented; `docs/migration/STATUS.md` line; `backend/AGENTS.md` repointel note
  (languages, `file_packages`); engineering-insights WRAP-UP for backend and frontend.
- [x] **Step 5:** commit `repo-intel Go/Java done: gates, live acceptance, docs`.

## Execution
- Mode: sequential by phase (tasks share `ports.go`, `edges.go`, `fakes_test.go`); inside Phase A, Tasks 6 and 7 are
  independent of 2–5 and may run in parallel lanes; Phase B Tasks 10 and 11 likewise after Task 9.
- Each task: failing test → implement → [V1] → commit; Phase ends with `make backend-check` (A, B) / `make frontend-check` (C).

## Verification
- Gates: [V1] per task, [V2] per phase and at close, [V3] for Task 11.
- Review risk: medium-high — untrusted clone input into two new parsers and a hand-written scanner, a resolution query on
  the hot index path, a schema migration, an indexer-version bump that re-indexes every repo.

## Spec traceability
FR1 T1/T9 · FR2 T9 · FR3 T1 · FR4 T2/T9 · FR5 T2 · FR6 T9 · FR7 T2/T9 · FR8 T2 · FR9 T9 · FR10 T2/T9 · FR11–FR12 T2 ·
FR13 T9 · FR14–FR15 T3 · FR16 T10 · FR17 T3/T10 · FR18 T4 · FR19 T5 · FR20 T4 · FR21–FR23 T6 · FR24 T11 · FR25–FR26 T5 ·
FR27 T7 · FR28–FR33 T13 · NFR1–NFR2 T14 · NFR3 T2/T9/T11 · NFR4 T2–T5 (parity in [V1]) · NFR5 T13 ·
AC1–AC9, AC16, AC21 T8 · AC10–AC15 T12 · AC17 T2/T9 · AC18 T11 · AC19 T4 · AC20 every task · AC22–AC23 T13 · AC24 T14.
