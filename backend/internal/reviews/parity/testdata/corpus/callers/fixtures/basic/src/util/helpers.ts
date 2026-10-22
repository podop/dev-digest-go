import { add, Calculator } from '../lib/math';

export const compute = (n: number) => add(n, n);

export class Runner {
  run() {
    return new Calculator().plus(1);
  }
}

export function onlyHereCaller() {
  return onlyHere();
}
