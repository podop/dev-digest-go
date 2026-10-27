import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for RepoRow — a row inside the prototype's integration card. */
export const s = {
  row: { ...kit.cardRow, listStyle: "none" } satisfies CSSProperties,
  icon: kit.rowIcon,
  repo: kit.rowName,
  target: { flexShrink: 0, display: "inline-flex", alignItems: "center", gap: 6 } satisfies CSSProperties,
  host: { fontSize: 11.5, color: "var(--text-muted)", maxWidth: 160, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  mr: { flexShrink: 0, fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  run: { ...kit.rowMeta, maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis" } satisfies CSSProperties,
  runLink: { color: "var(--accent-text)", textDecoration: "none" } satisfies CSSProperties,
  /** The prototype's MonoLink "Manage" as a real link. */
  manageLink: {
    flexShrink: 0,
    fontSize: 12,
    color: "var(--text-secondary)",
    textDecoration: "none",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  chevron: { marginLeft: 2, verticalAlign: "-1px" } satisfies CSSProperties,
} as const;
