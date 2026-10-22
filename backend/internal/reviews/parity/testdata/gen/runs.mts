/**
 * runs.json and sse.json — the review run executor driven through its ports with in-memory fakes
 * and a scripted (or mock, or HTTP) LLM, and the SSE route served by Fastify + fastify-sse-v2.
 *
 * Time is a logical clock that only the fake ports move (see lib.mts), so durations and `t` stamps
 * are reproducible. Store writes go through the REAL Node repository functions on a recording
 * drizzle handle (what would be INSERTed/UPDATEd is captured, nothing runs); the SQL CASE logic of
 * failAgentRun/cancelRunIfRunning is emulated in `finalRuns` (marked as such in the README).
 */
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import {
  LogicalClock, deferred, installFakeDate, settleMany,
} from './lib.mts';
import {
  corpusJson, expandReview, expandText, loadPr, recDb, resetFindingCounter, selectDb, type Obj,
} from './common.mts';
import { showRequests, startFakeProvider, type FakeProvider } from './llm.mts';
import type { Ctx } from './ctx.mts';

const dataOf = (v: unknown): unknown => JSON.parse(JSON.stringify(v));
const REVIEW_ID = '11111111-1111-4111-8111-111111111111';

interface HttpResult {
  status: number;
  headers: Obj;
  body: string;
}

export async function genRuns(ctx: Ctx): Promise<void> {
  const spec = corpusJson(ctx, 'runs/cases.json');
  const clock = new LogicalClock();
  const restore = installFakeDate(clock);
  const scenarios: Obj[] = [];
  const streams: Obj[] = [];
  let extras: Obj;
  try {
    for (const c of spec.cases as Obj[]) {
      const r = await runScenario(ctx, clock, spec, c);
      scenarios.push(r.scenario);
      streams.push(...r.streams);
    }
    extras = await genSseExtras(ctx, clock);
  } finally {
    restore();
  }
  ctx.writer.write('runs.json', {
    note: 'ReviewRunExecutor.executeRuns over fake ports. `events` are the bus buffers per run (runId, seq, kind, msg, t, data?) after the executor finished; timings ("(7ms)", duration fields, t) come from the logical clock and are not part of the parity comparison. `calls` are the port calls in order (store writes carry the recorded statements). `finalRuns` emulate the resulting agent_runs rows. `llmCalls` are the completeStructured requests, `promptLog` the prompt-log entries, `traces` the saved run traces (validated against the RunTrace contract).',
    scenarios,
  });
  ctx.writer.write('sse.json', {
    note: 'SSE responses recorded from Fastify + fastify-sse-v2 4.2.2 over a real socket (GET /runs/:id/events). `body` is the exact byte stream decoded as UTF-8; `t` values come from the logical clock (compare with `"t":"HH:MM:SS"` replaced). Headers exclude `date`. `streams` replay or follow the scenarios of runs.json; `synthetic` publishes hand-picked events on a bus; `finalStatus` are runs the bus no longer holds; `errors` are rejections before streaming.',
    streams,
    ...extras,
  });
}

// ---------------------------------------------------------------------------
// One executor scenario
// ---------------------------------------------------------------------------

