import type { CSSProperties } from "react";

/** Co-located styles for PluginExportDialog. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  body: {
    padding: "18px 24px",
    fontSize: 14,
    color: "var(--text-secondary)",
    lineHeight: 1.5,
    display: "flex",
    flexDirection: "column",
    gap: 14,
  } satisfies CSSProperties,
  note: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  label: { display: "block", fontSize: 13, fontWeight: 600, marginBottom: 8 } satisfies CSSProperties,
  hint: { fontSize: 12.5, color: "var(--text-muted)", margin: "6px 0 0" } satisfies CSSProperties,
  invalid: { fontSize: 12.5, color: "var(--crit)", marginTop: 6 } satisfies CSSProperties,
  options: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  error: { fontSize: 13, color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 10 } satisfies CSSProperties,
} as const;
