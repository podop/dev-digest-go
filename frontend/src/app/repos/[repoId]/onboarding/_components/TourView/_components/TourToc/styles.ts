import type { CSSProperties } from "react";

/** Co-located styles for TourToc. */
export const s = {
  nav: { position: "sticky", top: 24, alignSelf: "flex-start", width: 200, flexShrink: 0 } satisfies CSSProperties,
  label: { fontSize: 11, fontWeight: 600, letterSpacing: "0.06em", textTransform: "uppercase", color: "var(--text-muted)", marginBottom: 10 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column" } satisfies CSSProperties,
  item: {
    width: "100%",
    textAlign: "left",
    padding: "7px 12px",
    fontSize: 13.5,
    color: "var(--text-secondary)",
    borderLeftWidth: 2,
    borderLeftStyle: "solid",
    borderLeftColor: "var(--border)",
  } satisfies CSSProperties,
  itemActive: { color: "var(--text-primary)", fontWeight: 600, borderLeftColor: "var(--accent)" } satisfies CSSProperties,
} as const;
