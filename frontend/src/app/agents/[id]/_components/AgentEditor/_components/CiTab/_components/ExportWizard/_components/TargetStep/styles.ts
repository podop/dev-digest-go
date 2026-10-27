import type { CSSProperties } from "react";

/** Co-located styles of the Target step. */
export const s = {
  body: { padding: 24 } satisfies CSSProperties,
  cards: { margin: "0 0 24px", padding: 0, border: "none", display: "flex", flexWrap: "wrap", gap: 12 } satisfies CSSProperties,
  card: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
    width: "100%",
    maxWidth: 380,
    padding: 14,
    borderRadius: 9,
    border: "1px solid var(--accent)",
    background: "var(--bg-hover)",
    color: "var(--text-primary)",
    textAlign: "left",
    cursor: "default",
  } satisfies CSSProperties,
  cardOff: { opacity: 0.6, borderColor: "var(--border)" } satisfies CSSProperties,
  srOnly: { position: "absolute", width: 1, height: 1, overflow: "hidden", clip: "rect(0 0 0 0)", whiteSpace: "nowrap" } satisfies CSSProperties,
  cardHead: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 } satisfies CSSProperties,
  cardTitle: { display: "flex", alignItems: "center", gap: 8, fontSize: 14, fontWeight: 600 } satisfies CSSProperties,
  cardDesc: { fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  empty: { margin: 0, fontSize: 14, color: "var(--text-secondary)" } satisfies CSSProperties,
} as const;
