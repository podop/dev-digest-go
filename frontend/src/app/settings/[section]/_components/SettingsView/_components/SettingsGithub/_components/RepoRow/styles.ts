import type { CSSProperties } from "react";

/** Co-located styles for RepoRow. */
export const s = {
  row: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "12px 16px",
    borderBottom: "1px solid var(--border)",
  } satisfies CSSProperties,
  main: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  name: { fontSize: 14, fontWeight: 600 } satisfies CSSProperties,
  meta: { fontSize: 12, color: "var(--text-muted)", marginTop: 3, display: "flex", gap: 10 } satisfies CSSProperties,
  branch: { fontFamily: "var(--font-mono)" } satisfies CSSProperties,
} as const;
