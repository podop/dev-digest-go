import type { CSSProperties } from "react";

export const BAR_AREA = 100;

export const s = {
  card: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  header: { display: "flex", alignItems: "baseline", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  title: { margin: 0, fontSize: 15, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  subtitle: { fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  empty: { display: "flex", flexDirection: "column", gap: 2, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  emptyTitle: { color: "var(--text-secondary)", fontWeight: 600 } satisfies CSSProperties,
  scroll: { overflowX: "auto" } satisfies CSSProperties,
  chart: { display: "flex", alignItems: "stretch", gap: 10, padding: "0 4px", minWidth: "min-content" } satisfies CSSProperties,
  column: { flex: 1, minWidth: 44, display: "flex", flexDirection: "column", alignItems: "center", gap: 6 } satisfies CSSProperties,
  bars: { display: "flex", alignItems: "flex-end", justifyContent: "center", gap: 4, height: BAR_AREA + 16 } satisfies CSSProperties,
  barWrap: { display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "flex-end", gap: 2, width: 18 } satisfies CSSProperties,
  count: { fontSize: 11, lineHeight: "14px", fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  bar: { width: "100%", borderRadius: "3px 3px 0 0" } satisfies CSSProperties,
  critical: {
    minWidth: 18,
    height: 18,
    padding: "0 5px",
    boxSizing: "border-box",
    borderRadius: 99,
    display: "inline-grid",
    placeItems: "center",
    fontSize: 11,
    fontWeight: 700,
    color: "var(--crit)",
    background: "var(--crit-bg)",
    border: "1px solid var(--crit)",
  } satisfies CSSProperties,
  criticalSlot: { height: 18, display: "flex", alignItems: "center" } satisfies CSSProperties,
  tick: { fontSize: 10.5, color: "var(--text-muted)", whiteSpace: "nowrap" } satisfies CSSProperties,
  legend: { display: "flex", gap: 14, fontSize: 11.5, flexWrap: "wrap" } satisfies CSSProperties,
  legendItem: { display: "inline-flex", alignItems: "center", gap: 5, color: "var(--text-secondary)" } satisfies CSSProperties,
  swatch: { width: 9, height: 9, borderRadius: 2 } satisfies CSSProperties,
  dot: { width: 9, height: 9, borderRadius: 99, background: "var(--crit-bg)", border: "1px solid var(--crit)", boxSizing: "border-box" } satisfies CSSProperties,
} as const;
