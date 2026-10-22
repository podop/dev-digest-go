/**
 * performance.json — the Node agent-performance module: period validation, windows, the metric/trend/usage
 * folds and PerformanceService (dashboard + stats) over emulated tables (AC5015, AC5016, AC5055, EC5030–EC5032,
 * EC5036).
 */
import { corpusJson, expandText, showError, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

const toDate = (iso: string): Date => new Date(iso);

function win(w: Obj): Obj {
  return { key: w.key, from: toDate(w.from), to: toDate(w.to), toInclusive: w.toInclusive };
}
const showWin = (w: Obj): Obj => ({ key: w.key, from: w.from, to: w.to, toInclusive: w.toInclusive });

function showIssues(issues: Obj[]): Obj[] {
  return issues.map((i) => ({ code: i.code, path: i.path, message: i.message, ...(i.validation !== undefined ? { validation: i.validation } : {}) }));
}

// ---------------------------------------------------------------------------
// Period
// ---------------------------------------------------------------------------

function genPeriod(ctx: Ctx): Obj {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'performance/period.json'));
  const Q = N.perfSchemas.PeriodQuery;
  const queries = (spec.queries as Obj[]).map((c) => {
    const r = Q.safeParse(c.query);
    return { name: c.name, query: c.query, ok: r.success, ...(r.success ? { data: r.data } : { issues: showIssues(r.error.issues) }) };
  });
  const { z } = ctx.ddRequire('zod');
  const Instant = z.string().datetime({ offset: true });
  const datetimes = (spec.datetimes as string[]).map((s) => {
    const r = Instant.safeParse(s);
    const ms = Date.parse(s);
    return { value: s, ok: r.success, dateParse: Number.isNaN(ms) ? null : ms };
  });
  const windows = (spec.windows as Obj[]).map((c) => {
    const parsed = Q.parse(c.query);
    const now = c.now ? new Date(c.now) : new Date('2026-03-31T12:00:00.000Z');
    try {
      const w = N.perfPeriod.resolveWindow(parsed, now);
      const prev = N.perfPeriod.previousWindow(w);
      return {
        name: c.name,
        query: c.query,
        now: now.toISOString(),
        window: { key: w.key, from: w.from.toISOString(), to: w.to.toISOString(), toInclusive: w.toInclusive },
        previous: { key: prev.key, from: prev.from.toISOString(), to: prev.to.toISOString(), toInclusive: prev.toInclusive },
        period: N.perfPeriod.toPeriodInfo(w),
      };
    } catch (e) {
      return { name: c.name, query: c.query, now: now.toISOString(), error: showError(e) };
    }
  });
  return { queries, datetimes, windows };
}

// ---------------------------------------------------------------------------
// Folds
// ---------------------------------------------------------------------------

