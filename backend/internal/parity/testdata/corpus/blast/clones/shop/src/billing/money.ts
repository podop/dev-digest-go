export type Money = number;

export function round(m: Money): Money {
  return Math.round(m * 100) / 100;
}
