import type { CSSProperties } from "react";

/** Co-located styles for DeleteFileModal. */
export const s = {
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 10, fontSize: 14 } satisfies CSSProperties,
  path: { wordBreak: "break-all" } satisfies CSSProperties,
  warn: { margin: 0, color: "var(--warn)" } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 8 } satisfies CSSProperties,
} as const;