function genFolds(ctx: Ctx): Obj {
  const { N } = ctx;
  const M = N.perfMetrics;
  const Tr = N.perfTrends;
  const spec = expandText(corpusJson(ctx, 'performance/folds.json'));
  const base = spec.run as Obj;
  const run = (r: Obj): Obj => {
    const m = { ...base, ...r };
    return { ...m, ranAt: toDate(m.ranAt) };
  };
  const showRun = (r: Obj): Obj => ({ agentId: r.agentId, status: r.status, source: r.source, model: r.model, durationMs: r.durationMs, costUsd: r.costUsd, ranAt: r.ranAt instanceof Date ? r.ranAt.toISOString() : r.ranAt });
  const showRuns = (rs: Obj[]): Obj[] => rs.map((r) => showRun({ ...base, ...r }));

  const metrics = (spec.metrics as Obj[]).map((c) => {
    const runs = (c.runs as Obj[]).map(run);
    const res = c.minDecisions === undefined ? M.agentMetrics(runs, c.findings ?? undefined) : M.agentMetrics(runs, c.findings ?? undefined, c.minDecisions);
    return { name: c.name, runs: showRuns(c.runs), findings: c.findings, minDecisions: c.minDecisions ?? 5, result: res };
  });

  const summaries = (spec.summaries as Obj[]).map((c) => {
    const w = c.window ? win(c.window) : win({ key: '30d', from: '2026-03-01T12:00:00.000Z', to: '2026-03-31T12:00:00.000Z', toInclusive: true });
    const rows = (c.rows as Obj[]).map((r) =>
      M.buildRow(
        r.agent,
        { runs: (r.current.runs as Obj[]).map(run), findings: r.current.findings ?? undefined },
        { runs: (r.previous.runs as Obj[]).map(run), findings: r.previous.findings ?? undefined },
        w,
      ),
    );
    return {
      name: c.name,
      window: showWin(c.window ?? { key: '30d', from: '2026-03-01T12:00:00.000Z', to: '2026-03-31T12:00:00.000Z', toInclusive: true }),
      input: c.rows.map((r: Obj) => ({ agent: r.agent, current: { runs: showRuns(r.current.runs), findings: r.current.findings }, previous: { runs: showRuns(r.previous.runs), findings: r.previous.findings } })),
      rows,
      summary: M.summarize(rows),
      mostActive: M.mostActive(rows),
      costByAgent: M.costByAgent(rows),
    };
  });

  const costByModel = (spec.costByModel as Obj[]).map((c) => ({ name: c.name, runs: showRuns(c.runs), result: M.costByModel((c.runs as Obj[]).map(run)) }));

  const groups = (spec.groups as Obj[]).map((c) => {
    const known = new Set(c.known as string[]);
    const runs = (c.runs as Obj[]).map(run);
    const gr = M.groupRuns(runs, known);
    const gf = M.groupFindings(c.counts, known);
    return {
      name: c.name,
      known: c.known,
      runs: showRuns(c.runs),
      counts: c.counts,
      groupedRuns: [...gr].map(([k, v]) => ({ key: k, runs: (v as Obj[]).length })),
      groupedFindings: [...gf].map(([k, v]) => ({ key: k, ...v })),
    };
  });

  const trends = (spec.trends as Obj[]).map((c) => {
    const w = win(c.window);
    const slices = Tr.trendSlices(w);
    return {
      name: c.name,
      window: showWin(c.window),
      runs: showRuns(c.runs),
      slices: { from: slices.from, width: slices.width, count: slices.count },
      result: Tr.runsTrend((c.runs as Obj[]).map(run), w),
    };
  });

  const severity = (spec.severity as Obj[]).map((c) => ({
    name: c.name,
    window: showWin(c.window),
    facts: c.facts,
    result: Tr.findingsBySeverity((c.facts as Obj[]).map((f) => ({ ...f, ranAt: toDate(f.ranAt) })), win(c.window)),
  }));

  const usage = {
    skills: (spec.usage.skills as Obj[]).map((c) => ({ name: c.name, facts: c.facts, result: Tr.topSkills(c.facts) })),
    memories: (spec.usage.memories as Obj[]).map((c) => ({ name: c.name, pulledPerRun: c.pulledPerRun, result: Tr.topMemories(c.pulledPerRun) })),
    categories: (spec.usage.categories as Obj[]).map((c) => ({ name: c.name, facts: c.facts, result: Tr.findingsByCategory((c.facts as Obj[]).map((f) => ({ ...f, ranAt: toDate(f.ranAt) }))) })),
  };

  return { metrics, summaries, costByModel, groups, trends, severity, usage };
}

// ---------------------------------------------------------------------------
// Service over emulated tables
// ---------------------------------------------------------------------------

