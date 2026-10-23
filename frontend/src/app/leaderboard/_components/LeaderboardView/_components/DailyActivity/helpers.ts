/** "Mon 28" for a `YYYY-MM-DD` calendar day; the date is already a day in the window's tz, so it is
 *  read as UTC and never shifted. An unparsable value is shown as-is. */
export function formatDay(date: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!m) return date;
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
  const weekday = d.toLocaleDateString("en-US", { weekday: "short", timeZone: "UTC" });
  return `${weekday} ${d.getUTCDate()}`;
}
