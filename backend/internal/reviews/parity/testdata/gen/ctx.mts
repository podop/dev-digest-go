import type { Writer } from './lib.mts';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Mod = any;

/** Everything a generator part needs: paths, the loaded Node modules and the golden writer. */
export interface Ctx {
  dd: string;
  corpus: string;
  testdata: string;
  N: Record<string, Mod>;
  drizzle: Mod;
  fastify: Mod;
  sseV2: Mod;
  providerZod: Mod;
  rcRequire: NodeRequire;
  writer: Writer;
}