async function runScenario(ctx: Ctx, clock: LogicalClock, spec: Obj, c: Obj): Promise<{ scenario: Obj; streams: Obj[] }> {
  const { N } = ctx;
  clock.reset();
  const WS: string = spec.workspaceId;
  const pr = loadPr(ctx, c.pr);
  const agents = (c.agents as Obj[]).map((a) => ({ ...spec.agents[a.ref], ...(a.override ?? {}), runId: a.runId as string, ref: a.ref as string }));
  const agentById = new Map<string, Obj>(agents.map((a) => [a.id, a]));
  const execAgent = (a: Obj): Obj => ({
    id: a.id, name: a.name, provider: a.provider, model: a.model, systemPrompt: a.systemPrompt,
    strategy: a.strategy, ciFailOn: a.ciFailOn, repoIntel: a.repoIntel, version: a.version,
  });

  const calls: Obj[] = [];
  const rec = (port: string, args?: unknown, extra: Obj = {}): void => {
    calls.push({ port, ...(args !== undefined ? { args } : {}), ...extra });
  };
  const bus = new N.sse.RunBus({ schedule: () => ({ unref() {} }) });
  const runState = new Map<string, Obj>(agents.map((a) => [a.runId, { status: 'running', error: null }]));
  const traces = new Map<string, Obj>();
  const promptLog: Obj[] = [];
  const llmCalls: Obj[] = [];
  const pendingActions: Array<Promise<unknown>> = [];
  let lastChunk = -1;
  let service: Obj | undefined;

  const fire = async (at: string): Promise<void> => {
    for (const a of (c.actions ?? []) as Obj[]) {
      if (a.at !== at) continue;
      rec('action.cancel', [a.cancel], { at });
      const p = service!.cancelRun(WS, a.cancel);
      pendingActions.push(p);
      await p;
    }
  };

  // ---- store -------------------------------------------------------------
  const applyStats = (st: Obj, set: Obj): void => {
    for (const k of ['durationMs', 'tokensIn', 'tokensOut', 'costUsd', 'findingsCount', 'grounding', 'score', 'blockers']) {
      if (k in set) st[k] = set[k];
    }
  };
  const store: Obj = {
    async storedDiff(prId: string): Promise<Obj> {
      rec('store.storedDiff', [prId]);
      clock.bump(20);
      await fire('diffLoad');
      if (c.storedDiffError) throw new Error(c.storedDiffError);
      return N.pullRepo.storedDiff(selectDb(pr.files), prId);
    },
    async recordRunSkills(runId: string, skills: Obj[]): Promise<void> {
      const ops: Obj[] = [];
      await N.runRepo.recordRunSkills(recDb(ctx, (o) => ops.push(o), () => []), runId, skills);
      rec('store.recordRunSkills', [runId], { statements: ops });
      clock.bump(5);
    },
    async failAgentRun(runId: string, status: string, values: Obj): Promise<string | null> {
      rec('store.failAgentRun', [runId, status, values]);
      clock.bump(5);
      const st = runState.get(runId)!;
      if (st.status === 'done') return null;
      applyStats(st, { score: null, blockers: null, ...values });
      if (st.status === 'running') {
        st.status = status;
        st.error = values.error ?? null;
      } else {
        st.error = st.error ?? 'Cancelled by user';
      }
      return st.status;
    },
    async saveRunTrace(runId: string, trace: Obj): Promise<void> {
      N.shared.RunTrace.parse(trace);
      rec('store.saveRunTrace', [runId]);
      traces.set(runId, trace);
      clock.bump(5);
    },
    async getRunInWorkspace(_ws: string, runId: string): Promise<Obj | undefined> {
      const st = runState.get(runId);
      return st ? { id: runId, status: st.status, error: st.error } : undefined;
    },
    async cancelRunIfRunning(_ws: string, runId: string): Promise<boolean> {
      rec('store.cancelRunIfRunning', [runId]);
      clock.bump(5);
      const st = runState.get(runId)!;
      if (st.status !== 'running') return false;
      st.status = 'cancelled';
      st.error = 'Cancelled by user';
      return true;
    },
  };

  // ---- transaction: real repository functions on a recording handle -----------
  const tx = {
    async run<T>(work: (repos: Obj) => Promise<T>): Promise<T> {
      const ops: Obj[] = [];
      let currentRun: string | null = null;
      let completion: Obj | null = null;
      const db = recDb(
        ctx,
        (op) => ops.push(op),
        (op) => {
          if (op.kind === 'insert' && op.table === 'reviews') return [{ id: REVIEW_ID }];
          if (op.kind === 'insert' && op.table === 'findings') {
            return (op.values as Obj[]).map((v, i) => ({ ...v, id: `f0000000-0000-4000-8000-${String(i + 1).padStart(12, '0')}`, acceptedAt: null, dismissedAt: null }));
          }
          if (op.kind === 'update' && op.table === 'agent_runs') {
            const st = runState.get(currentRun!)!;
            if (c.persist?.completeReturns === false) {
              // A cancel landed between the last checkpoint and the persist.
              st.status = 'cancelled';
              st.error = 'Cancelled by user';
              return [];
            }
            if (st.status !== 'running') return [];
            completion = { runId: currentRun, set: op.set };
            return [{ id: currentRun }];
          }
          return [];
        },
      );
      const writes = {
        insertReview: (v: Obj) => N.reviewRepo.insertReview(db, v),
        insertFindings: (reviewId: string, fs: Obj[], skillIds: Map<string, string>) => N.reviewRepo.insertFindings(db, reviewId, fs, skillIds),
        markReviewed: (prId: string, sha: string) => N.pullRepo.markReviewed(db, prId, sha),
        completeAgentRunIfRunning: (runId: string, values: Obj) => {
          currentRun = runId;
          return N.runRepo.completeAgentRunIfRunning(db, runId, values);
        },
      };
      try {
        const result = await work({ reviews: writes });
        clock.bump(5);
        rec('tx.commit', undefined, { statements: ops });
        const done = completion as Obj | null;
        if (done) {
          const st = runState.get(done.runId)!;
          st.status = 'done';
          st.error = done.set.error ?? null;
          applyStats(st, done.set);
        }
        return result;
      } catch (err) {
        rec('tx.rollback', undefined, { statements: ops, error: (err as Error).message });
        throw err;
      }
    },
  };

  // ---- other ports ------------------------------------------------------------
  const skillsReader = {
    async enabledForAgent(agentId: string): Promise<Obj[]> {
      rec('skills.enabledForAgent', [agentId]);
      clock.bump(2);
      const a = agentById.get(agentId)!;
      if (c.skillsError?.[a.ref]) throw new Error(c.skillsError[a.ref]);
      return ((c.skills?.[a.ref] ?? []) as string[]).map((k) => spec.skills[k]);
    },
  };
  const projectContext = {
    async resolveForRun(input: Obj): Promise<Obj> {
      rec('projectContext.resolveForRun', [{ ...input, onWarn: undefined }]);
      clock.bump(30);
      const cfg: Obj | undefined = c.projectContext?.[agentById.get(input.agentId)!.ref];
      if (cfg?.warn) input.onWarn(cfg.warn);
      if (!cfg || cfg.warn) return { docs: [], included: [], tokensTotal: 0, budgetTokens: 16000, logLine: '' };
      return {
        docs: cfg.docs,
        included: cfg.included,
        tokensTotal: cfg.tokensTotal,
        budgetTokens: 16000,
        logLine: N.projectContext.projectContextLogLine(cfg.docs, cfg.tokensTotal),
      };
    },
  };
  const ri: Obj = c.repoIntel ?? {};
  const repoIntel = {
    async getCallerSignatures(...args: unknown[]): Promise<Obj[]> {
      rec('repoIntel.getCallerSignatures', args);
      clock.bump(10);
      if (ri.callers?.throw) throw new Error(ri.callers.throw);
      return ri.callers ?? [];
    },
    async getRepoMap(...args: unknown[]): Promise<Obj> {
      rec('repoIntel.getRepoMap', args);
      clock.bump(10);
      if (ri.repoMap?.throw) throw new Error(ri.repoMap.throw);
      return ri.repoMap ?? { text: '', tokens: 0, cached: false, degraded: true };
    },
    async getFileRank(...args: unknown[]): Promise<Obj[]> {
      rec('repoIntel.getFileRank', args);
      clock.bump(10);
      if (ri.rank?.throw) throw new Error(ri.rank.throw);
      return ri.rank ?? [];
    },
  };
  const gitPort = {
    async diff(...args: unknown[]): Promise<never> {
      rec('git.diff', args);
      throw new Error('no clone available');
    },
  };

  // ---- LLM --------------------------------------------------------------------
  // Scripted steps are resolved once (finding shorthand expanded, raw text fixed) and recorded that way.
  const resolveStep = (st: Obj): Obj => {
    if (st.error !== undefined) return { error: st.error, usage: st.usage ?? [] };
    const review = expandReviewFor(ctx, st.review);
    return { review, raw: JSON.stringify(review), usage: st.usage ?? [] };
  };
  const L: Obj =
    c.llm.mode === 'script'
      ? {
          ...c.llm,
          ...(c.llm.default ? { default: resolveStep(c.llm.default) } : {}),
          ...(c.llm.chunks ? { chunks: Object.fromEntries(Object.entries(c.llm.chunks as Obj).map(([k, v]) => [k, resolveStep(v as Obj)])) } : {}),
        }
      : c.llm;
  let fake: FakeProvider | undefined;
  if (L.mode === 'http') fake = await startFakeProvider(L.responses, 'm/x');
  const gate = c.sse?.live ? deferred<void>() : null;
  let gateOpen = false;
  const makeProvider = (): Obj => {
    let impl: Obj;
    if (L.mode === 'mock') impl = new N.mock.MockReviewLLMProvider('openrouter', { delayMs: L.delayMs ?? 0 });
    else if (L.mode === 'http') {
      impl = new N.openrouter.OpenRouterProvider('sk-test-key', { baseURL: fake!.baseURL, id: L.provider ?? 'openrouter', maxRetries: 0 });
    } else {
      impl = {
        async completeStructured(req: Obj): Promise<Obj> {
          const step: Obj | undefined = (L.chunks ?? {})[String(lastChunk)] ?? L.default;
          if (!step) throw new Error(`no script step for chunk ${lastChunk}`);
          let tokensIn = 0;
          let tokensOut = 0;
          let costUsd: number | null = 0;
          for (const u of step.usage as Obj[]) {
            req.onUsage?.(u);
            tokensIn += u.tokensIn;
            tokensOut += u.tokensOut;
            costUsd = costUsd === null || u.costUsd === null ? null : costUsd + u.costUsd;
          }
          if (step.error !== undefined) throw new Error(step.error);
          return {
            data: N.shared.Review.parse(step.review),
            model: req.model,
            tokensIn,
            tokensOut,
            costUsd,
            raw: step.raw,
            attempts: Math.max(1, (step.usage as Obj[]).length),
          };
        },
      };
    }
    return {
      id: 'openrouter',
      async completeStructured(req: Obj): Promise<Obj> {
        const idx = llmCalls.length;
        llmCalls.push({
          chunkIndex: lastChunk,
          schemaName: req.schemaName,
          model: req.model,
          ...(req.temperature !== undefined ? { temperature: req.temperature } : {}),
          maxRetries: req.maxRetries,
          ...(req.sessionId ? { sessionId: req.sessionId } : {}),
          messages: dataOf(req.messages),
        });
        clock.bump(1500);
        if (gate && !gateOpen) await gate.promise;
        const p: Promise<Obj> = impl.completeStructured(req);
        p.catch(() => undefined);
        if ((c.actions ?? []).some((a: Obj) => a.at === `llmCall:${idx}`)) {
          await settleMany(3);
          await fire(`llmCall:${idx}`);
        }
        return p;
      },
    };
  };
  const llmResolver = async (provider: string): Promise<Obj> => {
    rec('llm.resolve', [provider]);
    clock.bump(1);
    if (L.resolveError) throw new Error(L.resolveError);
    return makeProvider();
  };

  // ---- executor, service, HTTP ----------------------------------------------------
  const executor = new N.executor.ReviewRunExecutor({
    reviews: store,
    tx,
    runBus: bus,
    llm: llmResolver,
    git: gitPort,
    repoIntel,
    skills: skillsReader,
    clock: () => new Date(),
    ...(c.mapConcurrency !== undefined ? { mapConcurrency: c.mapConcurrency } : {}),
    projectContext,
    promptLog: {
      assembled: (e: Obj) => {
        promptLog.push(dataOf(e) as Obj);
        lastChunk = e.chunk.index;
      },
    },
  });
  service = new N.service.ReviewService({
    reviews: store,
    agents: { listEnabled: async () => [], getById: async () => undefined, namesByIds: async () => new Map() },
    runBus: bus,
    executor,
    clock: () => new Date(),
  });
  const multiAgent = new N.multiAgentService.MultiAgentService({ store: {}, tx: { run: async () => undefined } });

  const streams: Obj[] = [];
  const needHttp = Boolean(c.sse);
  const server = needHttp ? await startRoutes(ctx, WS, service, multiAgent) : null;
  const sseTarget = (runId: string): string => `/runs/${runId}/events`;

  let livePromise: Promise<HttpResult> | null = null;
  const jobs = agents.map((a) => ({ agent: execAgent(a), runId: a.runId }));

  try {
    if (c.sse?.live) {
      livePromise = httpGet(server!.port, sseTarget(c.sse.live));
    }
    const run = executor.executeRuns(WS, pr.pull, pr.repo, jobs, undefined);
    if (c.sse?.live) {
      await waitForListener(bus, c.sse.live);
      gateOpen = true;
      gate!.resolve();
    }
    await run;
    await Promise.all(pendingActions);
    await settleMany(5);

    if (livePromise) {
      const res = await livePromise;
      streams.push({ scenario: c.name, runId: c.sse.live, mode: 'live', request: { method: 'GET', path: sseTarget(c.sse.live) }, response: shown(res) });
    }
    for (const runId of (c.sse?.replay ?? []) as string[]) {
      const res = await httpGet(server!.port, sseTarget(runId));
      streams.push({ scenario: c.name, runId, mode: 'replay', request: { method: 'GET', path: sseTarget(runId) }, response: shown(res) });
    }
  } finally {
    await server?.close();
    await fake?.close();
  }

  const events: Obj = {};
  const busComplete: Obj = {};
  for (const a of agents) {
    events[a.runId] = bus.buffer(a.runId).map((e: Obj) => ({ ...e }));
    busComplete[a.runId] = bus.isComplete(a.runId);
  }
  const strict = N.rc.toJsonSchema(N.shared.Review, 'Review').schema;
  const scenario: Obj = {
    name: c.name,
    description: c.description ?? null,
    input: {
      workspaceId: WS,
      pull: pr.pull,
      repo: pr.repo,
      prFiles: pr.files,
      jobs: jobs.map((j) => ({ runId: j.runId, agent: j.agent })),
      skills: c.skills ? Object.fromEntries(Object.entries(c.skills as Obj).map(([k, v]) => [k, (v as string[]).map((n) => spec.skills[n])])) : {},
      projectContext: c.projectContext ?? {},
      repoIntel: c.repoIntel ?? {},
      llm: L,
      mapConcurrency: c.mapConcurrency ?? null,
      actions: c.actions ?? [],
      persist: c.persist ?? {},
      storedDiffError: c.storedDiffError ?? null,
      skillsError: c.skillsError ?? {},
    },
    events,
    busComplete,
    calls,
    finalRuns: Object.fromEntries([...runState.entries()].map(([k, v]) => [k, v])),
    traces: Object.fromEntries([...traces.entries()]),
    llmCalls,
    promptLog,
    ...(fake ? { llmHttpRequests: showRequests(fake.requests, strict) } : {}),
  };
  return { scenario, streams };
}

