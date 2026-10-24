import type { CSSProperties } from "react";

/** Co-located styles for SettingsAbout. */
export const s = {
  wrap: { maxWidth: 640 } satisfies CSSProperties,
  table: {
    width: "100%",
    borderCollapse: "collapse",
    border: "1px solid var(--border)",
    borderRadius: 8,
    background: "var(--bg-surface)",
    fontSize: 14,
  } satisfies CSSProperties,
  rowHead: {
    width: 160,
    padding: "12px 16px",
    textAlign: "left",
    fontWeight: 500,
    color: "var(--text-muted)",
    borderBottom: "1px solid var(--border)",
    verticalAlign: "top",
  } satisfies CSSProperties,
  cell: {
    padding: "12px 16px",
    color: "var(--text-primary)",
    borderBottom: "1px solid var(--border)",
    wordBreak: "break-all",
  } satisfies CSSProperties,
  mono: { fontFamily: "var(--font-mono, monospace)", fontSize: 13 } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  link: { display: "inline-block", marginTop: 16, fontSize: 14, color: "var(--accent)" } satisfies CSSProperties,
} as const;
