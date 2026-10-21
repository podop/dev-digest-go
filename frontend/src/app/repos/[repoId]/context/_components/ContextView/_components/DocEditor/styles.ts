import type { CSSProperties } from "react";

/** Co-located styles for DocEditor. */
export const s = {
  bar: { display: "flex", alignItems: "center", justifyContent: "flex-end", gap: 12, marginBottom: 12 } satisfies CSSProperties,
  status: { fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  frame: { border: "1px solid var(--border-strong)", borderRadius: 8, overflow: "hidden" } satisfies CSSProperties,
} as const;
