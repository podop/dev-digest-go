import type { CSSProperties } from "react";

export const s = {
  heading: { margin: "0 0 8px", fontSize: 11, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.04em", color: "var(--text-muted)" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 6 } satisfies CSSProperties,
  item: { display: "flex", alignItems: "center", gap: 16, fontSize: 12.5 } satisfies CSSProperties,
  agent: { minWidth: 160, fontWeight: 600 } satisfies CSSProperties,
  score: { minWidth: 70, color: "var(--text-secondary)" } satisfies CSSProperties,
  empty: { margin: 0, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  link: { display: "inline-block", marginTop: 12, fontSize: 12.5, fontWeight: 600, color: "var(--accent-text)" } satisfies CSSProperties,
} as const;
