import type { CSSProperties } from "react";

/** Co-located styles for SectionCard. */
export const s = {
  card: {
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 10,
    minWidth: 0,
    scrollMarginTop: 16,
  } satisfies CSSProperties,
  heading: { margin: 0, fontSize: 15 } satisfies CSSProperties,
  head: {
    width: "100%",
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "14px 18px",
    textAlign: "left",
    color: "var(--text-primary)",
  } satisfies CSSProperties,
  icon: {
    width: 28,
    height: 28,
    flexShrink: 0,
    display: "grid",
    placeItems: "center",
    borderRadius: 7,
    background: "var(--accent-bg)",
    color: "var(--accent-text)",
  } satisfies CSSProperties,
  title: { flex: 1, fontWeight: 600, fontSize: 15 } satisfies CSSProperties,
  chevron: { flexShrink: 0, color: "var(--text-muted)", transition: "transform .12s" } satisfies CSSProperties,
  chevronClosed: { transform: "rotate(-90deg)" } satisfies CSSProperties,
  body: { padding: "0 18px 18px", minWidth: 0 } satisfies CSSProperties,
} as const;
