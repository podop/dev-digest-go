import type { CSSProperties } from "react";

/** Co-located styles for TriggerConditions. */
export const s = {
  // A <fieldset> only to disable the vendored Checkboxes: reset its default border, padding and min-width.
  group: { display: "flex", flexDirection: "column", gap: 10, margin: 0, padding: 0, border: 0, minWidth: 0 } satisfies CSSProperties,
} as const;
