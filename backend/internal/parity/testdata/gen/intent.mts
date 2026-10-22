/**
 * intent.json — the Node intent layer: link extraction, cache key, text rules, clamps, the sources block,
 * the doc/ticket sources, IntentService flows over fake ports (review path, GET, refresh, staleness) and
 * the single-flight / cancellation / budget scenarios (AC5019–AC5022, AC5025, EC5001–EC5012).
 */
import { createHash } from 'node:crypto';
import { corpusJson, expandText, resolveRefs, showError, type Obj } from './common.mts';
import { settleMany } from './lib.mts';
import type { Ctx } from './ctx.mts';

const sha256 = (s: string): string => createHash('sha256').update(s, 'utf8').digest('hex');

// ---------------------------------------------------------------------------
// Domain rules
// ---------------------------------------------------------------------------

function genLinks(ctx: Ctx): { tickets: Obj[]; docs: Obj[] } {
  const { N } = ctx;
  const tspec = expandText(corpusJson(ctx, 'intent/tickets.json'));
  const tickets = (tspec.cases as Obj[]).map((c) => {
    const repo = (c.repo ?? tspec.repo) as Obj;
    const res = N.intentLinks.extractTicketRefs(c.texts, repo);
    return {
      name: c.name,
      repo,
      texts: c.texts,
      refs: (res.refs as Obj[]).map((r) => ({ number: r.number, ref: String(r.number), closing: r.closing })),
      skipped: res.skipped,
    };
  });
  const dspec = expandText(corpusJson(ctx, 'intent/docs.json'));
  const docs = (dspec.cases as Obj[]).map((c) => {
    const repo = (c.repo ?? dspec.repo) as Obj;
    try {
      const res = N.intentLinks.extractDocRefs(c.texts, repo);
      return { name: c.name, repo, texts: c.texts, refs: (res.refs as Obj[]).map((r) => r.path), skipped: res.skipped };
    } catch (e) {
      return { name: c.name, repo, texts: c.texts, error: (e as Error).message, errorName: (e as Error).name };
    }
  });
  return { tickets, docs };
}

function genKeys(ctx: Ctx): Obj[] {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'intent/keys.json'));
  return (spec.cases as Obj[]).map((c) => {
    const input = N.intentDomain.buildCacheKeyInput({
      provider: c.provider,
      model: c.model,
      title: c.title,
      body: c.body,
      branch: c.branch,
      headSha: c.headSha,
      ticketRefs: c.ticketRefs,
      docPaths: c.docPaths,
    });
    const canonical = N.intentDomain.canonicalJson(input) as string;
    return { name: c.name, args: c, input, canonical, input_hash: sha256(canonical) };
  });
}

function genText(ctx: Ctx): Obj {
  const { N } = ctx;
  const D = N.intentDomain;
  const spec = expandText(corpusJson(ctx, 'intent/text.json'));
  return {
    substantive: (spec.substantive as Obj[]).map((c) => ({
      name: c.name,
      body: c.body,
      stripped: c.body === null ? null : D.stripBodyBoilerplate(c.body),
      substantive: D.isSubstantiveBody(c.body),
    })),
    truncate: (spec.truncate as Obj[]).map((c) => ({ name: c.name, text: c.text, max: c.max, result: D.truncateText(c.text, c.max) })),
    clampIntent: (spec.clampIntent as Obj[]).map((c) => ({ name: c.name, text: c.text, result: D.clampIntentText(c.text) })),
    clampScope: (spec.clampScope as Obj[]).map((c) => ({ name: c.name, items: c.items, result: D.clampScopeList(c.items) })),
    confidence: (spec.confidence as Obj[]).map((c) => ({ ...c, result: D.computeConfidence(c.derived_from, c.degraded) })),
    renderSources: (spec.renderSources as Obj[]).map((c) => ({ name: c.name, sources: c.sources, block: D.renderSourcesBlock(c.sources) })),
    userMessage: (spec.userMessage as Obj[]).map((c) => ({ name: c.name, block: c.block, message: N.intentClassification.classificationUserMessage(c.block) })),
    renderIntent: (spec.renderIntent as Obj[]).map((c) => ({ name: c.name, intent: c.intent, text: N.rc.renderIntent(c.intent) })),
  };
}

