import type { CSSProperties } from "react";

/** Co-located styles for DocPreviewPane. */
export const s = {
  pane: { display: "flex", flexDirection: "column", minWidth: 0, flex: 1 } satisfies CSSProperties,
  header: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "18px 28px",
    borderBottom: "1px solid var(--border)",
    flexWrap: "wrap",
  } satisfies CSSProperties,
  title: { fontSize: 15, fontWeight: 600, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  dot: { width: 8, height: 8, borderRadius: "50%", background: "var(--warn)", flexShrink: 0 } satisfies CSSProperties,
  meta: { marginLeft: "auto", display: "flex", alignItems: "center", gap: 14, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  usedBy: { display: "inline-flex", alignItems: "center", gap: 6 } satisfies CSSProperties,
  body: { padding: "24px 28px 48px", maxWidth: 860, fontSize: 14 } satisfies CSSProperties,
  center: { padding: "24px 28px" } satisfies CSSProperties,
} as const;
