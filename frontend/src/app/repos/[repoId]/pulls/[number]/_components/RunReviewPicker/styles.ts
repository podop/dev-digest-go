import type { CSSProperties } from "react";
import { POPOVER_WIDTH } from "./constants";

/** Co-located styles for RunReviewPicker. */
export const s = {
  root: { position: "relative", display: "inline-block" },
  popover: {
    position: "absolute",
    top: "calc(100% + 6px)",
    right: 0,
    width: POPOVER_WIDTH,
    background: "var(--bg-elevated)",
    border: "1px solid var(--border-strong)",
    borderRadius: 9,
    boxShadow: "var(--shadow-modal)",
    padding: 10,
    zIndex: 40,
    animation: "ddpop .12s ease",
    display: "flex",
    flexDirection: "column",
    gap: 8,
  },
  warning: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    padding: "6px 8px",
    borderRadius: 6,
    background: "var(--bg-hover)",
    color: "var(--text-secondary)",
    fontSize: 12.5,
  },
  toolbar: { display: "flex", justifyContent: "space-between", alignItems: "center" },
  list: { display: "flex", flexDirection: "column", gap: 2, maxHeight: 280, overflowY: "auto" },
  row: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 10, padding: "5px 4px" },
  rowLabel: { display: "flex", flexDirection: "column", gap: 1, minWidth: 0 },
  rowName: { color: "var(--text-primary)", fontWeight: 500 },
  rowHint: { fontSize: 12, color: "var(--text-muted)" },
  estimate: { fontSize: 12, color: "var(--text-muted)", flexShrink: 0 },
  empty: { fontSize: 13, color: "var(--text-muted)", padding: "6px 4px" },
  footer: { display: "flex", flexDirection: "column", gap: 6 },
} satisfies Record<string, CSSProperties>;
