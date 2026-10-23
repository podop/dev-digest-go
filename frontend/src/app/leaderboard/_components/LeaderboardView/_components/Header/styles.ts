import type { CSSProperties } from "react";

/** Co-located styles of the leaderboard header. Toolbar items are grid containers so the Select and the
 *  period buttons stretch to one height. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  top: { display: "flex", alignItems: "flex-start", gap: 14, flexWrap: "wrap" } satisfies CSSProperties,
  text: { flex: 1, minWidth: 260 } satisfies CSSProperties,
  h1: { fontSize: 24, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  windowLabel: { fontSize: 14, color: "var(--text-secondary)", margin: "4px 0 0", minHeight: 21 } satisfies CSSProperties,
  controls: { display: "flex", alignItems: "stretch", gap: 10, flexWrap: "wrap", justifyContent: "flex-end" } satisfies CSSProperties,
  select: { display: "grid", minWidth: 220 } satisfies CSSProperties,
  toggle: {
    display: "inline-flex",
    margin: 0,
    minInlineSize: 0,
    padding: 2,
    gap: 2,
    border: "1px solid var(--border)",
    borderRadius: 9,
    background: "var(--bg-elevated)",
  } satisfies CSSProperties,
  toggleButton: (pressed: boolean): CSSProperties => ({
    padding: "6px 12px",
    border: "none",
    borderRadius: 7,
    fontSize: 13,
    fontWeight: 600,
    cursor: "pointer",
    color: pressed ? "var(--text-primary)" : "var(--text-secondary)",
    background: pressed ? "var(--bg-hover)" : "transparent",
  }),
  sync: { display: "flex", flexWrap: "wrap", gap: "4px 16px", listStyle: "none", margin: 0, padding: 0, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  syncRepo: { color: "var(--text-secondary)", fontWeight: 600 } satisfies CSSProperties,
} as const;
