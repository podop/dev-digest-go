/**
 * intent.json — the review run executor with the intent hook SWITCHED ON (REVIEW_INTENT_ENABLED), i.e. the
 * Node `ReviewRunExecutor` wired with the real `IntentService` (the production wiring of
 * reviews/composition.ts), driven through in-memory fakes and a scripted (or mock) LLM. runs.json (Phase 4)
 * is the switch-off case and is not touched; this file only adds scenarios (spec D5017, plan S22).
 *
 * What is real: ReviewRunExecutor, ReviewService.cancelRun, the RunBus, reviewer-core (prompt, scope policy,
 * `scope:` event), IntentService (cache key, single flight, budget, sources, clamps, events), LlmIntentModel
 * (the classification request), IntentRepository (the pr_intent statements, on a recording drizzle handle),
 * the review repository functions (finding INSERT values incl. `outOfScope`), the mock provider.
 * What is faked: settings (feature model), GitHub tickets, git/GitHub docs, the commit lookup, the providers.
 *
 * Time is the logical clock of lib.mts (the global Date); the intent budget timer (120 s, a real setTimeout in
 * Node) is shortened to 20 ms in the one scenario that exercises it (`intent.shortBudget`).
 */
import { createHash } from 'node:crypto';
import {
  LogicalClock, installFakeDate, settleMany,
} from './lib.mts';
import {
  corpusJson, expandReview, loadPr, recDb, resetFindingCounter, selectDb, type Obj,
} from './common.mts';
import type { Ctx } from './ctx.mts';

const dataOf = (v: unknown): unknown => JSON.parse(JSON.stringify(v));
const REVIEW_ID = '11111111-1111-4111-8111-111111111111';
const INTENT_BUDGET_MS = 120_000;
const STALE_HASH = 'f'.repeat(64);

export async function genIntent(ctx: Ctx): Promise<void> {
  const spec = corpusJson(ctx, 'intent/cases.json');
  const base = corpusJson(ctx, 'runs/cases.json'); // agents and skills are shared with the Phase 4 corpus
  const clock = new LogicalClock();
  const restore = installFakeDate(clock);
  const scenarios: Obj[] = [];
  try {
    for (const c of spec.cases as Obj[]) scenarios.push(await runScenario(ctx, clock, spec, base, c));
  } finally {
    restore();
  }
  ctx.writer.write('intent.json', {
    note: 'ReviewRunExecutor with the intent hook ON: the real IntentService behind the executor (reviews/composition.ts wiring) over fake ports, scripted or mock LLMs. Same layout as runs.json (`events`, `calls`, `finalRuns`, `traces`, `llmCalls`, `promptLog`, same normalisation rules: drop `t` and `(<n>ms)`, ignore duration fields) plus `resolve` (what the executor handed to the IntentResolver and what came back: the replay input for the Go executor test), `intentCalls` (the intent service ports in order), `intentLlmCalls` (the classification requests), `intentPromptLog` and `intentStore`. Phase 4 goldens are the switch-off case and stay unchanged.',
    runtime: { node: process.version },
    scenarios,
  });
}

/** Combines the corpus agent definitions with the per-case ref/runId. */
function agentsOf(spec: Obj, base: Obj, c: Obj): Obj[] {
  return (c.agents as Obj[]).map((a) => ({ ...base.agents[a.ref], ...(a.override ?? {}), runId: a.runId as string, ref: a.ref as string }));
}

