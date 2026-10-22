export function add(a: number, b: number): number {
  return a + b;
}

export class Calculator {
  total = 0;
  plus(n: number): Calculator {
    this.total = add(this.total, n);
    return this;
  }
}

export const double = (x: number) => x * 2;

function internal() {
  return add(1, 2);
}
