import type { CSSProperties } from "react";

/** Co-located styles for GitLabRepos and its rows. */
export const s = {
  head: { display: "flex", alignItems: "center", gap: 10, marginTop: 28, marginBottom: 12 } satisfies CSSProperties,
  title: { fontSize: 15, fontWeight: 700, flex: 1, margin: 0 } satisfies CSSProperties,
  list: { border: "1px solid var(--border)", borderRadius: 10, overflow: "hidden", listStyle: "none", margin: 0, padding: 0 } satisfies CSSProperties,
  muted: { fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  form: { border: "1px solid var(--border)", borderRadius: 10, padding: 16, marginBottom: 12 } satisfies CSSProperties,
  row: { display: "flex", alignItems: "center", gap: 12, padding: "12px 16px", borderBottom: "1px solid var(--border)" } satisfies CSSProperties,
  main: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  name: { fontSize: 14, fontWeight: 600, wordBreak: "break-all" } satisfies CSSProperties,
  meta: { fontSize: 12, color: "var(--text-muted)", marginTop: 3, display: "flex", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  mono: { fontFamily: "var(--font-mono)" } satisfies CSSProperties,
  badge: { display: "inline-flex", alignItems: "center", gap: 6, fontWeight: 600, color: "var(--warn)" } satisfies CSSProperties,
  hint: { fontSize: 12, color: "var(--text-muted)", marginTop: 4, lineHeight: 1.45 } satisfies CSSProperties,
} as const;
