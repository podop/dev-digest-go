import type { CSSProperties } from "react";

/** Co-located styles for GithubActionCard (the prototype's IntegrationCard "GitHub Action"). */
export const s = {
  actions: { display: "flex", gap: 8, marginBottom: 6 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0 } satisfies CSSProperties,
  skeletonRow: { marginTop: 6 } satisfies CSSProperties,
  empty: { fontSize: 12.5, color: "var(--text-muted)", marginTop: 6 } satisfies CSSProperties,
} as const;
