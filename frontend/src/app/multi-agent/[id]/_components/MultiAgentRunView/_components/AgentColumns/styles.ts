import type { CSSProperties } from "react";

/** Most columns side by side; more agents wrap to the next row (prototype ColumnsView). */
const MAX_COLUMNS = 5;

/** Numbers from Claude Design screen_multiagent.jsx (ColumnsView, AgentColHeader). */
export const s = {
  scroller: { padding: "20px 28px 0", overflowX: "auto" } satisfies CSSProperties,
  grid: (n: number): CSSProperties => ({
    display: "grid",
    gridTemplateColumns: `repeat(${Math.max(1, Math.min(n, MAX_COLUMNS))}, minmax(220px, 1fr))`,
    gap: 12,
  }),
  column: {
    border: "1px solid var(--border)",
    borderRadius: 9,
    background: "var(--bg-elevated)",
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
    minWidth: 0,
  } satisfies CSSProperties,
  head: (color: string): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    gap: 9,
    padding: 12,
    borderBottom: "1px solid var(--border)",
    borderTop: `2px solid ${color}`,
  }),
  headText: { minWidth: 0, flex: 1 } satisfies CSSProperties,
  name: {
    margin: 0,
    fontSize: 12.5,
    fontWeight: 600,
    color: "var(--text-primary)",
    whiteSpace: "nowrap",
    overflow: "hidden",
    textOverflow: "ellipsis",
  } satisfies CSSProperties,
  meta: { display: "block", fontSize: 10.5, color: "var(--text-muted)" } satisfies CSSProperties,
  body: { padding: 12, display: "flex", flexDirection: "column", gap: 7, flex: 1 } satisfies CSSProperties,
  empty: { margin: 0, fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  error: {
    margin: 0,
    padding: "8px 10px",
    fontSize: 12,
    lineHeight: 1.4,
    color: "var(--crit)",
    background: "var(--crit-bg)",
    borderRadius: 6,
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 7 } satisfies CSSProperties,
  footer: {
    padding: "9px 12px",
    borderTop: "1px solid var(--border)",
    background: "var(--bg-surface)",
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
  } satisfies CSSProperties,
  count: { fontSize: 11, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
