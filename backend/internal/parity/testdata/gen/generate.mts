/**
 * Golden generator for the Phase 5 parity corpus (step S21): intent, smart diff, blast radius (+ prior PRs),
 * PR brief, onboarding tour, agent performance, the three structured-output schemas, the mock fixtures
 * and the route-level wire bodies Node can produce without a database; extended in Phase 6 (steps S16, S17)
 * with the evals and ci modules, the workflow rules and the agent-runner (parts evals, ci, wfrules, runner).
 * See ./README.md.
 *
 * Runs the NODE implementation of dev-digest (read-only checkout, commit d1c9093): the real domain
 * functions, the real application services over in-memory fakes of their ports (scripted LLM, fake
 * stores, fake clock), and the real Fastify route plugins over those services.
 *
 * Hard rules, enforced below (same as the Phase 3/4 generators):
 *  - the dev-digest checkout is never written to: HEAD must be d1c9093 and `git status` clean
 *    before AND after the run; only read-only git commands are used;
 *  - output goes only into --out, which must lie inside the testdata directory;
 *  - tsx cache and scratch live under $TMPDIR (required, short, not inside the checkout);
 *  - output is deterministic: no real clocks (a logical clock replaces Date), no random data, the
 *    local HTTP servers only influence recorded bodies, never ports or timings.
 *
 * Usage: tsx gen/generate.mts --dd <dev-digest> --out goldens [--only intent,brief,evals,...]
 */
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { Writer } from './lib.mts';
import type { Ctx } from './ctx.mts';
import { genIntent } from './intent.mts';
import { genSmartDiff } from './smartdiff.mts';
import { genBlast } from './blast.mts';
import { genBrief } from './brief.mts';
import { genOnboarding } from './onboarding.mts';
import { genPerformance } from './performance.mts';
import { genSchemas } from './schemas.mts';
import { genRoutes } from './routes.mts';
import { genGit } from './git.mts';
import { genEvals } from './evals.mts';
import { genCi } from './ci.mts';
import { genWfRules } from './wfrules.mts';
import { genRunner } from './runner.mts';

const EXPECTED_DD_HEAD_PREFIX = 'd1c9093';

// All timestamps the Node code renders are in UTC.
process.env.TZ = 'UTC';

// ---------------------------------------------------------------------------
// CLI and guards
// ---------------------------------------------------------------------------

function arg(name: string, optional = false): string | undefined {
  const i = process.argv.indexOf(name);
  const v = i >= 0 ? process.argv[i + 1] : undefined;
  if (!v && !optional) throw new Error(`missing ${name} <value>`);
  return v;
}

const startCwd = process.cwd();
const dd = resolve(startCwd, arg('--dd')!);
const outDir = resolve(startCwd, arg('--out')!);
const only = arg('--only', true)?.split(',');
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
if (!scratchBase) throw new Error('TMPDIR must be set to a short scratch directory (tsx writes its cache there)');
if (inside(dd, resolve(scratchBase)) || inside(dd, tmpdir())) {
  throw new Error('TMPDIR must not be inside the dev-digest checkout');
}

