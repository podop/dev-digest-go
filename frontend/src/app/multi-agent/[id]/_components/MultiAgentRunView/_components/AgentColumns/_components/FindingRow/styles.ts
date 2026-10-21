import type { CSSProperties } from "react";

export const s = {
  row: {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-start",
    gap: 4,
    padding: "8px 10px",
    background: "var(--bg-base)",
    border: "1px solid var(--border)",
    borderRadius: 6,
  } satisfies CSSProperties,
  title: { fontSize: 13.5, fontWeight: 500, color: "var(--text-primary)", overflowWrap: "anywhere" } satisfies CSSProperties,
  location: { fontSize: 12, color: "var(--text-muted)", overflowWrap: "anywhere" } satisfies CSSProperties,
} as const;
