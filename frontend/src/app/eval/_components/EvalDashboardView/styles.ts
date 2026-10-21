import type { CSSProperties } from "react";

/** Co-located styles for the Eval Dashboard (all-agents screen). */
export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 1180, margin: "0 auto" } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 12, marginBottom: 28 } satisfies CSSProperties,
  header: { display: "flex", alignItems: "center", gap: 14, marginBottom: 22 } satisfies CSSProperties,
  headerText: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  h1: { fontSize: 24, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  h2: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    margin: 0,
    fontSize: 12,
    fontWeight: 600,
    letterSpacing: "0.08em",
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  subtitle: { fontSize: 14, color: "var(--text-secondary)", margin: "4px 0 0" } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  result: { fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
} as const;
