import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for TokenSection (prototype: read-only identity box sized as SelectInput). */
export const s = {
  /** Scopes from the last test: a muted helper line tucked under the token field. */
  line: { fontSize: 11.5, color: "var(--text-muted)", marginTop: -10, marginBottom: 18, lineHeight: 1.45 } satisfies CSSProperties,
  identity: { ...kit.inputBox("elevated"), fontSize: 13, color: "var(--text-primary)" } satisfies CSSProperties,
} as const;
