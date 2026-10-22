/**
 * onboarding.json — the Node onboarding tour: redaction, command safety, excerpts and signals, the prompt and
 * its trim, normalisation, staleness, stored-tour validation, OnboardingService flows over fake ports (real
 * LlmOnboardingModel on a fake LLM provider), the one-generation-per-repo marker and the bounded blob reader
 * (AC5013, AC5014, AC5050–AC5054, EC5025–EC5029, EC5035).
 */
import { corpusJson, expandText, recLogger, showError, showLogs, type Obj } from './common.mts';
import { installFakeDate, LogicalClock, settleMany } from './lib.mts';
import type { Ctx } from './ctx.mts';

function mergeShallow(base: Obj, patch: Obj | undefined): Obj {
  return { ...base, ...(patch ?? {}) };
}

// ---------------------------------------------------------------------------
// Pure rules
// ---------------------------------------------------------------------------

function genRedact(ctx: Ctx): Obj[] {
  const R = ctx.N.onbRedact.redactSecrets as (t: string) => string;
  const spec = expandText(corpusJson(ctx, 'onboarding/redact.json'));
  return (spec.cases as Obj[]).map((c) => {
    const out = R(c.text);
    return { name: c.name, text: c.text, output: out, idempotent: R(out) === out };
  });
}

function genCommands(ctx: Ctx): Obj[] {
  const spec = expandText(corpusJson(ctx, 'onboarding/command.json'));
  const seen = new Set<string>();
  const out: Obj[] = [];
  for (const command of spec.commands as string[]) {
    if (seen.has(command)) continue;
    seen.add(command);
    out.push({ command, dangerous: ctx.N.onbCommand.isDangerousCommand(command) as boolean });
  }
  return out;
}

function genInput(ctx: Ctx): Obj {
  const I = ctx.N.onbInput;
  const spec = expandText(corpusJson(ctx, 'onboarding/input.json'));
  return {
    readme: (spec.readme as Obj[]).map((c) => ({ name: c.name, text: c.text, result: I.readmeExcerpt(c.text) })),
    compose: (spec.compose as Obj[]).map((c) => ({ name: c.name, text: c.text, result: I.composeExcerpt(c.text) })),
    scripts: (spec.scripts as Obj[]).map((c) => ({ name: c.name, text: c.text, result: I.packageScripts(c.text) })),
    env: (spec.env as Obj[]).map((c) => {
      const text = c.extraNames ? c.text + Array.from({ length: c.extraNames as number }, (_, i) => `NAME_${i + 1}=x\n`).join('') : c.text;
      return { name: c.name, text, result: I.envVariableNames(text) };
    }),
    todo: (spec.todo as Obj[]).map((c) => ({ name: c.name, path: c.path, text: c.text, result: I.findTodoLines(c.path, c.text) })),
    collectTodos: (spec.collectTodos as Obj[]).map((c) => {
      const perFile = (c.files as Obj[]).map((f) => I.findTodoLines(f.path, f.text));
      return { name: c.name, files: c.files, result: I.collectTodoLines(perFile) };
    }),
    untested: (spec.untested as Obj[]).map((c) => {
      const ranked = [...(c.ranked as string[]), ...((c.rankedExtra as string[]) ?? [])];
      const indexed = [...(c.indexed as string[]), ...((c.indexedExtra as string[]) ?? [])];
      return { name: c.name, ranked, indexed, result: I.findUntestedFiles(ranked, indexed) };
    }),
    testPaths: (spec.testPaths as string[]).map((p) => ({ path: p, isTest: I.isTestPath(p) as boolean })),
  };
}

function genTour(ctx: Ctx): { cases: Obj[]; stale: Obj[] } {
  const T = ctx.N.onbTour;
  const spec = expandText(corpusJson(ctx, 'onboarding/tour.json'));
  const files = spec.files as string[];
  const cases = (spec.cases as Obj[]).map((c) => {
    const raw = mergeShallow(spec.base, c.raw);
    return { name: c.name, indexedPaths: files, raw, result: T.normalizeTour(raw, files) };
  });
  const stale = (spec.stale as Obj[]).map((c) => ({ ...c, result: T.staleness(c.stored, c.current) }));
  return { cases, stale };
}