// `localeCompare` without a locale (links.ts, blast, smart diff) follows the process locale: pin the English collation.
const collationLocale = new Intl.Collator().resolvedOptions().locale;
if (!collationLocale.startsWith('en-US') && collationLocale !== 'en') {
  throw new Error(`default collation locale is ${collationLocale}; run with LC_ALL=en_US.UTF-8 (goldens assume English collation)`);
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
const M = `${S}modules/`;
const A = 'agent-runner/src/';

const ddRequire = createRequire(join(dd, 'server', 'package.json'));
const rcRequire = createRequire(join(dd, 'reviewer-core', 'package.json'));

const N = {
  rc: await load(`${R}index.ts`),
  shared: await load(`${S}vendor/shared/index.ts`),
  errors: await load(`${S}platform/errors.ts`),
  errorHandler: await load(`${S}http/error-handler.ts`),
  promptLog: await load(`${S}platform/prompt-log.ts`),
  mockLlm: await load(`${S}adapters/llm/mock.ts`),
  // intent
  intentLinks: await load(`${M}intent/domain/links.ts`),
  intentDomain: await load(`${M}intent/domain/intent.ts`),
  intentClassification: await load(`${M}intent/domain/classification.ts`),
  intentConstants: await load(`${M}intent/domain/constants.ts`),
  intentService: await load(`${M}intent/application/intent-service.ts`),
  intentModel: await load(`${M}intent/infrastructure/llm-model.ts`),
  intentMappers: await load(`${M}intent/infrastructure/mappers.ts`),
  intentDocSource: await load(`${M}intent/infrastructure/doc-source.ts`),
  intentTicketSource: await load(`${M}intent/infrastructure/ticket-source.ts`),
  intentRoutes: await load(`${M}intent/http/routes.ts`),
  // smart diff
  smartDiffClassify: await load(`${M}smart-diff/domain/classify.ts`),
  smartDiffBuild: await load(`${M}smart-diff/domain/smart-diff.ts`),
  smartDiffService: await load(`${M}smart-diff/application/smart-diff-service.ts`),
  smartDiffRoutes: await load(`${M}smart-diff/http/routes.ts`),
  // blast
  blastDomain: await load(`${M}blast/domain/blast-radius.ts`),
  blastHistory: await load(`${M}blast/domain/history.ts`),
  blastService: await load(`${M}blast/application/blast-service.ts`),
  blastRoutes: await load(`${M}blast/http/routes.ts`),
  repoIntelBlast: await load(`${M}repo-intel/application/blast-radius.ts`),
  repoIntelRead: await load(`${M}repo-intel/infrastructure/read-repository.ts`),
  seedDiff: await load(`${S}db/seed-diff.ts`),
  simpleGit: await load(`${S}adapters/git/simple-git.ts`),
  ripgrep: await load(`${S}adapters/codeindex/ripgrep.ts`),
  codeindexExtract: await load(`${S}adapters/codeindex/extract.ts`),
  // brief
  briefConstants: await load(`${M}brief/domain/constants.ts`),
  briefInput: await load(`${M}brief/domain/input.ts`),
  briefPrompt: await load(`${M}brief/domain/prompt.ts`),
  briefNormalize: await load(`${M}brief/domain/normalize.ts`),
  briefSections: await load(`${M}brief/application/prompt-sections.ts`),
  briefService: await load(`${M}brief/application/brief-service.ts`),
  briefModel: await load(`${M}brief/infrastructure/llm-model.ts`),
  briefRoutes: await load(`${M}brief/http/routes.ts`),
  // onboarding
  onbConstants: await load(`${M}onboarding/domain/constants.ts`),
  onbInput: await load(`${M}onboarding/domain/input.ts`),
  onbRedact: await load(`${M}onboarding/domain/redact.ts`),
  onbCommand: await load(`${M}onboarding/domain/command-safety.ts`),
  onbPrompt: await load(`${M}onboarding/domain/prompt.ts`),
  onbTour: await load(`${M}onboarding/domain/tour.ts`),
  onbService: await load(`${M}onboarding/application/onboarding-service.ts`),
  onbModel: await load(`${M}onboarding/infrastructure/llm-model.ts`),
  onbRoutes: await load(`${M}onboarding/http/routes.ts`),
  // performance
  perfConstants: await load(`${M}performance/domain/constants.ts`),
  perfPeriod: await load(`${M}performance/domain/period.ts`),
  perfMetrics: await load(`${M}performance/domain/metrics.ts`),
  perfTrends: await load(`${M}performance/domain/trends.ts`),
  perfService: await load(`${M}performance/application/performance-service.ts`),
  perfSchemas: await load(`${M}performance/http/schemas.ts`),
  perfRoutes: await load(`${M}performance/http/routes.ts`),
  // evals (Phase 6)
  evConstants: await load(`${M}evals/domain/constants.ts`),
  evCaseBuilder: await load(`${M}evals/domain/case-builder.ts`),
  evCaseInput: await load(`${M}evals/domain/case-input.ts`),
  evRunInput: await load(`${M}evals/domain/run-input.ts`),
  evScoring: await load(`${M}evals/domain/scoring.ts`),
  evCompare: await load(`${M}evals/domain/compare.ts`),
  evDashboard: await load(`${M}evals/domain/dashboard.ts`),
  evCasesService: await load(`${M}evals/application/eval-cases-service.ts`),
  evRunsService: await load(`${M}evals/application/eval-runs-service.ts`),
  evExecutor: await load(`${M}evals/application/run-executor.ts`),
  skillsDomain: await load(`${M}skills/domain/skill.ts`),
  serverDiffParser: await load(`${S}adapters/git/diff-parser.ts`),
  // ci (Phase 6)
  ciConstants: await load(`${M}ci/domain/constants.ts`),
  ciSlug: await load(`${M}ci/domain/slug.ts`),
  ciWorkflow: await load(`${M}ci/domain/workflow.ts`),
  ciManifest: await load(`${M}ci/domain/manifest.ts`),
  ciIngest: await load(`${M}ci/domain/ingest.ts`),
  ciRules: await load(`${M}ci/domain/workflow-rules.ts`),
  ciCheck: await load(`${M}ci/application/workflow-check.ts`),
  ciArtifactReader: await load(`${M}ci/application/artifact-reader.ts`),
  ciExport: await load(`${M}ci/application/export-service.ts`),
  ciIngestService: await load(`${M}ci/application/ingest-service.ts`),
  ciYaml: await load(`${M}ci/infrastructure/yaml-codec.ts`),
  ciZip: await load(`${M}ci/infrastructure/zip-entry-reader.ts`),
  // agent-runner (Phase 6)
  runManifest: await load(`${A}manifest.ts`),
  runSkills: await load(`${A}skills.ts`),
  runContext: await load(`${A}context.ts`),
  runDiff: await load(`${A}diff.ts`),
  runGithub: await load(`${A}github.ts`),
  runArtifact: await load(`${A}artifact.ts`),
  runRun: await load(`${A}run.ts`),
  runErrors: await load(`${A}errors.ts`),
  rcOpenRouter: await load(`${R}llm/openrouter.ts`),
  rcToReview: await load(`${R}output/to-review.ts`),
  // tools of the checkout the Phase 6 parts use
  pQueue: (await import(pathToFileURL(createRequire(join(dd, 'server', 'package.json')).resolve('p-queue')).href)) as Mod,
  arRequire: createRequire(join(dd, 'agent-runner', 'package.json')),
};

const fastifyFactory = ddRequire('fastify') as Mod;
const providerZod = (await import(pathToFileURL(ddRequire.resolve('fastify-type-provider-zod')).href)) as Mod;

function versionOf(pkg: string, from: 'server' | 'reviewer-core' | 'agent-runner'): string {
  const pj = join(dd, from, 'node_modules', pkg, 'package.json');
  return (JSON.parse(readFileSync(pj, 'utf8')) as { version: string }).version;
}

const ctx: Ctx = {
  dd,
  corpus,
  testdata,
  N,
  fastify: fastifyFactory.default ?? fastifyFactory,
  providerZod,
  ddRequire,
  rcRequire,
  writer: new Writer(outDir),
};

const parts: Array<[string, (c: Ctx) => Promise<void>]> = [
  ['intent', genIntent],
  ['smartdiff', genSmartDiff],
  ['blast', genBlast],
  ['brief', genBrief],
  ['onboarding', genOnboarding],
  ['performance', genPerformance],
  ['schemas', genSchemas],
  ['routes', genRoutes],
  ['git', genGit],
  ['evals', genEvals],
  ['ci', genCi],
  ['wfrules', genWfRules],
  ['runner', genRunner],
];
const PHASE6_PARTS = ['evals', 'ci', 'wfrules', 'runner'];

// AbortSignal.timeout() timers are unref'd: keep the event loop alive while a budget scenario waits for one.
const keepAlive = setInterval(() => undefined, 1000);
try {
  for (const [name, fn] of parts) {
    if (only && !only.includes(name)) continue;
    await fn(ctx);
  }
  // meta6.json is separate from meta.json so that the Phase 5 goldens stay byte-identical (P15).
  if (!only || PHASE6_PARTS.some((p) => only.includes(p))) {
    ctx.writer.write('meta6.json', {
      note: 'Provenance of the Phase 6 goldens (evals, ci, wfrules, runner). Regenerate with the command in gen/README.md.',
      devDigestCommit: ddHead,
      collationLocale,
      icu: process.versions.icu,
      node: process.version,
      tools: {
        yaml: versionOf('yaml', 'server'),
        'yaml (agent-runner)': versionOf('yaml', 'agent-runner'),
        fflate: versionOf('fflate', 'server'),
        'p-queue': versionOf('p-queue', 'server'),
        zod: versionOf('zod', 'server'),
        'zod (agent-runner)': versionOf('zod', 'agent-runner'),
        openai: versionOf('openai', 'reviewer-core'),
      },
    });
  }
  if (!only) {
    ctx.writer.write('meta.json', {
      note: 'Provenance of the goldens. Regenerate with the command in gen/README.md.',
      devDigestCommit: ddHead,
      collationLocale,
      icu: process.versions.icu,
      node: process.version,
      tools: {
        fastify: versionOf('fastify', 'server'),
        'fastify-type-provider-zod': versionOf('fastify-type-provider-zod', 'server'),
        zod: versionOf('zod', 'server'),
        'zod (reviewer-core)': versionOf('zod', 'reviewer-core'),
        openai: versionOf('openai', 'reviewer-core'),
      },
    });
  }
} finally {
  clearInterval(keepAlive);
  process.chdir(startCwd);
}

const ddHeadAfter = assertCheckoutPristine('after');
if (ddHeadAfter !== ddHead) throw new Error('dev-digest HEAD changed during the run');
console.log(`goldens written to ${relative(startCwd, outDir) || '.'} (dev-digest ${ddHead.slice(0, 7)}, checkout clean before and after)`);
