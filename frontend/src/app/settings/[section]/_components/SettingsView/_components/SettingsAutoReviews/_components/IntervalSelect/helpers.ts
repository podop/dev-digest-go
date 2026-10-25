/** The polling intervals that can be chosen, in minutes (D9). */
export const INTERVAL_MINUTES: readonly number[] = [1, 2, 5, 10, 15, 30, 60];

/**
 * The intervals to list for a stored `polling_interval_min`: the seven choices, plus the stored
 * value in numeric order when it is not one of them (FR14). `value` is a positive integer by the
 * PUT /settings validation; anything else is not listed.
 */
export function intervalChoices(value: number): number[] {
  if (!Number.isInteger(value) || value < 1 || INTERVAL_MINUTES.includes(value)) return [...INTERVAL_MINUTES];
  return [...INTERVAL_MINUTES, value].sort((a, b) => a - b);
}

/** The minutes of a select value; undefined when it is not a positive integer. */
export function parseMinutes(value: string): number | undefined {
  const minutes = Number(value);
  return Number.isInteger(minutes) && minutes >= 1 ? minutes : undefined;
}
