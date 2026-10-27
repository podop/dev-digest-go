import type { CSSProperties } from "react";
import type { PopupPosition } from "./helpers";

/* Trigger mirrors SelectInput's look (vendor/ui/kit/SelectInput.tsx); the popup
   mirrors SearchableSelect's (vendor/ui/kit/SearchableSelect.tsx) so both themes
   stay in sync with the rest of the kit — see styles.css for the tokens. */
export const s = {
  trigger: (disabled: boolean | undefined, compact?: boolean): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    gap: compact ? 8 : 10,
    padding: compact ? "8px 11px" : "10px 12px",
    borderRadius: 7,
    border: "1px solid var(--border-strong)",
    background: "var(--bg-elevated)",
    cursor: disabled ? "default" : "pointer",
    opacity: disabled ? 0.6 : 1,
    outline: "none",
  }),
  value: (muted: boolean, compact?: boolean): CSSProperties => ({
    flex: 1,
    fontSize: compact ? 13 : 14,
    color: muted ? "var(--text-muted)" : "var(--text-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  }),
  icon: { color: "var(--text-muted)", flexShrink: 0 } as CSSProperties,
  chevron: { color: "var(--text-muted)", flexShrink: 0 } as CSSProperties,
  popup: (position: PopupPosition): CSSProperties => ({
    position: "fixed",
    left: position.left,
    // min, not fixed: the list may grow to fit its longest label (fixed + left ⇒ shrink-to-fit,
    // capped at the viewport, where optionLabel's ellipsis takes over).
    minWidth: position.width,
    top: position.top,
    bottom: position.bottom,
    maxHeight: position.maxHeight,
    background: "var(--bg-elevated)",
    border: "1px solid var(--border-strong)",
    borderRadius: 9,
    boxShadow: "var(--shadow-modal)",
    zIndex: 70,
    overflow: "auto",
    padding: 6,
    margin: 0,
    animation: "ddpop .12s ease",
  }),
  option: (active: boolean): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    gap: 8,
    padding: "8px 10px",
    borderRadius: 6,
    background: active ? "var(--bg-hover)" : "transparent",
    color: "var(--text-primary)",
    fontSize: 13,
    cursor: "pointer",
  }),
  check: (selected: boolean): CSSProperties => ({
    color: selected ? "var(--text-primary)" : "transparent",
    flexShrink: 0,
  }),
  optionLabel: {
    flex: 1,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } as CSSProperties,
};
