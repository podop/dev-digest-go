import type { CSSProperties } from "react";

/** Co-located styles for a Feature Models row (prototype FormField sizes). */
export const s = {
  pickers: { display: "flex", gap: 8, alignItems: "flex-start" } satisfies CSSProperties,
  provider: { width: 170, flexShrink: 0 } satisfies CSSProperties,
  model: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  blocked: { fontSize: 12, fontWeight: 600, color: "var(--warn)", lineHeight: "34px" } satisfies CSSProperties,
  defaultTag: {
    marginLeft: 8,
    fontSize: 11.5,
    fontWeight: 500,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
} as const;
