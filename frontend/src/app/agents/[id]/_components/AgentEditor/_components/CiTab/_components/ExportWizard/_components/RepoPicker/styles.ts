import type { CSSProperties } from "react";

/** Co-located styles of the repository picker. */
export const s = {
  group: { margin: 0, padding: 0, border: "none", minWidth: 0 } satisfies CSSProperties,
  legend: { padding: 0, marginBottom: 4, fontSize: 13, fontWeight: 600 } satisfies CSSProperties,
  hint: { margin: "0 0 8px", fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  list: {
    display: "flex",
    flexDirection: "column",
    gap: 6,
    maxHeight: 240,
    overflowY: "auto",
  } satisfies CSSProperties,
  row: { display: "flex", flexDirection: "column", gap: 2 } satisfies CSSProperties,
  label: { display: "flex", alignItems: "center", gap: 8, fontSize: 13.5, cursor: "pointer" } satisfies CSSProperties,
  labelOff: { display: "flex", alignItems: "center", gap: 8, fontSize: 13.5, cursor: "not-allowed", opacity: 0.7 } satisfies CSSProperties,
  reason: { marginLeft: 24, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
