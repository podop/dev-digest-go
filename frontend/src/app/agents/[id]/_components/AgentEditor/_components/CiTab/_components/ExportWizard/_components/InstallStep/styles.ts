import type { CSSProperties } from "react";

/** Co-located styles of the Install step. */
export const s = {
  body: { padding: 24, margin: 0, border: "none", display: "flex", flexDirection: "column", gap: 12, minWidth: 0 } satisfies CSSProperties,
  option: {
    display: "flex",
    alignItems: "flex-start",
    gap: 12,
    padding: 14,
    borderWidth: 1,
    borderStyle: "solid",
    borderColor: "var(--border)",
    borderRadius: 9,
    background: "var(--bg-elevated)",
    cursor: "pointer",
  } satisfies CSSProperties,
  optionOn: { borderColor: "var(--accent)", background: "var(--bg-hover)" } satisfies CSSProperties,
  optionText: { display: "flex", flexDirection: "column", gap: 4, minWidth: 0 } satisfies CSSProperties,
  optionTitle: { display: "flex", alignItems: "center", gap: 8, fontSize: 14, fontWeight: 600 } satisfies CSSProperties,
  muted: { margin: 0, fontSize: 13, color: "var(--text-muted)", lineHeight: 1.5 } satisfies CSSProperties,
  title: { margin: 0, fontSize: 16, fontWeight: 700 } satisfies CSSProperties,
  link: { fontSize: 14, fontWeight: 600, color: "var(--accent-text)", overflowWrap: "anywhere" } satisfies CSSProperties,
  label: { margin: "8px 0 0", fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  steps: { margin: 0, paddingLeft: 20, display: "flex", flexDirection: "column", gap: 6, fontSize: 13.5, lineHeight: 1.5 } satisfies CSSProperties,
  srOnly: { position: "absolute", width: 1, height: 1, overflow: "hidden", clip: "rect(0 0 0 0)", whiteSpace: "nowrap" } satisfies CSSProperties,
} as const;
