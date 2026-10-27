import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for ConnectionRow: the prototype's bordered list row, with the details indented under the name. */
export const s = {
  row: { ...kit.listRow, flexDirection: "column", alignItems: "stretch", gap: 5, listStyle: "none" } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 9, minWidth: 0 } satisfies CSSProperties,
  icon: kit.rowIcon,
  name: {
    fontSize: 12.5,
    fontWeight: 600,
    color: "var(--text-primary)",
    minWidth: 0,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  actions: { display: "flex", alignItems: "center", gap: 12, marginLeft: "auto", flexShrink: 0 } satisfies CSSProperties,
  /** Details line up with the name (13px icon + 9px gap). */
  body: { display: "flex", flexDirection: "column", gap: 4, paddingLeft: 22, minWidth: 0 } satisfies CSSProperties,
  url: { fontSize: 11.5, color: "var(--text-muted)", wordBreak: "break-all" } satisfies CSSProperties,
  identity: { fontSize: 11.5, color: "var(--text-secondary)" } satisfies CSSProperties,
  testing: { fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
