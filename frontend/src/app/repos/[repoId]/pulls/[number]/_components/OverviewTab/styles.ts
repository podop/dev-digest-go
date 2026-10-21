import type { CSSProperties } from "react";

export const s = {
  stack: {
    display: "flex",
    flexDirection: "column",
    gap: 20,
  } satisfies CSSProperties,
  /* Intent + Blast radius side by side (mockup: 1fr 1fr, gap 16); each column needs
     ~420px, so below ~2 columns' worth of width the cards stack. */
  cards: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 420px), 1fr))",
    gap: 16,
    alignItems: "start",
  } satisfies CSSProperties,
  descriptionBox: {
    border: "1px solid var(--border)",
    borderRadius: 8,
    background: "var(--bg-elevated)",
    padding: 18,
    fontSize: 14,
    color: "var(--text-secondary)",
    whiteSpace: "pre-wrap",
    lineHeight: 1.55,
  } satisfies CSSProperties,
} as const;
