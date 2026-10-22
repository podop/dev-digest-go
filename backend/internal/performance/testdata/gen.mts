/**
 * Node golden generator of the performance context (Phase 5, S16/S17; spec 7.11, AC5017, AC5055).
 *
 * Runs the NODE implementation of dev-digest (read-only checkout, commit d1c9093): the period query
 * schema (zod 3.25.76), window resolution, and the whole PerformanceService over an in-memory reader
 * that filters like the SQL does. Output: ./node.json (corpus inputs + Node outputs), read by
 * domain/period_test.go and transport/http/parity_test.go.
 *
 *   mkdir -p /tmp/p5f && cd backend/internal/performance/testdata
 *   TMPDIR=/tmp/p5f $DD/server/node_modules/.bin/tsx --tsconfig $DD/server/tsconfig.json \
 *     gen.mts --dd $DD --out node.json
 *
 * The checkout is never written to (HEAD d1c9093, clean before and after); the output is
 * deterministic (seeded PRNG, fixed clock, TZ=UTC), so a second run is byte-identical.
 */
import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

process.env.TZ = 'UTC';

function arg(name: string): string {
  const i = process.argv.indexOf(name);
  const v = i >= 0 ? process.argv[i + 1] : undefined;
  if (!v) throw new Error(`missing ${name} <value>`);
  return v;
}
const dd = resolve(arg('--dd'));
const here = dirname(fileURLToPath(import.meta.url));
const out = resolve(here, arg('--out'));
const rel = relative(here, out);
if (rel.startsWith('..') || isAbsolute(rel)) throw new Error('--out must lie inside testdata');
if (!relative(dd, here).startsWith('..')) throw new Error('refusing to work inside the dev-digest checkout');

function git(...args: string[]): string {
  return execFileSync('git', ['--no-optional-locks', '-C', dd, ...args], { encoding: 'utf8' });
}
function pristine(when: string): string {
  const head = git('rev-parse', 'HEAD').trim();
  if (!head.startsWith('d1c9093')) throw new Error(`${when}: dev-digest HEAD is ${head}, want d1c9093`);
  if (git('status', '--porcelain').trim() !== '') throw new Error(`${when}: dev-digest checkout is not clean`);
  return head;
}
const head = pristine('before');

const mod = (p: string) => import(pathToFileURL(`${dd}/server/src/modules/performance/${p}`).href);
const { PeriodQuery } = await mod('http/schemas.ts');
const { resolveWindow, previousWindow, toPeriodInfo } = await mod('domain/period.ts');
const { PerformanceService } = await mod('application/performance-service.ts');
const zodPkg = JSON.parse(
  execFileSync('cat', [`${dd}/server/node_modules/zod/package.json`], { encoding: 'utf8' }),
) as { version: string };

