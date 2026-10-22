/**
 * blast.json — BlastRadius mapping, prior-PR selection, the BlastService (logs, history failures) and the
 * repo-intel `getBlastRadius` use case over fake index tables and clone fixtures (AC5038, AC5039, AC5042,
 * AC5007, AC5008, EC5014–EC5018).
 */
import { cpSync, mkdirSync, mkdtempSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { byteCompare } from './lib.mts';
import { corpusJson, expandText, recLogger, showError, showLogs, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

function mapping(ctx: Ctx): Obj[] {
  const spec = expandText(corpusJson(ctx, 'blast/mapping.json'));
  return (spec.cases as Obj[]).map((c) => ({ name: c.name, result: c.result, blast: ctx.N.blastDomain.buildBlastRadius(c.result) }));
}

function history(ctx: Ctx): Obj[] {
  const spec = expandText(corpusJson(ctx, 'blast/history.json'));
  return (spec.cases as Obj[]).map((c) => ({
    name: c.name,
    current: c.current,
    candidates: c.candidates,
    max: c.max ?? 10,
    result: ctx.N.blastHistory.selectPriorPrs(c.candidates, c.current, c.max ?? 10),
  }));
}

async function service(ctx: Ctx): Promise<{ blast: Obj[]; history: Obj[] }> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'blast/service.json'));
  const ws = spec.workspace as string;
  const blast: Obj[] = [];
  for (const c of spec.blast as Obj[]) {
    const log = recLogger();
    const calls: Obj = { index: [] as Obj[] };
    const svc = new N.blastService.BlastService({
      source: { findPull: async () => (c.pull === null ? null : { ...c.pull }) },
      index: {
        getBlastRadius: async (repoId: string, files: string[]) => {
          calls.index.push({ repoId, files });
          if (c.index.throws) throw new Error(c.index.throws);
          return c.index;
        },
      },
      history: { listMergedPullRequests: async () => [] },
    });
    let outcome: Obj;
    try {
      outcome = { body: await svc.getBlast(ws, 'pr-1', log) };
    } catch (e) {
      outcome = { error: showError(e) };
    }
    blast.push({ name: c.name, pull: c.pull, index: c.index, ...outcome, calls, logs: showLogs(log.lines) });
  }
  const hist: Obj[] = [];
  for (const c of spec.history as Obj[]) {
    const log = recLogger();
    const calls: Obj = { github: [] as Obj[] };
    const svc = new N.blastService.BlastService({
      source: { findPull: async () => (c.pull === null ? null : { ...c.pull }) },
      index: { getBlastRadius: async () => ({ changedSymbols: [], callers: [] }) },
      history: {
        listMergedPullRequests: async (repo: Obj, opts: Obj) => {
          calls.github.push({ repo, opts });
          if (c.merged.throws) throw new Error(c.merged.throws);
          return c.merged;
        },
      },
    });
    let outcome: Obj;
    try {
      outcome = { body: await svc.getHistory(ws, 'pr-1', log) };
    } catch (e) {
      outcome = { error: showError(e) };
    }
    hist.push({ name: c.name, pull: c.pull, merged: c.merged, ...outcome, calls, logs: showLogs(log.lines) });
  }
  return { blast, history: hist };
}

// ---------------------------------------------------------------------------
// getBlastRadius
// ---------------------------------------------------------------------------

type Row = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

/** The Node port result as the Go port returns it: no `impactedEndpoints` (never reaches the response). */
function portOf(result: Obj): Obj {
  const { impactedEndpoints: _dropped, ...rest } = result;
  return rest;
}

