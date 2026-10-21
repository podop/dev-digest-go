import type { CSSProperties } from "react";

/** Co-located styles for the eval cases section. */
export const s = {
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  header: { display: "flex", alignItems: "center", gap: 12 } satisfies CSSProperties,
  title: { flex: 1, minWidth: 0, margin: 0, fontSize: 18, fontWeight: 700 } satisfies CSSProperties,
  passing: { marginLeft: 12, fontSize: 14, fontWeight: 500, color: "var(--text-secondary)" } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  list: { padding: 0, margin: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
} as const;
