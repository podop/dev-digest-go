type SearchValue = string | string[] | undefined;

const first = (v: SearchValue): string | undefined => (Array.isArray(v) ? v[0] : v);

/** `?pr=<prId>` → the id, or null when absent/blank. */
export function resolvePrId(raw: SearchValue): string | null {
  const v = first(raw)?.trim();
  return v ? v : null;
}

/** `?agents=<id>,<id>` → distinct ids; null when absent (the view then preselects the enabled agents). */
export function resolveAgentIds(raw: SearchValue): string[] | null {
  const v = first(raw);
  if (!v) return null;
  const ids = [...new Set(v.split(",").map((x) => x.trim()).filter(Boolean))];
  return ids.length > 0 ? ids : null;
}
