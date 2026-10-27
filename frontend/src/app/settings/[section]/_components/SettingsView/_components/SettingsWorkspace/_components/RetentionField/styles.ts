import type { CSSProperties } from "react";

/** Co-located styles for RetentionField (notes sized as the prototype's field hint). */
export const s = {
  warn: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    marginTop: 6,
    fontSize: 11.5,
    fontWeight: 600,
    color: "var(--warn)",
  } satisfies CSSProperties,
  error: { marginTop: 6, fontSize: 11.5, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
} as const;
