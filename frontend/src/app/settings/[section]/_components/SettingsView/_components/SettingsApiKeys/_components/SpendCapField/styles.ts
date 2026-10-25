import type { CSSProperties } from "react";

/** Co-located styles for SpendCapField. */
export const s = {
  row: { display: "flex", gap: 10, alignItems: "center" } satisfies CSSProperties,
  input: { flex: 1 } satisfies CSSProperties,
  summary: { marginTop: 10, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  error: { marginTop: 10, fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  unavailable: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    marginTop: 10,
    fontSize: 13,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  banner: {
    display: "flex",
    alignItems: "flex-start",
    gap: 10,
    padding: "10px 14px",
    borderRadius: 8,
    border: "1px solid var(--warn)",
    background: "var(--warn-bg)",
    color: "var(--text-primary)",
    fontSize: 13,
    lineHeight: 1.45,
    marginTop: 12,
  } satisfies CSSProperties,
  bannerIcon: { color: "var(--warn)", flexShrink: 0, marginTop: 1 } satisfies CSSProperties,
} as const;
