/**
 * brief.json — the Node PR brief: input builders, prompt and trim, normalisation and grounding, staleness,
 * stored-brief validation, BriefService flows over fake ports (with the real LlmBriefModel over a fake LLM
 * provider), the one-generation-per-PR marker and the GitHub issue source (AC5044–AC5049, EC5019–EC5024).
 */
import { corpusJson, expandText, resolveRefs, recLogger, showError, showLogs, type Obj } from './common.mts';
import { installFakeDate, LogicalClock, settleMany } from './lib.mts';
import type { Ctx } from './ctx.mts';

function mergeShallow(base: Obj, patch: Obj | undefined): Obj {
  return { ...base, ...(patch ?? {}) };
}

// ---------------------------------------------------------------------------
// Pure rules
// ---------------------------------------------------------------------------

function genInput(ctx: Ctx): Obj {
  const { N } = ctx;
  const I = N.briefInput;
  const spec = expandText(corpusJson(ctx, 'brief/input.json'));
  return {
    ranges: (spec.ranges as Obj[]).map((c) => ({ name: c.name, patch: c.patch, ranges: I.changedRanges(c.patch) })),
    selectFiles: (spec.selectFiles as Obj[]).map((c) => {
      const r = I.selectFiles(c.files);
      return { name: c.name, files: c.files, selected: r.files, moreFiles: r.moreFiles };
    }),
    selectFindings: (spec.selectFindings as Obj[]).map((c) => {
      const findings = [
        ...(c.findings as Obj[]),
        ...Array.from({ length: (c.plusCritical as number) ?? 0 }, (_, i) => ({ severity: 'CRITICAL', file: 'src/crit.ts', startLine: i + 1, endLine: i + 1, title: `critical ${i + 1}` })),
      ];
      return { name: c.name, findings, selected: I.selectFindings(findings) };
    }),
    blast: (spec.blast as Obj[]).map((c) => {
      const usable = I.usableBlast(c.blast);
      return { name: c.name, blast: c.blast, usable: usable !== null, prompt: usable ? I.promptBlast(usable) : null };
    }),
    linkedIssue: (spec.linkedIssue as Obj[]).map((c) => ({ body: c.body, number: I.linkedIssueNumber(c.body) })),
    missing: (spec.missing as Obj[]).map((c) => ({ name: c.name, facts: c.facts, result: I.buildMissingInputs(c.facts) })),
    truncate: (spec.truncate as Obj[]).map((c) => ({ ...c, result: I.truncateText(c.text, c.max) })),
  };
}

function genPrompt(ctx: Ctx): Obj[] {
  const { N } = ctx;
  const P = N.briefPrompt;
  const spec = expandText(corpusJson(ctx, 'brief/prompt.json'));
  const out: Obj[] = [];
  for (const c of spec.cases as Obj[]) {
    const input = mergeShallow(spec.base, c.input);
    const messages = P.buildMessages(input);
    const estimate = P.estimateInputTokens(input);
    let maxTokens: number | undefined;
    if (c.maxTokens === 'exact') maxTokens = estimate;
    else if (c.maxTokens === 'exact-1') maxTokens = estimate - 1;
    else if (typeof c.maxTokens === 'number') maxTokens = c.maxTokens;
    const trim = maxTokens === undefined ? P.trimToBudget(input) : P.trimToBudget(input, maxTokens);
    const trimmedMessages = P.buildMessages(trim.input);
    out.push({
      name: c.name,
      maxTokens: maxTokens ?? 8000,
      input,
      sources: P.promptSources(input),
      userMessage: messages[1].content,
      estimate,
      trim: {
        droppedSpecs: trim.droppedSpecs,
        droppedFiles: trim.droppedFiles,
        estTokens: trim.estTokens,
        moreFiles: trim.input.moreFiles,
        files: (trim.input.files as Obj[]).map((f) => f.path),
        specs: (trim.input.specs as Obj[]).map((s) => s.path),
        userMessage: trimmedMessages[1].content === messages[1].content ? '$same' : trimmedMessages[1].content,
        sections: N.briefSections.briefPromptSections(trim.input),
      },
      roles: messages.map((m: Obj) => m.role),
      systemIsTheNodeConstant: messages[0].content === P.BRIEF_SYSTEM_PROMPT,
    });
  }
  return out;
}

