import type { CSSProperties } from "react";

/** Co-located styles for StaleBanner. */
export const s = {
  banner: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    flexWrap: "wrap",
    padding: "10px 14px",
    marginBottom: 12,
    border: "1px solid var(--warn)",
    borderRadius: 8,
    background: "var(--warn-bg)",
    fontSize: 13,
  } satisfies CSSProperties,
  text: { margin: 0, flex: 1, minWidth: 220 } satisfies CSSProperties,
  actions: { display: "flex", gap: 8 } satisfies CSSProperties,
} as const;
