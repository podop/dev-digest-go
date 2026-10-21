import type { CSSProperties } from "react";

/** Width every column keeps; beyond the viewport the row scrolls horizontally. */
const COLUMN_WIDTH = 300;

export const s = {
  row: { display: "flex", gap: 14, overflowX: "auto", paddingBottom: 6, alignItems: "stretch" } satisfies CSSProperties,
  column: {
    flex: `1 0 ${COLUMN_WIDTH}px`,
    maxWidth: 460,
    display: "flex",
    flexDirection: "column",
    gap: 12,
    padding: 14,
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 8,
  } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 10 } satisfies CSSProperties,
  headText: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 3 } satisfies CSSProperties,
  name: { fontSize: 15, fontWeight: 600, margin: 0, overflowWrap: "anywhere" } satisfies CSSProperties,
  meta: { fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  count: { fontSize: 12.5, color: "var(--text-secondary)", margin: 0 } satisfies CSSProperties,
  error: {
    margin: 0,
    padding: "8px 10px",
    fontSize: 13,
    color: "var(--crit)",
    background: "var(--bg-base)",
    border: "1px solid var(--crit)",
    borderRadius: 6,
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8, flex: 1 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-start" } satisfies CSSProperties,
} as const;
