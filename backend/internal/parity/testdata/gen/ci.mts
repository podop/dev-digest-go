/**
 * ci.json — the Node export-to-CI module (server/src/modules/ci): slug, trigger and workflow generation, the PR body,
 * the manifest (values and YAML round trip), the result artifact (parse, verify, ZIP read), GitHub error codes, the
 * real `CiIngestService` and `CiExportService` over in-memory fakes (the GitHub client, the store, the agents). The
 * workflow rules have their own golden (wfrules.json). Spec: FR6056, NFR6001, AC6036–AC6039, AC6042 (Phase 6); plan S16.
 */
import { createHash } from 'node:crypto';
import { isDeepStrictEqual } from 'node:util';
import { corpusJson, expandText, type Obj } from './common.mts';
import { attempt, attemptAsync, merge, recLog, showObj, showParse } from './common6.mts';
import type { Ctx } from './ctx.mts';

const ISO0 = '2026-01-01T00:00:00.000Z';
const WS = 'ws-1';
const ZIP_MTIME = Date.UTC(2026, 0, 1);

// ---------------------------------------------------------------------------
// Bytes and zips built from corpus specs
// ---------------------------------------------------------------------------

/** `{text}` / `{hex}` / `{json}` / `{parts: [...]}` → bytes. */
export function bytesOf(spec: Obj): Uint8Array {
  if (spec.parts) return Buffer.concat((spec.parts as Obj[]).map((p) => Buffer.from(bytesOf(p))));
  if (spec.hex !== undefined) return Buffer.from(spec.hex as string, 'hex');
  if (spec.json !== undefined) return Buffer.from(JSON.stringify(spec.json), 'utf8');
  return Buffer.from((spec.text ?? '') as string, 'utf8');
}

export function buildZip(ctx: Ctx, spec: Obj): Uint8Array {
  const { zipSync } = ctx.ddRequire('fflate') as Obj;
  const level = (spec.level ?? 6) as number;
  const make = (padLen: number): Uint8Array => {
    const files: Record<string, [Uint8Array, Obj]> = {};
    for (const e of (spec.entries ?? []) as Obj[]) files[e.name as string] = [bytesOf(e), { level, mtime: ZIP_MTIME }];
    if (spec.padToSize !== undefined) files['pad.bin'] = [new Uint8Array(padLen).fill(120), { level: 0, mtime: ZIP_MTIME }];
    return zipSync(files);
  };
  let bytes: Uint8Array;
  if (spec.padToSize !== undefined) {
    const base = make(0).length;
    bytes = make((spec.padToSize as number) - base);
    if (bytes.length !== spec.padToSize) throw new Error(`zip padding: got ${bytes.length}, wanted ${spec.padToSize}`);
  } else {
    bytes = make(0);
  }
  if (spec.truncate !== undefined) bytes = bytes.slice(0, spec.truncate as number);
  if (spec.corruptAt !== undefined) {
    bytes = Uint8Array.from(bytes);
    bytes[spec.corruptAt as number] = (bytes[spec.corruptAt as number]! ^ 0xff) & 0xff;
  }
  return bytes;
}

const ABSENT = '$absent';
/** Merge with `"$absent"` deleting the key (Node sees it as missing, not null). */
function mergeAbsent(base: Obj, over: Obj | undefined): Obj {
  const out = merge(base, over);
  for (const [k, v] of Object.entries(over ?? {})) if (v === ABSENT) delete out[k];
  return out;
}

/** An Octokit-like error: `status` and `response.headers` are what the Node code reads. */
function mkErr(spec: Obj): Error {
  const e = new Error((spec.message as string | undefined) ?? `scripted error ${spec.status ?? ''}`.trim()) as Error & Obj;
  if (spec.status !== undefined) e.status = spec.status;
  if (spec.headers !== undefined) e.response = { headers: spec.headers };
  return e;
}

/** Non-finite numbers cannot be written to JSON: they become `{"$number": "Infinity"}` (the README lists it). */
function jsonSafe(v: unknown): unknown {
  if (typeof v === 'number' && !Number.isFinite(v)) return { $number: String(v) };
  if (Array.isArray(v)) return v.map(jsonSafe);
  if (v !== null && typeof v === 'object') return Object.fromEntries(Object.entries(v as Obj).map(([k, x]) => [k, jsonSafe(x)]));
  return v;
}

const b64 = (b: Uint8Array): string => Buffer.from(b).toString('base64');
const sha256 = (b: Uint8Array): string => createHash('sha256').update(b).digest('hex');

