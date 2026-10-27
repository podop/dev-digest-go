import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for step 1 (the custom-provider link at the right of the provider legend). */
export const s = {
  customLink: { ...kit.headLink, marginLeft: 0 } satisfies CSSProperties,
} as const;
