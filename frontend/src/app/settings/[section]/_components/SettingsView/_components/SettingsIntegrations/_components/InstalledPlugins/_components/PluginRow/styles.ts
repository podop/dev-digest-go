import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for PluginRow — a row inside the prototype's Plugin Import card. */
export const s = {
  row: { ...kit.cardRow, listStyle: "none" } satisfies CSSProperties,
  icon: kit.rowIcon,
  main: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  top: { display: "flex", alignItems: "baseline", gap: 8, minWidth: 0 } satisfies CSSProperties,
  name: {
    fontWeight: 600,
    minWidth: 0,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  version: { fontSize: 11.5, color: "var(--text-muted)", flexShrink: 0 } satisfies CSSProperties,
  meta: {
    fontSize: 11.5,
    color: "var(--text-muted)",
    marginTop: 2,
    display: "flex",
    flexWrap: "wrap",
    gap: "2px 10px",
  } satisfies CSSProperties,
} as const;