// ---------------------------------------------------------------------------
// ci.json
// ---------------------------------------------------------------------------

export async function genCi(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const domain = expandText(corpusJson(ctx, 'ci/domain.json'));
  const flows = expandText(corpusJson(ctx, 'ci/flows.json'));
  const C = N.ciConstants;
  const codec = new N.ciYaml.YamlLibCodec();
  const arYaml = N.arRequire('yaml') as Obj;

  const constants = {
    AGENTS_DIR: C.AGENTS_DIR,
    SKILLS_DIR: C.SKILLS_DIR,
    MEMORY_FILE_PATH: C.MEMORY_FILE_PATH,
    RUNNER_ENTRY_PATH: C.RUNNER_ENTRY_PATH,
    RUNNER_RUN_COMMAND: C.RUNNER_RUN_COMMAND,
    WORKFLOWS_DIR: C.WORKFLOWS_DIR,
    CI_BRANCH: C.CI_BRANCH,
    CI_PR_TITLE: C.CI_PR_TITLE,
    CI_COMMIT_MESSAGE: C.CI_COMMIT_MESSAGE,
    RESULT_FILE_NAME: C.RESULT_FILE_NAME,
    SLUG_MAX_LENGTH: C.SLUG_MAX_LENGTH,
    SLUG_PATTERN: C.SLUG_PATTERN.source,
    AGENT_SLUG_FALLBACK: C.AGENT_SLUG_FALLBACK,
    SKILL_SLUG_FALLBACK: C.SKILL_SLUG_FALLBACK,
    ALLOWED_TRIGGERS: C.ALLOWED_TRIGGERS,
    POST_AS_VALUES: C.POST_AS_VALUES,
    RUNNER_OS: C.RUNNER_OS,
    NODE_VERSION: C.NODE_VERSION,
    FORK_GUARD_EXPR: C.FORK_GUARD_EXPR,
    SECRET_NAMES: C.SECRET_NAMES,
    ACTION_PINS: C.ACTION_PINS,
    MAX_WORKFLOW_BYTES: C.MAX_WORKFLOW_BYTES,
    MAX_ARTIFACT_ZIP_BYTES: C.MAX_ARTIFACT_ZIP_BYTES,
    MAX_ARTIFACT_JSON_BYTES: C.MAX_ARTIFACT_JSON_BYTES,
    RUNS_PER_INSTALLATION: C.RUNS_PER_INSTALLATION,
    REFRESH_BUDGET_MS: C.REFRESH_BUDGET_MS,
    paths: {
      manifestPathFor: C.manifestPathFor('sec'),
      skillPathFor: C.skillPathFor('api-safety'),
      workflowPathFor: C.workflowPathFor('sec'),
      checkNameFor: C.checkNameFor('sec'),
      artifactNameFor: C.artifactNameFor('sec'),
    },
  };

  const slugs = (domain.slugs as Obj[]).map((c) => ({
    ...c,
    slug: c.fallback === undefined ? N.ciSlug.slugify(c.name) : N.ciSlug.slugify(c.name, c.fallback),
  }));
  const triggers = (domain.triggers as string[][]).map((t) => ({ input: t, result: N.ciWorkflow.normalizeTriggers(t) }));
  const workflows = (domain.workflows as Obj[]).map((w) => ({ ...w, ...attempt(() => N.ciWorkflow.buildWorkflow(w)) }));
  const prBodies = (domain.prBodies as Obj[]).map((p) => ({ ...p, body: N.ciWorkflow.buildPrBody(p) }));

  const manifests = (domain.manifests as Obj[]).map((c) => {
    const agent = merge(domain.agent, c.agent);
    const out: Obj = { name: c.name, agent, skills: c.skills, ...attempt(() => N.ciManifest.buildManifest(agent, c.skills)) };
    if (out.result) out.yaml = codec.stringify(out.result);
    return out;
  });
  const yamlValues = (domain.yamlValues as string[]).map((v) => {
    const manifest = N.ciManifest.buildManifest({ ...domain.agent, name: v === '' ? 'n' : v, system_prompt: v }, []);
    const text = codec.stringify(manifest);
    let back: unknown;
    let runnerBack: unknown;
    try { back = codec.parse(text); } catch (e) { back = { parseError: (e as Error).message }; }
    try { runnerBack = arYaml.parse(text); } catch (e) { runnerBack = { parseError: (e as Error).message }; }
    return {
      value: v,
      manifest,
      nodeYaml: text,
      roundTrips: isDeepStrictEqual(back, manifest),
      runnerRoundTrips: isDeepStrictEqual(runnerBack, manifest),
    };
  });

  const artifacts = (domain.artifacts as Obj[]).map((c) => {
    const bytes = bytesOf(c.base64 !== undefined ? { parts: [{ hex: Buffer.from(c.base64 as string, 'base64').toString('hex') }] } : c);
    return { ...c, bytesLen: bytes.length, result: jsonSafe(N.ciIngest.parseResultArtifact(bytes)) };
  });

  const verify = (domain.verify.cases as Obj[]).map((c) => {
    const artifact = mergeAbsent(domain.verify.base.artifact, c.artifact);
    const run = merge(domain.verify.base.run, c.run);
    const workflowPath = (c.workflowPath ?? domain.verify.base.workflowPath) as string;
    return { name: c.name, artifact, run, workflowPath, result: N.ciIngest.verifyArtifact(artifact, run, workflowPath) };
  });

  const githubErrors = (domain.githubErrors as Obj[]).map((c) => {
    const err = c.err === '$Error' ? new Error('boom') : c.err;
    return { name: c.name, err: c.err, status: N.ciIngest.githubStatus(err) ?? null, code: N.ciIngest.githubErrorCode(err) };
  });

  const reader = new N.ciZip.FflateZipEntryReader();
  const zips = (domain.zips as Obj[]).map((c) => {
    const bytes = c.raw ? bytesOf(c.raw) : buildZip(ctx, c);
    const entry = reader.read(bytes, C.RESULT_FILE_NAME, C.MAX_ARTIFACT_JSON_BYTES) as Obj;
    return {
      name: c.name,
      spec: c,
      zipLen: bytes.length,
      ...(bytes.length <= 12_288 ? { zipBase64: b64(bytes) } : { zipSha256: sha256(bytes) }),
      entry: entry.kind === 'entry' ? { kind: 'entry', length: entry.bytes.length, text: Buffer.from(entry.bytes).toString('utf8') } : entry,
      result: N.ciArtifactReader.readResultArtifact(bytes, reader),
    };
  });

  const requestSchemas = (flows.requestSchemas as Obj[]).map((v) => ({
    schema: v.schema,
    cases: (v.inputs as unknown[]).map((input) => ({ input, ...showParse(N.shared[v.schema as string].safeParse(input)) })),
  }));

  const ingest = await genIngest(ctx, flows.ingest as Obj);
  const exportFlows = await genExport(ctx, flows.export as Obj);

  ctx.writer.write('ci.json', {
    note: 'Node export-to-CI at d1c9093. The Node workflow still has the "Set up Node.js" step and runs `node .devdigest/runner/index.js` (the Go template differs there on purpose, D6007); the Node files list has the bundle at .devdigest/runner/index.js (Go: .gz.b64 + .sha256, D6003/D6008); the Node PR body has no "### Runner" paragraph. yamlValues.nodeYaml is informational (byte identity is not required, D6010); roundTrips says whether Node itself reads its own text back to the same values. zips carry Node-built ZIPs (fflate, fixed mtime) up to 12 KiB; Node picks the named entry whichever other entries exist and cannot see duplicates (D6018 is a Go hardening). ingest/export run the real services over fakes (the SQL of the store is not here).',
    constants,
    slugs,
    triggers,
    workflows,
    prBodies,
    manifests,
    yamlValues,
    artifacts,
    verify,
    githubErrors,
    zips,
    requestSchemas,
    ingest,
    export: exportFlows,
  });
}

