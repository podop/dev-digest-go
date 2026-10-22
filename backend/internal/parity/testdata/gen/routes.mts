/**
 * routes.json — wire bodies of the eleven Phase 5 routes as the real Node route plugins produce them
 * (Fastify + fastify-type-provider-zod validators/serializers + the Node error handler) over the real services
 * with fake ports (AC5001–AC5018 bodies, EC5033).
 */
import { corpusJson, expandText, type Obj } from './common.mts';
import { LogicalClock } from './lib.mts';
import type { Ctx } from './ctx.mts';
import * as intentGen from './intent.mts';
import * as briefGen from './brief.mts';
import * as onbGen from './onboarding.mts';
import { makeReader } from './performance.mts';

const WS = 'ws-1';

function patchWorld(w: Obj, patch: Obj): void {
  for (const [k, v] of Object.entries(patch)) {
    if (k === 'pull' && v !== null && typeof v === 'object') w.pull = { ...w.pull, ...(v as Obj) };
    else if (k === 'llm' && v !== null && typeof v === 'object' && !('data' in (v as Obj)) && !('error' in (v as Obj)) && !('hang' in (v as Obj)) && !('mock' in (v as Obj))) w.llm = { ...w.llm, ...(v as Obj) };
    else w[k] = v;
  }
}

async function startApp(ctx: Ctx, plugin: unknown, modules: Obj): Promise<Obj> {
  const { N } = ctx;
  const app = ctx.fastify({ logger: false });
  app.setValidatorCompiler(ctx.providerZod.validatorCompiler);
  app.setSerializerCompiler(ctx.providerZod.serializerCompiler);
  app.decorate('container', {
    auth: { currentUser: async () => ({ id: 'u1' }), currentWorkspace: async () => ({ id: WS }) },
    modules,
  });
  N.errorHandler.registerErrorHandling(app);
  await app.register(plugin);
  await app.ready();
  return app;
}

const fill = (s: string, ids: Obj): string => s.replace(/\{(\w+)\}/g, (m, k: string) => (ids[k] as string | undefined) ?? m);

