import type { CSSProperties } from "react";

/** Co-located styles for NewFolderDialog. */
export const s = {
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  label: { fontSize: 13, fontWeight: 500 } satisfies CSSProperties,
  hint: { margin: 0, fontSize: 12, color: "var(--text-muted)", wordBreak: "break-all" } satisfies CSSProperties,
  error: { margin: 0, fontSize: 12, color: "var(--crit)" } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 8 } satisfies CSSProperties,
} as const;
