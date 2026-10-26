import type { CSSProperties } from "react";

/** Co-located styles for ManualModels. */
export const s = {
  wrap: { marginBottom: 20 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", marginBottom: 6 } satisfies CSSProperties,
  title: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  hint: { fontSize: 12, color: "var(--text-muted)", marginBottom: 10, lineHeight: 1.45 } satisfies CSSProperties,
  empty: { fontSize: 13, color: "var(--text-muted)", marginBottom: 10 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  row: { display: "flex", gap: 8, alignItems: "flex-start" } satisfies CSSProperties,
  cell: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  price: { width: 110, flexShrink: 0 } satisfies CSSProperties,
  error: { fontSize: 12, color: "var(--crit)", marginTop: 4 } satisfies CSSProperties,
  add: { marginTop: 10 } satisfies CSSProperties,
} as const;
