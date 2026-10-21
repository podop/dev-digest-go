import type { OnboardingComplexity } from "@devdigest/shared";

/** Badge colour per complexity (design tokens: ok / warn / crit). */
export const COMPLEXITY_COLOR: Record<OnboardingComplexity, string> = {
  low: "var(--ok)",
  medium: "var(--warn)",
  high: "var(--crit)",
};
