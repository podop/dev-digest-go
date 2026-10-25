import type { CSSProperties } from "react";

/** Co-located styles for RemoveRepoDialog. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  body: { padding: "18px 24px", fontSize: 14, color: "var(--text-secondary)", lineHeight: 1.5 } satisfies CSSProperties,
  error: { marginTop: 12, fontSize: 13, color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 10 } satisfies CSSProperties,
} as const;
