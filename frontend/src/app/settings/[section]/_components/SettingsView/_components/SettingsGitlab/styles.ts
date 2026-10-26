import type { CSSProperties } from "react";

/** Co-located styles for SettingsGitlab. */
export const s = {
  wrap: { maxWidth: 640 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 10, marginTop: 20, marginBottom: 12 } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
  empty: { fontSize: 13, color: "var(--text-muted)", padding: "12px 0" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
} as const;