function genConstants(ctx: Ctx): Obj {
  const { N } = ctx;
  const K = N.intentConstants;
  return {
    systemPrompt: N.intentClassification.CLASSIFICATION_SYSTEM_PROMPT,
    taskText: N.intentClassification.classificationTaskText(),
    userMessageOfX: N.intentClassification.classificationUserMessage('X'),
    schemaName: K.INTENT_CLASSIFICATION_SCHEMA_NAME,
    promptVersion: K.PROMPT_VERSION,
    limits: {
      titleMaxChars: K.TITLE_MAX_CHARS,
      bodyMaxChars: K.BODY_MAX_CHARS,
      ticketMax: K.TICKET_MAX,
      ticketMaxChars: K.TICKET_MAX_CHARS,
      docMax: K.DOC_MAX,
      docFetchMaxBytes: K.DOC_FETCH_MAX_BYTES,
      docMaxChars: K.DOC_MAX_CHARS,
      docTotalMaxChars: K.DOC_TOTAL_MAX_CHARS,
      commitsMax: K.COMMITS_MAX,
      commitMaxChars: K.COMMIT_MAX_CHARS,
      branchMaxChars: K.BRANCH_MAX_CHARS,
      changedFilesMax: K.CHANGED_FILES_MAX,
      diffExcerptMaxChars: K.DIFF_EXCERPT_MAX_CHARS,
      substantiveBodyMinChars: K.SUBSTANTIVE_BODY_MIN_CHARS,
      intentTextMaxChars: K.INTENT_TEXT_MAX_CHARS,
      scopeMaxItems: K.SCOPE_MAX_ITEMS,
      scopeItemMaxChars: K.SCOPE_ITEM_MAX_CHARS,
      budgetMs: K.INTENT_BUDGET_MS,
      llmMaxOutputTokens: K.LLM_MAX_OUTPUT_TOKENS,
      llmMaxRetries: K.LLM_MAX_RETRIES,
      refreshRateLimit: K.REFRESH_RATE_LIMIT,
    },
    docExtensions: K.DOC_EXTENSIONS,
    nonTicketKeyPrefixes: K.NON_TICKET_KEY_PREFIXES,
  };
}

// ---------------------------------------------------------------------------
// Sources: GitDocSource, GitHubTicketSource
// ---------------------------------------------------------------------------

async function genSources(ctx: Ctx): Promise<Obj> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'intent/sources.json'));
  const repo = spec.repo as Obj;
  const docs: Obj[] = [];
  for (const c of spec.docs as Obj[]) {
    const calls: Obj = { git: [] as Obj[], github: 0, githubFactory: 0 };
    const git = {
      readFileAt: async (r: Obj, sha: string, path: string, opts: Obj) => {
        calls.git.push({ repo: r, sha, path, maxBytes: opts?.maxBytes });
        if (c.git.error) {
          const err = new Error(c.git.error.message);
          if (c.git.error.code) (err as Error & { code?: string }).code = c.git.error.code;
          throw err;
        }
        return { path, content: c.git.content, size: c.git.size };
      },
    };
    const github = async () => {
      calls.githubFactory++;
      if (c.github?.factoryError) throw new N.errors.ConfigError(c.github.factoryError);
      return {
        getFileContent: async (r: Obj, path: string, ref: string, opts: Obj) => {
          calls.github++;
          calls.githubArgs = { repo: r, path, ref, maxBytes: opts?.maxBytes };
          if (c.github?.error) throw new Error(c.github.error);
          return c.github?.file ?? null;
        },
      };
    };
    const source = new N.intentDocSource.GitDocSource({ git, github });
    const res = await source.read(repo, spec.headSha, c.path);
    docs.push({ name: c.name, repo, headSha: spec.headSha, path: c.path, git: c.git, github: c.github ?? null, result: res, calls });
  }
  const tickets: Obj[] = [];
  for (const c of spec.tickets as Obj[]) {
    const calls: Obj = { getIssue: [] as Obj[] };
    const github = async () => {
      if (c.github.factoryError) throw new N.errors.ConfigError(c.github.factoryError);
      return {
        getIssue: async (r: Obj, n: number) => {
          calls.getIssue.push({ repo: r, number: n });
          if (c.github.error) throw new Error(c.github.error);
          if (c.github.throwString) throw c.github.throwString; // eslint-disable-line @typescript-eslint/only-throw-error
          return c.github.issue;
        },
      };
    };
    const res = await new N.intentTicketSource.GitHubTicketSource({ github }).read(repo, c.number);
    tickets.push({ name: c.name, repo, number: c.number, github: c.github, result: res, calls });
  }
  return { docs, tickets };
}

