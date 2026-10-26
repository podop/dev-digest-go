import type { CSSProperties } from "react";

/** Co-located styles for TestResult. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 6, marginTop: 4 } satisfies CSSProperties,
  facts: { listStyle: "none", margin: 0, padding: 0, display: "flex", gap: 14, flexWrap: "wrap", fontSize: 12, color: "var(--text-secondary)" } satisfies CSSProperties,
  ok: { display: "inline-flex", alignItems: "center", gap: 8, fontSize: 13, fontWeight: 600, color: "var(--ok)" } satisfies CSSProperties,
  crit: { display: "inline-flex", alignItems: "center", gap: 8, fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  warn: { display: "flex", alignItems: "center", gap: 8, fontSize: 12, fontWeight: 600, color: "var(--warn)" } satisfies CSSProperties,
} as const;
