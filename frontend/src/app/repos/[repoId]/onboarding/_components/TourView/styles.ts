import type { CSSProperties } from "react";

/** Co-located styles for TourView. */
export const s = {
  page: { display: "flex", alignItems: "flex-start", justifyContent: "center", gap: 32, padding: "24px 32px 44px" } satisfies CSSProperties,
  main: { flex: 1, minWidth: 0, maxWidth: 920, display: "flex", flexDirection: "column", gap: 20 } satisfies CSSProperties,
  settingsLink: { color: "var(--accent-text)", fontSize: 13.5, fontWeight: 500, whiteSpace: "nowrap", textDecoration: "underline" } satisfies CSSProperties,
  noticeTitle: { fontWeight: 600, marginRight: 6 } satisfies CSSProperties,
} as const;
