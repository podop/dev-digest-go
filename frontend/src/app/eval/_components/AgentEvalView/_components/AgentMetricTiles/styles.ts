import type { CSSProperties } from "react";

/** Co-located styles for the agent view's three metric tiles. */
export const s = {
  row: { display: "flex", gap: 16, flexWrap: "wrap" } satisfies CSSProperties,
  tile: {
    flex: "1 1 200px",
    minWidth: 180,
    background: "var(--bg-surface)",
    border: "1px solid var(--border)",
    borderRadius: 10,
    padding: "16px 18px 18px",
  } satisfies CSSProperties,
  head: { display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 8, minHeight: 20 } satisfies CSSProperties,
  label: { fontSize: 12, fontWeight: 600, color: "var(--text-muted)", letterSpacing: "0.04em" } satisfies CSSProperties,
  body: { display: "flex", alignItems: "baseline", gap: 10, marginTop: 14, flexWrap: "wrap" } satisfies CSSProperties,
  value: { fontSize: 34, fontWeight: 700, letterSpacing: "-0.02em", lineHeight: 1 } satisfies CSSProperties,
  unit: { fontSize: 17, fontWeight: 500, color: "var(--text-muted)", marginLeft: 2 } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  delta: { fontSize: 13, fontWeight: 600 } satisfies CSSProperties,
  up: { color: "var(--ok)" } satisfies CSSProperties,
  down: { color: "var(--crit)" } satisfies CSSProperties,
  flat: { color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
