import type { CSSProperties } from "react";

/** Compact award cards: six in one row, about half the prototype's sizes (user, 2026-10-27). */
export const s = {
  grid: { display: "grid", gridTemplateColumns: "repeat(6, minmax(0, 1fr))", gap: 8 } satisfies CSSProperties,
  card: {
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 8,
    padding: "8px 10px",
    display: "flex",
    flexDirection: "column",
    gap: 3,
    minWidth: 0,
  } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 6, minWidth: 0 } satisfies CSSProperties,
  chip: { width: 18, height: 18, borderRadius: 5, display: "grid", placeItems: "center", flex: "none" } satisfies CSSProperties,
  title: {
    fontSize: 9.5,
    fontWeight: 600,
    color: "var(--text-muted)",
    letterSpacing: "0.02em",
    textTransform: "uppercase",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  metricRow: { display: "flex", alignItems: "baseline", gap: 4, minWidth: 0, whiteSpace: "nowrap", overflow: "hidden" } satisfies CSSProperties,
  metric: { fontSize: 16, fontWeight: 700, letterSpacing: "-0.02em" } satisfies CSSProperties,
  unit: { fontSize: 10, color: "var(--text-muted)", overflow: "hidden", textOverflow: "ellipsis" } satisfies CSSProperties,
  none: { fontSize: 16, fontWeight: 700, color: "var(--text-muted)" } satisfies CSSProperties,
  link: {
    display: "block",
    fontSize: 10.5,
    color: "var(--accent-text)",
    textDecoration: "none",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  prTitle: { fontSize: 10.5, color: "var(--text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
} as const;
