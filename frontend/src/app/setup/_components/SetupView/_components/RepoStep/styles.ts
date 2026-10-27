import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for step 3 (existing repositories list, prototype repository rows). */
export const s = {
  heading: { ...kit.label, margin: "0 0 7px" } satisfies CSSProperties,
  list: { ...kit.list, listStyle: "none", margin: "0 0 18px", padding: 0 } satisfies CSSProperties,
  row: kit.listRow,
  rowIcon: kit.rowIcon,
  rowName: kit.rowName,
} as const;
