/**
 * evals.json — the Node eval pipeline (server/src/modules/evals): the pure domain rules (case builder, diff
 * normalisation and content check, edit merge, task line, error text, usage cost, scoring, aggregation, verdict,
 * line diff, compare, dashboard derivations, period start), the schema checks of the request bodies, the real
 * `EvalCasesService` / `EvalRunsService` / `EvalRunExecutor` over in-memory fakes of their ports and a scripted
 * LLM. Spec: FR6056, NFR6001, AC6021, AC6066 (Phase 6); plan S16. See ./README.md.
 */
import { createHash } from 'node:crypto';
import { corpusJson, expandText, type Obj } from './common.mts';
import { attempt, merge, showParse } from './common6.mts';
import type { Ctx } from './ctx.mts';
import { prng, textOf } from './lib.mts';
import { genEvalFlows } from './evals-flows.mts';

// ---------------------------------------------------------------------------
// Corpus helpers
// ---------------------------------------------------------------------------

/** `{"$diff": name, eol?}` placeholders become the text of a diff of `content.json`. */
export function diffResolver(ctx: Ctx): (v: unknown) => unknown {
  const diffs = expandText(corpusJson(ctx, 'evals/content.json')).diffs as Obj;
  const walk = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(walk);
    if (v !== null && typeof v === 'object') {
      const o = v as Obj;
      if (typeof o.$diff === 'string') {
        const spec = diffs[o.$diff];
        if (!spec) throw new Error(`unknown corpus diff ${o.$diff}`);
        return textOf(o.eol ? { ...spec, eol: o.eol } : spec);
      }
      return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, walk(x)]));
    }
    return v;
  };
  return walk;
}

const sha = (s: string): string => createHash('sha256').update(s, 'utf8').digest('hex');

function genLines(spec: Obj): { before: string; after: string } {
  const range = (n: number, f: (i: number) => string): string[] => Array.from({ length: n }, (_, i) => f(i + 1));
  const head = range(spec.prefix ?? 0, (i) => `head-${i}`);
  const tail = range(spec.suffix ?? 0, (i) => `tail-${i}`);
  if (spec.kind === 'alternate') {
    const before = range(spec.before, (i) => `l-${i}`);
    const after = range(spec.after, (i) => (i % 2 === 0 ? `l-${i}` : `m-${i}`));
    return { before: before.join('\n'), after: after.join('\n') };
  }
  const before = [...head, ...range(spec.before, (i) => `a-${i}`), ...tail];
  const after = [...head, ...range(spec.after, (i) => `b-${i}`), ...tail];
  return { before: before.join('\n'), after: after.join('\n') };
}

const rle = (ops: string[]): Array<[string, number]> => {
  const out: Array<[string, number]> = [];
  for (const op of ops) {
    const last = out[out.length - 1];
    if (last && last[0] === op) last[1]++;
    else out.push([op, 1]);
  }
  return out;
};

const metricsOnly = (r: Obj): Obj => ({ recall: r.recall, precision: r.precision, citation_accuracy: r.citation_accuracy });

// ---------------------------------------------------------------------------
// evals.json
// ---------------------------------------------------------------------------

