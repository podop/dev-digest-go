/**
 * Shared helpers of the Phase 4 golden generator: the deterministic JSON writer, large-string
 * elision, synthetic diffs, a fake clock and small async utilities. No dev-digest code in here.
 */
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

// ---------------------------------------------------------------------------
// Deterministic JSON writer (same layout as the Phase 3 goldens)
// ---------------------------------------------------------------------------

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

/** Strings longer than this many UTF-16 units are written as a `$blob` digest (see README). */
export const BLOB_THRESHOLD = 16_384;

// Characters that are invisible or line-breaking in editors are written as \uXXXX escapes.
const ESCAPE_RE = /[\u007f-\u00a0\u2000-\u200f\u2028-\u202f\u205f-\u206f\u3000\ufeff\ufffd]/g;
function jstr(s: string): string {
  return JSON.stringify(s).replace(ESCAPE_RE, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
}

/** The digest object that stands for a string above BLOB_THRESHOLD. */
export function blobOf(s: string): { [k: string]: Json } {
  const cps = [...s];
  return {
    $blob: true,
    len: s.length,
    sha256: createHash('sha256').update(s, 'utf8').digest('hex'),
    head: cps.slice(0, 80).join(''),
    tail: cps.slice(-80).join(''),
  };
}

/** Recursively replaces every over-long string value (not keys) by its digest. */
export function elide(v: Json): Json {
  if (typeof v === 'string') return v.length > BLOB_THRESHOLD ? blobOf(v) : v;
  if (Array.isArray(v)) return v.map(elide);
  if (v !== null && typeof v === 'object') {
    const out: { [k: string]: Json } = {};
    for (const [k, x] of Object.entries(v)) out[k] = elide(x);
    return out;
  }
  return v;
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

export function ser(v: Json, ind = ''): string {
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

/** Drops `undefined` object members (as JSON.stringify does) and checks the value is JSON-safe. */
export function clean(v: unknown): Json {
  if (v === undefined) return null;
  if (v === null || typeof v === 'boolean' || typeof v === 'string') return v as Json;
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) throw new Error(`non-finite number: ${v}`);
    return v;
  }
  if (Array.isArray(v)) return v.map((x) => clean(x === undefined ? null : x));
  if (v instanceof Date) return v.toISOString();
  if (typeof v === 'object') {
    const out: { [k: string]: Json } = {};
    for (const [k, x] of Object.entries(v as Record<string, unknown>)) {
      if (x === undefined || typeof x === 'function') continue;
      out[k] = clean(x);
    }
    return out;
  }
  throw new Error(`unsupported value in golden: ${typeof v}`);
}

export class Writer {
  constructor(private readonly outDir: string) {}
  write(name: string, value: unknown): void {
    mkdirSync(this.outDir, { recursive: true });
    writeFileSync(join(this.outDir, name), ser(elide(clean(value))) + '\n');
  }
}

export function readJson<T>(path: string): T {
  return JSON.parse(readFileSync(path, 'utf8')) as T;
}

// ---------------------------------------------------------------------------
// Corpus text: line arrays, synthetic diffs
// ---------------------------------------------------------------------------

export interface TextSpec {
  /** Lines joined by `eol` (default "\n"). */
  lines?: string[];
  eol?: string;
  /** Append one `eol` after the last line. */
  trailingEol?: boolean;
  /** Literal text (use when lines would be awkward). */
  text?: string;
  /** Synthetic diff, see README "Synthetic diffs". */
  synth?: SynthSpec;
}

export interface SynthHunk {
  start: number;
  add?: number;
  del?: number;
  ctx?: number;
  text?: string;
}
export interface SynthSpec {
  files: Array<{ path: string; hunks: SynthHunk[] }>;
}

/** Lines of one synthetic hunk (header included). */
export function synthHunk(h: SynthHunk): string[] {
  const add = h.add ?? 0;
  const del = h.del ?? 0;
  const ctx = h.ctx ?? 0;
  const text = h.text ?? 'const value = compute();';
  const out = [`@@ -${h.start},${ctx + del} +${h.start},${ctx + add} @@`];
  for (let i = 1; i <= ctx; i++) out.push(` ${text} ctx${i}`);
  for (let i = 1; i <= del; i++) out.push(`-${text} old${i}`);
  for (let i = 1; i <= add; i++) out.push(`+${text} new${i}`);
  return out;
}

/** Raw diff of a synthetic spec: per file `diff --git`, `---`, `+++`, hunks; joined with "\n", no trailing newline. */
export function synthDiff(spec: SynthSpec): string {
  const lines: string[] = [];
  for (const f of spec.files) {
    lines.push(`diff --git a/${f.path} b/${f.path}`, `--- a/${f.path}`, `+++ b/${f.path}`);
    for (const h of f.hunks) lines.push(...synthHunk(h));
  }
  return lines.join('\n');
}

/** Patch (hunks only, as stored in pr_files.patch) of a synthetic file. */
export function synthPatch(hunks: SynthHunk[]): string {
  return hunks.flatMap(synthHunk).join('\n');
}

export function textOf(c: TextSpec): string {
  if (c.synth) return synthDiff(c.synth);
  if (c.text !== undefined) return c.text;
  const eol = c.eol ?? '\n';
  const body = (c.lines ?? []).join(eol);
  return c.trailingEol ? body + eol : body;
}

// ---------------------------------------------------------------------------
// Async and clock helpers
// ---------------------------------------------------------------------------

/** Lets every pending microtask and I/O-free continuation run. */
export const settle = (): Promise<void> => new Promise((r) => setImmediate(r));
export async function settleMany(n = 5): Promise<void> {
  for (let i = 0; i < n; i++) await settle();
}

export interface Deferred<T> {
  promise: Promise<T>;
  resolve(v: T): void;
  reject(e: unknown): void;
}
export function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** A logical clock in milliseconds: it only moves when the generator's fake ports call `bump`. */
export class LogicalClock {
  private ms = 0;
  readonly base = Date.UTC(2026, 0, 1, 12, 0, 0);
  bump(by: number): void {
    this.ms += by;
  }
  reset(): void {
    this.ms = 0;
  }
  get now(): number {
    return this.base + this.ms;
  }
}

/**
 * Replaces the global Date so that run logs are reproducible: `Date.now()` and `new Date()` both
 * read the logical clock, which moves only when a fake port bumps it (so the number of Date calls
 * made by libraries cannot change a duration). TZ is UTC (set by the generator), so the run bus
 * `t` stamp (`toTimeString().slice(0, 8)`) is HH:MM:SS of 2026-01-01T12:00:00Z plus the clock.
 * Returns the function that restores the real Date.
 */
export function installFakeDate(clock: LogicalClock): () => void {
  const RealDate = Date;
  class FakeDate extends RealDate {
    constructor(...args: unknown[]) {
      if (args.length === 0) super(clock.now);
      else super(...(args as [number]));
    }
    static override now(): number {
      return clock.now;
    }
  }
  globalThis.Date = FakeDate as unknown as DateConstructor;
  return () => {
    globalThis.Date = RealDate;
  };
}

/** mulberry32 — small seeded PRNG. */
export function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Byte-order (UTF-8) comparison, as PostgreSQL COLLATE "C" / Go string comparison. */
export function byteCompare(a: string, b: string): number {
  return Buffer.compare(Buffer.from(a, 'utf8'), Buffer.from(b, 'utf8'));
}
