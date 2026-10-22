# Parity corpus and Node golden generator (Phase 3, S11)

`../corpus/` holds committed fixture sources and case lists. `../goldens/*.json` are the expected
outputs, produced by running the **Node** implementation of dev-digest (commit `d1c9093`) from the
read-only checkout `<dev-digest>` (here `/home/ypodop/IdeaProjects/dev-digest`). Spec: NFR3008, AC3049,
AC3068, D3017, D3019.

## Regenerate

From this repository, with `SCRATCH` any directory outside both checkouts (tsx writes its cache there):

```sh
cd backend/internal/repointel/parity/testdata
DD=/home/ypodop/IdeaProjects/dev-digest
TMPDIR=$SCRATCH $DD/server/node_modules/.bin/tsx --tsconfig $DD/server/tsconfig.json \
  gen/generate.mts --dd $DD --out goldens
```

The generator refuses to run unless `<dev-digest>` is at `d1c9093` with a clean `git status`, checks
that again after the run, uses only read-only git commands (`--no-optional-locks`), and writes only
below `--out` (which must lie inside `testdata/`). It never installs anything: it needs the checkout's
own `server/node_modules` (tsx 4.22.4, TypeScript 5.9.3, dependency-cruiser 17.4.3, @ast-grep/napi 0.43.0,
js-tiktoken 1.0.21, graphology 0.26.0). The output is deterministic: a second run reproduces the first
byte for byte (AC3068). `goldens/meta.json` records the commit and tool versions.

Check reproducibility: run twice, then
`git add -N backend/internal/repointel/parity/testdata && git diff --exit-code -- backend/internal/repointel/parity/testdata`
(undo the intent-to-add with `git reset -q -- backend/internal/repointel/parity/testdata`).

## What is compared

| Golden | Node code | Corpus | Compare |
|---|---|---|---|
| `extract.json` | `parseSourceFile` with `astGrepSourceAnalyzer` (`parseSymbols`, `parseReferences`), `extractEndpoints`, `extractCrons` | `corpus/extract/**` (every file) | symbols and references as sets of all fields; endpoints/crons as sets; `contentHash` equal |
| `graphs.json` | `DepCruiseGraph.buildEdges(root, walkClone(root).files)` with `process.cwd()` = fixture root | `corpus/graphs/*` (one fixture root each) | edge sets of `[from, to]` |
| `rank.json` | `computeFileRank` | `corpus/rank/cases.json` + 3 seeded random graphs | pagerank within 1e-12, percentile exact |
| `repomap.json` | `renderRepoMap` with the js-tiktoken counter | `corpus/map/cases.json` + 1 seeded random case | text byte-identical, tokens equal |
| `tokens.json` | js-tiktoken `cl100k_base` `encode(text, [], [])` | `corpus/tokens/cases.json` | token count equal |
| `walk.json` | `walkClone` | `corpus/walk/cases.json` (trees materialised by the test) | files in order, stats equal |
| `graph_deviations_node.json` | `DepCruiseGraph` on the deviation fixtures | `corpus/graph-deviations/*` | **informational only** |

`rank.json`, `repomap.json`, `tokens.json` carry their inputs, so a test needs only the golden.
`extract.json` and `graphs.json` refer to files under `corpus/` (`path` is relative to
`corpus/extract/`, `fixture` to the testdata directory; graph `files` and edges are relative to the fixture root).

## Hand-written expectations (not generated)

`../graph-deviations.json` holds the expected edges of the D3019 deviation fixtures
(`corpus/graph-deviations/<case>/repo`). `graph_deviations_node.json` shows what Node does there, which
documents each deviation. `symlinks/repo` contains real symbolic links (`link.ts`, `linkdir`).
`syntax_error_imports` is marked `recovery_sensitive`: it relies on tree-sitter error recovery
(ast-grep runs 0.26, the Go binding 0.25). `corpus/extract/syntax_errors/**` uses error shapes that
recover (a broken statement inside a body; AC3044 is `second_function_broken.ts`).
`corpus/extract/syntax_errors_unstable/**` holds broken parameter lists (`second( {`): which following
declarations survive depends on the parser version and differs between Node and Go, so the S19 test
must skip that directory (or compare it with a hand-written expectation); the goldens only record Node.
Do not edit generated goldens.

## Corpus notes

- `corpus/extract/encoding/*` contain invalid UTF-8 and NUL bytes on purpose (git treats them as binary).
- The walk trees are declared in JSON, not committed, because `node_modules/`, `coverage/` and `.next/`
  are git-ignored here; the test creates them under `t.TempDir()` (entries with `link` are symlinks,
  `size` is the exact byte length, `bulk` is `count` files `dir/fNNNNN<ext>`).
- Node sorts walk results by UTF-16 code units; the walk corpus avoids names where that differs from
  byte order (an astral character next to a character in U+E000..U+FFFF).
- Signatures cut in the middle of a surrogate pair are not in the corpus: Node leaves a lone surrogate
  there, which JSON and Postgres cannot carry (Go drops the whole character).
