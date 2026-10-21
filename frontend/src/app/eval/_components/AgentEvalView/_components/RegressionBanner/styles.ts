import type { CSSProperties } from "react";

/** Co-located styles for the regression banner. */
export const s = {
  banner: {
    display: "flex",
    gap: 12,
    alignItems: "center",
    padding: "12px 16px",
    borderRadius: 9,
    border: "1px solid var(--warn)",
    background: "color-mix(in srgb, var(--warn) 10%, var(--bg-surface))",
    fontSize: 13,
  } satisfies CSSProperties,
  icon: { color: "var(--warn)", flexShrink: 0 } satisfies CSSProperties,
  text: { margin: 0, color: "var(--text-primary)" } satisfies CSSProperties,
  strong: { fontWeight: 700 } satisfies CSSProperties,
} as const;
