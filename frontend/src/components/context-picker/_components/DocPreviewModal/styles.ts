import type { CSSProperties } from "react";

/** Co-located styles for DocPreviewModal. */
export const s = {
  body: { padding: "20px 24px 32px", fontSize: 14 } satisfies CSSProperties,
  meta: { display: "flex", alignItems: "center", gap: 10, fontSize: 12.5, color: "var(--text-muted)", marginBottom: 14 } satisfies CSSProperties,
} as const;
