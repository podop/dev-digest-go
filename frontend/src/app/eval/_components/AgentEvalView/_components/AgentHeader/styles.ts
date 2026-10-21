import type { CSSProperties } from "react";

/** Co-located styles of the agent view header. Toolbar items are grid containers so the
 *  picker, the period control and the primary button stretch to one height (NFR2). */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 14 } satisfies CSSProperties,
  back: { display: "inline-flex", alignItems: "center", gap: 4, fontSize: 13, color: "var(--text-secondary)", alignSelf: "flex-start" } satisfies CSSProperties,
  header: { display: "flex", alignItems: "flex-end", gap: 14, flexWrap: "wrap" } satisfies CSSProperties,
  titleBlock: { flex: 1, minWidth: 260, display: "flex", flexDirection: "column", gap: 4 } satisfies CSSProperties,
  titleRow: { display: "flex", alignItems: "center", gap: 10, minWidth: 0 } satisfies CSSProperties,
  h1: {
    fontSize: 24,
    fontWeight: 700,
    letterSpacing: "-0.02em",
    margin: 0,
    minWidth: 0,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  model: {
    flexShrink: 0,
    fontSize: 11.5,
    padding: "2px 8px",
    borderRadius: 5,
    border: "1px solid var(--border-strong)",
    background: "var(--bg-surface)",
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  meta: { display: "flex", alignItems: "center", gap: 14, flexWrap: "wrap" } satisfies CSSProperties,
  subtitle: { fontSize: 13, color: "var(--text-secondary)", margin: 0 } satisfies CSSProperties,
  link: { fontSize: 13, color: "var(--accent-text)" } satisfies CSSProperties,
  toolbar: { display: "flex", alignItems: "stretch", gap: 8, flexWrap: "wrap" } satisfies CSSProperties,
  picker: { display: "grid", minWidth: 200 } satisfies CSSProperties,
  period: { display: "grid", minWidth: 140 } satisfies CSSProperties,
  run: { display: "grid" } satisfies CSSProperties,
} as const;
