import type { Writer } from './lib.mts';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Mod = any;

/** Everything a generator part needs: paths, the loaded Node modules and the golden writer. */
export interface Ctx {
  dd: string;
  corpus: string;
  testdata: string;
  N: Record<string, Mod>;
  fastify: Mod;
  providerZod: Mod;
  ddRequire: NodeRequire;
  rcRequire: NodeRequire;
  writer: Writer;
}
