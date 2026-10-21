import type { CSSProperties } from "react";

/** Co-located styles for the eval metric tiles. */
export const s = {
  row: { display: "flex", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  tile: {
    flex: "1 1 160px",
    minWidth: 150,
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 9,
    padding: 16,
  } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 } satisfies CSSProperties,
  label: { fontSize: 12, fontWeight: 600, color: "var(--text-muted)", letterSpacing: "0.03em", textTransform: "uppercase" } satisfies CSSProperties,
  body: { display: "flex", alignItems: "baseline", gap: 10, marginTop: 10, flexWrap: "wrap" } satisfies CSSProperties,
  value: { fontSize: 28, fontWeight: 700, letterSpacing: "-0.02em" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  delta: { fontSize: 13, fontWeight: 600 } satisfies CSSProperties,
  up: { color: "var(--ok)" } satisfies CSSProperties,
  down: { color: "var(--crit)" } satisfies CSSProperties,
  flat: { color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