export function makeReader(ds: Obj, workspace: string) {
  const agents = ds.agents as Obj[];
  const runs = ds.runs as Obj[];
  const findings = ds.findings as Obj[];
  const skills = ds.skills as Obj[];
  const runSkills = ds.run_skills as Obj[];
  const traces = ds.traces as Obj[];
  const pulls = ds.pulls as Obj[];
  const inWindow = (t: Date, w: Obj): boolean => t.getTime() >= w.from.getTime() && (w.toInclusive ? t.getTime() <= w.to.getTime() : t.getTime() < w.to.getTime());
  const counted = (ws: string, w: Obj, agentId?: string): Obj[] =>
    runs
      .filter(
        (r) =>
          r.workspace_id === ws &&
          ['done', 'failed', 'cancelled'].includes(r.status) &&
          ['local', 'ci'].includes(r.source) &&
          inWindow(new Date(r.ran_at), w) &&
          (agentId === undefined || r.agent_id === agentId),
      )
      .sort((a, b) => Date.parse(a.ran_at) - Date.parse(b.ran_at) || (a.id < b.id ? -1 : 1));
  const runOf = new Map(runs.map((r) => [r.id as string, r]));
  const findingsOf = (rs: Obj[]): Array<{ run: Obj; f: Obj }> => {
    const ids = new Set(rs.map((r) => r.id as string));
    return findings.filter((f) => ids.has(f.run_id)).map((f) => ({ run: runOf.get(f.run_id)!, f }));
  };
  return {
    listAgents: async (ws: string) => agents.filter((a) => a.workspace_id === ws).map((a) => ({ id: a.id, name: a.name })),
    findAgent: async (ws: string, id: string) => {
      const a = agents.find((x) => x.workspace_id === ws && x.id === id);
      return a ? { id: a.id, name: a.name } : undefined;
    },
    countedRuns: async (ws: string, w: Obj, agentId?: string) =>
      counted(ws, w, agentId).map((r) => ({ agentId: r.agent_id, status: r.status, source: r.source, model: r.model, durationMs: r.duration_ms, costUsd: r.cost_usd, ranAt: new Date(r.ran_at) })),
    findingCounts: async (ws: string, w: Obj, agentId?: string) => {
      const groups = new Map<string | null, { agentId: string | null; total: number; accepted: number; dismissed: number }>();
      for (const { run, f } of findingsOf(counted(ws, w, agentId))) {
        const g = groups.get(run.agent_id) ?? { agentId: run.agent_id, total: 0, accepted: 0, dismissed: 0 };
        g.total++;
        if (f.accepted_at !== null) g.accepted++;
        if (f.dismissed_at !== null && f.accepted_at === null) g.dismissed++;
        groups.set(run.agent_id, g);
      }
      return [...groups.values()];
    },
    findingFacts: async (ws: string, w: Obj, agentId: string) => {
      const groups = new Map<string, { ranAt: Date; severity: string; category: string; count: number }>();
      for (const { run, f } of findingsOf(counted(ws, w, agentId))) {
        const key = `${run.id}\u0000${f.severity}\u0000${f.category}`;
        const g = groups.get(key) ?? { ranAt: new Date(run.ran_at), severity: f.severity, category: f.category, count: 0 };
        g.count++;
        groups.set(key, g);
      }
      return [...groups.values()];
    },
    skillUsage: async (ws: string, w: Obj, agentId: string) => {
      const ids = new Set(counted(ws, w, agentId).map((r) => r.id as string));
      const perSkill = new Map<string, Set<string>>();
      for (const rs of runSkills) if (ids.has(rs.run_id)) perSkill.set(rs.skill_id, (perSkill.get(rs.skill_id) ?? new Set()).add(rs.run_id));
      return [...perSkill].map(([skillId, set]) => ({ skillId, name: skills.find((s) => s.id === skillId)!.name as string, runs: set.size }));
    },
    memoryPulled: async (ws: string, w: Obj, agentId: string) => {
      const ids = counted(ws, w, agentId).map((r) => r.id as string);
      return traces.filter((t) => ids.includes(t.run_id)).map((t) => (t.memory_pulled === undefined ? null : t.memory_pulled));
    },
    runHistory: async (ws: string, w: Obj, agentId: string, limit: number) =>
      counted(ws, w, agentId)
        .sort((a, b) => Date.parse(b.ran_at) - Date.parse(a.ran_at) || (a.id < b.id ? 1 : -1))
        .slice(0, limit)
        .map((r) => {
          const p = pulls.find((x) => x.id === r.pr_id);
          const usage = r.tokens_in === null && r.tokens_out === null ? null : (r.tokens_in ?? 0) + (r.tokens_out ?? 0);
          return {
            run_id: r.id,
            ran_at: new Date(r.ran_at).toISOString(),
            status: r.status,
            source: r.source,
            pr: p ? { id: p.id, repo_id: p.repo_id, number: p.number } : null,
            tokens: usage,
            cost_usd: r.cost_usd,
            findings_count: r.findings_count,
            has_trace: traces.some((t) => t.run_id === r.id),
          };
        }),
  };
}

