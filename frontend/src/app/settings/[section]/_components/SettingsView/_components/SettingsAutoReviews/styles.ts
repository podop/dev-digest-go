import type { CSSProperties } from "react";

/** Co-located styles for SettingsAutoReviews. */
export const s = {
  wrap: { maxWidth: 640, display: "flex", flexDirection: "column", gap: 20 } satisfies CSSProperties,
  chipsSkeleton: { display: "flex", gap: 8, flexWrap: "wrap" } satisfies CSSProperties,
  info: {
    display: "flex",
    gap: 8,
    alignItems: "flex-start",
    fontSize: 13,
    lineHeight: 1.5,
    color: "var(--text-secondary)",
    padding: "10px 12px",
    border: "1px solid var(--border)",
    borderRadius: 8,
  } satisfies CSSProperties,
  link: { color: "var(--accent)", textDecoration: "underline" } satisfies CSSProperties,
} as const;
