import type { CSSProperties } from "react";

export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 1400, margin: "0 auto" } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  back: { display: "inline-block", marginTop: 12, color: "var(--accent-text)", fontSize: 14 } satisfies CSSProperties,
} as const;
