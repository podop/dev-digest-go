/**
 * runner.json and runner_runci.json — the Node CI runner (agent-runner/src at d1c9093): manifest and skill loading
 * and their error texts, the PR context, the diff strip and the runner's diff parser, the gate, the review payload,
 * the result artifact and its JSON text, the GitHub calls (diff fetch, review post with the 422 retry, issue
 * comment), and `runCi` end to end with a scripted LLM, a scripted fetch and an in-memory file system, plus runCi
 * over the real `OpenRouterProvider` against a local HTTP stub. Spec: FR6056, NFR6001, AC6050, AC6051 (Phase 6);
 * plan S17. See ./README.md.
 */
import { createHash } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import path from 'node:path';
import { corpusJson, expandText, type Obj } from './common.mts';
import { attempt, attemptAsync, merge } from './common6.mts';
import type { Ctx } from './ctx.mts';
import { textOf } from './lib.mts';

const sha256 = (s: string): string => createHash('sha256').update(s, 'utf8').digest('hex');

function enoent(op: string, p: string): Error {
  const e = new Error(`ENOENT: no such file or directory, ${op} '${p}'`) as Error & { code: string };
  e.code = 'ENOENT';
  return e;
}

/** A diff of the corpus by name, as text. */
function diffTextOf(spec: Obj, name: string): string {
  const d = (spec.diffs as Obj)[name];
  if (!d) throw new Error(`unknown corpus diff ${name}`);
  return textOf(d);
}

// ---------------------------------------------------------------------------
// runner.json
// ---------------------------------------------------------------------------

