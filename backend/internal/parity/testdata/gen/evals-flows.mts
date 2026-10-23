/**
 * The service-level parts of evals.json: request-schema checks, `EvalCasesService`, `EvalRunsService` and the
 * background `EvalRunExecutor`, all real Node code over in-memory fakes of their ports (see ./README.md).
 * Anything SQL is emulated here and says so in the golden's note: ordering and filtering of the dashboard reads.
 */
import { corpusJson, expandText, type Obj } from './common.mts';
import { attempt, attemptAsync, merge, recLog, showObj, showParse } from './common6.mts';
import type { Ctx } from './ctx.mts';
import { deferred, settleMany, textOf } from './lib.mts';

type Helpers = {
  resolve: (v: unknown) => unknown;
  load: (rel: string) => Obj;
  parse: (raw: string) => unknown;
  check: (diff: string, expectation: Obj) => Obj;
};

const WS = 'ws-1';
const ISO0 = '2026-01-01T00:00:00.000Z';

export async function genEvalFlows(ctx: Ctx, h: Helpers): Promise<Obj> {
  const { N } = ctx;
  const flows = h.load('evals/flows.json');
  const domain = h.load('evals/domain.json');
  const content = h.load('evals/content.json');
  const edit = h.load('evals/edit.json');
  const cmp = h.load('evals/compare.json');
  const diffText = (name: string): string => {
    const spec = (content.diffs as Obj)[name];
    if (!spec) throw new Error(`unknown corpus diff ${name}`);
    return textOf(spec);
  };

  // ---- request schemas ----
  const validation = (flows.validation as Obj[]).map((v) => ({
    schema: v.schema,
    cases: (v.inputs as unknown[]).map((input) => ({ input, ...showParse(N.shared[v.schema as string].safeParse(input)) })),
  }));

  const executorSkills = (flows.executor.skills as Obj[]);
  const subst = (skills: unknown[] | undefined): unknown[] | undefined =>
    skills?.flatMap((s) => (s === '$skills' ? executorSkills : s === '$skill1' ? [executorSkills[0]] : s === '$skill2' ? [executorSkills[1]] : [s]));

  // ---- EvalCasesService ----
  const casesService = await Promise.all((flows.casesService as Obj[]).map(async (sc) => {
    const world = sc.world as Obj;
    const calls: Obj[] = [];
    let seq = 0;
    const toCase = (v: Obj): Obj => ({
      id: `case-${++seq}`,
      owner_kind: v.ownerKind,
      owner_id: v.ownerId,
      agent_id: v.ownerKind === 'agent' ? v.ownerId : null,
      name: v.name,
      notes: v.notes,
      expectation: v.expectation,
      input_diff: v.inputDiff,
      input_meta: v.inputMeta,
      source_finding_id: v.sourceFindingId,
      source_pr_id: v.sourcePrId,
      origin: v.sourceFindingId ? 'finding' : 'manual',
      content_edited_at: null,
      created_at: ISO0,
      last_result: null,
    });
    const key = (o: Obj): string => `${o.kind}:${o.id}`;
    const store = {
      findingSource: async (_w: string, id: string) => {
        calls.push({ fn: 'findingSource', id });
        const f = world.findings?.[id];
        return f === undefined ? undefined : merge(domain.findings.base, f);
      },
      caseIdForFinding: async (_w: string, id: string) => {
        calls.push({ fn: 'caseIdForFinding', id });
        return world.casesByFinding?.[id];
      },
      count: async (_w: string, owner: Obj) => {
        calls.push({ fn: 'count', owner: key(owner) });
        return world.counts?.[key(owner)] ?? 0;
      },
      insert: async (values: Obj) => {
        calls.push({ fn: 'insert', values });
        if (world.insertRace) return { kind: 'exists', caseId: 'case-winner' };
        return { kind: 'created', case: toCase(values) };
      },
      list: async (_w: string, owner: Obj) => {
        calls.push({ fn: 'list', owner: key(owner) });
        return world.lists?.[key(owner)] ?? [];
      },
      find: async () => undefined,
      lockForUpdate: async (_w: string, id: string) => {
        calls.push({ fn: 'lockForUpdate', id });
        return world.stored?.[id] === 'stored' ? structuredClone(edit.stored) : undefined;
      },
      update: async (_w: string, id: string, values: Obj) => {
        calls.push({ fn: 'update', id, values });
        return { id, applied: values };
      },
      delete: async (_w: string, id: string) => {
        calls.push({ fn: 'delete', id });
        return world.stored?.[id] === 'stored';
      },
    };
    const svc = new N.evCasesService.EvalCasesService({
      cases: store,
      tx: { run: async (fn: (s: Obj) => Promise<unknown>) => { calls.push({ fn: 'tx' }); return fn({ cases: store }); } },
      agents: {
        exists: async (_w: string, id: string) => {
          calls.push({ fn: 'agents.exists', id });
          return (world.agents ?? []).includes(id);
        },
        listIds: async () => world.agents ?? [],
      },
      skills: {
        get: async (_w: string, id: string) => {
          calls.push({ fn: 'skills.get', id });
          return (world.skills ?? []).includes(id) ? { id, name: id, description: '', body: '', version: 1 } : undefined;
        },
      },
      parseDiff: h.parse,
    });
    const steps: Obj[] = [];
    for (const step of sc.steps as Obj[]) {
      const log = recLog();
      const before = calls.length;
      let outcome: Obj;
      if (step.op === 'createFromFinding') {
        outcome = await attemptAsync(() => svc.createFromFinding(WS, step.findingId, log));
      } else if (step.op === 'create') {
        const parsed = N.shared.EvalCaseCreateInput.safeParse(step.body);
        outcome = parsed.success ? await attemptAsync(() => svc.create(WS, step.owner, parsed.data, log)) : { parse: showParse(parsed) };
      } else if (step.op === 'list') {
        outcome = await attemptAsync(() => svc.list(WS, step.owner));
      } else if (step.op === 'update') {
        const parsed = N.shared.AgentEvalCaseUpdateInput.safeParse(step.patch);
        outcome = parsed.success ? await attemptAsync(() => svc.update(WS, step.id, parsed.data)) : { parse: showParse(parsed) };
      } else if (step.op === 'remove') {
        outcome = await attemptAsync(async () => { await svc.remove(WS, step.id); return { removed: true }; });
      } else throw new Error(`unknown cases-service op ${step.op}`);
      steps.push({ ...step, ...outcome, calls: calls.slice(before), logs: log.lines });
    }
    return { name: sc.name, world, steps };
  }));

  // ---- EvalRunsService ----
  const runBase = cmp.runBase as Obj;
  const agentBase = (id: string): Obj => ({
    version: 3,
    name: id,
    config: { provider: 'openrouter', model: 'deepseek/deepseek-v4-flash', system_prompt: 'You are a reviewer.', strategy: 'auto', skills: [] },
  });
  const expandRuns = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(expandRuns);
    if (v !== null && typeof v === 'object') {
      const o = v as Obj;
      if (o.$runs) {
        const r = o.$runs as Obj;
        return Array.from({ length: r.n as number }, (_, k) => {
          const i = k + 1;
          const started = new Date(Date.parse(r.startAt) + k * r.stepHours * 3_600_000);
          const recall = i === 1 && r.latestRecall !== undefined ? r.latestRecall : i === 2 && r.previousRecall !== undefined ? r.previousRecall : Math.round((0.5 + 0.003 * (r.n - i)) * 1e4) / 1e4;
          const run = merge(runBase, {
            id: `${r.agent}-r${i}`,
            agent_id: r.agent,
            started_at: started.toISOString(),
            finished_at: new Date(started.getTime() + 300_000).toISOString(),
            recall,
            precision: Math.round((0.6 + 0.002 * (i % 7)) * 1e4) / 1e4,
          });
          return r.withAgentName ? { ...run, agent_name: r.withAgentName } : run;
        });
      }
      return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, expandRuns(x)]));
    }
    return v;
  };

  const runsService = await Promise.all((flows.runsService as Obj[]).map(async (sc) => {
    const world = expandRuns(sc.world) as Obj;
    const calls: Obj[] = [];
    const launched: Obj[] = [];
    const shutdowns: number[] = [];
    let seq = 0;
    const nowMs = Date.parse(world.now ?? '2026-03-31T12:00:00.000Z');
    const snap = (id: string): Obj | undefined => {
      if (!world.agents || !(id in world.agents) || world.vanish?.includes(id)) return undefined;
      const a = merge(agentBase(id), world.agents[id]);
      const skills = subst(a.config.skills);
      if (skills) a.config.skills = skills;
      return a;
    };
    const skillOf = (id: string): Obj | undefined =>
      world.skills && id in world.skills ? merge({ id, name: id, description: 'd', body: 'b', version: 1 }, world.skills[id]) : undefined;
    const ownerKey = (o: Obj): string => `${o.kind}:${o.id}`;
    const finishedOf = (agentId: string): Obj[] => (world.finished?.[agentId] as Obj[] | undefined) ?? [];
    const store = {
      caseIds: async (_w: string, owner: Obj) => {
        calls.push({ fn: 'caseIds', owner: ownerKey(owner) });
        return world.caseIds?.[ownerKey(owner)] ?? [];
      },
      insertRun: async (values: Obj) => {
        calls.push({ fn: 'insertRun', values });
        const k = values.ownerKind === 'skill' ? `skill:${values.skillId}` : `agent:${values.agentId}`;
        if (world.active?.includes(k)) return { kind: 'active' };
        return {
          kind: 'created',
          run: merge(runBase, {
            id: `run-${++seq}`,
            owner_kind: values.ownerKind,
            skill_id: values.skillId,
            skill_version: values.skillVersion,
            agent_id: values.agentId,
            agent_version: values.agentVersion,
            agent_name: values.agentName,
            model: values.config.model,
            status: 'running',
            started_at: new Date(nowMs).toISOString(),
            finished_at: null,
            cases_total: values.caseIds.length,
            cases_finished: 0,
            cases_passed: 0,
            cases_errored: 0,
            recall: null,
            precision: null,
            citation_accuracy: null,
            unlabeled_findings: 0,
            cost_usd: null,
            duration_ms: null,
          }),
        };
      },
      reapRunning: async (error: string) => {
        calls.push({ fn: 'reapRunning', error });
        return world.reaped ?? 0;
      },
      listRuns: async (_w: string, owner: Obj, limit: number, since?: Date) => {
        calls.push({ fn: 'listRuns', owner: ownerKey(owner), limit, since: since ? since.toISOString() : null });
        return [];
      },
      getRun: async (_w: string, id: string) => world.details?.[id],
      compareSide: async (_w: string, id: string) => {
        const s = world.sides?.[id];
        return s && { run: merge(runBase, s.run), config: merge(cmp.configBase, s.config), results: s.results };
      },
      // Emulated SQL: finished = status != running, newest first as given, `since` keeps started_at >= since.
      finishedRuns: async (_w: string, agentId: string, limit: number, since?: Date) => {
        calls.push({ fn: 'finishedRuns', agentId, limit, since: since ? since.toISOString() : null });
        return finishedOf(agentId)
          .filter((r) => r.status !== 'running' && (!since || Date.parse(r.started_at) >= since.getTime()))
          .slice(0, limit);
      },
      finishedRunsByAgent: async (_w: string, agentIds: string[], perAgent: number) => {
        calls.push({ fn: 'finishedRunsByAgent', agentIds, perAgent });
        return new Map(agentIds.filter((id) => finishedOf(id).length > 0).map((id) => [id, finishedOf(id).filter((r) => r.status !== 'running').slice(0, perAgent)]));
      },
      agentsWithEvals: async () => {
        calls.push({ fn: 'agentsWithEvals' });
        return world.overviewAgents ?? [];
      },
      recentRuns: async (_w: string, limit: number) => {
        calls.push({ fn: 'recentRuns', limit });
        return ((world.recent as Obj[]) ?? []).slice(0, limit);
      },
    };
    const svc = new N.evRunsService.EvalRunsService({
      runs: store,
      agents: {
        exists: async (_w: string, id: string) => {
          calls.push({ fn: 'agents.exists', id });
          return snap(id) !== undefined;
        },
        listIds: async () => Object.keys(world.agents ?? {}),
        snapshot: async (_w: string, id: string) => {
          calls.push({ fn: 'agents.snapshot', id });
          return snap(id);
        },
      },
      skills: {
        get: async (_w: string, id: string) => {
          calls.push({ fn: 'skills.get', id });
          return skillOf(id);
        },
      },
      executor: {
        launch: (job: Obj) => void launched.push(job),
        shutdown: async (ms: number) => { shutdowns.push(ms); return true; },
      },
      now: () => nowMs,
    });
    const steps: Obj[] = [];
    for (const step of sc.steps as Obj[]) {
      const before = calls.length;
      const launchedBefore = launched.length;
      let outcome: Obj;
      const log = recLog();
      switch (step.op) {
        case 'start': outcome = await attemptAsync(() => svc.start(WS, step.agentId, log)); break;
        case 'startSkill': outcome = await attemptAsync(() => svc.startSkill(WS, step.skillId, step.hostAgentId, log)); break;
        case 'runAll': outcome = await attemptAsync(() => svc.runAll(WS, log)); break;
        case 'list': outcome = await attemptAsync(() => svc.list(WS, step.agentId, step.limit, step.period)); break;
        case 'listSkillRuns': outcome = await attemptAsync(() => svc.listSkillRuns(WS, step.skillId, step.limit)); break;
        case 'get': outcome = await attemptAsync(() => svc.get(WS, step.id)); break;
        case 'compare': outcome = await attemptAsync(() => svc.compare(WS, step.a, step.b)); break;
        case 'overview': outcome = await attemptAsync(() => svc.overview(WS)); break;
        case 'agentDashboard': outcome = await attemptAsync(() => svc.agentDashboard(WS, step.agentId, step.period)); break;
        case 'reapInterrupted': outcome = await attemptAsync(() => svc.reapInterrupted()); break;
        case 'shutdown': outcome = await attemptAsync(() => (step.ms === undefined ? svc.shutdown() : svc.shutdown(step.ms))); break;
        default: throw new Error(`unknown runs-service op ${step.op}`);
      }
      steps.push({ ...step, ...outcome, calls: calls.slice(before), launched: launched.slice(launchedBefore), shutdowns: step.op === 'shutdown' ? shutdowns.slice(-1) : undefined });
    }
    return { name: sc.name, world, steps };
  }));

  // ---- EvalRunExecutor ----
  const executor = await genExecutor(ctx, flows.executor as Obj, diffText, subst);

  return { validation, casesService, runsService, executor };
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

