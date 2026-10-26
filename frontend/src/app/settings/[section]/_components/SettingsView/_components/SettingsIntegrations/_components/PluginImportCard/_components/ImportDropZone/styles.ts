import type { CSSProperties } from "react";

/** Co-located styles for ImportDropZone. */
export const s = {
  zone: {
    minWidth: 0,
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: 8,
    padding: "22px 16px",
    marginTop: 14,
    border: "1.5px dashed var(--border-strong)",
    borderRadius: 10,
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  over: { borderColor: "var(--accent)", background: "var(--bg-elevated)" } satisfies CSSProperties,
  prompt: { fontSize: 13.5, color: "var(--text-primary)", fontWeight: 600 } satisfies CSSProperties,
  or: { fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  checking: { fontSize: 13.5, color: "var(--text-secondary)", padding: "8px 0" } satisfies CSSProperties,
} as const;
