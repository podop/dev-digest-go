import type { CSSProperties } from "react";

/** Co-located styles for the inline setup flow (footer line, loading skeletons). */
export const s = {
  footer: { fontSize: 12, color: "var(--text-muted)", marginTop: 20 } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 14 } satisfies CSSProperties,
} as const;
