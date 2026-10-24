import type { CSSProperties } from "react";

/** Co-located styles for KeyField. */
export const s = {
  wrap: { marginBottom: 20 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 10, marginBottom: 8 } satisfies CSSProperties,
  label: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  optional: {
    fontSize: 11,
    fontWeight: 600,
    color: "var(--text-muted)",
    border: "1px solid var(--border-strong)",
    borderRadius: 4,
    padding: "1px 6px",
  } satisfies CSSProperties,
  docs: {
    display: "inline-flex",
    alignItems: "center",
    gap: 4,
    fontSize: 12,
    color: "var(--accent-text)",
  } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
  row: { display: "flex", gap: 10, alignItems: "center" } satisfies CSSProperties,
  input: { flex: 1 } satisfies CSSProperties,
  toggle: {
    display: "inline-flex",
    background: "transparent",
    border: "none",
    padding: 0,
    color: "var(--text-muted)",
    cursor: "pointer",
  } satisfies CSSProperties,
  hint: { fontSize: 12, color: "var(--text-muted)", marginTop: 8, lineHeight: 1.45 } satisfies CSSProperties,
  banner: (tone: "ok" | "warn" | "crit"): CSSProperties => ({
    display: "inline-flex",
    alignItems: "center",
    gap: 8,
    marginTop: 10,
    fontSize: 13,
    fontWeight: 600,
    color: `var(--${tone})`,
  }),
  badge: (tone: "ok" | "muted" | "crit"): CSSProperties => ({
    display: "inline-flex",
    alignItems: "center",
    gap: 6,
    fontSize: 12,
    fontWeight: 600,
    color: tone === "muted" ? "var(--text-muted)" : `var(--${tone})`,
  }),
  badgeDot: (tone: "ok" | "muted" | "crit"): CSSProperties => ({
    width: 7,
    height: 7,
    borderRadius: "50%",
    background: tone === "muted" ? "var(--text-muted)" : `var(--${tone})`,
  }),
} as const;
