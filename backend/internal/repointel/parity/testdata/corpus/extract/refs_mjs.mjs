import { load } from './load.mjs';

export async function boot() {
  const mod = await import('./lazy.mjs');
  mod.default();
  return load();
}

boot();
