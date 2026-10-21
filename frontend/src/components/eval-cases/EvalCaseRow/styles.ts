import type { CSSProperties } from "react";

/** Co-located styles for one eval case row. */
export const s = {
  row: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "10px 14px",
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 8,
    listStyle: "none",
  } satisfies CSSProperties,
  status: { display: "inline-flex", alignItems: "center", gap: 6, fontSize: 12, fontWeight: 600, minWidth: 92 } satisfies CSSProperties,
  main: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 2 } satisfies CSSProperties,
  name: {
    background: "transparent",
    border: "none",
    padding: 0,
    textAlign: "left",
    fontSize: 14,
    fontWeight: 600,
    color: "var(--text-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    cursor: "pointer",
  } satisfies CSSProperties,
  expectation: { fontSize: 12.5, color: "var(--text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  actions: { display: "flex", gap: 2 } satisfies CSSProperties,
} as const;