async function genService(ctx: Ctx): Promise<Obj[]> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'performance/dataset.json'));
  const ws = spec.workspace as string;
  const Q = N.perfSchemas.PeriodQuery;
  const out: Obj[] = [];
  for (const c of spec.cases as Obj[]) {
    const ds = spec.datasets[c.dataset] as Obj;
    const now = new Date((c.now as string | undefined) ?? (spec.now as string));
    const svc = new N.perfService.PerformanceService({ reader: makeReader(ds, ws), clock: () => now });
    const query = Q.parse(c.query);
    let outcome: Obj;
    let rowsEqualStats: boolean | undefined;
    try {
      if (c.call === 'overview') {
        const body = await svc.overview(ws, query);
        outcome = { status: 200, body };
        rowsEqualStats = true;
        for (const row of body.agents as Obj[]) {
          if (row.agent_id === null) continue;
          const stats = await svc.agentStats(ws, row.agent_id, query);
          if (JSON.stringify(stats.metrics) !== JSON.stringify(row.metrics) || JSON.stringify(stats.previous) !== JSON.stringify(row.previous) || JSON.stringify(stats.runs_trend) !== JSON.stringify(row.runs_trend)) rowsEqualStats = false;
        }
      } else {
        outcome = { status: 200, body: await svc.agentStats(ws, c.agent, query) };
      }
    } catch (e) {
      outcome = { error: showError(e) };
    }
    out.push({ name: c.name, dataset: c.dataset, call: c.call, agent: c.agent ?? null, query: c.query, parsedQuery: query, now: now.toISOString(), ...(rowsEqualStats !== undefined ? { dashboardRowsEqualStats: rowsEqualStats } : {}), ...outcome });
  }
  return out;
}

export async function genPerformance(ctx: Ctx): Promise<void> {
  const K = ctx.N.perfConstants;
  const spec = expandText(corpusJson(ctx, 'performance/dataset.json'));
  ctx.writer.write('performance.json', {
    note: 'Node agent performance (modules/performance) at d1c9093. period: PeriodQuery.safeParse over query objects (issues as zod reports them), the zod datetime check of a list of strings (with Date.parse of each, null = NaN), resolveWindow/previousWindow. folds: metrics, summaries (rows built with buildRow), cost segments, groupings, trends (trendSlices = {from ms, width ms, count}), severity buckets, usage tops and categories. service: PerformanceService over the tables of `datasets`; the repository SQL is emulated by the generator (see corpus/performance/dataset.json). `dashboardRowsEqualStats` = every dashboard row equals the same agent\'s stats metrics/previous/runs_trend (AC10).',
    constants: {
      minDecisions: K.MIN_DECISIONS,
      maxCustomSpanDays: K.MAX_CUSTOM_SPAN_DAYS,
      presetHours: K.PRESET_HOURS,
      defaultPeriod: K.DEFAULT_PERIOD,
      moneyDecimals: K.MONEY_DECIMALS,
      deletedAgentsName: K.DELETED_AGENTS_NAME,
      countedStatuses: K.COUNTED_STATUSES,
      countedSources: K.COUNTED_SOURCES,
      severitySlices: K.SEVERITY_SLICES,
      topUsage: K.TOP_USAGE,
      hourlyTrendMaxHours: K.HOURLY_TREND_MAX_HOURS,
      runHistoryLimit: K.RUN_HISTORY_LIMIT,
    },
    period: genPeriod(ctx),
    folds: genFolds(ctx),
    datasets: spec.datasets,
    service: await genService(ctx),
  });
}