// ---------------------------------------------------------------------------
// The service over fake ports
// ---------------------------------------------------------------------------

interface Rec {
  llmCalls: Obj[];
  ticketReads: number[];
  docReads: Obj[];
  upserts: Obj[];
  promptLog: Obj[];
  modelResolves: number;
  llmSawAbort: boolean;
  llmAbortReason: string | null;
}
export const newRec = (): Rec => ({ llmCalls: [], ticketReads: [], docReads: [], upserts: [], promptLog: [], modelResolves: 0, llmSawAbort: false, llmAbortReason: null });

interface Store {
  row: Obj | null;
}

/** snake_case record keys → the camelCase row columns. */
const ROW_KEY: Record<string, string> = {
  pr_id: 'prId', in_scope: 'inScope', out_of_scope: 'outOfScope', change_type: 'changeType', derived_from: 'derivedFrom',
  head_sha: 'headSha', input_hash: 'inputHash', prompt_version: 'promptVersion', tokens_in: 'tokensIn', tokens_out: 'tokensOut',
  cost_usd: 'costUsd', derived_at: 'derivedAt',
};

function patchRow(row: Obj, patch: Obj): Obj {
  const out = { ...row };
  for (const [k, v] of Object.entries(patch)) out[ROW_KEY[k] ?? k] = k === 'derived_at' && typeof v === 'string' ? new Date(v) : v;
  return out;
}

/** numeric(12,6) column: the stored value is the call cost rounded to six decimals. */
const column6 = (c: number | null): number | null => (c === null ? null : Number(c.toFixed(6)));

export class Gate {
  private readonly waiters: Array<{ resolve: (v: Obj) => void; reject: (e: unknown) => void }> = [];
  constructor(private readonly rec: Rec) {}
  wait(signal: AbortSignal): Promise<Obj> {
    return new Promise<Obj>((resolve, reject) => {
      const onAbort = () => {
        this.rec.llmSawAbort = true;
        this.rec.llmAbortReason = signal.reason instanceof Error ? signal.reason.message : String(signal.reason);
        reject(signal.reason);
      };
      if (signal.aborted) return onAbort();
      signal.addEventListener('abort', onAbort, { once: true });
      this.waiters.push({
        resolve: (v) => {
          signal.removeEventListener('abort', onAbort);
          resolve(v);
        },
        reject: (e) => {
          signal.removeEventListener('abort', onAbort);
          reject(e);
        },
      });
    });
  }
  release(kind: string, value: Obj, message?: string): void {
    const w = this.waiters.shift();
    if (!w) throw new Error('scenario: nothing waits on the LLM gate');
    if (kind === 'fail') w.reject(new Error(message ?? 'failed'));
    else w.resolve(value);
  }
}

export function mergeWorld(base: Obj, patch: Obj | undefined): Obj {
  const out: Obj = { ...base };
  for (const [k, v] of Object.entries(patch ?? {})) {
    if (k === 'pull' && v !== null && typeof v === 'object') out.pull = { ...base.pull, ...(v as Obj) };
    else out[k] = v;
  }
  return out;
}

