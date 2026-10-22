/** Pure reviewer-core / reviews-domain goldens: diff, chunks, reduce, grounding, prompt, schema. */
import { join } from 'node:path';
import { deferred, readJson, settleMany, textOf, type TextSpec } from './lib.mts';
import {
  corpusJson, expandFinding, expandReview, expandText, loadPr, parseDiff, rawDiffOf, resetFindingCounter, rowsOf,
  selectDb, type Obj,
} from './common.mts';
import type { Ctx } from './ctx.mts';

const dataOf = (v: unknown): unknown => JSON.parse(JSON.stringify(v));

export async function genDomain(ctx: Ctx): Promise<void> {
  await genDiff(ctx);
  genChunks(ctx);
  await genReduce(ctx);
  genGrounding(ctx);
  genPrompt(ctx);
  genSchema(ctx);
}

// ---------------------------------------------------------------------------
// diff.json — parse + reconstruction from pr_files rows (AC4041, spec 7.7.1)
// ---------------------------------------------------------------------------

async function genDiff(ctx: Ctx): Promise<void> {
  const parse = corpusJson(ctx, 'diff/parse.json').cases as Obj[];
  const reconstruct = corpusJson(ctx, 'diff/reconstruct.json').cases as Obj[];
  ctx.writer.write('diff.json', {
    note: 'parse: parseUnifiedDiff(raw) of server/src/adapters/git/diff-parser.ts. reconstruct: the stored-diff rebuild of pull.repo.ts (rows → raw → parse); rows are in path byte order, patch null/empty rows are skipped. `files` use the Node field names (path, additions, deletions, hunks[file, oldStart, oldLines, newStart, newLines, newLineNumbers]).',
    parse: parse.map((c) => {
      const raw = textOf(c as TextSpec);
      return { name: c.name, raw, diff: dataOf(parseDiff(ctx, raw)) };
    }),
    reconstruct: await Promise.all(
      reconstruct.map(async (c) => {
        const rows = rowsOf(c.rows.map((r: Obj) => ({ path: r.path, patchLines: r.patchLines })));
        const diff = await ctx.N.pullRepo.storedDiff(selectDb(rows), 'pr');
        return { name: c.name, rows, raw: diff.raw, diff: dataOf(diff) };
      }),
    ),
  });
}

// ---------------------------------------------------------------------------
// chunks.json — sliceDiff, splitOversizeDiff
// ---------------------------------------------------------------------------

function genChunks(ctx: Ctx): void {
  const { rc } = ctx.N;
  const slices = (corpusJson(ctx, 'chunks/slice.json').cases as Obj[]).map((c) => {
    const raw = textOf(c as TextSpec);
    const diff = parseDiff(ctx, raw);
    return {
      name: c.name,
      raw,
      fileLabels: diff.files.map((f: Obj) => f.path),
      slices: (c.paths as string[]).map((path) => ({ path, text: rc.sliceDiff(diff, path) })),
    };
  });

  const splits = (corpusJson(ctx, 'chunks/split.json').cases as Obj[]).map((c) => {
    let raw = textOf(c as TextSpec);
    if (c.padTo) {
      if (raw.length > c.padTo) throw new Error(`${c.name}: raw is already longer than padTo`);
      raw += 'x'.repeat(c.padTo - raw.length);
    }
    const r = rc.splitOversizeDiff(raw, c.max);
    const out: Obj = { name: c.name, max: c.max, raw, parts: r.parts, truncated: r.truncated };
    if (c.synth) out.synth = c.synth;
    if (c.padTo) out.padTo = c.padTo;
    return out;
  });

  ctx.writer.write('chunks.json', {
    note: 'slice: sliceDiff(parseUnifiedDiff(raw), path) for each path (map-reduce chunk text). split: splitOversizeDiff(raw, max) → parts and truncated. Large cases rebuild `raw` from `synth` (README "Synthetic diffs"; padTo = extend the last line with x until raw has that many UTF-16 units) and appear as $blob digests.',
    slice: slices,
    split: splits,
  });
}

// ---------------------------------------------------------------------------
// reduce.json — reduce, score, blockers, ordered map
// ---------------------------------------------------------------------------

async function genReduce(ctx: Ctx): Promise<void> {
  const { rc, pool } = ctx.N;
  const spec = corpusJson(ctx, 'reduce/cases.json');
  resetFindingCounter();

  const reduce = (spec.reduce as Obj[]).map((c) => {
    const partials = (c.partials as Obj[]).map(expandReview);
    return { name: c.name, partials, merged: dataOf(rc.reduceReviews(partials)) };
  });
  const finding = (sev: string): Obj => ({ severity: sev });
  const score = (spec.score as Obj[]).map((c) => {
    const sev: string[] = c.repeat ? Array(c.repeat.count).fill(c.repeat.severity) : c.severities;
    return { name: c.name, severities: c.repeat ? undefined : sev, repeat: c.repeat, score: rc.scoreFromFindings(sev.map(finding)) };
  });
  const blockers = (spec.blockers as Obj[]).map((c) => ({
    name: c.name,
    severities: c.severities,
    counts: Object.fromEntries((spec.failOn as string[]).map((f) => [f, rc.countBlockers((c.severities as string[]).map(finding), f)])),
  }));

  ctx.writer.write('reduce.json', {
    note: 'reduce: reduceReviews(partials). score: scoreFromFindings (100 − 35/12/3 per CRITICAL/WARNING/SUGGESTION, clamped to 0..100). blockers: countBlockers(findings, failOn) for every gate. mapOrdered: ordered map with a concurrency limit, driven by released deferreds (script order); `started` lists item indexes in the order their function began; on failure no new item starts, in-flight items are awaited and the first error (by time) is rethrown.',
    reduce,
    score,
    blockers,
    mapOrdered: await Promise.all((spec.mapOrdered as Obj[]).map((c) => runPool(pool, c))),
  });
}

