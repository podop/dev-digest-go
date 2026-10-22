/** engine.json — reviewPullRequest runs with a scripted LLM (single pass, map-reduce, split, errors, modes). */
import {
  corpusJson, expandReview, loadPr, modeDiffSpec, parseDiff, rawDiffOf, resetFindingCounter, type Obj,
} from './common.mts';
import type { Ctx } from './ctx.mts';

const dataOf = (v: unknown): unknown => JSON.parse(JSON.stringify(v));
const MODEL = 'deepseek/deepseek-v4-flash';

export async function genEngine(ctx: Ctx): Promise<void> {
  const spec = corpusJson(ctx, 'engine/cases.json');
  const cases: Obj[] = [];
  for (const raw of spec.cases as Obj[]) {
    const c = raw.modeCase ? modeCaseOf(raw) : raw;
    cases.push(await runCase(ctx, c));
  }
  ctx.writer.write('engine.json', {
    note: 'reviewPullRequest(input) with a scripted LLM. `calls` are the completeStructured requests in call order (chunkIndex = the index onPrompt reported just before). Events: `events` in Node order; `eventGroups` split them into pre (before the first chunk), per-chunk tool/result pairs and post — compare groups, not the global order, when concurrency > 1. `usage` is every onUsage report. `outcome` is the ReviewOutcome or `error` the thrown message. Large strings are $blob digests; rebuild such diffs from `diffSynth`.',
    cases,
  });
}

function modeCaseOf(c: Obj): Obj {
  return {
    name: c.name,
    input: { systemPrompt: 'You are a general reviewer.', strategy: c.strategy, task: 'Review pull request #1 "Mode" by sam.' },
    diff: modeDiffSpec(c.files),
    script: { default: { review: { verdict: 'approve', summary: 'ok', score: 100, findings: [] }, usage: [{ tokensIn: 10, tokensOut: 1, costUsd: 0.01 }] } },
    mode: true,
    files: c.files,
  };
}

