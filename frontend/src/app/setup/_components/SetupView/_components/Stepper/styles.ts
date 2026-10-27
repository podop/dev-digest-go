import type { CSSProperties } from "react";

type State = "done" | "current" | "future";

/** Co-located styles for the setup stepper (prototype Stepper: 26px dots, 40px connectors). */
export const s = {
  list: {
    display: "flex",
    alignItems: "center",
    listStyle: "none",
    margin: "0 0 30px",
    padding: 0,
  } satisfies CSSProperties,
  item: (state: State): CSSProperties => ({
    position: "relative",
    display: "flex",
    alignItems: "center",
    gap: 9,
    flexShrink: 0,
    whiteSpace: "nowrap",
    fontSize: 12.5,
    fontWeight: state === "current" ? 600 : 500,
    color: state === "future" ? "var(--text-muted)" : "var(--text-primary)",
  }),
  dot: (state: State): CSSProperties => ({
    width: 26,
    height: 26,
    borderRadius: 99,
    display: "grid",
    placeItems: "center",
    fontSize: 12,
    fontWeight: 700,
    flexShrink: 0,
    color: state === "future" ? "var(--text-muted)" : "#fff",
    background: state === "done" ? "var(--ok)" : state === "current" ? "var(--accent)" : "var(--bg-elevated)",
    border: state === "future" ? "1px solid var(--border-strong)" : "none",
  }),
  /** 40px like the prototype, shrinking when the labels need the room. */
  line: {
    flex: "0 1 40px",
    minWidth: 12,
    height: 1,
    background: "var(--border-strong)",
    margin: "0 14px",
  } satisfies CSSProperties,
  srOnly: {
    position: "absolute",
    width: 1,
    height: 1,
    overflow: "hidden",
    clip: "rect(0 0 0 0)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
} as const;
