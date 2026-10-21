/** Chart colours of the Agent Performance screens (mockup N11 / Agent Editor · Stats). */

/** Categorical series colours: cost by agent / by model, in segment order. */
export const SERIES_COLORS = ["var(--accent)", "var(--warn)", "var(--ok)", "#8b5cf6", "var(--crit)", "#06b6d4", "var(--info)"] as const;

/** Accept rates at or above this read as healthy (green), below as a warning (mockup N11). */
export const GOOD_ACCEPT_RATE = 0.6;

export const seriesColor = (i: number): string => SERIES_COLORS[i % SERIES_COLORS.length]!;

/** Findings-by-category donut colours (mockup); an unknown category falls back to --info. */
export const CATEGORY_COLORS: Readonly<Record<string, string>> = {
  security: "var(--crit)",
  bug: "var(--warn)",
  perf: "#8b5cf6",
  style: "var(--accent)",
  test: "var(--ok)",
};

export const categoryColor = (category: string): string => CATEGORY_COLORS[category] ?? "var(--info)";
