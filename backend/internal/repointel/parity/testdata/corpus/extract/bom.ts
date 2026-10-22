export function afterBom(a: number): number {
  return a;
}

export const bomArrow = () => afterBom(1);
