import type { CSSProperties } from "react";

/** Co-located styles for the setup stepper. */
export const s = {
  list: {
    display: "flex",
    alignItems: "center",
    gap: 0,
    listStyle: "none",
    margin: "0 0 28px",
    padding: 0,
  } satisfies CSSProperties,
  item: (state: "done" | "current" | "future", grows: boolean): CSSProperties => ({
    flex: grows ? 1 : undefined,
    position: "relative",
    display: "flex",
    alignItems: "center",
    gap: 8,
    fontSize: 13,
    fontWeight: state === "current" ? 600 : 500,
    color: state === "future" ? "var(--text-muted)" : "var(--text-primary)",
  }),
  dot: (state: "done" | "current" | "future"): CSSProperties => ({
    width: 22,
    height: 22,
    borderRadius: "50%",
    display: "grid",
    placeItems: "center",
    fontSize: 12,
    fontWeight: 700,
    flexShrink: 0,
    color: state === "current" ? "var(--bg-primary)" : state === "done" ? "var(--ok)" : "var(--text-muted)",
    background: state === "current" ? "var(--accent)" : "transparent",
    border: `1px solid ${state === "current" ? "var(--accent)" : state === "done" ? "var(--ok)" : "var(--border-strong)"}`,
  }),
  line: { flex: 1, height: 1, background: "var(--border)", minWidth: 12, marginLeft: 6 } satisfies CSSProperties,
  srOnly: {
    position: "absolute",
    width: 1,
    height: 1,
    overflow: "hidden",
    clip: "rect(0 0 0 0)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
} as const;
