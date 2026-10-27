import type { CSSProperties } from "react";

/** Co-located styles for DataDirField (copy action inside the prototype's input box). */
export const s = {
  copied: { fontSize: 11.5, fontWeight: 600, color: "var(--ok)", whiteSpace: "nowrap" } satisfies CSSProperties,
  copy: {
    display: "inline-flex",
    alignItems: "center",
    gap: 4,
    background: "transparent",
    border: "none",
    padding: 0,
    fontSize: 11.5,
    fontWeight: 600,
    color: "var(--text-muted)",
    cursor: "pointer",
    flexShrink: 0,
  } satisfies CSSProperties,
} as const;
