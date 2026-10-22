/** Corpus loading and small helpers shared by the generator parts. */
import { join } from 'node:path';
import { readJson } from './lib.mts';
import type { Ctx } from './ctx.mts';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Obj = Record<string, any>;

export function corpusJson<T = Obj>(ctx: Ctx, rel: string): T {
  return readJson<T>(join(ctx.corpus, rel));
}

/**
 * Expands corpus placeholders anywhere in a value (keys are left alone):
 *  - `{"$repeat": [text, n]}` → the text repeated n times; `{"$concat": [a, b, ...]}` → the expanded parts joined;
 *  - `{"$each": [n, template]}` → n instances of the template (`{i}`, `{i3}`, `{"$lin": [a, b]}` = a + b·i);
 *  - `{"$seed": path}` / `{"$mock": name}` are resolved by resolveRefs (Node seed patches and mock fixtures);
 *  - `{"$commits": [n, len]}` / `{"$files": n}` / `{"$items": n}` → synthetic commits, paths and list items;
 *  - `\u{HEX}` inside a string → that code point (a lone surrogate is allowed). The braces form is not a
 *    JSON escape, so a corpus file can carry invisible or unpaired characters as readable text.
 */
export function expandText<T>(v: T): T {
  if (typeof v === 'string') {
    return v.replace(/\\u\{([0-9A-Fa-f]{1,6})\}/g, (_m, h: string) => String.fromCodePoint(parseInt(h, 16))) as unknown as T;
  }
  if (Array.isArray(v)) return v.map(expandText) as unknown as T;
  if (v !== null && typeof v === 'object') {
    const o = v as Obj;
    if (Array.isArray(o.$concat)) return (o.$concat as unknown[]).map((x) => expandText(x) as string).join('') as unknown as T;
    if (Array.isArray(o.$each)) {
      const [n, template] = o.$each as [number, unknown];
      return Array.from({ length: n }, (_, k) => expandText(instance(template, k + 1))) as unknown as T;
    }
    if (Array.isArray(o.$commits)) {
      const [n, len] = o.$commits as [number, number];
      return Array.from({ length: n }, (_, i) => ({ sha: `c${i + 1}`, message: `m${i + 1} ${'x'.repeat(len)}` })) as unknown as T;
    }
    if (typeof o.$files === 'number') return Array.from({ length: o.$files as number }, (_, i) => `src/f${String(i).padStart(3, '0')}.ts`) as unknown as T;
    if (typeof o.$items === 'number') return Array.from({ length: o.$items as number }, (_, i) => `item ${i + 1}`) as unknown as T;
    if (Array.isArray(o.$repeat)) return expandText(o.$repeat[0] as string).repeat(o.$repeat[1] as number) as unknown as T;
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, expandText(x)])) as T;
  }
  return v;
}

/** One element of an `$each`: `{i}` / `{i3}` in strings become the 1-based index (zero padded), `{"$lin": [a, b]}` becomes a + b·i. */
function instance(t: unknown, i: number): unknown {
  if (typeof t === 'string') return t.replace(/\{i(\d*)\}/g, (_m, w: string) => String(i).padStart(w === '' ? 0 : Number(w), '0'));
  if (Array.isArray(t)) return t.map((x) => instance(x, i));
  if (t !== null && typeof t === 'object') {
    const o = t as Obj;
    if (Array.isArray(o.$lin)) return (o.$lin[0] as number) + (o.$lin[1] as number) * i;
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, instance(x, i)]));
  }
  return t;
}

/**
 * Replaces `{"$seed": "<path>"}` by the seeded PR #482 patch of that path (db/seed-diff.ts) and
 * `{"$mock": "<NAME>"}` by that export of the Node mock LLM (adapters/llm/mock.ts), after expandText.
 */
export function resolveRefs<T>(ctx: Ctx, v: T): T {
  if (Array.isArray(v)) return v.map((x) => resolveRefs(ctx, x)) as unknown as T;
  if (v !== null && typeof v === 'object') {
    const o = v as Obj;
    if (typeof o.$seed === 'string') {
      const patch = (ctx.N.seedDiff.SEED_PR_482_PATCHES as Record<string, string>)[o.$seed];
      if (patch === undefined) throw new Error(`unknown seeded patch ${o.$seed}`);
      return patch as unknown as T;
    }
    if (typeof o.$mock === 'string') return structuredClone(ctx.N.mockLlm[o.$mock]) as unknown as T;
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, resolveRefs(ctx, x)])) as T;
  }
  return v;
}

export interface LogLine {
  level: 'info' | 'warn';
  obj: unknown;
  msg: string | null;
}

/** A request-scoped logger (fastify's `req.log` shape) that records every call. */
export function recLogger(): { info(o: unknown, m?: string): void; warn(o: unknown, m?: string): void; lines: LogLine[] } {
  const lines: LogLine[] = [];
  return {
    lines,
    info: (obj, msg) => void lines.push({ level: 'info', obj, msg: msg ?? null }),
    warn: (obj, msg) => void lines.push({ level: 'warn', obj, msg: msg ?? null }),
  };
}

/** Errors become `{name, code?, kind?, message, details?}`; Error objects inside log lines too. */
export function showError(e: unknown): Obj {
  if (e instanceof Error) {
    const x = e as Error & { code?: string; kind?: string; details?: unknown };
    return {
      name: x.name,
      ...(x.kind !== undefined ? { kind: x.kind } : {}),
      ...(x.code !== undefined ? { code: x.code } : {}),
      message: x.message,
      ...(x.details !== undefined ? { details: x.details } : {}),
    };
  }
  return { name: 'non-error', message: String(e) };
}

/** Log lines with Error values replaced by their message (content-free fields only). */
export function showLogs(lines: readonly { level: string; obj: unknown; msg: string | null }[]): Obj[] {
  const walk = (v: unknown): unknown => {
    if (v instanceof Error) return v.message;
    if (Array.isArray(v)) return v.map(walk);
    if (v !== null && typeof v === 'object') return Object.fromEntries(Object.entries(v as Obj).map(([k, x]) => [k, walk(x)]));
    return v;
  };
  return lines.map((l) => ({ level: l.level, msg: l.msg, obj: walk(l.obj) }));
}

/** JS `<` order of two strings (UTF-16 code units). */
export const codeUnitCompare = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);
