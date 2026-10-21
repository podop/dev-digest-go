import type { CSSProperties } from "react";

/** Co-located styles for TourSections. */
export const s = {
  stack: { display: "flex", flexDirection: "column", gap: 16, minWidth: 0 } satisfies CSSProperties,
  skeleton: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
} as const;
