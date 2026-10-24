import type { CSSProperties } from "react";

/** Co-located styles for ConnectedRepos. */
export const s = {
  head: { display: "flex", alignItems: "center", gap: 10, marginTop: 28, marginBottom: 12 } satisfies CSSProperties,
  title: { fontSize: 15, fontWeight: 700, flex: 1 } satisfies CSSProperties,
  list: {
    border: "1px solid var(--border)",
    borderRadius: 10,
    overflow: "hidden",
    listStyle: "none",
    margin: 0,
    padding: 0,
  } satisfies CSSProperties,
  muted: { fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  form: {
    border: "1px solid var(--border)",
    borderRadius: 10,
    padding: 16,
    marginBottom: 12,
  } satisfies CSSProperties,
  note: {
    display: "flex",
    gap: 8,
    alignItems: "flex-start",
    fontSize: 12,
    color: "var(--text-muted)",
    marginTop: 14,
    lineHeight: 1.45,
  } satisfies CSSProperties,
} as const;
