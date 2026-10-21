import type { CSSProperties } from "react";

/** Co-located styles for the agent Evals tab. */
export const s = {
  wrap: { maxWidth: 920, display: "flex", flexDirection: "column", gap: 28 } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  header: { display: "flex", alignItems: "center", gap: 12 } satisfies CSSProperties,
  titleBlock: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  h2: { margin: 0, fontSize: 18, fontWeight: 700 } satisfies CSSProperties,
  subtitle: { margin: "2px 0 0", fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  link: { fontSize: 13, color: "var(--accent-text)" } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
} as const;
