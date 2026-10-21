import type { CSSProperties } from "react";

/** Co-located styles for the compare dialog. */
export const s = {
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 22, outline: "none" } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  h3: { margin: 0, fontSize: 14, fontWeight: 700 } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 13 } satisfies CSSProperties,
  th: { textAlign: "left", fontWeight: 600, padding: "6px 8px", color: "var(--text-muted)", borderBottom: "1px solid var(--border)" } satisfies CSSProperties,
  td: { padding: "6px 8px", borderBottom: "1px solid var(--border)" } satisfies CSSProperties,
  up: { color: "var(--ok)", fontWeight: 600 } satisfies CSSProperties,
  down: { color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  flat: { color: "var(--text-muted)" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)", fontSize: 13, margin: 0 } satisfies CSSProperties,
  flipped: { fontWeight: 700 } satisfies CSSProperties,
  diff: {
    margin: 0,
    maxHeight: 260,
    overflow: "auto",
    border: "1px solid var(--border)",
    borderRadius: 7,
    fontSize: 12,
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  line: { display: "flex", gap: 8, padding: "1px 10px", whiteSpace: "pre-wrap", wordBreak: "break-word" } satisfies CSSProperties,
  add: { background: "color-mix(in srgb, var(--ok) 14%, transparent)" } satisfies CSSProperties,
  del: { background: "color-mix(in srgb, var(--crit) 14%, transparent)" } satisfies CSSProperties,
  marker: { width: 10, flexShrink: 0, color: "var(--text-muted)" } satisfies CSSProperties,
  list: { margin: 0, paddingLeft: 18, fontSize: 13 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end" } satisfies CSSProperties,
} as const;