function persistentDeps(ctx: Ctx, repo: Obj, c: Obj, cloneRoot: string | null): Obj {
  const { N } = ctx;
  const symbols = (c.symbols as Row[]) ?? [];
  const references = [...((c.references as Row[]) ?? []), ...((c.extraReferences as Row[]) ?? [])];
  const fileRank = [...((c.file_rank as Row[]) ?? []), ...((c.extraFileRank as Row[]) ?? [])];
  const facts = (c.file_facts as Row[]) ?? [];
  const rankOf = new Map<string, number>(fileRank.map((r) => [r.path as string, r.rank as number]));
  const state = c.index_state as Row | null | 'error' | undefined;
  return {
    enabled: c.enabled !== false,
    reader: {
      getRepoBasics: async () => (c.noRepo ? null : { id: repo.id, owner: repo.owner, name: repo.name, defaultBranch: 'main', clonePath: c.noClone ? null : cloneRoot }),
      tryGetIndexState: async () => {
        if (state === undefined || state === null || state === 'error') return null;
        return N.repoIntelRead.toIndexState({
          repoId: repo.id,
          status: state.status,
          filesIndexed: state.files_indexed,
          filesSkipped: state.files_skipped,
          lastIndexedSha: state.last_indexed_sha,
          indexerVersion: state.indexer_version,
          updatedAt: new Date('2026-01-01T00:00:00Z'),
          stats: state.stats,
        });
      },
      // SELECT ... FROM symbols WHERE path IN (...) — the Go side orders by path, line, name (byte order, D5009).
      getSymbolRows: async (_repoId: string, paths: string[]) =>
        symbols
          .filter((s) => paths.includes(s.path))
          .map((s) => ({ path: s.path, name: s.name, kind: s.kind, line: s.line, endLine: s.end_line, exported: s.exported ?? false, signature: s.signature ?? null }))
          .sort((a, b) => byteCompare(a.path, b.path) || (a.line ?? 0) - (b.line ?? 0) || byteCompare(a.name, b.name)),
      // references JOIN file_rank ON from_path ... ORDER BY rank DESC, from_path, line, to_symbol (D5009).
      getResolvedCallers: async (_repoId: string, declFiles: string[], names: string[]) =>
        references
          .filter((r) => r.decl_file !== null && declFiles.includes(r.decl_file) && names.includes(r.to_symbol) && rankOf.has(r.from_path))
          .map((r) => ({ fromPath: r.from_path, toSymbol: r.to_symbol, line: r.line, rank: rankOf.get(r.from_path)! }))
          .sort((a, b) => b.rank - a.rank || byteCompare(a.fromPath, b.fromPath) || a.line - b.line || byteCompare(a.toSymbol, b.toSymbol)),
      getFileFacts: async (_repoId: string, files: string[]) =>
        facts.filter((f) => files.includes(f.path)).map((f) => ({ filePath: f.path, endpoints: f.endpoints, crons: f.crons })),
    },
    analyzer: { endpoints: () => [] as string[] },
    files: { read: async () => null },
  };
}

/** Node's ripgrep CodeIndex, with the walk in full-path byte order (what the Go scan does) instead of readdir order. */
function sortedCodeIndex(ctx: Ctx, root: string): Obj {
  const real = new ctx.N.ripgrep.RipgrepCodeIndex({ clonePathFor: () => root });
  return {
    symbols: async (ref: Obj) => {
      const all: Obj[] = await real.symbols(ref);
      return all.map((s, i) => ({ s, i })).sort((a, b) => byteCompare(a.s.path, b.s.path) || a.i - b.i).map((x) => x.s);
    },
    references: async (ref: Obj, symbol: string) => {
      const refs: Obj[] = await real.references(ref, symbol);
      return refs.sort((a, b) => byteCompare(a.fromPath, b.fromPath) || a.line - b.line);
    },
  };
}

