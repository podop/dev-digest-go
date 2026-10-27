import type { CSSProperties } from "react";

/** Numbers from Claude Design screen_multiagent.jsx (PersonaPickCard). */
export const s = {
  card: (on: boolean, color: string): CSSProperties => ({
    display: "flex",
    alignItems: "flex-start",
    gap: 12,
    padding: "12px 14px",
    borderRadius: 9,
    cursor: "pointer",
    textAlign: "left",
    width: "100%",
    font: "inherit",
    color: "var(--text-primary)",
    border: `1px solid ${on ? color : "var(--border)"}`,
    background: on ? `${color}12` : "var(--bg-elevated)",
    transition: "border-color .12s, background .12s",
  }),
  box: (on: boolean, color: string): CSSProperties => ({
    width: 18,
    height: 18,
    borderRadius: 5,
    flexShrink: 0,
    marginTop: 1,
    display: "grid",
    placeItems: "center",
    border: `1.5px solid ${on ? color : "var(--border-strong)"}`,
    background: on ? color : "transparent",
  }),
  check: { color: "#fff" } satisfies CSSProperties,
  main: { minWidth: 0, flex: 1, display: "block" } satisfies CSSProperties,
  name: { display: "block", fontSize: 13.5, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  desc: { display: "block", fontSize: 11.5, color: "var(--text-muted)", marginTop: 3, lineHeight: 1.45 } satisfies CSSProperties,
  estimate: { fontSize: 10.5, color: "var(--text-muted)", whiteSpace: "nowrap", flexShrink: 0 } satisfies CSSProperties,
} as const;
