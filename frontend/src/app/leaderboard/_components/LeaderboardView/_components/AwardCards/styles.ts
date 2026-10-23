import type { CSSProperties } from "react";

export const s = {
  grid: { display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(250px, 1fr))", gap: 14 } satisfies CSSProperties,
  card: {
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 9,
    padding: 16,
    display: "flex",
    flexDirection: "column",
    gap: 8,
    minWidth: 0,
  } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 10 } satisfies CSSProperties,
  chip: { width: 28, height: 28, borderRadius: 7, display: "grid", placeItems: "center", flex: "none" } satisfies CSSProperties,
  title: { fontSize: 11, fontWeight: 600, color: "var(--text-muted)", letterSpacing: "0.03em", textTransform: "uppercase" } satisfies CSSProperties,
  metricRow: { display: "flex", alignItems: "baseline", gap: 6, flexWrap: "wrap" } satisfies CSSProperties,
  metric: { fontSize: 28, fontWeight: 700, letterSpacing: "-0.02em" } satisfies CSSProperties,
  unit: { fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  none: { fontSize: 28, fontWeight: 700, color: "var(--text-muted)" } satisfies CSSProperties,
  link: {
    display: "block",
    fontSize: 12.5,
    color: "var(--accent-text)",
    textDecoration: "none",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  ref: { fontSize: 12.5, color: "var(--text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  prTitle: { fontSize: 13, color: "var(--text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
} as const;
