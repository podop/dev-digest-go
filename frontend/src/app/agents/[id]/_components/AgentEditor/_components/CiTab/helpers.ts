const PLACEHOLDER = "—";

const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;
const MONTH = 30 * DAY;
const YEAR = 365 * DAY;

/** True when the agent moved past the manifest deployed to a repository (unknown deployed version → false). */
export function isOutdated(agentVersion: number, deployedVersion: number | null | undefined): boolean {
  return deployedVersion != null && agentVersion > deployedVersion;
}

/** "3 hours ago" style text for an ISO time; "—" when missing or not a date. `now` is injectable for tests. */
export function formatRelativeTime(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return PLACEHOLDER;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return PLACEHOLDER;
  const diff = Math.round((then - now) / 1000);
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  if (abs < MINUTE) return rtf.format(diff, "second");
  if (abs < HOUR) return rtf.format(Math.round(diff / MINUTE), "minute");
  if (abs < DAY) return rtf.format(Math.round(diff / HOUR), "hour");
  if (abs < MONTH) return rtf.format(Math.round(diff / DAY), "day");
  if (abs < YEAR) return rtf.format(Math.round(diff / MONTH), "month");
  return rtf.format(Math.round(diff / YEAR), "year");
}