export async function genRunner(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const pure = expandText(corpusJson(ctx, 'runner/pure.json'));
  const runci = expandText(corpusJson(ctx, 'runner/runci.json'));
  const dir = pure.dir as string;

  // ---- manifest path ----
  const manifestPaths = (pure.manifestPaths as Obj[]).map((c) => {
    const readDir = (d: string): string[] => {
      const l = c.listing as Obj | undefined;
      if (l?.throw) throw new Error(l.throw as string);
      if (!l || !(d in l)) throw enoent('scandir', d);
      return l[d] as string[];
    };
    return { ...c, ...attempt(() => N.runManifest.findManifestPath(dir, { readDir }, c.agent)) };
  });

  // ---- manifest load / validation ----
  const manifests = (pure.manifests as Obj[]).map((c) => {
    const p = `${dir}/agents/x.yaml`;
    const readFile = (): string => {
      if (c.throw) throw new Error(c.throw as string);
      return c.text as string;
    };
    return { ...c, path: p, ...attempt(() => N.runManifest.loadAgentManifest(p, { readFile })) };
  });

  // ---- skills ----
  const skills = (pure.skills as Obj[]).map((c) => {
    const reads: string[] = [];
    const files = new Map<string, string>();
    for (const [slug, text] of Object.entries(c.files as Obj)) files.set(path.join(dir, 'skills', `${slug}.md`), text as string);
    const errors = new Map<string, string>();
    for (const [slug, msg] of Object.entries((c.errors ?? {}) as Obj)) errors.set(path.join(dir, 'skills', `${slug}.md`), msg as string);
    const readFile = (p: string): string => {
      reads.push(p);
      if (errors.has(p)) throw new Error(errors.get(p));
      if (!files.has(p)) throw enoent('open', p);
      return files.get(p)!;
    };
    return { ...c, reads, ...attempt(() => N.runSkills.loadSkillBodies(dir, c.slugs, readFile)) };
  });

  // ---- context ----
  const contexts = (pure.contexts as Obj[]).map((c) => {
    const reads: string[] = [];
    const readFile = (p: string): string => {
      reads.push(p);
      if (c.eventThrow) throw new Error(c.eventThrow as string);
      if (c.eventText !== undefined) return c.eventText as string;
      if (c.event === undefined || c.event === null) throw enoent('open', p);
      return JSON.stringify(c.event);
    };
    return { ...c, reads, ...attempt(() => N.runContext.resolvePrContext(c.env, readFile)) };
  });

  const intOrNull = (pure.intOrNull as string[]).map((v) => ({ value: v, result: N.runArtifact.intOrNull(v) }));
  const intOrNullUndefined = N.runArtifact.intOrNull(undefined);

  // ---- diffs ----
  const diffText = (n: string): string => diffTextOf(pure, n);
  const strips = (pure.strips as string[]).map((n) => ({ diff: n, raw: diffText(n), result: N.runDiff.stripIgnoredFiles(diffText(n)) }));
  const showDiff = (d: Obj): Obj => ({
    files: d.files.map((f: Obj) => ({
      path: f.path,
      additions: f.additions,
      deletions: f.deletions,
      hunks: f.hunks.map((h: Obj) => ({ file: h.file, oldStart: h.oldStart, oldLines: h.oldLines, newStart: h.newStart, newLines: h.newLines, newLineNumbers: h.newLineNumbers })),
    })),
  });
  const parses = (pure.parses as string[]).map((n) => ({ diff: n, raw: diffText(n), result: showDiff(N.runDiff.parseUnifiedDiff(diffText(n))) }));

  // ---- gate and payload ----
  const mergeFinding = (f: Obj, i = 0): Obj => ({ ...pure.findingBase, id: `f${i + 1}`, ...f });
  const gates = (pure.gates as Obj[]).map((c) => {
    const findings = (c.findings as Obj[]).map(mergeFinding);
    const byPolicy = Object.fromEntries(
      ['never', 'critical', 'warning', 'any'].map((p) => [p, { blockers: N.rc.countBlockers(findings, p), triggered: N.rc.gateTriggered(findings, p) }]),
    );
    return { name: c.name, findings, byPolicy };
  });
  const payloads = (pure.payloads as Obj[]).map((c) => {
    const findings = ((c.review.findings ?? []) as Obj[]).map(mergeFinding);
    const review = { verdict: 'comment', summary: 'summary', score: 80, findings };
    const diff = c.diff ? N.runDiff.parseUnifiedDiff(diffText(c.diff)) : undefined;
    const opts = { ...c.opts, ...(diff ? { diff } : {}) };
    return { name: c.name, diff: c.diff, review, opts: c.opts, ...attempt(() => N.rc.toReviewPayload(review, opts)) };
  });

  // ---- artifact ----
  const artifacts = (pure.artifacts as Obj[]).map((c) => {
    const input = { ...c.input, findings: (c.input.findings as Obj[]).map(mergeFinding) };
    const out = attempt(() => {
      const artifact = N.runArtifact.buildResultArtifact(input);
      return { artifact, json: `${JSON.stringify(artifact, null, 2)}\n` };
    });
    return { name: c.name, input: c.input, ...out };
  });

  // ---- GitHub calls ----
  const fetches = await Promise.all((pure.fetches as Obj[]).map(async (c) => {
    const requests: Obj[] = [];
    const responses = (c.responses ?? [c.response]) as Obj[];
    let i = 0;
    const fetchImpl = (async (input: unknown, init?: RequestInit) => {
      requests.push(recordRequest(String(input), init));
      const r = responses[Math.min(i++, responses.length - 1)]!;
      if (r.throw) throw new Error(r.throw as string);
      return new Response(r.text as string, { status: r.status as number });
    }) as unknown as typeof fetch;
    let outcome: Obj;
    if (c.op === 'diff') outcome = await attemptAsync(() => N.runGithub.fetchPrDiff(c.ctx, c.token, fetchImpl));
    else if (c.op === 'review') outcome = await attemptAsync(async () => { await N.runGithub.postGithubReview(c.ctx, c.token, c.payload, fetchImpl); return { posted: true }; });
    else outcome = await attemptAsync(async () => { await N.runGithub.postPrComment(c.ctx, c.token, c.body, fetchImpl); return { posted: true }; });
    return { ...c, ...outcome, requests };
  }));

  ctx.writer.write('runner.json', {
    note: 'Node agent-runner at d1c9093. Paths in messages are under dir (/ws/.devdigest); a Go test replaces that prefix with its temp directory. YAML error texts ("yamlError": true cases) come from the yaml library and are not parity data: compare the prefix "Agent manifest at <path> is not valid YAML: ". Zod issue texts ("<path or (root)>: <message>; ...") are the Phase 2 validator texts. skills.reads lists the paths the Node code opened (it does not validate slugs: D6018 makes the Go runner refuse the traversal cases instead). contexts: Node splits GITHUB_REPOSITORY with split("/", 2), so "a/b/c" gives repo "b" (the spec says "after the first /": a deviation to decide). fetches: Node reads the whole response text, adds no body cut and no token redaction to its error texts (the Go runner cuts at 1,000 bytes and redacts the token: spec 7.8.5). artifacts[].json is the exact file text JSON.stringify(artifact, null, 2) + "\\n". payloads use the runner parser (trailing newline adds no context line).',
    constants: {
      RUNNER_VERSION: N.runArtifact.RUNNER_VERSION,
      IGNORED_DIFF_PREFIXES: N.runDiff.IGNORED_DIFF_PREFIXES,
      AGENT_SLUG: '^[a-z0-9][a-z0-9-]{0,39}$',
      SEV_RANK: N.rcToReview.SEV_RANK,
      FAIL_ON_MIN_RANK: Object.fromEntries(Object.entries(N.rcToReview.FAIL_ON_MIN_RANK as Obj).map(([k, v]) => [k, Number.isFinite(v) ? v : 'Infinity'])),
    },
    manifestPaths,
    manifests,
    skills,
    contexts,
    intOrNull: { cases: intOrNull, undefined: intOrNullUndefined },
    strips,
    parses,
    gates,
    payloads,
    artifacts,
    fetches,
  });

  const runciOut = await genRunCi(ctx, runci);
  ctx.writer.write('runner_runci.json', runciOut);
}

