/** Helpers shared by the Phase 6 generator parts (evals, ci, wfrules, runner). No dev-digest code in here. */
import { showError, type Obj } from './common.mts';

/** Runs `fn` and returns `{result}` or `{error}` (Node `AppError` / `Error` shown as in the Phase 5 goldens). */
export function attempt(fn: () => unknown): Obj {
  try {
    return { result: fn() };
  } catch (e) {
    return { error: showError(e) };
  }
}

export async function attemptAsync(fn: () => Promise<unknown>): Promise<Obj> {
  try {
    return { result: await fn() };
  } catch (e) {
    return { error: showError(e) };
  }
}

/** Deep merge for corpus templates: objects merge recursively, everything else (arrays included) is replaced. */
export function merge(base: Obj, over: Obj | undefined): Obj {
  if (!over) return structuredClone(base);
  const out: Obj = structuredClone(base);
  for (const [k, v] of Object.entries(over)) {
    if (v !== null && typeof v === 'object' && !Array.isArray(v) && out[k] !== null && typeof out[k] === 'object' && !Array.isArray(out[k])) {
      out[k] = merge(out[k] as Obj, v as Obj);
    } else {
      out[k] = structuredClone(v);
    }
  }
  return out;
}

/** A Zod `safeParse` outcome as `{ok, data}` / `{ok:false, issues:[{code, path, message}]}`. */
export function showParse(r: { success: boolean; data?: unknown; error?: { issues: Obj[] } }): Obj {
  if (r.success) return { ok: true, data: r.data };
  return {
    ok: false,
    issues: r.error!.issues.map((i) => ({
      code: i.code,
      path: i.path,
      message: i.message,
      ...(i.validation !== undefined ? { validation: i.validation } : {}),
      ...(i.expected !== undefined ? { expected: i.expected } : {}),
      ...(i.received !== undefined ? { received: i.received } : {}),
      ...(i.options !== undefined ? { options: i.options } : {}),
    })),
  };
}

/** Recording logger with the `(obj, msg)` shape of the evals/ci ports. */
export function recLog(): { info(o: object, m: string): void; warn(o: object, m: string): void; error(o: object, m: string): void; lines: Obj[] } {
  const lines: Obj[] = [];
  const rec = (level: string) => (obj: object, msg: string) => void lines.push({ level, msg, obj: showObj(obj) });
  return { lines, info: rec('info'), warn: rec('warn'), error: rec('error') };
}

/** Error values inside log objects are shown by their message. */
export function showObj(v: unknown): unknown {
  if (v instanceof Error) return v.message;
  if (Array.isArray(v)) return v.map(showObj);
  if (v !== null && typeof v === 'object') return Object.fromEntries(Object.entries(v as Obj).map(([k, x]) => [k, showObj(x)]));
  return v;
}
