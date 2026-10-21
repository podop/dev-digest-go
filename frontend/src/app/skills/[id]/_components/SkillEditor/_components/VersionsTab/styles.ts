import type { CSSProperties } from "react";
import type { DiffLine } from "./helpers";

const DIFF_COLORS: Record<DiffLine["kind"], { bg: string; color: string }> = {
  add: { bg: "var(--ok-bg)", color: "var(--ok)" },
  del: { bg: "var(--crit-bg)", color: "var(--crit)" },
  same: { bg: "transparent", color: "var(--text-secondary)" },
};

/** Co-located styles for VersionsTab. */
export const s = {
  wrap: { maxWidth: 860 } satisfies CSSProperties,
  titleRow: { display: "flex", alignItems: "center", gap: 10 } satisfies CSSProperties,
  h2: { fontSize: 18, fontWeight: 700, margin: 0 } satisfies CSSProperties,
  count: {
    fontSize: 12,
    fontWeight: 600,
    color: "var(--text-secondary)",
    background: "var(--bg-hover)",
    borderRadius: 5,
    padding: "2px 8px",
  } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: "6px 0 16px" } satisfies CSSProperties,
  list: { listStyle: "none", padding: 0, margin: 0, display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  row: (current: boolean): CSSProperties => ({
    border: `1px solid ${current ? "var(--border-strong)" : "var(--border)"}`,
    borderRadius: 8,
    background: "var(--bg-elevated)",
    overflow: "hidden",
  }),
  rowHead: { display: "flex", alignItems: "center", gap: 14, padding: "14px 16px" } satisfies CSSProperties,
  /** The version tile: accent for the current version, muted for older ones. */
  version: (current: boolean): CSSProperties => ({
    flex: "none",
    minWidth: 36,
    textAlign: "center",
    fontSize: 13,
    fontWeight: 700,
    padding: "6px 8px",
    borderRadius: 6,
    color: current ? "var(--accent-text)" : "var(--text-muted)",
    background: current ? "var(--accent-bg)" : "var(--bg-hover)",
  }),
  text: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 3 } satisfies CSSProperties,
  message: {
    fontSize: 14,
    fontWeight: 600,
    color: "var(--text-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  date: { fontSize: 12, color: "var(--text-muted)", whiteSpace: "nowrap" } satisfies CSSProperties,
  actions: { display: "flex", alignItems: "center", gap: 8, flex: "none" } satisfies CSSProperties,
  /** "Diff": outlined and muted like the mockup; highlighted while its diff is open. */
  diffButton: (open: boolean): CSSProperties => ({
    border: "1px solid var(--border)",
    color: open ? "var(--text-primary)" : "var(--text-muted)",
    background: open ? "var(--bg-hover)" : "transparent",
  }),
  diff: {
    borderTop: "1px solid var(--border)",
    background: "var(--bg-surface)",
    padding: "8px 0",
    maxHeight: 360,
    overflow: "auto",
  } satisfies CSSProperties,
  diffLine: (kind: DiffLine["kind"]): CSSProperties => ({
    fontSize: 12.5,
    whiteSpace: "pre-wrap",
    padding: "0 14px",
    background: DIFF_COLORS[kind].bg,
    color: DIFF_COLORS[kind].color,
  }),
  sign: { display: "inline-block", width: 16, userSelect: "none" } satisfies CSSProperties,
} as const;