/** Finding shorthand → the model's Review JSON; ids restart at f1 for every review. */
function expandReviewFor(_ctx: Ctx, r: Obj): Obj {
  resetFindingCounter();
  return expandReview(r);
}

// ---------------------------------------------------------------------------
// Fastify app with the real reviews routes
// ---------------------------------------------------------------------------

interface Routes {
  port: number;
  close(): Promise<void>;
}

async function startRoutes(ctx: Ctx, workspaceId: string, service: Obj, multiAgent: Obj): Promise<Routes> {
  const { N } = ctx;
  const app = ctx.fastify({ logger: false });
  app.setValidatorCompiler(ctx.providerZod.validatorCompiler);
  app.setSerializerCompiler(ctx.providerZod.serializerCompiler);
  app.decorate('container', {
    auth: { currentUser: async () => ({ id: 'u1' }), currentWorkspace: async () => ({ id: workspaceId }) },
    modules: { reviews: { service, multiAgent } },
  });
  await app.register(ctx.sseV2.FastifySSEPlugin);
  N.errorHandler.registerErrorHandling(app);
  await app.register(N.routes.default);
  await app.listen({ port: 0, host: '127.0.0.1' });
  const port = (app.server.address() as AddressInfo).port;
  return { port, close: () => app.close() };
}