export async function genEvals(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const resolve = diffResolver(ctx);
  const load = (rel: string): Obj => resolve(expandText(corpusJson(ctx, rel))) as Obj;
  const domain = load('evals/domain.json');
  const content = load('evals/content.json');
  const create = load('evals/create.json');
  const edit = load('evals/edit.json');
  const scoring = load('evals/scoring.json');
  const cmp = load('evals/compare.json');
  const parse = N.serverDiffParser.parseUnifiedDiff as (raw: string) => unknown;
  const ws = domain.workspaceId as string;
  const S = N.shared;

  const constants = {
    REGRESSION_THRESHOLD: N.evConstants.REGRESSION_THRESHOLD,
    METRIC_DECIMALS: N.evConstants.METRIC_DECIMALS,
    COST_DECIMALS: N.evConstants.COST_DECIMALS,
    SPARKLINE_POINTS: N.evConstants.SPARKLINE_POINTS,
    TREND_POINTS: N.evConstants.TREND_POINTS,
    PERIOD_DAYS: N.evConstants.PERIOD_DAYS,
    MS_PER_DAY: N.evConstants.MS_PER_DAY,
    RECENT_RUNS_LIMIT: N.evConstants.RECENT_RUNS_LIMIT,
    EVAL_CASE_CONCURRENCY: N.evConstants.EVAL_CASE_CONCURRENCY,
    EVAL_CASE_TIMEOUT_MS: N.evConstants.EVAL_CASE_TIMEOUT_MS,
    LINE_DIFF_MAX_CELLS: N.evConstants.LINE_DIFF_MAX_CELLS,
    INTERRUPTED_ERROR: N.evConstants.INTERRUPTED_ERROR,
    ERROR_MESSAGE_MAX_CHARS: N.evConstants.ERROR_MESSAGE_MAX_CHARS,
    EVAL_SHUTDOWN_WAIT_MS: N.evConstants.EVAL_SHUTDOWN_WAIT_MS,
    shared: {
      EVAL_CASE_NAME_MAX: S.EVAL_CASE_NAME_MAX,
      EVAL_INPUT_DIFF_MAX_CHARS: S.EVAL_INPUT_DIFF_MAX_CHARS,
      EVAL_CASES_PER_AGENT_MAX: S.EVAL_CASES_PER_AGENT_MAX,
      EVAL_DIFF_FILES_DETAIL_MAX: S.EVAL_DIFF_FILES_DETAIL_MAX,
      EVAL_PERIOD_TREND_MAX: S.EVAL_PERIOD_TREND_MAX,
      EVAL_EXPECTATION_FILE_MAX: S.EVAL_EXPECTATION_FILE_MAX,
      EVAL_EXPECTATION_TITLE_MAX: S.EVAL_EXPECTATION_TITLE_MAX,
      EVAL_PR_TITLE_MAX: S.EVAL_PR_TITLE_MAX,
      EVAL_PR_DESCRIPTION_MAX: S.EVAL_PR_DESCRIPTION_MAX,
      EVAL_PERIODS: S.EVAL_PERIODS,
    },
  };

  const rounding = (domain.rounding as Array<[number, number]>).map(([value, decimals]) => ({
    value,
    decimals,
    result: N.evScoring.roundTo(value, decimals),
  }));
  const deltas = (domain.deltas as Obj[]).map((d) => ({
    ...d,
    result: d.decimals === undefined ? N.evScoring.metricDelta(d.prev, d.next) : N.evScoring.metricDelta(d.prev, d.next, d.decimals),
  }));
  const intersect = (domain.intersect as number[][]).map(([aStart, aEnd, bStart, bEnd]) => ({
    a: [aStart, aEnd],
    b: [bStart, bEnd],
    result: N.evScoring.rangesIntersect(aStart, aEnd, bStart, bEnd),
  }));

  const caseNames = (domain.names as Obj[]).map((c) => ({ ...c, name: N.evCaseBuilder.caseName(c.title, c.file, c.startLine) }));
  const singleFile = (domain.singleFile as Obj[]).map((c) => ({ ...c, diff: N.evCaseBuilder.buildSingleFileDiff(c.path, c.patch) }));
  const normalize = (domain.normalize as Obj[]).map((c) => ({ ...c, result: N.evCaseInput.normalizeCaseDiff(c.raw, c.file) }));

  const fromFinding = (domain.findings.cases as Obj[]).map((c) => {
    const source = merge(domain.findings.base, c.set);
    return { name: c.name, source, ...attempt(() => N.evCaseBuilder.buildEvalCase(ws, source)) };
  });

  const check = (diff: string, expectation: Obj): Obj =>
    attempt(() => {
      N.evCaseInput.checkCaseContent(diff, expectation, parse);
      return { ok: true };
    });
  // Corpus diffs are referenced by name (their texts are written to the golden once, as `diffs`).
  const diffs = Object.fromEntries(Object.entries(content.diffs as Obj).map(([k, v]) => [k, textOf(v)]));
  const contentWithRefs = (content.content as Obj[]).map((c) => ({
    name: c.name,
    diff: c.diff,
    expectation: c.expectation,
    ...check(diffs[c.diff as string]!, c.expectation),
  }));

  // The create-case pipeline of EvalCasesService.create without the owner/limit/insert steps.
  const manual = (create.manual as Obj[]).map((c) => {
    const parsed = S.EvalCaseCreateInput.safeParse(c.body);
    const out: Obj = { name: c.name, body: c.body, parse: showParse(parsed) };
    if (!parsed.success) return out;
    const input = parsed.data as Obj;
    const owner = { kind: 'agent', id: 'agent-1' };
    const attempted = attempt(() => {
      const diff = N.evCaseInput.normalizeCaseDiff(input.input_diff, input.expectation.file);
      N.evCaseInput.checkCaseContent(diff, input.expectation, parse);
      return N.evCaseInput.buildManualCase(ws, owner, input, diff);
    });
    return { ...out, owner, ...attempted };
  });

  // The edit pipeline of EvalCasesService.update without the row lock and the write.
  const editOne = (stored: Obj, c: Obj): Obj => {
    const parsed = S.AgentEvalCaseUpdateInput.safeParse(c.patch);
    const out: Obj = { name: c.name, patch: c.patch, parse: showParse(parsed) };
    if (!parsed.success) return out;
    let merged: Obj | undefined;
    const m = attempt(() => (merged = N.evCaseInput.mergeCaseEdit(stored, parsed.data)));
    if (m.error) return { ...out, ...m };
    const mm = merged as Obj;
    const res: Obj = { values: mm.values, recheck: mm.recheck, contentChanged: mm.contentChanged };
    if (mm.recheck) {
      res.recheckResult = check(mm.values.inputDiff ?? stored.inputDiff, mm.values.expectation ?? stored.expectation);
    }
    return { ...out, result: res };
  };
  const editCases = (edit.edits as Obj[]).map((c) => editOne(edit.stored, c));
  const manualEditCases = (edit.manualEdits as Obj[]).map((c) => editOne(edit.manualStored, c));

  const taskLines = (domain.taskLines as Obj[]).map((m) => ({ meta: m, line: N.evRunInput.evalTaskLine(m) }));
  const errorText = (domain.errors as Obj[]).map((c) => {
    const value =
      c.kind === 'error' ? new Error(c.message) : c.kind === 'string' ? c.message : c.kind === 'number' ? 42 : c.kind === 'null' ? null : c.kind === 'undefined' ? undefined : {};
    return { ...c, result: N.evRunInput.errorMessage(value) };
  });
  const usage = (domain.usage as Obj[]).map((c) => {
    const u = new N.evRunInput.CaseUsage();
    for (const cost of c.costs as Array<number | null>) u.add({ costUsd: cost });
    const raw = u.costUsd;
    return { ...c, costUsd: raw, rounded: raw === null ? null : N.evScoring.roundTo(raw, N.evConstants.COST_DECIMALS) };
  });
  const kept = (domain.kept as Obj[]).map((c) => ({ ...c, result: N.evRunInput.toKeptFindings(c.findings) }));
  const skillBlocks = (domain.skillBlocks as Obj[]).map((c) => ({ ...c, block: N.skillsDomain.renderSkillBlock(c) }));

  // ---- scoring ----
  const expectations = scoring.expect as Obj;
  const score = (scoring.score as Obj[]).map((c) => {
    const expectation = { ...expectations[c.expect], ...(c.expectOverride ?? {}) };
    const keptFindings = (c.kept as Obj[]).map((k) => ({ ...scoring.findingBase, ...k }));
    return { name: c.name, expectation, kept: keptFindings, result: N.evScoring.scoreCase(expectation, keptFindings) };
  });
  const outcome = (o: Obj): Obj => ({ ...scoring.outcomeBase, ...o });
  const aggregate = (scoring.aggregate as Obj[]).map((c) => {
    const outcomes = (c.outcomes as Obj[]).map(outcome);
    return { name: c.name, outcomes, result: N.evScoring.aggregateRun(outcomes) };
  });
  const rnd = prng(scoring.random.seed);
  const pick = <T,>(xs: T[]): T => xs[Math.floor(rnd() * xs.length)]!;
  const randomRuns = Array.from({ length: scoring.random.runs as number }, (_, k) => {
    const n = Math.floor(rnd() * (scoring.random.maxCases as number + 1));
    const outcomes = Array.from({ length: n }, () => {
      const kind = pick(['must_find', 'must_not_flag']);
      const status = pick(['passed', 'failed', 'error', 'passed']);
      const hit = kind === 'must_find' ? Math.floor(rnd() * 3) : 0;
      return {
        kind,
        status,
        hit_count: status === 'error' ? 0 : hit,
        violation_count: status === 'error' ? 0 : kind === 'must_not_flag' ? Math.floor(rnd() * 3) : 0,
        unlabeled_count: status === 'error' ? 0 : Math.floor(rnd() * 4),
        dropped_count: status === 'error' ? 0 : Math.floor(rnd() * 4),
        cost_usd: rnd() < 0.2 ? null : Math.round(rnd() * 5e7) / 1e9,
        error: status === 'error' ? 'boom' : null,
      };
    });
    return { name: `random ${k + 1}`, outcomes, result: N.evScoring.aggregateRun(outcomes) };
  });
  const verdict = (scoring.verdict as Obj[]).map((c) => {
    const outcomes = (c.outcomes as Obj[]).map(outcome);
    return { name: c.name, outcomes, result: N.evScoring.runVerdict(outcomes) };
  });

  // ---- compare ----
  const lineDiff = (cmp.lineDiff as Obj[]).map((c) => {
    const text = c.gen ? genLines(c.gen) : { before: c.before as string, after: c.after as string };
    const result = N.evCompare.lineDiff(text.before, text.after) as Array<{ op: string; line: string }>;
    const base: Obj = c.gen ? { name: c.name, gen: c.gen } : { name: c.name, before: c.before, after: c.after };
    if (!c.compact) return { ...base, result };
    return {
      ...base,
      summary: {
        count: result.length,
        ops: rle(result.map((r) => r.op)),
        sha256: sha(result.map((r) => `${r.op}\t${r.line}\n`).join('')),
      },
    };
  });
  const side = (s: Obj, runId: string): Obj => ({
    run: merge(cmp.runBase, { id: runId, ...(s.run ?? {}) }),
    config: merge(cmp.configBase, s.config),
    results: s.results,
  });
  const compare = (cmp.compare as Obj[]).map((c) => {
    const a = side(c.a, 'run-a');
    const b = side(c.b, 'run-b');
    return { name: c.name, a, b, ...attempt(() => N.evCompare.compareRuns(a, b)) };
  });
  const alerts = (cmp.alerts as Obj[]).map((c) => ({
    ...c,
    result: N.evDashboard.regressionAlert(c.latest && { ...c.latest }, c.previous && { ...c.previous }),
  }));
  const series = (cmp.series as Obj[]).map((c) => {
    const runs = Array.from({ length: c.n as number }, (_, k) => {
      const i = k + 1;
      const isNull = c.nullEvery !== undefined && i % c.nullEvery === 0;
      return {
        id: `r${i}`,
        recall: isNull ? null : Math.round((0.3 + i * 0.0061) * 1e4) / 1e4,
        precision: isNull ? null : Math.round((0.9 - i * 0.003) * 1e4) / 1e4,
        citation_accuracy: isNull ? null : 1,
      };
    });
    const maxes: number[] = c.maxes ?? [1, 5, 20];
    return {
      name: c.name,
      runsNewestFirst: runs,
      sparkline: N.evDashboard.sparklineOf(runs),
      trendDefault: N.evDashboard.trendOf(runs).map((r: Obj) => r.id),
      trends: maxes.map((max) => ({ max, ids: N.evDashboard.trendOf(runs, max).map((r: Obj) => r.id) })),
    };
  });
  const periods = (cmp.periods as Obj[]).map((c) => ({
    ...c,
    result: N.evDashboard.periodStart(c.period, new Date(c.now))?.toISOString() ?? null,
  }));

  // ---- request schemas ----
  const flows = await genEvalFlows(ctx, { resolve, load, parse, check });

  ctx.writer.write('evals.json', {
    note: 'Node eval pipeline at d1c9093: pure domain rules, request schemas, EvalCasesService / EvalRunsService / EvalRunExecutor over in-memory fakes and a scripted LLM. Inputs travel with every case. results are raw Node values (errors as {name, kind, code?, message, details?}). The SQL of the repository is NOT here (emulated reads in flows say so).',
    constants,
    rounding,
    deltas,
    intersect,
    caseNames,
    singleFile,
    normalize,
    fromFinding,
    diffs,
    content: contentWithRefs,
    manual,
    edit: { stored: edit.stored, cases: editCases, manualStored: edit.manualStored, manualCases: manualEditCases },
    taskLines,
    errorText,
    usage,
    kept,
    skillBlocks,
    score,
    aggregate: [...aggregate, ...randomRuns],
    verdict,
    lineDiff,
    compare,
    alerts,
    series,
    periods,
    ...flows,
  });
}
