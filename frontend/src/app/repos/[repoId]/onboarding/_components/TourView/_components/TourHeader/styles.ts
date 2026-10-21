import type { CSSProperties } from "react";

/** Co-located styles for TourHeader. */
export const s = {
  header: { display: "flex", alignItems: "flex-start", gap: 16, flexWrap: "wrap" } satisfies CSSProperties,
  text: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  h1: { margin: 0, fontSize: 24, fontWeight: 700, letterSpacing: "-0.02em", overflowWrap: "anywhere" } satisfies CSSProperties,
  repo: { color: "var(--accent-text)" } satisfies CSSProperties,
  meta: { margin: "6px 0 0", fontSize: 13.5, color: "var(--text-muted)" } satisfies CSSProperties,
  actions: { display: "flex", gap: 10, flexShrink: 0 } satisfies CSSProperties,
} as const;
