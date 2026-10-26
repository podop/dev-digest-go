import type { CSSProperties } from "react";

/** Co-located styles for ProviderName. */
export const s = {
  /** Clips a long provider name with an ellipsis; the full name is the native title. */
  name: {
    display: "inline-block",
    verticalAlign: "bottom",
    maxWidth: 200,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
} as const;
