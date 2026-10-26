import type { CSSProperties } from "react";

/** Co-located styles for PluginBadge. */
export const s = {
  wrap: { display: "inline-flex", alignItems: "center", gap: 6, maxWidth: "100%", minWidth: 0 } satisfies CSSProperties,
  /** Native-title carrier: the vendored Badge takes no `title` prop. */
  titled: { display: "inline-flex", minWidth: 0, maxWidth: "100%" } satisfies CSSProperties,
  badge: { maxWidth: "100%", minWidth: 0 } satisfies CSSProperties,
  /** The label text, clipped with an ellipsis when the plugin name is long. */
  text: {
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    maxWidth: 220,
  } satisfies CSSProperties,
} as const;
