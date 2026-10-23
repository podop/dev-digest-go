import type { CSSProperties } from "react";

export const s = {
  empty: { margin: 0, fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  item: {
    display: "grid",
    gridTemplateColumns: "16px minmax(0, 1fr) auto auto",
    gap: 10,
    alignItems: "center",
    padding: "8px 10px",
    borderRadius: 7,
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
  } satisfies CSSProperties,
  main: { minWidth: 0 } satisfies CSSProperties,
  title: { fontSize: 12.5, fontWeight: 550, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  location: { fontSize: 11, color: "var(--text-muted)", marginTop: 2, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  agent: { fontSize: 11.5, color: "var(--text-secondary)", whiteSpace: "nowrap" } satisfies CSSProperties,
} as const;
