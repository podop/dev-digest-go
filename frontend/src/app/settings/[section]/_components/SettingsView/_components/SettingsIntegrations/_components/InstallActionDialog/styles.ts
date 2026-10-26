import type { CSSProperties } from "react";

/** Co-located styles for InstallActionDialog. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 18 } satisfies CSSProperties,
  field: { display: "flex", flexDirection: "column", gap: 6, border: "none", padding: 0, margin: 0, minWidth: 0 } satisfies CSSProperties,
  label: { fontSize: 12.5, fontWeight: 600, color: "var(--text-secondary)", padding: 0 } satisfies CSSProperties,
  muted: { fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  link: { color: "var(--accent)", fontSize: 13, fontWeight: 500 } satisfies CSSProperties,
  agents: {
    listStyle: "none",
    margin: 0,
    padding: 0,
    border: "1px solid var(--border)",
    borderRadius: 10,
    overflow: "hidden",
  } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 10 } satisfies CSSProperties,
} as const;
