# Context pack: repo-intel Go + Java
[A] `B/`=backend, `RI/`=B/internal/repointel, `TS/`=RI/adapters/treesitter, `IG/`=RI/domain/importgraph,
`PG/`=RI/adapters/postgres, `IT/`=RI/integration, `MG/`=B/internal/platform/postgres/migrate, `S/`=B/cmd/server ·
`F/`=frontend/src, `E/`=frontend/messages/en · spec = `specs/2026-10-28-repo-intel-go-java.md` (FR/EC/NFR/AC/D ids)

## Lane contracts (exact names; every task codes against these)
- [C1] `RI/domain` (model.go): `SyntaxGo`, `SyntaxJava` appended after `SyntaxJavaScript` (existing values keep their
  numbers); `String()` → `go`, `java`. New types:
  `type FileImports struct { Package string; Specs []string }` ·
  `type FilePackage struct { Path, Key string }` ·
  `type Graph struct { Edges []Edge; Packages []FilePackage }` (both slices non-nil) ·
  `func GoPackageKey(file string) string` → `"go:" + path.Dir(file)` · `func JavaPackageKey(pkg string) string` →
  `"java:" + pkg` · `IndexerVersion = 3` (Task 5).
- [C2] `RI/app` ports.go: `Analyzer.Imports(ctx, path, text string) (domain.FileImports, error)` ·
  `GraphBuilder.BuildGraph(ctx, root string, files []string) (domain.Graph, error)` (replaces `BuildEdges`) ·
  `IndexWriter.ReplaceFilePackages(ctx, repoID string, rows []domain.FilePackage) error`.
- [C3] `IG/`: `type SourceImports func(ctx context.Context, file string) domain.FileImports` ·
  `func Build(ctx, fs domain.SourceFS, root string, files []string, imports SourceImports) (g domain.Graph, aliasErr, err error)` ·
  `func NewGoResolver(ctx context.Context, tree Tree, files []string) (*GoResolver, error)` + `func (r *GoResolver) ResolveAll(spec string) []string` ·
  `func NewJavaResolver(pkgFiles map[string][]string) *JavaResolver` (key = package name without `java:`) +
  `func (r *JavaResolver) ResolveAll(spec string) []string`. `BuildEdges(ctx, files, *Resolver, SpecifierSource)` keeps
  its signature and tests (TS-only helper).
- [C4] `RI/domain` facts: `ExtractFacts(path, text)` keeps its signature and dispatches on `SyntaxForPath(path)`;
  `type Annotation struct { Name, Args string; Line int }` + `func JavaAnnotations(text string) []Annotation`
  (Args = text between the balanced parentheses, "" without; skipped when unclosed or > `MaxAnnotationBytes = 4096`).
- [C5] `TS/`: unexported per-language passes in `golang.go` / `java.go`:
  `extractGo(root sitter.Node, src []byte, g *guard) (domain.Extraction, error)`,
  `importsGo(root sitter.Node, src []byte, file string, g *guard) (domain.FileImports, error)`, same for `Java`.
  `Analyzer.Extract`/`Imports` switch on the `syntax` that `withTree` passes.

## Verified facts (main @ a0f6c1c + clone-retry change)
- [F1] Walk: `RI/domain/walkrules.go:11` `IsExcludedDir` (no `target`); `:44` `SelectFiles` (byte order, cap 5000).
  `SyntaxForPath` `RI/domain/model.go:122`.
- [F2] Parser dispatch: `TS/parser.go:93` `withTree` resolves the syntax and calls `fn(root, src, syntax, guard)`;
  `Extract` `:62`, `Imports` `:76` (TS imports call `extractImports(root, src, isTypeScriptPath(file), g)`).
  Helpers to reuse: `walk`, `field`, `children`, `eachChild`, `firstChildKind`, `nodeText`, `line`, `endLine`
  (`:167-245`); `guard.tick` every 1,024 nodes (`:24-46`). Symbol collector + signature helpers: `TS/symbols.go`,
  `TS/signature.go` (`MaxSignatureUnits`, collapse rule). Grammar table `TS/languages.go`.
- [F3] Graph: `RI/app/graph.go:36` `BuildEdges` → `importgraph.Build` (`IG/edges.go:110`); `specifiers` `:68` reads each
  file, parses under `ParseTimeout`, returns `analyzer.Imports`. `buildGraph` `:86` (graphFailed on builder error).
  `IG/edges.go:66` edge loop: dedupe, skip self, skip unwalked or unsupported targets, ctx every 256 specifiers.
  `Tree` (`IG/resolve.go:24`) has `IsFile`, `ReadFile`; walked tree knows every directory above a walked file
  (`HasFilesUnder`).
- [F4] Writes: full index `RI/app/full.go:90-115` (`ReplaceEdges` → `ResolveReferences(false)` → rank → map → facts,
  inside `InTx`, only when not budget-stopped); incremental `RI/app/incremental.go:104` rebuilds the graph over the whole
  walk, writes edges + `ResolveReferences(true)` + rank + map in a `Savepoint` (`:126-139`). Changed paths:
  `:52` `DiffNames` → `:60` `supportedPaths` (drops `go.mod`) → `no_supported_changes` when empty.
