import type { CSSProperties } from "react";

/** Co-located styles for TriggerConditions (the prototype's trigger checkboxes). */
export const s = {
  // A <fieldset> only to disable the checkboxes: reset its default border, padding and min-width.
  group: { display: "flex", flexDirection: "column", gap: 8, margin: 0, padding: 0, border: 0, minWidth: 0 } satisfies CSSProperties,
  option: {
    display: "flex",
    alignItems: "center",
    gap: 9,
    fontSize: 13,
    color: "var(--text-secondary)",
    cursor: "pointer",
  } satisfies CSSProperties,
  box: (on: boolean): CSSProperties => ({
    width: 16,
    height: 16,
    flexShrink: 0,
    borderRadius: 4,
    border: `1.5px solid ${on ? "var(--accent)" : "var(--border-strong)"}`,
    background: on ? "var(--accent)" : "transparent",
    display: "grid",
    placeItems: "center",
    position: "relative",
  }),
  input: {
    position: "absolute",
    inset: -1.5,
    margin: 0,
    opacity: 0,
    cursor: "pointer",
  } satisfies CSSProperties,
  check: { color: "#fff", pointerEvents: "none" } satisfies CSSProperties,
} as const;
