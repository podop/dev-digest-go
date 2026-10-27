import type { CSSProperties } from "react";

/** Co-located styles for SpendCapField (prototype FormField sizes). */
export const s = {
  row: { display: "flex", gap: 8, alignItems: "center" } satisfies CSSProperties,
  input: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  summary: { marginTop: 7, fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  error: { marginTop: 7, fontSize: 12, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  unavailable: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    marginTop: 7,
    fontSize: 12,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  banner: {
    display: "flex",
    alignItems: "flex-start",
    gap: 10,
    padding: "10px 12px",
    borderRadius: 8,
    border: "1px solid rgba(245,158,11,0.3)",
    background: "var(--warn-bg)",
    color: "var(--text-secondary)",
    fontSize: 12.5,
    lineHeight: 1.45,
    marginTop: 10,
  } satisfies CSSProperties,
  bannerIcon: { color: "var(--warn)", flexShrink: 0, marginTop: 1 } satisfies CSSProperties,
} as const;
