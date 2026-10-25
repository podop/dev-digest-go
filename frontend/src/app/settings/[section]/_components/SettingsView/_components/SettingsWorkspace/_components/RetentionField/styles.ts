import type { CSSProperties } from "react";

/** Co-located styles for RetentionField. */
export const s = {
  select: { width: 220, maxWidth: "100%" } satisfies CSSProperties,
  warn: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    marginTop: 10,
    fontSize: 13,
    fontWeight: 600,
    color: "var(--warn)",
  } satisfies CSSProperties,
  error: { marginTop: 10, fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
} as const;
