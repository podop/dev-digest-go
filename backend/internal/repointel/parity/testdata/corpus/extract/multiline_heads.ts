export function withManyParams(
  first: string,
  second: number,
  third: boolean,
): Promise<void> {
  return Promise.resolve();
}

export const multiArrow = (
  alpha: string,
  beta: number,
): string => {
  return alpha + beta;
};

export class Spread<
  TKey extends string,
  TValue,
> extends Map<TKey, TValue> {
  lookup(
    key: TKey,
  ): TValue | undefined {
    return this.get(key);
  }
}

export interface Wide<
  A,
  B,
> {
  a: A;
  b: B;
}

export type Union =
  | 'one'
  | 'two'
  | 'three';

const spaced    =    async   (   x   :   number   )    =>    {
  return x;
};

function	tabbed	(	a	)	{
}

export const nobody = (x: number) => x + 1;
export const nobody2 = async (x: number) => await Promise.resolve(x);
