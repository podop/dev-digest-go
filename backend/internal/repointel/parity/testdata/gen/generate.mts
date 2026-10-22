/**
 * Golden generator for the repo-intel parity corpus (Phase 3, step S11).
 *
 * Runs the NODE implementation of dev-digest (read-only checkout, commit d1c9093) over the
 * committed corpus and writes the results as JSON goldens. See ./README.md for the command.
 *
 * Hard rules, enforced below:
 *  - the dev-digest checkout is never written to: HEAD must be d1c9093 and `git status`
 *    clean before AND after the run; only read-only git commands are used;
 *  - output goes only into --out, which must lie inside the testdata directory;
 *  - tsx cache and scratch trees live under $TMPDIR (required, must not be inside the checkout);
 *  - output is deterministic (no clocks, no absolute paths, no randomness except seeded PRNGs),
 *    so a second run reproduces the first byte for byte.
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { tmpdir } from 'node:os';

const EXPECTED_DD_HEAD_PREFIX = 'd1c9093';

// ---------------------------------------------------------------------------
// CLI and guards
// ---------------------------------------------------------------------------

function arg(name: string): string {
  const i = process.argv.indexOf(name);
  const v = i >= 0 ? process.argv[i + 1] : undefined;
  if (!v) throw new Error(`missing ${name} <value>`);
  return v;
}

const startCwd = process.cwd();
const dd = resolve(startCwd, arg('--dd'));
const outDir = resolve(startCwd, arg('--out'));
const testdata = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const corpus = join(testdata, 'corpus');

function inside(parent: string, child: string): boolean {
  const rel = relative(parent, child);
  return rel === '' || (!rel.startsWith('..') && !isAbsolute(rel));
}

if (!inside(testdata, outDir) || outDir === testdata) {
  throw new Error(`--out must be a directory inside ${testdata}, got ${outDir}`);
}
if (inside(dd, outDir) || inside(dd, testdata)) {
  throw new Error('refusing to write inside the dev-digest checkout');
}
const scratchBase = process.env.TMPDIR;
if (!scratchBase) throw new Error('TMPDIR must be set to a scratch directory (tsx writes its cache there)');
if (inside(dd, resolve(scratchBase)) || inside(dd, tmpdir())) {
  throw new Error('TMPDIR must not be inside the dev-digest checkout');
}

function git(...args: string[]): string {
  // --no-optional-locks: `git status` must not refresh (write) the checkout's index.
  return execFileSync('git', ['--no-optional-locks', '-C', dd, ...args], { encoding: 'utf8' });
}

function assertCheckoutPristine(when: string): string {
  const head = git('rev-parse', 'HEAD').trim();
  if (!head.startsWith(EXPECTED_DD_HEAD_PREFIX)) {
    throw new Error(`${when}: dev-digest HEAD is ${head}, expected ${EXPECTED_DD_HEAD_PREFIX}…`);
  }
  const status = git('status', '--porcelain', '--untracked-files=all');
  if (status.trim() !== '') throw new Error(`${when}: dev-digest checkout is not clean:\n${status}`);
  return head;
}

const ddHead = assertCheckoutPristine('before');

// ---------------------------------------------------------------------------
// Node implementation under test (imported by file URL from the checkout)
// ---------------------------------------------------------------------------

const N = join(dd, 'server', 'src');
async function load<T>(rel: string): Promise<T> {
  return (await import(pathToFileURL(join(N, rel)).href)) as T;
}

interface Walk {
  walkClone(root: string): Promise<{ files: string[]; stats: { totalCandidates: number; skippedTooLarge: number; bounded: number } }>;
}
interface SourceAdapters {
  astGrepSourceAnalyzer: {
    endpoints(source: string): string[];
    crons(source: string): string[];
  };
  cloneFileSystem: { read(root: string, rel: string): Promise<string> };
}
interface ParseSources {
  parseSourceFile(
    deps: { analyzer: unknown; files: unknown },
    repoId: string,
    root: string,
    relPath: string,
    buf: {
      symbols: Array<{ name: string; kind: string; line: number; endLine: number; exported: boolean; signature: string | null; contentHash: string }>;
      references: Array<{ toSymbol: string; line: number }>;
      facts: unknown[];
    },
  ): Promise<{ kind: string; reason?: string }>;
  emptyBuffers(): {
    symbols: Array<{ name: string; kind: string; line: number; endLine: number; exported: boolean; signature: string | null; contentHash: string }>;
    references: Array<{ toSymbol: string; line: number }>;
    facts: unknown[];
  };
}
interface DepGraphMod {
  DepCruiseGraph: new () => { buildEdges(root: string, files: string[]): Promise<Array<{ from: string; to: string }>> };
}
interface RankMod {
  computeFileRank(
    files: string[],
    edges: Array<{ fromFile: string; toFile: string }>,
  ): Array<{ filePath: string; pagerank: number; percentile: number }>;
}
interface MapMod {
  renderRepoMap(
    candidates: Array<{ path: string; name: string; exported: boolean; signature: string | null; rank: number }>,
    tokenizer: { count(text: string): number },
    budget: number,
  ): { text: string; tokens: number };
}

const { walkClone } = await load<Walk>('modules/repo-intel/infrastructure/walk.ts');
const { astGrepSourceAnalyzer, cloneFileSystem } = await load<SourceAdapters>('modules/repo-intel/infrastructure/source-adapters.ts');
const { parseSourceFile, emptyBuffers } = await load<ParseSources>('modules/repo-intel/application/parse-sources.ts');
const { DepCruiseGraph } = await load<DepGraphMod>('adapters/depgraph/index.ts');
const { computeFileRank } = await load<RankMod>('modules/repo-intel/domain/rank.ts');
const { renderRepoMap } = await load<MapMod>('modules/repo-intel/domain/repo-map.ts');

// js-tiktoken is resolved from the checkout's server package, like the Node adapter does.
const ddRequire = createRequire(join(dd, 'server', 'package.json'));
const { getEncoding } = ddRequire('js-tiktoken') as { getEncoding(name: string): { encode(t: string, allowed?: string[], disallowed?: string[]): number[] } };
const encoder = getEncoding('cl100k_base');
/** Special-token literals count as ordinary text (spec D3007): allowed = [], disallowed = []. */
const countTokens = (text: string): number => encoder.encode(text, [], []).length;