function recordRequest(url: string, init?: RequestInit): Obj {
  const headers = (init?.headers ?? {}) as Record<string, string>;
  let body: unknown = init?.body ?? null;
  if (typeof body === 'string') {
    try {
      body = JSON.parse(body);
    } catch {
      /* keep the text */
    }
  }
  return { method: init?.method ?? 'GET', url, headers, body };
}

// ---------------------------------------------------------------------------
// runner_runci.json
// ---------------------------------------------------------------------------

interface World {
  files: Map<string, string>;
  agentsListed: boolean;
  reads: string[];
  writes: string[];
  requests: Obj[];
  logs: string[];
}

function buildWorld(spec: Obj, sc: Obj): { world: World; env: Obj; fsDeps: Obj; dir: string; resultPath: string } {
  const dir = (spec.dir as string);
  const resultPath = spec.resultPath as string;
  const files = new Map<string, string>();
  const manifestFor = (v: unknown): string | undefined => (v === null || v === undefined ? undefined : (spec.manifests[v as string] ?? (v as string)));
  if (sc.manifestText !== undefined) files.set(`${dir}/agents/${sc.agentFile ?? 'security-reviewer.yaml'}`, sc.manifestText as string);
  else if (sc.manifest !== null && sc.manifest !== undefined) files.set(`${dir}/agents/${sc.agentFile ?? 'security-reviewer.yaml'}`, manifestFor(sc.manifest)!);
  for (const [file, name] of Object.entries((sc.extraManifests ?? {}) as Obj)) files.set(`${dir}/agents/${file}`, manifestFor(name)!);
  for (const [slug, text] of Object.entries((sc.skills ?? {}) as Obj)) files.set(`${dir}/skills/${slug}.md`, text as string);
  for (const [p, text] of Object.entries((sc.extraFiles ?? {}) as Obj)) files.set(p, text as string);
  const event = sc.event === undefined ? spec.event : sc.event;
  if (event !== null) files.set('/ws/event.json', JSON.stringify(event));

  const env: Obj = { ...spec.env };
  for (const [k, v] of Object.entries((sc.env ?? {}) as Obj)) {
    if (v === '$unset') delete env[k];
    else env[k] = v;
  }

  const world: World = { files, agentsListed: !sc.noAgents, reads: [], writes: [], requests: [], logs: [] };
  const fsDeps = {
    readFile: (p: string) => {
      world.reads.push(p);
      if (!world.files.has(p)) throw enoent('open', p);
      return world.files.get(p)!;
    },
    readDir: (d: string) => {
      world.reads.push(`${d}/`);
      if (!world.agentsListed || d !== `${dir}/agents`) throw enoent('scandir', d);
      return [...world.files.keys()].filter((k) => k.startsWith(`${d}/`) && !k.slice(d.length + 1).includes("/")).map((k) => k.slice(d.length + 1)).sort();
    },
    writeFile: (p: string, data: string) => {
      world.writes.push(p);
      world.files.set(p, data);
    },
  };
  return { world, env, fsDeps, dir, resultPath };
}

