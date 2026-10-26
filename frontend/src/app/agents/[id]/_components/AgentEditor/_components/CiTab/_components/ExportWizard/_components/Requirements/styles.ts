import type { CSSProperties } from "react";

/** Co-located styles of the secrets and variables list. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 12, minWidth: 0 } satisfies CSSProperties,
  group: { margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8, minWidth: 0 } satisfies CSSProperties,
  title: { margin: 0, fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  item: { display: "flex", alignItems: "baseline", flexWrap: "wrap", gap: 12, fontSize: 13 } satisfies CSSProperties,
  key: { fontWeight: 600 } satisfies CSSProperties,
  value: {
    padding: "2px 8px",
    borderRadius: 6,
    background: "var(--bg-hover)",
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
  muted: { margin: 0, fontSize: 13, color: "var(--text-muted)", lineHeight: 1.5 } satisfies CSSProperties,
  copied: { fontSize: 12.5, color: "var(--ok)" } satisfies CSSProperties,
  warning: {
    margin: 0,
    padding: "10px 14px",
    borderRadius: 8,
    border: "1px solid var(--warn)",
    background: "var(--warn-bg)",
    fontSize: 13,
    color: "var(--text-primary)",
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
} as const;
