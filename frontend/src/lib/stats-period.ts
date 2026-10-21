/* stats-period.ts — the reporting period of the Agent Performance dashboard and the agent
   Stats tab (specs/2026-10-16-agent-performance.md FR11). The period lives in the URL
   (`period=1d|30d|custom`, plus `from`/`to` as local `YYYY-MM-DD` for custom); the API gets
   ISO instants built from LOCAL midnights, so the day boundaries follow the browser's time zone.
   Pure functions only — no React, no I/O. */

export const STATS_PRESETS = ["1d", "30d"] as const;
export type StatsPreset = (typeof STATS_PRESETS)[number];

export const DEFAULT_STATS_PRESET: StatsPreset = "30d";

/** Longest custom span the server accepts (`to − from`), in days. */
export const MAX_CUSTOM_SPAN_DAYS = 366;

const MS_PER_DAY = 24 * 60 * 60 * 1000;
const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

export type StatsPeriod =
  | { key: StatsPreset }
  | { key: "custom"; from: string; to: string };

/** Why a custom range cannot be applied; the picker maps each code to its message. */
export type CustomRangeError = "invalid" | "endBeforeStart" | "tooLong" | "startInFuture";

type RawParam = string | string[] | undefined;

const first = (raw: RawParam): string | undefined => (Array.isArray(raw) ? raw[0] : raw);

/** Local midnight of a `YYYY-MM-DD` date; null when the text is not a real calendar date. */
function localMidnight(date: string | undefined): Date | null {
  const m = date ? DATE_RE.exec(date) : null;
  if (!m) return null;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const at = new Date(y, mo - 1, d);
  const real = at.getFullYear() === y && at.getMonth() === mo - 1 && at.getDate() === d;
  return real ? at : null;
}

/** `YYYY-MM-DD` of a date in local time. */
export function toDateInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The instants the API receives for a custom range: [local midnight of start, local midnight after end). */
function customInstants(from: Date, endDay: Date): { from: Date; to: Date } {
  return { from, to: new Date(endDay.getFullYear(), endDay.getMonth(), endDay.getDate() + 1) };
}

/**
 * Validate a custom range of `YYYY-MM-DD` dates (end inclusive). The span is checked on the
 * local-midnight instants the API will get. "Start in the future" is only checked when `now`
 * is given — the form passes it, URL resolution does not.
 */
export function customRangeError(from: string, to: string, now?: Date): CustomRangeError | null {
  const start = localMidnight(from);
  const end = localMidnight(to);
  if (!start || !end) return "invalid";
  if (end.getTime() < start.getTime()) return "endBeforeStart";
  const range = customInstants(start, end);
  if (range.to.getTime() - range.from.getTime() > MAX_CUSTOM_SPAN_DAYS * MS_PER_DAY) return "tooLong";
  if (now && start.getTime() > now.getTime()) return "startInFuture";
  return null;
}

/** Normalize the raw `period`/`from`/`to` URL values; anything missing or invalid resolves to 30d. */
export function resolveStatsPeriod(raw: { period?: RawParam; from?: RawParam; to?: RawParam }): StatsPeriod {
  const period = first(raw.period);
  const preset = STATS_PRESETS.find((p) => p === period);
  if (preset) return { key: preset };
  if (period === "custom") {
    const from = first(raw.from);
    const to = first(raw.to);
    if (from && to && customRangeError(from, to) === null) return { key: "custom", from, to };
  }
  return { key: DEFAULT_STATS_PRESET };
}

/** URL search params of a period (no leading `?`), e.g. `period=custom&from=2026-10-01&to=2026-10-15`. */
export function periodSearch(p: StatsPeriod): string {
  const params = new URLSearchParams({ period: p.key });
  if (p.key === "custom") {
    params.set("from", p.from);
    params.set("to", p.to);
  }
  return params.toString();
}

/**
 * API query string of a period: presets send the key only; custom sends ISO instants of the
 * LOCAL midnights (`from` = start day, `to` = the day after the end day, exclusive).
 * The server clock defines "now" for presets.
 */
export function periodApiQuery(p: StatsPeriod): string {
  if (p.key !== "custom") return new URLSearchParams({ period: p.key }).toString();
  const start = localMidnight(p.from);
  const end = localMidnight(p.to);
  if (!start || !end) return new URLSearchParams({ period: DEFAULT_STATS_PRESET }).toString();
  const range = customInstants(start, end);
  return new URLSearchParams({
    period: "custom",
    from: range.from.toISOString(),
    to: range.to.toISOString(),
  }).toString();
}

/** Label of the applied period: the preset's own label, or "Oct 1 – Oct 15, 2026" for a custom range. */
export function formatPeriodRange(p: StatsPeriod, presetLabels: Record<StatsPreset, string>): string {
  if (p.key !== "custom") return presetLabels[p.key];
  const start = localMidnight(p.from);
  const end = localMidnight(p.to);
  if (!start || !end) return presetLabels[DEFAULT_STATS_PRESET];
  const withYear: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", year: "numeric" };
  const noYear: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
  const sameYear = start.getFullYear() === end.getFullYear();
  return `${start.toLocaleDateString(undefined, sameYear ? noYear : withYear)} – ${end.toLocaleDateString(undefined, withYear)}`;
}

/** The agent editor's Stats tab for a period (the dashboard's "View" link). */
export function agentStatsHref(agentId: string, p: StatsPeriod): string {
  return `/agents/${encodeURIComponent(agentId)}?tab=stats&${periodSearch(p)}`;
}