interface FakeTimers {
  fire(delay: number): number;
  pending(): number[];
  restore(): void;
}

function installFakeTimers(): FakeTimers {
  const realSet = globalThis.setTimeout;
  const realClear = globalThis.clearTimeout;
  const timers = new Map<number, { fn: () => void; delay: number }>();
  let next = 1;
  globalThis.setTimeout = ((fn: () => void, delay: number) => {
    const id = next++;
    timers.set(id, { fn, delay });
    return id;
  }) as unknown as typeof setTimeout;
  globalThis.clearTimeout = ((id: number) => void timers.delete(id)) as unknown as typeof clearTimeout;
  return {
    fire(delay) {
      const due = [...timers.entries()].filter(([, t]) => t.delay === delay);
      for (const [id, t] of due) {
        timers.delete(id);
        t.fn();
      }
      return due.length;
    },
    pending: () => [...timers.values()].map((t) => t.delay),
    restore() {
      globalThis.setTimeout = realSet;
      globalThis.clearTimeout = realClear;
    },
  };
}

async function genExecutor(ctx: Ctx, spec: Obj, diffText: (n: string) => string, subst: (s: unknown[] | undefined) => unknown[] | undefined): Promise<Obj[]> {
  const out: Obj[] = [];
  for (const sc of spec.scenarios as Obj[]) {
    if (sc.strategyVariants) {
      const variants: Obj[] = [];
      for (const strategy of sc.strategyVariants as string[]) {
        variants.push({ strategy, ...(await runExecutorScenario(ctx, spec, { ...sc, job: merge(sc.job, { config: { strategy } }) }, diffText, subst)) });
      }
      out.push({ name: sc.name, variants });
    } else {
      out.push({ name: sc.name, ...(await runExecutorScenario(ctx, spec, sc, diffText, subst)) });
    }
  }
  return out;
}

