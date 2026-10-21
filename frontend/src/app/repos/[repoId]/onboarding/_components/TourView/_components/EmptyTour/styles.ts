import type { CSSProperties } from "react";

/** Co-located styles for EmptyTour. */
export const s = {
  root: { display: "flex", flexDirection: "column", alignItems: "center", gap: 14, paddingBottom: 40 } satisfies CSSProperties,
  hint: { margin: 0, fontSize: 13, color: "var(--text-muted)", textAlign: "center" } satisfies CSSProperties,
} as const;
