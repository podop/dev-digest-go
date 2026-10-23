/**
 * wfrules.json — the Node workflow rule check (`checkWorkflow`: size limit, YAML parse, document shape, the seven
 * rules of ci/domain/workflow-rules.ts) over a corpus of workflow texts, with the real `yaml` package of the server
 * checkout. Every case carries its full text, so the file is self-contained. Spec: FR6056, NFR6001, AC6040–AC6041,
 * D6020 (Phase 6); plan S16. See ./README.md.
 */
import { corpusJson, expandText, type Obj } from './common.mts';
import type { Ctx } from './ctx.mts';

/** Builds the text of one corpus case (see the note of corpus/wfrules/cases.json). */
function textOfCase(ctx: Ctx, spec: Obj, go: string): string {
  const { N } = ctx;
  let text: string;
  if (spec.gen) text = N.ciWorkflow.buildWorkflow(spec.gen);
  else if (spec.text !== undefined) text = spec.text as string;
  else if (spec.from === 'go') text = go;
  else if (spec.from === 'base') text = N.ciWorkflow.buildWorkflow({ slug: 'security-reviewer', triggers: ['opened', 'synchronize', 'reopened'], postAs: 'github_review' });
  else throw new Error(`wfrules case "${spec.name}" has no source`);

  for (const [from, to] of (spec.replace ?? []) as Array<[string, string]>) {
    if (!text.includes(from)) throw new Error(`wfrules case "${spec.name}": text to replace not found: ${JSON.stringify(from)}`);
    text = text.replace(from, () => to);
  }
  if (spec.cutFrom !== undefined) {
    const i = text.indexOf(spec.cutFrom as string);
    if (i < 0) throw new Error(`wfrules case "${spec.name}": cutFrom not found: ${JSON.stringify(spec.cutFrom)}`);
    text = text.slice(0, i);
  }
  if (spec.prepend !== undefined) text = (spec.prepend as string) + text;
  if (spec.append !== undefined) text += spec.append as string;
  if (spec.padToBytes !== undefined) {
    const target = spec.padToBytes as number;
    const ch = (spec.padChar ?? 'x') as string;
    text += '\n#';
    const rem = target - Buffer.byteLength(text, 'utf8');
    const per = Buffer.byteLength(ch, 'utf8');
    if (rem < 0) throw new Error(`wfrules case "${spec.name}": text already over ${target} bytes`);
    text += ch.repeat(Math.floor(rem / per)) + 'x'.repeat(rem % per);
    if (Buffer.byteLength(text, 'utf8') !== target) throw new Error(`wfrules case "${spec.name}": padding missed ${target}`);
  }
  if (spec.crlf) text = text.replace(/\n/g, '\r\n');
  if (spec.cr) text = text.replace(/\n/g, '\r');
  return text;
}

export async function genWfRules(ctx: Ctx): Promise<void> {
  const { N } = ctx;
  const corpus = expandText(corpusJson(ctx, 'wfrules/cases.json'));
  const codec = new N.ciYaml.YamlLibCodec();
  const cases = (corpus.cases as Obj[]).map((spec) => {
    const text = textOfCase(ctx, spec, corpus.go as string);
    const bytes = Buffer.byteLength(text, 'utf8');
    const result = N.ciCheck.checkWorkflow(text, codec) as Obj;
    // Which stage decided, for diagnosis (the Go check only has to agree on `result`).
    let stage: string;
    let yamlCode: string | undefined;
    if (N.ciRules.workflowTooLarge(text)) stage = 'size';
    else {
      let doc: unknown;
      let parsed = true;
      try {
        doc = codec.parse(text);
      } catch (e) {
        parsed = false;
        yamlCode = (e as { code?: string }).code ?? (e as Error).name;
      }
      stage = !parsed ? 'parse' : result.ok ? 'ok' : (N.ciRules.checkWorkflowDocument(doc) as Obj).ok === false && result.rule === 'invalid_yaml' ? 'shape' : 'rule';
    }
    return {
      name: spec.name as string,
      text,
      bytes,
      result: result.ok ? { ok: true } : { ok: false, rule: result.rule },
      stage,
      ...(yamlCode !== undefined ? { yamlCode } : {}),
    };
  });
  ctx.writer.write('wfrules.json', {
    note: 'Node checkWorkflow (ci/application/workflow-check.ts + domain/workflow-rules.ts) with the yaml package of the server checkout at d1c9093. result = {ok:true} or {ok:false, rule} with rule in invalid_yaml | forbidden_trigger | permissions | secrets | unpinned_action | fork_guard | untrusted_expression; first broken rule wins. stage = size (over 65,536 UTF-8 bytes, before parsing) | parse (yaml.parse threw; yamlCode is the library code, informational) | shape (parsed, but the root/jobs shape is wrong) | rule (a rule rejected it) | ok. texts over 16,384 units are $blob digests (rebuild them from corpus/wfrules/cases.json: the corpus is the source). The Go workflow rules must agree on result for every case; the yaml.v3 differences found while generating are documented in README.md.',
    maxWorkflowBytes: N.ciConstants.MAX_WORKFLOW_BYTES,
    cases,
  });
}
