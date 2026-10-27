import type { CSSProperties } from "react";

const rowBorder = (last: boolean) => (last ? "none" : "1px solid var(--border)");

/** Co-located styles for SettingsAbout — the prototype's SettingsAbout box (rows 11px 14px, 12.5px). */
export const s = {
  box: {
    border: "1px solid var(--border)",
    borderRadius: 10,
    background: "var(--bg-elevated)",
    overflow: "hidden",
  } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 12.5 } satisfies CSSProperties,
  rowHead: (last: boolean): CSSProperties => ({
    width: 130,
    padding: "11px 0 11px 14px",
    textAlign: "left",
    fontWeight: 400,
    color: "var(--text-secondary)",
    borderBottom: rowBorder(last),
    verticalAlign: "top",
  }),
  cell: (last: boolean): CSSProperties => ({
    padding: "11px 14px 11px 10px",
    fontWeight: 600,
    color: "var(--text-primary)",
    borderBottom: rowBorder(last),
    wordBreak: "break-all",
  }),
  skeletons: { display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  link: {
    display: "inline-flex",
    alignItems: "center",
    gap: 3,
    marginTop: 14,
    fontSize: 12.5,
    color: "var(--accent-text)",
  } satisfies CSSProperties,
} as const;
