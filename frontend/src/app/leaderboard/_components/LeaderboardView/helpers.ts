import { LEADERBOARD_PERIODS, type LeaderboardPeriod, type LeaderboardPeriodKey } from "@devdigest/shared";
import { ALL_REPOS, DEFAULT_PERIOD, REPO_ID_PATTERN } from "./constants";

type RawParam = string | string[] | undefined;

const first = (raw: RawParam): string | undefined => (Array.isArray(raw) ? raw[0] : raw);

/** Normalize the raw ?period=; an unknown value → "week" (EC13). */
export function resolvePeriod(raw: RawParam): LeaderboardPeriodKey {
  return LEADERBOARD_PERIODS.find((p) => p === first(raw)) ?? DEFAULT_PERIOD;
}

/** Normalize the raw ?repo= to "all" or a UUID-shaped id; anything else → "all" (EC13). Whether the id exists is checked against the repo list (`resolveRepoFilter`). */
export function resolveRepoParam(raw: RawParam): string {
  const value = first(raw);
  return value !== undefined && REPO_ID_PATTERN.test(value) ? value : ALL_REPOS;
}

/**
 * The repo filter to request: "all" stays; an id that is in `repos` stays; an id that is not
 * (removed repository, EC14) → "all". `null` while `repos` is not known yet and the URL names a
 * repository — the caller holds the request back so no request carries an unchecked id.
 */
export function resolveRepoFilter(repo: string, repos: readonly { id: string }[] | undefined): string | null {
  if (repo === ALL_REPOS) return ALL_REPOS;
  if (repos === undefined) return null;
  return repos.some((r) => r.id === repo) ? repo : ALL_REPOS;
}

/** Leaderboard URL for a period and a repo filter. */
export function leaderboardHref(period: LeaderboardPeriodKey, repo: string): string {
  return `/leaderboard?period=${period}&repo=${encodeURIComponent(repo)}`;
}

const YMD = /^(\d{4})-(\d{2})-(\d{2})/;
const MS_PER_DAY = 86_400_000;
const monthFormat = new Intl.DateTimeFormat("en", { month: "short", timeZone: "UTC" });

/** UTC instant of the calendar date at the start of a local ISO time ("2026-10-19T00:00:00.000+03:00" → 2026-10-19); null when it does not parse. */
function calendarDay(iso: string): number | null {
  const m = YMD.exec(iso);
  if (!m) return null;
  const at = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return Number.isNaN(at) ? null : at;
}

/** "Sep 21 – 27", "Sep 28 – Oct 4"; the years appear only when the range crosses a year. */
function formatDays(start: number, endInclusive: number): string {
  const a = new Date(start);
  const b = new Date(endInclusive);
  const sameYear = a.getUTCFullYear() === b.getUTCFullYear();
  const day = (d: Date) => `${monthFormat.format(d)} ${d.getUTCDate()}${sameYear ? "" : `, ${d.getUTCFullYear()}`}`;
  if (sameYear && a.getUTCMonth() === b.getUTCMonth()) return `${monthFormat.format(a)} ${a.getUTCDate()} – ${b.getUTCDate()}`;
  return `${day(a)} – ${day(b)}`;
}

/** Last day shown for an exclusive end: the day before it. */
const lastDay = (exclusiveEnd: number): number => exclusiveEnd - MS_PER_DAY;

/**
 * The date ranges of the header label (FR18) for a resolved window: `range` is the current window,
 * `previous` the one it is compared with. Dates are the calendar days of the response's local
 * times (`to` is exclusive), so the label does not depend on the browser's zone. Null when a time
 * does not parse.
 */
export function windowRanges(period: LeaderboardPeriod): { range: string; previous: string } | null {
  const from = calendarDay(period.from);
  const to = calendarDay(period.to);
  const prevFrom = calendarDay(period.previous_from);
  const prevTo = calendarDay(period.previous_to);
  if (from === null || to === null || prevFrom === null || prevTo === null) return null;
  return { range: formatDays(from, lastDay(to)), previous: formatDays(prevFrom, lastDay(prevTo)) };
}

const RELATIVE_UNITS: readonly (readonly [Intl.RelativeTimeFormatUnit, number])[] = [
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
];
const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** "5 minutes ago" / "now" for the last-polled time (FR23); null when it is missing or not a date ("never polled"). English only. */
export function syncedAgo(iso: string | null, now: number = Date.now()): string | null {
  if (!iso) return null;
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return null;
  const seconds = Math.max(0, Math.floor((now - then) / 1000));
  for (const [unit, size] of RELATIVE_UNITS) {
    if (seconds >= size) return rtf.format(-Math.floor(seconds / size), unit);
  }
  return rtf.format(0, "second");
}
