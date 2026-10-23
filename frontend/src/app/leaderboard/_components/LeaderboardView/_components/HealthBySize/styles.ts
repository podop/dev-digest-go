import type { CSSProperties } from "react";

export const s = {
  card: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  header: { display: "flex", alignItems: "baseline", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  title: { margin: 0, fontSize: 15, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  subtitle: { fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  empty: { display: "flex", flexDirection: "column", gap: 2, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  emptyTitle: { color: "var(--text-secondary)", fontWeight: 600 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 6 } satisfies CSSProperties,
  row: { display: "grid", gridTemplateColumns: "28px 1fr 32px 150px", alignItems: "center", gap: 12 } satisfies CSSProperties,
  size: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  track: { height: 10, background: "var(--bg-hover)", borderRadius: 3, overflow: "hidden" } satisfies CSSProperties,
  fill: { height: "100%", borderRadius: 3 } satisfies CSSProperties,
  health: { fontSize: 13, fontWeight: 700, color: "var(--text-primary)", textAlign: "right" } satisfies CSSProperties,
  summary: { fontSize: 12, color: "var(--text-secondary)", whiteSpace: "nowrap" } satisfies CSSProperties,
  none: { fontSize: 12, color: "var(--text-muted)", textAlign: "right" } satisfies CSSProperties,
} as const;