function toolVersion(pkg: string): string {
  return (JSON.parse(readFileSync(join(dd, 'server', 'node_modules', pkg, 'package.json'), 'utf8')) as { version: string }).version;
}

// ---------------------------------------------------------------------------
// Deterministic JSON writer
// ---------------------------------------------------------------------------

type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

// Characters that are invisible or line-breaking in editors are written as \uXXXX escapes.
const ESCAPE_RE = /[\u007f-\u00a0\u2000-\u200f\u2028-\u202f\u205f-\u206f\u3000\ufeff\ufffd]/g;
function jstr(s: string): string {
  return JSON.stringify(s).replace(ESCAPE_RE, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
}

function isScalar(v: Json): boolean {
  return v === null || typeof v !== 'object';
}
function isFlat(v: Json): boolean {
  if (Array.isArray(v)) return v.every(isScalar);
  if (v !== null && typeof v === 'object') return Object.values(v).every(isScalar);
  return true;
}

function inline(v: Json): string {
  if (v === null) return 'null';
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) throw new Error(`non-finite number in golden: ${v}`);
    return JSON.stringify(v);
  }
  if (typeof v === 'string') return jstr(v);
  if (typeof v === 'boolean') return String(v);
  if (Array.isArray(v)) return '[' + v.map(inline).join(', ') + ']';
  return '{' + Object.entries(v).map(([k, x]) => `${jstr(k)}: ${inline(x)}`).join(', ') + '}';
}

function ser(v: Json, ind = ''): string {
  if (isScalar(v)) return inline(v);
  if (isFlat(v)) {
    const s = inline(v);
    if (s.length <= 150) return s;
  }
  const next = ind + '  ';
  if (Array.isArray(v)) {
    if (v.length === 0) return '[]';
    return '[\n' + v.map((x) => next + ser(x, next)).join(',\n') + '\n' + ind + ']';
  }
  const entries = Object.entries(v as { [k: string]: Json });
  if (entries.length === 0) return '{}';
  return '{\n' + entries.map(([k, x]) => `${next}${jstr(k)}: ${ser(x, next)}`).join(',\n') + '\n' + ind + '}';
}

function writeGolden(name: string, value: Json): void {
  mkdirSync(outDir, { recursive: true });
  writeFileSync(join(outDir, name), ser(value) + '\n');
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function readJson<T>(rel: string): T {
  return JSON.parse(readFileSync(join(testdata, rel), 'utf8')) as T;
}

function listFiles(dir: string): string[] {
  const out: string[] = [];
  const rec = (d: string): void => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) rec(p);
      else if (e.isFile()) out.push(relative(dir, p).split(sep).join('/'));
    }
  };
  rec(dir);
  return out.sort();
}

