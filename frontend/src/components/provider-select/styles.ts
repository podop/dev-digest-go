import type { CSSProperties } from "react";

/** Co-located styles for ProviderSelect. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 6, minWidth: 0 } satisfies CSSProperties,
  error: {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 8,
    fontSize: 12.5,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
} as const;