// ---------------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------------

async function genIngest(ctx: Ctx, spec: Obj): Promise<Obj[]> {
  const { N } = ctx;
  const out: Obj[] = [];
  for (const sc of spec.scenarios as Obj[]) {
    const world = sc.world as Obj;
    let clock = Date.UTC(2026, 2, 2);
    const latency = (world.latency ?? 0) as number;
    const installs = ((world.installations ?? []) as Obj[]).map((i) => merge(spec.installation, i));
    const calls: Obj[] = [];
    const recorded: Obj[] = [];
    const storeCalls: Obj[] = [];
    const storedKeys = new Set<string>((world.stored ?? []) as string[]);
    const log = recLog();
    const byRepo = (repo: Obj): Obj | undefined => installs.find((i) => i.repo === `${repo.owner}/${repo.name}`);
    const advance = (): void => void (clock += latency);

    const github = {
      listWorkflowRuns: async (repo: Obj, workflowFile: string, opts: Obj) => {
        calls.push({ fn: 'listWorkflowRuns', repo: `${repo.owner}/${repo.name}`, workflowFile, opts });
        advance();
        const inst = byRepo(repo);
        const runs = inst ? world.runs?.[inst.id] : undefined;
        if (runs && !Array.isArray(runs) && runs.throw) throw mkErr(runs.throw);
        return ((runs ?? []) as Obj[]).map((r) => merge(spec.run, r));
      },
      listRunArtifacts: async (repo: Obj, runId: number) => {
        calls.push({ fn: 'listRunArtifacts', repo: `${repo.owner}/${repo.name}`, runId });
        advance();
        const arts = world.artifacts?.[String(runId)];
        if (arts && !Array.isArray(arts) && arts.throw) throw mkErr(arts.throw);
        return ((arts ?? []) as Obj[]).map((a) => merge(spec.artifact, a));
      },
      downloadArtifact: async (repo: Obj, artifactId: number, opts: Obj) => {
        calls.push({ fn: 'downloadArtifact', repo: `${repo.owner}/${repo.name}`, artifactId, opts });
        advance();
        const d = world.downloads?.[String(artifactId)] as Obj | undefined;
        if (!d) throw new Error(`no download scripted for artifact ${artifactId}`);
        if (d.throw) throw mkErr(d.throw);
        if (d.null) return null;
        let bytes: Uint8Array;
        if (d.raw) bytes = bytesOf(d.raw);
        else if (d.zip) bytes = buildZip(ctx, d.zip);
        else {
          const json = mergeAbsent(spec.result, d.result);
          bytes = buildZip(ctx, { entries: [{ name: N.ciConstants.RESULT_FILE_NAME, json }] });
        }
        // The real adapter never returns more than maxBytes (null instead).
        return bytes.length > opts.maxBytes ? null : bytes;
      },
    };

    const store = {
      listInstallations: async (_w: string, filter?: Obj) => {
        storeCalls.push({ fn: 'listInstallations', filter: filter ?? null });
        return installs.filter((i) => filter?.agentId === undefined || i.agent_id === filter.agentId);
      },
      storedRunKeys: async (keys: Obj[]) => {
        storeCalls.push({ fn: 'storedRunKeys', keys: keys.map((k) => `${k.id}:${k.attempt}`) });
        if (world.dbFail?.storedRunKeys) throw new Error(world.dbFail.storedRunKeys);
        return new Set(keys.map((k) => `${k.id}:${k.attempt}`).filter((k) => storedKeys.has(k)));
      },
      recordRun: async (run: Obj) => {
        const key = `${run.githubRunId}:${run.runAttempt}`;
        storeCalls.push({ fn: 'recordRun', key });
        if (world.dbFail?.recordRun) throw new Error(world.dbFail.recordRun);
        if ((world.recordFalse ?? []).includes(key) || storedKeys.has(key)) return false;
        storedKeys.add(key);
        recorded.push({ ...run, ranAt: run.ranAt.toISOString() });
        return true;
      },
    };

    const svc = new N.ciIngestService.CiIngestService({
      agents: { exists: async (_w: string, id: string) => (world.agents ?? []).includes(id), get: async () => undefined },
      store,
      github: async () => {
        if (world.githubFail === 'config') throw new N.errors.ConfigError('GitHub token is not configured', undefined, 'config_error');
        return github;
      },
      zip: new N.ciZip.FflateZipEntryReader(),
      now: () => clock,
    });
    const outcome = await attemptAsync(() => svc.refresh(WS, sc.agentId, log));
    out.push({ name: sc.name, agentId: sc.agentId ?? null, world, ...outcome, recorded, githubCalls: calls, storeCalls, logs: log.lines.map((l) => ({ level: l.level, msg: l.msg, obj: showObj(l.obj) })) });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Export
// ---------------------------------------------------------------------------

function resolveWorkflowRefs(ctx: Ctx, v: unknown): unknown {
  if (Array.isArray(v)) return v.map((x) => resolveWorkflowRefs(ctx, x));
  if (v !== null && typeof v === 'object') {
    const o = v as Obj;
    if (o.$workflow) return ctx.N.ciWorkflow.buildWorkflow(o.$workflow);
    return Object.fromEntries(Object.entries(o).map(([k, x]) => [k, resolveWorkflowRefs(ctx, x)]));
  }
  return v;
}

async function genExport(ctx: Ctx, spec: Obj): Promise<Obj[]> {
  const { N } = ctx;
  const out: Obj[] = [];
  for (const sc of spec.scenarios as Obj[]) {
    const world = sc.world as Obj;
    const log = recLog();
    const calls: Obj[] = [];
    let saved: Obj | undefined;
    const agent = merge(spec.agent, world.agent);
    let failFor: Obj = {};

    const installationOf = (agentId: string, repo: string): Obj | undefined =>
      saved ?? (world.installation ? merge({ id: 'inst-1', agent_id: agentId, repo, target_type: 'gha', installed_at: ISO0, slug: 'security-reviewer', workflow_path: null, manifest_version: 2, last_run: null }, world.installation) : undefined);

    const client = {
      commitFiles: async (ref: Obj, opts: Obj) => {
        calls.push({ fn: 'commitFiles', ref, branch: opts.branch, base: opts.base, message: opts.message, paths: (opts.files as Obj[]).map((f) => f.path) });
        if (failFor.commitFiles) throw mkErr(failFor.commitFiles);
      },
      findOpenPr: async (ref: Obj, branch: string) => {
        calls.push({ fn: 'findOpenPr', ref, branch });
        if (failFor.findOpenPr) throw mkErr(failFor.findOpenPr);
        return world.openPr ?? null;
      },
      openPullRequest: async (ref: Obj, payload: Obj) => {
        calls.push({ fn: 'openPullRequest', ref, ...payload });
        if (failFor.openPullRequest) throw mkErr(failFor.openPullRequest);
        return { url: 'https://github.com/acme/widgets/pull/12' };
      },
    };
    const store = {
      findInstallation: async (_w: string, agentId: string, repo: string) => installationOf(agentId, repo),
      slugOwner: async (repo: string, slug: string) => {
        calls.push({ fn: 'slugOwner', repo, slug });
        return world.slugOwner;
      },
      upsertInstallation: async (values: Obj) => {
        calls.push({ fn: 'upsertInstallation', values });
        if (world.upsert === 'slug_taken') return { kind: 'slug_taken' };
        if (!world.vanishAfterUpsert) {
          saved = { id: 'inst-new', agent_id: values.agentId, repo: values.repo, target_type: 'gha', installed_at: ISO0, slug: values.slug, workflow_path: N.ciConstants.workflowPathFor(values.slug), manifest_version: values.manifestVersion, last_run: null };
        }
        return { kind: 'saved' };
      },
      listInstallations: async (_w: string, filter?: Obj) => {
        calls.push({ fn: 'listInstallations', filter: filter ?? null });
        return ((world.installations ?? []) as Obj[]).map((i) => merge({ id: 'inst-1', agent_id: 'agent-1', repo: 'acme/widgets', target_type: 'gha', installed_at: ISO0, slug: 'security-reviewer', workflow_path: null, manifest_version: 3, last_run: null }, i));
      },
      storedRunKeys: async () => new Set<string>(),
      recordRun: async () => true,
      listRuns: async () => [],
    };
    const svc = new N.ciExport.CiExportService({
      agents: {
        get: async (_w: string, id: string) => (id === 'agent-1' ? agent : undefined),
        exists: async (_w: string, id: string) => id === 'agent-1',
      },
      repos: { findByFullName: async (_w: string, fullName: string) => (fullName === spec.repo.fullName ? spec.repo : undefined) },
      store,
      bundle: { read: async () => (world.runner === null ? null : (world.runner ?? 'RUNNER-BUNDLE-STUB')) },
      github: async () => client,
      yaml: new N.ciYaml.YamlLibCodec(),
    });

    const steps: Obj[] = [];
    for (const raw of sc.steps as Obj[]) {
      const step = resolveWorkflowRefs(ctx, raw) as Obj;
      failFor = (step.githubFail ?? {}) as Obj;
      saved = undefined;
      const before = calls.length;
      const logBefore = log.lines.length;
      let outcome: Obj;
      if (step.op === 'export') {
        const parsed = N.shared.CiExportInput.safeParse(step.body);
        outcome = parsed.success ? await attemptAsync(() => svc.export(WS, step.agentId, parsed.data, log)) : { parse: showParse(parsed) };
      } else if (step.op === 'listInstallations') {
        outcome = await attemptAsync(() => svc.listInstallations(WS, step.agentId));
      } else throw new Error(`unknown export op ${step.op}`);
      steps.push({ ...step, ...outcome, calls: calls.slice(before), logs: log.lines.slice(logBefore).map((l) => ({ level: l.level, msg: l.msg, obj: showObj(l.obj) })) });
    }
    out.push({ name: sc.name, world, steps });
  }
  return out;
}
