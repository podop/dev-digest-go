// Generates callers_refs.json: Node's extractReferences (read-only reference checkout
// ../dev-digest, commit d1c9093) over a seeded random corpus of lines and a few fixed files.
// Run from the repository root:
//   cd ../dev-digest/server && npx tsx <this file> <abs output path>
import { writeFileSync } from 'node:fs';
import { extractReferences } from '/home/ypodop/IdeaProjects/dev-digest/server/src/adapters/codeindex/extract.ts';

let seed = 20261022;
const rnd = () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 2 ** 32);
const pick = <T,>(a: T[]): T => a[Math.floor(rnd() * a.length)]!;

const names = ['foo', 'Bar', 'a$b', 'x', 'useFoo', 'é', 'foo.bar'];
const frags = [
  'foo(', ' foo (', 'x.foo(', 'new foo', 'new  Bar()', '<Bar />', '<Bar>', '<Bar\t', '<Bar.x', 'function foo(',
  'function* foo', 'function * foo', 'function foo', 'class Bar', 'const foo =', 'let x =', 'var  x', 'type x =',
  'enum Bar', 'interface x', ' // c', '"foo("', "'foo('", '`x(`', '"a\\"b" foo(', '"unterminated foo(', "'it\\'s' x(",
  '\\', '"', "'", '`', '/* foo( */', '* foo(', 'import ', 'export { foo } from "m"', 'export * from "m"',
  'export {', '} from', 'a$b(', '.a$b(', '$a$b(', '_x(', '9x(', 'foo2(', 'foofoo(', 'xfoo(', 'é(', 'éé(',
  '\r', ' ', '﻿', ' ', '\t', '  ', '(', ')', ';', '{', '}', '=>', 'return ', 'await ', 'foo', 'Bar', 'x',
  'useFoo(', 'foo.bar(', '.bar(', 'bar(', '//', '/', '*',
];
const lineOf = () => Array.from({ length: 1 + Math.floor(rnd() * 7) }, () => pick(frags)).join(pick(['', '', ' ']));

const cases: { content: string; symbol: string; lines: number[] }[] = [];
const add = (content: string, symbol: string) =>
  cases.push({ content, symbol, lines: extractReferences(content, symbol).map((r) => r.line) });

for (let i = 0; i < 1500; i++) {
  const lines = Array.from({ length: 1 + Math.floor(rnd() * 4) }, lineOf);
  add(lines.join('\n'), pick(names));
}
add('import { foo } from "x";\nfoo();\n  // foo()\n * foo()\n/* foo() */\nfunction foo() {}\nconst r = foo();\n', 'foo');
add('a(); // foo()\r\nb(foo());\r\n', 'foo');
add('', 'foo');
add('foo(\n', 'foo');
add('x = new Bar();\r\n<Bar />\n', 'Bar');
for (const n of names) add('foo( Bar x a$b useFoo é', n);

writeFileSync(process.argv[2]!, JSON.stringify(cases, null, 1) + '\n');
console.log(cases.length, 'cases');