function listDirs(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true })
    .filter((e) => e.isDirectory())
    .map((e) => e.name)
    .sort();
}

/** mulberry32 — small seeded PRNG so random corpora are reproducible. */
function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const scratch = mkdtempSync(join(scratchBase, 'repointel-parity-'));

// ---------------------------------------------------------------------------
// 1. Extraction: symbols, references, facts, content hash (AC3042-3045, AC3047)
// ---------------------------------------------------------------------------

async function genExtract(): Promise<void> {
  const root = join(corpus, 'extract');
  const records: Json[] = [];
  for (const rel of listFiles(root)) {
    const buf = emptyBuffers();
    const outcome = await parseSourceFile(
      { analyzer: astGrepSourceAnalyzer, files: cloneFileSystem },
      'parity-repo',
      root,
      rel,
      buf,
    );
    const source = await cloneFileSystem.read(root, rel);
    const rec: { [k: string]: Json } = {
      path: rel,
      outcome: outcome.kind,
      contentHash: createHash('sha1').update(source).digest('hex'),
      symbols: buf.symbols.map((s) => ({
        name: s.name,
        kind: s.kind,
        line: s.line,
        endLine: s.endLine,
        exported: s.exported,
        signature: s.signature,
      })),
      references: buf.references.map((r) => ({ toSymbol: r.toSymbol, line: r.line })),
      endpoints: astGrepSourceAnalyzer.endpoints(source),
      crons: astGrepSourceAnalyzer.crons(source),
    };
    if (outcome.reason) rec.reason = outcome.reason;
    if (outcome.kind !== 'indexed') throw new Error(`extract fixture ${rel} did not index: ${outcome.kind} ${outcome.reason ?? ''}`);
    records.push(rec);
  }
  writeGolden('extract.json', {
    note: 'Node parseSymbols/parseReferences/extractEndpoints/extractCrons through parseSourceFile; compare symbols and references as sets, endpoints and crons as sets. contentHash = sha1 of the utf8-decoded text (invalid bytes -> U+FFFD, BOM kept).',
    files: records,
  });
}

// ---------------------------------------------------------------------------
// 2. Import graph (AC3049)
// ---------------------------------------------------------------------------

async function runGraph(root: string, files: string[], cwd: string): Promise<Array<{ from: string; to: string }>> {
  const before = process.cwd();
  process.chdir(cwd);
  try {
    return await new DepCruiseGraph().buildEdges(root, files);
  } finally {
    process.chdir(before);
  }
}

async function genGraphs(): Promise<void> {
  const base = join(corpus, 'graphs');
  const fixtures: Json[] = [];
  for (const name of listDirs(base)) {
    const root = join(base, name);
    const { files } = await walkClone(root);
    // Node's working directory = fixture root: the only place its alias resolution works.
    const edges = await runGraph(root, files, root);
    if (edges.length === 0) throw new Error(`graph fixture ${name}: Node returned no edges (silent failure?)`);
    fixtures.push({
      fixture: `corpus/graphs/${name}`,
      cwd: 'fixture root',
      files,
      edges: edges.map((e) => [e.from, e.to]),
    });
  }
  writeGolden('graphs.json', {
    note: 'DepCruiseGraph.buildEdges(root, files) with files = walkClone(root).files and process.cwd() = fixture root. Compare edges as sets of [from, to].',
    graphs: fixtures,
  });

  // Deviation fixtures (D3019): expectations are hand-written in ../graph-deviations.json.
  // Here we only record what Node does, to document the deviation and check the fixture design.
  interface Dev { name: string; root: string; files: string[]; files_from_walk: boolean; expected_edges: Array<{ from: string; to: string }> }
  const dev = readJson<{ cases: Dev[] }>('graph-deviations.json');
  const serverCwd = join(scratch, 'server-cwd');
  mkdirSync(serverCwd, { recursive: true });
  const observed: Json[] = [];
  for (const c of dev.cases) {
    const root = join(testdata, c.root);
    if (c.files_from_walk) {
      const { files } = await walkClone(root);
      if (JSON.stringify(files) !== JSON.stringify(c.files)) {
        throw new Error(`graph-deviations.json ${c.name}: files differ from the walk: ${JSON.stringify(files)}`);
      }
    }
    const elsewhere = await runGraph(root, c.files, serverCwd);
    const atRoot = await runGraph(root, c.files, root);
    observed.push({
      name: c.name,
      nodeEdgesCwdElsewhere: elsewhere.map((e) => [e.from, e.to]),
      nodeEdgesCwdRoot: atRoot.map((e) => [e.from, e.to]),
    });
  }
  writeGolden('graph_deviations_node.json', {
    note: 'INFORMATIONAL: what Node produces for the deviation fixtures (cwd elsewhere = the server process; cwd root = the fixture root). Not an expectation for Go: those are hand-written in graph-deviations.json.',
    cases: observed,
  });
}

