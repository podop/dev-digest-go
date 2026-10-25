import type { CSSProperties } from "react";

/** Co-located styles for TokenSection. */
export const s = {
  line: { fontSize: 13, color: "var(--text-secondary)", marginTop: -8, marginBottom: 16 } satisfies CSSProperties,
  warn: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    fontSize: 13,
    color: "var(--warn)",
    fontWeight: 600,
    marginBottom: 16,
  } satisfies CSSProperties,
  identity: { fontSize: 14, color: "var(--text-primary)" } satisfies CSSProperties,
} as const;
