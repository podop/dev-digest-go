import type { CSSProperties } from "react";

/** Co-located styles for ConnectionDialog. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 18 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 10 } satisfies CSSProperties,
  field: { display: "flex", flexDirection: "column", gap: 6, minWidth: 0 } satisfies CSSProperties,
  labelRow: { display: "flex", alignItems: "center", gap: 10 } satisfies CSSProperties,
  label: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)", padding: 0 } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
  link: { display: "inline-flex", alignItems: "center", gap: 4, fontSize: 12, color: "var(--accent)" } satisfies CSSProperties,
  hint: { fontSize: 12, color: "var(--text-muted)", lineHeight: 1.45 } satisfies CSSProperties,
  error: { fontSize: 12, color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  toggle: {
    display: "inline-flex",
    background: "transparent",
    border: "none",
    padding: 0,
    color: "var(--text-muted)",
    cursor: "pointer",
  } satisfies CSSProperties,
  check: { display: "flex", alignItems: "center", gap: 8, fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
  alert: { fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  testing: { fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