function genPrompt(ctx: Ctx): Obj[] {
  const P = ctx.N.onbPrompt;
  const spec = expandText(corpusJson(ctx, 'onboarding/prompt.json'));
  return (spec.cases as Obj[]).map((c) => {
    const input = mergeShallow(spec.base, c.input);
    const estimate = P.estimateInputTokens(input);
    let maxTokens: number | undefined;
    if (c.maxTokens === 'exact') maxTokens = estimate;
    else if (c.maxTokens === 'exact-1') maxTokens = estimate - 1;
    else if (typeof c.maxTokens === 'number') maxTokens = c.maxTokens;
    const trimmed = maxTokens === undefined ? P.trimToBudget(input) : P.trimToBudget(input, maxTokens);
    const messages = P.buildMessages(input);
    return {
      name: c.name,
      maxTokens: maxTokens ?? 24000,
      input,
      estimate,
      trimmed: {
        estimate: P.estimateInputTokens(trimmed),
        readme: trimmed.readme,
        compose: trimmed.compose,
        todoLines: trimmed.todoLines,
        repoMapLength: (trimmed.repoMap as string).length,
        repoMap: trimmed.repoMap,
      },
      // buildMessages always trims with the default budget
      messages: messages.map((m: Obj) => ({ role: m.role, content: m.role === 'system' ? '$ONBOARDING_SYSTEM_PROMPT' : m.content })),
      systemIsTheNodeConstant: messages[0].content === P.ONBOARDING_SYSTEM_PROMPT,
    };
  });
}

function genStored(ctx: Ctx, docs: Obj[]): Obj[] {
  return docs.map((c) => {
    const r = ctx.N.shared.OnboardingTour.safeParse(c.doc);
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
  const K = N.onbConstants;
  return {
    systemPrompt: N.onbPrompt.ONBOARDING_SYSTEM_PROMPT,
    schemaName: K.ONBOARDING_SCHEMA_NAME,
    promptVersion: K.PROMPT_VERSION,
    language: N.shared.ONBOARDING_LANGUAGE,
    limits: N.shared.ONBOARDING_LIMITS,
    constants: {
      inputMaxTokens: K.INPUT_MAX_TOKENS,
      llmMaxOutputTokens: K.LLM_MAX_OUTPUT_TOKENS,
      llmMaxRetries: K.LLM_MAX_RETRIES,
      generationTimeoutMs: K.GENERATION_TIMEOUT_MS,
      topFilesCount: K.TOP_FILES_COUNT,
      readmeCandidates: K.README_CANDIDATES,
      packageJsonPath: K.PACKAGE_JSON_PATH,
      composeCandidates: K.COMPOSE_CANDIDATES,
      envExamplePath: K.ENV_EXAMPLE_PATH,
      readmeMaxChars: K.README_MAX_CHARS,
      composeMaxChars: K.COMPOSE_MAX_CHARS,
      scriptsMaxChars: K.SCRIPTS_MAX_CHARS,
      envNamesMax: K.ENV_NAMES_MAX,
      fileFetchMaxBytes: K.FILE_FETCH_MAX_BYTES,
      readConcurrency: K.READ_CONCURRENCY,
      todoScanFiles: K.TODO_SCAN_FILES,
      todoMax: K.TODO_MAX,
      todoLineMaxChars: K.TODO_LINE_MAX_CHARS,
      untestedMax: K.UNTESTED_MAX,
    },
  };
}

// ---------------------------------------------------------------------------
// The service
// ---------------------------------------------------------------------------

interface Rec {
  llmRequests: Obj[];
  upserts: Obj[];
  readCalls: Obj[];
  rankedRequests: number[];
}
export const newRec = (): Rec => ({ llmRequests: [], upserts: [], readCalls: [], rankedRequests: [] });

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

const short = (paths: readonly string[]): unknown => (paths.length <= 12 ? [...paths] : { count: paths.length, first: paths.slice(0, 3), last: paths.slice(-3) });

export function makeService(ctx: Ctx, w: Obj, state: { tour: Obj | null }, rec: Rec, clock: LogicalClock, gate?: Gate) {
  const { N } = ctx;
  const store = {
    findRepo: async (_ws: string, id: string) => (w.repo === null || id !== w.repo.id ? null : { ...w.repo }),
    getTour: async () => {
      if (state.tour === null) return null;
      const r = N.shared.OnboardingTour.safeParse(state.tour);
      return r.success ? r.data : null;
    },
    upsertTour: async (_id: string, tour: Obj) => {
      rec.upserts.push(tour);
      state.tour = JSON.parse(JSON.stringify(tour));
    },
  };
  const index = {
    getIndexState: async () => ({ ...w.index }),
    getRepoMap: async () => ({ ...w.repoMap }),
    getTopFilesByRank: async (_id: string, n: number) => {
      rec.rankedRequests.push(n);
      return (w.ranked as string[]).slice(0, n);
    },
    getCriticalPaths: async () => (w.chains as string[][]).map((c) => [...c]),
    listIndexedFiles: async () => [...(w.indexed as string[])],
  };
  const files = {
    readMany: async (repo: Obj, sha: string, paths: readonly string[], signal?: AbortSignal) => {
      rec.readCalls.push({ repo: { owner: repo.owner, name: repo.name }, sha, paths: short(paths), hasSignal: signal instanceof AbortSignal });
      if (w.blobDelayMs) await new Promise((r) => setTimeout(r, w.blobDelayMs as number));
      return paths.map((p) => (typeof w.blobs[p] === 'string' ? (w.blobs[p] as string) : null));
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
          systemIsTheNodeConstant: req.messages[0].content === N.onbPrompt.ONBOARDING_SYSTEM_PROMPT,
          user: req.messages[1].content,
        });
        clock.bump(1500);
        if (w.llm.mock) return new N.mockLlm.MockReviewLLMProvider('openrouter').completeStructured(req);
        if (gate) await gate.wait(req.signal);
        if (w.llm.hang) await new Promise((_, reject) => req.signal.addEventListener('abort', () => reject(req.signal.reason), { once: true }));
        if (w.llm.error === 'other') throw new Error('provider exploded: secret details of the repository');
        if (w.llm.error === 'invalid') throw new Error('Output did not match the schema after the reprompt');
        return { data: req.schema.parse(w.llm.data), model: req.model, tokensIn: w.llm.tokensIn, tokensOut: w.llm.tokensOut, costUsd: w.llm.costUsd, attempts: 1, raw: '' };
      },
    };
  };
  const model = new N.onbModel.LlmOnboardingModel({
    resolveModel: async () => ({ provider: w.model.provider, model: w.model.model }),
    llm,
  });
  return new N.onbService.OnboardingService({ store, index, files, model, clock: () => new Date(w.now), timeoutMs: w.timeoutMs });
}

