import type { CSSProperties } from "react";

/** Co-located styles for ImportDropZone — the prototype's dashed drop zone (1.5px dashed, padding 20, centred). */
export const s = {
  zone: {
    minWidth: 0,
    margin: "0 0 12px",
    padding: 20,
    border: "1.5px dashed var(--border-strong)",
    borderRadius: 9,
    textAlign: "center",
  } satisfies CSSProperties,
  over: { borderColor: "var(--accent)", background: "var(--accent-bg)" } satisfies CSSProperties,
  // Preflight makes svg display:block, so centre it with auto margins.
  icon: { color: "var(--text-muted)", display: "block", margin: "0 auto" } satisfies CSSProperties,
  prompt: { fontSize: 12.5, color: "var(--text-secondary)", marginTop: 8 } satisfies CSSProperties,
  browse: {
    background: "none",
    border: "none",
    padding: 0,
    font: "inherit",
    color: "var(--accent-text)",
    cursor: "pointer",
  } satisfies CSSProperties,
  checking: { fontSize: 12.5, color: "var(--text-secondary)", padding: "8px 0" } satisfies CSSProperties,
} as const;