function httpGet(port: number, path: string): Promise<HttpResult> {
  return new Promise((resolve, reject) => {
    const req = http.get({ host: '127.0.0.1', port, path, agent: false, headers: { accept: 'text/event-stream' } }, (res) => {
      const chunks: Buffer[] = [];
      res.on('data', (d: Buffer) => chunks.push(d));
      res.on('end', () => resolve({ status: res.statusCode ?? 0, headers: res.headers as Obj, body: Buffer.concat(chunks).toString('utf8') }));
      res.on('error', reject);
    });
    req.on('error', reject);
  });
}

function shown(res: HttpResult): Obj {
  const headers: Obj = {};
  for (const [k, v] of Object.entries(res.headers)) if (k !== 'date') headers[k] = v;
  return { status: res.status, headers, body: res.body };
}

/** Poll until a stream is subscribed to the run's bus emitter (real short sleeps; no model time). */
async function waitForListener(bus: Obj, runId: string): Promise<void> {
  for (let i = 0; i < 400; i++) {
    const em = bus.emitters.get(runId);
    if (em && em.listenerCount('event') > 0) return;
    await new Promise((r) => setTimeout(r, 5));
  }
  throw new Error(`no SSE subscriber appeared for run ${runId}`);
}

// ---------------------------------------------------------------------------
// SSE extras: synthetic buses, final-status events, rejections
// ---------------------------------------------------------------------------

