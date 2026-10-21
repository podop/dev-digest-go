/** Pure formatting of the Agent Performance numbers (specs/2026-10-16-agent-performance.md FR13). */
import { GOOD_ACCEPT_RATE } from "./constants";

/** Whole milliseconds → seconds with one decimal ("6.2"); null stays null (shown as "—", never 0). */
export function formatDurationS(ms: number | null | undefined): string | null {
  if (ms == null || !Number.isFinite(ms)) return null;
  return (ms / 1000).toFixed(1);
}

/** A 0..1 rate → whole percent (0.756 → 75.6 → 76); null stays null. */
export function formatPercent(rate: number | null | undefined): number | null {
  if (rate == null || !Number.isFinite(rate)) return null;
  return Math.round(rate * 100);
}

/** Accept-rate colour (mockup N11): green from 60%, amber below, muted when unknown. */
export function acceptRateColor(rate: number | null | undefined): string {
  if (rate == null || !Number.isFinite(rate)) return "var(--text-muted)";
  return rate >= GOOD_ACCEPT_RATE ? "var(--ok)" : "var(--warn)";
}