export function makeService(ctx: Ctx, w: Obj, store: Store, rec: Rec, gate?: Gate) {
  const { N } = ctx;
  const IntentClassification = N.intentClassification.IntentClassification;
  const map = N.intentMappers.toPrIntentRecord;
  const pulls = {
    getPull: async (_ws: string, id: string) => (w.pullMissing || id !== w.pull.id ? undefined : { ...w.pull }),
    getRepo: async () => (w.repo === null ? undefined : { ...w.repo }),
    getCommits: async () => (w.commits as Obj[]).map((c) => ({ ...c })),
    getChangedFiles: async () => [...(w.changedFiles as string[])],
  };
  const llmResult = (): Obj => {
    if (w.llm.error !== undefined) throw new Error(w.llm.error);
    return { data: IntentClassification.parse(w.llm.data), tokensIn: w.llm.tokensIn, tokensOut: w.llm.tokensOut, costUsd: w.llm.costUsd };
  };
  const model = {
    resolve: async () => {
      rec.modelResolves++;
      if (w.model.error !== undefined) throw new Error(w.model.error);
      return { provider: w.model.provider, model: w.model.model };
    },
    classify: async (resolved: Obj, messages: Obj[], signal: AbortSignal) => {
      rec.llmCalls.push({
        provider: resolved.provider,
        model: resolved.model,
        roles: messages.map((m) => m.role),
        systemIsTheNodeConstant: messages[0].content === N.intentClassification.CLASSIFICATION_SYSTEM_PROMPT,
        user: messages[1].content,
      });
      if (gate) return gate.wait(signal);
      return llmResult();
    },
  };
  const hangOr = <T,>(entry: unknown, fallback: T): Promise<T> => (entry === 'hang' ? new Promise<T>(() => undefined) : Promise.resolve(fallback));
  const tickets = {
    read: async (_repo: Obj, n: number) => {
      rec.ticketReads.push(n);
      const e = w.tickets[String(n)];
      if (e === 'hang') return hangOr(e, { ok: false, reason: 'unreachable' });
      return e ?? { ok: false, reason: 'Not Found' };
    },
  };
  const docs = {
    read: async (_repo: Obj, sha: string, path: string) => {
      rec.docReads.push({ sha, path });
      const e = w.docs[path];
      if (e === 'hang') return hangOr(e, { ok: false, reason: 'unreachable' });
      return e ?? { ok: false, reason: 'not found' };
    },
  };
  const storePort = {
    get: async () => (store.row?.derivedAt ? map(store.row) : undefined),
    upsert: async (r: Obj) => {
      const values = {
        prId: r.prId,
        intent: r.intent.intent,
        inScope: r.intent.in_scope,
        outOfScope: r.intent.out_of_scope,
        changeType: r.intent.change_type ?? null,
        confidence: r.intent.confidence ?? null,
        derivedFrom: r.intent.derived_from ?? 'inferred',
        sources: r.intent.sources ?? [],
        headSha: r.headSha,
        inputHash: r.inputHash,
        promptVersion: r.promptVersion,
        provider: r.provider,
        model: r.model,
        tokensIn: r.tokensIn,
        tokensOut: r.tokensOut,
        costUsd: r.costUsd,
        derivedAt: r.derivedAt,
      };
      rec.upserts.push({ ...values, derivedAt: (values.derivedAt as Date).toISOString() });
      store.row = { ...values, costUsd: column6(values.costUsd as number | null) };
      return map(store.row);
    },
  };
  const svc = new N.intentService.IntentService({
    pulls,
    model,
    docs,
    tickets,
    store: storePort,
    clock: () => new Date(w.now),
    promptLog: { assembled: (e: Obj) => void rec.promptLog.push(e) },
  });
  return svc;
}

const showResult = (res: Obj): Obj =>
  res.status === 'used' ? { status: 'used', intent: res.intent, trace: res.trace, record: res.record } : { status: 'unavailable', warning: res.warning };

interface EventLine {
  kind: string;
  msg: string;
  data?: unknown;
}