function scriptedFetch(spec: Obj, sc: Obj, world: World): typeof fetch {
  const reviews = ((sc.github?.reviews ?? []) as Obj[]);
  let reviewIdx = 0;
  return (async (input: unknown, init?: RequestInit) => {
    const url = String(input);
    world.requests.push(recordRequest(url, init));
    const method = init?.method ?? 'GET';
    if (method === 'GET' && /\/pulls\/\d+$/.test(url)) {
      const r = (sc.diffResponse ?? { status: 200, text: diffTextOf(spec, sc.diff ?? 'fixture') }) as Obj;
      if (r.throw) throw new Error(r.throw as string);
      return new Response(r.text as string, { status: r.status as number });
    }
    if (method === 'POST' && /\/pulls\/\d+\/reviews$/.test(url)) {
      const r = reviews[Math.min(reviewIdx++, Math.max(reviews.length - 1, 0))] ?? { status: 200, text: '{}' };
      return new Response(r.text as string, { status: r.status as number });
    }
    if (method === 'POST' && /\/issues\/\d+\/comments$/.test(url)) {
      const r = (sc.github?.comment ?? { status: 201, text: '{}' }) as Obj;
      return new Response(r.text as string, { status: r.status as number });
    }
    return new Response('unexpected request', { status: 599 });
  }) as unknown as typeof fetch;
}

function findingsOf(spec: Obj, entry: Obj): Obj[] {
  const list = entry.findings === '$grounded' ? (spec.grounded as Obj[]) : ((entry.findings ?? []) as Obj[]);
  return list.map((f, i) => ({ ...spec.findingBase, id: `f${i + 1}`, ...f }));
}

function reviewOf(spec: Obj, entry: Obj): Obj {
  return { verdict: 'approve', summary: 'looks fine', score: 95, findings: findingsOf(spec, entry) };
}

function scriptedLlm(spec: Obj, sc: Obj, llmRequests: Obj[]): Obj {
  const script = (sc.llm ?? []) as Obj[];
  let idx = 0;
  return {
    id: 'openrouter',
    listModels: async () => [],
    complete: async () => { throw new Error('complete() not used by reviewPullRequest'); },
    embed: async () => [],
    completeStructured: async (req: Obj) => {
      const entry = script[Math.min(idx, script.length - 1)];
      llmRequests.push({
        call: idx,
        model: req.model,
        temperature: req.temperature === undefined ? '(omitted)' : req.temperature,
        schemaName: req.schemaName,
        maxRetries: req.maxRetries,
        sessionId: req.sessionId ?? null,
        messages: req.messages,
      });
      idx++;
      if (!entry) throw new Error('no scripted model reply');
      if (entry.error !== undefined) throw new Error(entry.error);
      const review = reviewOf(spec, entry);
      return { data: review, model: req.model, tokensIn: 100, tokensOut: 50, costUsd: entry.cost === undefined ? null : entry.cost, raw: JSON.stringify(review), attempts: 1 };
    },
  };
}

function showResult(r: Obj): Obj {
  if (r.artifact === null) return { exitCode: r.exitCode, error: r.error, artifact: null, posted: null };
  return { exitCode: r.exitCode, artifact: r.artifact, posted: r.posted, blockers: r.blockers, gateTriggered: r.gateTriggered };
}