async function runPool(pool: Obj, c: Obj): Promise<Obj> {
  const defs = Array.from({ length: c.n as number }, () => deferred<string>());
  const started: number[] = [];
  const outcome = pool
    .mapOrdered(
      defs.map((_, i) => i),
      c.limit,
      async (_item: number, index: number) => {
        started.push(index);
        return defs[index]!.promise;
      },
    )
    .then(
      (results: string[]) => ({ results }),
      (err: Error) => ({ error: err.message }),
    );
  await settleMany();
  const initialStarted = [...started];
  const steps: Obj[] = [];
  for (const step of c.script as Obj[]) {
    if ('ok' in step) defs[step.release]!.resolve(step.ok);
    else defs[step.release]!.reject(new Error(step.fail));
    await settleMany();
    steps.push({ ...step, startedAfter: [...started] });
  }
  const result = await Promise.race([outcome, settleMany(20).then(() => ({ hung: true }))]);
  if ('hung' in (result as Obj)) throw new Error(`mapOrdered case ${c.name} never settled: release every started item`);
  return { name: c.name, n: c.n, limit: c.limit, initialStarted, steps, started, ...(result as Obj) };
}

// ---------------------------------------------------------------------------
// grounding.json — citation grounding, grounding summary, scope policy
// ---------------------------------------------------------------------------

function genGrounding(ctx: Ctx): void {
  const { rc } = ctx.N;
  const spec = corpusJson(ctx, 'grounding/cases.json');
  resetFindingCounter();
  const diffs: Obj = {};
  for (const [name, d] of Object.entries(spec.diffs as Obj)) {
    const raw = textOf(d as TextSpec);
    diffs[name] = { raw, diff: dataOf(parseDiff(ctx, raw)) };
  }
  const ground = (spec.cases as Obj[]).map((c) => {
    const diff = parseDiff(ctx, diffs[c.diff].raw);
    const findings = (c.findings as Obj[]).map(expandFinding);
    const res = rc.groundFindings(findings, diff);
    const idx = (f: Obj): number => findings.indexOf(f);
    return {
      name: c.name,
      diff: c.diff,
      findings,
      keptIndexes: res.kept.map(idx),
      dropped: res.dropped.map((d: Obj) => ({ index: idx(d.finding), reason: d.reason })),
      summary: rc.groundingSummary(res),
    };
  });
  const scope = (spec.scope as Obj[]).map((c) => {
    const findings = (c.findings as Obj[]).map(expandFinding);
    const res = rc.applyScopePolicy(findings, c.intent ?? undefined);
    return {
      name: c.name,
      intent: c.intent,
      findings,
      result: { findings: dataOf(res.findings), outOfScopeCount: res.outOfScopeCount, outOfScopeCriticalCount: res.outOfScopeCriticalCount },
    };
  });
  ctx.writer.write('grounding.json', {
    note: 'ground: groundFindings(findings, parseUnifiedDiff(diff)) → indexes into `findings` of the kept and dropped ones (dropped carry the reason text) and groundingSummary. A pure-deletion hunk grounds on its declared new range (newStart..newStart+max(newLines,1)-1). scope: applyScopePolicy(findings, intent) (Phase 4 has no intent: Go only needs the no-intent rows).',
    diffs,
    ground,
    scope,
  });
}

// ---------------------------------------------------------------------------
// prompt.json — assemblePrompt, wrapUntrusted, and the server-side prompt builders
// ---------------------------------------------------------------------------