function genNormalize(ctx: Ctx): { cases: Obj[]; stale: Obj[] } {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'brief/normalize.json'));
  const cases = (spec.cases as Obj[]).map((c) => {
    const facts = mergeShallow(spec.base, c.facts);
    const res = N.briefNormalize.normalizeBrief(c.raw, facts);
    return { name: c.name, facts, raw: c.raw, result: res };
  });
  const stale = (spec.stale as Obj[]).map((c) => ({
    ...c,
    result: c.currentPromptVersion === null ? N.briefNormalize.isStale(c.stored, c.current) : N.briefNormalize.isStale(c.stored, c.current, c.currentPromptVersion),
  }));
  return { cases, stale };
}

function genStored(ctx: Ctx, docs: Obj[]): Obj[] {
  const { N } = ctx;
  return docs.map((c) => {
    const r = N.shared.PrBrief.safeParse(c.doc);
    return {
      name: c.name,
      doc: c.doc,
      ok: r.success,
      ...(r.success ? { parsed: r.data } : { issues: (r.error.issues as Obj[]).map((i) => ({ code: i.code, path: i.path, message: i.message })) }),
    };
  });
}

function constants(ctx: Ctx): Obj {
  const { N } = ctx;
  const K = N.briefConstants;
  return {
    systemPrompt: N.briefPrompt.BRIEF_SYSTEM_PROMPT,
    schemaName: K.BRIEF_SCHEMA_NAME,
    promptVersion: K.PROMPT_VERSION,
    language: K.BRIEF_LANGUAGE,
    limits: {
      inputMaxTokens: K.INPUT_MAX_TOKENS,
      llmMaxOutputTokens: K.LLM_MAX_OUTPUT_TOKENS,
      llmMaxRetries: K.LLM_MAX_RETRIES,
      generationTimeoutMs: K.GENERATION_TIMEOUT_MS,
      issueFetchTimeoutMs: K.ISSUE_FETCH_TIMEOUT_MS,
      descriptionMaxChars: K.DESCRIPTION_MAX_CHARS,
      issueBodyMaxChars: K.ISSUE_BODY_MAX_CHARS,
      maxFiles: K.MAX_FILES,
      maxRangesPerFile: K.MAX_RANGES_PER_FILE,
      maxFindings: K.MAX_FINDINGS,
      findingTitleMaxChars: K.FINDING_TITLE_MAX_CHARS,
      maxCallers: K.MAX_CALLERS,
      summaryMax: K.SUMMARY_MAX,
      risksMax: K.RISKS_MAX,
      riskTitleMax: K.RISK_TITLE_MAX,
      riskExplanationMax: K.RISK_EXPLANATION_MAX,
      riskKindMax: K.RISK_KIND_MAX,
      focusMax: K.FOCUS_MAX,
      focusReasonMax: K.FOCUS_REASON_MAX,
    },
  };
}

// ---------------------------------------------------------------------------
// The service
// ---------------------------------------------------------------------------

interface Rec {
  llmRequests: Obj[];
  upserts: Obj[];
  promptLog: Obj[];
  specCalls: Obj[];
  issueCalls: Obj[];
  intentCalls: number;
  blastCalls: number;
}
export const newRec = (): Rec => ({ llmRequests: [], upserts: [], promptLog: [], specCalls: [], issueCalls: [], intentCalls: 0, blastCalls: 0 });

export class Gate {
  private readonly waiters: Array<{ resolve: () => void; reject: (e: unknown) => void }> = [];
  wait(signal: AbortSignal | undefined): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      this.waiters.push({ resolve, reject });
      signal?.addEventListener('abort', () => reject(signal.reason), { once: true });
    });
  }
  release(n: number): void {
    for (let i = 0; i < n; i++) this.waiters.shift()?.resolve();
  }
  fail(message: string): void {
    this.waiters.shift()?.reject(new Error(message));
  }
}

