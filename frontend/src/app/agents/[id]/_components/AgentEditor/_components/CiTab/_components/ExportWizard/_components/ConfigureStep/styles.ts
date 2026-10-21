import type { CSSProperties } from "react";

/** Co-located styles of the Configure step. */
export const s = {
  body: { padding: 24, display: "flex", flexDirection: "column", gap: 20 } satisfies CSSProperties,
  notice: {
    margin: 0,
    padding: "10px 14px",
    borderRadius: 8,
    border: "1px solid var(--warn)",
    background: "var(--warn-bg)",
    fontSize: 13,
    color: "var(--text-primary)",
  } satisfies CSSProperties,
  group: { margin: 0, padding: 0, border: "none", display: "flex", flexDirection: "column", gap: 8, minWidth: 0 } satisfies CSSProperties,
  legend: { padding: 0, margin: "0 0 8px", fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  chips: { display: "flex", flexWrap: "wrap", gap: 8 } satisfies CSSProperties,
  chip: {
    padding: "5px 12px",
    borderRadius: 6,
    borderWidth: 1,
    borderStyle: "solid",
    borderColor: "var(--border)",
    background: "transparent",
    color: "var(--text-secondary)",
    fontSize: 12.5,
    cursor: "pointer",
  } satisfies CSSProperties,
  chipOn: { borderColor: "var(--accent)", background: "var(--accent-bg)", color: "var(--accent-text)" } satisfies CSSProperties,
  invalid: { margin: 0, fontSize: 12.5, color: "var(--crit)" } satisfies CSSProperties,
  secrets: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 6 } satisfies CSSProperties,
  secret: { display: "flex", alignItems: "baseline", gap: 12, fontSize: 13 } satisfies CSSProperties,
  secretKey: { fontWeight: 600 } satisfies CSSProperties,
  radio: { display: "flex", alignItems: "center", gap: 10, fontSize: 14, color: "var(--text-secondary)" } satisfies CSSProperties,
  muted: { margin: 0, fontSize: 13, color: "var(--text-muted)", lineHeight: 1.5 } satisfies CSSProperties,
} as const;
