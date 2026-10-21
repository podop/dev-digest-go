import type { CSSProperties } from "react";

/** Co-located styles of the Export wizard shell and its shared bits. */
export const s = {
  contents: { display: "contents" } satisfies CSSProperties,
  stepsBar: { padding: "16px 24px 0" } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "space-between", alignItems: "center", gap: 12 } satisfies CSSProperties,
  footerRight: { display: "flex", gap: 8 } satisfies CSSProperties,
  body: { padding: 24 } satisfies CSSProperties,
  error: {
    margin: "0 0 12px",
    padding: "10px 14px",
    borderRadius: 8,
    border: "1px solid var(--crit)",
    background: "var(--crit-bg)",
    fontSize: 13,
    color: "var(--text-primary)",
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
  errorCode: { fontWeight: 700, marginRight: 6 } satisfies CSSProperties,
  errorRule: { display: "block", marginTop: 4, color: "var(--text-secondary)" } satisfies CSSProperties,
  muted: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
} as const;