async function genFlows(ctx: Ctx): Promise<{ cases: Obj[]; mapper: Obj[] }> {
  const { N } = ctx;
  const spec = resolveRefs(ctx, expandText(corpusJson(ctx, 'intent/flows.json')));
  const defaults = spec.defaults as Obj;
  const out: Obj[] = [];
  for (const c of spec.cases as Obj[]) {
    const store: Store = { row: null };
    let storedBefore: Obj | null = null;
    if (c.before !== undefined) {
      const wBefore = mergeWorld(defaults, c.before);
      const svc = makeService(ctx, wBefore, store, newRec());
      const res = await svc.resolveForReview({
        workspaceId: wBefore.workspace,
        pull: wBefore.pull,
        repo: wBefore.repo,
        changedFiles: wBefore.changedFiles,
        diffExcerpt: wBefore.diffExcerpt ?? undefined,
      });
      if (res.status !== 'used') throw new Error(`${c.name}: the baseline derivation failed: ${res.warning}`);
      if (c.storedPatch) store.row = patchRow(store.row!, c.storedPatch);
      storedBefore = N.intentMappers.toPrIntentRecord(store.row);
    }
    const w = mergeWorld(defaults, c.world);
    const rec = newRec();
    const svc = makeService(ctx, w, store, rec);
    const events: EventLine[] = [];
    let outcome: Obj;
    try {
      if (c.call === 'resolve') {
        const res = await svc.resolveForReview({
          workspaceId: w.workspace,
          pull: w.pull,
          repo: w.repo,
          changedFiles: w.changedFiles,
          diffExcerpt: w.diffExcerpt ?? undefined,
          force: c.force === true,
          onEvent: (e: EventLine) => void events.push({ kind: e.kind, msg: e.msg, ...(e.data !== undefined ? { data: e.data } : {}) }),
        });
        outcome = { result: showResult(res) };
      } else if (c.call === 'get') {
        outcome = { response: await svc.get(w.workspace, w.pull.id) };
      } else {
        outcome = { response: await svc.refresh(w.workspace, w.pull.id) };
      }
    } catch (e) {
      outcome = { error: showError(e) };
    }
    out.push({
      name: c.name,
      call: c.call,
      force: c.force === true,
      world: { workspace: w.workspace, now: w.now, pullMissing: w.pullMissing === true, pull: w.pull, repo: w.repo, commits: w.commits, changedFiles: w.changedFiles, diffExcerpt: w.diffExcerpt, model: w.model, tickets: w.tickets, docs: w.docs, llm: w.llm },
      stored: storedBefore,
      ...outcome,
      events,
      llmCalls: rec.llmCalls,
      ticketReads: rec.ticketReads,
      docReads: rec.docReads,
      upserts: rec.upserts,
      promptLog: rec.promptLog,
      storedAfter: store.row ? N.intentMappers.toPrIntentRecord(store.row) : null,
    });
  }
  const mapper = (spec.mapper as Obj[]).map((c) => {
    const row = { ...c.row, derivedAt: c.row.derivedAt === null ? null : new Date(c.row.derivedAt) };
    return { name: c.name, row: c.row, record: N.intentMappers.toPrIntentRecord(row), repositoryTreatsAsNoRecord: row.derivedAt === null };
  });
  return { cases: out, mapper };
}

// ---------------------------------------------------------------------------
// Scenarios (single flight, cancellation, budget)
// ---------------------------------------------------------------------------