async function runScenario(ctx: Ctx, clock: LogicalClock, spec: Obj, base: Obj, c: Obj): Promise<Obj> {
  const { N } = ctx;
  clock.reset();
  const WS: string = base.workspaceId;
  const loaded = loadPr(ctx, c.pr);
  const pr = { ...loaded, pull: { ...loaded.pull, ...(c.pull ?? {}) } };
  const agents = agentsOf(spec, base, c);
  const agentById = new Map<string, Obj>(agents.map((a) => [a.id, a]));
  const execAgent = (a: Obj): Obj => ({
    id: a.id, name: a.name, provider: a.provider, model: a.model, systemPrompt: a.systemPrompt,
    strategy: a.strategy, ciFailOn: a.ciFailOn, repoIntel: a.repoIntel, version: a.version,
  });
  const I: Obj = c.intent;

  const calls: Obj[] = [];
  const rec = (port: string, args?: unknown, extra: Obj = {}): void => {
    calls.push({ port, ...(args !== undefined ? { args } : {}), ...extra });
  };
  const intentCalls: Obj[] = [];
  const irec = (port: string, args?: unknown, extra: Obj = {}): void => {
    intentCalls.push({ port, ...(args !== undefined ? { args } : {}), ...extra });
  };
  const bus = new N.sse.RunBus({ schedule: () => ({ unref() {} }) });
  const runState = new Map<string, Obj>(agents.map((a) => [a.runId, { status: 'running', error: null }]));
  const traces = new Map<string, Obj>();
  const promptLog: Obj[] = [];
  const intentPromptLog: Obj[] = [];
  const llmCalls: Obj[] = [];
  const intentLlmCalls: Obj[] = [];
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

  // ---- review store (as runs.mts) ---------------------------------------------
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
  const skillsReader = {
    async enabledForAgent(agentId: string): Promise<Obj[]> {
      rec('skills.enabledForAgent', [agentId]);
      clock.bump(2);
      const a = agentById.get(agentId)!;
      return ((c.skills?.[a.ref] ?? []) as string[]).map((k) => base.skills[k]);
    },
  };
  const projectContext = {
    async resolveForRun(input: Obj): Promise<Obj> {
      rec('projectContext.resolveForRun', [{ ...input, onWarn: undefined }]);
      clock.bump(30);
      return { docs: [], included: [], tokensTotal: 0, budgetTokens: 16000, logLine: '' };
    },
  };
  const ri: Obj = c.repoIntel ?? {};
  const repoIntel = {
    async getCallerSignatures(...args: unknown[]): Promise<Obj[]> {
      rec('repoIntel.getCallerSignatures', args);
      clock.bump(10);
      return ri.callers ?? [];
    },
    async getRepoMap(...args: unknown[]): Promise<Obj> {
      rec('repoIntel.getRepoMap', args);
      clock.bump(10);
      return ri.repoMap ?? { text: '', tokens: 0, cached: false, degraded: true };
    },
    async getFileRank(...args: unknown[]): Promise<Obj[]> {
      rec('repoIntel.getFileRank', args);
      clock.bump(10);
      return ri.rank ?? [];
    },
  };
  const gitPort = {
    async diff(...args: unknown[]): Promise<never> {
      rec('git.diff', args);
      throw new Error('no clone available');
    },
  };

  // ---- the review LLM (script or mock, as runs.mts) -----------------------------
  const resolveStep = (st: Obj): Obj => {
    if (st.error !== undefined) return { error: st.error, usage: st.usage ?? [] };
    resetFindingCounter();
    const review = expandReview(st.review);
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
  const makeReviewProvider = (): Obj => {
    let impl: Obj;
    if (L.mode === 'mock') impl = new N.mock.MockReviewLLMProvider('openrouter', { delayMs: L.delayMs ?? 0 });
    else {
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
    return makeReviewProvider();
  };

  // ---- the intent service (real) over fake ports -------------------------------
  const intentModelChoice: Obj = I.model ?? { provider: 'openrouter', model: 'deepseek/deepseek-v4-flash' };
  const intentLlm: Obj = I.llm;
  const intentProvider = (provider: string): Obj => ({
    id: provider,
    async completeStructured(req: Obj): Promise<Obj> {
      const idx = intentLlmCalls.length;
      intentLlmCalls.push({
        schemaName: req.schemaName,
        model: req.model,
        ...(req.temperature !== undefined ? { temperature: req.temperature } : {}),
        ...(req.maxTokens !== undefined ? { maxTokens: req.maxTokens } : {}),
        maxRetries: req.maxRetries,
        hasSignal: Boolean(req.signal),
        messages: dataOf(req.messages),
      });
      clock.bump(1500);
      if ((c.actions ?? []).some((a: Obj) => a.at === `intentCall:${idx}`)) {
        await settleMany(3);
        await fire(`intentCall:${idx}`);
      }
      if (intentLlm.mode === 'error') throw new Error(intentLlm.error);
      if (intentLlm.mode === 'hang') {
        await new Promise<never>((_resolve, reject) => {
          const abort = (): void => reject(new Error('Request was aborted.'));
          if (req.signal.aborted) abort();
          else req.signal.addEventListener('abort', abort, { once: true });
        });
      }
      if (intentLlm.mode === 'mock') {
        return new N.mock.MockReviewLLMProvider(provider, { delayMs: intentLlm.delayMs ?? 0 }).completeStructured(req);
      }
      const u: Obj = intentLlm.usage;
      return {
        data: req.schema.parse(intentLlm.result),
        model: req.model,
        tokensIn: u.tokensIn,
        tokensOut: u.tokensOut,
        costUsd: u.costUsd,
        raw: JSON.stringify(intentLlm.result),
        attempts: 1,
      };
    },
  });
  const model = new N.intentModel.LlmIntentModel({
    resolveModel: async (workspaceId: string) => {
      irec('settings.resolveFeatureModel', [workspaceId, 'review_intent']);
      clock.bump(1);
      if (I.modelError) throw new Error(I.modelError);
      return intentModelChoice;
    },
    llm: async (provider: string) => {
      irec('llm.resolve', [provider]);
      clock.bump(1);
      if (intentLlm.mode === 'keyMissing') throw new Error(intentLlm.error);
      return intentProvider(provider);
    },
  });

  // pr_intent: the real IntentRepository on a recording handle holding at most the one row of this PR.
  const intentRows = new Map<string, Obj>();
  let intentOps: Obj[] = [];
  const intentDb = recDb(
    ctx,
    (op) => intentOps.push(op),
    (op) => {
      if (op.table !== 'pr_intent') return [];
      if (op.kind === 'insert') {
        const row = { ...(op.values as Obj) };
        intentRows.set(row.prId, row);
        return [row];
      }
      return [...intentRows.values()];
    },
  );
  const intentRepo = new N.intentRepo.IntentRepository(intentDb);
  const intentStore = {
    async get(prId: string): Promise<unknown> {
      intentOps = [];
      const out = await intentRepo.get(prId);
      irec('store.get', [prId], { found: out !== undefined });
      return out;
    },
    async upsert(record: Obj): Promise<unknown> {
      intentOps = [];
      const out = await intentRepo.upsert(record);
      irec('store.upsert', [{ prId: record.prId, inputHash: record.inputHash }], { statements: intentOps });
      clock.bump(5);
      return out;
    },
  };
  const pulls = {
    async getPull(): Promise<never> { throw new Error('getPull is not used by resolveForReview'); },
    async getRepo(): Promise<never> { throw new Error('getRepo is not used by resolveForReview'); },
    async getChangedFiles(): Promise<never> { throw new Error('getChangedFiles is not used by resolveForReview'); },
    async getCommits(prId: string): Promise<Obj[]> {
      irec('pulls.getCommits', [prId]);
      return I.commits ?? [];
    },
  };
  const tickets = {
    async read(repo: Obj, number: number): Promise<Obj> {
      irec('tickets.read', [`${repo.owner}/${repo.name}`, number]);
      clock.bump(7);
      return I.tickets?.[String(number)] ?? { ok: false, reason: 'issue not found' };
    },
  };
  const docs = {
    async read(repo: Obj, headSha: string, path: string): Promise<Obj> {
      irec('docs.read', [`${repo.owner}/${repo.name}`, headSha, path]);
      clock.bump(7);
      return I.docs?.[path] ?? { ok: false, reason: 'file not found' };
    },
  };
  const intentService = new N.intentService.IntentService({
    pulls,
    store: intentStore,
    model,
    docs,
    tickets,
    clock: () => new Date(),
    promptLog: { assembled: (e: Obj) => intentPromptLog.push(dataOf(e) as Obj) },
  });

  // Pre-seeded cache row: its hash is the key the service computes for this PR now (or a stale one).
  let preseeded: Obj | null = null;
  if (I.preseed) {
    const key = await intentService.computeKey(WS, pr.pull, pr.repo);
    const p: Obj = I.preseed;
    const row = {
      prId: pr.pull.id,
      intent: p.row.intent,
      inScope: p.row.in_scope,
      outOfScope: p.row.out_of_scope,
      changeType: p.row.change_type,
      confidence: p.row.confidence,
      derivedFrom: p.row.derived_from,
      sources: p.row.sources,
      headSha: pr.pull.headSha,
      inputHash: p.state === 'stale' ? STALE_HASH : key.hash,
      promptVersion: key.cacheInput.prompt_version,
      provider: p.row.provider,
      model: p.row.model,
      tokensIn: p.row.tokens_in,
      tokensOut: p.row.tokens_out,
      costUsd: p.row.cost_usd,
      derivedAt: new Date(p.row.derived_at),
    };
    intentRows.set(row.prId, row);
    preseeded = { ...row, inputHash: row.inputHash };
    intentCalls.length = 0;
    clock.reset();
  }

  // The resolver the executor sees: the production adapter, plus a recording of what crossed the port.
  const resolve: Obj = { calls: [] };
  const resolver = {
    async resolveForReview(input: Obj): Promise<Obj> {
      const entry: Obj = {
        input: {
          workspaceId: input.workspaceId,
          pull: input.pull,
          repo: input.repo,
          changedFiles: input.changedFiles,
          diffExcerptLen: (input.diffExcerpt as string).length,
          diffExcerptSha256: createHash('sha256').update(input.diffExcerpt as string, 'utf8').digest('hex'),
          force: input.force ?? false,
          hasSignal: Boolean(input.signal),
        },
        events: [] as Obj[],
      };
      resolve.calls.push(entry);
      const result = await intentService.resolveForReview({
        ...input,
        onEvent: (e: Obj) => {
          entry.events.push({ kind: e.kind, msg: e.msg, ...(e.data !== undefined ? { data: e.data } : {}) });
          input.onEvent?.(e);
        },
      });
      entry.signalAbortedAtReturn = input.signal?.aborted ?? false;
      entry.result =
        result.status === 'used'
          ? { status: 'used', intent: result.intent, trace: result.trace }
          : { status: 'unavailable', warning: result.warning };
      return result;
    },
  };

  // ---- executor, service ----------------------------------------------------------
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
    intent: resolver,
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
  const jobs = agents.map((a) => ({ agent: execAgent(a), runId: a.runId }));

  // Budget scenario: the service's 120 s timer fires after 20 ms of real time instead.
  const realSetTimeout = globalThis.setTimeout;
  if (I.shortBudget) {
    globalThis.setTimeout = ((fn: () => void, ms?: number, ...rest: unknown[]) =>
      realSetTimeout(fn, ms === INTENT_BUDGET_MS ? 20 : ms, ...rest)) as typeof setTimeout;
  }
  try {
    const run = executor.executeRuns(WS, pr.pull, pr.repo, jobs, undefined);
    await run;
    await Promise.all(pendingActions);
    await settleMany(5);
  } finally {
    globalThis.setTimeout = realSetTimeout;
  }

  const events: Obj = {};
  const busComplete: Obj = {};
  for (const a of agents) {
    events[a.runId] = bus.buffer(a.runId).map((e: Obj) => ({ ...e }));
    busComplete[a.runId] = bus.isComplete(a.runId);
  }
  return {
    name: c.name,
    description: c.description ?? null,
    input: {
      workspaceId: WS,
      pull: pr.pull,
      repo: pr.repo,
      prFiles: pr.files,
      jobs: jobs.map((j) => ({ runId: j.runId, agent: j.agent })),
      skills: c.skills ? Object.fromEntries(Object.entries(c.skills as Obj).map(([k, v]) => [k, (v as string[]).map((n) => base.skills[n])])) : {},
      repoIntel: c.repoIntel ?? {},
      llm: L,
      mapConcurrency: c.mapConcurrency ?? null,
      actions: c.actions ?? [],
      storedDiffError: c.storedDiffError ?? null,
      intent: { ...I, ...(preseeded ? { preseed: { ...I.preseed, inputHash: preseeded.inputHash } } : {}) },
    },
    resolve,
    events,
    busComplete,
    calls,
    finalRuns: Object.fromEntries([...runState.entries()].map(([k, v]) => [k, v])),
    traces: Object.fromEntries([...traces.entries()]),
    llmCalls,
    promptLog,
    intentCalls,
    intentLlmCalls,
    intentPromptLog,
    intentStore: { row: intentRows.get(pr.pull.id) ?? null },
  };
}
