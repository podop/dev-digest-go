import type { CSSProperties } from "react";

/** Co-located styles for DataDirField. */
export const s = {
  row: { display: "flex", gap: 10, alignItems: "center" } satisfies CSSProperties,
  input: { flex: 1 } satisfies CSSProperties,
  copied: { fontSize: 13, fontWeight: 600, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
