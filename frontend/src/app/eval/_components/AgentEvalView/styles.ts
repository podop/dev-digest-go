import type { CSSProperties } from "react";

/** Co-located styles for the per-agent eval view. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 24 } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
} as const;
