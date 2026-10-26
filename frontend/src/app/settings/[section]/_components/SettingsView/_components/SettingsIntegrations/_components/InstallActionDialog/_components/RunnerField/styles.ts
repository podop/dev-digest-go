import type { CSSProperties } from "react";

/** Co-located styles for RunnerField. */
export const s = {
  field: { display: "flex", flexDirection: "column", gap: 6, border: "none", padding: 0, margin: 0, minWidth: 0 } satisfies CSSProperties,
  legend: { fontSize: 12.5, fontWeight: 600, color: "var(--text-secondary)", padding: 0 } satisfies CSSProperties,
  options: { display: "flex", flexWrap: "wrap", gap: 18 } satisfies CSSProperties,
  option: (disabled: boolean): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    gap: 8,
    fontSize: 13.5,
    color: disabled ? "var(--text-muted)" : "var(--text-primary)",
    cursor: disabled ? "not-allowed" : "pointer",
  }),
} as const;
