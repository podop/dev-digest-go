import type { CSSProperties } from "react";

/* Full-width like the prototype: each part brings its own 28px side padding. */
export const s = {
  page: { paddingBottom: 40 } satisfies CSSProperties,
  state: { padding: "24px 28px" } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 12, padding: "24px 28px" } satisfies CSSProperties,
  back: { display: "inline-block", marginTop: 12, color: "var(--accent-text)", fontSize: 14 } satisfies CSSProperties,
  disagreement: { padding: "0 28px" } satisfies CSSProperties,
} as const;