async function genSseExtras(ctx: Ctx, clock: LogicalClock): Promise<Obj> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'sse/cases.json'));
  const rawCases = new Map<string, Obj>(((corpusJson(ctx, 'sse/cases.json').synthetic as Obj[]) ?? []).map((c) => [c.name as string, c]));
  const WS = '0b6f2d1a-7c34-4e0a-8a55-00000000w001';
  clock.reset();
  const bus = new N.sse.RunBus({ schedule: () => ({ unref() {} }) });
  const states = new Map<string, Obj>();
  const store = {
    getRunInWorkspace: async (_w: string, id: string) => (states.has(id) ? { id, ...states.get(id) } : undefined),
  };
  const service = new N.service.ReviewService({
    reviews: store,
    agents: { listEnabled: async () => [], getById: async () => undefined, namesByIds: async () => new Map() },
    runBus: bus,
    executor: { executeRuns: async () => undefined },
    clock: () => new Date(),
  });
  const multiAgent = new N.multiAgentService.MultiAgentService({ store: {}, tx: { run: async () => undefined } });
  const server = await startRoutes(ctx, WS, service, multiAgent);
  const synthetic: Obj[] = [];
  const finalStatus: Obj[] = [];
  const errors: Obj[] = [];
  try {
    for (const c of spec.synthetic as Obj[]) {
      states.set(c.runId, { status: c.dbStatus ?? 'done', error: null });
      const path = `/runs/${c.runId}/events`;
      let pending: Promise<HttpResult> | null = null;
      if (c.connectFirst) {
        pending = httpGet(server.port, path);
        await waitForHas(bus, c.runId);
      }
      for (const e of c.events as Obj[]) {
        clock.bump(400);
        bus.publish(c.runId, e.kind, e.msg, e.data);
      }
      if (c.complete !== false) bus.complete(c.runId);
      const res = await (pending ?? httpGet(server.port, path));
      synthetic.push({ name: c.name, runId: c.runId, connectFirst: Boolean(c.connectFirst), events: rawCases.get(c.name)!.events, escapingSensitive: Boolean(c.escapingSensitive), request: { method: 'GET', path }, response: shown(res) });
    }
    for (const c of spec.finalStatus as Obj[]) {
      states.set(c.runId, { status: c.status, error: c.error });
      clock.bump(1000);
      const path = `/runs/${c.runId}/events`;
      const res = await httpGet(server.port, path);
      finalStatus.push({ name: c.name, runId: c.runId, dbStatus: c.status, dbError: c.error, request: { method: 'GET', path }, response: shown(res) });
    }
    for (const c of spec.errors as Obj[]) {
      const res = await httpGet(server.port, c.path);
      errors.push({ name: c.name, request: { method: 'GET', path: c.path }, response: shown(res) });
    }
  } finally {
    await server.close();
  }
  return { synthetic, finalStatus, errors };
}

async function waitForHas(bus: Obj, runId: string): Promise<void> {
  for (let i = 0; i < 400; i++) {
    const em = bus.emitters.get(runId);
    if (em && em.listenerCount('event') > 0) return;
    await new Promise((r) => setTimeout(r, 5));
  }
  throw new Error(`no SSE subscriber appeared for run ${runId}`);
}
