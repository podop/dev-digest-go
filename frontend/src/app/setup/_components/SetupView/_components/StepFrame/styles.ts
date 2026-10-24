import type { CSSProperties } from "react";

/** Co-located styles for a setup step's title, body and action row. */
export const s = {
  title: { fontSize: 22, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  sub: {
    fontSize: 14,
    color: "var(--text-secondary)",
    margin: "8px 0 24px",
    lineHeight: 1.5,
  } satisfies CSSProperties,
  actions: { display: "flex", gap: 12, alignItems: "center", marginTop: 28 } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
} as const;
