/** Corpus loading and the shorthand expanders shared by the generator parts. */
import { join } from 'node:path';
import { readJson, textOf, synthPatch, byteCompare } from './lib.mts';
import type { SynthHunk, TextSpec } from './lib.mts';
import type { Ctx, Mod } from './ctx.mts';

export type Obj = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

export function corpusJson<T = Obj>(ctx: Ctx, rel: string): T {
  return readJson<T>(join(ctx.corpus, rel));
}

/** Expands {"$repeat": [text, n]} placeholders anywhere in a value. */
export function expandText<T>(v: T): T {
  if (Array.isArray(v)) return v.map(expandText) as unknown as T;
  if (v !== null && typeof v === 'object') {
    const o = v as Obj;
    if (Array.isArray(o.$repeat)) return (o.$repeat[0] as string).repeat(o.$repeat[1] as number) as unknown as T;
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, expandText(x)])) as T;
  }
  return v;
}

// ---- findings and reviews -------------------------------------------------

let findingCounter = 0;
export function resetFindingCounter(): void {
  findingCounter = 0;
}

/** Finding shorthand → the Finding JSON a model returns (schema key order). */
export function expandFinding(f: Obj): Obj {
  findingCounter++;
  const out: Obj = {
    id: f.id ?? `f${findingCounter}`,
    severity: f.sev ?? 'WARNING',
    category: f.cat ?? 'bug',
    title: f.t ?? 'Finding',
    file: f.f,
    start_line: f.s,
    end_line: f.e,
    rationale: f.why ?? 'Because.',
  };
  if ('fix' in f) out.suggestion = f.fix;
  out.confidence = f.c ?? 0.8;
  if ('k' in f) out.kind = f.k;
  if ('tri' in f) out.trifecta_components = f.tri;
  if ('evidence' in f) out.evidence = f.evidence;
  if ('skill' in f) out.skill = f.skill;
  if ('oos' in f) out.out_of_scope = f.oos;
  return out;
}

export function expandReview(r: Obj): Obj {
  return { verdict: r.verdict, summary: r.summary, score: r.score, findings: (r.findings ?? []).map(expandFinding) };
}

// ---- pull requests --------------------------------------------------------

export interface PrFile {
  path: string;
  patch: string | null;
  patchLines?: string[] | null;
  synth?: SynthHunk[];
}
export interface Pr {
  pull: Obj;
  repo: Obj;
  files: PrFile[];
}

export function loadPr(ctx: Ctx, name: string): Pr {
  const raw = corpusJson(ctx, `prs/${name}.json`);
  return { pull: raw.pull, repo: raw.repo, files: rowsOf(raw.files) };
}

/** Rows as pr_files: {path, patch}; sorted by path in byte order (what the Go store returns). */
export function rowsOf(files: Obj[]): PrFile[] {
  const rows = files.map((f) => {
    let patch: string | null;
    if (f.synth) patch = synthPatch(f.synth);
    else if (f.patchLines === null || f.patchLines === undefined) patch = null;
    else patch = (f.patchLines as string[]).join('\n');
    return { path: f.path as string, patch };
  });
  return rows.sort((a, b) => byteCompare(a.path, b.path));
}

// ---- Node helpers ---------------------------------------------------------

export function parseDiff(ctx: Ctx, raw: string): Obj {
  return ctx.N.diffParser.parseUnifiedDiff(raw);
}

/** Drizzle handle whose select chain resolves to `rows` (enough for the stored-diff query). */
export function selectDb(rows: unknown[]): Mod {
  const chain: Obj = {};
  for (const m of ['from', 'where', 'orderBy', 'innerJoin', 'leftJoin', 'limit', 'groupBy']) chain[m] = () => chain;
  chain.then = (res: (v: unknown) => unknown, rej: (e: unknown) => unknown) => Promise.resolve(rows).then(res, rej);
  return { select: () => chain };
}

/** The raw diff of a corpus entry: {pr}, or lines/text/synth. */
export async function rawDiffOf(ctx: Ctx, spec: Obj): Promise<string> {
  if (spec.pr) {
    const pr = loadPr(ctx, spec.pr as string);
    const diff = await ctx.N.pullRepo.storedDiff(selectDb(pr.files), 'pr');
    return diff.raw as string;
  }
  return textOf(spec as TextSpec);
}

/** Synthetic diff with the given per-file addition/deletion counts (mode cases). */
export function modeDiffSpec(files: Array<{ path: string; add?: number; del?: number }>): TextSpec {
  return {
    synth: {
      files: files.map((f) => ({ path: f.path, hunks: [{ start: 1, add: f.add ?? 0, del: f.del ?? 0, text: 'x' }] })),
    },
  };
}

// ---- recording drizzle handle ----------------------------------------------

/**
 * A drizzle-shaped handle that records the statements built on it instead of running them:
 * insert/update/delete/select chains are thenables; `result(op)` supplies what awaiting returns.
 */
export function recDb(ctx: Ctx, onOp: (op: Obj) => void, result: (op: Obj) => unknown): Mod {
  const make = (kind: string, table?: unknown) => {
    const op: Obj = { kind };
    if (table !== undefined) op.table = ctx.drizzle.getTableName(table);
    const chain: Mod = new Proxy(
      {},
      {
        get(_t, prop) {
          if (prop === 'then') {
            return (res: (v: unknown) => unknown, rej: (e: unknown) => unknown) => {
              onOp(op);
              return Promise.resolve().then(() => result(op)).then(res, rej);
            };
          }
          return (...args: unknown[]) => {
            if (prop === 'values') op.values = args[0];
            else if (prop === 'set') op.set = args[0];
            else if (prop === 'from') op.table = ctx.drizzle.getTableName(args[0]);
            else if (prop === 'onConflictDoUpdate') op.onConflict = 'update';
            else if (prop === 'onConflictDoNothing') op.onConflict = 'nothing';
            else if (prop === 'returning') op.returning = true;
            return chain;
          };
        },
      },
    );
    return chain;
  };
  return {
    insert: (t: unknown) => make('insert', t),
    update: (t: unknown) => make('update', t),
    delete: (t: unknown) => make('delete', t),
    select: () => make('select'),
  };
}

/** A FindingRecord (wire shape) built through the real row mapper from finding shorthand. */
export function findingRecord(ctx: Ctx, f: Obj, reviewId: string, memoryId: string | null): Obj {
  const e = expandFinding(f);
  const row = {
    id: e.id,
    reviewId,
    severity: e.severity,
    category: e.category,
    title: e.title,
    file: e.file,
    startLine: e.start_line,
    endLine: e.end_line,
    rationale: e.rationale,
    suggestion: e.suggestion ?? null,
    confidence: e.confidence,
    kind: e.kind ?? null,
    trifectaComponents: e.trifecta_components ?? null,
    skillName: e.skill ?? null,
    outOfScope: e.out_of_scope ?? false,
    acceptedAt: f.accepted ? new Date(f.accepted) : null,
    dismissedAt: f.dismissed ? new Date(f.dismissed) : null,
  };
  return { ...ctx.N.mappers.toFindingRecord(row, f.evalCase ?? null), ...(memoryId !== undefined && memoryId !== null ? { memory_id: memoryId } : {}) };
}
