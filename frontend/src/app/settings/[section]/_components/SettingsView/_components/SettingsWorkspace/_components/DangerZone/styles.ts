import type { CSSProperties } from "react";

/** Co-located styles for DangerZone. */
export const s = {
  card: {
    border: "1px solid var(--crit)",
    borderRadius: 10,
    padding: "16px 18px",
    background: "var(--bg-elevated)",
    marginTop: 28,
  } satisfies CSSProperties,
  title: { fontSize: 14, fontWeight: 700, color: "var(--crit)", margin: 0 } satisfies CSSProperties,
  body: { fontSize: 13, color: "var(--text-secondary)", lineHeight: 1.5, margin: "8px 0 0" } satisfies CSSProperties,
  note: { fontSize: 12.5, color: "var(--text-muted)", lineHeight: 1.5, margin: "6px 0 0" } satisfies CSSProperties,
  actions: { display: "flex", gap: 10, marginTop: 14, flexWrap: "wrap" } satisfies CSSProperties,
  reason: { fontSize: 12.5, color: "var(--text-muted)", marginTop: 8 } satisfies CSSProperties,
} as const;