// ---------------------------------------------------------------------------
// 3. Rank (AC3051)
// ---------------------------------------------------------------------------

interface RankCase { name: string; files: string[]; edges: Array<[string, string]> }
interface RandomRank { name: string; nodes: number; edges: number; seed: number; skew: number; dangling_edges: number; dup_edges: number; self_loops: number }

function randomGraph(p: RandomRank): RankCase {
  const rnd = prng(p.seed);
  const files = Array.from({ length: p.nodes }, (_, i) => `src/m${String(i).padStart(4, '0')}.ts`);
  const edges: Array<[string, string]> = [];
  for (let i = 0; i < p.edges; i++) {
    const from = Math.floor(rnd() * p.nodes);
    const to = Math.floor(Math.pow(rnd(), p.skew) * p.nodes);
    edges.push([files[from]!, files[to]!]);
  }
  for (let i = 0; i < p.dup_edges && edges.length > 0; i++) edges.push(edges[Math.floor(rnd() * edges.length)]!);
  for (let i = 0; i < p.self_loops; i++) {
    const f = files[Math.floor(rnd() * p.nodes)]!;
    edges.push([f, f]);
  }
  for (let i = 0; i < p.dangling_edges; i++) {
    const f = files[Math.floor(rnd() * p.nodes)]!;
    edges.push(i % 2 === 0 ? [f, `src/ghost${i}.ts`] : [`src/ghost${i}.ts`, f]);
  }
  // Deterministic shuffle so edge order is not sorted by construction.
  for (let i = edges.length - 1; i > 0; i--) {
    const j = Math.floor(rnd() * (i + 1));
    [edges[i], edges[j]] = [edges[j]!, edges[i]!];
  }
  return { name: p.name, files, edges };
}

function genRank(): void {
  const spec = readJson<{ cases: RankCase[]; random: RandomRank[] }>('corpus/rank/cases.json');
  const all = [...spec.cases, ...spec.random.map(randomGraph)];
  const out: Json[] = all.map((c) => {
    const rows = computeFileRank(c.files, c.edges.map(([fromFile, toFile]) => ({ fromFile, toFile })));
    return {
      name: c.name,
      files: c.files,
      edges: c.edges,
      result: rows.map((r) => ({ filePath: r.filePath, pagerank: r.pagerank, percentile: r.percentile })),
    };
  });
  writeGolden('rank.json', {
    note: 'computeFileRank(files, edges): node order = files order; edges as listed (self-loops, duplicates and unknown endpoints are filtered by Node). Compare pagerank within 1e-12, percentile exactly.',
    cases: out,
  });
}

// ---------------------------------------------------------------------------
// 4. Repo map and tokenizer (AC3053, AC3054)
// ---------------------------------------------------------------------------

interface Cand { path: string; name: string; exported: boolean; signature: string | null; rank: number }
interface MapCase { name: string; budgets: number[]; candidates: Cand[] }
interface RandomMap { name: string; files: number; perFile: number; seed: number; budgets: number[] }

function randomMapCase(p: RandomMap): MapCase {
  const rnd = prng(p.seed);
  const verbs = ['load', 'save', 'render', 'parse', 'build', 'resolve', 'index', 'fetch', 'merge', 'split'];
  const nouns = ['User', 'Repo', 'Config', 'Digest', 'Review', 'Graph', 'Symbol', 'Chunk', 'Token', 'Queue'];
  const candidates: Cand[] = [];
  for (let f = 0; f < p.files; f++) {
    const path = `src/mod${String(f % 17).padStart(2, '0')}/file${String(f).padStart(3, '0')}.ts`;
    const rank = 1 / (f + 2);
    for (let s = 0; s < p.perFile; s++) {
      const name = `${verbs[Math.floor(rnd() * verbs.length)]}${nouns[Math.floor(rnd() * nouns.length)]}${f}x${s}`;
      const exported = s % 3 !== 2;
      const sig = s % 4 === 3
        ? `${exported ? 'export ' : ''}class ${name} extends Base<${nouns[s % nouns.length]}>`
        : `${exported ? 'export ' : ''}function ${name}(input: ${nouns[s % nouns.length]}, opts?: { depth: number }): Promise<void>`;
      candidates.push({ path, name, exported, signature: s === 7 ? null : sig, rank });
    }
  }
  return { name: p.name, budgets: p.budgets, candidates };
}

