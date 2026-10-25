import type { CSSProperties } from "react";

/** Co-located styles for WorkspaceStats. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  grid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))",
    gap: 12,
    margin: 0,
  } satisfies CSSProperties,
  tile: {
    display: "flex",
    flexDirection: "column",
    gap: 4,
    padding: "12px 16px",
    border: "1px solid var(--border)",
    borderRadius: 8,
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  label: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  value: {
    margin: 0,
    minHeight: 28,
    fontSize: 22,
    fontWeight: 600,
    color: "var(--text-primary)",
    display: "flex",
    alignItems: "center",
  } satisfies CSSProperties,
  error: { display: "flex", alignItems: "center", gap: 12, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
