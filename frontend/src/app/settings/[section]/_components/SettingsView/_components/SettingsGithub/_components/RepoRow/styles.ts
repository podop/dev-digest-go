import type { CSSProperties } from "react";

/** Co-located styles for RepoRow (the row itself is the kit's ListRow). */
export const s = {
  item: { listStyle: "none" } satisfies CSSProperties,
  hint: { fontSize: 11.5, color: "var(--text-muted)", marginTop: 4, paddingLeft: 12, lineHeight: 1.45 } satisfies CSSProperties,
} as const;