// ---- seeded PRNG ----
function mulberry32(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const rnd = mulberry32(5055);
const pick = <T>(xs: readonly T[]): T => xs[Math.floor(rnd() * xs.length)]!;
const int = (lo: number, hi: number) => lo + Math.floor(rnd() * (hi - lo + 1));

const NOW_MS = Date.UTC(2026, 9, 22, 12, 0, 0, 123);
const NOW = new Date(NOW_MS);
const HOUR = 3600_000;
const DAY = 24 * HOUR;

// ---- period cases ----
type Q = { period?: string | string[]; from?: string | string[]; to?: string | string[] };
const periodQueries: { name: string; query: Q }[] = [];
const addQ = (name: string, query: Q) => periodQueries.push({ name, query });

addQ('empty', {});
addQ('1d', { period: '1d' });
addQ('30d', { period: '30d' });
addQ('period empty string', { period: '' });
addQ('period unknown', { period: '7d' });
addQ('period upper case', { period: '1D' });
addQ('period repeated', { period: ['1d', '30d'] });
addQ('from repeated', { period: 'custom', from: ['2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z'], to: '2026-02-01T00:00:00Z' });
addQ('to repeated', { period: 'custom', from: '2026-01-01T00:00:00Z', to: ['2026-02-01T00:00:00Z', 'x'] });
addQ('preset with from', { period: '1d', from: '2026-01-01T00:00:00Z' });
addQ('preset with to', { period: '30d', to: '2026-01-01T00:00:00Z' });
addQ('preset with both', { from: '2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z' });
addQ('preset with invalid from', { period: '1d', from: 'garbage' });
addQ('preset with empty from', { period: '1d', from: '' });
addQ('custom none', { period: 'custom' });
addQ('custom only from', { period: 'custom', from: '2026-01-01T00:00:00Z' });
addQ('custom only to', { period: 'custom', to: '2026-01-01T00:00:00Z' });
addQ('custom invalid from only', { period: 'custom', from: 'nope' });
addQ('custom invalid both', { period: 'custom', from: 'nope', to: '2026-13-01T00:00:00Z' });
addQ('custom bad enum and bad from', { period: 'x', from: 'nope' });
addQ('custom bad enum and preset from', { period: 'x', from: '2026-01-01T00:00:00Z' });
addQ('custom equal', { period: 'custom', from: '2026-01-01T00:00:00Z', to: '2026-01-01T00:00:00Z' });
addQ('custom reversed', { period: 'custom', from: '2026-01-02T00:00:00Z', to: '2026-01-01T00:00:00Z' });
addQ('custom 7 days', { period: 'custom', from: '2026-01-01T00:00:00Z', to: '2026-01-08T00:00:00Z' });
addQ('custom 366 days', { period: 'custom', from: '2025-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z' });
addQ('custom 366 days + 1ms', { period: 'custom', from: '2025-01-01T00:00:00.000Z', to: '2026-01-02T00:00:00.001Z' });
addQ('custom 367 days', { period: 'custom', from: '2025-01-01T00:00:00Z', to: '2026-01-03T00:00:00Z' });
addQ('custom 1 ms', { period: 'custom', from: '2026-01-01T00:00:00.000Z', to: '2026-01-01T00:00:00.001Z' });
addQ('custom sub-ms equal', { period: 'custom', from: '2026-01-01T00:00:00.1234Z', to: '2026-01-01T00:00:00.1239Z' });
addQ('custom sub-ms longer', { period: 'custom', from: '2026-01-01T00:00:00.123Z', to: '2026-01-01T00:00:00.1239999Z' });
addQ('custom minutes only', { period: 'custom', from: '2026-01-01T00:00Z', to: '2026-01-02T00:00Z' });
addQ('custom nanos', { period: 'custom', from: '2026-01-01T00:00:00.123456789Z', to: '2026-01-02T00:00:00.987654321Z' });
addQ('custom offset colon', { period: 'custom', from: '2026-01-01T00:00:00+05:30', to: '2026-01-02T00:00:00+05:30' });
addQ('custom offset compact', { period: 'custom', from: '2026-01-01T00:00:00+0530', to: '2026-01-02T00:00:00-0800' });
addQ('custom offset hours only', { period: 'custom', from: '2026-01-01T00:00:00+05', to: '2026-01-02T00:00:00Z' });
addQ('custom offset minutes only', { period: 'custom', from: '2026-01-01T00:00Z', to: '2026-01-02T00:00-0000' });
addQ('custom offset huge', { period: 'custom', from: '2026-01-01T00:00:00+99:99', to: '2026-01-02T00:00:00Z' });
addQ('custom offset 24', { period: 'custom', from: '2026-01-01T00:00:00+24:00', to: '2026-01-02T00:00:00-24:00' });
addQ('custom offset 23:60', { period: 'custom', from: '2026-01-01T00:00:00+23:60', to: '2026-01-05T00:00:00Z' });
addQ('custom date only', { period: 'custom', from: '2026-01-01', to: '2026-01-02' });
addQ('custom lower t', { period: 'custom', from: '2026-01-01t00:00:00Z', to: '2026-01-02T00:00:00Z' });
addQ('custom lower z', { period: 'custom', from: '2026-01-01T00:00:00z', to: '2026-01-02T00:00:00Z' });
addQ('custom space', { period: 'custom', from: '2026-01-01 00:00:00Z', to: '2026-01-02T00:00:00Z' });
addQ('custom padded', { period: 'custom', from: ' 2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z ' });
addQ('custom impossible date', { period: 'custom', from: '2026-02-30T00:00:00Z', to: '2026-03-02T00:00:00Z' });
addQ('custom leap day', { period: 'custom', from: '2024-02-29T00:00:00Z', to: '2024-03-02T00:00:00Z' });
addQ('custom non-leap day', { period: 'custom', from: '2025-02-29T00:00:00Z', to: '2025-03-02T00:00:00Z' });
addQ('custom 1900 leap', { period: 'custom', from: '1900-02-29T00:00:00Z', to: '1900-03-02T00:00:00Z' });
addQ('custom 2000 leap', { period: 'custom', from: '2000-02-29T00:00:00Z', to: '2000-03-02T00:00:00Z' });
addQ('custom 0000 leap', { period: 'custom', from: '0000-02-29T00:00:00Z', to: '0000-03-02T00:00:00Z' });
addQ('custom hour 24', { period: 'custom', from: '2026-01-01T24:00:00Z', to: '2026-01-03T00:00:00Z' });
addQ('custom second 60', { period: 'custom', from: '2026-01-01T00:00:60Z', to: '2026-01-03T00:00:00Z' });
addQ('custom trailing dot', { period: 'custom', from: '2026-01-01T00:00:00.Z', to: '2026-01-03T00:00:00Z' });
addQ('custom no zone', { period: 'custom', from: '2026-01-01T00:00:00', to: '2026-01-03T00:00:00Z' });
addQ('custom plus sign year', { period: 'custom', from: '+2026-01-01T00:00:00Z', to: '2026-01-03T00:00:00Z' });
addQ('custom unicode digits', { period: 'custom', from: '２０２６-01-01T00:00:00Z', to: '2026-01-03T00:00:00Z' });
addQ('custom newline', { period: 'custom', from: '2026-01-01T00:00:00Z\n', to: '2026-01-03T00:00:00Z' });
addQ('custom max year', { period: 'custom', from: '9999-12-31T00:00:00Z', to: '9999-12-31T23:59:59.999Z' });
addQ('custom year end offset', { period: 'custom', from: '9999-12-31T00:00:00Z', to: '9999-12-31T23:59:00-23:59' });
addQ('custom year zero offset', { period: 'custom', from: '0000-01-01T00:00:00+23:59', to: '0000-01-01T23:59:00Z' });
addQ('custom both offsets', { period: 'custom', from: '2026-03-29T01:30:00+01:00', to: '2026-03-29T03:30:00+02:00' });
addQ('unknown key ignored', { period: '1d', limit: '5' } as Q);

// random datetime-ish strings: the regex, V8's parser and the span rules
const pad = (n: number, w: number) => String(n).padStart(w, '0');
function randomInstant(): string {
  const y = pick([0, 1, 1900, 2000, 2024, 2025, 2026, 9999]);
  const m = int(0, 13);
  const d = int(0, 32);
  const date = `${pad(y, 4)}-${pad(m, 2)}-${pad(d, 2)}`;
  const time = `${pad(int(0, 25), 2)}:${pad(int(0, 60), 2)}`;
  const sec = pick(['', `:${pad(int(0, 60), 2)}`, `:${pad(int(0, 59), 2)}`, `:${pad(int(0, 59), 2)}.${pad(int(0, 999), 3)}`, `:${pad(int(0, 59), 2)}.${pad(int(0, 99999999), 8)}`]);
  const zone = pick(['Z', 'Z', 'Z', '+00:00', '-00:00', '+05:30', '-0800', '+0530', '+14:00', '-12:00', '+23:59', '+24:00', '+00:60', '+99:99', '+05', 'z', '', '+5:30']);
  return `${date}T${time}${sec}${zone}`;
}
for (let i = 0; i < 400; i++) {
  addQ(`random from #${i}`, { period: 'custom', from: randomInstant(), to: '2030-01-01T00:00:00Z' });
}
for (let i = 0; i < 100; i++) {
  addQ(`random to #${i}`, { period: 'custom', from: '2026-01-01T00:00:00Z', to: randomInstant() });
}

function issuesOf(error: { issues: { code: string; path: (string | number)[]; message: string }[] }) {
  return error.issues.map((i) => ({ code: i.code, path: i.path, message: i.message }));
}
const iso = (d: Date) => d.toISOString();

const period = periodQueries.map(({ name, query }) => {
  const r = PeriodQuery.safeParse(query);
  if (!r.success) return { name, query, issues: issuesOf(r.error) };
  const q = r.data as { period: string; from?: string; to?: string };
  const entry: Record<string, unknown> = { name, query, issues: null, parsed: q };
  try {
    const win = resolveWindow(q, NOW);
    const prev = previousWindow(win);
    entry.window = { key: win.key, from: win.from.getTime(), to: win.to.getTime(), toInclusive: win.toInclusive };
    entry.previous = { from: prev.from.getTime(), to: prev.to.getTime(), toInclusive: prev.toInclusive };
    entry.info = toPeriodInfo(win);
  } catch (e) {
    entry.windowError = (e as Error).name + ': ' + (e as Error).message;
    try {
      entry.windowMs = [new Date(q.from!).getTime(), new Date(q.to!).getTime()];
    } catch {
      /* ignored */
    }
  }
  return entry;
});

// ---- fold scenarios ----
const STATUSES = ['done', 'done', 'done', 'failed', 'cancelled'] as const;
const SOURCES = ['local', 'local', 'ci'] as const;
const MODELS = [null, 'gpt-4o', 'gpt-4o-mini', 'claude-sonnet', 'Claude-Sonnet', 'z-model', 'é-model', '\u{1F600}-model', '～-model', 'model-1', 'model-10', 'model-2'];
const NAMES = ['Zeta', 'alpha', 'Alpha', 'beta', 'security-review', 'security_review', 'security review', 'Éclair', 'eclair', '\u{1F600} smile', '～ tilde', 'no-then-chains', 'n-plus-one-gate', 'same', 'same'];
const SEVERITIES = ['CRITICAL', 'WARNING', 'SUGGESTION', 'INFO'];
const CATEGORIES = ['bug', 'security', 'perf', 'style', 'test', 'Bug', 'é', ''];
const COSTS = [0.1, 0.2, 0.3, 0.0000005, 0.0000015, 0.0000025, 0.000001, 1e-7, 0.123456789, 12.3456785, 0.30000000000000004, 1.005, 2.675, 0.7, 1234.5678915, 0.049999995, 0.0149999995];

function cost(): number | null {
  const r = rnd();
  if (r < 0.3) return null;
  if (r < 0.6) return pick(COSTS);
  if (r < 0.8) return Math.round(rnd() * 1e8) / 1e6;
  return Math.round(rnd() * 1e9) / 1e9;
}

const uuid = (n: number) => `00000000-0000-4000-8000-${pad(n, 12)}`;

type Run = {
  agentId: string | null; status: string; source: string; model: string | null; durationMs: number | null;
  costUsd: number | null; ranAtMs: number;
  findings: { total: number; accepted: number; dismissed: number } | null;
  facts: { severity: string; category: string; count: number }[];
};
type Scenario = {
  name: string; query: Q; agents: { id: string; name: string }[]; runs: Run[];
  skills: { skillId: string; name: string; runs: number }[]; memory: unknown[];
  history: unknown[];
};

const WINDOWS: { name: string; query: Q }[] = [
  { name: '1d', query: { period: '1d' } },
  { name: '30d', query: { period: '30d' } },
  { name: 'default', query: {} },
  { name: 'custom 7d', query: { period: 'custom', from: '2026-10-10T00:00:00Z', to: '2026-10-17T00:00:00Z' } },
  { name: 'custom 36h', query: { period: 'custom', from: '2026-10-20T00:00:00Z', to: '2026-10-21T12:00:00Z' } },
  { name: 'custom 48h', query: { period: 'custom', from: '2026-10-20T00:00:00Z', to: '2026-10-22T00:00:00Z' } },
  { name: 'custom 48h+1ms', query: { period: 'custom', from: '2026-10-20T00:00:00.000Z', to: '2026-10-22T00:00:00.001Z' } },
  { name: 'custom 100h17ms', query: { period: 'custom', from: '2026-10-01T00:00:00.000Z', to: '2026-10-05T04:00:00.017Z' } },
  { name: 'custom 1ms', query: { period: 'custom', from: '2026-10-21T00:00:00.000Z', to: '2026-10-21T00:00:00.001Z' } },
  { name: 'custom 3ms', query: { period: 'custom', from: '2026-10-21T00:00:00.000Z', to: '2026-10-21T00:00:00.003Z' } },
  { name: 'custom 5 days odd', query: { period: 'custom', from: '2026-10-10T07:31:11.111Z', to: '2026-10-15T19:02:03.457Z' } },
  { name: 'custom 366d', query: { period: 'custom', from: '2025-10-21T00:00:00Z', to: '2026-10-22T00:00:00Z' } },
  { name: 'custom offsets', query: { period: 'custom', from: '2026-10-10T00:00:00+05:30', to: '2026-10-13T12:00:00-0800' } },
];

const scenarios: Scenario[] = [];
let seq = 0;
function windowBounds(q: Q) {
  const parsed = PeriodQuery.parse(q) as { period: string; from?: string; to?: string };
  const w = resolveWindow(parsed, NOW);
  const p = previousWindow(w);
  return { lo: p.from.getTime(), hi: w.to.getTime(), from: w.from.getTime(), span: w.to.getTime() - w.from.getTime() };
}
function makeScenario(name: string, query: Q, nAgents: number, nRuns: number, ghostAgents: number): Scenario {
  const b = windowBounds(query);
  const agents = Array.from({ length: nAgents }, () => ({ id: uuid(++seq), name: pick(NAMES) }));
  const ghosts = Array.from({ length: ghostAgents }, () => uuid(++seq));
  const runs: Run[] = [];
  for (let i = 0; i < nRuns; i++) {
    const r = rnd();
    let at: number;
    if (r < 0.06) at = b.from;
    else if (r < 0.12) at = b.hi;
    else if (r < 0.16) at = b.lo;
    else if (r < 0.2) at = b.from - 1;
    else if (r < 0.24) at = b.hi - 1;
    else if (r < 0.3) at = b.lo - 5;
    else if (r < 0.35) at = b.hi + 7;
    else at = b.lo + Math.floor(rnd() * (b.hi - b.lo + 1));
    const who = rnd();
    const agentId = who < 0.1 ? null : who < 0.2 && ghosts.length > 0 ? pick(ghosts) : agents.length > 0 ? pick(agents).id : null;
    const hasF = rnd() < 0.7;
    const total = int(0, 12);
    const accepted = int(0, total);
    const dismissed = int(0, total - accepted);
    const facts = hasF
      ? Array.from({ length: int(0, 4) }, () => ({ severity: pick(SEVERITIES), category: pick(CATEGORIES), count: int(0, 5) }))
      : [];
    runs.push({
      agentId, status: pick(STATUSES), source: pick(SOURCES), model: pick(MODELS),
      durationMs: rnd() < 0.2 ? null : int(0, 200000), costUsd: cost(), ranAtMs: at,
      findings: hasF ? { total, accepted, dismissed } : null, facts,
    });
  }
  const skills = Array.from({ length: int(0, 9) }, () => ({ skillId: uuid(++seq), name: pick(NAMES), runs: int(0, 6) }));
  const memTexts = ['use const', 'Use const', ' padded ', '', '   ', 'é', '\u{1F600}', '～', 'a', 'b'];
  const memory: unknown[] = Array.from({ length: int(0, 7) }, () => {
    const k = rnd();
    if (k < 0.07) return null;
    if (k < 0.12) return 'not an array';
    if (k < 0.16) return { text: 'object' };
    if (k < 0.2) return [];
    return Array.from({ length: int(0, 6) }, () => {
      const e = rnd();
      if (e < 0.06) return null;
      if (e < 0.1) return 5;
      if (e < 0.14) return 'text';
      if (e < 0.18) return [{ text: 'in array' }];
      if (e < 0.24) return { text: 7 };
      if (e < 0.28) return { other: 'x' };
      return { text: pick(memTexts), extra: int(0, 3) };
    });
  });
  const history = Array.from({ length: int(0, 4) }, () => ({
    run_id: uuid(++seq), ran_at: iso(new Date(b.from + int(0, 1000))), status: pick(['done', 'failed']), source: pick(SOURCES),
    pr: rnd() < 0.5 ? null : { id: uuid(++seq), repo_id: uuid(++seq), number: int(1, 999) },
    tokens: rnd() < 0.3 ? null : int(0, 99999), cost_usd: cost(), findings_count: rnd() < 0.3 ? null : int(0, 20), has_trace: rnd() < 0.5,
  }));
  return { name, query, agents, runs, skills, memory, history };
}

for (const w of WINDOWS) {
  scenarios.push(makeScenario(`${w.name} / empty workspace`, w.query, 2, 0, 0));
  scenarios.push(makeScenario(`${w.name} / no agents`, w.query, 0, 8, 1));
  for (let k = 0; k < 2; k++) scenarios.push(makeScenario(`${w.name} / small ${k}`, w.query, 3, 14, 1));
  scenarios.push(makeScenario(`${w.name} / large`, w.query, 5, 90, 2));
}

// ties in most-active: equal runs, equal last run, names decide
{
  const q: Q = { period: '30d' };
  const b = windowBounds(q);
  const agents = [
    { id: uuid(9001), name: 'bravo' }, { id: uuid(9002), name: 'alpha' }, { id: uuid(9003), name: 'Alpha' },
    { id: uuid(9004), name: 'charlie' },
  ];
  const at = b.from + 12345;
  const mk = (agentId: string, ranAtMs: number, c: number | null): Run => ({
    agentId, status: 'done', source: 'local', model: 'm', durationMs: 10, costUsd: c, ranAtMs, findings: null, facts: [],
  });
  scenarios.push({
    name: 'ties in most-active', query: q, agents,
    runs: [mk(agents[0]!.id, at, 0.1), mk(agents[1]!.id, at, 0.1), mk(agents[2]!.id, at, 0.2), mk(agents[3]!.id, at - 1, 0.3),
      mk(agents[3]!.id, at - 2, null)],
    skills: [], memory: [], history: [],
  });
}

// ---- run the Node service over each scenario ----
function inWindow(win: { from: Date; to: Date; toInclusive: boolean }, ms: number): boolean {
  return ms >= win.from.getTime() && (win.toInclusive ? ms <= win.to.getTime() : ms < win.to.getTime());
}
const sevCats = (r: Run) => r.facts;

async function runScenario(s: Scenario) {
  const reader = {
    listAgents: async () => s.agents.map((a) => ({ ...a })),
    findAgent: async (_ws: string, id: string) => s.agents.find((a) => a.id === id),
    countedRuns: async (_ws: string, win: { from: Date; to: Date; toInclusive: boolean }, agentId?: string) =>
      s.runs
        .filter((r) => inWindow(win, r.ranAtMs) && (agentId === undefined || r.agentId === agentId))
        .map((r) => ({ agentId: r.agentId, status: r.status, source: r.source, model: r.model, durationMs: r.durationMs, costUsd: r.costUsd, ranAt: new Date(r.ranAtMs) })),
    findingCounts: async (_ws: string, win: { from: Date; to: Date; toInclusive: boolean }, agentId?: string) => {
      const by = new Map<string | null, { agentId: string | null; total: number; accepted: number; dismissed: number }>();
      for (const r of s.runs) {
        if (!inWindow(win, r.ranAtMs) || (agentId !== undefined && r.agentId !== agentId) || r.findings === null) continue;
        if (r.findings.total === 0) continue; // SQL: runs without findings add no rows
        const e = by.get(r.agentId) ?? { agentId: r.agentId, total: 0, accepted: 0, dismissed: 0 };
        e.total += r.findings.total; e.accepted += r.findings.accepted; e.dismissed += r.findings.dismissed;
        by.set(r.agentId, e);
      }
      return [...by.values()];
    },
    findingFacts: async (_ws: string, win: { from: Date; to: Date; toInclusive: boolean }, agentId: string) =>
      s.runs
        .filter((r) => inWindow(win, r.ranAtMs) && r.agentId === agentId)
        .flatMap((r) => sevCats(r).map((f) => ({ ranAt: new Date(r.ranAtMs), severity: f.severity, category: f.category, count: f.count }))),
    skillUsage: async () => s.skills.map((k) => ({ ...k })),
    memoryPulled: async () => s.memory,
    runHistory: async () => s.history,
  };
  const service = new PerformanceService({ reader, clock: () => NOW });
  const parsed = PeriodQuery.parse(s.query);
  const dashboard = await service.overview('ws', parsed);
  const stats: Record<string, unknown> = {};
  for (const a of s.agents) stats[a.id] = await service.agentStats('ws', a.id, parsed);
  let unknown: string;
  try {
    await service.agentStats('ws', uuid(999999), parsed);
    unknown = 'no error';
  } catch (e) {
    unknown = (e as Error).message;
  }
  return { dashboard, stats, unknown };
}

const folds = [];
for (const s of scenarios) folds.push({ ...s, expected: await runScenario(s) });

const after = pristine('after');
if (after !== head) throw new Error('dev-digest HEAD moved during the run');
const doc = {
  meta: { devDigest: head.slice(0, 7), zod: zodPkg.version, now: NOW_MS, tz: 'UTC' },
  period,
  folds,
};
writeFileSync(out, JSON.stringify(doc) + '\n');
console.log(`wrote ${out}: ${period.length} period cases, ${folds.length} fold scenarios`);
