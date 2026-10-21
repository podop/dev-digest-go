/** Pure helpers of the agent Stats tab charts. */

/** `runs` as a whole percent of `total` (0 when there is nothing to divide by). */
export function usageShare(runs: number, total: number): number {
  if (!(total > 0)) return 0;
  return Math.min(100, Math.round((runs / total) * 100));
}

/** A chart slice start in the browser's locale: time of day for hourly periods, else month + day. */
export function formatSliceStart(iso: string, hourly: boolean): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return hourly
    ? d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/** Periods up to two days are charted by time of day (mirrors the server's hourly trend). */
export function isHourlyPeriod(from: string, to: string): boolean {
  return Date.parse(to) - Date.parse(from) <= 48 * 60 * 60 * 1000;
}

/** A run's start as a compact local date-time ("06/01/2026, 09:14" in en-US). */
export function formatRunTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(undefined, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

/** Token count, compact: 940 → "940", 16 300 → "16k", 403 329 → "403k". */
export function formatTokenCount(n: number): string {
  return n < 1000 ? String(n) : `${Math.round(n / 1000)}k`;
}