export function mergeWorld(base: Obj, patch: Obj | undefined): Obj {
  const out: Obj = { ...base };
  for (const [k, v] of Object.entries(patch ?? {})) out[k] = v;
  return out;
}

async function genFlows(ctx: Ctx): Promise<{ cases: Obj[]; scenarios: Obj[]; repoFiles: Obj[]; stored: Obj[] }> {
  const spec = expandText(corpusJson(ctx, 'onboarding/flows.json'));
  const defaults = spec.defaults as Obj;
  const clock = new LogicalClock();
  const restoreDate = installFakeDate(clock);
  const cases: Obj[] = [];
  const scenarios: Obj[] = [];
  const repoFiles: Obj[] = [];
  try {
    for (const c of spec.cases as Obj[]) {
      const state: { tour: Obj | null } = { tour: null };
      let storedBefore: Obj | null = null;
      if (c.before !== undefined) {
        clock.reset();
        const wb = mergeWorld(defaults, c.before);
        await makeService(ctx, wb, state, newRec(), clock).generate(wb.workspace, wb.repo.id, recLogger());
        if (c.storedPatch) state.tour = { ...state.tour, ...c.storedPatch };
        storedBefore = state.tour;
      }
      clock.reset();
      const w = mergeWorld(defaults, c.world);
      const rec = newRec();
      const svc = makeService(ctx, w, state, rec, clock);
      const log = recLogger();
      const repoId = w.repo ? w.repo.id : 'repo-1';
      let outcome: Obj;
      try {
        outcome = { response: c.call === 'get' ? await svc.getTour(w.workspace, repoId) : await svc.generate(w.workspace, repoId, log) };
      } catch (e) {
        outcome = { error: showError(e) };
      }
      const same = (v: unknown): unknown => (outcome.response?.tour && JSON.stringify(v) === JSON.stringify(outcome.response.tour) ? '$response.tour' : v);
      cases.push({
        name: c.name,
        call: c.call,
        world: {
          workspace: w.workspace, now: w.now, timeoutMs: w.timeoutMs, model: w.model, repo: w.repo, index: w.index, repoMap: w.repoMap, ranked: w.ranked, chains: w.chains,
          indexed: w.indexed, blobs: w.blobs, blobDelayMs: w.blobDelayMs ?? null, llm: w.llm,
        },
        stored: storedBefore,
        ...outcome,
        logs: showLogs(log.lines),
        ...rec,
        upserts: rec.upserts.map(same),
        storedAfter: same(state.tour),
      });
    }

    for (const sc of spec.scenarios as Obj[]) {
      clock.reset();
      const w = mergeWorld(defaults, sc.world);
      const state: { tour: Obj | null } = { tour: null };
      const rec = newRec();
      const gate = new Gate();
      const svc = makeService(ctx, w, state, rec, clock, gate);
      const callers: Record<string, { promise?: Promise<void>; result?: Obj; log: ReturnType<typeof recLogger> }> = {};
      for (const step of sc.steps as Obj[]) {
        if (step.start !== undefined) {
          const entry: (typeof callers)[string] = { log: recLogger() };
          callers[step.start as string] = entry;
          entry.promise = svc.generate(w.workspace, w.repo.id, entry.log).then(
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

    // The bounded blob reader (infrastructure/repo-files.ts) over a fake git client.
    const mod = await import(new URL(`file://${ctx.dd}/server/src/modules/onboarding/infrastructure/repo-files.ts`).href);
    for (const c of spec.repoFiles as Obj[]) {
      const calls: Obj[] = [];
      let inFlight = 0;
      let peak = 0;
      const ac = new AbortController();
      const git = {
        readFileAt: async (repo: Obj, sha: string, path: string, opts: Obj) => {
          calls.push({ repo, sha, path, maxBytes: opts?.maxBytes });
          if (c.abortAfterReads && calls.length === c.abortAfterReads) ac.abort();
          inFlight++;
          peak = Math.max(peak, inFlight);
          await new Promise((r) => setImmediate(r));
          inFlight--;
          if (c.errors?.[path]) throw new Error(c.errors[path]);
          if (typeof c.blobs[path] !== 'string') throw new Error(`Object '${sha}:${path}' not found`);
          return { path, content: c.blobs[path], size: c.blobs[path].length };
        },
      };
      const res = await new mod.GitRepoFiles(git).readMany({ owner: 'acme', name: 'shop' }, 'abc123', c.paths, c.abortAfterReads ? ac.signal : undefined);
      repoFiles.push({
        name: c.name,
        paths: short(c.paths as string[]),
        blobs: c.blobs,
        errors: c.errors ?? null,
        abortAfterReads: c.abortAfterReads ?? null,
        result: (res as (string | null)[]).length > 40 ? { count: res.length, nonNull: (res as (string | null)[]).filter((x) => x !== null).length } : res,
        reads: calls.length,
        peakInFlight: peak,
        firstCall: calls[0] ?? null,
      });
    }
  } finally {
    restoreDate();
  }
  return { cases, scenarios, repoFiles, stored: genStored(ctx, spec.stored as Obj[]) };
}

export async function genOnboarding(ctx: Ctx): Promise<void> {
  const tour = genTour(ctx);
  const flows = await genFlows(ctx);
  ctx.writer.write('onboarding.json', {
    note: 'Node onboarding tour (modules/onboarding) at d1c9093. redact: redactSecrets + idempotence. command: isDangerousCommand. input: excerpts, package scripts, env names, TODO lines, untested files, test-path rule. tour: normalizeTour + staleness. prompt: estimateInputTokens, trimToBudget (maxTokens = the budget) and buildMessages (always trimmed with the default 24,000 budget; the system message is shown as $ONBOARDING_SYSTEM_PROMPT, see constants). stored: OnboardingTour.safeParse. flows: OnboardingService over fake ports (LLM via the real LlmOnboardingModel; logs; durationMs comes from a logical clock, 1,500 ms per LLM call, not parity data). scenarios: the one-generation-per-repo marker. repoFiles: GitRepoFiles (bounded concurrency, abort).',
    constants: constants(ctx),
    redact: genRedact(ctx),
    command: genCommands(ctx),
    input: genInput(ctx),
    tour: tour.cases,
    stale: tour.stale,
    prompt: genPrompt(ctx),
    stored: flows.stored,
    flows: flows.cases,
    scenarios: flows.scenarios,
    repoFiles: flows.repoFiles,
  });
}