export function makeService(ctx: Ctx, w: Obj, state: { brief: Obj | null }, rec: Rec, clock: LogicalClock, gate?: Gate) {
  const { N } = ctx;
  const pullFor = (id: string): Obj | null => {
    if (w.pullMissing) return null;
    if (id === w.pull.id) return { ...w.pull, repo: { ...w.pull.repo } };
    if (id === 'pr-2') return { ...w.pull, id: 'pr-2', repo: { ...w.pull.repo } };
    return null;
  };
  const store = {
    findPull: async (_ws: string, id: string) => pullFor(id),
    listFiles: async () => (w.files as Obj[]).map((f) => ({ path: f.path, additions: f.additions, deletions: f.deletions, patch: f.patch })),
    currentReviews: async () => (w.reviews as Obj[]).map((r) => ({ agentId: r.agentId, createdAt: new Date(r.createdAt), findings: r.findings.map((f: Obj) => ({ ...f })) })),
    enabledSkills: async (agentId: string) => (w.skills[agentId] ?? []).map((s: Obj) => ({ ...s })),
    // The repository reads pr_brief.json with PrBrief.safeParse and answers null on a failure.
    getBrief: async () => {
      if (state.brief === null) return null;
      const r = N.shared.PrBrief.safeParse(state.brief);
      return r.success ? r.data : null;
    },
    upsertBrief: async (_prId: string, brief: Obj) => {
      rec.upserts.push(brief);
      state.brief = JSON.parse(JSON.stringify(brief));
    },
  };
  const intent = {
    get: async () => {
      rec.intentCalls++;
      if (w.intent && w.intent.throws) throw new Error(w.intent.throws);
      return w.intent;
    },
  };
  const blast = {
    getBlast: async () => {
      rec.blastCalls++;
      if (w.blast.throws) throw new Error(w.blast.throws);
      return w.blast;
    },
  };
  const specs = {
    resolveForRun: async (input: Obj) => {
      rec.specCalls.push({ agentId: input.agentId, repoId: input.repoId, repo: input.repo, base: input.base, headSha: input.headSha, skills: input.skills });
      return { docs: (w.specs[input.agentId] ?? []).map((d: Obj) => ({ ...d })) };
    },
  };
  const issues = {
    fetch: async (repo: Obj, n: number) => {
      rec.issueCalls.push({ repo, number: n });
      if (w.issue === null) return null;
      if (w.issue.delayMs) await new Promise((r) => setTimeout(r, w.issue.delayMs as number));
      return { number: w.issue.number, title: w.issue.title, body: w.issue.body };
    },
  };
  const llm = async (provider: string) => {
    if (w.llm.error === 'config') throw new N.errors.ConfigError(`${provider.toUpperCase()}_API_KEY is not configured`);
    return {
      completeStructured: async (req: Obj) => {
        rec.llmRequests.push({
          provider,
          model: req.model,
          schemaName: req.schemaName,
          temperature: req.temperature,
          maxTokens: req.maxTokens,
          maxRetries: req.maxRetries,
          hasUsageObserver: typeof req.onUsage === 'function',
          hasSignal: req.signal instanceof AbortSignal,
          roles: (req.messages as Obj[]).map((m) => m.role),
          systemIsTheNodeConstant: req.messages[0].content === N.briefPrompt.BRIEF_SYSTEM_PROMPT,
          user: req.messages[1].content,
        });
        clock.bump(1500);
        if (gate) await gate.wait(req.signal);
        if (w.llm.hang) await new Promise((_, reject) => req.signal.addEventListener('abort', () => reject(req.signal.reason), { once: true }));
        const usages: Obj[] = w.llm.usages ?? [{ tokensIn: w.llm.tokensIn, tokensOut: w.llm.tokensOut, costUsd: w.llm.costUsd }];
        for (const u of usages) req.onUsage?.(u);
        if (w.llm.error === 'other') throw new Error('provider exploded: secret details of the pull request');
        if (w.llm.error === 'invalid') throw new Error('Output did not match the schema after the reprompt');
        return { data: req.schema.parse(w.llm.data), model: req.model, tokensIn: w.llm.tokensIn, tokensOut: w.llm.tokensOut, costUsd: w.llm.costUsd, attempts: w.llm.attempts ?? 1, raw: '' };
      },
    };
  };
  const model = new N.briefModel.LlmBriefModel({
    resolveModel: async () => {
      if (w.resolveError) throw new Error(w.resolveError);
      return { provider: w.model.provider, model: w.model.model };
    },
    llm,
  });
  return new N.briefService.BriefService({
    store,
    intent,
    blast,
    specs,
    issues,
    model,
    clock: () => new Date(w.now),
    promptLog: { assembled: (e: Obj) => void rec.promptLog.push(e) },
    timeoutMs: w.timeoutMs,
  });
}

