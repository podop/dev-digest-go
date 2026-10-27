import type { CSSProperties } from "react";

/** Co-located styles for DangerZone — the prototype's red-tinted danger box. */
export const s = {
  card: {
    padding: 16,
    borderRadius: 10,
    border: "1px solid rgba(239,68,68,0.3)",
    background: "var(--crit-bg)",
  } satisfies CSSProperties,
  title: { fontSize: 13.5, fontWeight: 700, margin: "0 0 4px" } satisfies CSSProperties,
  body: { fontSize: 12.5, color: "var(--text-secondary)", lineHeight: 1.5, margin: 0 } satisfies CSSProperties,
  note: { fontSize: 11.5, color: "var(--text-muted)", lineHeight: 1.45, margin: "4px 0 0" } satisfies CSSProperties,
  actions: { display: "flex", gap: 8, marginTop: 12, flexWrap: "wrap" } satisfies CSSProperties,
  reason: { fontSize: 11.5, color: "var(--text-muted)", marginTop: 8 } satisfies CSSProperties,
} as const;