export async function genRoutes(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'routes/exchanges.json'));
  const ids = spec.ids as Obj;
  const result: Obj = {};

  const runGroup = async (name: string, plugin: unknown, modules: Obj, onWorld: (patch: Obj) => void): Promise<void> => {
    const app = await startApp(ctx, plugin, modules);
    const exchanges: Obj[] = [];
    try {
      for (const step of (spec.groups[name] as Obj).steps as Obj[]) {
        if (step.world) {
          onWorld(step.world);
          exchanges.push({ world: step.world });
          continue;
        }
        const req = step.req as Obj;
        const url = fill(req.url, ids);
        const res = await app.inject({ method: req.method, url, headers: req.headers, payload: req.payload });
        exchanges.push({
          request: { method: req.method, url, ...(req.headers ? { headers: req.headers } : {}), ...(req.payload !== undefined ? { payload: req.payload } : {}) },
          status: res.statusCode,
          contentType: res.headers['content-type'] ?? null,
          body: res.body,
        });
      }
    } finally {
      await app.close();
    }
    result[name] = exchanges;
  };

  // --- intent --------------------------------------------------------------
  {
    const flows = expandText(corpusJson(ctx, 'intent/flows.json'));
    const w = intentGen.mergeWorld(flows.defaults, { pull: { id: ids.pr, repoId: ids.repo }, repo: { id: ids.repo, owner: 'acme', name: 'api' } });
    const store = { row: null as Obj | null };
    const svc = intentGen.makeService(ctx, w, store, intentGen.newRec());
    await runGroup('intent', N.intentRoutes.default, { intent: { service: svc } }, (p) => patchWorld(w, p));
  }

  // --- smart diff ----------------------------------------------------------
  {
    const sd = expandText(corpusJson(ctx, 'smartdiff/service.json'));
    const c = (sd.cases as Obj[])[0]!;
    const w: Obj = { files: c.files, reviews: c.reviews, findings: c.findings };
    const source = {
      pullExists: async (ws: string, id: string) => ws === WS && id === ids.pr,
      listFiles: async () => (w.files as Obj[]).map((f) => ({ ...f })),
      latestReviewFindings: async () => {
        const newest = new Map<string | null, Obj>();
        for (const r of (w.reviews as Obj[]).filter((x) => x.kind === 'review')) {
          const cur = newest.get(r.agent_id ?? null);
          if (!cur || Date.parse(r.created_at) > Date.parse(cur.created_at)) newest.set(r.agent_id ?? null, r);
        }
        const keep = new Set([...newest.values()].map((r) => r.id as string));
        return (w.findings as Obj[]).filter((f) => keep.has(f.review_id)).map((f) => ({ file: f.file, start_line: f.start_line }));
      },
    };
    const svc = new N.smartDiffService.SmartDiffService({ source });
    await runGroup('smartdiff', N.smartDiffRoutes.default, { smartDiff: { service: svc } }, (p) => patchWorld(w, p));
  }

  // --- blast and history -----------------------------------------------------
  {
    const w: Obj = {
      files: ['src/a.ts', 'src/b.ts'],
      index: {
        changedSymbols: [{ file: 'src/a.ts', name: 'foo', kind: 'function' }, { file: 'src/b.ts', name: 'bar', kind: 'class' }],
        callers: [
          { file: 'src/x.ts', symbol: 'handleX', viaSymbol: 'foo', line: 10, rank: 0.5 },
          { file: 'src/y.ts', symbol: 'handleY', viaSymbol: 'bar', line: 3, rank: 0.9 },
        ],
        factsByFile: { 'src/x.ts': { endpoints: ['GET /x'], crons: [] }, 'src/y.ts': { endpoints: ['POST /y'], crons: ['nightly'] } },
        degraded: false,
      },
      merged: [
        { number: 4, title: 'Touches a', author: 'ann', merged_at: '2026-01-01T00:00:00Z', files: ['src/a.ts', 'zzz.ts'] },
        { number: 3, title: 'Unrelated', author: 'bob', merged_at: '2026-01-02T00:00:00Z', files: ['other.ts'] },
        { number: 2, title: 'Both files', author: 'cy', merged_at: '2026-02-01T00:00:00Z', files: ['src/b.ts', 'src/a.ts'] },
      ],
    };
    const svc = new N.blastService.BlastService({
      source: { findPull: async (_ws: string, id: string) => (id === ids.pr ? { repoId: ids.repo, number: 5, owner: 'acme', name: 'api', files: [...w.files] } : null) },
      index: {
        getBlastRadius: async () => {
          if (w.index.throws) throw new Error(w.index.throws);
          return w.index;
        },
      },
      history: {
        listMergedPullRequests: async () => {
          if (w.merged.throws) throw new Error(w.merged.throws);
          return w.merged;
        },
      },
    });
    await runGroup('blast', N.blastRoutes.default, { blast: { service: svc } }, (p) => patchWorld(w, p));
  }

  // --- brief -----------------------------------------------------------------
  {
    const flows = expandText(corpusJson(ctx, 'brief/flows.json'));
    const w = briefGen.mergeWorld(flows.defaults, { pull: { id: ids.pr, repoId: ids.repo } });
    const state = { brief: null as Obj | null };
    const svc = briefGen.makeService(ctx, w, state, briefGen.newRec(), new LogicalClock());
    await runGroup('brief', N.briefRoutes.default, { brief: { service: svc } }, (p) => patchWorld(w, p));
  }

  // --- onboarding --------------------------------------------------------------
  {
    const flows = expandText(corpusJson(ctx, 'onboarding/flows.json'));
    const w = onbGen.mergeWorld(flows.defaults, { repo: { ...flows.defaults.repo, id: ids.repo } });
    const state = { tour: null as Obj | null };
    const svc = onbGen.makeService(ctx, w, state, onbGen.newRec(), new LogicalClock());
    await runGroup('onboarding', N.onbRoutes.default, { onboarding: { service: svc } }, (p) => patchWorld(w, p));
  }

  // --- performance -----------------------------------------------------------
  {
    const ds = expandText(corpusJson(ctx, 'performance/dataset.json'));
    const text = JSON.stringify(ds.datasets.main)
      .replaceAll('a-alpha', ids.agent)
      .replaceAll('a-bug', ids.bug)
      .replaceAll('a-sec', ids.sec)
      .replaceAll('a-zed', ids.zed)
      .replaceAll('a-foreign', ids.foreign);
    const dataset = JSON.parse(text) as Obj;
    const svc = new N.perfService.PerformanceService({ reader: makeReader(dataset, WS), clock: () => new Date(ds.now as string) });
    await runGroup('performance', N.perfRoutes.default, { performance: { service: svc } }, () => undefined);
  }

  ctx.writer.write('routes.json', {
    note: 'Raw responses of the real Node route plugins (inject, no socket): `body` is the exact response text (key order matters: serializers emit contract field order). Services are the real ones over fake ports; the intent/brief/onboarding LLM is scripted, the performance and smart-diff data come from the corpus tables with UUID ids. A {world} entry changes what the fakes answer for the following requests. Node answers a POST with a malformed JSON body 400 (Fastify content-type parser): the Go routes ignore request bodies by design (spec 7.1). Rate limits, CORS, Origin and Host checks are not registered here.',
    ids,
    groups: result,
  });
}
