import type { CSSProperties } from "react";

/** Co-located styles for StoreSection. */
export const s = {
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  toolbar: { display: "flex", alignItems: "center", gap: 4, padding: "0 4px" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 2 } satisfies CSSProperties,
  muted: { fontSize: 12, color: "var(--text-muted)", padding: "0 4px", margin: 0 } satisfies CSSProperties,
  hiddenInput: { display: "none" } satisfies CSSProperties,
} as const;
