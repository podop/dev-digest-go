import type { ResultsView } from "./_components/MultiAgentRunView";

type SearchValue = string | string[] | undefined;

const first = (v: SearchValue): string | undefined => (Array.isArray(v) ? v[0] : v);

/** `?view=tabs` → "tabs"; anything else (absent, unknown) → "columns". */
export function resolveView(raw: SearchValue): ResultsView {
  return first(raw) === "tabs" ? "tabs" : "columns";
}

/** `?trace=<runId>` → the id, or null when absent/blank. */
export function resolveTraceRunId(raw: SearchValue): string | null {
  const v = first(raw)?.trim();
  return v ? v : null;
}
