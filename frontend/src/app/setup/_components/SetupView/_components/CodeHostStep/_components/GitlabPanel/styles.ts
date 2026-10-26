import type { CSSProperties } from "react";

/** Co-located styles for the GitLab form of the code-host step. */
export const s = {
  field: { marginBottom: 20 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 10, marginBottom: 8 } satisfies CSSProperties,
  label: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  docs: {
    display: "inline-flex",
    alignItems: "center",
    gap: 4,
    fontSize: 12,
    color: "var(--accent-text)",
  } satisfies CSSProperties,
  hint: { fontSize: 12, color: "var(--text-muted)", marginTop: 8, lineHeight: 1.45 } satisfies CSSProperties,
  row: { display: "flex", gap: 10, alignItems: "center" } satisfies CSSProperties,
  grow: { flex: 1 } satisfies CSSProperties,
  toggle: {
    display: "inline-flex",
    background: "transparent",
    border: "none",
    padding: 0,
    color: "var(--text-muted)",
    cursor: "pointer",
  } satisfies CSSProperties,
  error: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    marginBottom: 12,
    fontSize: 13,
    fontWeight: 600,
    color: "var(--crit)",
  } satisfies CSSProperties,
  who: { fontSize: 13, color: "var(--text-secondary)", lineHeight: 1.6 } satisfies CSSProperties,
  login: { fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  warn: {
    display: "flex",
    alignItems: "flex-start",
    gap: 8,
    marginTop: 8,
    fontSize: 13,
    lineHeight: 1.45,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  warnIcon: { color: "var(--warn)", flexShrink: 0, marginTop: 2 } satisfies CSSProperties,
} as const;
