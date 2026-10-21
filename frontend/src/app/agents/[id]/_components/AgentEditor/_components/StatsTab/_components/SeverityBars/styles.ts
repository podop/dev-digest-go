import type { CSSProperties } from "react";

export const s = {
  chart: { display: "flex", alignItems: "stretch", gap: 12, height: 140, padding: "0 4px" } satisfies CSSProperties,
  column: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 4 } satisfies CSSProperties,
  stack: { flex: 1, display: "flex", flexDirection: "column", justifyContent: "flex-end", gap: 2 } satisfies CSSProperties,
  segment: { borderRadius: 2, minHeight: 2 } satisfies CSSProperties,
  tick: { fontSize: 10, color: "var(--text-muted)", textAlign: "center", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" } satisfies CSSProperties,
  legend: { display: "flex", gap: 14, marginTop: 12, fontSize: 11.5, flexWrap: "wrap" } satisfies CSSProperties,
  legendItem: { display: "inline-flex", alignItems: "center", gap: 5, color: "var(--text-secondary)" } satisfies CSSProperties,
  swatch: { width: 9, height: 9, borderRadius: 2 } satisfies CSSProperties,
  empty: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
} as const;
