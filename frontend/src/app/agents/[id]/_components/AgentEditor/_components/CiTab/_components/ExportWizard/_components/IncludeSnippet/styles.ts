import type { CSSProperties } from "react";

/** Co-located styles of the include snippet box. */
export const s = {
  box: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
    margin: 0,
    padding: "12px 14px",
    borderRadius: 8,
    border: "1px solid var(--border)",
    background: "var(--bg-hover)",
    minWidth: 0,
  } satisfies CSSProperties,
  intro: { margin: 0, fontSize: 13, color: "var(--text-primary)", overflowWrap: "anywhere" } satisfies CSSProperties,
  code: {
    margin: 0,
    padding: "8px 10px",
    borderRadius: 6,
    background: "var(--bg-primary)",
    fontSize: 12.5,
    overflowX: "auto",
    whiteSpace: "pre",
  } satisfies CSSProperties,
  row: { display: "flex", alignItems: "center", gap: 12 } satisfies CSSProperties,
  copied: { fontSize: 12.5, color: "var(--ok)" } satisfies CSSProperties,
  warnings: { margin: 0, paddingLeft: 18, display: "flex", flexDirection: "column", gap: 4, fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
} as const;
