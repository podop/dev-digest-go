import type { CSSProperties } from "react";

/** Co-located styles for SettingsAutoReviews (fields keep the prototype's 18px rhythm). */
export const s = {
  loading: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  chipsSkeleton: { display: "flex", gap: 7, flexWrap: "wrap" } satisfies CSSProperties,
  /** The prototype's bold "Integrations" inside the info note, here a link. */
  link: { color: "var(--text-secondary)", fontWeight: 700 } satisfies CSSProperties,
} as const;
