// Generates jstext_cases.json: reference values for jstext.go computed by Node's own string,
// number, JSON and Intl functions (dev-digest d1c9093 uses these in intent/domain/intent.ts,
// links.ts, intent-service.ts). Run: node jstext_gen.mjs > jstext_cases.json
// Inputs never contain lone surrogates (JSON cannot carry them into a Go string).
let seed = 20261022;
const rnd = () => ((seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0) / 2 ** 32);
const pick = (a) => a[Math.floor(rnd() * a.length)];
const int = (n) => Math.floor(rnd() * n);

const alphabet = [
  'a', 'b', 'Z', 'é', 'ß', 'ſ', 'K', ' ', '\t', '\n', '\r', ' ', '﻿', '\u0085', ' ', ' ',
  '　', '​', ' ', ' ', '😀', '𝒳', '😀', '漢', '字', '́', '', '￿', '-', '_', '.', '/',
  '1', '9', '%', '"', '\\', '<', '>', '&', '\u0000', '\u001f', '\u007f',
];
const word = (max) => Array.from({ length: int(max) }, () => pick(alphabet)).join('');

const cut = [];
const trunc = (text, max) => {
  const t = text.trim();
  if (t.length <= max) return { text: t, truncated: false };
  return { text: `${t.slice(0, max - 1)}…`, truncated: true };
};
for (let i = 0; i < 600; i++) {
  const text = word(24);
  const max = int(14) - (i % 7 === 0 ? 0 : 0);
  const r = trunc(text, max);
  cut.push({ text, max, want: r.text, truncated: r.truncated });
}

const order = [];
for (let i = 0; i < 400; i++) {
  const a = word(5), b = rnd() < 0.3 ? a + word(3) : word(5);
  order.push({ a, b, units: a < b ? -1 : a > b ? 1 : 0, en: Math.sign(a.localeCompare(b, 'en')) });
}
const sorts = [];
for (let i = 0; i < 60; i++) {
  const items = Array.from({ length: 2 + int(12) }, () => word(6));
  sorts.push({
    items,
    units: [...items].sort(),
    english: [...items].sort((x, y) => x.localeCompare(y)),
  });
}
const words = ['README.md', 'docs/a.md', 'Docs/B.md', 'docs/b.md', 'doc-1.md', 'doc_1.md', 'docs/Zeta.md', 'ä.md', 'z.md', 'a b.md'];
sorts.push({ items: words, units: [...words].sort(), english: [...words].sort((x, y) => x.localeCompare(y)) });

const hex = '0123456789abcdefABCDEF';
const enc = (s) => Array.from(Buffer.from(s, 'utf8')).map((b) => '%' + b.toString(16).toUpperCase().padStart(2, '0')).join('');
const decode = [];
const decodeIn = (s) => {
  try { return { ok: true, want: decodeURIComponent(s) }; } catch (e) { return { ok: false, err: e.message }; }
};
for (let i = 0; i < 700; i++) {
  let s = '';
  for (let k = 0; k < 1 + int(5); k++) {
    const t = int(10);
    if (t < 3) s += enc(pick(alphabet));
    else if (t < 5) s += pick(['a', 'b', '/', '.', 'é', '😀', ' ']);
    else if (t === 5) s += '%' + pick(hex) + pick(hex);
    else if (t === 6) s += pick(['%', '%4', '%G1', '%C0%80', '%ED%A0%80', '%F4%90%80%80', '%E0%80%AF', '%C3', '%C3%28', '%F8%88%80%80%80', '%80', '%FF', '%c3%a9', '%f0%9f%98%80', '%e4%bd%a0']);
    else s += enc(pick(['é', '漢', '😀', '', '￿', '\u0085']));
  }
  decode.push({ in: s, ...decodeIn(s) });
}

const numbers = [];
const interesting = [0, -0, 1, -1, 0.5, 1.5, 2.5, -2.5, 0.125, 0.375, 1e21, 1e-7, 1.5e-7, 123456789012345680000, 1e20, 123e-20, 0.000001, 0.0000001,
  4294967295, 9007199254740993, 0.1 + 0.2, 1 / 3, 2 / 3, 1.005, 8.345, 1.45, 0.00005, 0.00015, 0.00025, 0.00035, 0.00045, 5e-324, 1.7976931348623157e308,
  -1.7976931348623157e308, 99.99995, 0.12345, 12.34565, 1e21 + 1e6, -1e21, 0.3, 0.7, 0.15, 0.25, 1234.5678, 0.0001];
const gen = [...interesting];
for (let i = 0; i < 600; i++) {
  const t = int(5);
  const v = t === 0 ? rnd() : t === 1 ? (rnd() - 0.5) * 10 ** int(25) : t === 2 ? int(100000) / 10 ** int(8) : t === 3 ? rnd() * 10 ** (int(40) - 20) : int(2000) / 8;
  gen.push(v);
}
for (const v of gen) {
  const d = {};
  for (const n of [0, 1, 2, 4, 6]) d[n] = v.toFixed(n);
  numbers.push({ value: v, string: String(v), json: JSON.stringify(v), fixed: d });
}

const keys = ['b', 'a', 'A', 'é', 'z', '10', '2', '1', '01', '4294967294', '4294967295', 'a b', ' ', '😀', '', '"', '</script>', 'x\ny', '', '__proto__x'];
const value = (depth) => {
  const t = int(depth > 2 ? 5 : 8);
  if (t === 0) return null;
  if (t === 1) return rnd() < 0.5;
  if (t === 2) return pick([0, 1, -3, 12.5, 1e21, 1e-7, 2 ** 53, 0.1, 120000]);
  if (t <= 4) return word(8) + (rnd() < 0.3 ? ' <>& ' : '');
  if (t === 5) return Array.from({ length: int(4) }, () => value(depth + 1));
  const o = {};
  for (let k = 0; k < int(6); k++) o[pick(keys)] = value(depth + 1);
  return o;
};
const canonical = [];
const sortKeysDeep = (v) => {
  if (Array.isArray(v)) return v.map(sortKeysDeep);
  if (v && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v).sort()) out[k] = sortKeysDeep(v[k]);
    return out;
  }
  return v;
};
for (let i = 0; i < 300; i++) {
  const v = Object.fromEntries(Array.from({ length: 1 + int(5) }, () => [pick(keys), value(1)]));
  canonical.push({ input: JSON.stringify(v), want: JSON.stringify(sortKeysDeep(v)) });
}
// the intent key shape (intent/domain/intent.ts:165-177)
canonical.push({
  input: JSON.stringify({ title: 'Fix <b>&</b> x', body: '', branch: 'feat/é', head_sha: 'abc', model: 'm', provider: 'p', prompt_version: 2, ticket_refs: [3, 12], doc_paths: ['a.md', 'B.md'] }),
  want: JSON.stringify(sortKeysDeep({ title: 'Fix <b>&</b> x', body: '', branch: 'feat/é', head_sha: 'abc', model: 'm', provider: 'p', prompt_version: 2, ticket_refs: [3, 12], doc_paths: ['a.md', 'B.md'] })),
});

process.stdout.write(JSON.stringify({ cut, order, sorts, decode, numbers, canonical }, null, 0) + '\n');
