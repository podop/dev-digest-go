/* Select helpers — pure functions only (no React, no DOM). See Select.tsx /
   useSelectListbox.ts for how these are wired into the component. */

/** Same option shape SelectInput/SearchableSelect accept: a plain value or a {value,label} pair.
 *  `V` narrows the values to a string union (e.g. Provider), so onChange needs no cast. */
export type SelectOption<V extends string = string> = V | { value: V; label: string };

export interface NormalizedOption<V extends string = string> {
  value: V;
  label: string;
}

/** A key that moves the active option without wrapping. */
export type MoveKey = "ArrowDown" | "ArrowUp" | "Home" | "End";

/** `{value,label}[]`, expanding bare strings to `{value: s, label: s}`. */
export function normalizeOptions<V extends string>(options: readonly SelectOption<V>[]): NormalizedOption<V>[] {
  return options.map((o) => (typeof o === "string" ? { value: o, label: o } : o));
}

/** Next active index for `key`. Arrows clamp at the ends (no wrap); Home/End jump to an end.
 *  Returns -1 when `count` is 0 (nothing to activate). */
export function moveActive(key: MoveKey, index: number, count: number): number {
  if (count <= 0) return -1;
  switch (key) {
    case "ArrowDown":
      return Math.min(index + 1, count - 1);
    case "ArrowUp":
      return Math.max(index - 1, 0);
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return index;
  }
}

/** Index of the first label starting with `buffer` (case-insensitive), searching forward
 *  from `from + 1` and wrapping around the whole list. -1 when `buffer` is empty, the list
 *  is empty, or nothing matches. */
export function typeaheadIndex(labels: readonly string[], buffer: string, from: number): number {
  const needle = buffer.trim().toLowerCase();
  if (!needle || labels.length === 0) return -1;
  const count = labels.length;
  for (let step = 1; step <= count; step++) {
    const index = (((from + step) % count) + count) % count;
    if (labels[index]!.toLowerCase().startsWith(needle)) return index;
  }
  return -1;
}

export interface TriggerRect {
  left: number;
  width: number;
  top: number;
  bottom: number;
}

export interface PopupPosition {
  left: number;
  width: number;
  top?: number;
  bottom?: number;
  maxHeight: number;
}

/** Where to render the popup relative to the trigger `rect`: below by default, flipped
 *  above when there isn't enough room below but there is above. `maxHeight` is clamped
 *  to the smaller of `max` and the room available on the chosen side. */
export function placePopup(
  rect: TriggerRect,
  viewportH: number,
  listH: number,
  gap = 6,
  max = 280,
): PopupPosition {
  const spaceBelow = Math.max(viewportH - rect.bottom - gap, 0);
  const spaceAbove = Math.max(rect.top - gap, 0);
  const fitsBelow = spaceBelow >= Math.min(listH, max);
  if (fitsBelow || spaceBelow >= spaceAbove) {
    return { left: rect.left, width: rect.width, top: rect.bottom + gap, maxHeight: Math.min(max, spaceBelow) };
  }
  return { left: rect.left, width: rect.width, bottom: viewportH - rect.top + gap, maxHeight: Math.min(max, spaceAbove) };
}
