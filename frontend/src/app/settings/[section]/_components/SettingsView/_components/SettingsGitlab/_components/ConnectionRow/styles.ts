import type { CSSProperties } from "react";

/** Co-located styles for ConnectionRow. */
export const s = {
  row: {
    padding: "12px 14px",
    border: "1px solid var(--border)",
    borderRadius: 8,
    background: "var(--bg-elevated)",
    display: "flex",
    flexDirection: "column",
    gap: 8,
  } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  name: { fontSize: 13, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
  url: { fontFamily: "var(--font-mono)", fontSize: 12, color: "var(--text-muted)", wordBreak: "break-all" } satisfies CSSProperties,
  meta: { display: "flex", gap: 12, flexWrap: "wrap", alignItems: "center", fontSize: 12, color: "var(--text-secondary)" } satisfies CSSProperties,
  actions: { display: "flex", gap: 8, marginLeft: "auto" } satisfies CSSProperties,
  badge: (tone: "ok" | "muted" | "crit"): CSSProperties => ({
    display: "inline-flex",
    alignItems: "center",
    gap: 6,
    fontSize: 12,
    fontWeight: 600,
    color: `var(--${tone === "muted" ? "text-muted" : tone})`,
  }),
  badgeDot: (tone: "ok" | "muted" | "crit"): CSSProperties => ({
    width: 7,
    height: 7,
    borderRadius: "50%",
    background: `var(--${tone === "muted" ? "text-muted" : tone})`,
  }),
  identity: { fontSize: 12, color: "var(--text-secondary)" } satisfies CSSProperties,
  testing: { fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