const worldOf = (w: Obj): Obj => ({
  workspace: w.workspace,
  now: w.now,
  model: w.model,
  timeoutMs: w.timeoutMs,
  pullMissing: w.pullMissing === true,
  pull: w.pull,
  files: w.files,
  reviews: w.reviews,
  skills: w.skills,
  specs: w.specs,
  intent: w.intent,
  blast: w.blast,
  issue: w.issue,
  llm: w.llm,
  resolveError: w.resolveError ?? null,
});

async function genFlows(ctx: Ctx): Promise<{ cases: Obj[]; scenarios: Obj[]; issueSource: Obj[]; stored: Obj[] }> {
  const { N } = ctx;
  const spec = resolveRefs(ctx, expandText(corpusJson(ctx, 'brief/flows.json')));
  const defaults = spec.defaults as Obj;
  const clock = new LogicalClock();
  const restoreDate = installFakeDate(clock);
  const cases: Obj[] = [];
  const scenarios: Obj[] = [];
  const issueSource: Obj[] = [];
  try {
    for (const c of spec.cases as Obj[]) {
      const state: { brief: Obj | null } = { brief: null };
      let storedBefore: Obj | null = null;
      if (c.before !== undefined) {
        clock.reset();
        const wb = mergeWorld(defaults, c.before);
        const svc = makeService(ctx, wb, state, newRec(), clock);
        await svc.generate(wb.workspace, wb.pull.id, recLogger());
        if (c.storedPatch) state.brief = { ...state.brief, ...c.storedPatch };
        storedBefore = state.brief;
      }
      clock.reset();
      const w = mergeWorld(defaults, c.world);
      const rec = newRec();
      const svc = makeService(ctx, w, state, rec, clock);
      const log = recLogger();
      let outcome: Obj;
      try {
        outcome = { response: c.call === 'get' ? await svc.get(w.workspace, w.pull.id) : await svc.generate(w.workspace, w.pull.id, log) };
      } catch (e) {
        outcome = { error: showError(e) };
      }
      const same = (v: unknown): unknown => (outcome.response?.brief && JSON.stringify(v) === JSON.stringify(outcome.response.brief) ? '$response.brief' : v);
      cases.push({
        name: c.name,
        call: c.call,
        world: worldOf(w),
        stored: storedBefore,
        ...outcome,
        logs: showLogs(log.lines),
        ...rec,
        upserts: rec.upserts.map(same),
        storedAfter: same(state.brief),
      });
    }

    for (const sc of spec.scenarios as Obj[]) {
      clock.reset();
      const w = mergeWorld(defaults, sc.world);
      const state: { brief: Obj | null } = { brief: null };
      const rec = newRec();
      const gate = new Gate();
      const svc = makeService(ctx, w, state, rec, clock, gate);
      const callers: Record<string, { promise?: Promise<void>; result?: Obj; log: ReturnType<typeof recLogger> }> = {};
      for (const step of sc.steps as Obj[]) {
        if (step.start !== undefined) {
          const name = step.start as string;
          const entry: (typeof callers)[string] = { log: recLogger() };
          callers[name] = entry;
          entry.promise = svc.generate(w.workspace, step.pr, entry.log).then(
            (r: Obj) => void (entry.result = { response: r }),
            (e: unknown) => void (entry.result = { error: showError(e) }),
          );
        } else if (step.settle) await settleMany(10);
        else if (step.release !== undefined) {
          gate.release(step.release as number);
          await settleMany(10);
        } else if (step.fail !== undefined) {
          gate.fail(step.fail as string);
          await settleMany(10);
        } else if (step.await !== undefined) await callers[step.await as string]!.promise;
      }
      scenarios.push({
        name: sc.name,
        steps: sc.steps,
        callers: Object.fromEntries(Object.entries(callers).map(([k, v]) => [k, { result: v.result ?? null, logs: showLogs(v.log.lines) }])),
        llmRequests: rec.llmRequests.length,
        upserts: rec.upserts.length,
      });
    }

    // GitHub issue source: raced against a deadline (timer faked).
    const realSet = globalThis.setTimeout;
    const realClear = globalThis.clearTimeout;
    try {
      for (const c of spec.issueSource as Obj[]) {
        const timers = new Map<number, () => void>();
        let seq = 0;
        globalThis.setTimeout = ((fn: () => void, _ms: number) => {
          const id = ++seq;
          timers.set(id, fn);
          return id;
        }) as unknown as typeof setTimeout;
        globalThis.clearTimeout = ((id: number) => void timers.delete(id)) as unknown as typeof clearTimeout;
        const github = async () => {
          if (c.github.factoryError) throw new N.errors.ConfigError(c.github.factoryError);
          return {
            getIssue: async () => {
              if (c.github.error) throw new Error(c.github.error);
              if (c.github.hang) return new Promise(() => undefined);
              return c.github.issue;
            },
          };
        };
        // The real source imported through the brief module's infrastructure file.
        const mod = await import(new URL(`file://${ctx.dd}/server/src/modules/brief/infrastructure/issue-source.ts`).href);
        const source = new mod.GitHubIssueSource({ github });
        const pending = source.fetch({ owner: 'acme', name: 'api' }, 12) as Promise<Obj | null>;
        await settleMany(6);
        if (c.fireTimer) for (const [id, fn] of [...timers]) { timers.delete(id); fn(); }
        const result = await pending;
        issueSource.push({ name: c.name, github: c.github, timerFired: c.fireTimer === true, result });
      }
    } finally {
      globalThis.setTimeout = realSet;
      globalThis.clearTimeout = realClear;
    }
  } finally {
    restoreDate();
  }
  return { cases, scenarios, issueSource, stored: genStored(ctx, spec.stored as Obj[]) };
}

