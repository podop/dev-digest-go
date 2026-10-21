import type { CSSProperties } from "react";

/** Co-located styles for ArchitectureCard. */
export const s = {
  root: { display: "flex", flexDirection: "column", gap: 14, minWidth: 0 } satisfies CSSProperties,
  empty: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  summary: {
    margin: 0,
    fontSize: 14,
    lineHeight: 1.6,
    color: "var(--text-secondary)",
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
  code: {
    fontSize: 12.5,
    padding: "1px 6px",
    borderRadius: 5,
    background: "var(--bg-hover)",
    color: "var(--accent-text)",
  } satisfies CSSProperties,
  /** Scrolls inside the card for very wide diagrams, never the page. */
  diagram: {
    overflowX: "auto",
    background: "var(--bg-primary)",
    border: "1px solid var(--border)",
    borderRadius: 8,
    padding: 4,
  } satisfies CSSProperties,
  svg: { display: "block", maxWidth: "100%", height: "auto", margin: "0 auto" } satisfies CSSProperties,
  arrow: { stroke: "var(--text-muted)", strokeWidth: 1.2 } satisfies CSSProperties,
  arrowHead: { fill: "var(--text-muted)" } satisfies CSSProperties,
  box: { fill: "var(--bg-elevated)", strokeWidth: 1.5 } satisfies CSSProperties,
  boxText: { fontSize: 12, fill: "var(--text-primary)", fontWeight: 600 } satisfies CSSProperties,
} as const;
