import type { CSSProperties } from "react";

export const s = {
  footer: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    flexWrap: "wrap",
    marginTop: 12,
    paddingTop: 10,
    borderTop: "1px solid var(--border)",
    fontSize: 12,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  item: { whiteSpace: "nowrap" } satisfies CSSProperties,
} as const;