export function mergeWorld(base: Obj, patch: Obj | undefined): Obj {
  const out: Obj = { ...base };
  for (const [k, v] of Object.entries(patch ?? {})) {
    if (k === 'pull' && v !== null && typeof v === 'object') out.pull = { ...base.pull, ...(v as Obj) };
    else if (k === 'llm' && v !== null && typeof v === 'object' && !('data' in (v as Obj)) && !('error' in (v as Obj)) && !('hang' in (v as Obj))) out.llm = { ...base.llm, ...(v as Obj) };
    else out[k] = v;
  }
  return out;
}

export async function genBrief(ctx: Ctx): Promise<void> {
  const norm = genNormalize(ctx);
  const flows = await genFlows(ctx);
  ctx.writer.write('brief.json', {
    note: 'Node PR brief (modules/brief) at d1c9093. input: ranges from hunk headers, file/finding selection, usable blast and prompt blast, linked issue number, missing inputs, truncateText. prompt: the user message, the prompt-log sections, the estimate and trimToBudget (maxTokens = the budget used). normalize: normalizeBrief + isStale. stored: PrBrief.safeParse of stored documents (parsed shows key order and stripped keys). flows: BriefService.get/generate over fake ports (LLM calls go through the real LlmBriefModel to a fake provider, so llmRequests show the call parameters; durationMs in log lines comes from a logical clock: 1,500 ms per LLM call, not parity data). scenarios: the one-generation-per-PR marker. issueSource: GitHubIssueSource with a faked deadline timer.',
    constants: constants(ctx),
    input: genInput(ctx),
    prompt: genPrompt(ctx),
    normalize: norm.cases,
    stale: norm.stale,
    stored: flows.stored,
    flows: flows.cases,
    scenarios: flows.scenarios,
    issueSource: flows.issueSource,
  });
}
