import type { CSSProperties } from "react";

/** Co-located styles for CustomProviderDialog and its parts. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 18 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 10 } satisfies CSSProperties,
  field: { display: "flex", flexDirection: "column", gap: 6, border: "none", margin: 0, padding: 0, minWidth: 0 } satisfies CSSProperties,
  label: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)", padding: 0 } satisfies CSSProperties,
  hint: { fontSize: 12, color: "var(--text-muted)", lineHeight: 1.45 } satisfies CSSProperties,
  error: { fontSize: 12, color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  radios: { display: "flex", flexDirection: "column", gap: 6 } satisfies CSSProperties,
  radio: { display: "flex", alignItems: "center", gap: 8, fontSize: 13, color: "var(--text-primary)" } satisfies CSSProperties,
  row: { display: "flex", gap: 8, alignItems: "flex-start" } satisfies CSSProperties,
  grow: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  toggle: {
    display: "inline-flex",
    background: "transparent",
    border: "none",
    padding: 0,
    color: "var(--text-muted)",
    cursor: "pointer",
  } satisfies CSSProperties,
  check: { display: "flex", alignItems: "center", gap: 8, fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
  note: { fontSize: 12, color: "var(--warn)", fontWeight: 600 } satisfies CSSProperties,
  banner: (tone: "ok" | "warn" | "crit"): CSSProperties => ({
    display: "inline-flex",
    alignItems: "center",
    gap: 8,
    fontSize: 13,
    fontWeight: 600,
    color: `var(--${tone})`,
  }),
  alert: { fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
} as const;
