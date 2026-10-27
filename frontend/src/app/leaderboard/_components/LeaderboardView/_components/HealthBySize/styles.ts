import type { CSSProperties } from "react";

export const s = {
  card: { display: "flex", flexDirection: "column", gap: 8, padding: "10px 12px" } satisfies CSSProperties,
  header: { display: "flex", alignItems: "baseline", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  title: { margin: 0, fontSize: 12.5, fontWeight: 650, color: "var(--text-primary)" } satisfies CSSProperties,
  subtitle: { fontSize: 11, color: "var(--text-muted)" } satisfies CSSProperties,
  empty: { display: "flex", flexDirection: "column", gap: 2, fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  emptyTitle: { color: "var(--text-secondary)", fontWeight: 600 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 4 } satisfies CSSProperties,
  row: { display: "grid", gridTemplateColumns: "24px 1fr 28px 140px", alignItems: "center", gap: 10 } satisfies CSSProperties,
  size: { fontSize: 11, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  track: { height: 8, background: "var(--bg-hover)", borderRadius: 3, overflow: "hidden" } satisfies CSSProperties,
  fill: { height: "100%", borderRadius: 3 } satisfies CSSProperties,
  health: { fontSize: 11.5, fontWeight: 700, color: "var(--text-primary)", textAlign: "right" } satisfies CSSProperties,
  summary: { fontSize: 10.5, color: "var(--text-secondary)", whiteSpace: "nowrap" } satisfies CSSProperties,
  none: { fontSize: 10.5, color: "var(--text-muted)", textAlign: "right" } satisfies CSSProperties,
} as const;
