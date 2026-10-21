import type { CSSProperties } from "react";

/** Co-located styles for the Agent Performance dashboard. */
export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 1180, margin: "0 auto", display: "flex", flexDirection: "column", gap: 22 } satisfies CSSProperties,
  header: { display: "flex", alignItems: "flex-start", gap: 14, flexWrap: "wrap" } satisfies CSSProperties,
  headerText: { flex: 1, minWidth: 260 } satisfies CSSProperties,
  h1: { fontSize: 24, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  subtitle: { fontSize: 14, color: "var(--text-secondary)", margin: "4px 0 0" } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  skeletonCards: { display: "flex", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  skeletonCard: { flex: "1 1 160px", minWidth: 150 } satisfies CSSProperties,
} as const;