async function genScenarios(ctx: Ctx): Promise<Obj[]> {
  const spec = expandText(corpusJson(ctx, 'intent/scenarios.json'));
  const flows = expandText(corpusJson(ctx, 'intent/flows.json'));
  const defaults = flows.defaults as Obj;
  const out: Obj[] = [];

  // Fake timers for the whole scenario: the only timer the service creates is the derivation budget.
  const realSet = globalThis.setTimeout;
  const realClear = globalThis.clearTimeout;
  // Node quirk: a caller that joins with an already aborted signal leaves the flight's rejection unobserved.
  let unhandled = 0;
  const onUnhandled = () => void unhandled++;
  process.on('unhandledRejection', onUnhandled);
  try {
    for (const sc of spec.scenarios as Obj[]) {
      const timers = new Map<number, { fn: () => void; delay: number }>();
      let seq = 0;
      globalThis.setTimeout = ((fn: () => void, delay: number) => {
        const id = ++seq;
        timers.set(id, { fn, delay });
        return id;
      }) as unknown as typeof setTimeout;
      globalThis.clearTimeout = ((id: number) => void timers.delete(id)) as unknown as typeof clearTimeout;

      unhandled = 0;
      const w = mergeWorld(defaults, sc.world);
      const rec = newRec();
      const store: Store = { row: null };
      const gate = new Gate(rec);
      const svc = makeService(ctx, w, store, rec, gate);
      const callers = sc.callers as Record<string, Obj>;
      const state: Record<string, { controller?: AbortController; events: EventLine[]; promise?: Promise<Obj>; result?: Obj }> = {};
      const trace: Obj[] = [];
      const flightsNow = () => (svc as unknown as { inflight: Map<string, unknown> }).inflight.size;

      for (const step of sc.steps as Obj[]) {
        const label: Obj = { ...step };
        if (step.start !== undefined) {
          const name = step.start as string;
          const opt = callers[name] ?? {};
          const s: (typeof state)[string] = { events: [] };
          if (opt.signal) {
            s.controller = new AbortController();
            if (opt.abortedBefore) s.controller.abort();
          }
          state[name] = s;
          const pull = { ...w.pull, ...(opt.pull ?? {}) };
          s.promise = svc
            .resolveForReview({
              workspaceId: w.workspace,
              pull,
              repo: w.repo,
              changedFiles: w.changedFiles,
              diffExcerpt: w.diffExcerpt ?? undefined,
              force: opt.force === true,
              onEvent: (e: EventLine) => void s.events.push({ kind: e.kind, msg: e.msg, ...(e.data !== undefined ? { data: e.data } : {}) }),
              signal: s.controller?.signal,
            })
            .then((r: Obj) => (s.result = showResult(r)));
        } else if (step.settle) {
          await settleMany(8);
        } else if (step.abort !== undefined) {
          const reason = step.reason as string | undefined;
          state[step.abort as string]!.controller!.abort(reason === undefined ? undefined : new Error(reason));
        } else if (step.release !== undefined) {
          gate.release(step.release as string, { data: ctx.N.intentClassification.IntentClassification.parse(w.llm.data), tokensIn: w.llm.tokensIn, tokensOut: w.llm.tokensOut, costUsd: w.llm.costUsd }, step.message as string | undefined);
          await settleMany(8);
        } else if (step.fire !== undefined) {
          const due = [...timers.entries()].filter(([, t]) => t.delay === step.fire);
          label.fired = due.length;
          for (const [id, t] of due) {
            timers.delete(id);
            t.fn();
          }
          await settleMany(8);
        } else if (step.await !== undefined) {
          await state[step.await as string]!.promise;
        }
        trace.push({ step: label, flights: flightsNow(), pendingTimers: [...timers.values()].map((t) => t.delay) });
      }
      out.push({
        name: sc.name,
        world: { pull: w.pull, tickets: w.tickets, docs: w.docs, llm: w.llm },
        callers: Object.fromEntries(Object.entries(state).map(([k, s]) => [k, { events: s.events, result: s.result ?? null, signalAbortedAtEnd: s.controller?.signal.aborted ?? null }])),
        steps: trace,
        llmCalls: rec.llmCalls.length,
        llmSawAbort: rec.llmSawAbort,
        llmAbortReason: rec.llmAbortReason,
        ticketReads: rec.ticketReads,
        docReads: rec.docReads.map((d) => d.path),
        upserts: rec.upserts.length,
        storedRow: store.row !== null,
        flightsLeft: flightsNow(),
        nodeUnhandledRejections: (await settleMany(4), unhandled),
      });
    }
  } finally {
    process.off('unhandledRejection', onUnhandled);
    globalThis.setTimeout = realSet;
    globalThis.clearTimeout = realClear;
  }
  return out;
}

export async function genIntent(ctx: Ctx): Promise<void> {
  const links = genLinks(ctx);
  const flows = await genFlows(ctx);
  ctx.writer.write('intent.json', {
    note: 'Node intent layer (modules/intent) at d1c9093. constants: verbatim prompt texts and limits. tickets/docs: link extraction (refs are {number, ref = String(number), closing}; a doc extraction that throws is {error: "URI malformed"}). keys: buildCacheKeyInput → JSON.stringify of the sorted-keys value → sha256 hex. text: the pure text rules, the sources block, the user message and renderIntent. sources: GitDocSource / GitHubTicketSource over fakes. flows: IntentService over fake ports (see corpus/intent/flows.json), the stored row is emulated (numeric(12,6) rounds the cost to 6 decimals; the event cost comes from the call result). mapper: toPrIntentRecord over rows. scenarios: single flight, cancellation, budget (fake timers). Texts that come from GitHub/Octokit in Node (ticket failure reasons) are scripted in the corpus (D5006: Go uses its own client texts).',
    constants: genConstants(ctx),
    tickets: links.tickets,
    docs: links.docs,
    keys: genKeys(ctx),
    text: genText(ctx),
    sources: await genSources(ctx),
    flows: flows.cases,
    mapper: flows.mapper,
    scenarios: await genScenarios(ctx),
  });
}
