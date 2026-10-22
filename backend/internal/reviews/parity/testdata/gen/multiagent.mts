/** multiagent.json — the multi-agent read model, Learn, and the stored-row mappers. */
import { corpusJson, findingRecord, recDb, resetFindingCounter, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

const dataOf = (v: unknown): unknown => JSON.parse(JSON.stringify(v));
const WS = '0b6f2d1a-7c34-4e0a-8a55-00000000w001';

function columnOf(ctx: Ctx, c: Obj): Obj {
  return {
    run_id: c.run,
    agent_id: c.agent,
    agent_name: c.name,
    provider: c.provider ?? 'openrouter',
    model: c.model ?? 'deepseek/deepseek-v4-flash',
    status: c.status,
    error: c.error ?? null,
    verdict: 'verdict' in c ? c.verdict : 'comment',
    score: 'score' in c ? c.score : 70,
    summary: 'summary' in c ? c.summary : 'summary',
    duration_ms: 'dur' in c ? c.dur : 1000,
    cost_usd: 'cost' in c ? c.cost : 0.001,
    // memory_id is present on every column finding (null when none).
    findings: (c.findings as Obj[]).map((f) => ({
      ...findingRecord(ctx, f, `review-${c.run}`, null),
      memory_id: f.mem ?? c.memory ?? null,
    })),
  };
}

export async function genMultiAgent(ctx: Ctx): Promise<void> {
  const { multiAgent, multiAgentService, multiAgentRepo, errors, reviewDomain } = ctx.N;
  const spec = corpusJson(ctx, 'multiagent/cases.json');
  resetFindingCounter();

  const columns = (spec.columns as Obj[]).map((c) => {
    const cols = (c.columns as Obj[]).map((x) => columnOf(ctx, x));
    const ordered = multiAgent.orderColumns(cols);
    return {
      name: c.name,
      columns: cols,
      orderedRunIds: ordered.map((x: Obj) => x.run_id),
      totals: multiAgent.runTotals(ordered),
      conflicts: dataOf(multiAgent.groupFindings(ordered)),
    };
  });

  const learn = (spec.learn as Obj[]).map((f) => ({ finding: f, content: multiAgent.learnedMemoryContent(f) }));
  const learnContext = (spec.learnContext as string[]).map((id) => ({ findingId: id, context: multiAgent.learnContext(id) }));

  const errOf = (e: unknown): Obj => {
    const x = e as Obj;
    return { kind: x.kind, code: x.code, message: x.message, status: x.statusCode ?? null };
  };

  const get: Obj[] = [];
  for (const c of spec.get as Obj[]) {
    const cols = (c.columns as Obj[]).map((x) => columnOf(ctx, x));
    const svc = new multiAgentService.MultiAgentService({
      store: {
        getMultiAgentRun: async () => ({ ...c.header, ranAt: new Date(c.header.ranAt) }),
        columnsForMultiAgentRun: async () => cols,
      },
      tx: { run: async () => undefined },
    });
    get.push({ name: c.name, header: c.header, columns: cols, response: dataOf(await svc.get(WS, c.header.id)) });
  }
  {
    const svc = new multiAgentService.MultiAgentService({ store: { getMultiAgentRun: async () => undefined }, tx: { run: async () => undefined } });
    try {
      await svc.get(WS, 'missing');
    } catch (e) {
      get.push({ name: 'get_unknown_run', error: errOf(e) });
    }
  }

  const list: Obj[] = [];
  for (const c of spec.list as Obj[]) {
    let limitUsed: number | null = null;
    const svc = new multiAgentService.MultiAgentService({
      store: {
        repoInWorkspace: async () => true,
        listMultiAgentRuns: async (_w: string, _r: string, limit: number) => {
          limitUsed = limit;
          return (c.rows as Obj[]).map((r) => ({ ...r, ranAt: new Date(r.ranAt) }));
        },
      },
      tx: { run: async () => undefined },
    });
    try {
      list.push({ name: c.name, limit: c.limit, rows: c.rows, limitUsed: undefined, response: dataOf(await svc.list(WS, 'repo-1', c.limit ?? undefined)), limitPassedToStore: limitUsed });
    } catch (e) {
      list.push({ name: c.name, limit: c.limit, error: errOf(e) });
    }
  }
  {
    const svc = new multiAgentService.MultiAgentService({ store: { repoInWorkspace: async () => false }, tx: { run: async () => undefined } });
    try {
      await svc.list(WS, 'missing', undefined);
    } catch (e) {
      list.push({ name: 'list_unknown_repo', error: errOf(e) });
    }
  }

  const estimates = {
    rows: spec.estimates,
    response: dataOf(await new multiAgentService.MultiAgentService({ store: { agentRunEstimates: async () => spec.estimates }, tx: { run: async () => undefined } }).estimates(WS)),
  };

  // Learn: the real service over the real repository's insert, on a recording handle.
  const learnCases: Obj[] = [];
  const target = {
    finding: findingRecord(ctx, { id: 'f-1', f: 'src/config.ts', s: 12, e: 12, sev: 'CRITICAL', cat: 'security', t: 'Hardcoded key', why: 'A live key.', fix: 'Use an env var.', c: 0.98 }, 'review-1', null),
    workspaceId: WS,
    repoId: 'repo-1',
    prNumber: 482,
  };
  for (const c of [
    { name: 'learn_new_entry', target, existing: null },
    { name: 'learn_existing_entry_is_returned', target, existing: 'mem-existing' },
    { name: 'learn_without_suggestion', target: { ...target, finding: { ...target.finding, suggestion: null } }, existing: null },
    { name: 'learn_unknown_finding', target: null, existing: null },
    { name: 'learn_finding_of_another_workspace', target: { ...target, workspaceId: 'other-workspace' }, existing: null },
  ]) {
    const ops: Obj[] = [];
    const db = recDb(ctx, (op) => ops.push(op), (op) => (op.kind === 'insert' ? [{ id: 'mem-new' }] : []));
    const repo = new multiAgentRepo.MultiAgentRepository(db);
    const learnWrites = {
      lockLearn: async () => {
        ops.push({ kind: 'lock', key: `learn:${(c.target as Obj | null)?.finding?.id ?? ''}` });
      },
      memoryIdsForFindings: async (_w: string, ids: string[]) => new Map(c.existing ? [[ids[0], c.existing]] : []),
      insertLearnedMemory: (v: Obj) => repo.insertLearnedMemory(v),
    };
    const svc = new multiAgentService.MultiAgentService({
      store: { findingForLearn: async () => c.target ?? undefined },
      tx: { run: async (work: (r: Obj) => Promise<unknown>) => work({ learn: learnWrites }) },
    });
    try {
      learnCases.push({ name: c.name, target: c.target, existingMemoryId: c.existing, response: dataOf(await svc.learn(WS, 'f-1')), statements: ops });
    } catch (e) {
      learnCases.push({ name: c.name, target: c.target, error: errOf(e), statements: ops });
    }
  }

  // Row → wire mapping of findings and reviews (mappers.ts, domain/review.ts).
  const when = new Date('2026-01-02T03:04:05.678Z');
  const baseRow = {
    id: 'f-1', reviewId: 'rev-1', severity: 'WARNING', category: 'bug', title: 'T', file: 'a.ts', startLine: 1, endLine: 2,
    rationale: 'R', suggestion: null, confidence: 0.5, kind: null, trifectaComponents: null, skillName: null, outOfScope: false,
    acceptedAt: null, dismissedAt: null,
  };
  const rowCases: Obj[] = [
    { name: 'defaults', row: baseRow, evalCaseId: null },
    { name: 'all_optional_fields', row: { ...baseRow, suggestion: 'fix', kind: 'secret_leak', trifectaComponents: ['untrusted_input'], skillName: 'rubric', outOfScope: true, acceptedAt: when, dismissedAt: when }, evalCaseId: 'case-1' },
  ].map((c) => ({ ...c, record: dataOf(ctx.N.mappers.toFindingRecord(c.row, c.evalCaseId)) }));

  const reviewRow = { id: 'rev-1', prId: 'pr-1', agentId: 'agent-1', runId: 'run-1', kind: 'review', verdict: 'comment', summary: 'S', score: 70, model: 'm', createdAt: when };
  const stored = (r: Obj, fs: Obj[]) => ctx.N.mappers.toStoredReview(r, fs, new Map());
  const reviewCases: Obj[] = [
    { name: 'with_run_and_agent', stored: stored(reviewRow, [baseRow]), agentName: 'Security', usage: { tokensIn: 100, tokensOut: 20, costUsd: 0.01 } },
    { name: 'run_without_usage_row_has_no_usage_keys', stored: stored(reviewRow, []), agentName: 'Security', usage: undefined },
    { name: 'agent_deleted_run_null', stored: stored({ ...reviewRow, agentId: null, runId: null }, []), agentName: undefined, usage: undefined },
    { name: 'usage_with_nulls', stored: stored(reviewRow, []), agentName: 'Security', usage: { tokensIn: null, tokensOut: null, costUsd: null } },
  ].map((c) => ({ name: c.name, stored: dataOf(c.stored), agentName: c.agentName ?? null, usage: c.usage ?? null, record: dataOf(reviewDomain.withRunContext(c.stored, c.agentName, c.usage)) }));

  void errors;
  ctx.writer.write('multiagent.json', {
    note: 'columns: orderColumns (agent name localeCompare "en", then run id), runTotals and groupFindings (on the ordered columns, as MultiAgentService.get does). get/list/estimates: MultiAgentService over a fake store (expected response bodies). learn: learnedMemoryContent / learnContext, and the Learn use case with the real repository insert on a recording handle (statements = the INSERT values). findingRows/reviewRecords: the row→wire mappers. File names compare in UTF-16 order in groupFindings (a cited astral file sorts before U+FF5E).',
    columns,
    learn,
    learnContext,
    get,
    list,
    estimates,
    learnUseCase: learnCases,
    findingRows: rowCases,
    reviewRecords: reviewCases,
  });
}