async function runCase(ctx: Ctx, c: Obj): Promise<Obj> {
  const { rc, shared, prompt, skillsIndex } = ctx.N;
  resetFindingCounter();
  const raw = await rawDiffOf(ctx, c.diff);
  const diff = parseDiff(ctx, raw);

  // Resolve the input.
  const inp = c.input as Obj;
  const resolved: Obj = { systemPrompt: inp.systemPrompt, strategy: inp.strategy };
  if (inp.concurrency !== undefined) resolved.concurrency = inp.concurrency;
  if (inp.skills) {
    resolved.skillSpecs = inp.skills;
    resolved.skills = (inp.skills as Obj[]).map((s) => skillsIndex.renderSkillBlock(s));
  }
  if (inp.specs) resolved.specs = inp.specs;
  if (inp.callers !== undefined) resolved.callers = inp.callers;
  if (inp.repoMap !== undefined) resolved.repoMap = inp.repoMap;
  if (inp.prDescriptionFrom) resolved.prDescription = loadPr(ctx, inp.prDescriptionFrom).pull.body;
  if (inp.prDescription !== undefined) resolved.prDescription = inp.prDescription;
  let task: string | undefined = inp.task;
  if (inp.taskFrom) task = prompt.taskLine(loadPr(ctx, inp.taskFrom).pull);
  if (task !== undefined) {
    if (inp.rankNote) task += prompt.rankNote(inp.rankNote.hot, inp.rankNote.changed);
    resolved.task = task;
  }

  // Expand and validate the script like a provider would (Zod parse).
  const steps: Obj = {};
  const expandStep = (s: Obj): Obj => {
    if (s.error !== undefined) return { error: s.error, usage: s.usage ?? [] };
    const review = expandReview(s.review);
    shared.Review.parse(review); // corpus self-check
    return { review, raw: s.raw ?? JSON.stringify(review), usage: s.usage ?? [] };
  };
  const script: Obj = { chunks: {} };
  if (c.script.default) script.default = expandStep(c.script.default);
  for (const [k, s] of Object.entries(c.script.chunks ?? {})) script.chunks[k] = expandStep(s as Obj);
  void steps;

  // Run.
  const events: Obj[] = [];
  const promptEvents: Obj[] = [];
  const calls: Obj[] = [];
  const usage: Obj[] = [];
  let lastChunk = -1;
  let polls = 0;
  const llm = {
    id: 'openrouter',
    async completeStructured(req: Obj): Promise<Obj> {
      const idx = lastChunk;
      calls.push({
        chunkIndex: idx,
        schemaName: req.schemaName,
        model: req.model,
        ...(req.temperature !== undefined ? { temperature: req.temperature } : {}),
        maxRetries: req.maxRetries,
        messages: dataOf(req.messages),
      });
      const step: Obj | undefined = script.chunks[String(idx)] ?? script.default;
      if (!step) throw new Error(`no script step for chunk ${idx}`);
      let tokensIn = 0;
      let tokensOut = 0;
      let costUsd: number | null = 0;
      for (const u of step.usage as Obj[]) {
        usage.push({ chunkIndex: idx, ...u });
        req.onUsage?.(u);
        tokensIn += u.tokensIn;
        tokensOut += u.tokensOut;
        costUsd = rc.addCost(costUsd, u.costUsd);
      }
      if (step.error !== undefined) throw new Error(step.error);
      return {
        data: shared.Review.parse(step.review),
        model: req.model,
        tokensIn,
        tokensOut,
        costUsd,
        raw: step.raw,
        attempts: Math.max(1, (step.usage as Obj[]).length),
      };
    },
  };

  const input: Obj = {
    systemPrompt: resolved.systemPrompt,
    model: MODEL,
    diff,
    llm,
    strategy: resolved.strategy,
    ...(resolved.concurrency !== undefined ? { concurrency: resolved.concurrency } : {}),
    ...(resolved.skills ? { skills: resolved.skills } : {}),
    ...(resolved.specs ? { specs: resolved.specs } : {}),
    ...(resolved.callers !== undefined ? { callers: resolved.callers } : {}),
    ...(resolved.repoMap !== undefined ? { repoMap: resolved.repoMap } : {}),
    ...(resolved.prDescription ? { prDescription: resolved.prDescription } : {}),
    ...(resolved.task !== undefined ? { task: resolved.task } : {}),
    onEvent: (e: Obj) => events.push({ kind: e.kind, msg: e.msg, ...(e.data !== undefined ? { data: e.data } : {}) }),
    onPrompt: (e: Obj) => {
      lastChunk = e.chunkIndex;
      promptEvents.push(dataOf(e) as Obj);
    },
    onUsage: undefined,
    checkCancelled: () => {
      polls++;
      if (inp.cancelAtPoll !== undefined && polls >= inp.cancelAtPoll) throw new Error('review cancelled');
    },
  };
  delete input.onUsage;

  let outcome: Obj | undefined;
  let error: string | undefined;
  try {
    outcome = await rc.reviewPullRequest(input);
  } catch (err) {
    error = (err as Error).message;
  }

  const out: Obj = {
    name: c.name,
    input: { ...resolved, ...(inp.cancelAtPoll !== undefined ? { cancelAtPoll: inp.cancelAtPoll } : {}) },
    diffRaw: raw,
    ...(c.diff.synth ? { diffSynth: c.diff.synth } : {}),
    ...(c.mode ? { files: c.files } : {}),
    script,
    calls,
    promptEvents,
    events,
    eventGroups: groupEvents(events),
    usage,
    pollsMade: polls,
  };
  if (outcome) {
    out.outcome = dataOf({
      review: outcome.review,
      grounding: outcome.grounding,
      dropped: outcome.dropped,
      mode: outcome.mode,
      assembly: outcome.assembly,
      chunks: outcome.chunks,
      tokensIn: outcome.tokensIn,
      tokensOut: outcome.tokensOut,
      costUsd: outcome.costUsd,
      raw: outcome.raw,
    });
  } else {
    out.error = error;
  }
  return out;
}

/** pre = before the first chunk tool event; chunks = tool/result pairs by label; post = the rest. */
function groupEvents(events: Obj[]): Obj {
  const pre: Obj[] = [];
  const post: Obj[] = [];
  const chunks: Obj[] = [];
  const byLabel = new Map<string, Obj>();
  for (const e of events) {
    if (e.kind === 'tool' && e.data?.file !== undefined) {
      const g: Obj = { label: e.data.file, tool: e };
      byLabel.set(e.data.file, g);
      chunks.push(g);
      continue;
    }
    const m = e.kind === 'result' ? /^(.*): \d+ candidate finding\(s\)$/.exec(e.msg) : null;
    if (m && byLabel.has(m[1]!)) {
      byLabel.get(m[1]!)!.result = e;
      continue;
    }
    (chunks.length === 0 ? pre : post).push(e);
  }
  return { pre, chunks, post };
}
