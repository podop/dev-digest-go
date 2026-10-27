import type { CSSProperties } from "react";

export const s = {
  card: { display: "flex", flexDirection: "column", gap: 8, padding: "10px 12px" } satisfies CSSProperties,
  header: { display: "flex", alignItems: "baseline", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  title: { margin: 0, fontSize: 12.5, fontWeight: 650, color: "var(--text-primary)" } satisfies CSSProperties,
  subtitle: { fontSize: 11, color: "var(--text-muted)" } satisfies CSSProperties,
  empty: { display: "flex", flexDirection: "column", gap: 2, fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  emptyTitle: { color: "var(--text-secondary)", fontWeight: 600 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 2 } satisfies CSSProperties,
  row: { display: "flex", alignItems: "center", gap: 10, padding: "3px 8px", borderRadius: 6 } satisfies CSSProperties,
  rowCritical: { background: "var(--crit-bg)" } satisfies CSSProperties,
  info: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 0 } satisfies CSSProperties,
  path: {
    fontSize: 11.5,
    color: "var(--text-primary)",
    whiteSpace: "nowrap",
    overflow: "hidden",
    textOverflow: "ellipsis",
  } satisfies CSSProperties,
  location: { fontSize: 10.5, color: "var(--text-muted)", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" } satisfies CSSProperties,
  prLink: { color: "inherit", textDecoration: "underline" } satisfies CSSProperties,
  counts: { display: "flex", alignItems: "center", gap: 8, flexShrink: 0 } satisfies CSSProperties,
  findings: { fontSize: 11, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  critical: { fontSize: 11, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
} as const;
