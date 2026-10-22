/**
 * Golden generator for the Phase 4 review parity corpus (step S12).
 *
 * Runs the NODE implementation of dev-digest (read-only checkout, commit d1c9093) over the
 * committed corpus and writes JSON goldens: reviewer-core (diff, slicing, splitting, modes, reduce,
 * grounding, scope, score, prompt assembly, the Review schema, parse and reprompt texts), the review
 * run executor with in-memory fakes and a scripted LLM (run events, store writes, traces), the
 * multi-agent read model and the SSE byte streams (Fastify + fastify-sse-v2). See ./README.md.
 *
 * Hard rules, enforced below (same as the Phase 3 generator):
 *  - the dev-digest checkout is never written to: HEAD must be d1c9093 and `git status` clean
 *    before AND after the run; only read-only git commands are used;
 *  - output goes only into --out, which must lie inside the testdata directory;
 *  - tsx cache and scratch live under $TMPDIR (required, must not be inside the checkout);
 *  - output is deterministic: no real clocks (a logical clock replaces Date), no random data, the
 *    local HTTP servers only influence recorded bodies, never ports or timings.
 */
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { Writer } from './lib.mts';
import { genCallers } from './callers.mts';
import { genDomain } from './domain.mts';
import { genEngine } from './engine.mts';
import { genLlm } from './llm.mts';
import { genMultiAgent } from './multiagent.mts';
import { genRuns } from './runs.mts';
import type { Ctx } from './ctx.mts';

const EXPECTED_DD_HEAD_PREFIX = 'd1c9093';

// All timestamps the Node code renders are in UTC.
process.env.TZ = 'UTC';

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

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Mod = any;
const load = async (rel: string): Promise<Mod> => import(pathToFileURL(join(dd, rel)).href);
const S = 'server/src/';
const R = 'reviewer-core/src/';
const M = `${S}modules/reviews/`;

const ddRequire = createRequire(join(dd, 'server', 'package.json'));
const rcRequire = createRequire(join(dd, 'reviewer-core', 'package.json'));

const N = {
  rc: await load(`${R}index.ts`),
  pool: await load(`${R}review/pool.ts`),
  grounding: await load(`${R}grounding.ts`),
  shared: await load(`${S}vendor/shared/index.ts`),
  diffParser: await load(`${S}adapters/git/diff-parser.ts`),
  mock: await load(`${S}adapters/llm/mock.ts`),
  openrouter: await load(`${R}llm/openrouter.ts`),
  sse: await load(`${S}platform/sse.ts`),
  errors: await load(`${S}platform/errors.ts`),
  errorHandler: await load(`${S}http/error-handler.ts`),
  schema: await load(`${S}db/schema.ts`),
  executor: await load(`${M}application/run-executor.ts`),
  service: await load(`${M}application/review-service.ts`),
  multiAgentService: await load(`${M}application/multi-agent-service.ts`),
  routes: await load(`${M}routes.ts`),
  prompt: await load(`${M}domain/prompt.ts`),
  skillsDomain: await load(`${M}domain/skills.ts`),
  multiAgent: await load(`${M}domain/multi-agent.ts`),
  reviewDomain: await load(`${M}domain/review.ts`),
  mappers: await load(`${M}repository/mappers.ts`),
  reviewRepo: await load(`${M}repository/review.repo.ts`),
  runRepo: await load(`${M}repository/run.repo.ts`),
  pullRepo: await load(`${M}repository/pull.repo.ts`),
  multiAgentRepo: await load(`${M}repository/multi-agent.repo.ts`),
  skillsIndex: await load(`${S}modules/skills/index.ts`),
  projectContext: await load(`${S}modules/project-context/domain/run-context.ts`),
  codeindexExtract: await load(`${S}adapters/codeindex/extract.ts`),
  ripgrep: await load(`${S}adapters/codeindex/ripgrep.ts`),
  sourceAdapters: await load(`${S}modules/repo-intel/infrastructure/source-adapters.ts`),
  sourceQueries: await load(`${S}modules/repo-intel/application/source-queries.ts`),
};

const drizzle = ddRequire('drizzle-orm') as Mod;
const fastifyFactory = ddRequire('fastify') as Mod;
const sseV2 = ddRequire('fastify-sse-v2') as Mod;
const providerZod = (await import(pathToFileURL(ddRequire.resolve('fastify-type-provider-zod')).href)) as Mod;

function versionOf(pkg: string, from: 'server' | 'reviewer-core'): string {
  const pj = join(dd, from, 'node_modules', pkg, 'package.json');
  return (JSON.parse(readFileSync(pj, 'utf8')) as { version: string }).version;
}

const ctx: Ctx = {
  dd,
  corpus,
  testdata,
  N,
  drizzle,
  fastify: fastifyFactory.default ?? fastifyFactory,
  sseV2,
  providerZod,
  rcRequire,
  writer: new Writer(outDir),
};

try {
  await genDomain(ctx);
  await genEngine(ctx);
  await genLlm(ctx);
  await genMultiAgent(ctx);
  await genCallers(ctx);
  await genRuns(ctx);
  ctx.writer.write('meta.json', {
    note: 'Provenance of the goldens. Regenerate with the command in gen/README.md.',
    devDigestCommit: ddHead,
    tools: {
      '@fastify/fastify': versionOf('fastify', 'server'),
      'fastify-sse-v2': versionOf('fastify-sse-v2', 'server'),
      'fastify-type-provider-zod': versionOf('fastify-type-provider-zod', 'server'),
      'drizzle-orm': versionOf('drizzle-orm', 'server'),
      zod: versionOf('zod', 'server'),
      'zod (reviewer-core)': versionOf('zod', 'reviewer-core'),
      openai: versionOf('openai', 'reviewer-core'),
    },
  });
} finally {
  process.chdir(startCwd);
}

const ddHeadAfter = assertCheckoutPristine('after');
if (ddHeadAfter !== ddHead) throw new Error('dev-digest HEAD changed during the run');
console.log(`goldens written to ${relative(startCwd, outDir) || '.'} (dev-digest ${ddHead.slice(0, 7)}, checkout clean before and after)`);