async function runExecutorScenario(ctx: Ctx, spec: Obj, sc: Obj, diffText: (n: string) => string, subst: (s: unknown[] | undefined) => unknown[] | undefined): Promise<Obj> {
  const { N } = ctx;
  const job = merge(spec.job, sc.job);
  const skills = subst(job.config.skills);
  if (skills) job.config.skills = skills;
  const cases = (sc.cases ?? {}) as Obj;
  const script = (sc.script ?? {}) as Obj;
  const loads: string[] = [];
  const records: Obj[] = [];
  const recordAttempts: string[] = [];
  const dropped: string[] = [];
  const finals: Obj[] = [];
  const llmRequests: Obj[] = [];
  const resolverCalls: string[] = [];
  const counters: Record<string, number> = {};
  const finish = deferred<void>();
  const log = recLog();

  const runnable = (id: string): Obj | undefined => {
    const c = cases[id];
    if (c === undefined || c === null) return undefined;
    return {
      id,
      expectation: spec.expect[c.expect],
      inputDiff: diffText(c.diff),
      inputMeta: { pr_title: `Case ${id}`, pr_description: null, repo: null, pr_number: null, head_sha: null, ...c.meta },
    };
  };
  const runs = {
    loadCase: async (_w: string, id: string) => {
      loads.push(id);
      if (sc.loadFail?.[id]) throw new Error(sc.loadFail[id]);
      return runnable(id);
    },
    recordCase: async (_r: string, rec: Obj) => {
      recordAttempts.push(rec.caseId);
      if (sc.recordFail?.[rec.caseId]) throw new Error(sc.recordFail[rec.caseId]);
      if (sc.recordGone?.includes(rec.caseId)) return 'case_gone';
      records.push(rec);
      return 'recorded';
    },
    dropCase: async (_r: string) => void dropped.push('dropped'),
    finish: async (_r: string, final: Obj) => {
      finals.push(final);
      finish.resolve();
      if (sc.finishFail) throw new Error(sc.finishFail);
    },
  };
  const review = (entry: Obj): Obj => ({
    verdict: 'comment',
    summary: 'ok',
    score: 80,
    findings: ((entry.findings ?? []) as Obj[]).map((f, i) => ({ ...spec.finding, id: `f${i + 1}`, ...f })),
  });
  const provider = {
    id: 'openrouter',
    listModels: async () => [],
    complete: async () => { throw new Error('unused'); },
    embed: async () => [],
    completeStructured: async (req: Obj) => {
      const user = (req.messages as Obj[]).find((m) => m.role === 'user')!.content as string;
      const m = /Review pull request(?: #\d+)? "Case (c\d+)"/.exec(user);
      const caseId = m ? m[1]! : '?';
      const idx = (counters[caseId] = (counters[caseId] ?? -1) + 1);
      const entries = (script[caseId] ?? []) as Obj[];
      const entry = entries[Math.min(idx, entries.length - 1)];
      llmRequests.push({
        case: caseId,
        call: idx,
        model: req.model,
        temperature: req.temperature === undefined ? '(omitted)' : req.temperature,
        schemaName: req.schemaName,
        maxRetries: req.maxRetries,
        sessionId: req.sessionId ?? null,
        hasSignal: req.signal !== undefined,
        hasOnUsage: req.onUsage !== undefined,
        messages: req.messages,
      });
      if (!entry) throw new Error(`no script for ${caseId}`);
      if (entry.error !== undefined) {
        if (entry.costOnError !== undefined) req.onUsage?.({ tokensIn: 100, tokensOut: 10, costUsd: entry.costOnError });
        throw new Error(entry.error);
      }
      if (entry.hang) {
        if (entry.cost !== undefined) req.onUsage?.({ tokensIn: 100, tokensOut: 10, costUsd: entry.cost });
        return new Promise((_res, rej) => {
          const s = req.signal as AbortSignal | undefined;
          if (s?.aborted) rej(s.reason);
          s?.addEventListener('abort', () => rej(s.reason), { once: true });
        });
      }
      if (entry.cost !== undefined) req.onUsage?.({ tokensIn: 100, tokensOut: 50, costUsd: entry.cost });
      const r = review(entry);
      return { data: r, model: req.model, tokensIn: 100, tokensOut: 50, costUsd: entry.cost ?? null, raw: JSON.stringify(r), attempts: 1 };
    },
  };

  const timers = installFakeTimers();
  let shutdownResult: boolean | undefined;
  let waitedOk = true;
  try {
    const executor = new N.evExecutor.EvalRunExecutor({
      runs,
      llm: async (provider_: string) => { resolverCalls.push(provider_); return provider; },
      parseDiff: N.serverDiffParser.parseUnifiedDiff,
      runBounded: (tasks: Array<() => Promise<unknown>>) => new N.pQueue.default({ concurrency: N.evConstants.EVAL_CASE_CONCURRENCY }).addAll(tasks),
      now: () => 1_000_000,
    });
    executor.launch(job, log);
    for (const step of (sc.steps ?? [{ waitFinish: true }]) as Obj[]) {
      if (step.settle) await settleMany(30);
      if (step.fire !== undefined) timers.fire(step.fire);
      if (step.shutdown !== undefined) shutdownResult = await executor.shutdown(step.shutdown);
      if (step.waitFinish) {
        const ok = await Promise.race([finish.promise.then(() => true), settleMany(200).then(() => false)]);
        if (!ok) waitedOk = false;
      }
    }
    // Let a failing final write and the log lines settle.
    await settleMany(10);
  } finally {
    timers.restore();
  }
  if (!waitedOk) throw new Error(`executor scenario "${sc.name}" did not finish`);

  const order = (job.caseIds as string[]);
  const byCase = (a: { case?: string; caseId?: string }, b: { case?: string; caseId?: string }): number =>
    order.indexOf((a.case ?? a.caseId)!) - order.indexOf((b.case ?? b.caseId)!);
  return {
    job,
    cases: sc.cases,
    script: sc.script,
    steps: sc.steps,
    result: {
      loads: [...loads].sort((a, b) => order.indexOf(a) - order.indexOf(b)),
      resolverCalls,
      llmRequests: llmRequests.sort((a, b) => byCase(a, b) || (a.call as number) - (b.call as number)),
      records: records.sort(byCase),
      recordAttempts: recordAttempts.sort((a, b) => order.indexOf(a) - order.indexOf(b)),
      dropped: dropped.length,
      finals,
      shutdown: shutdownResult,
      logs: log.lines.map((l) => ({ level: l.level, msg: l.msg, obj: showObj(l.obj) })),
    },
  };
}