function genMap(): void {
  const spec = readJson<{ cases: MapCase[]; random: RandomMap[] }>('corpus/map/cases.json');
  const all = [...spec.cases, ...spec.random.map(randomMapCase)];
  const out: Json[] = all.map((c) => ({
    name: c.name,
    candidates: c.candidates as unknown as Json,
    results: c.budgets.map((budget) => {
      const r = renderRepoMap(c.candidates, { count: countTokens }, budget);
      return { budget, text: r.text, tokens: r.tokens };
    }),
  }));
  writeGolden('repomap.json', {
    note: 'renderRepoMap(candidates, js-tiktoken cl100k_base counter with special tokens as ordinary text, budget). Candidates are already in repo-map order. Text must be byte-identical, tokens equal.',
    cases: out,
  });
}

function genTokens(): void {
  const spec = readJson<{ cases: Array<{ name: string; text: string }>; repeat: Array<{ name: string; unit: string; bytes: number }> }>('corpus/tokens/cases.json');
  const cases = [...spec.cases];
  for (const r of spec.repeat) {
    let text = '';
    while (Buffer.byteLength(text) < r.bytes) text += r.unit;
    cases.push({ name: r.name, text });
  }
  writeGolden('tokens.json', {
    note: 'countTokens(text) = js-tiktoken cl100k_base encode(text, [], []).length. Special-token literals such as <|endoftext|> are ordinary text.',
    cases: cases.map((c) => ({ name: c.name, text: c.text, tokens: countTokens(c.text) })),
  });
}

// ---------------------------------------------------------------------------
// 5. Walk (NFR3008 "the walk")
// ---------------------------------------------------------------------------

interface WalkEntry { path: string; size?: number; link?: string }
interface WalkCase { name: string; entries: WalkEntry[]; bulk?: { dir: string; count: number; ext: string } }

async function genWalk(): Promise<void> {
  const spec = readJson<{ cases: WalkCase[] }>('corpus/walk/cases.json');
  const out: Json[] = [];
  for (const c of spec.cases) {
    const root = join(scratch, 'walk', c.name);
    mkdirSync(root, { recursive: true });
    for (const e of c.entries) {
      const full = join(root, e.path);
      mkdirSync(dirname(full), { recursive: true });
      if (e.link !== undefined) symlinkSync(e.link, full);
      else writeFileSync(full, e.size !== undefined ? 'x'.repeat(e.size) : '// fixture\n');
    }
    if (c.bulk) {
      mkdirSync(join(root, c.bulk.dir), { recursive: true });
      for (let i = 0; i < c.bulk.count; i++) {
        writeFileSync(join(root, c.bulk.dir, `f${String(i).padStart(5, '0')}${c.bulk.ext}`), '// fixture\n');
      }
    }
    const r = await walkClone(root);
    out.push({
      name: c.name,
      files: r.files,
      stats: { totalCandidates: r.stats.totalCandidates, skippedTooLarge: r.stats.skippedTooLarge, bounded: r.stats.bounded },
    });
  }
  writeGolden('walk.json', {
    note: 'walkClone(root) over the trees described in corpus/walk/cases.json (materialised by the test: entries with link are symlinks, size = exact byte length, bulk = count files dir/fNNNNN<ext>). Node sorts by UTF-16 code units; the corpus avoids names where that differs from byte order.',
    cases: out,
  });
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

try {
  await genExtract();
  await genGraphs();
  genRank();
  genMap();
  genTokens();
  await genWalk();
  writeGolden('meta.json', {
    note: 'Provenance of the goldens. Regenerate with the command in gen/README.md.',
    devDigestCommit: ddHead,
    tools: {
      '@ast-grep/napi': toolVersion('@ast-grep/napi'),
      'dependency-cruiser': toolVersion('dependency-cruiser'),
      typescript: toolVersion('typescript'),
      'enhanced-resolve': toolVersion('enhanced-resolve'),
      graphology: toolVersion('graphology'),
      'graphology-metrics': toolVersion('graphology-metrics'),
      'js-tiktoken': toolVersion('js-tiktoken'),
    },
  });
} finally {
  process.chdir(startCwd);
  rmSync(scratch, { recursive: true, force: true });
}

const ddHeadAfter = assertCheckoutPristine('after');
if (ddHeadAfter !== ddHead) throw new Error('dev-digest HEAD changed during the run');
console.log(`goldens written to ${relative(startCwd, outDir) || '.'} (dev-digest ${ddHead.slice(0, 7)}, checkout clean before and after)`);
