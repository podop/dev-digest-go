// Generates extract_symbols.json: Node's extractSymbols (read-only reference checkout
// ../dev-digest, commit d1c9093) over a seeded random corpus of source fragments and a few fixed
// files. Run with a short TMPDIR (tsx writes its cache there):
//   cd /home/ypodop/IdeaProjects/dev-digest/server && TMPDIR=/tmp/p5d npx tsx <this file> <abs output path>
import { writeFileSync } from 'node:fs';
import { extractSymbols } from '/home/ypodop/IdeaProjects/dev-digest/server/src/adapters/codeindex/extract.ts';

let seed = 20261022;
const rnd = () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 2 ** 32);
const pick = <T,>(a: T[]): T => a[Math.floor(rnd() * a.length)]!;

const frags = [
  'function foo(', 'export function bar<T>(', 'export default async function baz (', 'async function* gen(', 'function * g2(',
  'function (', 'function if(', 'class A', 'export class B extends C', 'export default abstract class D', 'abstract class E<T> {',
  'class get', 'const f = (a, b) =>', 'let g = async (x) : number =>', 'var h = function', 'const i = x =>', 'const j = async x=>',
  'export const k: Foo = (', 'const l = (a: (b) => void) =>', 'const m = 5', 'const n = (', 'interface I', 'export interface J {',
  'type T = ', 'export type U<V> =', 'type W', 'enum E1', 'export enum E2 {', 'const enum E3',
  'run() {', 'async fetch(a, b) {', 'static make(): Foo {', 'public readonly x(', 'private async *gen() {', 'constructor() {', 'get x() {',
  'if (a) {', 'for (;;) {', 'while (x) {', 'catch (e) {', 'return foo() {', 'method(a): Promise<void> {', 'x(a) : b = {',
  '{', '}', '{}', '{ {', '} }', '(', ')', ';', '=', '=>', ',', ' ', '  ', '\t', '\r', ' ', '﻿',
  '// c', '/* c', ' * c', '//', '"str {"', "'it\\'s }'", '`tpl { `', '"unterminated', '\\', '"', "'", '`',
  'a$b', '$x', '_y', 'é', 'x1', '9', 'foo', 'Bar', 'default', 'export', 'async',
];
const lineOf = () => Array.from({ length: 1 + Math.floor(rnd() * 6) }, () => pick(frags)).join(pick(['', ' ', ' ', '\t']));

const cases: { content: string; symbols: { name: string; kind: string; line: number }[] }[] = [];
const add = (content: string) => cases.push({ content, symbols: extractSymbols(content) });

for (let i = 0; i < 2500; i++) {
  const n = 1 + Math.floor(rnd() * 8);
  add(Array.from({ length: n }, lineOf).join(pick(['\n', '\n', '\r\n'])));
}
// class-structured files: methods, nested classes, comments with braces
for (let i = 0; i < 800; i++) {
  const lines: string[] = [];
  const cls = pick(['class A {', 'export class B {', 'class C extends D {', 'abstract class E {', 'class F {}', 'class G { run() {} }']);
  lines.push(cls);
  for (let k = 0; k < 1 + Math.floor(rnd() * 6); k++) {
    lines.push(pick(['  run() {', '  async go(a, b) {', '  static of(x): X {', '  get v() {', '  if (x) {', '    inner() {', '  }', '  // {', '  /* }', '  prop = 1;',
      '  private helper(a) : void {', '  *gen() {', '  constructor(a) {', '  m(a) { return 1; }', '  }}', '  x = () => {', '    y(); }']));
  }
  lines.push(pick(['}', '', 'function after() {}', '}\nclass H {\n  last() {}\n}']));
  add(lines.join('\n'));
}
add('export function a() {}\nexport const b = () => 1;\nexport class C {\n  m() {}\n  static n() {}\n}\ninterface I {}\ntype T = string;\nenum E {}\n');
add('');
add('\n\n');
add('function a() {}\r\nclass B {\r\n  c() {}\r\n}\r\n');
add('class A {\n  if (x) {\n    y();\n  }\n  catch(e) {\n  }\n}\n');
add('const x = 1; const f = () => {}');
add('function    spaced  (a) {}\nclass N {}\n');

writeFileSync(process.argv[2]!, '[\n' + cases.map((c) => JSON.stringify(c)).join(',\n') + '\n]\n');
console.log(cases.length, 'cases', cases.filter((c) => c.symbols.length > 0).length, 'with symbols');
