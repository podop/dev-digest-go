import type { CSSProperties } from "react";

/** Co-located styles for PluginImportCard. */
export const s = {
  card: {
    border: "1px solid var(--border-strong)",
    borderRadius: 10,
    padding: "16px 18px",
    background: "var(--bg-elevated)",
    marginTop: 20,
  } satisfies CSSProperties,
  title: { fontSize: 14, fontWeight: 700, color: "var(--text-primary)", margin: 0 } satisfies CSSProperties,
  body: { fontSize: 13, color: "var(--text-secondary)", lineHeight: 1.5, margin: "8px 0 0" } satisfies CSSProperties,
  installed: {
    marginTop: 14,
    padding: "12px 14px",
    border: "1px solid var(--border-strong)",
    borderRadius: 8,
    fontSize: 13,
    color: "var(--text-secondary)",
    lineHeight: 1.5,
  } satisfies CSSProperties,
  installedTitle: { fontWeight: 700, color: "var(--text-primary)", minWidth: 0 } satisfies CSSProperties,
  name: {
    display: "inline-block",
    maxWidth: "100%",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    verticalAlign: "bottom",
  } satisfies CSSProperties,
  hint: { marginTop: 6, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  actions: { display: "flex", justifyContent: "flex-end", marginTop: 12 } satisfies CSSProperties,
} as const;