async function runScenario(ctx: Ctx, spec: Obj, sc: Obj, llmFactory: (world: World, llmRequests: Obj[]) => Obj, durationMs = 4321): Promise<Obj> {
  const { N } = ctx;
  const { world, env, fsDeps, dir, resultPath } = buildWorld(spec, sc);
  const llmRequests: Obj[] = [];
  const times = [0, durationMs];
  let t = 0;
  const result = await N.runRun.runCi({
    devdigestDir: dir,
    env,
    llm: llmFactory(world, llmRequests),
    postAs: sc.postAs ?? 'github_review',
    resultPath,
    fetchImpl: scriptedFetch(spec, sc, world),
    readFile: fsDeps.readFile,
    readDir: fsDeps.readDir,
    writeFile: fsDeps.writeFile,
    now: () => times[Math.min(t++, times.length - 1)],
    log: (line: string) => void world.logs.push(line),
  });
  return {
    result: showResult(result),
    artifactFile: world.files.get(resultPath) ?? null,
    writes: world.writes,
    reads: world.reads,
    requests: world.requests,
    llmRequests,
    logs: world.logs,
  };
}

async function genRunCi(ctx: Ctx, spec: Obj): Promise<Obj> {
  const scenarios: Obj[] = [];
  for (const sc of spec.scenarios as Obj[]) {
    const manifestText = sc.manifestText ?? (sc.manifest === null || sc.manifest === undefined ? null : (spec.manifests[sc.manifest] ?? sc.manifest));
    const out = await runScenario(ctx, spec, sc, (_w, llmRequests) => scriptedLlm(spec, sc, llmRequests));
    scenarios.push({
      name: sc.name,
      input: {
        manifest: manifestText,
        agentFile: sc.agentFile ?? 'security-reviewer.yaml',
        extraManifests: sc.extraManifests ? Object.fromEntries(Object.entries(sc.extraManifests as Obj).map(([k, v]) => [k, spec.manifests[v as string] ?? v])) : undefined,
        skills: sc.skills,
        extraFiles: sc.extraFiles,
        noAgentsDirectory: sc.noAgents === true ? true : undefined,
        event: sc.event === undefined ? spec.event : sc.event,
        env: mergeEnv(spec.env, sc.env),
        postAs: sc.postAs ?? 'github_review',
        diff: sc.diffResponse ? undefined : diffTextOf(spec, sc.diff ?? 'fixture'),
        diffResponse: sc.diffResponse,
        github: sc.github,
        llm: sc.llm,
      },
      ...out,
    });
  }

  const openrouter = await genOpenRouter(ctx, spec);

  return {
    note: 'Node runCi (agent-runner/src/run.ts) at d1c9093. scenarios: scripted LLMProvider (the stub of run.test.ts), scripted fetch (GitHub), in-memory files. input is everything a scenario needs (diff is the text the GitHub stub answers with; llm entries are the scripted model replies: findings merged over spec findingBase, "$grounded" = a grounded finding plus a phantom one at line 999; cost = the costUsd the provider reports, absent = null). output: result (exitCode, error, artifact, posted {kind, payload}, blockers, gateTriggered), artifactFile (the exact text written to resultPath, or null), writes, reads (paths opened, in order), requests (GitHub calls in order: method, url, headers, parsed body), llmRequests (what the engine sent: messages are byte-exact), logs (the trace line; the success/failure lines are printed by index.ts and are not recorded here). duration_ms is 4321 in every scenario (normalise it in Go). openrouter: runCi over the real OpenRouterProvider (OpenAI SDK, baseURL = a local stub) so the HTTP request body and the cost rules are Node truth; the key is the marker sk-or-test-marker; headers other than authorization/content-type are SDK noise and are not recorded.',
    dir: spec.dir,
    resultPath: spec.resultPath,
    scenarios,
    openrouter,
  };
}

function mergeEnv(base: Obj, over: Obj | undefined): Obj {
  const out: Obj = { ...base };
  for (const [k, v] of Object.entries(over ?? {})) {
    if (v === '$unset') delete out[k];
    else out[k] = v;
  }
  return out;
}

// ---------------------------------------------------------------------------
// runCi over the real OpenRouterProvider and a local HTTP stub
// ---------------------------------------------------------------------------

