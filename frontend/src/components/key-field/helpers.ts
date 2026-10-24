import { MASK_CHAR, MASK_MAX_DOTS, MASK_TAIL } from "./constants";

function dotCount(length: number): number {
  return Math.min(MASK_MAX_DOTS, Math.max(0, length - MASK_TAIL));
}

/** FR3: `•` × min(28, max(0, length − 4)) followed by the last 4 characters. */
export function maskKey(key: string): string {
  return MASK_CHAR.repeat(dotCount(key.length)) + key.slice(-MASK_TAIL);
}

/**
 * The raw key after the user edited the masked text field.
 *
 * The field shows `maskKey(raw)`, so a browser edit event only carries the edited *display*
 * string. The edit is found as the span between the longest common prefix and suffix of the old
 * and new display strings, then replayed on the raw key. A display position inside the dots maps
 * to the same raw position; the boundary at the end of the dots and everything after it is counted
 * from the end of the raw key (so a key longer than 32 characters keeps its hidden middle when the
 * dots are edited, and select-all + paste replaces everything).
 */
export function applyMaskedEdit(raw: string, nextDisplay: string): string {
  const prev = maskKey(raw);
  if (nextDisplay === prev) return raw;

  const limit = Math.min(prev.length, nextDisplay.length);
  let head = 0;
  while (head < limit && prev[head] === nextDisplay[head]) head++;
  let tail = 0;
  while (
    tail < limit - head &&
    prev[prev.length - 1 - tail] === nextDisplay[nextDisplay.length - 1 - tail]
  ) {
    tail++;
  }

  const dots = dotCount(raw.length);
  const toRaw = (displayIndex: number) =>
    displayIndex < dots ? displayIndex : raw.length - (prev.length - displayIndex);

  return (
    raw.slice(0, toRaw(head)) +
    nextDisplay.slice(head, nextDisplay.length - tail) +
    raw.slice(toRaw(prev.length - tail))
  );
}
