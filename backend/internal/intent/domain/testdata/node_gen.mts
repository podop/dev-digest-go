/**
 * Generates node_cases.json and intent_schema.node.json: reference values for the intent domain
 * (links.go, key.go, sources.go, classify.go, clamp.go, schema.go) computed by the Node implementation of
 * dev-digest (commit d1c9093, modules/intent/domain/*) from the read-only checkout. Nothing is written to the
 * checkout; it must be clean before and after.
 *
 *   DD=/home/ypodop/IdeaProjects/dev-digest
 *   mkdir -p /tmp/p5c
 *   cd backend/internal/intent/domain/testdata
 *   TMPDIR=/tmp/p5c $DD/server/node_modules/.bin/tsx --tsconfig $DD/server/tsconfig.json node_gen.mts --dd $DD --out .
 *
 * (A long TMPDIR breaks tsx's IPC socket.) Output is deterministic: a seeded generator, no clock.
 * Inputs never contain lone surrogates (JSON cannot carry them into a Go string).
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const args = process.argv.slice(2);
const arg = (name: string): string => {
  const i = args.indexOf(name);
  if (i < 0 || !args[i + 1]) throw new Error(`missing ${name}`);
  return args[i + 1]!;
};
const dd = resolve(arg('--dd'));
const out = resolve(arg('--out'));

const git = (...a: string[]): string =>
  execFileSync('git', ['--no-optional-locks', '-C', dd, ...a], { encoding: 'utf8' }).trim();
const requireClean = (): void => {
  if (git('rev-parse', '--short=7', 'HEAD') !== 'd1c9093') throw new Error('dev-digest is not at d1c9093');
  if (git('status', '--porcelain') !== '') throw new Error('dev-digest checkout is not clean');
};
requireClean();

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Mod = any;
const load = async (rel: string): Promise<Mod> => import(pathToFileURL(join(dd, rel)).href);
const I = 'server/src/modules/intent/domain/';
const links = await load(`${I}links.ts`);
const intent = await load(`${I}intent.ts`);
const classification = await load(`${I}classification.ts`);
const rc = await load('reviewer-core/src/index.ts');

let seed = 20261023;
const rnd = (): number => ((seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0) / 2 ** 32);
const pick = <T>(a: readonly T[]): T => a[Math.floor(rnd() * a.length)]!;
const int = (n: number): number => Math.floor(rnd() * n);

const repo = { owner: 'acme', name: 'api' };
const repos = [repo, { owner: 'Acme', name: 'API' }, { owner: 'o.w-n_er', name: 'r.e-p_o' }];

// ---- tickets -----------------------------------------------------------------
const ticketBits = [
  '#12', '#7', '#007', '#1', '(#3)', 'closes #5', 'Closes #5', 'fixes #8', 'Fixed #9', 'resolve #10', 'resolved #11', 'RESOLVES #13',
  'fix #14', 'fixing #15', 'closed #16', 'prefix #17', 'acme/api#9', 'Acme/Api#9', 'other/repo#4', 'closes other/repo#4', 'x/y#', 'a/b#c',
  '/#5', 'a#6', 'word#18', '_#19', '-#20', 'https://github.com/acme/api/issues/21', 'https://github.com/ACME/API/issues/22',
  'http://github.com/x/y/issues/2', 'fixes https://github.com/acme/api/issues/23', 'https://github.com/acme/api/issues/24x',
  'https://github.com/acme/api/issues/25/', 'ABC-123', 'DEVX-42', 'UTF-8', 'SHA-256', 'ES-2024', 'X-1', 'A-1', 'ABCDEFGHIJK-1', 'ABC-1234567',
  'IPV-6', 'http-1', 'HTTP-2', 'GPT-4', '#99999999999999999999', '#9007199254740993', '#9007199254740992', '#0', '#00',
  '#', '# 5', '#5a', '#a5', '#5_', 'closes', 'close', 'closes\n#30', 'closes the issue #31', 'fixes  #32', 'resolves x #33', 'xfixes #34',
  '😀#35', 'é#36', '😀 closes #37', 'ſ#38', 'K#39', 'closes #40', '.', ',', '\n', ' ', '\t', '\r\n', 'é', '😀', '漢',
];
const ticketText = (): string => {
  let s = '';
  for (let k = int(8); k >= 0; k--) s += pick(ticketBits) + pick(['', ' ', ' ', '\n', ', ', '/', '-']);
  if (rnd() < 0.02) s += ' #' + '9'.repeat(400); // Number(...) = Infinity
  return s;
};
const tickets: object[] = [];
for (let i = 0; i < 450; i++) {
  const r = repos[i % 3]!;
  const texts = Array.from({ length: int(5) }, () => (rnd() < 0.15 ? '' : ticketText()));
  const res = links.extractTicketRefs(texts, r);
  tickets.push({
    repo: r,
    texts,
    refs: res.refs.map((t: { number: number; closing: boolean }) => ({ ref: String(t.number), closing: t.closing })),
    skipped: res.skipped,
  });
}

// ---- docs --------------------------------------------------------------------
const docBits = [
  '[doc](docs/plans/a.md)', '[x](./specs/b.md)', '[l](https://github.com/acme/api/blob/main/docs/c.md)',
  '[l](https://github.com/other/r/blob/main/d.md)', '[l](https://example.com/x.md)', '[l](HTTPS://github.com/acme/api/blob/main/z.md)',
  '[l](Http://github.com/acme/api/blob/main/z.md)', '[a](https://github.com/acme/api/blob/main/docs/%E2%9C%93.md)',
  '[bad](https://github.com/acme/api/blob/main/docs/%E2%9C.md)', '[bad2](https://github.com/acme/api/blob/main/docs/%.md)',
  '[l](https://github.com/acme/api/tree/dev/specs/s.mdx)', '[l](https://github.com/ACME/API/blob/main/Docs/E.MD)',
  '[l](https://github.com/acme/api/blob/main/)', '[l](https://github.com/acme/api/issues/3)', '[l](https://github.com/acme/api/blob/a/b/c.txt)',
  '[x](a.md)', '[x](a b.md)', '[x]()', '[x](a.md', '[](x.md)', '[a\n](n.md)', '[x](../up.md)', '[x](/abs.md)', '[x](docs\\w.md)',
  '[x](a.pdf)', '[x](docs/%2e%2e/x.md)', '[x](docs/%41.md)', '[x](docs/a.md))', '[x](docs/a.md).', '[x](#anchor)', '[x](mailto:a@b.c)',
  'https://github.com/acme/api/blob/main/docs/f.md', 'https://github.com/other/r/blob/main/docs/f.md', 'https://github.com/acme/api/tree/v1/specs/g.md)',
  'https://github.com/acme/api/blob/main/docs/%C3%A9.md', 'https://github.com/acme/api/blob/main/docs/%C3.md',
  'https://github.com/acme/api/blob/main/docs/a%20b.md', 'https://example.com/docs/h.md', 'http://x.y/z.txt',
  'see docs/readme.md,', 'see docs/readme.md.', '(notes.txt)', '[notes.txt]', '../etc/passwd.md', '/abs/x.md', 'a\\b.md', 'docs/x.MD', 'README.markdown.',
  'README.MarkDown', 'docs/plans/p.md', 'docs/plans/Q.md', 'docs/specs/s.md', 'specs/top.md', 'a/specs/mid.md', 'docs/other.md', 'Docs/other.md',
  'z.md', 'Z.md', 'a.md', 'é.md', '😀.md', 'ä.md', 'doc-1.md', 'doc_1.md', '.md', 'x.mdx', 'x.mdxx', 'x.md5', 'x.txt', 'x.TXT', 'x.md.', 'x.md)',
  './a.md', './', '././b.md', 'a/./b.md', 'a..b.md', 'dir/.git/x.md', '-flag.md', 'x.md\0y.md', 'foo.md/bar', 'one/two/three/four.md', 'a.b.c.md',
  'ſ.md', 'x.Kaa.md', 'x.marKdown', '%', '%41.md', '%E2%9C%93', '.', ',', ')', '\n', ' ', '\t', 'é', '😀', '漢',
];
const docText = (): string => {
  let s = '';
  for (let k = int(8); k >= 0; k--) s += pick(docBits) + pick(['', ' ', ' ', '\n', ', ', ' and ', '/']);
  return s;
};
const docs: object[] = [];
for (let i = 0; i < 550; i++) {
  const r = repos[i % 3]!;
  const texts = Array.from({ length: int(4) }, () => (rnd() < 0.15 ? '' : docText()));
  let res: { refs: { path: string }[]; skipped: { ref: string; reason: string }[] } | undefined;
  let error: string | undefined;
  try {
    res = links.extractDocRefs(texts, r);
  } catch (e) {
    error = (e as Error).message;
  }
  docs.push(res ? { repo: r, texts, refs: res.refs.map((d) => d.path), skipped: res.skipped } : { repo: r, texts, error });
}

// ---- key ---------------------------------------------------------------------
const strBits = ['a', 'b', 'Z', 'é', 'ß', ' ', '\t', '\n', '\r', '"', '\\', '/', '<', '>', '&', ' ', ' ', '\u0000', '\u001f', '\u007f', '😀', '漢', '﻿', '\u0085', '-', '_', '.', '#'];
const word = (max: number): string => Array.from({ length: int(max) }, () => pick(strBits)).join('');
const bigNumbers = ['1', '2', '12', '007', '9007199254740993', '9007199254740992', '99999999999999999999', '1' + '0'.repeat(25), '3', '10', '100', '9'];
const keys: object[] = [];
for (let i = 0; i < 150; i++) {
  const refs = Array.from({ length: int(4) }, () => pick(bigNumbers));
  if (i % 40 === 0) refs.push('9'.repeat(400)); // Number(...) = Infinity → null in JSON
  const paths = Array.from({ length: int(5) }, () => word(5));
  const body = rnd() < 0.2 ? null : word(8);
  const params = {
    provider: pick(['openrouter', 'anthropic', 'mock']),
    model: word(4) || 'deepseek/deepseek-v4-flash',
    title: word(8),
    body,
    branch: word(5),
    headSha: pick(['abc1234', 'DEF', word(3)]),
    ticketRefs: refs.map(Number),
    docPaths: paths,
  };
  const input = intent.buildCacheKeyInput(params);
  const canon = intent.canonicalJson(input);
  keys.push({
    provider: params.provider,
    model: params.model,
    title: params.title,
    body,
    branch: params.branch,
    head_sha: params.headSha,
    ticket_refs: refs,
    doc_paths: paths,
    canonical: canon,
    hash: createHash('sha256').update(canon).digest('hex'),
  });
}

// ---- substantive body --------------------------------------------------------
const bodyBits = [
  '# Title', '## Heading with text', '#', '# ', '# x', '###### six', '####### seven', '   # indented3', '    # indented4', '\t# tab',
  '<!-- comment -->', '<!--\nmulti\nline\n-->', '<!-- unterminated', '--> stray', 'Real description text goes here.', 'ok', 'x'.repeat(30),
  'x'.repeat(39), 'x'.repeat(40), '\r', '## Title\r', '## a b', '## a b', '## é', '# 😀', ' # nbsp', '　# ideo', '- [ ] item', '', ' ',
];
const bodies: object[] = [];
for (let i = 0; i < 300; i++) {
  const body = Array.from({ length: int(8) }, () => pick(bodyBits)).join(pick(['\n', '\n', '\r\n', ' ', '']));
  bodies.push({
    body,
    stripped: intent.stripBodyBoilerplate(body),
    substantive: intent.isSubstantiveBody(body),
  });
}
bodies.push({ body: '', stripped: '', substantive: false });

// ---- clamps ------------------------------------------------------------------
const clampText: object[] = [];
for (let i = 0; i < 150; i++) {
  const text = rnd() < 0.3 ? word(8) : (word(3) + ' ').repeat(int(120));
  clampText.push({ text, intent: intent.clampIntentText(text) });
}
const clampLists: object[] = [];
for (let i = 0; i < 120; i++) {
  const items = Array.from({ length: int(16) }, () => (rnd() < 0.25 ? pick(['', ' ', '\n', ' ']) : rnd() < 0.2 ? 'y'.repeat(200 + int(100)) : word(6)));
  clampLists.push({ items, list: intent.clampScopeList(items) });
}

// ---- sources block, labels, messages -----------------------------------------
const kinds = ['title', 'body', 'ticket', 'doc', 'commits', 'branch', 'diff'];
const refsPool = ['title', 'body', '12', '#7', 'docs/plans/a.md', 'a b/ć.md', 'Docs/X_y.MD', 'commits', 'branch', 'changed-files', 'ſ.md', 'K.md', '', '😀.md', 'a--b', '-x-'];
const contents = [
  'Plain text', '', ' ', '\n\t', 'has </untrusted> inside', '<untrusted source="x">', '< /UNTRUSTED >', '<untrusted foo>', 'a < b > c', 'é😀漢', ' ', 'x\ny\nz',
  '<\\/untrusted>', '<untrustedx>', '<untrusted >',
];
const blocks: object[] = [];
for (let i = 0; i < 150; i++) {
  const sources = Array.from({ length: int(7) }, () => ({
    kind: pick(kinds), ref: pick(refsPool), status: pick(['used', 'truncated', 'skipped', 'failed']), content: rnd() < 0.2 ? undefined : pick(contents),
  }));
  blocks.push({
    sources: sources.map((s) => ({ ...s, content: s.content ?? null })),
    block: intent.renderSourcesBlock(sources),
    message: classification.classificationUserMessage(intent.renderSourcesBlock(sources)),
  });
}
// the sub-kinds `default` heading cannot occur: the kind set is closed.

// ---- schema ------------------------------------------------------------------
const schema = rc.toJsonSchema(classification.IntentClassification, 'IntentClassification');
writeFileSync(join(out, 'intent_schema.node.json'), JSON.stringify(schema.schema) + '\n');

const cases = {
  note: 'Reference values computed by Node (dev-digest d1c9093 intent/domain: links.ts, intent.ts, classification.ts). tickets/docs: extractTicketRefs / extractDocRefs (ref = String(number), docs may carry the thrown error message). keys: buildCacheKeyInput + canonicalJson + sha256 (ticket_refs are decimal digit strings, Number()-ed in Node). bodies: stripBodyBoilerplate, isSubstantiveBody. clamp*: clampIntentText, clampScopeList. blocks: renderSourcesBlock and classificationUserMessage.',
  system_prompt: classification.CLASSIFICATION_SYSTEM_PROMPT,
  task_text: classification.classificationTaskText(),
  tickets,
  docs,
  keys,
  bodies,
  clampText,
  clampLists,
  blocks,
};
writeFileSync(join(out, 'node_cases.json'), JSON.stringify(cases, null, 1) + '\n');
requireClean();