- [F5] Resolution SQL: `PG/write.go:140-176` (`UPDATE … FROM (SELECT … GROUP BY r.id HAVING count(DISTINCT e.to_file) = 1)`).
  Reset clears every `decl_file` first.
- [F6] Facts: `RI/domain/facts.go` (TS patterns, `ExtractEndpoints`, `ExtractCrons`, `ExtractFacts:103`), called from
  `RI/app/parse.go:123` per indexed file.
- [F7] Caller scan: `RI/domain/callers.go:39` `IsCallerScanDir` (`.git node_modules dist build .next coverage`), `:50`
  `IsCallerScanFile` (six TS/JS extensions, case-sensitive); used by `RI/adapters/clonetree/scan.go:29-30`.
- [F8] Callers of `Analyzer.Imports` to update: `RI/app/graph.go:76`, `RI/parity/parity_test.go:177`,
  `TS/{hostile_test,symbols_test,imports_test}.go`. Callers of `importgraph.Build`: `RI/app/graph.go:42`,
  `IG/{edges_test,limits_test}.go`, `RI/adapters/clonetree/clonetree_test.go:334`, `RI/parity/parity_test.go:187`.
  Fake graph: `RI/app/fakes_test.go:618-622`.
- [F9] `IndexerVersion` literal `2` in tests: `RI/domain/state_test.go`, `RI/app/{full,backfill,reader,service,fakes}_test.go`,
  `PG/{read,write}_test.go`, `IT/{full,incremental,blast}_test.go`; goldens
  `S/testdata/contracts/repo_index_state_{full,degraded,synth}.json` (written by `go test ./cmd/server -run Contracts -update`).
- [F10] Migrations: last is `B/migrations/0012_gitlab_ci.{up,down}.sql`; per-migration test file pattern
  `MG/migrate_0012_test.go` (`TestMigration0012_GitLabCI`, pinned name lists, up → down → up).
- [F11] Integration env: `IT/helpers_test.go:367` `newEnv(t, withFiles(map[string]string))`, `mustFull()`,
  `mustIncremental()`, `count(table)`, `exec(sql)`; git simulator `gitSim.Commit(write, del...)`; blast helpers
  `IT/blast_test.go:24-69` (`blastReader`, `blast(r, changed...)`).
- [F12] Grammar modules: `github.com/tree-sitter/tree-sitter-go` v0.25.0 (`bindings/go`, `Language()`),
  `github.com/tree-sitter/tree-sitter-java` v0.23.5 (`bindings/go`, `Language()`); binding go-tree-sitter v0.25.0.
- [F13] UI: index state hook `F/lib/hooks/repo-intel.ts:20` `useRepoIntelStatus`, `:31` `isRepoIndexed`; surfaces
  `F/app/settings/[section]/…/SettingsGithub/_components/RepoRow/RepoRow.tsx`,
  `F/app/settings/[section]/…/SettingsGitlab/_components/GitLabRepos/GitLabRepoRow.tsx:16-27`,
  `F/app/setup/_components/SetupView/_components/IndexProgress/IndexProgress.tsx`,
  `F/app/repos/[repoId]/onboarding/_components/TourView/{TourView.tsx:40,_components/EmptyTour/EmptyTour.tsx}`,
  `F/app/repos/[repoId]/pulls/[number]/_components/OverviewTab/_components/BlastRadiusCard/BlastRadiusCard.tsx:26-90`
  (props `prId, repoId, headSha`). Only locale: `E/`. Shared namespace `E/common.json`.

## Repo rules and notes
- [K1] `CLAUDE.md` engineering skills: load `go-architecture` + `go-testing` for Go steps; up + down migrations, never
  AutoMigrate; no API or contract change in this plan.
- [K2] `B/AGENTS.md` repointel conventions; domain imports stdlib only (+ `platform/codehost`).
- [K3] `B/INSIGHTS.md`: ResolveReferences self-join took 67 s → one aggregate per reference (2026-10-22, P14);
  hostile clone input → ctx tick + linear passes (refscan, blast heap entries); clone strings through `SanitizeText`;
  fuzz runs need `-fuzzminimizetime 1x`; host Go behind Zscaler: `GOPROXY='https://proxy.golang.org|direct'`;
  full gate in an `autocrlf=false` worktree (CRLF goldens); load-sensitive repointel timing tests flake under load.
- [K4] `F/INSIGHTS.md`: `renderWithProviders` + `mockFetch` for component tests; `pnpm exec vitest run <file>` for one
  file; separators inside message strings (stray-text test); `getByRole('status')` may hit the toast region.

## Skill map
Go code: `go-architecture`, `go-testing` · migration: `postgresql-table-design` · UI: `react-best-practices`,
`react-testing-library`, `typescript-expert` · everywhere: `security`.

## Verification sources
- [V1] one package: `cd backend && go test -count=1 ./internal/repointel/<pkg>/...`
- [V2] gates: `make backend-check`, `make frontend-check`, `make check` (in a `git -c core.autocrlf=false worktree`
  with `GIT_CONFIG_GLOBAL=/dev/null`, [K3])
- [V3] fuzz: `go test -run '^$' -fuzz FuzzJavaFacts -fuzztime 20s -fuzzminimizetime 1x ./internal/repointel/domain`
