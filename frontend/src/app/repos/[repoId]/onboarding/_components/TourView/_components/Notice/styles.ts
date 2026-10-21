import type { CSSProperties } from "react";

/** Co-located styles for Notice. */
export const s = {
  box: { display: "flex", alignItems: "center", gap: 12, padding: "10px 14px", borderRadius: 8, borderWidth: 1, borderStyle: "solid" } satisfies CSSProperties,
  warn: { background: "var(--warn-bg)", borderColor: "var(--warn)" } satisfies CSSProperties,
  crit: { background: "var(--crit-bg)", borderColor: "var(--crit)" } satisfies CSSProperties,
  text: { flex: 1, minWidth: 0, fontSize: 13.5, color: "var(--text-primary)", overflowWrap: "anywhere" } satisfies CSSProperties,
} as const;
