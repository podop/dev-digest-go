import { helper } from './helper';

export function plain(a: number, b: string): string {
  return b + a;
}

function* gen(n: number) {
  yield n;
}

export function* exportedGen(): Generator<number> {
  yield 1;
}

export async function fetchAll(url: string): Promise<void> {
  await helper(url);
}

async function internalAsync() {
  return 1;
}

export const arrow = (x: number) => x * 2;

export const asyncArrow = async (x: number): Promise<number> => {
  return x;
};

const fnExpr = function (a: string) {
  return a;
};

let genExpr = function* () {
  yield 1;
};

var oldStyle = function legacy() {
  return 0;
};

const plainValue = 42;
const objectValue = { a: 1 };

export class Widget {
  name = 'w';

  constructor(private readonly size: number) {}

  render(depth: number): string {
    return String(depth);
  }

  static create(): Widget {
    return new Widget(1);
  }

  async load(): Promise<void> {}

  get label(): string {
    return this.name;
  }

  set label(v: string) {
    this.name = v;
  }

  private hidden() {}
}

class Internal {
  run() {}
}

export interface Options {
  verbose: boolean;
  depth?: number;
}

interface Local<T> extends Options {
  value: T;
}

export type Id = string | number;

type Pair<A, B> = [A, B];

export enum Color {
  Red,
  Green,
}

enum Local2 {
  A = 1,
}

declare function ambient(x: number): void;

abstract class Shape {
  abstract area(): number;
  describe() {
    return 'shape';
  }
}

namespace Space {
  export function nested() {}
}

if (plainValue) {
  function inBlock() {}
}

export default class DefaultThing {
  go() {}
}
