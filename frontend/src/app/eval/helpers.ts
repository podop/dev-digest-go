import { EVAL_PERIODS, type EvalPeriodId } from "@devdigest/shared/constants/evals";
import { DEFAULT_EVAL_PERIOD } from "./constants";

/** Normalize a raw ?agent= value (missing, empty or repeated → the first non-empty value, else null). */
export function resolveAgentId(raw: string | string[] | undefined): string | null {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return value ? value : null;
}

/** Normalize a raw ?period= value (repeated → the first; missing or not a preset → 30d). */
export function resolvePeriod(raw: string | string[] | undefined): EvalPeriodId {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return EVAL_PERIODS.find((p) => p === value) ?? DEFAULT_EVAL_PERIOD;
}

/** Eval Dashboard URL: all agents, or one agent's view (with its period, when given). */
export function evalHref(agentId?: string | null, period?: EvalPeriodId): string {
  if (!agentId) return "/eval";
  const params = new URLSearchParams({ agent: agentId });
  if (period) params.set("period", period);
  return `/eval?${params.toString()}`;
}

/** The agent editor's Evals tab. */
export function agentEvalsHref(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}?tab=evals`;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** A run's start as local `YYYY-MM-DD HH:mm`; "—" when the timestamp is not a date. */
export function formatRunStamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
