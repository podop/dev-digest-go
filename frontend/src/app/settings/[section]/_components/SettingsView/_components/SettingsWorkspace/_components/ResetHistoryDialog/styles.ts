import type { CSSProperties } from "react";

/** Co-located styles for ResetHistoryDialog. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  body: {
    padding: "18px 24px",
    fontSize: 14,
    color: "var(--text-secondary)",
    lineHeight: 1.5,
    display: "flex",
    flexDirection: "column",
    gap: 14,
  } satisfies CSSProperties,
  heading: {
    fontSize: 12,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.04em",
    color: "var(--text-muted)",
    marginBottom: 4,
  } satisfies CSSProperties,
  list: { margin: 0, paddingLeft: 18 } satisfies CSSProperties,
  note: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  label: { display: "block", fontSize: 13, fontWeight: 600, marginBottom: 8 } satisfies CSSProperties,
  name: { color: "var(--text-primary)" } satisfies CSSProperties,
  reason: { marginTop: 8, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  error: { fontSize: 13, color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "flex-end", gap: 10 } satisfies CSSProperties,
} as const;
