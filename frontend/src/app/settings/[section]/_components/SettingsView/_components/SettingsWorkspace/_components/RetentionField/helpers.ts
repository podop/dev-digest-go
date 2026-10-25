import { RETENTION_DAYS_OPTIONS, RETENTION_DEFAULT_DAYS } from "@devdigest/shared/constants/workspace";

/** Select value for "Keep forever" (`retention_days: null`). */
export const FOREVER = "forever";
/** Select value when the stored setting is not an allowed one: matches no option. */
export const NO_CHOICE = "";

/** The days that can be chosen, as the select's string values. */
export const DAY_VALUES: readonly string[] = RETENTION_DAYS_OPTIONS.map(String);

/**
 * Select value for a stored `retention_days`: absent → the 90-day default,
 * `null` → keep forever, one of the options → itself, anything else → no choice.
 * The raw value is read as `unknown` because the API client does not validate the
 * payload, so the strict contract type would hide a hand-edited 45.
 */
export function toSelectValue(stored: unknown): string {
  if (stored === undefined) return String(RETENTION_DEFAULT_DAYS);
  if (stored === null) return FOREVER;
  if (typeof stored === "number" && DAY_VALUES.includes(String(stored))) return String(stored);
  return NO_CHOICE;
}

/** A `retention_days` value the API accepts. */
export type RetentionChoice = (typeof RETENTION_DAYS_OPTIONS)[number] | null;

/** The `retention_days` to save for a select value; undefined when it is not a choosable one. */
export function toRetentionDays(value: string): RetentionChoice | undefined {
  if (value === FOREVER) return null;
  return RETENTION_DAYS_OPTIONS.find((days) => String(days) === value);
}
