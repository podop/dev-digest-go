import { add, Calculator } from './lib/math';

// add(1, 2) in a comment must not count
export function main(): number {
  const c = new Calculator();
  c.plus(3);
  return add(c.total, 1);
}

export function again() {
  return add(2, 2) + add(3, 3);
}

const top = add(5, 5);
