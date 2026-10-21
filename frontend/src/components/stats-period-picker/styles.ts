import type { CSSProperties } from "react";

/** Co-located styles of the period picker. Toolbar items are grid containers so the Select
 *  and the Apply button stretch to one height. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 10, alignItems: "flex-end" } satisfies CSSProperties,
  bar: { display: "flex", alignItems: "stretch", gap: 10, flexWrap: "wrap", justifyContent: "flex-end" } satisfies CSSProperties,
  select: { display: "grid", minWidth: 170 } satisfies CSSProperties,
  label: { alignSelf: "center", fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
  form: {
    display: "flex",
    alignItems: "flex-end",
    gap: 10,
    flexWrap: "wrap",
    justifyContent: "flex-end",
    padding: 12,
    border: "1px solid var(--border)",
    borderRadius: 9,
    background: "var(--bg-elevated)",
  } satisfies CSSProperties,
  field: { display: "flex", flexDirection: "column", gap: 4, fontSize: 12, fontWeight: 600, color: "var(--text-muted)" } satisfies CSSProperties,
  apply: { display: "grid" } satisfies CSSProperties,
  error: { flexBasis: "100%", margin: 0, fontSize: 12.5, color: "var(--crit)", textAlign: "right" } satisfies CSSProperties,
} as const;
