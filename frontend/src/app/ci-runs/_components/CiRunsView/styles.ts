import type { CSSProperties } from "react";

/** Co-located styles for the CI Runs page. */
export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 1180, margin: "0 auto" } satisfies CSSProperties,
  header: { display: "flex", alignItems: "center", gap: 14, marginBottom: 22 } satisfies CSSProperties,
  headerText: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  h1: { fontSize: 24, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  subtitle: { fontSize: 14, color: "var(--text-secondary)", margin: "4px 0 0" } satisfies CSSProperties,
  result: { fontSize: 13, color: "var(--text-secondary)" } satisfies CSSProperties,
  banner: {
    margin: "0 0 18px",
    padding: "10px 14px",
    borderRadius: 8,
    border: "1px solid var(--warn)",
    background: "var(--warn-bg)",
    fontSize: 13,
    color: "var(--text-primary)",
  } satisfies CSSProperties,
  bannerList: { margin: "6px 0 0", paddingLeft: 18 } satisfies CSSProperties,
  scroll: {
    overflowX: "auto",
    border: "1px solid var(--border)",
    borderRadius: 9,
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 13 } satisfies CSSProperties,
  th: {
    padding: "10px 12px",
    textAlign: "left",
    fontSize: 11.5,
    fontWeight: 600,
    letterSpacing: "0.04em",
    color: "var(--text-muted)",
    borderBottom: "1px solid var(--border)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  td: { padding: "11px 12px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap" } satisfies CSSProperties,
  stamp: { color: "var(--text-secondary)", fontSize: 12.5 } satisfies CSSProperties,
  link: { color: "var(--accent-text)" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
