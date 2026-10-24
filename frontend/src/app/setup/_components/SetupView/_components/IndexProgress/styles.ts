import type { CSSProperties } from "react";

/** Co-located styles for the indexing status line. */
export const s = {
  box: {
    display: "flex",
    flexDirection: "column",
    gap: 6,
    padding: "12px 14px",
    borderRadius: 8,
    background: "var(--bg-hover)",
    fontSize: 13,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  line: { display: "flex", alignItems: "center", gap: 8 } satisfies CSSProperties,
  ok: { color: "var(--ok)" } satisfies CSSProperties,
} as const;
