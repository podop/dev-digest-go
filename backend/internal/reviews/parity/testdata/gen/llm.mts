/** llm.json — OpenRouterProvider (reviewer-core) driven over HTTP against a local server. */
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { deepStrictEqual } from 'node:assert/strict';
import { corpusJson, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

const dataOf = (v: unknown): unknown => JSON.parse(JSON.stringify(v));

export async function genLlm(ctx: Ctx): Promise<void> {
  const spec = corpusJson(ctx, 'schema/llm.json');
  const strict = ctx.N.rc.toJsonSchema(ctx.N.shared.Review, 'Review');
  const scenarios: Obj[] = [];
  for (const s of spec.scenarios as Obj[]) {
    const models: string[] = s.models ?? [s.model];
    const runs: Obj[] = [];
    for (const model of models) runs.push(await runScenario(ctx, s, model, strict.schema));
    scenarios.push({ name: s.name, provider: s.provider, temperature: s.temperature, maxRetries: s.maxRetries, sessionId: s.sessionId ?? null, responses: s.responses, runs });
  }
  ctx.writer.write('llm.json', {
    note: 'Each run drives OpenRouterProvider.completeStructured({model, schema: Review, schemaName: "Review", messages: [system "SYSTEM", user "USER"], maxRetries, temperature, sessionId}) against a local HTTP server that answers `responses` in order. `requests` are the parsed JSON bodies the SDK sent; response_format equal to the strict Review schema (schema.json strict) is shown as "$strictReview". `usage` are the onUsage reports (one per response), `warnings` the onWarning texts. `result` is the StructuredResult or `error` the thrown message. The request key order is the SDK order.',
    scenarios,
  });
}

export interface FakeProvider {
  baseURL: string;
  requests: Obj[];
  close(): Promise<void>;
}

/** A local OpenAI-compatible server that answers `responses` in order and records request bodies. */
export async function startFakeProvider(responses: Obj[], model: string): Promise<FakeProvider> {
  const requests: Obj[] = [];
  let served = 0;
  const server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const chunks: Buffer[] = [];
    req.on('data', (d: Buffer) => chunks.push(d));
    req.on('end', () => {
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8')) as Obj;
      requests.push({ path: req.url, authorization: req.headers.authorization, body });
      const r = responses[served++];
      res.setHeader('content-type', 'application/json');
      if (!r) {
        res.statusCode = 400;
        res.end(JSON.stringify({ error: { message: 'no scripted response left' } }));
        return;
      }
      const payload = r.raw ?? {
        id: `gen-${served}`,
        object: 'chat.completion',
        created: 0,
        model,
        choices: [{ index: 0, finish_reason: 'stop', message: { role: 'assistant', content: r.content } }],
        ...(r.usage ? { usage: r.usage } : {}),
      };
      res.end(JSON.stringify(payload));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const port = (server.address() as AddressInfo).port;
  return {
    baseURL: `http://127.0.0.1:${port}/v1`,
    requests,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

/** Request bodies as shown in goldens: the strict Review response_format is abbreviated. */
export function showRequests(requests: Obj[], strictSchema: unknown): Obj[] {
  return requests.map((r) => {
    const body = { ...(r.body as Obj) };
    const rf = body.response_format as Obj;
    try {
      deepStrictEqual(rf.json_schema.schema, strictSchema);
      if (rf.type !== 'json_schema' || rf.json_schema.name !== 'Review' || rf.json_schema.strict !== true) throw new Error('shape');
      body.response_format = '$strictReview';
    } catch {
      /* keep the full response_format when it differs */
    }
    return { path: r.path, authorization: r.authorization, body };
  });
}

async function runScenario(ctx: Ctx, s: Obj, model: string, strictSchema: unknown): Promise<Obj> {
  const { OpenRouterProvider } = ctx.N.openrouter;
  const { shared } = ctx.N;
  const fake = await startFakeProvider(s.responses as Obj[], model);
  const warnings: string[] = [];
  const usage: Obj[] = [];
  const provider = new OpenRouterProvider('sk-test-key', {
    baseURL: fake.baseURL,
    id: s.provider,
    maxRetries: 0,
    onWarning: (m: string) => warnings.push(m),
  });
  let result: Obj | undefined;
  let error: string | undefined;
  try {
    result = await provider.completeStructured({
      model,
      schema: shared.Review,
      schemaName: 'Review',
      messages: [
        { role: 'system', content: 'SYSTEM' },
        { role: 'user', content: 'USER' },
      ],
      maxRetries: s.maxRetries,
      ...(s.temperature !== null ? { temperature: s.temperature } : {}),
      ...(s.sessionId ? { sessionId: s.sessionId } : {}),
      onUsage: (u: Obj) => usage.push(u),
    });
  } catch (err) {
    error = (err as Error).message;
  } finally {
    await fake.close();
  }
  return { model, requests: showRequests(fake.requests, strictSchema), usage, warnings, ...(result ? { result: dataOf(result) } : { error }) };
}
