import type { CSSProperties } from "react";

/** Co-located styles for WorkspaceStats — the prototype's count tiles (number first, label under it). */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 10, marginBottom: 22 } satisfies CSSProperties,
  grid: { display: "flex", flexWrap: "wrap", gap: 10, margin: 0 } satisfies CSSProperties,
  // column-reverse keeps <dt> before <dd> in the DOM while the number sits on top.
  tile: {
    flex: "1 1 0",
    minWidth: 88,
    display: "flex",
    flexDirection: "column-reverse",
    justifyContent: "flex-end",
    padding: "12px 14px",
    border: "1px solid var(--border)",
    borderRadius: 9,
    background: "var(--bg-elevated)",
  } satisfies CSSProperties,
  label: { margin: "2px 0 0", fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  value: {
    margin: 0,
    minHeight: 26,
    fontSize: 20,
    fontWeight: 700,
    fontVariantNumeric: "tabular-nums",
    color: "var(--text-primary)",
    display: "flex",
    alignItems: "center",
  } satisfies CSSProperties,
  error: { display: "flex", alignItems: "center", gap: 12, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
