import type { CSSProperties } from "react";

/** Co-located styles for FirstTasks. */
export const s = {
  empty: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  grid: {
    listStyle: "none",
    margin: 0,
    padding: 0,
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
    gap: 12,
  } satisfies CSSProperties,
  card: {
    display: "flex",
    flexDirection: "column",
    gap: 6,
    padding: 14,
    minWidth: 0,
    background: "var(--bg-primary)",
    border: "1px solid var(--border)",
    borderRadius: 8,
  } satisfies CSSProperties,
  title: { fontSize: 14, fontWeight: 600, color: "var(--text-primary)", overflowWrap: "anywhere" } satisfies CSSProperties,
  path: {
    fontSize: 12,
    color: "var(--text-muted)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    minWidth: 0,
  } satisfies CSSProperties,
  badge: { marginTop: 4 } satisfies CSSProperties,
} as const;
