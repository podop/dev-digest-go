import type { CSSProperties } from "react";

export const s = {
  scroll: { overflowX: "auto", border: "1px solid var(--border)", borderRadius: 10, background: "var(--bg-elevated)" } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 13 } satisfies CSSProperties,
  th: {
    padding: "10px 14px",
    textAlign: "left",
    fontSize: 10.5,
    fontWeight: 700,
    textTransform: "uppercase",
    background: "var(--bg-surface)",
    letterSpacing: "0.04em",
    color: "var(--text-muted)",
    borderBottom: "1px solid var(--border)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  sortButton: {
    all: "unset",
    cursor: "pointer",
    display: "inline-flex",
    alignItems: "center",
    gap: 4,
    font: "inherit",
    letterSpacing: "inherit",
    textTransform: "inherit",
  } satisfies CSSProperties,
  arrow: { width: 10, display: "inline-block" } satisfies CSSProperties,
} as const;