async function genOpenRouter(ctx: Ctx, spec: Obj): Promise<Obj> {
  const { N } = ctx;
  const stub = (spec.openrouter as Obj);
  let responses: Obj[] = [];
  let idx = 0;
  let httpRequests: Obj[] = [];
  /** Paths of the current scenario's diff in order: concurrent chunk requests are matched to responses by chunk, not by arrival. */
  let chunkPaths: string[] = [];
  let byChunk = false;
  let schemaFull: unknown;

  const server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const chunks: Buffer[] = [];
    req.on('data', (c: Buffer) => chunks.push(c));
    req.on('end', () => {
      const raw = Buffer.concat(chunks).toString('utf8');
      let body: Obj = {};
      try { body = JSON.parse(raw) as Obj; } catch { /* keep empty */ }
      const rf = body.response_format as Obj | undefined;
      const js = rf?.json_schema as Obj | undefined;
      if (js?.schema && schemaFull === undefined) schemaFull = js.schema;
      httpRequests.push({
        method: req.method,
        path: req.url,
        authorization: req.headers.authorization ?? null,
        contentType: req.headers['content-type'] ?? null,
        body: { ...body, ...(rf ? { response_format: { type: rf.type, json_schema: { name: js?.name, strict: js?.strict, schemaSha256: js?.schema ? sha256(JSON.stringify(js.schema)) : null } } } : {}) },
      });
      const userText = ((body.messages as Obj[] | undefined) ?? []).find((m) => m.role === 'user')?.content as string | undefined;
      const firstPath = /diff --git a\/(\S+)/.exec(userText?.split('## Diff to review')[1] ?? '')?.[1];
      const chunk = firstPath === undefined ? 0 : Math.max(0, chunkPaths.indexOf(firstPath));
      httpRequests[httpRequests.length - 1]!.chunk = chunk;
      const pick = byChunk ? chunk : idx++;
      const r = responses[Math.min(pick, Math.max(responses.length - 1, 0))] ?? { http: { status: 500, json: { error: { message: 'no scripted response' } } } };
      res.setHeader('content-type', 'application/json');
      if (r.http) {
        res.statusCode = r.http.status as number;
        res.end(JSON.stringify(r.http.json));
        return;
      }
      const review = { verdict: 'approve', summary: 'looks fine', score: 95, findings: findingsOf(spec, r.review ?? {}) };
      let content = r.raw !== undefined ? (r.raw as string) : JSON.stringify(review);
      if (r.fenced) content = `\`\`\`json\n${JSON.stringify(review)}\n\`\`\``;
      res.statusCode = 200;
      res.end(JSON.stringify({
        id: 'chatcmpl-1',
        object: 'chat.completion',
        created: 1,
        model: 'deepseek/deepseek-v4-flash',
        choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
        ...(r.usage ? { usage: r.usage } : {}),
      }));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const addr = server.address() as { port: number };
  const out: Obj[] = [];
  try {
    for (const sc of stub.scenarios as Obj[]) {
      responses = sc.responses as Obj[];
      idx = 0;
      httpRequests = [];
      chunkPaths = [...diffTextOf(spec, sc.diff).matchAll(/^diff --git a\/(\S+)/gm)].map((m) => m[1]!);
      byChunk = sc.byChunk === true;
      const scEnv = { OPENROUTER_API_KEY: 'sk-or-test-marker', ...(sc.env ?? {}) };
      const res = await runScenario(
        ctx,
        spec,
        { ...sc, env: scEnv },
        () => new N.rcOpenRouter.OpenRouterProvider(scEnv.OPENROUTER_API_KEY, { baseURL: `http://127.0.0.1:${addr.port}/api/v1` }),
      );
      out.push({
        name: sc.name,
        input: { manifest: spec.manifests[sc.manifest] ?? sc.manifest, diff: diffTextOf(spec, sc.diff), env: scEnv, responses: sc.responses },
        ...res,
        llmRequests: undefined,
        // Concurrent chunk requests arrive in any order: they are listed by chunk (then arrival within a chunk).
        httpRequests: httpRequests.map((r, i) => ({ r, i })).sort((a, b) => (a.r.chunk as number) - (b.r.chunk as number) || a.i - b.i).map(({ r }) => r),
      });
    }
  } finally {
    await new Promise<void>((resolve) => {
      server.close(() => resolve());
      (server as unknown as { closeAllConnections?: () => void }).closeAllConnections?.();
    });
  }
  return { reviewSchema: schemaFull, scenarios: out };
}
