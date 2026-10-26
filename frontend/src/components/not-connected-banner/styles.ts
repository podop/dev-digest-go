import type { CSSProperties } from "react";

export const s = {
  root: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    padding: "10px 20px",
    borderBottom: "1px solid var(--border)",
    background: "var(--bg-surface)",
    fontSize: 13,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  icon: { color: "var(--warn)", flexShrink: 0 } satisfies CSSProperties,
  link: { color: "var(--accent-text)", fontWeight: 550, whiteSpace: "nowrap" } satisfies CSSProperties,
} as const;