function genPrompt(ctx: Ctx): void {
  const { rc, prompt, skillsIndex, skillsDomain } = ctx.N;
  const spec = expandText(corpusJson(ctx, 'prompt/cases.json'));

  const assemble = (spec.assemble as Obj[]).map((c) => {
    const a = rc.assemblePrompt(c.parts);
    return { name: c.name, parts: c.parts, messages: dataOf(a.messages), assembly: dataOf(a.assembly), sections: dataOf(a.sections) };
  });
  const wrap = (spec.wrap as Obj[]).map((c) => ({ ...c, out: rc.wrapUntrusted(c.label, c.content) }));
  const label = (spec.contextLabel as string[]).map((p) => ({ path: p, label: rc.contextLabel(p) }));
  const estimate = (spec.estimateTokens as string[]).map((t) => ({ text: t, units: t.length, tokens: rc.estimateTokens(t) }));
  const task = (spec.taskLine as Obj[]).map((p) => ({ pull: p, line: prompt.taskLine(p) }));
  const rank = (spec.rankNote as number[][]).map(([hot, changed]) => ({ hot, changed, note: prompt.rankNote(hot, changed) }));
  const hot = (spec.hotFiles as Obj[][]).map((rows) => ({ rows, hot: prompt.hotFiles(rows) }));
  const callers = (spec.callersDigest as Obj[][]).map((rows) => ({ rows, digest: prompt.callersDigest(rows) ?? null }));
  const skillBlock = (spec.skillBlock as Obj[]).map((sk) => ({ skill: sk, block: skillsIndex.renderSkillBlock(sk) }));
  const skillsLine = (spec.skillsLogLine as Obj[]).map((c) => ({
    ...c,
    line: skillsDomain.skillsLogLine(c.skills.map((s: Obj) => ({ ...s, description: '', body: '' })), c.tokens, c.repeat),
    used: dataOf(skillsDomain.skillsUsed(c.skills)),
  }));

  // The PR description cut: the one place where Node leaves a lone surrogate (see handwritten/prompt_surrogate.json).
  const body = 'a'.repeat(3999) + '\u{1F600}' + 'tail';
  const cut = rc.assemblePrompt({ system: 'S', prDescription: body, diff: 'd' });
  const dangling = cut.assembly.pr_description;
  const nodeSurrogate = {
    inputUnits: body.length,
    descriptionUnits: dangling.length,
    lastUnitIsHighSurrogate: dangling.charCodeAt(dangling.length - 1) >= 0xd800 && dangling.charCodeAt(dangling.length - 1) <= 0xdbff,
    truncatedFlag: cut.sections.find((s: Obj) => s.name === 'pr_description').truncated,
  };

  // Consistency check of the hand-written surrogate expectation against what Node really does.
  const hw = readJson<Obj>(join(ctx.testdata, 'handwritten', 'prompt_surrogate.json'));
  if (
    hw.inputUnits !== nodeSurrogate.inputUnits ||
    hw.nodeBehaviour.descriptionUnits !== nodeSurrogate.descriptionUnits ||
    hw.nodeBehaviour.lastUnitIsHighSurrogate !== nodeSurrogate.lastUnitIsHighSurrogate ||
    hw.nodeBehaviour.truncatedFlag !== nodeSurrogate.truncatedFlag
  ) {
    throw new Error('handwritten/prompt_surrogate.json no longer matches the Node behaviour');
  }
  const probe = rc.assemblePrompt({ system: 'S', prDescription: 'a'.repeat(3999), diff: 'd' }).sections.find((x: Obj) => x.name === 'pr_description');
  if (probe.chars !== hw.goExpected.sectionChars || probe.tokens !== hw.goExpected.sectionTokens) {
    throw new Error('handwritten/prompt_surrogate.json: goExpected section size is wrong');
  }

  ctx.writer.write('prompt.json', {
    note: 'assemble: reviewer-core assemblePrompt(parts) → messages (system, user), the trace assembly and the per-section metadata (chars/tokens in UTF-16 units). wrap/contextLabel/estimateTokens/taskLine/rankNote/hotFiles/callersDigest/skillBlock/skillsLogLine: the helpers of the same names. nodeSurrogate documents what Node does when the 4000-unit cut falls inside a surrogate pair; Go must drop the dangling half instead (handwritten/prompt_surrogate.json).',
    assemble,
    wrap,
    contextLabel: label,
    estimateTokens: estimate,
    taskLine: task,
    rankNote: rank,
    hotFiles: hot,
    callersDigest: callers,
    skillBlock,
    skillsLogLine: skillsLine,
    nodeSurrogate,
  });
}

// ---------------------------------------------------------------------------
// schema.json — the strict Review schema, parseWithRepair, extractJson
// ---------------------------------------------------------------------------

function genSchema(ctx: Ctx): void {
  const { rc, shared } = ctx.N;
  const spec = corpusJson(ctx, 'schema/cases.json');
  const strict = rc.toJsonSchema(shared.Review, 'Review');
  const parse = (spec.parse as Obj[]).map((c) => {
    const r = rc.parseWithRepair(shared.Review, c.raw);
    return r.ok
      ? { name: c.name, raw: c.raw, ok: true, data: dataOf(r.data) }
      : { name: c.name, raw: c.raw, ok: false, error: r.error, repromptMessage: r.repromptMessage };
  });
  const extract = (spec.extractJson as string[]).map((t) => ({ text: t, out: rc.extractJson(t) }));
  ctx.writer.write('schema.json', {
    note: 'strict: toJsonSchema(Review, "Review") — the schema sent as response_format.json_schema.schema (D4010, P7). parse: parseWithRepair(Review, raw): on success the Zod-parsed data (unknown keys stripped, absent optional keys stay absent), else the issue list and the reprompt message. Invalid-JSON error texts are V8 messages (Go compares only the "Output was not valid JSON: " prefix and the reprompt tail). extractJson: the fallback extraction.',
    strict,
    parse,
    extractJson: extract,
  });
}
