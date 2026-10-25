import type { CSSProperties } from "react";

/** Co-located styles for step 3 (existing repositories list). */
export const s = {
  heading: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)", margin: "0 0 8px" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 6 } satisfies CSSProperties,
  row: {
    padding: "10px 12px",
    borderRadius: 8,
    border: "1px solid var(--border)",
    fontSize: 14,
    fontFamily: "var(--font-mono, monospace)",
  } satisfies CSSProperties,
} as const;
