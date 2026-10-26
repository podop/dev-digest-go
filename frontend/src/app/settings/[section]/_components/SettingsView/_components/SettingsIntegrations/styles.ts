import type { CSSProperties } from "react";

/** Co-located styles for SettingsIntegrations. The import card carries its own top margin (20), so
    only the joints around it are spaced here: GitHub Action → export (below the first card) and
    import → installed list (above the list). */
export const s = {
  wrap: { maxWidth: 640 } satisfies CSSProperties,
  first: { marginBottom: 20 } satisfies CSSProperties,
  last: { marginTop: 20 } satisfies CSSProperties,
} as const;
