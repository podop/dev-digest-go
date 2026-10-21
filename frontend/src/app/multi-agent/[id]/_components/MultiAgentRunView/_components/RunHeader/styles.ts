import type { CSSProperties } from "react";

export const s = {
  header: { display: "flex", flexDirection: "column", gap: 10, marginBottom: 22 } satisfies CSSProperties,
  titleRow: { display: "flex", alignItems: "center", gap: 14, flexWrap: "wrap" } satisfies CSSProperties,
  h1: { flex: 1, minWidth: 0, margin: 0, fontSize: 22, fontWeight: 700, letterSpacing: "-0.02em", overflowWrap: "anywhere" } satisfies CSSProperties,
  prLink: { color: "inherit", textDecoration: "underline", textDecorationColor: "var(--border-strong)" } satisfies CSSProperties,
  metaRow: { display: "flex", alignItems: "center", gap: 14, flexWrap: "wrap", fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
  configure: { fontSize: 13, color: "var(--accent-text)" } satisfies CSSProperties,
  toggle: { display: "inline-flex", gap: 4 } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
} as const;
