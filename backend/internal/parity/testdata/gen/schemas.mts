/**
 * schemas.json — the provider-facing JSON schemas of IntentClassification, PrBriefLlm and OnboardingTour
 * (`toJsonSchema`), parseWithRepair over invalid answers, the mock LLM fixtures and the call parameters of the
 * three feature models (AC5057, AC5058).
 */
import { corpusJson, expandText, showError, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

export async function genSchemas(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const spec = expandText(corpusJson(ctx, 'schemas/parse.json'));
  const schemas: Record<string, unknown> = {
    IntentClassification: N.intentClassification.IntentClassification,
    PrBriefLlm: N.briefPrompt.BriefLlmOutput,
    OnboardingTour: N.onbPrompt.OnboardingLlmOutput,
  };

  const out: Obj = {};
  for (const [name, schema] of Object.entries(schemas)) {
    const strict = N.rc.toJsonSchema(schema, name);
    const parse = ((spec[name] as Obj).cases as Obj[]).map((c) => {
      const raw = c.raw !== undefined ? (c.raw as string) : JSON.stringify(c.value);
      const r = N.rc.parseWithRepair(schema, raw);
      return { name: c.name, raw, ...(r.ok ? { ok: true, data: r.data } : { ok: false, error: r.error, repromptMessage: r.repromptMessage }) };
    });
    out[name] = { schemaName: name, strict, parse };
  }

  // Mock LLM fixtures (adapters/llm/mock.ts), through the real provider and the real Zod schemas.
  const mock: Obj[] = [];
  const passThrough = { parse: (x: unknown) => x };
  for (const schemaName of (spec.mock as Obj).schemas as string[]) {
    const provider = new N.mockLlm.MockReviewLLMProvider('openrouter');
    const usage: Obj[] = [];
    const schema = (schemas[schemaName] as { parse(x: unknown): unknown } | undefined) ?? passThrough;
    try {
      const res = await provider.completeStructured({
        model: 'mock/reviewer',
        schemaName,
        schema,
        messages: (spec.mock as Obj).messages,
        onUsage: (u: Obj) => void usage.push(u),
      });
      mock.push({ schemaName, usageReports: usage, result: res });
    } catch (e) {
      mock.push({ schemaName, usageReports: usage, error: showError(e) });
    }
  }

  // Call parameters of the three feature models through the real adapters and a fake provider.
  const calls: Obj[] = [];
  const fakeProvider = (record: Obj[], fail?: Error) => async () => ({
    completeStructured: async (req: Obj) => {
      record.push({ model: req.model, schemaName: req.schemaName, temperature: req.temperature, maxTokens: req.maxTokens, maxRetries: req.maxRetries, hasSignal: req.signal instanceof AbortSignal, hasUsageObserver: typeof req.onUsage === 'function', hasSessionId: req.sessionId !== undefined, roles: (req.messages as Obj[]).map((m) => m.role) });
      if (fail) throw fail;
      return { data: req.schema.parse(req.__fixture), tokensIn: 7, tokensOut: 8, costUsd: 0.5, attempts: 2 };
    },
  });
  const choice = { provider: 'openrouter', model: 'm' };
  const messages = [{ role: 'system', content: 'S' }, { role: 'user', content: 'U' }];
  {
    const rec: Obj[] = [];
    const model = new N.intentModel.LlmIntentModel({ resolveModel: async () => choice, llm: fakeProvider(rec) });
    const resolved = await model.resolve('ws');
    const wrapped = async () => {
      const p = (await fakeProvider(rec)()) as Obj;
      return { ...p, completeStructured: (req: Obj) => p.completeStructured({ ...req, __fixture: N.mockLlm.MOCK_INTENT_CLASSIFICATION }) };
    };
    const m2 = new N.intentModel.LlmIntentModel({ resolveModel: async () => choice, llm: wrapped });
    const res = await m2.classify(resolved, messages, new AbortController().signal);
    calls.push({ feature: 'intent', resolved, requests: rec, result: res });
    const rec2: Obj[] = [];
    const m3 = new N.intentModel.LlmIntentModel({ resolveModel: async () => choice, llm: fakeProvider(rec2, new N.errors.ConfigError('OPENROUTER_API_KEY is not configured')) });
    try {
      await m3.classify(resolved, messages, new AbortController().signal);
    } catch (e) {
      calls.push({ feature: 'intent', case: 'provider error is passed through unmapped', requests: rec2, error: showError(e) });
    }
  }

  ctx.writer.write('schemas.json', {
    note: 'strict = toJsonSchema(schema, name) = {schema, name}: the schema sent as response_format.json_schema.schema (compare as JSON values, key order is not significant for objects). parse = parseWithRepair(schema, raw): ok + the Zod-parsed data (unknown keys stripped, absent optional keys stay absent) or the issue text and the reprompt message (V8 JSON error texts are not reproducible in Go: compare the "Output was not valid JSON: " prefix). mock = the real MockReviewLLMProvider.completeStructured per schema name (raw is the exact JSON text the mock returns). calls = request parameters the feature adapters hand to the provider. The brief and onboarding adapters are covered by the llmRequests of brief.json / onboarding.json.',
    constants: {
      mockUsage: N.mockLlm.MOCK_USAGE,
      mockIntentClassification: N.mockLlm.MOCK_INTENT_CLASSIFICATION,
      mockPrBrief: N.mockLlm.MOCK_PR_BRIEF,
    },
    schemas: out,
    mock,
    calls,
  });
}