function materialize(ctx: Ctx, scratch: string, name: string, fixture: string, extra: Obj[]): string {
  const root = join(scratch, name, 'repo');
  mkdirSync(root, { recursive: true });
  cpSync(join(ctx.corpus, 'blast', 'clones', fixture), root, { recursive: true });
  for (const f of extra) {
    const full = join(root, f.path);
    mkdirSync(dirname(full), { recursive: true });
    if (f.link !== undefined) symlinkSync(f.link, full);
    else if (f.padTo !== undefined) {
      const head = f.content as string;
      writeFileSync(full, head + '//' + 'p'.repeat(f.padTo - Buffer.byteLength(head) - 2));
    } else writeFileSync(full, f.content);
  }
  return root;
}

async function radius(ctx: Ctx): Promise<{ persistent: Obj[]; clone: Obj[] }> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'blast/radius.json'));
  const repo = spec.repo as Obj;
  const persistent: Obj[] = [];
  for (const c of spec.persistent as Obj[]) {
    const deps = persistentDeps(ctx, repo, c, null);
    deps.codeIndex = { symbols: async () => [], references: async () => [] };
    const result = await N.repoIntelBlast.getBlastRadius(deps, repo.id, c.changedFiles);
    const port = portOf(result);
    persistent.push({
      name: c.name,
      repo,
      enabled: c.enabled !== false,
      changedFiles: c.changedFiles,
      index_state: c.index_state,
      tables: {
        symbols: c.symbols,
        references: [...((c.references as Obj[]) ?? []), ...((c.extraReferences as Obj[]) ?? [])],
        file_rank: [...((c.file_rank as Obj[]) ?? []), ...((c.extraFileRank as Obj[]) ?? [])],
        file_facts: c.file_facts,
      },
      port,
      blast: N.blastDomain.buildBlastRadius(port),
    });
  }

  const scratch = mkdtempSync(join(process.env.TMPDIR!, 'p5-blast-'));
  const clone: Obj[] = [];
  try {
    for (const c of spec.clone as Obj[]) {
      const root = materialize(ctx, scratch, c.name, 'shop', (c.extraFiles as Obj[]) ?? []);
      const deps = persistentDeps(ctx, repo, { enabled: c.enabled, index_state: c.index_state, noClone: c.noClone, noRepo: c.noRepo }, root);
      deps.codeIndex = sortedCodeIndex(ctx, root);
      const result = await N.repoIntelBlast.getBlastRadius(deps, repo.id, c.changedFiles);
      const port = portOf(result);
      const sizes: Obj = {};
      for (const f of (c.extraFiles as Obj[]) ?? []) if (f.padTo !== undefined) sizes[f.path] = statSync(join(root, f.path)).size;
      clone.push({
        name: c.name,
        repo,
        fixture: 'shop',
        enabled: c.enabled !== false,
        noClone: c.noClone === true,
        noRepo: c.noRepo === true,
        index_state: c.index_state ?? null,
        changedFiles: c.changedFiles,
        extraFiles: c.extraFiles ?? [],
        extraFileSizes: sizes,
        port,
        blast: N.blastDomain.buildBlastRadius(port),
      });
    }
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
  return { persistent, clone };
}

export async function genBlast(ctx: Ctx): Promise<void> {
  const svc = await service(ctx);
  const rad = await radius(ctx);
  ctx.writer.write('blast.json', {
    note: 'Node blast: mapping = buildBlastRadius(port result); history = selectPriorPrs; service = BlastService.getBlast / getHistory with recorded log lines (Error values shown by message); persistent = repo-intel getBlastRadius over fake index tables (the reader SQL is emulated: see corpus/blast/radius.json); clone = the clone-scan path over corpus/blast/clones/shop plus `extraFiles` written into a temp copy (a padTo file is `content` followed by `//` and p-padding up to that many bytes; `link` is a symbolic link to that target). `port` is the Node BlastResult without impactedEndpoints (never part of the response); the clone-scan walk is taken in full-path byte order (the Go scan order) instead of readdir order; Node symbols of the clone scan keep qualified Class.method names, the persistent path skips them.',
    mapping: mapping(ctx),
    history: history(ctx),
    service: svc,
    persistent: rad.persistent,
    clone: rad.clone,
  });
}
