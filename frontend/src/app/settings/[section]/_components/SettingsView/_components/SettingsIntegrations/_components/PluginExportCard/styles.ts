import type { CSSProperties } from "react";

/** Co-located styles for PluginExportCard. */
export const s = {
  card: {
    border: "1px solid var(--border)",
    borderRadius: 10,
    padding: "16px 18px",
    background: "var(--bg-elevated)",
  } satisfies CSSProperties,
  title: { fontSize: 14, fontWeight: 700, color: "var(--text-primary)", margin: 0 } satisfies CSSProperties,
  body: { fontSize: 13, color: "var(--text-secondary)", lineHeight: 1.5, margin: "8px 0 0" } satisfies CSSProperties,
  actions: { display: "flex", gap: 10, marginTop: 14, flexWrap: "wrap" } satisfies CSSProperties,
  hint: { fontSize: 12.5, color: "var(--text-muted)", marginTop: 8 } satisfies CSSProperties,
  last: {
    display: "flex",
    alignItems: "baseline",
    gap: 4,
    minWidth: 0,
    margin: "12px 0 0",
    fontSize: 12.5,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  file: {
    minWidth: 0,
    maxWidth: 320,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    color: "var(--text-primary)",
  } satisfies CSSProperties,
} as const;
