const YMD = /^(\d{4})-(\d{2})-(\d{2})$/;

/** UTC date of a `YYYY-MM-DD` calendar day of the window's zone (never shifted); null when it does not parse. */
function calendarDate(date: string): Date | null {
  const m = YMD.exec(date);
  if (!m) return null;
  return new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
}

/** "Mon 28" for a `YYYY-MM-DD` calendar day; an unparsable value is shown as-is. */
export function formatDay(date: string): string {
  const d = calendarDate(date);
  if (!d) return date;
  const weekday = d.toLocaleDateString("en-US", { weekday: "short", timeZone: "UTC" });
  return `${weekday} ${d.getUTCDate()}`;
}

/** "Oct 5" for a `YYYY-MM-DD` calendar day (the start of an activity week); an unparsable value is shown as-is. */
export function formatMonthDay(date: string): string {
  const d = calendarDate(date);
  if (!d) return date;
  const month = d.toLocaleDateString("en-US", { month: "short", timeZone: "UTC" });
  return `${month} ${d.getUTCDate()}`;
}
